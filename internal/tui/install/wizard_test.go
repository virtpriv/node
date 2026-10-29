package install

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/installer"
	"github.com/virtpriv/node/internal/loginpassword"
)

// TestMain shortens the screen's own timers, so tests that follow them run
// in milliseconds.
func TestMain(m *testing.M) {
	frameEvery, typeEvery, finishHold = time.Millisecond, time.Millisecond, time.Millisecond
	os.Exit(m.Run())
}

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

func wizardFixture(t *testing.T) (wizardModel, *fakeSession) {
	m, session, _ := wizardFixtureWithLogin(t)
	return m, session
}

func wizardFixtureWithLogin(t *testing.T) (wizardModel, *fakeSession, *loginStub) {
	session := &fakeSession{events: make(chan installer.InstallEvent, 10)}
	info := installer.InstallView{NeedIdentity: true,
		Steps: []installer.InstallStepView{{Name: "First"}, {Name: "Second"}}}
	login := &loginStub{}
	return showDialog(t, newWizardModel(info, session, login.check)), session, login
}

// follow runs cmd and then the commands the screen itself asks for, one
// message at a time as Bubble Tea does, until done reports true or nothing
// is left to wait for. It reports whether the screen asked to quit, and
// fails the test when done never comes.
func follow(t *testing.T, m wizardModel, cmd tea.Cmd, done func(wizardModel) bool) (wizardModel, bool) {
	t.Helper()
	out := make(chan tea.Msg)
	pending := 0
	run := func(c tea.Cmd) {
		if c != nil {
			pending++
			go func() { out <- c() }()
		}
	}
	run(cmd)
	quit := false
	timeout := time.After(5 * time.Second)
	for done == nil || !done(m) {
		if pending == 0 {
			if done != nil {
				t.Fatal("the screen stopped before it got there")
			}
			return m, quit
		}
		select {
		case msg := <-out:
			pending--
			switch msg := msg.(type) {
			case tea.BatchMsg:
				for _, c := range msg {
					run(c)
				}
			case tea.QuitMsg:
				quit = true
			default:
				var next tea.Cmd
				m, next = updateWizard(t, m, msg)
				run(next)
			}
		case <-timeout:
			t.Fatal("the screen never got there")
		}
	}
	return m, quit
}

// take runs cmd as Bubble Tea would and returns its first message of type
// T, the one a test is about. The screen may batch it with its timers.
func take[T tea.Msg](t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	out := make(chan tea.Msg, 16)
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		go func() {
			msg := c()
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, b := range batch {
					run(b)
				}
				return
			}
			out <- msg
		}()
	}
	run(cmd)
	timeout := time.After(5 * time.Second)
	for {
		select {
		case msg := <-out:
			if _, ok := msg.(T); ok {
				return msg
			}
		case <-timeout:
			var want T
			t.Fatalf("no %T came", want)
			return nil
		}
	}
}

// showDialog runs the opening until the password dialog is on screen, as the
// owner sees it before typing.
func showDialog(t *testing.T, m wizardModel) wizardModel {
	t.Helper()
	m, cmd := updateWizard(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = follow(t, m, cmd, wizardModel.dialogShown)
	return m
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
			m, session := wizardFixture(t)
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
				if !m.starting || cmd == nil {
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
	m = showDialog(t, m)
	m, _ = updateWizard(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.pwInput.Value() != "x" {
		t.Fatal("input did not reach the shown dialog")
	}
}

func TestWizardPasswordMinimumMismatchAndRecovery(t *testing.T) {
	m, session := wizardFixture(t)
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
	if !m.starting || cmd == nil {
		t.Fatal("corrected password could not continue")
	}
}

func TestWizardStopWaitsForEngineResult(t *testing.T) {
	m, session := wizardFixture(t)
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
	m, wait = updateWizard(t, m, take[wizardProgressMsg](t, wait))
	if m.steps[0].Status != installer.StepDone || wait == nil {
		t.Fatal("admitted step result lost")
	}
	session.events <- installer.InstallEvent{Final: true, Result: installer.RunResult{Outcome: installer.RunInterrupted}}
	m, cmd = updateWizard(t, m, take[wizardProgressMsg](t, wait))
	assertQuit(t, cmd)
}

// A start the installer refuses, for example because the cache decision was
// not saved, returns to the password screen with the error and can be retried.
func TestWizardDecisionRetryAndCompletionFailure(t *testing.T) {
	m, session := wizardFixture(t)
	m.pwInput.SetValue("abcdefghijklmnop")
	m.pwConfirm.SetValue("abcdefghijklmnop")
	m.pwFocus = 2
	session.err = errors.New("cache write failed")
	m, cmd := updateWizard(t, m, enter())
	if m.phase != wzPassword || cmd == nil {
		t.Fatal("the start was not requested from the password dialog")
	}
	m, cmd = updateWizard(t, m, cmd())
	if m.phase != wzPassword || m.pwErr == "" || cmd != nil || !m.dialogShown() {
		t.Fatal("decision error not shown on the dialog")
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
	m, wait = updateWizard(t, m, take[wizardProgressMsg](t, wait))
	if m.steps[0].Status != installer.StepSkipped || wait == nil {
		t.Fatal("skip evidence lost")
	}
	failure := errors.New("terminal ledger publication failed")
	session.events <- installer.InstallEvent{Final: true, Result: installer.RunResult{Outcome: installer.RunComplete}, CompletionErr: failure}
	m, cmd = updateWizard(t, m, take[wizardProgressMsg](t, wait))
	if m, _ = follow(t, m, cmd, nil); m.phase != wzDone || m.completeErr != failure.Error() {
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
	m, cmd := updateWizard(t, m, wizardProgressMsg{ok: true, event: installer.InstallEvent{Final: true, Result: installer.RunResult{Outcome: installer.RunComplete}}})
	m, _ = follow(t, m, cmd, func(m wizardModel) bool { return m.phase == wzConnect })
	return m
}

func TestWizardCompletionWaitsForSSHLogin(t *testing.T) {
	m, _, login := wizardFixtureWithLogin(t)
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
			m, _, _ := wizardFixtureWithLogin(t)
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

		// After a login seen while asking, the same key leaves at once.
		m, _ = updateWizard(t, asked(), loginResultMsg{seen: true})
		_, cmd = updateWizard(t, m, key)
		assertQuit(t, cmd)
	}
}

// Once every step is done, no way out skips the question before a login:
// ctrl+c while the last title shows, or a pause asked for during the last
// step, which then has nothing left to pause.
func TestWizardCompleteInstallAsksBeforeLeaving(t *testing.T) {
	complete := wizardProgressMsg{ok: true, event: installer.InstallEvent{Final: true, Result: installer.RunResult{Outcome: installer.RunComplete}}}

	m, _, login := wizardFixtureWithLogin(t)
	m.phase = wzSteps
	m, _ = updateWizard(t, m, complete)
	m, check := updateWizard(t, m, ctrlC())
	if check == nil {
		t.Fatal("ctrl+c after the last step started no login check")
	}
	_, cmd := updateWizard(t, m, ctrlC())
	assertQuit(t, cmd)
	// The login check started there: its result, a login, lets the key
	// leave at once.
	login.seen = true
	m, _ = updateWizard(t, m, take[loginResultMsg](t, check))
	_, cmd = updateWizard(t, m, enter())
	assertQuit(t, cmd)

	m, _, _ = wizardFixtureWithLogin(t)
	m.phase = wzSteps
	m, _ = updateWizard(t, m, ctrlC())
	m, cmd = updateWizard(t, m, complete)
	m, quit := follow(t, m, cmd, func(m wizardModel) bool { return m.phase == wzConnect })
	if quit {
		t.Fatal("left without asking")
	}
	_, cmd = updateWizard(t, m, enter())
	assertStays(t, cmd)
}

// The finish title types itself with its own timer. Leaving it with ctrl+c
// before it is typed out must end that timer, or the connect dialog redraws
// for nothing as long as it is open.
func TestTitleTimerEndsWhenTheTitleLeavesTheScreen(t *testing.T) {
	m, _, _ := wizardFixtureWithLogin(t)
	m.phase = wzSteps
	m, _ = updateWizard(t, m, wizardProgressMsg{ok: true, event: installer.InstallEvent{Final: true, Result: installer.RunResult{Outcome: installer.RunComplete}}})
	m, _ = updateWizard(t, m, ctrlC())
	if _, next := updateWizard(t, m, typeTick{title: m.typed}); next != nil {
		t.Fatal("the title's timer went on after the title left the screen")
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
		m, _, login := wizardFixtureWithLogin(t)
		m.phase = wzSteps
		m, cmd := updateWizard(t, m, end)
		m, _ = follow(t, m, cmd, nil)
		m, _ = updateWizard(t, m, loginTick{})
		if m.phase == wzConnect || login.calls != 0 {
			t.Fatal("an install that did not finish reached the connect dialog")
		}
		_, cmd = updateWizard(t, m, ctrlC())
		assertQuit(t, cmd)
	}
	m, session := wizardFixture(t)
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

// The dialog stays until the start succeeds, so a failed start shows its
// error at once, whatever the start took. Keys wait for the start, except
// ctrl+c, which leaves as Exit does.
func TestWizardStartKeepsTheDialogUntilItSucceeds(t *testing.T) {
	start := func(t *testing.T) (wizardModel, *fakeSession, tea.Cmd) {
		t.Helper()
		m, session := wizardFixture(t)
		m.pwInput.SetValue("abcdefghijklmnop")
		m.pwConfirm.SetValue("abcdefghijklmnop")
		m.pwFocus = focusStart
		m, cmd := updateWizard(t, m, enter())
		if cmd == nil {
			t.Fatal("no start requested")
		}
		return m, session, cmd
	}

	t.Run("keys wait for the start", func(t *testing.T) {
		m, session, startCmd := start(t)
		for range 3 {
			m, _ = updateWizard(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
			if _, again := updateWizard(t, m, enter()); again != nil {
				t.Fatal("a second start was requested")
			}
		}
		if m.pwFocus != focusStart {
			t.Fatal("a key moved the focus during the start")
		}
		if !m.dialogShown() {
			t.Fatal("the dialog left the screen during the start")
		}
		m, wait := updateWizard(t, m, startCmd())
		if m.phase != wzSteps || wait == nil || session.starts != 1 {
			t.Fatal("a successful start did not move to the steps")
		}
	})

	t.Run("ctrl+c leaves during the start", func(t *testing.T) {
		m, _, _ := start(t)
		_, cmd := updateWizard(t, m, ctrlC())
		if cmd == nil {
			t.Fatal("ctrl+c did nothing")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatal("ctrl+c did not leave")
		}
	})
}

// A failed step's error and the step's name stay in view wherever the step
// is in the list. An error longer than the window keeps the name.
func TestWizardFailedStepErrorStaysInView(t *testing.T) {
	names := make([]installer.InstallStepView, 28)
	for i := range names {
		names[i].Name = fmt.Sprintf("Step %d of the install", i+1)
	}
	for _, words := range []int{8, 60, 400} {
		for at := range names {
			steps := append([]installer.InstallStepView(nil), names...)
			for i := range at {
				steps[i].Status = installer.StepDone
			}
			steps[at].Status = installer.StepFailed
			steps[at].Err = errors.New(strings.Repeat("cause ", words) + "LASTWORD")
			m := newWizardModel(installer.InstallView{Steps: steps}, &fakeSession{events: make(chan installer.InstallEvent)}, nil)
			m.phase, m.failed, m.drawn = wzDone, true, namesShown
			m, _ = updateWizard(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
			view := m.View().Content
			if !strings.Contains(view, steps[at].Name) {
				t.Fatalf("%d words at step %d: the step's name is not in view", words, at+1)
			}
			if words < 400 && !strings.Contains(view, "LASTWORD") {
				t.Fatalf("%d words at step %d: the end of the error is not in view", words, at+1)
			}
		}
	}
}
