// Package install presents the root installation workflow.
package install

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/virtpriv/node/internal/installer"
	"github.com/virtpriv/node/internal/loginpassword"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/theme"
)

type wizardPhase int

const (
	wzPassword wizardPhase = iota
	wzSteps
	wzDone
)

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

	phase         wizardPhase
	width, height int

	pwInput   textinput.Model
	pwConfirm textinput.Model
	pwFocus   int // 0 = new, 1 = confirm, 2 = buttons
	pwErr     string

	failed bool

	openConsole bool
}

func newWizardModel(info installer.InstallView, session installSession) wizardModel {
	m := wizardModel{info: info, session: session, steps: info.Steps}
	m.pwInput = newWizardPasswordInput()
	m.pwConfirm = newWizardPasswordInput()
	if m.info.NeedIdentity {
		m.enterPasswordScreen()
	} else {
		m.phase = wzSteps
	}
	return m
}

func newWizardPasswordInput() textinput.Model {
	ti := textinput.New()
	ti.CharLimit = 128
	ti.SetWidth(40)
	ti.Prompt = "  "
	ti.SetStyles(textinput.DefaultStyles(theme.IsDark()))
	ti.EchoMode = textinput.EchoPassword
	return ti
}

func (m wizardModel) Init() tea.Cmd {
	if m.phase == wzSteps {
		return m.startSteps()
	}
	return nil
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

func (m wizardModel) Update(
	msg tea.Msg,
) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case wizardStartMsg:
		if m.phase != wzSteps {
			return m, nil
		}
		if msg.err != nil {
			if m.stopping {
				return m, tea.Quit
			}
			// Only the password screen offers a retry. Without one, the done
			// screen reports the error.
			if !m.info.NeedIdentity {
				m.completeErr = "Could not start the install: " + msg.err.Error()
				m.phase = wzDone
				return m, nil
			}
			m.phase = wzPassword
			m.pwErr = "Could not start the install: " + msg.err.Error()
			return m, nil
		}
		m.input.Password = loginpassword.Password{}
		m.pwInput.Reset()
		m.pwConfirm.Reset()
		return m, m.waitProgress()
	case wizardProgressMsg:
		return m.updateSteps(msg)

	case tea.PasteMsg:
		return m.handlePaste(msg)

	case tea.KeyPressMsg:
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
			return m.updatePassword(msg)
		case wzDone:
			if msg.String() == "enter" && !m.failed &&
				m.completeErr == "" {
				m.openConsole = true
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m wizardModel) handlePaste(
	msg tea.PasteMsg,
) (tea.Model, tea.Cmd) {
	if m.phase == wzPassword && m.pwFocus < 2 {
		input := &m.pwInput
		if m.pwFocus == 1 {
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
	m.pwFocus = 0
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
		if m.pwFocus > 0 {
			m.pwFocus--
			m.syncPwFocus()
		}
		return m, nil
	case "down", "tab":
		if m.pwFocus < 2 {
			m.pwFocus++
			m.syncPwFocus()
		}
		return m, nil
	case "enter":
		if m.pwFocus < 2 {
			m.pwFocus++
			m.syncPwFocus()
			return m, nil
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
		m.phase = wzSteps
		return m, m.startSteps()
	}
	if m.pwFocus < 2 {
		var cmd tea.Cmd
		switch m.pwFocus {
		case 0:
			m.pwInput, cmd = m.pwInput.Update(tea.Msg(msg))
		case 1:
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
	case 0:
		m.pwInput.Focus()
	case 1:
		m.pwConfirm.Focus()
	}
}

func (m wizardModel) viewPassword(p *wizPane) {
	p.header("Login password")
	p.blank()
	p.text("Set the password for '" + paths.AdminUser + "', your node's owner account. " +
		"Use it for your first SSH login and for sudo maintenance. " +
		"After installation, you can import existing SSH keys from System, Accounts " +
		"or add a key from System, SSH Keys.")
	p.blank()
	p.text("Use a password manager. Generate the password and " +
		"store it there first. Minimum " +
		fmt.Sprintf("%d", loginpassword.MinLength) + " bytes.")
	p.blank()

	p.input("Password:", m.pwInput.View(), m.pwFocus == 0)
	if n := len(m.pwInput.Value()); n > 0 {
		p.dim(fmt.Sprintf("   (%d bytes)", n))
	}
	p.input("Confirm: ", m.pwConfirm.View(), m.pwFocus == 1)
	if n := len(m.pwConfirm.Value()); n > 0 {
		p.dim(fmt.Sprintf("   (%d bytes)", n))
	}
	if m.pwErr != "" {
		p.blank()
		p.warn(m.pwErr)
	}
	p.blank()
	p.buttons([]string{"Start install"}, 0, m.pwFocus == 2)
	p.blank()
	p.hint("up/down: move   enter: select   ctrl+c: exit   paste works in both fields")
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
		m.failed = e.Result.Outcome == installer.RunFailed
		if e.CompletionErr != nil {
			m.completeErr = e.CompletionErr.Error()
		}
		m.phase = wzDone
		if m.stopping || e.Result.Outcome == installer.RunInterrupted {
			return m, tea.Quit
		}
		return m, nil
	}
	if e.Index < 0 || e.Index >= len(m.steps) {
		return m, nil
	}
	m.steps[e.Index].Status = e.Status
	m.steps[e.Index].Err = e.Err
	return m, m.waitProgress()
}

func renderStepRows(p *wizPane, steps []installer.InstallStepView) {
	total := len(steps)
	for i, s := range steps {
		var sty lipgloss.Style
		var ind string
		switch s.Status {
		case installer.StepDone:
			sty, ind = theme.Value, "[done]"
		case installer.StepRunning:
			sty, ind = theme.Action, "[....]"
		case installer.StepFailed:
			sty, ind = theme.Warning, "[FAIL]"
		case installer.StepSkipped:
			sty, ind = theme.Dim, "[skip]"
		default:
			sty, ind = theme.Grayed, "[wait]"
		}
		p.line(" " + sty.Render(fmt.Sprintf(
			"%s [%2d/%d] %s", ind, i+1, total, s.Name)))
		if s.Status == installer.StepFailed && s.Err != nil {
			p.warn("    Error: " + s.Err.Error())
		}
	}
}

func (m wizardModel) viewSteps(p *wizPane) {
	p.header("Installing")
	p.blank()
	renderStepRows(p, m.steps)
	p.blank()
	if m.stopping {
		p.hint("Stopping after the current step is recorded. Keep this terminal open.")
	} else {
		p.hint("ctrl+c: stop after the current step, a later install command resumes")
	}
}

func (m wizardModel) sshTarget() string {
	if m.info.Address != "" {
		return "ssh " + paths.AdminUser + "@" + m.info.Address
	}
	return "ssh " + paths.AdminUser + "@<your-server-ip>"
}

func (m wizardModel) viewDone(p *wizPane) {
	if m.failed {
		p.header("Install failed")
		p.blank()
		renderStepRows(p, m.steps)
		p.blank()
		p.warn("Installation stopped after a step failed. Inspect the reported problem before running " +
			"'sudo vpn install' again. It resumes from the " +
			"first incomplete step.")
		p.blank()
		p.hint("ctrl+c: exit")
		return
	}
	if m.completeErr != "" {
		p.header("Installation needs attention")
		p.blank()
		p.warn(m.completeErr)
		p.blank()
		p.warn("Exit and inspect the reported problem. A later 'sudo vpn install' checks the saved state before resuming.")
		p.blank()
		p.hint("ctrl+c: exit")
		return
	}
	p.header("Install complete")
	p.blank()
	renderStepRows(p, m.steps)
	p.blank()
	p.text("Connect as vpn with your installation password:")
	p.line(" " + theme.Action.Render("   "+m.sshTarget()))
	p.blank()
	p.text("Press Enter to open the node TUI as user '" +
		paths.AdminUser + "' on this terminal, or run the " +
		"command above from a SECOND terminal first to " +
		"verify your access.")
	p.blank()
	p.hint("enter: open the node TUI   ctrl+c: exit to " +
		"your shell (your install is saved, connect any time " +
		"with the command above)")
}

type wizPane struct {
	width int
	lines []string
}

func (p *wizPane) line(s string) { p.lines = append(p.lines, s) }
func (p *wizPane) blank()        { p.line("") }
func (p *wizPane) header(s string) {
	p.line(" " + theme.Header.Render(s))
}
func (p *wizPane) dim(s string) {
	for _, l := range strings.Split(ansi.Wordwrap(s, p.width-4, ""), "\n") {
		p.line(" " + theme.Dim.Render(l))
	}
}
func (p *wizPane) hint(s string) {
	for _, l := range strings.Split(ansi.Wordwrap(s, p.width-4, ""), "\n") {
		p.line(" " + theme.Grayed.Render(l))
	}
}
func (p *wizPane) warn(s string) {
	for _, l := range strings.Split(ansi.Wordwrap(s, p.width-4, ""), "\n") {
		p.line(" " + theme.Warning.Render(l))
	}
}
func (p *wizPane) text(s string) {
	for _, l := range strings.Split(ansi.Wordwrap(s, p.width-4, ""), "\n") {
		p.line(" " + theme.Label.Render(l))
	}
}
func (p *wizPane) input(label, view string, focused bool) {
	sty := theme.Label
	if focused {
		sty = theme.Action
	}
	p.line(" " + sty.Render("  "+label) + view)
}
func (p *wizPane) buttons(labels []string, idx int, focused bool) {
	var parts []string
	for i, l := range labels {
		if i == idx && focused {
			parts = append(parts, theme.BtnFocused.Render(l))
		} else {
			parts = append(parts, theme.BtnNormal.Render(l))
		}
	}
	p.line(" " + strings.Join(parts, "  "))
}

func (m wizardModel) View() tea.View {
	if m.width == 0 {
		v := tea.NewView("Loading...")
		v.AltScreen = true
		return v
	}
	bw := min(m.width-4, theme.ContentWidth)
	p := &wizPane{width: bw}

	switch m.phase {
	case wzPassword:
		m.viewPassword(p)
	case wzSteps:
		m.viewSteps(p)
	case wzDone:
		m.viewDone(p)
	}

	title := theme.Title.Render(
		"Install Virtual Private Node")
	box := theme.Box.Width(bw).Render(
		strings.Join(p.lines, "\n"))
	full := lipgloss.JoinVertical(lipgloss.Center,
		"", title, box)
	content := lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center, full)
	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "Virtual Private Node"
	return v
}

// Run presents a session. The installer keeps the execution and the lock.
func Run(info installer.InstallView, session *installer.InstallSession) (bool, error) {
	// Root installation uses the built-in theme, not user-owned preferences.
	theme.Init(true)
	result, err := tea.NewProgram(newWizardModel(info, session)).Run()
	if err != nil {
		return false, err
	}
	return result.(wizardModel).openConsole, nil
}
