package tui

import (
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/theme"
	"github.com/virtpriv/node/internal/tui/steplist"
)

// workflowProgressMsg identifies one watched workflow and one look at it.
type workflowProgressMsg struct {
	operation *app.Operation
	event     app.WorkflowProgress
}

type closeHelperProgressMsg struct{ screen *InstallProgressScreen }

// InstallProgressScreen draws one Syncthing install or P2P switch job from
// its record: its steps, when each began and ended, and its outcome. The job
// runs in its own service, so the owner may leave this screen while it runs
// and find it again here. Only the event loop changes what the screen holds
// and publishes configuration.
type InstallProgressScreen struct {
	ctx       *ScreenContext
	operation *app.Operation
	names     []string
	job       api.Job
	onDone    func() tea.Cmd
	onFail    func() tea.Cmd
	started   bool
	opened    time.Time
	done      bool
	result    app.WorkflowResult
	// local is the TUI's own last step, reading the node's configuration.
	local steplist.Step
	spin  steplist.Spinner
}

func NewInstallProgressScreen(ctx *ScreenContext, operation *app.Operation, onDone, onFail func() tea.Cmd) *InstallProgressScreen {
	return &InstallProgressScreen{ctx: ctx, operation: operation, names: operation.Steps(), onDone: onDone, onFail: onFail, spin: steplist.NewSpinner()}
}

func (s *InstallProgressScreen) Init() tea.Cmd {
	if s.started {
		return nil
	}
	s.started, s.opened = true, time.Now()
	return s.waitProgress()
}

func (s *InstallProgressScreen) waitProgress() tea.Cmd {
	operation := s.operation
	return func() tea.Msg {
		event, ok := <-operation.Events()
		if !ok {
			return nil
		}
		return workflowProgressMsg{operation: operation, event: event}
	}
}

func (s *InstallProgressScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		return s, emitFocusSidebar
	case "up", "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
	case "backspace":
		return s, emitFocusParent
	case "enter":
		if s.done {
			return s, func() tea.Msg { return closeHelperProgressMsg{screen: s} }
		}
	}
	return s, nil
}

func (s *InstallProgressScreen) HandleMsg(msg tea.Msg) (Screen, tea.Cmd) {
	m, ok := msg.(workflowProgressMsg)
	if !ok || m.operation != s.operation || !s.started || s.done {
		return s, nil
	}
	if m.event.Job.ID != "" {
		s.job = m.event.Job
	}
	result := m.event.Result
	if result == nil {
		return s, s.waitProgress()
	}
	s.done, s.result = true, *result
	now := time.Now()
	if result.JobDone {
		s.local.Start(s.job.Ended)
		s.local.End(now)
	}
	if result.Config != nil {
		publishWorkflowConfig(s.ctx, s.operation.Kind(), result.Config)
	}
	if result.Err != nil {
		logger.Install("%s: %v", s.job.Title, result.Err)
		if s.onFail != nil {
			return s, s.onFail()
		}
		return s, nil
	}
	if s.onDone != nil {
		return s, s.onDone()
	}
	return s, nil
}

// publishWorkflowConfig publishes only the setting the workflow owns. A
// delayed reload must not overwrite another workflow's newer setting, such
// as auto-unlock.
func publishWorkflowConfig(ctx *ScreenContext, kind app.WorkflowKind, cfg *config.AppConfig) {
	switch kind {
	case app.SyncthingInstall:
		ctx.Cfg.SyncthingEnabled = cfg.SyncthingEnabled
	case app.P2PUpgrade:
		ctx.Cfg.P2PMode = cfg.P2PMode
		ctx.Cfg.P2PAddress = cfg.P2PAddress
	}
}

func (s *InstallProgressScreen) failed() bool { return s.done && s.result.Err != nil }

// steps draws the job's record. Before the first look the first step runs
// from when the screen opened.
func (s *InstallProgressScreen) steps() []steplist.Step {
	steps := make([]steplist.Step, len(s.names))
	jobSteps := len(s.names) - 1
	j := s.job
	current := j.StepNumber - 1
	if j.ID == "" {
		current = 0
		if s.started && !s.done {
			steps[0].Start(s.opened)
		}
	}
	for i := range jobSteps {
		steps[i].Name = s.names[i]
		if i < len(j.StepStarts) {
			steps[i].Started = j.StepStarts[i]
		}
		if i < len(j.StepEnds) {
			steps[i].Ended = j.StepEnds[i]
		}
		switch {
		case j.State == api.JobDone || i < current:
			steps[i].State = steplist.Done
		case i == current && j.State == api.JobFailed:
			steps[i].State = steplist.Failed
			steps[i].Err = j.Error
		case i == current && j.Finished():
			// Unknown: the step is not marked failed, the outcome below
			// says it was not confirmed.
		case i == current && !s.done:
			steps[i].State = steplist.Running
		}
	}
	last := &steps[jobSteps]
	*last = s.local
	last.Name = s.names[jobSteps]
	switch {
	case s.done && s.result.JobDone && s.result.Err != nil:
		last.State = steplist.Failed
		last.Err = s.result.Err.Error()
	case s.done && s.result.JobDone:
		last.State = steplist.Done
	case j.State == api.JobDone:
		last.State = steplist.Running
	}
	return steps
}

// ── View ────────────────────────────────────────────────

func (s *InstallProgressScreen) View(
	w, h int,
) string {
	p := newPane(w)
	rows, from, to := steplist.Rows(s.steps(), max(p.w-1, 16), s.spin.View(), time.Now(), 0)
	// The outcome below the list and the button row stay in view.
	p.lines = append(p.lines, steplist.Window(rows, from, to, max(1, h-progressBelowRows), s.done && !s.failed())...)

	p.blank()

	switch {
	case s.done && !s.failed():
		p.line(" " + theme.Good.Render("Complete."))
	case s.done && s.result.JobDone:
		p.warnWrap("The change completed, but this screen could not read the node's new configuration.")
	case s.done && s.result.Outcome == app.JobNotStarted:
		p.fitText(lipgloss.NewStyle(), "The workflow stopped before changing anything.", 0)
	case s.done && s.result.Outcome == app.JobUnconfirmed:
		p.warnWrap("The change was not confirmed. It may have finished. Check the node before trying again.")
	case s.done:
		p.warnWrap("The workflow did not complete successfully. Changes may have been applied. Check the node before trying again.")
	default:
		p.dim("Closing this screen does not stop the change.")
		return p.renderWithBottomButtons(
			[]string{"Installing..."}, 0, false, h)
	}
	return p.renderWithBottomButtons(
		[]string{"Done"}, 0,
		s.ctx.ContentFocused, h)
}

// progressBelowRows is what the screen shows under its list at most: an
// empty row, the outcome in up to three rows, and the button row.
const progressBelowRows = 6

// spinner and stepRuns let the model run the spinner while a step runs and
// the screen shows.
func (s *InstallProgressScreen) spinner() *steplist.Spinner { return &s.spin }
func (s *InstallProgressScreen) stepRuns() bool {
	return s.started && !s.done
}

// ── HelpBindings ────────────────────────────────────────

func (s *InstallProgressScreen) HelpBindings() []key.Binding {
	if s.done {
		return resultBindings(s.ctx.HasTabs)
	}
	bindings := []key.Binding{kSidebar}
	if s.ctx.HasTabs {
		bindings = append(bindings, kShiftTabBar)
	}
	return append(bindings, kBack, kQuit)
}
