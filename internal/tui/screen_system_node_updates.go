package tui

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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
	// lead is the next step button focused when its state appeared.
	lead string
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
			if s.target == "" && s.notice == "" {
				// The release check may have answered after this screen opened.
				s.selectStable()
			}
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
					// Guidance, not an approvable review. Offer the release
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
			s.focusLead()
		}
		if s.review == nil && s.releaseInput == nil {
			return s, s.poll()
		}
	}
	return s, nil
}
func (s *NodeUpdateScreen) hasTarget() bool {
	newer, err := release.Newer(s.ctx.Version, s.target)
	return err == nil && newer && !s.olderThanUpdate() && (s.target != s.status.Version || s.status.Phase == "cancelled")
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
			if expectedStop(s.status) {
				out = append(out, "Continue")
			} else {
				out = append(out, "Retry")
			}
		}
		if s.status.Cancellable {
			out = append(out, "Cancel update")
		}
	}
	if (!s.status.Active || !s.status.Running) && s.hasTarget() {
		out = append(out, "Review")
	}
	if s.olderThanUpdate() {
		return append(out, "Relaunch")
	}
	if !s.status.Active || s.status.Phase == "failed" {
		out = append(out, "Select release")
	}
	return out
}

// focusLead moves the focus to the button the screen's message names when
// that state appears, not on every refresh, so it never moves under the
// operator. A state whose message names no button focuses the first one.
func (s *NodeUpdateScreen) focusLead() {
	lead := ""
	st := s.status
	switch {
	case s.olderThanUpdate():
		lead = "Relaunch"
	case !st.Active:
	case st.Running && st.Phase == "waiting-unlock":
		lead = "Unlock wallet"
	case st.Running:
	case expectedStop(st):
		lead = "Continue"
	case st.Phase == "failed" && knownFailure(st.Failure) != "":
		lead = "Cancel update"
	default:
		lead = "Retry"
	}
	if lead != s.lead {
		s.lead = lead
		s.button = max(slices.Index(s.buttons(), lead), 0)
	}
}

// olderThanUpdate reports a finished update of a newer version than this
// running program, so the operator has to start the new program.
func (s *NodeUpdateScreen) olderThanUpdate() bool {
	newer, err := release.Newer(s.ctx.Version, s.status.Version)
	return s.status.Phase == "complete" && err == nil && newer
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
	case "l":
		if s.hasLog() {
			return s, s.showLog()
		}
	case "enter":
		switch buttons[s.button] {
		case "Close":
			return s, emitCloseTab
		case "Relaunch":
			return s, emitRelaunch
		case "Cancel":
			s.review = nil
			s.err = nil
			s.selectStable()
			s.button = 0
			return s, s.request("status")
		case "Review":
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
		case "Retry", "Continue":
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
			unlock := &introducedCommand{introduction: walletUnlockIntroduction, cmd: exec.Command("/usr/local/bin/lncli", args...)}
			return s, tea.Exec(unlock, func(err error) tea.Msg {
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

// The most rows the saved failure and the two other errors get. The l key
// shows the whole failure.
const (
	nodeUpdateFailureRows = 3
	nodeUpdateActionRows  = 4
	nodeUpdateStatusRows  = 3
)

// nodeUpdateFit says how much of the messages under the state is drawn.
type nodeUpdateFit struct {
	actionRows, statusRows int
	// Lines left out, the least needed first.
	noBusy, noAvailable bool
}

// nodeUpdateFits lists the ways to draw the messages, from all of them in
// full to the least that is still useful, one row of the operator's own
// action error. The busy line and the Available block go
// first, then the errors shrink. The screen uses the first way that fits.
func nodeUpdateFits() []nodeUpdateFit {
	f := nodeUpdateFit{nodeUpdateActionRows, nodeUpdateStatusRows, false, false}
	fits := []nodeUpdateFit{f}
	for _, drop := range []*bool{&f.noBusy, &f.noAvailable} {
		*drop = true
		fits = append(fits, f)
	}
	for f.statusRows > 1 {
		f.statusRows--
		fits = append(fits, f)
	}
	for f.actionRows > 1 {
		f.actionRows--
		fits = append(fits, f)
	}
	f.statusRows = 0
	return append(fits, f)
}

// hasLog reports an update record whose log the l key can open.
func (s *NodeUpdateScreen) hasLog() bool {
	return s.status.ID != "" && s.review == nil && s.releaseInput == nil
}

// showLog opens the update's journal in the terminal, as the System screen
// does for services, and refreshes the status on return.
func (s *NodeUpdateScreen) showLog() tea.Cmd {
	c := exec.Command("bash", "-c",
		"clear && journalctl -u "+protocol.WorkerUnit+" -n 100 --no-pager"+
			" && echo && echo '  Press Enter to return...' && read && clear")
	return tea.ExecProcess(c, func(error) tea.Msg { return nodeUpdateTick{owner: s} })
}

// indented adds text below a step, lined up under the step's name.
func indented(p *paneBuilder, style lipgloss.Style, text string, rows int) {
	q := newPane(p.w - 2)
	q.fitText(style, text, rows)
	for _, row := range q.lines[1:] {
		p.lines = append(p.lines, "  "+row)
	}
}

// paragraphs adds a message whose parts are split by an empty line, keeping
// that line, so the sentence that names a button stands apart.
func paragraphs(p *paneBuilder, text string) {
	for i, part := range strings.Split(text, "\n\n") {
		if i > 0 {
			p.blank()
		}
		p.fitText(lipgloss.NewStyle(), part, 0)
	}
}

// expectedStop reports a failure the update meets in normal use and that
// Continue gets past: LND exits with an error when it is stopped while its
// wallet is locked or not created yet, and a start cut off by a restart is
// not repeated without the operator. Continue sends the same request as
// Retry. Only the screen's words and colors differ.
func expectedStop(st protocol.Status) bool {
	f := st.Failure
	if st.Phase != "failed" || f == nil {
		return false
	}
	return f.Code == protocol.FailureStartInterrupted ||
		(f.Code == protocol.FailureStopFailed && f.Service == protocol.LND)
}

// knownFailure gives the exact next step for a failure the worker named with
// a code. Anything else gets the general advice, which stays true for codes a
// later release adds.
func knownFailure(f *protocol.Failure) string {
	if f == nil {
		return ""
	}
	name, onSystemTab := systemTabName(f.Service)
	switch f.Code {
	case protocol.FailureStopFailed:
		if f.Service == protocol.LND {
			return "This happens when the LND wallet is locked or not created yet.\n\nSelect Continue."
		}
	case protocol.FailureStartInterrupted:
		return "The update was interrupted, for example by a restart.\n\nSelect Continue."
	case protocol.FailureServiceNotRunning:
		if onSystemTab {
			return fmt.Sprintf("%s is not running.\n\nSelect Cancel update, start %s on the System tab, then review again.", name, name)
		}
	case protocol.FailureServiceChanged:
		if onSystemTab {
			return fmt.Sprintf("%s is not running the installed version.\n\nSelect Cancel update, restart %s on the System tab, then review again.", name, name)
		}
	}
	return ""
}

// systemTabName is the name the System tab gives an updated service.
func systemTabName(c protocol.Component) (string, bool) {
	switch c {
	case protocol.Bitcoin:
		return "bitcoind", true
	case protocol.LND, protocol.Syncthing:
		return string(c), true
	}
	return "", false
}

// failure is the saved failure as the install screens show it.
func (s *NodeUpdateScreen) failure() string {
	return "Error: " + strings.TrimSpace(s.status.Error)
}

// drawSteps draws the worker's list of steps with the install screen's
// marks: a green check when done, an orange mark on the running step, a red
// cross and the error on a failure, and a faint dot for the steps ahead. A
// step the update stopped at for the operator, an expected stop or a worker
// that is not running, gets a gray mark. An expected stop shows no error,
// the log keeps it.
func (s *NodeUpdateScreen) drawSteps(p *paneBuilder) {
	st := s.status
	faint := lipgloss.NewStyle().Foreground(theme.ColorFaint)
	for i, step := range st.Steps {
		number := i + 1
		step = theme.PlainText(step)
		mark, name := faint.Render("·"), faint.Render(step)
		switch {
		case st.Phase == "complete" || number < st.StepNumber:
			mark, name = lipgloss.NewStyle().Foreground(theme.ColorSuccess).Render("✓"), theme.Label.Render(step)
		case number > st.StepNumber:
		case expectedStop(st):
			mark, name = theme.Dim.Render("›"), theme.Label.Render(step)
		case st.Phase == "failed":
			mark, name = theme.Warning.Render("✗"), theme.Warning.Render(step)
		case st.Running:
			mark, name = theme.Bitcoin.Render("›"), theme.Header.Render(step)
		default:
			mark, name = theme.Dim.Render("›"), theme.Label.Render(step)
		}
		p.line(ansi.Truncate(" "+mark+" "+name, max(p.w-1, 16), "…"))
		if number == st.StepNumber && st.Phase == "failed" && !expectedStop(st) {
			indented(p, theme.Warning, s.failure(), nodeUpdateFailureRows)
		}
	}
}

func (s *NodeUpdateScreen) View(w, h int) string {
	p := newPane(w)
	plain := lipgloss.NewStyle()
	if s.releaseInput != nil {
		p.title(theme.Header, "Select a Test Release")
		p.line(" Use only a disposable node without funds.")
		p.line(" Enter the exact published VPN release tag.")
		p.blank()
		s.releaseInput.SetWidth(min(40, max(1, w-8)))
		p.line(" " + s.releaseInput.View())
		if s.err != nil {
			p.blank()
			p.fitText(theme.Warning, s.err.Error(), nodeUpdateActionRows)
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
			p.fitText(theme.Warning, "Release candidate, for testing on a node without funds.", 0)
		}
		p.field("VPN: ", r.Source.VPN+" → "+m.Version)
		for _, c := range protocol.Components {
			if from := r.Source.Get(c); from != "" {
				p.field(string(c)+": ", from+" → "+m.Artifact(c).Version)
			}
		}
		p.blank()
		// The summary is what the operator approves, so it is never shortened.
		p.fitText(plain, m.Summary, 0)
		for _, step := range m.HostSteps {
			p.field("Host change: ", step)
		}
		if r.KeyServerUnreachable {
			p.blank()
			p.fitText(theme.Warning, "Key server not reached. A new key revocation would be missed.", 0)
		}
		p.blank()
		p.line(" Affected services will restart. Downloads use Tor.")
		p.line(" Databases are preserved. A failed update may need repair.")
	} else if s.status.ID != "" && !s.status.Active && !s.olderThanUpdate() {
		// A finished or cancelled update is history, shown in one line.
		outcome := "complete"
		if s.status.Phase == "cancelled" {
			outcome = "cancelled before host changes"
		}
		p.field("Last update: ", fmt.Sprintf("v%s, %s.", s.status.Version, outcome))
	} else if s.status.ID != "" {
		failed := s.status.Phase == "failed"
		p.field("Release: ", "v"+s.status.Version)
		if len(s.status.Steps) > 0 {
			p.blank()
			s.drawSteps(p)
		} else if s.status.Phase != "complete" {
			// A record without a list comes from an update that stopped before its
			// worker wrote one, or one started by an older release.
			p.field("Status: ", theme.PlainText(s.status.Step))
			if failed && !expectedStop(s.status) {
				p.fitText(theme.Warning, s.failure(), nodeUpdateFailureRows)
			}
		}
		if s.olderThanUpdate() {
			p.blank()
			paragraphs(p, "Update complete.\n\nSelect Relaunch to open the new version.")
		} else if s.status.Phase == "waiting-unlock" && s.status.Running && len(s.status.Steps) > 0 {
			p.blank()
			paragraphs(p, "Unlock the LND wallet to finish.\n\nSelect Unlock wallet.")
		} else if failed && !s.status.Running {
			// A known failure gets its exact next step. Otherwise only whether
			// the update had begun changing services, which is when Cancel is
			// refused. While the worker still finishes its cleanup neither
			// Retry nor Cancel is offered, so no advice is given yet.
			p.blank()
			if known := knownFailure(s.status.Failure); known != "" {
				paragraphs(p, known)
			} else if s.status.Cancellable {
				paragraphs(p, "No services were changed.\n\nSelect Retry, or Cancel update to leave the node as it was.")
			} else {
				paragraphs(p, "Services may have been changed.\n\nSelect Retry to try again.")
			}
		} else if s.status.Active && !s.status.Running {
			p.blank()
			p.line(" The update is not running. Retry continues it.")
		}
	} else if s.statusErr == nil && !s.hasTarget() {
		switch {
		case !s.ctx.LatestChecked:
			p.line(" Checking for updates...")
		case s.ctx.LatestVersion == "":
			p.line(" Could not check for updates.")
		default:
			p.line(" No update available.")
		}
	}
	buttons := s.buttons()
	index := min(s.button, len(buttons)-1)
	room := h - lipgloss.Height(renderButtons(buttons, index, s.ctx.ContentFocused, w)) - paneRows(p)
	var lower []string
	for _, fit := range nodeUpdateFits() {
		if lower = s.messages(w, fit); len(lower) <= room {
			break
		}
	}
	p.lines = append(p.lines, lower...)
	return p.renderWithBottomButtons(buttons, index, s.ctx.ContentFocused, h)
}

// paneRows counts the rows drawn so far. One entry can hold several.
func paneRows(p *paneBuilder) int {
	return len(strings.Split(strings.Join(p.lines, "\n"), "\n"))
}

// messages draws what goes under the state, as fit allows, and returns its
// rows.
func (s *NodeUpdateScreen) messages(w int, fit nodeUpdateFit) []string {
	p := newPane(w)
	plain := lipgloss.NewStyle()
	if s.hasTarget() && s.review == nil && !fit.noAvailable {
		p.blank()
		p.fitText(plain, s.notice, 0)
		p.field("Available: ", "v"+s.target)
	}
	if s.busy != "" && !fit.noBusy {
		p.blank()
		if s.busy == "prepare" {
			p.line(" Downloading and verifying the release plan...")
		} else {
			p.line(" Checking update state...")
		}
	}
	if s.err != nil {
		p.blank()
		p.fitText(theme.Warning, s.err.Error(), fit.actionRows)
	}
	if s.statusErr != nil && fit.statusRows > 0 {
		p.blank()
		p.fitText(theme.Warning, s.statusErr.Error(), fit.statusRows)
	}
	if len(p.lines) == 1 {
		return nil
	}
	return strings.Split(strings.Join(p.lines[1:], "\n"), "\n")
}
func (s *NodeUpdateScreen) HelpBindings() []key.Binding {
	if s.releaseInput != nil {
		return []key.Binding{kTabButtons, kEnter, bind("esc", "cancel", "esc"), kQuit}
	}
	bindings := actionButtonBindings(s.button, s.ctx.HasTabs)
	if s.hasLog() {
		bindings = append(bindings, bind("l", "logs", "l"))
	}
	return bindings
}
