package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/syncthing"
	"github.com/virtpriv/node/internal/theme"
)

// ── SyncthingDeviceScreen ──────────────────────────────
// Device detail with Cancel / Remove buttons, plus
// confirm step. Current details are resolved by stable device identity.

type syncDeviceStep int

const (
	syncDeviceStepDetail syncDeviceStep = iota
	syncDeviceStepConfirm
	syncDeviceStepRemoving
	syncDeviceStepRemoved
)

type SyncthingDeviceScreen struct {
	ctx         *ScreenContext
	step        syncDeviceStep
	deviceID    string // reviewed identity; never select a replacement by row
	attempt     uint64
	viewBtnIdx  int // 0=Cancel, 1=Remove
	confirmIdx  int // 0=Go Back, 1=Remove
	removeError string
}

func NewSyncthingDeviceScreen(
	ctx *ScreenContext,
	device syncthing.Device,
) *SyncthingDeviceScreen {
	return &SyncthingDeviceScreen{
		ctx:      ctx,
		deviceID: device.DeviceID,
	}
}

// ── Screen interface ────────────────────────────────────

func (s *SyncthingDeviceScreen) Init() tea.Cmd {
	return requestSyncthingDevicesCmd(s.ctx)
}

func (s *SyncthingDeviceScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	switch s.step {
	case syncDeviceStepRemoving:
		if keyStr == "ctrl+c" {
			return s, tea.Quit
		}
		if keyStr == "left" {
			return s, emitFocusSidebar
		}
		if keyStr == "up" || keyStr == "shift+tab" {
			return s, emitFocusTabBar
		}
		return s, nil
	case syncDeviceStepRemoved:
		if keyStr == "enter" {
			return s, closeSyncthingCmd(s, s.attempt)
		}
		if keyStr == "ctrl+c" {
			return s, tea.Quit
		}
		if keyStr == "left" {
			return s, emitFocusSidebar
		}
		return s, nil
	case syncDeviceStepDetail:
		return s.handleDetailKey(keyStr)
	case syncDeviceStepConfirm:
		return s.handleConfirmKey(keyStr)
	}
	return s, nil
}

func (s *SyncthingDeviceScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tabActivatedMsg:
		return s, requestSyncthingDevicesCmd(s.ctx)
	case syncthingRemovedMsg:
		if msg.owner != s || msg.attempt != s.attempt || s.step != syncDeviceStepRemoving {
			return s, nil
		}
		if msg.result.Outcome != app.SyncthingComplete {
			s.removeError = "Removal was not completed."
			if msg.result.Err != nil {
				s.removeError = msg.result.Err.Error()
			}
			s.step = syncDeviceStepDetail
			return s, nil
		}
		s.step = syncDeviceStepRemoved
		s.removeError = ""

	}
	return s, nil
}

func (s *SyncthingDeviceScreen) View(
	w, h int,
) string {
	switch s.step {
	case syncDeviceStepRemoving, syncDeviceStepRemoved:
		p := newPane(w)
		title, button := "Removing Device...", "Removing..."
		if s.step == syncDeviceStepRemoved {
			title, button = "Device Removed", "Done"
		}
		p.title(theme.Header, title)
		p.monoWrap(s.deviceID)
		return p.renderWithBottomButtons([]string{button}, 0, s.ctx.ContentFocused && s.step == syncDeviceStepRemoved, h)
	case syncDeviceStepDetail:
		return s.viewDetail(w, h)
	case syncDeviceStepConfirm:
		return s.viewConfirm(w, h)
	}
	return ""
}

func (s *SyncthingDeviceScreen) HelpBindings() []key.Binding {
	switch s.step {
	case syncDeviceStepRemoving:
		return []key.Binding{kSidebar, kShiftTabBar, kQuit}
	case syncDeviceStepRemoved:
		return tabButtonBindings(s.ctx.HasTabs)
	case syncDeviceStepDetail:
		return detailActionBindings(
			"remove", s.viewBtnIdx, s.ctx.HasTabs)
	case syncDeviceStepConfirm:
		return tabButtonBindings(s.ctx.HasTabs)
	}
	return nil
}

// ── Detail step ─────────────────────────────────────────
// Read-only info with Cancel / Remove buttons. Cancel
// closes the tab; Remove advances to confirm step.

func (s *SyncthingDeviceScreen) handleDetailKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.viewBtnIdx > 0 {
			s.viewBtnIdx--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right":
		if s.viewBtnIdx < 1 {
			s.viewBtnIdx++
		}
		return s, nil
	case "up", "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "down", "tab":
		return s, nil
	case "backspace":
		return s, emitFocusParent
	case "enter":
		if s.viewBtnIdx == 0 {
			return s, closeSyncthingCmd(s, s.attempt)
		}
		if _, ok := s.ctx.syncthingDevice(s.deviceID); !ok {
			s.removeError = "Current device configuration is unavailable or the device is no longer configured."
			return s, nil
		}
		s.step = syncDeviceStepConfirm
		s.confirmIdx = 0
		s.removeError = ""
		return s, nil
	}
	return s, nil
}

func (s *SyncthingDeviceScreen) viewDetail(
	w, h int,
) string {
	dev, found := s.ctx.syncthingDevice(s.deviceID)
	p := newPane(w)
	title := "Syncthing Device"
	if found {
		title = dev.Name
	}
	p.title(theme.Header, title)

	p.labelLine("Device ID:")
	p.monoWrap(dev.DeviceID)
	if !s.ctx.State.SyncthingDevicesKnown {
		p.warnWrapWords("Current configuration unavailable. Retrying automatically.")
	} else if !found {
		p.warnWrapWords("This device is no longer configured.")
	} else {
		p.labelLine("Backup sharing: " + backupSharingText(dev))
		p.dim("Sharing configuration does not confirm backup delivery.")
	}
	if s.ctx.syncthingActive != 0 {
		p.dim("Refreshing current configuration...")
	}
	if s.removeError != "" {
		p.warnWrapWords(s.removeError)
	}

	return p.renderWithBottomButtons(
		[]string{"Cancel", "Remove"}, s.viewBtnIdx,
		s.ctx.ContentFocused, h)
}

// ── Confirm step ────────────────────────────────────────

func (s *SyncthingDeviceScreen) handleConfirmKey(
	keyStr string,
) (Screen, tea.Cmd) {
	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.confirmIdx > 0 {
			s.confirmIdx--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right":
		if s.confirmIdx < 1 {
			s.confirmIdx++
		}
		return s, nil
	case "up", "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "down", "tab":
		return s, nil
	case "backspace":
		s.step = syncDeviceStepDetail
		return s, nil
	case "enter":
		switch s.confirmIdx {
		case 0: // Go Back
			s.step = syncDeviceStepDetail
			return s, nil
		case 1: // Remove
			if _, ok := s.ctx.syncthingDevice(s.deviceID); !ok {
				s.removeError = "Current device configuration is unavailable or the device is no longer configured."
				s.step = syncDeviceStepDetail
				return s, nil
			}
			s.attempt++
			s.step = syncDeviceStepRemoving
			return s, removeSyncthingDeviceCmd(s)
		}
	}
	return s, nil
}

func (s *SyncthingDeviceScreen) viewConfirm(
	w, h int,
) string {
	p := newPane(w)
	dev, found := s.ctx.syncthingDevice(s.deviceID)
	name := "device"
	if found {
		name = dev.Name
	}
	p.title(theme.Warning, "Remove "+name+"?")
	if !found {
		p.warnWrapWords("Current device configuration is unavailable or the device is no longer configured.")
	}
	p.monoWrap(s.deviceID)
	p.line(" " + theme.Value.Render(
		"• Stop syncing channel backups"+
			" to this device"))
	p.line(" " + theme.Value.Render(
		"• Remove device from Syncthing"))
	p.line(" " + theme.Value.Render(
		"• Does not delete data on the"+
			" remote device"))

	return p.renderWithBottomButtons(
		[]string{"Go Back", "Remove"},
		s.confirmIdx,
		s.ctx.ContentFocused, h)
}
