package tui

import (
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/release"
	"github.com/virtpriv/node/internal/theme"
	"github.com/virtpriv/node/internal/tui/steplist"
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
	ctx       *ScreenContext
	target    string
	review    *protocol.Review
	status    protocol.Status
	err       error
	statusErr error
	busy      string
	button    int
	attempt   uint64
	// notice explains why target is not the release the operator selected.
	notice string
	// lead is the next step button focused when its state appeared.
	lead string
	spin steplist.Spinner
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

var errNodeUpdatesUnavailable = errors.New("update client is unavailable")

func NewNodeUpdateScreen(ctx *ScreenContext) *NodeUpdateScreen {
	s := &NodeUpdateScreen{ctx: ctx, spin: steplist.NewSpinner()}
	s.selectStable()
	return s
}

// newReviewedRelease opens Node Updates on the node's answer for a release
// named in Settings: its review, or the release to install first.
func newReviewedRelease(ctx *ScreenContext, version string, answer protocol.Review) *NodeUpdateScreen {
	s := &NodeUpdateScreen{ctx: ctx, spin: steplist.NewSpinner(), target: version}
	s.prepared(answer)
	return s
}

// prepared takes the node's answer for the target. A release to install
// first is guidance, not an approvable review: the screen offers that
// release instead.
func (s *NodeUpdateScreen) prepared(answer protocol.Review) {
	s.button = 0
	if first := answer.UpdateFirst; first != "" {
		s.notice = fmt.Sprintf("v%s can be installed after v%s. Review v%s first.", s.target, first, first)
		s.target = first
		return
	}
	s.notice = ""
	s.review = &answer
}

// nodeUpdatesBusy reports a Node Updates screen waiting for the node's
// answer, which a new screen in its tab would lose.
func nodeUpdatesBusy(screen Screen) bool {
	s, ok := screen.(*NodeUpdateScreen)
	return ok && s.busy != ""
}

func (s *NodeUpdateScreen) selectStable() {
	s.target, s.notice = "", ""
	if release.IsStable(s.ctx.LatestVersion) {
		s.target = s.ctx.LatestVersion
	}
}
func (s *NodeUpdateScreen) Init() tea.Cmd {
	if s.review != nil {
		return nil
	}
	return s.request("status")
}
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
			msg.err = errNodeUpdatesUnavailable
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
	case nodeUpdateTick:
		if m.owner == s && s.review == nil {
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
				s.prepared(m.review)
				if s.review == nil {
					return s, s.poll()
				}
				return s, nil
			}
			s.status = m.status
			if m.kind == "start" || m.kind == "resume" {
				s.review = nil
				s.button = 0
			}
			s.focusLead()
		}
		if s.review == nil {
			return s, s.poll()
		}
	}
	return s, nil
}
func (s *NodeUpdateScreen) hasTarget() bool {
	newer, err := release.Newer(s.ctx.Version, s.target)
	return err == nil && newer && !s.olderThanUpdate() && (s.target != s.status.Version || s.status.Phase == "canceled")
}
func (s *NodeUpdateScreen) buttons() []string {
	if s.busy != "" {
		return []string{"Close"}
	}
	if s.review != nil {
		return []string{"Cancel", "Install"}
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
	case st.Phase == "failed":
		next, _ := advice(st)
		lead = map[protocol.Next]string{protocol.NextContinue: "Continue", protocol.NextRetry: "Retry", protocol.NextCancel: "Cancel update"}[next]
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
func (s *NodeUpdateScreen) HandleKey(k string, _ tea.KeyPressMsg) (Screen, tea.Cmd) {
	buttons := s.buttons()
	if s.button >= len(buttons) {
		s.button = len(buttons) - 1
	}
	switch k {
	case "ctrl+c":
		return s, tea.Quit
	case "backspace":
		return s, emitFocusParent
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
	return s.status.ID != "" && s.review == nil
}

// showLog opens the update's journal in the terminal, as the System screen
// does for services, and refreshes the status on return.
func (s *NodeUpdateScreen) showLog() tea.Cmd {
	c := exec.Command("bash", "-c",
		"clear && journalctl -u "+protocol.WorkerUnit+" -n 100 --no-pager"+
			" && echo && echo '  Press Enter to return...' && read && clear")
	return tea.ExecProcess(c, func(error) tea.Msg { return nodeUpdateTick{owner: s} })
}

// nodeUpdateAdviceRows is the most rows a failure's advice takes, so advice
// from a later release cannot push the buttons off the screen.
const nodeUpdateAdviceRows = 6

// paragraphs adds a message whose parts are split by an empty line, keeping
// that line, so the sentence that names a button stands apart. With maxRows
// above zero, at most that many rows are added: the last part, the one that
// names the button, is kept first, and the parts before it get the rows
// left, the last one shown ending with "…" when it is cut.
func paragraphs(p *paneBuilder, text string, maxRows int) {
	plain := lipgloss.NewStyle()
	parts := strings.Split(text, "\n\n")
	if maxRows <= 0 {
		for i, part := range parts {
			if i > 0 {
				p.blank()
			}
			p.fitText(plain, part, 0)
		}
		return
	}
	rows := make([]int, len(parts))
	for i, part := range parts {
		q := newPane(p.w)
		q.fitText(plain, part, 0)
		rows[i] = len(q.lines) - 1
	}
	last := len(parts) - 1
	keep := min(rows[last], maxRows)
	left := maxRows - keep
	if last > 0 {
		left-- // the empty line before the last part
	}
	shown := false
	for i := 0; i < last && left > 0; i++ {
		if shown {
			if left < 2 {
				break
			}
			p.blank()
			left--
		}
		n := min(rows[i], left)
		p.fitText(plain, parts[i], n)
		left -= n
		shown = true
		if n < rows[i] {
			break
		}
	}
	if shown {
		p.blank()
	}
	p.fitText(plain, parts[last], keep)
}

// advice is a failure's next step and words. The worker's are used as a
// pair, and only when this release knows the next step and can offer its
// button. Otherwise this release's own pair is used, so the words never
// name another button than the one offered.
func advice(st protocol.Status) (protocol.Next, string) {
	switch {
	case st.Advice == "":
	case st.Next == protocol.NextContinue, st.Next == protocol.NextRetry,
		st.Next == protocol.NextCancel && st.Cancellable:
		return st.Next, st.Advice
	}
	return protocol.Advise(st.Failure, st.Cancellable)
}

// expectedStop reports a failure the update meets in normal use and that
// Continue gets past, such as LND that exits with an error when it is
// stopped while its wallet is locked or not created yet. Continue sends the
// same request as Retry. Only the screen's words and colors differ.
func expectedStop(st protocol.Status) bool {
	if st.Phase != "failed" {
		return false
	}
	next, _ := advice(st)
	return next == protocol.NextContinue
}

// failure is the saved failure as the install screens show it.
func (s *NodeUpdateScreen) failure() string {
	return "Error: " + strings.TrimSpace(s.status.Error)
}

// stepRows draws the worker's list on the shared step list: a green check
// when done, the spinner on the running step, a red cross and the error on a
// failure, and a faint dot for the steps ahead. A step the update stopped at
// for the operator, an expected stop or a worker that is not running, gets a
// gray mark. An expected stop shows no error, the log keeps it. from and to
// are the rows to keep in view.
func (s *NodeUpdateScreen) stepRows(w int) (rows []string, from, to int) {
	st := s.status
	at := func(times []time.Time, i int) time.Time {
		if i < len(times) {
			return times[i]
		}
		return time.Time{}
	}
	steps := make([]steplist.Step, len(st.Steps))
	for i, name := range st.Steps {
		number := i + 1
		step := steplist.Step{Name: name, Started: at(st.StepStarts, i), Ended: at(st.StepEnds, i)}
		switch {
		case st.Phase == "complete" || number < st.StepNumber:
			step.State = steplist.Done
		case number > st.StepNumber:
		case expectedStop(st):
			step.State = steplist.Stopped
		case st.Phase == "failed":
			step.State, step.Err = steplist.Failed, strings.TrimSpace(st.Error)
		case st.Running:
			step.State = steplist.Running
		default:
			step.State = steplist.Stopped
		}
		steps[i] = step
	}
	return steplist.Rows(steps, max(w-1, 16), s.spin.View(), time.Now(), nodeUpdateFailureRows)
}

// spinner and stepRuns let the model run the spinner while a step runs and
// the screen shows.
func (s *NodeUpdateScreen) spinner() *steplist.Spinner { return &s.spin }
func (s *NodeUpdateScreen) stepRuns() bool {
	return s.review == nil && s.status.Active && s.status.Running &&
		s.status.Phase != "failed" && len(s.status.Steps) > 0
}

func (s *NodeUpdateScreen) View(w, h int) string {
	p := newPane(w)
	plain := lipgloss.NewStyle()
	p.title(theme.Header, "Node Updates")
	// list is the step list, from and to its rows to keep in view, and after
	// what the screen shows below it.
	var list []string
	var from, to int
	after := newPane(w)
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
		// A finished or canceled update is history, shown in one line.
		outcome := "complete"
		if s.status.Phase == "canceled" {
			outcome = "canceled before host changes"
		}
		p.field("Last update: ", fmt.Sprintf("v%s, %s.", s.status.Version, outcome))
	} else if s.status.ID != "" {
		failed := s.status.Phase == "failed"
		p.field("Release: ", "v"+s.status.Version)
		if len(s.status.Steps) > 0 {
			p.blank()
			list, from, to = s.stepRows(p.w)
		} else if s.status.Phase != "complete" {
			// A record without a list comes from an update that stopped before its
			// worker wrote one, or one started by an older release.
			p.field("Status: ", theme.PlainText(s.status.Step))
			if failed && !expectedStop(s.status) {
				p.fitText(theme.Warning, s.failure(), nodeUpdateFailureRows)
			}
		}
		// What follows the list is drawn first, so the list gets only the
		// rows left and never pushes the sentence that names a button off.
		if s.olderThanUpdate() {
			after.blank()
			paragraphs(after, "Update complete.\n\nSelect Relaunch to open the new version.", 0)
		} else if s.status.Phase == "waiting-unlock" && s.status.Running && len(s.status.Steps) > 0 {
			after.blank()
			paragraphs(after, "Unlock the LND wallet to finish.\n\nSelect Unlock wallet.", 0)
		} else if failed && !s.status.Running {
			// While the worker still finishes its cleanup neither Retry nor
			// Cancel is offered, so no advice is given yet.
			after.blank()
			_, words := advice(s.status)
			paragraphs(after, words, nodeUpdateAdviceRows)
		} else if s.status.Active && !s.status.Running {
			after.blank()
			after.line(" The update is not running. Retry continues it.")
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
	below := after.lines[1:]
	// The list keeps at least its running or failed step's rows, or all of
	// a short list, when the messages under it are fitted.
	keep := min(len(list), to-from+2)
	room := h - lipgloss.Height(renderButtons(buttons, index, s.ctx.ContentFocused, w)) - paneRows(p) - len(below)
	var lower []string
	for _, fit := range nodeUpdateFits() {
		if lower = s.messages(w, fit); len(lower) <= room-keep {
			break
		}
	}
	if len(list) > 0 {
		p.lines = append(p.lines, steplist.Window(list, from, to, room-len(lower), s.status.Phase == "complete")...)
	}
	p.lines = append(p.lines, below...)
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
	bindings := actionButtonBindings(s.button, s.ctx.HasTabs)
	if s.hasLog() {
		bindings = append(bindings, bind("l", "logs", "l"))
	}
	return bindings
}
