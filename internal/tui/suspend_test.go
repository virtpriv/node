package tui

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// ctrl+z suspends only when a shell can take the TUI back. Started as the
// session's own process group (from the login file without job control, or
// as a single command over ssh), a stopped TUI would wait forever.
func TestSuspendOnlyWhenAShellCanResume(t *testing.T) {
	saved := processGroups
	t.Cleanup(func() { processGroups = saved })
	for _, tc := range []struct {
		name     string
		pgid     int
		sid      int
		err      error
		suspends bool
	}{
		{"a job of a shell", 210, 200, nil, true},
		{"the session's own group", 200, 200, nil, false},
		{"unknown", 0, 0, errors.New("no answer"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			processGroups = func() (int, int, error) { return tc.pgid, tc.sid, tc.err }
			m := Model{nav: NewNavSidebar()}
			_, cmd := m.Update(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
			suspended := false
			if cmd != nil {
				_, suspended = cmd().(tea.SuspendMsg)
			}
			if suspended != tc.suspends {
				t.Fatalf("suspended %v, want %v", suspended, tc.suspends)
			}
		})
	}
}

func TestExitKey(t *testing.T) {
	m := Model{nav: NewNavSidebar()}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("lost normal exit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("normal exit did not request quit")
	}
}
