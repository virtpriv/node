package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/update/protocol"
)

func TestRelaunchEndsTheTUIAskingForTheNewVersion(t *testing.T) {
	client := &nodeUpdateClientStub{status: protocol.Status{ID: strings.Repeat("a", 64), Version: "0.7.1", Phase: "complete"}}
	s := NewNodeUpdateScreen(&ScreenContext{NodeUpdates: client, Version: "0.7.0"})
	s.HandleMsg(s.Init()())
	s.button = slices.Index(s.buttons(), "Relaunch")
	if s.button < 0 {
		t.Fatal("a finished update of a newer version offers no Relaunch")
	}
	_, cmd := s.HandleKey("enter", tea.KeyPressMsg{})
	if cmd == nil {
		t.Fatal("Relaunch did nothing")
	}
	if _, ok := cmd().(relaunchMsg); !ok {
		t.Fatal("Relaunch did not ask for the new version")
	}
	next, cmd := Model{}.Update(relaunchMsg{})
	if !next.(Model).relaunch || cmd == nil {
		t.Fatal("the TUI did not end with a relaunch request")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("the TUI did not end after Relaunch")
	}
	_, cmd = s.HandleKey("ctrl+c", tea.KeyPressMsg{})
	if _, ok := cmd().(relaunchMsg); ok {
		t.Fatal("ctrl+c asked for a relaunch")
	}
}

func TestRelaunchStartsTheInstalledProgramOnlyWhenAsked(t *testing.T) {
	var started []string
	start := func(path string, argv, env []string) error {
		started = append(started, path)
		return nil
	}
	if err := finishRun(Model{}, start); err != nil || started != nil {
		t.Fatal("a TUI that was not asked to relaunch started a program")
	}
	if err := finishRun(Model{relaunch: true}, start); err != nil || !slices.Equal(started, []string{paths.BinaryPath}) {
		t.Fatalf("relaunch started %v, want %s", started, paths.BinaryPath)
	}
	refused := errors.New("exec format error")
	failing := func(string, []string, []string) error { return refused }
	if err := finishRun(Model{relaunch: true}, failing); !errors.Is(err, refused) {
		t.Fatal("a failed relaunch was not reported")
	}
}

func TestRelaunchOnlyOpensANewerVersion(t *testing.T) {
	// A finished update of an older release than the running one, for
	// example after a newer install by hand, has nothing newer to open.
	client := &nodeUpdateClientStub{status: protocol.Status{ID: strings.Repeat("a", 64), Version: "0.7.1", Phase: "complete"}}
	s := NewNodeUpdateScreen(&ScreenContext{NodeUpdates: client, Version: "0.7.2"})
	s.HandleMsg(s.Init()())
	if slices.Contains(s.buttons(), "Relaunch") {
		t.Fatal("Relaunch offered to open an older version")
	}
}
