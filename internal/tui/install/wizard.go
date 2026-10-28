// Package install presents the root installation workflow.
package install

import (
	"fmt"
	"image/color"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/harmonica"

	"github.com/virtpriv/node/internal/installer"
	"github.com/virtpriv/node/internal/loginpassword"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/theme"
)

type wizardPhase int

const (
	wzPassword wizardPhase = iota
	wzSteps
	// wzDone ends an install that did not complete: a failed step, a
	// finalization error or a lost progress stream.
	wzDone
	// wzFinish shows the last title after a complete install.
	wzFinish
	wzConnect
)

// Password screen focus, top to bottom, then the two buttons.
const (
	focusPassword = iota
	focusConfirm
	focusStart
	focusExit
)

// One clock drives every animation: the opening, the typed titles, the
// spinner and the bar. It starts with the first window size and runs while
// the screen is open. Bubble Tea only sends the cells that changed, so a
// still picture costs nothing on the link.
const (
	frameRate = 30
	// spinEvery advances the spinner every third frame, ten times a second.
	spinEvery = 3
	// finishHold keeps the finished install in view before the connect dialog.
	finishHold = 24
	// typeEvery is how many frames each letter of a title takes to appear.
	typeEvery = 2
	// loginEvery is how often the connect dialog reads SSH's log.
	loginEvery = 3 * time.Second
)

type frameTick struct{}
type loginTick struct{}
type loginResultMsg struct {
	seen bool
	err  error
}

type wizardStartMsg struct{ err error }
type wizardProgressMsg struct {
	event installer.InstallEvent
	ok    bool
}
type installSession interface {
	Start(installer.InteractiveInput) error
	Stop()
	Events() <-chan installer.InstallEvent
}

type wizardModel struct {
	info        installer.InstallView
	session     installSession
	input       installer.InteractiveInput
	steps       []installer.InstallStepView
	completeErr string
	stopping    bool
	failed      bool
	// starting is set from Enter on Start install until the start answers.
	// The dialog stays on screen, so a refused start shows its error at once.
	starting bool

	phase         wizardPhase
	width, height int

	pwInput   textinput.Model
	pwConfirm textinput.Model
	pwFocus   int
	pwErr     string

	// The animations: whether the clock runs, the frame count, the opening's
	// progress and the frames held after a complete install. typed is the
	// title being typed out, from frame typedFrom.
	clock               bool
	frames, drawn, held int
	typed               string
	typedFrom           int

	spinFrames []string
	bar        progress.Model
	// The bar glides to its share of finished steps on a spring.
	spring                     harmonica.Spring
	barAt, barSpeed, barTarget float64
	started                    time.Time
	finished                   time.Time
	stepFrom                   []time.Time
	stepTook                   []time.Duration

	login           func() (bool, error)
	loggedIn        bool
	loginUnreadable bool
	// asking is the question shown before leaving without a login, and
	// askExit whether its Exit button is selected.
	asking, askExit bool
	// rootLoginOff follows the install record: before the run, then from
	// the final event. rootLoginMayBeOff comes from the final event when the
	// SSH step failed after it may have turned root's login off.
	rootLoginOff, rootLoginMayBeOff bool
}

func newWizardModel(info installer.InstallView, session installSession, login func() (bool, error)) wizardModel {
	m := wizardModel{info: info, session: session, steps: info.Steps, login: login, rootLoginOff: info.RootLoginOff,
		stepFrom: make([]time.Time, len(info.Steps)), stepTook: make([]time.Duration, len(info.Steps))}
	m.pwInput = newWizardPasswordInput("Password")
	m.pwConfirm = newWizardPasswordInput("Confirm password")
	m.spinFrames = trimmed(spinner.Dot.Frames)
	m.bar = progress.New(progress.WithColors(theme.ColorPrimary), progress.WithFillCharacters('━', '━'),
		progress.WithoutPercentage())
	m.bar.EmptyColor = theme.ColorFaint
	m.spring = harmonica.NewSpring(harmonica.FPS(frameRate), 6, 1)
	if m.info.NeedIdentity {
		m.enterPasswordScreen()
	} else {
		m.phase = wzSteps
	}
	return m
}

// The spinner's frames carry a trailing space that would push the step name
// one column right of the finished rows.
func trimmed(frames []string) []string {
	out := make([]string, len(frames))
	for i, f := range frames {
		out[i] = strings.TrimSpace(f)
	}
	return out
}

func newWizardPasswordInput(placeholder string) textinput.Model {
	ti := textinput.New()
	ti.CharLimit = 128
	ti.Prompt = " "
	ti.Placeholder = placeholder
	ti.EchoMode = textinput.EchoPassword
	// The field is shaded like a desktop text field. The input fills its own
	// width with the text style, so the shade reaches the field's end.
	st := textinput.DefaultStyles(theme.IsDark())
	for _, s := range []struct {
		state       *textinput.StyleState
		shade, hint color.Color
	}{
		{&st.Focused, theme.ColorInstallShade, theme.ColorInstallHint},
		{&st.Blurred, theme.ColorInstallShadeDim, theme.ColorDim},
	} {
		s.state.Text = s.state.Text.Foreground(theme.ColorPrimary).Background(s.shade)
		s.state.Placeholder = s.state.Placeholder.Foreground(s.hint).Background(s.shade)
		s.state.Prompt = s.state.Prompt.Foreground(theme.ColorPrimary).Background(s.shade)
	}
	st.Cursor.Color = theme.ColorCursor
	ti.SetStyles(st)
	ti.SetWidth(fieldW - 2)
	return ti
}

// fieldW is a password field's width, its one-cell prompt included.
const fieldW = 32

// passwordField shows the live input while it has focus. Otherwise it draws
// the masked characters or the placeholder as one shaded block, because the
// input leaves its cursor cell unshaded when it has no focus.
func passwordField(in textinput.Model, focused bool) string {
	if focused {
		return in.View()
	}
	shade := lipgloss.NewStyle().Background(theme.ColorInstallShadeDim).Width(fieldW)
	if in.Value() == "" {
		return shade.Foreground(theme.ColorDim).Render(" " + in.Placeholder)
	}
	masked := strings.Repeat(string(in.EchoCharacter), min(utf8.RuneCountInString(in.Value()), fieldW-2))
	return shade.Foreground(theme.ColorPrimary).Render(" " + masked)
}

func (m wizardModel) Init() tea.Cmd {
	if m.phase == wzSteps {
		return m.startSteps()
	}
	return nil
}

func nextFrame() tea.Cmd {
	return tea.Tick(time.Second/frameRate, func(time.Time) tea.Msg { return frameTick{} })
}

// A start resolves before input is enabled again. Success starts one reader
// of the ordered progress stream. There are no overlapping attempts.
func (m wizardModel) startSteps() tea.Cmd {
	session, input := m.session, m.input
	return func() tea.Msg { return wizardStartMsg{err: session.Start(input)} }
}

func (m wizardModel) waitProgress() tea.Cmd {
	events := m.session.Events()
	return func() tea.Msg { e, ok := <-events; return wizardProgressMsg{event: e, ok: ok} }
}

func (m wizardModel) checkLogin() tea.Cmd {
	login := m.login
	return func() tea.Msg { seen, err := login(); return loginResultMsg{seen: seen, err: err} }
}

func (m wizardModel) Update(
	msg tea.Msg,
) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if !m.clock {
			m.clock = true
			return m, nextFrame()
		}
		return m, nil

	case frameTick:
		return m.advanceFrame()

	case loginTick:
		if m.phase != wzConnect || m.loggedIn {
			return m, nil
		}
		return m, m.checkLogin()

	case loginResultMsg:
		if m.phase != wzConnect || m.loggedIn {
			return m, nil
		}
		// A read that failed says nothing about a login, even with a result.
		m.loginUnreadable = msg.err != nil
		if msg.err == nil && msg.seen {
			m.loggedIn = true
			m.asking = false
			return m, nil
		}
		return m, tea.Tick(loginEvery, func(time.Time) tea.Msg { return loginTick{} })

	case wizardStartMsg:
		if !m.starting && m.phase != wzSteps {
			return m, nil
		}
		m.starting = false
		if msg.err != nil {
			if m.stopping {
				return m, tea.Quit
			}
			// Only the password screen offers a retry. Without one, the done
			// screen reports the error.
			if !m.info.NeedIdentity {
				m.completeErr = installer.RefusedStartText(msg.err)
				m.phase = wzDone
				return m, nil
			}
			m.pwErr = installer.RefusedStartText(msg.err) + " Choose Start install to try again."
			return m, nil
		}
		m.phase = wzSteps
		m.input.Password = loginpassword.Password{}
		m.pwInput.Reset()
		m.pwConfirm.Reset()
		m.started = time.Now()
		return m, m.waitProgress()
	case wizardProgressMsg:
		return m.updateSteps(msg)

	case tea.PasteMsg:
		// Nothing reaches a field the owner cannot see yet, or while the
		// start answers.
		if m.phase == wzPassword && (!m.dialogShown() || m.starting) {
			return m, nil
		}
		return m.handlePaste(msg)

	case tea.KeyPressMsg:
		if m.phase == wzFinish && !m.loggedIn && msg.String() == "ctrl+c" {
			// Every step is done, so root can no longer log in over SSH.
			// Leaving goes through the question, as on the connect dialog.
			m.phase = wzConnect
			m.asking, m.askExit = true, false
			return m, m.checkLogin()
		}
		if m.phase == wzConnect && !m.loggedIn {
			return m.updateConnect(msg)
		}
		if msg.String() == "ctrl+c" {
			if m.phase == wzSteps {
				m.stopping = true
				m.session.Stop()
				return m, nil
			}
			return m, tea.Quit
		}
		switch m.phase {
		case wzPassword:
			// Keys wait for the dialog and for the start's answer. ctrl+c
			// above still leaves, as Exit does.
			if !m.dialogShown() || m.starting {
				return m, nil
			}
			return m.updatePassword(msg)
		case wzConnect:
			if msg.String() == "enter" {
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

// updateConnect asks once before leaving the connect dialog without a login,
// because root can no longer log in over SSH.
func (m wizardModel) updateConnect(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if !m.asking {
		if key == "enter" || key == "ctrl+c" {
			m.asking, m.askExit = true, false
		}
		return m, nil
	}
	switch key {
	case "left":
		m.askExit = false
	case "right":
		m.askExit = true
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		if m.askExit {
			return m, tea.Quit
		}
		m.asking = false
	}
	return m, nil
}

// advanceFrame moves every animation one frame.
func (m wizardModel) advanceFrame() (tea.Model, tea.Cmd) {
	m.frames++
	if m.drawn < namesShown {
		m.drawn++
	}
	// A title types itself out once the frame is drawn, and again whenever
	// the title changes.
	if m.drawn >= namesShown && m.typed != m.title() {
		m.typed, m.typedFrom = m.title(), m.frames
	}
	m.barAt, m.barSpeed = m.spring.Update(m.barAt, m.barSpeed, m.barTarget)
	if m.phase == wzFinish && m.titleDone() {
		m.held++
		if m.held >= finishHold {
			m.phase = wzConnect
			return m, tea.Batch(nextFrame(), m.checkLogin())
		}
	}
	return m, nextFrame()
}

func (m wizardModel) spinnerView() string {
	frame := m.spinFrames[(m.frames/spinEvery)%len(m.spinFrames)]
	return lipgloss.NewStyle().Foreground(theme.ColorBitcoin).Render(frame)
}

func (m wizardModel) handlePaste(
	msg tea.PasteMsg,
) (tea.Model, tea.Cmd) {
	if m.phase == wzPassword && m.pwFocus < focusStart {
		input := &m.pwInput
		if m.pwFocus == focusConfirm {
			input = &m.pwConfirm
		}
		value := strings.TrimSuffix(msg.Content, "\n")
		input.SetValue(value)
		if input.Value() != value {
			input.Reset()
			m.pwErr = "Paste rejected and field cleared: unsupported characters or more than 128 characters."
		} else {
			m.pwErr = ""
		}
	}
	return m, nil
}

func (m *wizardModel) enterPasswordScreen() {
	m.pwFocus = focusPassword
	m.pwErr = ""
	m.pwInput.Focus()
	m.pwConfirm.Blur()
	m.phase = wzPassword
}

func (m wizardModel) updatePassword(
	msg tea.KeyPressMsg,
) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "shift+tab":
		m.pwFocus = max(focusPassword, min(m.pwFocus, focusStart)-1)
		m.syncPwFocus()
		return m, nil
	case "down", "tab":
		if m.pwFocus < focusStart {
			m.pwFocus++
			m.syncPwFocus()
		}
		return m, nil
	case "left":
		if m.pwFocus >= focusStart {
			m.pwFocus = focusExit
		}
		return m, nil
	case "right":
		if m.pwFocus >= focusStart {
			m.pwFocus = focusStart
		}
		return m, nil
	case "enter":
		switch m.pwFocus {
		case focusPassword, focusConfirm:
			m.pwFocus++
			m.syncPwFocus()
			return m, nil
		case focusExit:
			return m, tea.Quit
		}
		if m.pwInput.Value() != m.pwConfirm.Value() {
			m.pwErr = "Passwords do not match."
			return m, nil
		}
		pw, err := loginpassword.New(m.pwInput.Value())
		if err != nil {
			m.pwErr = err.Error() + "."
			return m, nil
		}
		m.input.Password = pw
		m.pwErr = ""
		m.starting = true
		return m, m.startSteps()
	}
	if m.pwFocus < focusStart {
		var cmd tea.Cmd
		switch m.pwFocus {
		case focusPassword:
			m.pwInput, cmd = m.pwInput.Update(tea.Msg(msg))
		case focusConfirm:
			m.pwConfirm, cmd = m.pwConfirm.Update(tea.Msg(msg))
		}
		return m, cmd
	}
	return m, nil
}

func (m *wizardModel) syncPwFocus() {
	m.pwInput.Blur()
	m.pwConfirm.Blur()
	switch m.pwFocus {
	case focusPassword:
		m.pwInput.Focus()
	case focusConfirm:
		m.pwConfirm.Focus()
	}
}

func (m wizardModel) updateSteps(msg wizardProgressMsg) (tea.Model, tea.Cmd) {
	if m.phase != wzSteps {
		return m, nil
	}
	if !msg.ok {
		m.completeErr = "Installation observation ended without a final result."
		m.phase = wzDone
		return m, nil
	}
	e := msg.event
	if e.Final {
		m.rootLoginOff, m.rootLoginMayBeOff = e.RootLoginOff, e.RootLoginMayBeOff
		m.failed = e.Result.Outcome == installer.RunFailed
		if e.CompletionErr != nil {
			m.completeErr = e.CompletionErr.Error()
		}
		// A pause asked for during the last step has nothing left to
		// pause, so a complete install still goes on to the connect dialog.
		complete := e.Result.Outcome == installer.RunComplete && m.completeErr == ""
		if e.Result.Outcome == installer.RunInterrupted || (m.stopping && !complete) {
			return m, tea.Quit
		}
		m.finished = time.Now()
		if m.failed || m.completeErr != "" || e.Result.Outcome != installer.RunComplete {
			m.phase = wzDone
			return m, nil
		}
		// A complete install shows its last title, then moves on to the
		// connect dialog by itself. The install terminal never opens the TUI.
		m.phase = wzFinish
		m.barTarget = 1
		return m, nil
	}
	if e.Index < 0 || e.Index >= len(m.steps) {
		return m, nil
	}
	m.steps[e.Index].Status = e.Status
	m.steps[e.Index].Err = e.Err
	switch e.Status {
	case installer.StepRunning:
		m.stepFrom[e.Index] = time.Now()
	default:
		if !m.stepFrom[e.Index].IsZero() {
			m.stepTook[e.Index] = time.Since(m.stepFrom[e.Index])
		}
	}
	m.barTarget = float64(m.stepsDone()) / float64(max(1, len(m.steps)))
	return m, m.waitProgress()
}

func (m wizardModel) stepsDone() int {
	n := 0
	for _, s := range m.steps {
		if s.Status == installer.StepDone || s.Status == installer.StepSkipped {
			n++
		}
	}
	return n
}

func (m wizardModel) sshTarget() string {
	if m.info.Address != "" {
		return "ssh " + paths.AdminUser + "@" + m.info.Address
	}
	return "ssh " + paths.AdminUser + "@<your-server-ip>"
}

func clock(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

func count(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// ── Drawing ─────────────────────────────────────────────

// statusBar draws the top bar's two rows: empty on the password screen and
// with the connect dialog, otherwise the running step and the time above the
// bar.
func (m wizardModel) statusBar() []string {
	if m.phase == wzPassword || m.phase == wzConnect {
		return []string{"", ""}
	}
	now, took := "", ""
	if !m.started.IsZero() {
		took = clock(time.Since(m.started))
	}
	if !m.finished.IsZero() {
		took = clock(m.finished.Sub(m.started))
	}
	for _, s := range m.steps {
		switch s.Status {
		case installer.StepRunning:
			now = m.spinnerView() + " " + theme.Label.Render(s.Name)
		case installer.StepFailed:
			now = theme.Warning.Render("✗ " + s.Name)
		}
	}
	if m.phase == wzFinish {
		now = lipgloss.NewStyle().Foreground(theme.ColorSuccess).Render("✓") + " " + theme.Label.Render("All steps finished")
		took = "in " + took
	}
	// The step that runs or failed, the number a pause names in the shell.
	of := fmt.Sprintf(" %d of %d ", min(m.stepsDone()+1, len(m.steps)), len(m.steps))
	bar := m.bar
	bar.SetWidth(frameInside - 2 - len(of))
	return []string{
		spread(" "+now, theme.Dim.Render(took)+" ", frameInside),
		" " + bar.ViewAs(min(1, max(0, m.barAt))) + theme.Dim.Render(of),
	}
}

// title is the heading centered above the content.
func (m wizardModel) title() string {
	switch {
	case m.phase == wzPassword:
		return "Install Virtual Private Node"
	case m.phase == wzFinish:
		return "Virtual Private Node is installed"
	case m.failed:
		return "Install failed"
	case m.phase == wzDone:
		return "Installation needs attention"
	}
	return "Installing Virtual Private Node"
}

// titleDone reports whether the current title has been typed out.
func (m wizardModel) titleDone() bool {
	return m.typed == m.title() && (m.frames-m.typedFrom)/typeEvery >= utf8.RuneCountInString(m.typed)
}

// titleRows types the title out letter by letter, with a thin cursor while
// it types. Its place is fixed by the whole title, so it does not move.
func (m wizardModel) titleRows() []string {
	full := m.title()
	shown := ""
	if m.typed == full {
		r := []rune(full)
		shown = string(r[:min(len(r), (m.frames-m.typedFrom)/typeEvery)])
	}
	t := lipgloss.NewStyle().Bold(true).Foreground(theme.ColorPrimary).Render(shown)
	if m.typed == full && !m.titleDone() {
		t += theme.Dim.Render("▏")
	}
	return []string{"", strings.Repeat(" ", (contentW-lipgloss.Width(full))/2) + t}
}

// stepRows lists every step: a mark, the name and, once it ran, its time.
// from and to are the first and last rows of the running or failed step,
// its error included.
func (m wizardModel) stepRows(w int) (rows []string, from, to int) {
	done := lipgloss.NewStyle().Foreground(theme.ColorSuccess).Render("✓")
	faint := lipgloss.NewStyle().Foreground(theme.ColorFaint)
	for i, s := range m.steps {
		var mark, name, took string
		switch s.Status {
		case installer.StepDone:
			mark, name = done, theme.Label.Render(s.Name)
			took = faint.Render(clock(m.stepTook[i]))
		case installer.StepRunning:
			mark = m.spinnerView()
			name = lipgloss.NewStyle().Bold(true).Foreground(theme.ColorPrimary).Render(s.Name)
			took = theme.Dim.Render(clock(time.Since(m.stepFrom[i])))
		case installer.StepFailed:
			mark, name = theme.Warning.Render("✗"), theme.Warning.Render(s.Name)
		case installer.StepSkipped:
			// Done in an earlier run: a gray check and no time.
			mark, name = theme.Grayed.Render("✓"), theme.Dim.Render(s.Name)
		default:
			mark, name = faint.Render("·"), faint.Render(s.Name)
		}
		start := len(rows)
		rows = append(rows, spread(" "+mark+" "+name, took, w))
		if s.Status == installer.StepFailed && s.Err != nil {
			for _, l := range wrap("Error: "+s.Err.Error(), w-6, theme.Warning) {
				rows = append(rows, "   "+l)
			}
		}
		if s.Status == installer.StepRunning || s.Status == installer.StepFailed {
			from, to = start, len(rows)-1
		}
	}
	return rows, from, to
}

// stepWindow shows as many steps as fit in h rows and keeps the running or
// failed step in view, with a few of the steps after it.
func (m wizardModel) stepWindow(h int) []string {
	const colW = 65
	rows, from, to := m.stepRows(colW)
	fits := max(1, h-1)
	// The running or failed step and its whole error stay in view, with a
	// few steps after it. An error longer than the window keeps its step's
	// name. After a complete install the list shows its end.
	first := min(max(0, to-fits+4), max(0, len(rows)-fits), from)
	if m.phase == wzFinish {
		first = max(0, len(rows)-fits)
	}
	out := []string{""}
	for _, r := range rows[first:min(len(rows), first+fits)] {
		out = append(out, strings.Repeat(" ", (contentW-colW)/2)+r)
	}
	return out
}

func (m wizardModel) passwordDialog() []string {
	label := func(text string, focused bool) string {
		style := lipgloss.NewStyle().Width(10).Align(lipgloss.Right).Foreground(theme.ColorLabel)
		if focused {
			style = style.Foreground(theme.ColorPrimary).Bold(true)
		}
		return style.Render(text) + "  "
	}
	hint := theme.Dim.Render(fmt.Sprintf("At least %s.", count(loginpassword.MinLength, "character")))
	if n := utf8.RuneCountInString(m.pwInput.Value()); n > 0 {
		hint = theme.Dim.Render(count(n, "character"))
	}
	body := []string{
		label("Password", m.pwFocus == focusPassword) + passwordField(m.pwInput, m.pwFocus == focusPassword), "",
		label("Confirm", m.pwFocus == focusConfirm) + passwordField(m.pwConfirm, m.pwFocus == focusConfirm), "",
	}
	indent := strings.Repeat(" ", 12)
	if m.pwErr != "" {
		// The error lines up with the fields and ends where they end.
		for _, l := range wrap(m.pwErr, fieldW, theme.Warning) {
			body = append(body, indent+l)
		}
	} else {
		body = append(body, indent+hint)
	}
	return dialog("Choose a password",
		"You use it to log in to your node. Keep it in a password manager.",
		body, button("Exit", m.pwFocus == focusExit)+"  "+button("Start install", m.pwFocus == focusStart))
}

func (m wizardModel) connectDialog() []string {
	ok := lipgloss.NewStyle().Foreground(theme.ColorSuccess)
	if m.loggedIn {
		return dialog("You are logged in", "Your node is open in the other terminal. You can close this one.",
			[]string{ok.Render("✓") + " " + theme.Label.Render("Logged in as "+paths.AdminUser+" over SSH")},
			button("Exit", true))
	}
	target := lipgloss.NewStyle().Background(theme.ColorInstallShade).Width(dialogW - 8).
		Render(" " + lipgloss.NewStyle().Bold(true).Foreground(theme.ColorPrimary).Background(theme.ColorInstallShade).Render(m.sshTarget()))
	status := "Waiting for your login"
	if m.loginUnreadable {
		status = "Could not read the login log"
	}
	if m.asking {
		return dialog("Leave without logging in?",
			"Log in once first to check your password works. Root can no longer log in over SSH.",
			[]string{target}, button("Wait", !m.askExit)+"  "+button("Exit", m.askExit))
	}
	return dialog("Your node is ready",
		"Open a new terminal and run this command. Enter your password and your node opens.",
		[]string{target, "", m.spinnerView() + " " + theme.Dim.Render(status)},
		button("Exit", true))
}

// endNotice explains an install that did not complete, below its steps.
func (m wizardModel) endNotice() []string {
	text := "Exit and inspect the reported problem. A later 'sudo vpn install' checks the saved state before resuming."
	if m.failed {
		text = "Installation stopped after a step failed. Inspect the reported problem before running 'sudo vpn install' again. It resumes from the first incomplete step."
	}
	text += " Details are in " + paths.LogFile + "."
	if m.rootLoginOff {
		text += " " + installer.RootLoginOffNotice
	} else if m.rootLoginMayBeOff {
		text += " " + installer.RootLoginMayBeOffNotice
	}
	var out []string
	if m.completeErr != "" {
		out = append(out, wrap(m.completeErr, contentW-4, theme.Warning)...)
		out = append(out, "")
	}
	return append(out, wrap(text, contentW-4, theme.Warning)...)
}

// helpLine names the keys that work on the screen, in the TUI's help style.
func (m wizardModel) helpLine() string {
	switch m.phase {
	case wzPassword:
		if !m.dialogShown() {
			return ""
		}
		return theme.Hints("↑↓", "fields", "←→", "buttons", "↩", "select", "⌃C", "quit")
	case wzSteps:
		if m.stopping {
			return theme.HelpDesc.Render("Pausing after this step. Keep this terminal open.")
		}
		return theme.Hints("⌃C", "pause install")
	case wzDone:
		return theme.Hints("⌃C", "quit")
	case wzConnect:
		if m.asking {
			return theme.Hints("←→", "buttons", "↩", "select")
		}
		return theme.Hints("↩", "exit")
	}
	return ""
}

// dialogShown reports whether the password dialog is drawn: after the
// opening and once its title is typed out.
func (m wizardModel) dialogShown() bool {
	return m.drawn >= namesShown && m.titleDone()
}

func (m wizardModel) View() tea.View {
	if m.width == 0 {
		v := tea.NewView("Loading...")
		v.BackgroundColor, v.ForegroundColor = theme.ColorBackground, theme.ColorText
		v.AltScreen = true
		return v
	}
	h := theme.ContentRows
	var content []string
	head := m.titleRows()
	switch m.phase {
	case wzPassword:
		if m.drawn >= namesShown {
			content = head
		}
		if m.dialogShown() {
			content = append(head, center(m.passwordDialog(), contentW, h-len(head))...)
		}
	case wzSteps, wzFinish:
		content = append(head, m.stepWindow(h-len(head))...)
	case wzDone:
		notice := m.endNotice()
		content = append(head, m.stepWindow(h-len(head)-len(notice)-1)...)
		content = append(content, "")
		for _, l := range notice {
			content = append(content, "  "+l)
		}
	case wzConnect:
		content = center(m.connectDialog(), contentW, h)
	}
	full := frame(m.statusBar(), m.drawn, m.loggedIn, content, m.helpLine())
	v := tea.NewView(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, full))
	v.BackgroundColor, v.ForegroundColor = theme.ColorBackground, theme.ColorText
	v.AltScreen = true
	v.WindowTitle = "Virtual Private Node"
	return v
}

// Run presents a session. The installer keeps the execution and the lock.
func Run(info installer.InstallView, session *installer.InstallSession) error {
	// Root installation uses the built-in theme, not user-owned preferences.
	theme.Init(true)
	_, err := tea.NewProgram(newWizardModel(info, session, session.SSHLoginSeen)).Run()
	return err
}
