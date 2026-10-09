package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/virtpriv/node/internal/theme"
)

// ── SyncthingDetailScreen ──────────────────────────────
// Main Syncthing manage tab: header with Pair Device /
// Web UI buttons, scrollable paired device table below.
// Reads the daemon-observed device list through shared runtime state.

const (
	syncDetailZoneButtons = 0
	syncDetailZoneList    = 1
)

type SyncthingDetailScreen struct {
	ctx       *ScreenContext
	btnIdx    int // 0=Pair Device, 1=Web UI
	focusZone int // 0=buttons, 1=device list
	cursor    int // position in device list
}

func NewSyncthingDetailScreen(
	ctx *ScreenContext,
) *SyncthingDetailScreen {
	return &SyncthingDetailScreen{
		ctx: ctx,
	}
}

// ── Screen interface ────────────────────────────────────

func (s *SyncthingDetailScreen) Init() tea.Cmd {
	return requestSyncthingDevicesCmd(s.ctx)
}

func (s *SyncthingDetailScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	devices := s.ctx.State.SyncthingDevices
	s.clampCursor()

	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.focusZone == syncDetailZoneButtons &&
			s.btnIdx > 0 {
			s.btnIdx--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right":
		if s.focusZone == syncDetailZoneButtons &&
			s.btnIdx < 1 {
			s.btnIdx++
		}
		return s, nil
	case "up":
		if s.focusZone == syncDetailZoneList {
			if s.cursor > 0 {
				s.cursor--
			} else {
				s.focusZone = syncDetailZoneButtons
				s.btnIdx = 0
			}
			return s, nil
		}
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "down", "tab":
		if s.focusZone == syncDetailZoneButtons {
			if len(devices) > 0 {
				s.focusZone = syncDetailZoneList
				s.cursor = 0
			}
			return s, nil
		}
		if s.focusZone == syncDetailZoneList {
			if s.cursor < len(devices)-1 {
				s.cursor++
			}
		}
		return s, nil
	case "shift+tab":
		if s.focusZone == syncDetailZoneList {
			s.focusZone = syncDetailZoneButtons
			s.btnIdx = 0
			return s, nil
		}
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "backspace":
		return s, emitFocusParent
	case "enter":
		return s.handleEnter()
	}
	return s, nil
}

func (s *SyncthingDetailScreen) handleEnter() (
	Screen, tea.Cmd,
) {
	if s.focusZone == syncDetailZoneButtons {
		switch s.btnIdx {
		case 0: // Pair Device
			screen := NewSyncthingPairScreen(s.ctx)
			return s, func() tea.Msg {
				return openTabMsg{
					Kind:   tabSyncthingPair,
					Label:  "Pair Device",
					Screen: screen,
					Parent: tabSyncthing,
				}
			}
		case 1: // Web UI
			screen := NewSyncthingWebUIScreen(s.ctx)
			return s, func() tea.Msg {
				return openTabMsg{
					Kind:   tabSyncthingWebUI,
					Label:  "Web UI",
					Screen: screen,
					Parent: tabSyncthing,
				}
			}
		}
		return s, nil
	}

	// Device list: open device detail
	devices := s.ctx.State.SyncthingDevices
	if s.ctx.State.SyncthingDevicesKnown && s.cursor >= 0 && s.cursor < len(devices) {
		dev := devices[s.cursor]
		label := dev.Name
		if len(label) > 17 {
			label = label[:17] + "..."
		}
		screen := NewSyncthingDeviceScreen(
			s.ctx, dev)
		return s, func() tea.Msg {
			return openTabMsg{
				Kind:   tabSyncthingDevice,
				Label:  label,
				Key:    dev.DeviceID,
				Screen: screen,
				Parent: tabSyncthing,
			}
		}
	}
	return s, nil
}

func (s *SyncthingDetailScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	if refreshed, ok := msg.(syncthingDevicesMsg); ok {
		if s.focusZone == syncDetailZoneList {
			previous := s.ctx.State.SyncthingDevices
			id := ""
			if s.cursor >= 0 && s.cursor < len(previous) {
				id = previous[s.cursor].DeviceID
			}
			for i, d := range refreshed.devices {
				if refreshed.err == nil && d.DeviceID == id {
					s.cursor = i
					return s, nil
				}
			}
			s.focusZone = syncDetailZoneButtons
			s.cursor = 0
		}
	}
	if _, ok := msg.(tabActivatedMsg); ok {
		return s, requestSyncthingDevicesCmd(s.ctx)
	}
	return s, nil
}

func (s *SyncthingDetailScreen) View(
	w, h int,
) string {
	s.clampCursor()
	devices := s.ctx.State.SyncthingDevices
	isFocused := s.ctx.ContentFocused
	onButtons := isFocused &&
		s.focusZone == syncDetailZoneButtons

	// ── Fixed header: title + buttons ────────────
	var headerLines []string
	headerLines = append(headerLines, "")
	headerLines = append(headerLines,
		centerPad(
			theme.Header.Render(
				"Syncthing details"), w))
	headerLines = append(headerLines, "")
	headerLines = append(headerLines,
		renderButtons(
			[]string{"Pair Device", "Web UI"},
			s.btnIdx, onButtons, w))
	headerLines = append(headerLines, "")

	headerH := len(headerLines)
	header := strings.Join(headerLines, "\n")

	// ── Scrollable body ─────────────────────────
	var midLines []string

	if text, warn := backupCopyLine(s.ctx.State, time.Now()); text != "" {
		style := theme.Value
		if warn {
			style = theme.Warning
		}
		for _, line := range strings.Split(ansi.Wrap(text, max(w-2, 16), ""), "\n") {
			midLines = append(midLines, " "+style.Render(line))
		}
		midLines = append(midLines, "")
	}

	pairedCount := len(devices)
	label := "Configured Devices (unavailable)"
	if s.ctx.State.SyncthingDevicesKnown {
		label = fmt.Sprintf("Configured Devices (%d)", pairedCount)
	}
	midLines = append(midLines, " "+theme.Label.Render(label))
	if s.ctx.syncthingActive != 0 {
		midLines = append(midLines, " "+theme.Dim.Render("Refreshing current configuration..."))
	}
	if s.ctx.State.SyncthingDevicesKnown && !s.ctx.State.SyncthingDevicesChecked.IsZero() {
		midLines = append(midLines, " "+theme.Dim.Render("Last checked "+s.ctx.State.SyncthingDevicesChecked.Format("15:04:05")))
	}
	midLines = append(midLines, "")

	cursorLine := 0

	if !s.ctx.State.SyncthingDevicesKnown {
		midLines = append(midLines,
			" "+theme.Warning.Render(
				"Cannot read the current device list"))
		midLines = append(midLines,
			" "+theme.Dim.Render(
				"Retrying automatically. Inspect Syncthing if it persists."))
	} else if pairedCount == 0 {
		midLines = append(midLines,
			" "+theme.Dim.Render(
				"No remote devices configured"))
	} else {
		hdrStyle := theme.TableHeader
		sepStyle := theme.TableDim

		nameW := min(24, max(6, w-35))
		shareW := 16
		idW := max(5, w-nameW-shareW-3)
		idLabel := "Device ID"
		if idW < len(idLabel) {
			idLabel = "ID"
		}

		hdr := " " +
			hdrStyle.Render(pad("Name", nameW)) +
			hdrStyle.Render(pad(idLabel, idW)) + " " +
			hdrStyle.Render(pad("Backup", shareW))
		midLines = append(midLines, hdr)
		midLines = append(midLines,
			" "+sepStyle.Render(
				strings.Repeat("─", w-2)))

		onList := isFocused &&
			s.focusZone == syncDetailZoneList

		selStyle := theme.NavActive

		tableStart := len(midLines)

		for i := 0; i < pairedCount; i++ {
			d := devices[i]

			name := d.Name
			if len(name) > nameW-1 {
				name = name[:nameW-2] + ".."
			}
			nameStr := pad(name, nameW)

			devID := d.DeviceID
			if len(devID) > idW-1 {
				devID = devID[:idW-4] + "..."
			}
			idStr := pad(devID, idW)
			shareStr := " " + pad(deliveryText(s.ctx.State, d.DeviceID), shareW)

			isSelected := onList && s.cursor == i

			marker := " "
			if isSelected {
				marker = theme.NavActive.Render("▸")
				cursorLine = tableStart + i
				midLines = append(midLines,
					marker+
						selStyle.Render(nameStr)+
						selStyle.Render(idStr)+selStyle.Render(shareStr))
			} else {
				midLines = append(midLines,
					marker+
						theme.Value.Render(nameStr)+
						theme.Dim.Render(idStr)+theme.Value.Render(shareStr))
			}
		}
	}

	midContent := strings.Join(midLines, "\n")

	// ── Viewport ────────────────────────────────
	vpH := h - headerH
	if vpH < 1 {
		vpH = 1
	}

	vpRendered := renderViewport(
		midContent, w, vpH, cursorLine,
		len(midLines),
		isFocused &&
			s.focusZone == syncDetailZoneList)

	return header + "\n" + vpRendered
}

func (s *SyncthingDetailScreen) HelpBindings() []key.Binding {
	if s.focusZone == syncDetailZoneButtons {
		return manageButtonBindings(
			"devices", s.btnIdx, s.ctx.HasTabs)
	}
	return homeListBindings(
		"navigate", "open", "buttons")
}

// clampCursor ensures the cursor is within bounds after
// the device list may have shrunk (e.g. after a remove).
func (s *SyncthingDetailScreen) clampCursor() {
	devices := s.ctx.State.SyncthingDevices
	if s.cursor >= len(devices) {
		s.cursor = len(devices) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
	if len(devices) == 0 &&
		s.focusZone == syncDetailZoneList {
		s.focusZone = syncDetailZoneButtons
		s.btnIdx = 0
	}
}
