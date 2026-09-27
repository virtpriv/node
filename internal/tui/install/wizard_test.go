package install

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/virtualprivatenode/vpn/internal/installer"
	"github.com/virtualprivatenode/vpn/internal/loginpassword"
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
func wizardFixture() (wizardModel, *fakeSession) {
	session := &fakeSession{events: make(chan installer.InstallEvent, 10)}
	info := installer.InstallView{NeedIdentity: true,
		DBCacheChoices: []int{512, 1024, 2048}, RecommendedDBCache: 1024,
		Steps: []installer.InstallStepView{{Name: "First"}, {Name: "Second"}}}
	return newWizardModel(info, session), session
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
		{"unicode byte minimum", strings.Repeat("é", 8), strings.Repeat("é", 8)},
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

func TestWizardDecisionRetryAndCompletionFailure(t *testing.T) {
	m, session := wizardFixture()
	m.phase = wzSteps
	session.err = errors.New("cache write failed")
	m.info.NeedHardware = true
	m, cmd := updateWizard(t, m, m.Init()())
	if m.phase != wzHardware || m.hwErr == "" || cmd != nil {
		t.Fatal("decision error not recoverable")
	}
	session.err = nil
	m.hwFocus = 1
	m.hwBtn = len(m.hwButtons()) - 1
	m, cmd = updateWizard(t, m, enter())
	if cmd == nil {
		t.Fatal("retry not scheduled")
	}
	m, wait := updateWizard(t, m, cmd())
	if wait == nil || session.starts != 2 || session.input.DBCacheMB != 1024 {
		t.Fatal("retry did not submit selected cache")
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
	if cmd != nil || m.openConsole {
		t.Fatal("failed finalization opened console")
	}
}

func TestWizardCompletionChoiceAndEarlyExit(t *testing.T) {
	for _, open := range []bool{false, true} {
		t.Run(map[bool]string{false: "exit", true: "console"}[open], func(t *testing.T) {
			m, _ := wizardFixture()
			m.phase = wzSteps
			m, cmd := updateWizard(t, m, wizardProgressMsg{ok: true, event: installer.InstallEvent{Final: true, Result: installer.RunResult{Outcome: installer.RunComplete}}})
			if cmd != nil || m.phase != wzDone {
				t.Fatal("completion did not wait for operator choice")
			}
			key := ctrlC()
			if open {
				key = enter()
			}
			m, cmd = updateWizard(t, m, key)
			assertQuit(t, cmd)
			if m.openConsole != open {
				t.Fatal("completion choice lost")
			}
		})
	}
	m, session := wizardFixture()
	m, cmd := updateWizard(t, m, ctrlC())
	assertQuit(t, cmd)
	if session.starts != 0 || m.openConsole {
		t.Fatal("pre-install exit started work")
	}
}

func TestWizardObservationFailureRefusesHandoff(t *testing.T) {
	m, _ := wizardFixture()
	m.phase = wzSteps
	m, cmd := updateWizard(t, m, wizardProgressMsg{ok: false})
	if m.phase != wzDone || m.completeErr == "" || cmd != nil {
		t.Fatal("closed stream claimed success")
	}
	m, cmd = updateWizard(t, m, enter())
	if cmd != nil || m.openConsole {
		t.Fatal("missing completion permitted console handoff")
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
