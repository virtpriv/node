package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSuspendAndExitKeys(t *testing.T) {
	m := Model{nav: NewNavSidebar()}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+z did not request suspension")
	}
	if _, ok := cmd().(tea.SuspendMsg); !ok {
		t.Fatal("ctrl+z did not request suspension")
	}
	_, cmd = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("lost normal exit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("normal exit did not request quit")
	}
}
