package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/syncthing"
)

type refreshSyncthingMsg struct{ owner *ScreenContext }
type syncthingPollMsg struct{ owner *ScreenContext }

func (c *ScreenContext) invalidateSyncthing() {
	c.syncthingRevision++
	c.State.SyncthingDevicesKnown = false
}

func (m Model) hasSyncthingObservers() bool {
	if m.cfg.SyncthingEnabled && m.nav.ActiveSection() == secAddons {
		return true
	}
	for _, tab := range m.tabs {
		if tab.Kind == tabSyncthing || tab.Kind == tabSyncthingDevice {
			return true
		}
	}
	return false
}

func (m Model) syncthingMutationActive() bool {
	for _, tab := range m.tabs {
		if syncthingBusy(tab.Screen) {
			return true
		}
	}
	return false
}

func (m Model) scheduleSyncthingPoll() tea.Cmd {
	if m.screenCtx.syncthingPolling || !m.hasSyncthingObservers() {
		return nil
	}
	m.screenCtx.syncthingPolling = true
	owner := m.screenCtx
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg {
		return syncthingPollMsg{owner: owner}
	})
}

// All triggers share one read. A change during that read invalidates its result
// and requests one follow-up; it never launches a competing observation.
func (m Model) admitSyncthing() tea.Cmd {
	ctx := m.screenCtx
	if ctx.syncthingActive != 0 || m.syncthingMutationActive() {
		ctx.syncthingPending = true
		return nil
	}
	ctx.syncthingPending = false
	ctx.syncthingRevision++
	revision := ctx.syncthingRevision
	ctx.syncthingActive = revision
	runtime := ctx.syncthing()
	return func() tea.Msg {
		devices, err := runtime.ListDevices()
		return syncthingDevicesMsg{owner: ctx, revision: revision, devices: devices, err: err}
	}
}

func (m Model) completeSyncthing(msg syncthingDevicesMsg) tea.Cmd {
	ctx := m.screenCtx
	if msg.owner != ctx || ctx.syncthingActive == 0 || msg.revision != ctx.syncthingActive {
		return nil
	}
	ctx.syncthingActive = 0
	if msg.revision == ctx.syncthingRevision && !m.syncthingMutationActive() {
		for _, tab := range m.tabs {
			if detail, ok := tab.Screen.(*SyncthingDetailScreen); ok {
				detail.HandleMsg(msg)
			}
		}
		ctx.State.SyncthingDevices = msg.devices
		ctx.State.SyncthingDevicesErr = msg.err
		ctx.State.SyncthingDevicesKnown = msg.err == nil
		ctx.State.SyncthingDevicesChecked = time.Now()
		for i := range m.tabs {
			if m.tabs[i].Kind != tabSyncthingDevice {
				continue
			}
			device, found := ctx.syncthingDevice(m.tabs[i].Key)
			label := "Device unavailable"
			if found {
				label = device.Name
				if len(label) > 17 {
					label = label[:17] + "..."
				}
			}
			m.tabs[i].Label = label
		}
	} else {
		ctx.syncthingPending = true
	}
	if ctx.syncthingPending {
		return m.admitSyncthing()
	}
	return nil
}

func (c *ScreenContext) syncthingDevice(id string) (syncthing.Device, bool) {
	if c.State.SyncthingDevicesKnown {
		for _, device := range c.State.SyncthingDevices {
			if device.DeviceID == id {
				return device, true
			}
		}
	}
	return syncthing.Device{DeviceID: id}, false
}

func backupSharingText(device syncthing.Device) string {
	if !device.BackupKnown {
		return "Unavailable"
	}
	if device.BackupShared {
		return "Configured"
	}
	return "Not configured"
}
