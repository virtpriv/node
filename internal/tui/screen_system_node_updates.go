package tui

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/release"
	"github.com/virtpriv/node/internal/theme"
	"github.com/virtpriv/node/internal/update/protocol"
)

type nodeUpdates interface {
	Prepare(string) (protocol.Review, error)
	Start(string) (protocol.Status, error)
	Status() (protocol.Status, error)
	Resume(string) (protocol.Status, error)
	Cancel(string) (protocol.Status, error)
}

type NodeUpdateScreen struct {
	ctx          *ScreenContext
	target       string
	review       *protocol.Review
	status       protocol.Status
	err          error
	statusErr    error
	busy         string
	button       int
	attempt      uint64
	releaseInput *textinput.Model
	// notice explains why target is not the release the operator selected.
	notice string
}

type nodeUpdateMsg struct {
	owner   *NodeUpdateScreen
	attempt uint64
	kind    string
	review  protocol.Review
	status  protocol.Status
	err     error
}
type nodeUpdateTick struct{ owner *NodeUpdateScreen }

func NewNodeUpdateScreen(ctx *ScreenContext) *NodeUpdateScreen {
	s := &NodeUpdateScreen{ctx: ctx}
	s.selectStable()
	return s
}
func (s *NodeUpdateScreen) selectStable() {
	s.target, s.notice = "", ""
	if release.IsStable(s.ctx.LatestVersion) {
		s.target = s.ctx.LatestVersion
	}
}
func (s *NodeUpdateScreen) Init() tea.Cmd { return s.request("status") }
func (s *NodeUpdateScreen) request(kind string) tea.Cmd {
	if s.busy != "" {
		return nil
	}
	s.busy = kind
	if kind != "status" {
		s.err = nil
	}
	s.attempt++
	attempt := s.attempt
	client := s.ctx.NodeUpdates
	target := s.target
	digest := s.status.ID
	if s.review != nil {
		digest = s.review.Token
	}
	return func() tea.Msg {
		msg := nodeUpdateMsg{owner: s, attempt: attempt, kind: kind}
		if client == nil {
			msg.err = fmt.Errorf("update client is unavailable")
			return msg
		}
		switch kind {
		case "status":
			msg.status, msg.err = client.Status()
		case "prepare":
			msg.review, msg.err = client.Prepare(target)
		case "start":
			msg.status, msg.err = client.Start(digest)
		case "resume":
			msg.status, msg.err = client.Resume(digest)
		case "cancel":
			msg.status, msg.err = client.Cancel(digest)
		}
		return msg
	}
}
func (s *NodeUpdateScreen) poll() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return nodeUpdateTick{owner: s} })
}
func (s *NodeUpdateScreen) HandleMsg(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case tea.PasteMsg:
		if s.releaseInput != nil && s.releaseInput.Focused() {
			value := strings.TrimSpace(m.Content)
			if len(value) > s.releaseInput.CharLimit || validatePrintableASCII(value) != nil {
				s.err = fmt.Errorf("enter a release tag such as v0.7.0-rc.1")
				return s, nil
			}
			s.releaseInput.SetValue(value)
		}
	case nodeUpdateTick:
		if m.owner == s && s.review == nil && s.releaseInput == nil {
			return s, s.request("status")
		}
	case nodeUpdateMsg:
		if m.owner != s || m.attempt != s.attempt {
			return s, nil
		}
		s.busy = ""
		if m.kind == "status" {
			s.statusErr = m.err
		} else {
			s.err = m.err
			if m.err == nil {
				s.statusErr = nil
			}
		}
		if m.err == nil {
			if m.kind == "prepare" {
				if first := m.review.UpdateFirst; first != "" {
					// Guidance, not an approvable review: offer the release
					// this node has to install first.
					s.notice = fmt.Sprintf("v%s can be installed after v%s. Review v%s first.", s.target, first, first)
					s.target = first
					s.button = 0
					return s, s.poll()
				}
				s.notice = ""
				s.review = &m.review
				s.button = 0
				return s, nil
			}
			s.status = m.status
			if m.kind == "start" || m.kind == "resume" {
				s.review = nil
				s.button = 0
			}
		}
		if s.review == nil && s.releaseInput == nil {
			return s, s.poll()
		}
	}
	return s, nil
}
func (s *NodeUpdateScreen) hasTarget() bool {
	newer, err := release.Newer(s.ctx.Version, s.target)
	return err == nil && newer && !(s.status.Phase == "complete" && s.status.Version != s.ctx.Version) && (s.target != s.status.Version || s.status.Phase == "cancelled")
}
func (s *NodeUpdateScreen) buttons() []string {
	if s.busy != "" {
		return []string{"Close"}
	}
	if s.review != nil {
		return []string{"Cancel", "Install"}
	}
	if s.releaseInput != nil {
		return []string{"Cancel", "Review release"}
	}
	out := []string{"Close"}
	if s.status.Active {
		if s.status.Running && s.status.Phase == "waiting-unlock" {
			return append(out, "Unlock wallet")
		}
		if !s.status.Running {
			out = append(out, "Retry")
		}
		if s.status.Cancellable {
			out = append(out, "Cancel update")
		}
	}
	if (!s.status.Active || !s.status.Running) && s.hasTarget() {
		out = append(out, "Review update")
	}
	if (!s.status.Active || s.status.Phase == "failed") && !(s.status.Phase == "complete" && s.status.Version != s.ctx.Version) {
		out = append(out, "Select release")
	}
	return out
}
func (s *NodeUpdateScreen) HandleKey(k string, msg tea.KeyPressMsg) (Screen, tea.Cmd) {
	if s.releaseInput != nil {
		return s.handleReleaseKey(k, msg)
	}
	buttons := s.buttons()
	if s.button >= len(buttons) {
		s.button = len(buttons) - 1
	}
	switch k {
	case "ctrl+c":
		return s, tea.Quit
	case "backspace":
		return s, emitCloseTab
	case "left":
		if s.button > 0 {
			s.button--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right", "tab":
		if s.button+1 < len(buttons) {
			s.button++
		}
	case "up":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
	case "enter":
		switch buttons[s.button] {
		case "Close":
			return s, emitCloseTab
		case "Cancel":
			s.review = nil
			s.err = nil
			s.selectStable()
			s.button = 0
			return s, s.request("status")
		case "Review update":
			return s, s.request("prepare")
		case "Select release":
			input := textinput.New()
			input.Placeholder = "v0.7.0-rc.1"
			input.CharLimit = 64
			input.Validate = validatePrintableASCII
			applyInputStyles(&input)
			s.releaseInput = &input
			s.err = nil
			s.button = 0
			return s, input.Focus()
		case "Install":
			return s, s.request("start")
		case "Retry":
			return s, s.request("resume")
		case "Cancel update":
			return s, s.request("cancel")
		case "Unlock wallet":
			args, err := app.UpdateUnlockArguments(s.status)
			if err != nil {
				s.err = err
				return s, nil
			}
			s.busy = "unlock"
			s.err = nil
			s.attempt++
			attempt := s.attempt
			status := s.status
			return s, tea.ExecProcess(exec.Command("/usr/local/bin/lncli", args...), func(err error) tea.Msg {
				return nodeUpdateMsg{owner: s, attempt: attempt, kind: "unlock", status: status, err: err}
			})
		}
	}
	return s, nil
}

func (s *NodeUpdateScreen) handleReleaseKey(k string, msg tea.KeyPressMsg) (Screen, tea.Cmd) {
	cancel := func() (Screen, tea.Cmd) {
		s.releaseInput = nil
		s.err = nil
		s.button = 0
		return s, s.request("status")
	}
	switch k {
	case "ctrl+c":
		return s, tea.Quit
	case "esc":
		return cancel()
	case "tab", "down", "enter":
		if s.releaseInput.Focused() {
			s.releaseInput.Blur()
			s.button = 1
			return s, nil
		}
	case "up", "shift+tab":
		return s, s.releaseInput.Focus()
	}
	if s.releaseInput.Focused() {
		var cmd tea.Cmd
		*s.releaseInput, cmd = s.releaseInput.Update(msg)
		return s, cmd
	}
	switch k {
	case "backspace":
		return cancel()
	case "left":
		if s.button == 0 {
			return s, emitFocusSidebar
		}
		s.button = 0
	case "right", "tab":
		s.button = 1
	case "enter":
		if s.button == 0 {
			return cancel()
		}
		version := strings.TrimPrefix(strings.TrimSpace(s.releaseInput.Value()), "v")
		newer, err := release.Newer(s.ctx.Version, version)
		if err != nil || !newer {
			s.err = fmt.Errorf("select a newer release tag, such as v0.7.0-rc.1")
			return s, s.releaseInput.Focus()
		}
		s.target, s.notice = version, ""
		s.releaseInput = nil
		s.button = 0
		return s, s.request("prepare")
	}
	return s, nil
}

func (s *NodeUpdateScreen) View(w, h int) string {
	p := newPane(w)
	if s.releaseInput != nil {
		p.title(theme.Header, "Select a Test Release")
		p.line("Use only a disposable node without funds.")
		p.line("Enter the exact published VPN release tag.")
		p.blank()
		s.releaseInput.SetWidth(min(40, max(1, w-8)))
		p.line(s.releaseInput.View())
		if s.err != nil {
			p.blank()
			p.line(theme.Warning.Render(s.err.Error()))
		}
		p.blank()
		p.dim("This selects one update. Normal discovery stays stable.")
		return p.renderWithBottomButtons(s.buttons(), s.button, s.ctx.ContentFocused && !s.releaseInput.Focused(), h)
	}
	p.title(theme.Header, "Node Updates")
	if s.review != nil {
		r := s.review
		m := r.Manifest
		if release.IsCandidate(m.Version) {
			p.line(theme.Warning.Render("Release candidate: testing only, on a node without funds."))
		}
		p.field("VPN: ", r.Source.VPN+" → "+m.Version)
		for _, c := range protocol.Components {
			if from := r.Source.Get(c); from != "" {
				p.field(string(c)+": ", from+" → "+m.Artifact(c).Version)
			}
		}
		p.blank()
		p.line(m.Summary)
		for _, step := range m.HostSteps {
			p.field("Host change: ", step)
		}
		if r.KeyServerUnreachable {
			p.blank()
			p.line(theme.Warning.Render("Key server not reached. A new key revocation would be missed."))
		}
		p.blank()
		p.line("Affected services will restart. Downloads use Tor.")
		p.line("Databases are preserved. A failed update may need repair.")
	} else if s.status.ID != "" {
		p.field("Release: ", "v"+s.status.Version)
		p.field("Status: ", s.status.Step)
		if s.status.Active && !s.status.Running {
			p.line("The worker is stopped. Review the failure before Retry.")
		}
		if s.status.Error != "" {
			p.line(theme.Warning.Render(s.status.Error))
		}
	} else {
		p.line("No update job has been accepted.")
	}
	if s.hasTarget() && s.review == nil {
		p.blank()
		if s.notice != "" {
			p.line(s.notice)
		}
		p.field("Available: ", "v"+s.target)
	}
	if s.busy != "" {
		p.blank()
		if s.busy == "prepare" {
			p.line("Downloading and verifying the release plan...")
		} else {
			p.line("Checking update state...")
		}
	}
	if s.err != nil {
		p.blank()
		p.line(theme.Warning.Render(s.err.Error()))
	}
	if s.statusErr != nil {
		p.blank()
		p.line(theme.Warning.Render(s.statusErr.Error()))
	}
	p.blank()
	p.dim("You can close this screen and return to the saved update.")
	buttons := s.buttons()
	index := min(s.button, len(buttons)-1)
	return p.renderWithBottomButtons(buttons, index, s.ctx.ContentFocused, h)
}
func (s *NodeUpdateScreen) HelpBindings() []key.Binding {
	if s.releaseInput != nil {
		return []key.Binding{kTabButtons, kEnter, bind("esc", "cancel", "esc"), kQuit}
	}
	return actionButtonBindings(s.button, s.ctx.HasTabs)
}
