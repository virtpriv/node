package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/system"
)

type statusReadCall struct {
	result chan app.StatusSnapshot
}
type controlledStatusReader struct {
	calls chan statusReadCall
	done  <-chan struct{}
}

func (r *controlledStatusReader) Collect() app.StatusSnapshot {
	call := statusReadCall{result: make(chan app.StatusSnapshot, 1)}
	r.calls <- call
	select {
	case result := <-call.result:
		return result
	case <-r.done:
		return app.StatusSnapshot{}
	}
}
func (*controlledStatusReader) Close() {}
func statusModelFixture(t *testing.T) (Model, *controlledStatusReader) {
	t.Helper()
	ctx := &ScreenContext{State: &RuntimeState{}}
	r := &controlledStatusReader{calls: make(chan statusReadCall, 4), done: t.Context().Done()}
	return Model{state: ctx.State, screenCtx: ctx, statusCollector: r, nav: NewNavSidebar()}, r
}
func startStatusCommand(t *testing.T, cmd tea.Cmd, r *controlledStatusReader) (statusReadCall, <-chan tea.Msg) {
	t.Helper()
	if cmd == nil {
		t.Fatal("refresh was not scheduled")
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case call := <-r.calls:
		return call, result
	case <-time.After(time.Second):
		t.Fatal("collector not reached")
		return statusReadCall{}, nil
	}
}
func statusUpdate(m *Model, msg tea.Msg) tea.Cmd {
	updated, cmd := m.Update(msg)
	*m = updated.(Model)
	return cmd
}
func freshStatus[T any](v T) app.Observation[T] {
	return app.Observation[T]{Value: v, ObservedAt: time.Now()}
}

func diskStatus(used uint64) app.StatusSnapshot {
	return app.StatusSnapshot{Disk: freshStatus(system.DiskInfo{UsedBytes: used, TotalBytes: 80_000_000_000, Percent: "6%"})}
}

func TestStatusCoalescesAndRejectsReplayedCompletion(t *testing.T) {
	m, r := statusModelFixture(t)
	first, firstDone := startStatusCommand(t, statusUpdate(&m, refreshStatusMsg{}), r)
	for range 5 {
		if statusUpdate(&m, refreshStatusMsg{}) != nil {
			t.Fatal("overlapping refresh admitted a reader")
		}
	}
	first.result <- diskStatus(1_000_000_000)
	old := <-firstDone
	next := statusUpdate(&m, old)
	second, secondDone := startStatusCommand(t, next, r)
	if statusUpdate(&m, old) != nil || m.screenCtx.Status.Disk.Value.UsedBytes != 1_000_000_000 {
		t.Fatal("duplicate completion changed publication")
	}
	// A replay must not clear the second read's admission guard.
	if statusUpdate(&m, refreshStatusMsg{}) != nil {
		t.Fatal("old completion cleared the current guard")
	}
	second.result <- diskStatus(2_000_000_000)
	thirdCmd := statusUpdate(&m, <-secondDone)
	third, thirdDone := startStatusCommand(t, thirdCmd, r)
	third.result <- diskStatus(3_000_000_000)
	if statusUpdate(&m, <-thirdDone) != nil {
		t.Fatal("coalesced requests became an unbounded queue")
	}
	statusUpdate(&m, old)
	if m.screenCtx.Status.Disk.Value.UsedBytes != 3_000_000_000 {
		t.Fatal("older completion replaced the current reading")
	}
}

func TestStatusFailureRenderingAndRecovery(t *testing.T) {
	m, r := statusModelFixture(t)
	home := NewSystemHomeScreen(m.screenCtx)
	failed := errors.New("injected read failure")
	publish := func(s app.StatusSnapshot) {
		t.Helper()
		call, done := startStatusCommand(t, statusUpdate(&m, refreshStatusMsg{}), r)
		call.result <- s
		statusUpdate(&m, <-done)
	}
	publish(app.StatusSnapshot{Disk: app.Observation[system.DiskInfo]{Err: failed}})
	view := ansi.Strip(home.View(67, 31))
	if !strings.Contains(view, "Disk: unavailable") || strings.Contains(view, "under 1 MB") {
		t.Fatalf("unavailable disk became a fabricated size:\n%s", view)
	}
	publish(diskStatus(5_000_000_000))
	publish(app.StatusSnapshot{Disk: app.Observation[system.DiskInfo]{Err: failed}})
	view = ansi.Strip(home.View(67, 31))
	if !strings.Contains(view, "Disk: 5.0 GB / 80.0 GB (6%) (stale)") {
		t.Fatalf("last-good disk was not explicitly stale:\n%s", view)
	}
	publish(diskStatus(0))
	view = ansi.Strip(home.View(67, 31))
	if !strings.Contains(view, "Disk: under 1 MB / 80.0 GB (6%)") || strings.Contains(view, "stale") || !m.screenCtx.Status.Disk.Fresh() {
		t.Fatalf("successful zero observation failed to recover:\n%s", view)
	}
}
