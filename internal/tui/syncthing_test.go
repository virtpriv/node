package tui

import (
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/syncthing"
)

func syncModel(t *testing.T) (Model, *SyncthingDeviceScreen, *SyncthingDeviceScreen) {
	t.Helper()
	ctx := &ScreenContext{Cfg: config.Default(), State: &RuntimeState{}, Syncthing: app.NewSyncthing()}
	t.Cleanup(ctx.Syncthing.Close)
	a := NewSyncthingDeviceScreen(ctx, syncthing.Device{Name: "same", DeviceID: "A"})
	b := NewSyncthingDeviceScreen(ctx, syncthing.Device{Name: "same", DeviceID: "B"})
	m := Model{cfg: ctx.Cfg, state: ctx.State, screenCtx: ctx, nav: NewNavSidebar(), tabs: []openTab{
		{Kind: tabSyncthing, Section: secAddons, Screen: NewSyncthingDetailScreen(ctx)},
		{Kind: tabSyncthingDevice, Section: secAddons, Key: "A", Parent: tabSyncthing, Screen: a},
		{Kind: tabSyncthingDevice, Section: secAddons, Key: "B", Parent: tabSyncthing, Screen: b},
	}}
	m.nav.ActiveItem = secAddons
	ctx.State.SyncthingDevicesKnown = true
	ctx.State.SyncthingDevices = []syncthing.Device{{DeviceID: "A", Name: "same"}, {DeviceID: "B", Name: "same"}}
	return m, a, b
}
func TestSyncthingRemovalOwnsAttemptAndHiddenResult(t *testing.T) {
	m, a, b := syncModel(t)
	b.step = syncDeviceStepConfirm
	b.confirmIdx = 1
	_, first := b.HandleKey("enter", tea.KeyPressMsg{})
	_, second := b.HandleKey("enter", tea.KeyPressMsg{})
	if first == nil || second != nil {
		t.Fatal("repeated confirmation queued another removal")
	}
	m.nav.ActiveItem = secWallet
	result := syncthingRemovedMsg{owner: b, attempt: b.attempt, result: app.SyncthingResult{Outcome: app.SyncthingUnknown, Err: errors.New("removal unconfirmed")}}
	updated, _ := m.Update(result)
	m = updated.(Model)
	if b.removeError != "removal unconfirmed" || a.removeError != "" {
		t.Fatal("hidden result lost or misdelivered")
	}
	// A stale result cannot complete a later attempt.
	b.step = syncDeviceStepRemoving
	b.attempt++
	m.Update(result)
	if b.step != syncDeviceStepRemoving {
		t.Fatal("stale result changed new attempt")
	}
	result.attempt = b.attempt
	result.result = app.SyncthingResult{Outcome: app.SyncthingComplete}
	updated, _ = m.Update(result)
	m = updated.(Model)
	if b.step != syncDeviceStepRemoved {
		t.Fatal("hidden success lost")
	}
	if len(m.tabs) != 3 {
		t.Fatal("success silently removed result")
	}
	_, done := b.HandleKey("enter", tea.KeyPressMsg{})
	updated, _ = m.Update(done())
	m = updated.(Model)
	if len(m.tabs) != 2 || m.tabs[1].Screen != a || m.nav.ActiveSection() != secWallet {
		t.Fatal("Done closed another tab or changed section")
	}
}
func TestSyncthingDeviceTabsUseIDsAndProtectBusyParent(t *testing.T) {
	m, a, b := syncModel(t)
	other := NewSyncthingDeviceScreen(m.screenCtx, syncthing.Device{Name: "same", DeviceID: "C"})
	updated, _ := m.Update(openTabMsg{Kind: tabSyncthingDevice, Key: "C", Index: 0, Screen: other, Parent: tabSyncthing})
	m = updated.(Model)
	if len(m.tabs) != 4 {
		t.Fatal("row zero reused another device tab")
	}
	updated, _ = m.Update(openTabMsg{Kind: tabSyncthingDevice, Key: "B", Index: 9, Screen: b, Parent: tabSyncthing})
	m = updated.(Model)
	if len(m.tabs) != 4 {
		t.Fatal("reordered device duplicated tab")
	}
	a.step = syncDeviceStepRemoving
	for _, index := range []int{1, 2} {
		updated, _ = m.closeTab(index)
		if len(updated.(Model).tabs) != 4 {
			t.Fatal("busy device or parent closed")
		}
	}
}
func TestSyncthingPairPartialResultAndStaleDone(t *testing.T) {
	m, _, _ := syncModel(t)
	s := NewSyncthingPairScreen(m.screenCtx)
	s.step = syncPairStepPairing
	s.attempt = 1
	m.tabs = append(m.tabs, openTab{Kind: tabSyncthingPair, Section: secAddons, Parent: tabSyncthing, Screen: s})
	m.nav.ActiveItem = secWallet
	result := syncthingPairedMsg{owner: s, attempt: 1, result: app.SyncthingResult{Outcome: app.SyncthingPartial, Err: errors.New("device configured; share incomplete")}}
	updated, _ := m.Update(result)
	m = updated.(Model)
	if s.step != syncPairStepResult || s.pairError == "" {
		t.Fatal("partial pair returned to ordinary input")
	}
	oldDone := closeSyncthingCmd(s, s.attempt)()
	s.attempt++
	s.step = syncPairStepPairing
	m.Update(result)
	if s.step != syncPairStepPairing {
		t.Fatal("old result completed newer pair")
	}
	s.step = syncPairStepResult
	updated, _ = m.Update(oldDone)
	if len(updated.(Model).tabs) != 4 {
		t.Fatal("old Done closed newer completed attempt")
	}
}
func TestSyncthingRefreshKeepsIdentityAndRejectsOlderRead(t *testing.T) {
	m, _, _ := syncModel(t)
	detail := m.tabs[0].Screen.(*SyncthingDetailScreen)
	m.state.SyncthingDevicesKnown = true
	m.state.SyncthingDevices = []syncthing.Device{{DeviceID: "A"}, {DeviceID: "B"}}
	detail.focusZone = syncDetailZoneList
	detail.cursor = 1
	m.screenCtx.syncthingRevision = 2
	m.screenCtx.syncthingActive = 2
	fresh := syncthingDevicesMsg{owner: m.screenCtx, revision: 2, devices: []syncthing.Device{{DeviceID: "B"}, {DeviceID: "A"}}}
	m.Update(fresh)
	if detail.cursor != 0 {
		t.Fatal("selection changed identity on reorder")
	}
	m.Update(syncthingDevicesMsg{owner: m.screenCtx, revision: 1, err: errors.New("late read")})
	if !m.state.SyncthingDevicesKnown || m.state.SyncthingDevices[0].DeviceID != "B" {
		t.Fatal("older read overwrote current list")
	}
	m.screenCtx.syncthingRevision++
	fresh.revision++
	m.screenCtx.syncthingActive = fresh.revision
	fresh.devices = []syncthing.Device{{DeviceID: "A"}}
	m.Update(fresh)
	if detail.focusZone != syncDetailZoneButtons {
		t.Fatal("missing selection silently substituted another device")
	}
}

func TestSyncthingRefreshCoalescesAndRejectsPreMutationRead(t *testing.T) {
	for _, operation := range []string{"pair", "remove"} {
		t.Run(operation, func(t *testing.T) {
			m, device, _ := syncModel(t)
			if m.admitSyncthing() == nil {
				t.Fatal("initial observation not admitted")
			}
			old := m.screenCtx.syncthingActive
			for range 3 {
				if m.admitSyncthing() != nil {
					t.Fatal("overlapping observation admitted")
				}
			}
			var mutation tea.Cmd
			var result tea.Msg
			if operation == "pair" {
				pair := NewSyncthingPairScreen(m.screenCtx)
				pair.input.SetValue("REMOTE")
				pair.focusZone, pair.btnIdx = syncPairZoneButtons, 1
				m.tabs = append(m.tabs, openTab{Kind: tabSyncthingPair, Section: secAddons, Parent: tabSyncthing, Screen: pair})
				_, mutation = pair.HandleKey("enter", tea.KeyPressMsg{})
				result = syncthingPairedMsg{owner: pair, attempt: pair.attempt, result: app.SyncthingResult{Outcome: app.SyncthingComplete}}
			} else {
				device.step, device.confirmIdx = syncDeviceStepConfirm, 1
				_, mutation = device.HandleKey("enter", tea.KeyPressMsg{})
				result = syncthingRemovedMsg{owner: device, attempt: device.attempt, result: app.SyncthingResult{Outcome: app.SyncthingComplete}}
			}
			// Leave the backend command unexecuted; deliver its result below.
			if mutation == nil || m.state.SyncthingDevicesKnown {
				t.Fatal("mutation did not start or left its old observation current")
			}
			_, refresh := m.Update(result)
			if refresh == nil {
				t.Fatal("mutation completion lost the follow-up observation")
			}
			m.Update(refresh())
			if m.screenCtx.syncthingActive != old {
				t.Fatal("mutation completion overlapped the outstanding observation")
			}
			_, follow := m.Update(syncthingDevicesMsg{owner: m.screenCtx, revision: old,
				devices: []syncthing.Device{{DeviceID: "STALE"}}})
			if follow == nil || m.state.SyncthingDevicesKnown {
				t.Fatal("pre-mutation read became current or follow-up was lost")
			}
			current := m.screenCtx.syncthingActive
			if current == 0 || current == old {
				t.Fatal("mutation completion did not request a fresh observation")
			}
			_, follow = m.Update(syncthingDevicesMsg{owner: m.screenCtx, revision: old})
			if follow != nil || m.screenCtx.syncthingActive != current {
				t.Fatal("late duplicate result disturbed active read")
			}
			_, follow = m.Update(syncthingDevicesMsg{owner: m.screenCtx, revision: current,
				devices: []syncthing.Device{{DeviceID: "CURRENT"}}})
			if follow != nil {
				t.Fatal("coalesced reads produced an unnecessary follow-up")
			}
			if !m.state.SyncthingDevicesKnown || m.state.SyncthingDevices[0].DeviceID != "CURRENT" {
				t.Fatal("fresh result not published")
			}
		})
	}
}

func TestSyncthingDeviceDetailsFollowCurrentIdentityAndReadFailures(t *testing.T) {
	m, a, _ := syncModel(t)
	a.viewBtnIdx = 1
	m.state.SyncthingDevices = []syncthing.Device{
		{DeviceID: "B", Name: "other"},
		{DeviceID: "A", Name: "renamed", BackupKnown: true, BackupShared: true},
	}
	view := a.View(90, 30)
	if !strings.Contains(view, "renamed") || !strings.Contains(view, "Configured") {
		t.Fatal("detail retained an old name or lost current backup membership")
	}
	m.state.SyncthingDevices = m.state.SyncthingDevices[:1]
	if !strings.Contains(a.View(90, 30), "no longer configured") {
		t.Fatal("removed device still appears current")
	}
	if _, cmd := a.HandleKey("enter", tea.KeyPressMsg{}); cmd != nil || a.step != syncDeviceStepDetail {
		t.Fatal("missing identity advanced to removal of another row")
	}
	m.admitSyncthing()
	m.completeSyncthing(syncthingDevicesMsg{owner: m.screenCtx, revision: m.screenCtx.syncthingActive, err: errors.New("offline")})
	view = m.tabs[0].Screen.View(90, 30)
	if strings.Contains(view, "Devices (0)") || !strings.Contains(view, "unavailable") || !strings.Contains(a.View(90, 30), "unavailable") {
		t.Fatal("failed observation was presented as an empty list or current detail")
	}
}

func TestSyncthingPollingRefreshesOpenTabsAndStopsWhenClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, _, _ := syncModel(t)
		cmd := m.scheduleSyncthingPoll()
		if cmd == nil || m.scheduleSyncthingPoll() != nil {
			t.Fatal("polling did not keep one timer")
		}
		msg := cmd()
		_, next := m.Update(msg)
		if next == nil || m.screenCtx.syncthingActive == 0 || !m.screenCtx.syncthingPolling {
			t.Fatal("open screen did not request a refresh and rearm polling")
		}
		if batch, ok := next().(tea.BatchMsg); !ok || len(batch) != 2 {
			t.Fatal("refresh and next timer were not both returned for execution")
		}
		if m.scheduleSyncthingPoll() != nil {
			t.Fatal("open screen scheduled a duplicate timer")
		}
		m.Update(syncthingDevicesMsg{owner: m.screenCtx, revision: m.screenCtx.syncthingActive})
		m.tabs = nil
		_, next = m.Update(msg)
		if next != nil || m.screenCtx.syncthingActive != 0 || m.screenCtx.syncthingPolling {
			t.Fatal("closed screens continued polling")
		}
	})
}
