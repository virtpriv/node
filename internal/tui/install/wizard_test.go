package install

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/installer"
	"github.com/virtpriv/node/internal/loginpassword"
)

type fakeSession struct {
	input         installer.InteractiveInput
	starts, stops int
	err           error
	events        chan installer.InstallEvent
}

func (s *fakeSession) Start(in installer.InteractiveInput) error {
	s.starts++
	s.input = in
	return s.err
}
func (s *fakeSession) Stop()                                 { s.stops++ }
func (s *fakeSession) Events() <-chan installer.InstallEvent { return s.events }

// loginStub stands in for the SSH log reader of the connect dialog.
type loginStub struct {
	calls int
	seen  bool
	err   error
}

func (l *loginStub) check() (bool, error) { l.calls++; return l.seen, l.err }

func wizardFixture() (wizardModel, *fakeSession) {
	m, session, _ := wizardFixtureWithLogin()
	return m, session
}

func wizardFixtureWithLogin() (wizardModel, *fakeSession, *loginStub) {
	session := &fakeSession{events: make(chan installer.InstallEvent, 10)}
	info := installer.InstallView{NeedIdentity: true,
		Steps: []installer.InstallStepView{{Name: "First"}, {Name: "Second"}}}
	login := &loginStub{}
	return showDialog(newWizardModel(info, session, login.check)), session, login
}

// showDialog runs the opening until the password dialog is on screen, as the
// owner sees it before typing.
func showDialog(m wizardModel) wizardModel {
	for range 1000 {
		if m.dialogShown() {
			return m
		}
		next, _ := m.Update(frameTick{})
		m = next.(wizardModel)
	}
	panic("the password dialog never showed")
}
func updateWizard(t *testing.T, m wizardModel, msg tea.Msg) (wizardModel, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	return next.(wizardModel), cmd
}
func enter() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEnter} }
func ctrlC() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl} }

func TestWizardPasswordPastePreservesOrRejects(t *testing.T) {
	cases := []struct {
		name, value, want string
	}{
		{"plain", "abcdefghijklmnop", "abcdefghijklmnop"},
		{"padding", "  abcdefghijklmnop  ", "  abcdefghijklmnop  "},
		{"one trailing LF", "abcdefghijklmnop\n", "abcdefghijklmnop"},
		{"limit", strings.Repeat("x", 128), strings.Repeat("x", 128)},
		{"unicode characters", strings.Repeat("é", 16), strings.Repeat("é", 16)},
		{"over limit", strings.Repeat("x", 129), ""},
		{"second line", "abcdefghijklmnop\nsecond", ""},
		{"two trailing LF", "abcdefghijklmnop\n\n", ""},
		{"CRLF", "abcdefghijklmnop\r\n", ""},
		{"tab", "abcdefghijklmnop\tend", ""},
		{"NUL", "abcdefghijklmnop\x00", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, session := wizardFixture()
			for focus := 0; focus < 2; focus++ {
				var cmd tea.Cmd
				m, cmd = updateWizard(t, m, tea.PasteMsg{Content: tc.value})
				if cmd != nil || session.starts != 0 {
					t.Fatal("paste scheduled installation")
				}
				input := m.pwInput
				if focus == 1 {
					input = m.pwConfirm
				}
				if tc.want != "" {
					if input.Value() != tc.want || m.pwErr != "" {
						t.Fatal("accepted secret changed")
					}
				} else if input.Value() != "" || m.pwErr == "" {
					t.Fatal("altered input was not rejected and cleared")
				}
				m, cmd = updateWizard(t, m, enter())
				if cmd != nil || session.starts != 0 {
					t.Fatal("input navigation scheduled installation")
				}
			}
			m, cmd := updateWizard(t, m, enter())
			if tc.want != "" {
				if m.phase != wzSteps || cmd == nil {
					t.Fatal("valid password could not continue")
				}
				result := cmd()
				m, cmd = updateWizard(t, m, result)
				if session.starts != 1 || session.input.Password.Text() != tc.want {
					t.Fatal("submitted password differs from original")
				}
				if m.pwInput.Value() != "" || m.pwConfirm.Value() != "" || m.input.Password.Text() != "" || cmd == nil {
					t.Fatal("submission did not clear input or observe progress")
				}
			} else if m.phase != wzPassword || cmd != nil || session.starts != 0 {
				t.Fatal("invalid input reached installation")
			}
		})
	}
}

// Keys and paste during the opening must not reach a field the owner cannot
// see yet.
func TestWizardPasswordIgnoresInputBeforeDialog(t *testing.T) {
	session := &fakeSession{events: make(chan installer.InstallEvent, 10)}
	m := newWizardModel(installer.InstallView{NeedIdentity: true,
		Steps: []installer.InstallStepView{{Name: "First"}}}, session, (&loginStub{}).check)
	for _, msg := range []tea.Msg{tea.KeyPressMsg{Code: 'x', Text: "x"}, tea.PasteMsg{Content: "abcdefghijklmnop"}, enter(), enter()} {
		m, _ = updateWizard(t, m, msg)
	}
	if m.pwInput.Value() != "" || m.pwConfirm.Value() != "" || m.pwFocus != focusPassword || m.pwErr != "" || session.starts != 0 {
		t.Fatal("input reached the password dialog before it showed")
	}
	m = showDialog(m)
	m, _ = updateWizard(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.pwInput.Value() != "x" {
		t.Fatal("input did not reach the shown dialog")
	}
}

func TestWizardPasswordMinimumMismatchAndRecovery(t *testing.T) {
	m, session := wizardFixture()
	m.pwFocus = 2
	for _, values := range [][2]string{{"abcdefghijklmno", "abcdefghijklmno"}, {"abcdefghijklmnop", "abcdefghijklmnopX"}} {
		m.pwInput.SetValue(values[0])
		m.pwConfirm.SetValue(values[1])
		var cmd tea.Cmd
		m, cmd = updateWizard(t, m, enter())
		if m.phase != wzPassword || m.pwErr == "" || cmd != nil || session.starts != 0 {
			t.Fatal("short or mismatched password accepted")
		}
	}
	m.pwInput.SetValue("abcdefghijklmnop")
	m.pwConfirm.SetValue("abcdefghijklmnop")
	m, cmd := updateWizard(t, m, enter())
	if m.phase != wzSteps || cmd == nil {
		t.Fatal("corrected password could not continue")
	}
}

func TestWizardStopWaitsForEngineResult(t *testing.T) {
	m, session := wizardFixture()
	m.phase = wzSteps
	pw, _ := loginpassword.New("abcdefghijklmnop")
	m.input.Password = pw
	start := m.Init()
	m, wait := updateWizard(t, m, start())
	if wait == nil || session.starts != 1 {
		t.Fatal("missing observer")
	}
	m, cmd := updateWizard(t, m, ctrlC())
	if cmd != nil || !m.stopping || session.stops != 1 || m.phase != wzSteps {
		t.Fatal("Ctrl+C exited without waiting for the engine")
	}
	for _, msg := range []tea.Msg{enter(), ctrlC()} {
		m, cmd = updateWizard(t, m, msg)
		if cmd != nil || session.starts != 1 {
			t.Fatal("stopping restarted or abandoned work")
		}
	}
	event := installer.InstallEvent{Index: 0, Status: installer.StepDone}
	session.events <- event
	m, wait = updateWizard(t, m, wait())
	if m.steps[0].Status != installer.StepDone || wait == nil {
		t.Fatal("admitted step result lost")
	}
	session.events <- installer.InstallEvent{Final: true, Result: installer.RunResult{Outcome: installer.RunInterrupted}}
	m, cmd = updateWizard(t, m, wait())
	assertQuit(t, cmd)
}

// A start the installer refuses, for example because the cache decision was
// not saved, returns to the password screen with the error and can be retried.
func TestWizardDecisionRetryAndCompletionFailure(t *testing.T) {
	m, session := wizardFixture()
	m.pwInput.SetValue("abcdefghijklmnop")
	m.pwConfirm.SetValue("abcdefghijklmnop")
	m.pwFocus = 2
	session.err = errors.New("cache write failed")
	m, cmd := updateWizard(t, m, enter())
	if m.phase != wzSteps || cmd == nil {
		t.Fatal("password did not lead straight to the install")
	}
	m, cmd = updateWizard(t, m, cmd())
	if m.phase != wzPassword || m.pwErr == "" || cmd != nil {
		t.Fatal("decision error not recoverable")
	}
	session.err = nil
	m.pwFocus = 2
	m, cmd = updateWizard(t, m, enter())
	if cmd == nil {
		t.Fatal("retry not scheduled")
	}
	m, wait := updateWizard(t, m, cmd())
	if wait == nil || session.starts != 2 || session.input.Password.Text() != "abcdefghijklmnop" {
		t.Fatal("retry did not submit the password")
	}
	event := installer.InstallEvent{Index: 0, Status: installer.StepSkipped}
	session.events <- event
	m, wait = updateWizard(t, m, wait())
	if m.steps[0].Status != installer.StepSkipped || wait == nil {
		t.Fatal("skip evidence lost")
	}
	failure := errors.New("terminal ledger publication failed")
	session.events <- installer.InstallEvent{Final: true, Result: installer.RunResult{Outcome: installer.RunComplete}, CompletionErr: failure}
	m, cmd = updateWizard(t, m, wait())
	if m.phase != wzDone || m.completeErr != failure.Error() || cmd != nil {
		t.Fatal("finalization failure lost")
	}
	m, cmd = updateWizard(t, m, enter())
	if cmd != nil {
		t.Fatal("enter acted on a failed finalization")
	}
}

// After a complete install the screen moves to the connect dialog by itself:
// no key is needed and the install terminal never opens the node TUI.
func reachConnect(t *testing.T, m wizardModel) wizardModel {
	t.Helper()
	m.phase = wzSteps
	m, _ = updateWizard(t, m, wizardProgressMsg{ok: true, event: installer.InstallEvent{Final: true, Result: installer.RunResult{Outcome: installer.RunComplete}}})
	for range 1000 {
		if m.phase == wzConnect {
			return m
		}
		m, _ = updateWizard(t, m, frameTick{})
	}
	t.Fatal("a complete install never reached the connect dialog")
	return m
}

func TestWizardCompletionWaitsForSSHLogin(t *testing.T) {
	m, _, login := wizardFixtureWithLogin()
	m = reachConnect(t, m)
	if login.calls != 0 {
		t.Fatal("login checked before the connect dialog asked")
	}
	check := func(seen bool, err error) {
		t.Helper()
		login.seen, login.err = seen, err
		var cmd tea.Cmd
		m, cmd = updateWizard(t, m, loginTick{})
		if cmd == nil {
			t.Fatal("connect dialog did not check the login")
		}
		var next tea.Cmd
		m, next = updateWizard(t, m, cmd())
		if !seen && next == nil {
			t.Fatal("connect dialog stopped asking before a login was seen")
		}
	}
	check(false, nil)
	if m.loggedIn || m.loginUnreadable {
		t.Fatal("no login claimed one or claimed a read failure")
	}
	check(false, errors.New("journal unavailable"))
	if m.loggedIn || !m.loginUnreadable {
		t.Fatal("an unreadable log claimed a login or hid the failure")
	}
	check(true, errors.New("journal unavailable"))
	if m.loggedIn {
		t.Fatal("a failed read claimed a login")
	}
	check(false, nil)
	if m.loginUnreadable {
		t.Fatal("a later good read kept the failure")
	}
	check(true, nil)
	if !m.loggedIn {
		t.Fatal("a seen login was not shown")
	}
}

// Root can no longer log in over SSH once the install is done, so leaving
// before a login with an untried password can lock the owner out. Before a
// login, Exit and ctrl+c ask once, after a login they leave at once.
func TestWizardExitAsksOnceBeforeLoginOnly(t *testing.T) {
	right := tea.KeyPressMsg{Code: tea.KeyRight}
	for _, key := range []tea.KeyPressMsg{enter(), ctrlC()} {
		// asked reaches the connect dialog and presses key once, which asks.
		asked := func() wizardModel {
			m, _, _ := wizardFixtureWithLogin()
			m = reachConnect(t, m)
			m, cmd := updateWizard(t, m, key)
			assertStays(t, cmd)
			return m
		}

		// Wait is selected first and goes back to the dialog, where right
		// selects nothing and the key asks again. A second ctrl+c then leaves.
		m, cmd := updateWizard(t, asked(), enter())
		assertStays(t, cmd)
		m, _ = updateWizard(t, m, right)
		m, cmd = updateWizard(t, m, key)
		assertStays(t, cmd)
		_, cmd = updateWizard(t, m, ctrlC())
		assertQuit(t, cmd)

		// Exit leaves.
		m, _ = updateWizard(t, asked(), right)
		_, cmd = updateWizard(t, m, enter())
		assertQuit(t, cmd)

		// A second ctrl+c leaves.
		_, cmd = updateWizard(t, asked(), ctrlC())
		assertQuit(t, cmd)

		// A login while asking closes the question, and the key then leaves.
		m, _ = updateWizard(t, asked(), loginResultMsg{seen: true})
		_, cmd = updateWizard(t, m, key)
		assertQuit(t, cmd)
	}
}

// assertStays fails if cmd ends the program.
func assertStays(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	if _, ok := cmd().(tea.QuitMsg); ok {
		t.Fatal("left without asking")
	}
}

func TestWizardFailuresNeverReachConnect(t *testing.T) {
	failed := wizardProgressMsg{ok: true, event: installer.InstallEvent{Final: true, Result: installer.RunResult{Outcome: installer.RunFailed}}}
	unfinished := wizardProgressMsg{ok: true, event: installer.InstallEvent{Final: true, Result: installer.RunResult{Outcome: installer.RunComplete}, CompletionErr: errors.New("terminal ledger publication failed")}}
	closed := wizardProgressMsg{ok: false}
	for _, end := range []wizardProgressMsg{failed, unfinished, closed} {
		m, _, login := wizardFixtureWithLogin()
		m.phase = wzSteps
		m, _ = updateWizard(t, m, end)
		for range 1000 {
			m, _ = updateWizard(t, m, frameTick{})
			m, _ = updateWizard(t, m, loginTick{})
		}
		if m.phase == wzConnect || login.calls != 0 {
			t.Fatal("an install that did not finish reached the connect dialog")
		}
		_, cmd := updateWizard(t, m, ctrlC())
		assertQuit(t, cmd)
	}
	m, session := wizardFixture()
	_, cmd := updateWizard(t, m, ctrlC())
	assertQuit(t, cmd)
	if session.starts != 0 {
		t.Fatal("pre-install exit started work")
	}
}

func assertQuit(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("missing quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("exit command returned %T, want tea.QuitMsg", msg)
	}
}
