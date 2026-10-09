package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/syncthing"
)

type refreshSyncthingMsg struct{ owner *ScreenContext }
type syncthingPollMsg struct{ owner *ScreenContext }

func (c *ScreenContext) invalidateSyncthing() {
	c.syncthingRevision++
	c.State.SyncthingDevicesKnown = false
	c.State.SyncthingDeliveryKnown = false
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
// and requests one follow-up. It never launches a competing observation.
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
		o := runtime.Observe()
		return syncthingDevicesMsg{owner: ctx, revision: revision, devices: o.Devices, err: o.DevicesErr,
			delivery: o.Delivery, deliveryErr: o.DeliveryErr, copy: o.Copy, copyErr: o.CopyErr}
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
		ctx.State.SyncthingDelivery = msg.delivery
		ctx.State.SyncthingDeliveryErr = msg.deliveryErr
		ctx.State.SyncthingDeliveryKnown = msg.err == nil && msg.deliveryErr == nil
		ctx.State.SyncthingCopy = msg.copy
		ctx.State.SyncthingCopyErr = msg.copyErr
		ctx.State.SyncthingCopyKnown = msg.copyErr == nil
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

// deliveryText is the Backup column and the device tab's delivery line.
func deliveryText(state *RuntimeState, id string) string {
	if !state.SyncthingDeliveryKnown {
		if state.SyncthingDeliveryErr != nil {
			return "Unavailable"
		}
		return ""
	}
	d, ok := state.SyncthingDelivery[id]
	if !ok {
		return "Unavailable"
	}
	switch d.State {
	case syncthing.DeliveryUpToDate:
		return "Up to date"
	case syncthing.DeliveryReceiving:
		return fmt.Sprintf("Receiving %d%%", d.Percent)
	case syncthing.DeliveryNotConnected:
		return "Not connected"
	case syncthing.DeliveryNotAccepted:
		return "Not accepted"
	case syncthing.DeliveryPaused:
		return "Paused"
	case syncthing.DeliveryChecking:
		return "Checking"
	case syncthing.DeliveryOutOfSync:
		return "Out of sync"
	default:
		return "Not shared"
	}
}

// backupCopyLine is the Syncthing screen's line about the node's own copy.
// It is empty until the first read. Warnings come back with warn set. The
// record is judged at now, so a pause in reading cannot keep it current.
func backupCopyLine(state *RuntimeState, now time.Time) (text string, warn bool) {
	if !state.SyncthingCopyKnown {
		if state.SyncthingCopyErr != nil {
			return "Backup copy: unavailable", true
		}
		return "", false
	}
	c := state.SyncthingCopy.Judged(now)
	at := clockOrDate(c.At, now)
	switch c.State {
	case app.BackupCopyCurrent:
		return "Backup copy: up to date, checked " + at, false
	case app.BackupCopyFailed:
		if c.At.IsZero() {
			return "Backup copy: last check failed. See journalctl -u lnd-backup-export", true
		}
		return "Backup copy: last check failed at " + at + ". See journalctl -u lnd-backup-export", true
	case app.BackupCopyLate:
		if c.At.IsZero() {
			return "Backup copy: checks are late", true
		}
		return "Backup copy: checks are late, last at " + at, true
	case app.BackupCopyChecking:
		return "Backup copy: checking now", false
	case app.BackupCopyStuck:
		return "Backup copy: a check has been running since " + at, true
	case app.BackupCopyNotYet:
		return "Backup copy: not checked since boot", false
	case app.BackupCopyOff:
		return "Backup copy: checks are off. Start them: sudo systemctl enable --now lnd-backup-check.timer", true
	default:
		return "Backup copy: no minute check on this node", true
	}
}

// clockOrDate shows the time alone for today and adds the date otherwise.
func clockOrDate(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04:05")
	}
	return t.Format("2 Jan 15:04")
}

// lastSeenText says whether the device is connected or when it last was.
func lastSeenText(state *RuntimeState, id string, now time.Time) string {
	if !state.SyncthingDeliveryKnown {
		return ""
	}
	d, ok := state.SyncthingDelivery[id]
	switch {
	case !ok:
		return ""
	case d.Connected:
		return "Connected now"
	case d.LastSeen.IsZero():
		return "Never seen"
	}
	return "Last seen " + clockOrDate(d.LastSeen, now)
}
