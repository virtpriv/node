package tui

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/bitcoin"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/release"
	"github.com/virtpriv/node/internal/servicecontrol"
	"github.com/virtpriv/node/internal/system"
	"github.com/virtpriv/node/internal/theme"
)

// ── SystemHomeScreen ──────────────────────────────────
// Section home for System. Two focus zones: buttons
// and scrollable service list with hotkey actions.
//
// Buttons are dynamic. Only actionable buttons appear:
//   Update Packages and Accounts (always),
//   Node Updates (always, including saved recovery), Reboot (when required).
//
// Confirms are screen-owned: svcConfirm and sysConfirm
// live here. When active, they intercept all keys and
// show y/n prompt in the view and helpbar.

const (
	sysHomeZoneButtons  = 0
	sysHomeZoneServices = 1
)

// sysBtn identifies a logical button action independent
// of its position in the dynamic button bar.
type sysBtn int

const (
	sysBtnUpdatePkg sysBtn = iota
	sysBtnAccounts
	sysBtnUpdateNode
	sysBtnReboot
)

type SystemHomeScreen struct {
	ctx       *ScreenContext
	btnIdx    int
	focusZone int
	svcCursor int

	// Confirmed service identity is independent of the cursor.
	svcConfirm *servicecontrol.Request
	sysConfirm string // "Update packages", "Reboot"

	// Background service action in progress
	svcPending     *serviceAttempt
	svcResult      string
	pkgPending     *packageAttempt
	pkgResult      string
	rebootPending  *rebootAttempt
	rebootResult   string
	rebootAccepted bool
}

func NewSystemHomeScreen(
	ctx *ScreenContext,
) *SystemHomeScreen {
	return &SystemHomeScreen{ctx: ctx}
}

// ── Screen interface ────────────────────────────────────

func (s *SystemHomeScreen) Init() tea.Cmd {
	return nil
}

func (s *SystemHomeScreen) HandleKey(
	keyStr string, msg tea.KeyPressMsg,
) (Screen, tea.Cmd) {
	// Confirm dialogs intercept all keys
	if s.svcConfirm != nil {
		return s.handleSvcConfirm(keyStr)
	}
	if s.sysConfirm != "" {
		return s.handleSysConfirm(keyStr)
	}

	// Block service actions while one is pending
	if s.svcPending != nil {
		switch keyStr {
		case "r", "s", "a", "p", "u":
			return s, nil
		}
	}

	actions := s.buttonActions()
	maxBtn := len(actions) - 1
	if s.btnIdx > maxBtn {
		s.btnIdx = maxBtn
	}

	switch keyStr {
	case "ctrl+c":
		return s, tea.Quit
	case "left":
		if s.focusZone == sysHomeZoneButtons &&
			s.btnIdx > 0 {
			s.btnIdx--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right":
		if s.focusZone == sysHomeZoneButtons &&
			s.btnIdx < maxBtn {
			s.btnIdx++
		}
		return s, nil
	case "up":
		if s.focusZone == sysHomeZoneServices {
			if s.svcCursor > 0 {
				s.svcCursor--
			} else {
				s.focusZone = sysHomeZoneButtons
				s.btnIdx = 0
			}
			return s, nil
		}
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "down", "tab":
		if s.focusZone == sysHomeZoneButtons {
			if s.svcCount() > 0 {
				s.focusZone = sysHomeZoneServices
				s.svcCursor = 0
			}
			return s, nil
		}
		if s.focusZone == sysHomeZoneServices {
			if s.svcCursor < s.svcCount()-1 {
				s.svcCursor++
			}
		}
		return s, nil
	case "shift+tab":
		if s.focusZone == sysHomeZoneServices {
			s.focusZone = sysHomeZoneButtons
			s.btnIdx = 0
			return s, nil
		}
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
		return s, nil
	case "r":
		if s.focusZone == sysHomeZoneServices {
			request, err := servicecontrol.New(s.svcName(s.svcCursor), "restart")
			if err == nil {
				s.svcConfirm = &request
			}
		}
		return s, nil
	case "s":
		if s.focusZone == sysHomeZoneServices {
			request, err := servicecontrol.New(s.svcName(s.svcCursor), "stop")
			if err == nil {
				s.svcConfirm = &request
			}
		}
		return s, nil
	case "a":
		if s.focusZone == sysHomeZoneServices {
			request, err := servicecontrol.New(s.svcName(s.svcCursor), "start")
			if err == nil {
				s.svcConfirm = &request
			}
		}
		return s, nil
	case "l":
		if s.focusZone == sysHomeZoneServices {
			svc := servicecontrol.Unit(s.svcName(s.svcCursor))
			if svc == "" {
				return s, nil
			}
			c := exec.Command("bash", "-c",
				"clear && journalctl -u "+svc+
					" -n 100 --no-pager"+
					" && echo && echo "+
					"'  Press Enter to return...'"+
					" && read && clear")
			return s, tea.ExecProcess(c,
				func(err error) tea.Msg {
					return systemRefreshMsg{}
				})
		}
		return s, nil
	case "p":
		if s.focusZone == sysHomeZoneServices &&
			s.svcName(s.svcCursor) == "lnd" &&
			s.ctx.Cfg.P2PMode == "tor" {
			screen := NewP2PUpgradeScreen(s.ctx)
			return s, func() tea.Msg {
				return openTabMsg{
					Kind:   tabP2PUpgrade,
					Label:  "P2P Upgrade",
					Screen: screen,
				}
			}
		}
		return s, nil
	case "u":
		if s.focusZone == sysHomeZoneServices &&
			s.svcName(s.svcCursor) == "lnd" &&
			s.ctx.walletExists() {
			screen := NewAutoUnlockScreen(s.ctx)
			label := "Auto-Unlock"
			if s.ctx.Cfg.AutoUnlock {
				label = "Disable Auto-Unlock"
			}
			return s, func() tea.Msg {
				return openTabMsg{
					Kind:   tabAutoUnlock,
					Label:  label,
					Screen: screen,
				}
			}
		}
		return s, nil
	case "backspace":
		return s, emitFocusSidebar
	case "enter":
		if s.focusZone == sysHomeZoneButtons &&
			s.btnIdx < len(actions) {
			switch actions[s.btnIdx] {
			case sysBtnUpdatePkg:
				if s.pkgPending == nil {
					s.sysConfirm = "Update packages"
				}
			case sysBtnAccounts:
				return s, openAccountsCmd(s.ctx)
			case sysBtnUpdateNode:
				screen := NewNodeUpdateScreen(
					s.ctx)
				return s, func() tea.Msg {
					return openTabMsg{
						Kind:   tabNodeUpdates,
						Label:  "Updates",
						Screen: screen,
					}
				}
			case sysBtnReboot:
				if s.rebootPending == nil && !s.rebootAccepted {
					s.sysConfirm = "Reboot"
				}
			}
		}
		return s, nil
	}
	return s, nil
}

// ── Confirm handlers ────────────────────────────────────

func (s *SystemHomeScreen) handleSvcConfirm(
	keyStr string,
) (Screen, tea.Cmd) {
	request := s.svcConfirm
	s.svcConfirm = nil
	if keyStr == "y" && request != nil {
		attempt := &serviceAttempt{request: *request}
		s.svcPending, s.svcResult = attempt, ""
		return s, func() tea.Msg { return serviceActionRequestMsg{owner: s, attempt: attempt} }
	}
	return s, nil
}

func (s *SystemHomeScreen) handleSysConfirm(
	keyStr string,
) (Screen, tea.Cmd) {
	action := s.sysConfirm
	s.sysConfirm = ""
	if keyStr == "y" {
		if action == "Reboot" {
			if s.rebootPending != nil || s.rebootAccepted {
				return s, nil
			}
			attempt := &rebootAttempt{}
			s.rebootPending, s.rebootResult = attempt, ""
			return s, func() tea.Msg { return rebootRequestMsg{owner: s, attempt: attempt} }
		}
		if s.pkgPending != nil {
			return s, nil
		}
		s.pkgPending = &packageAttempt{}
		s.pkgResult = ""
		return s, packageUpdateCmd(s, s.pkgPending)
	}
	return s, nil
}

func (s *SystemHomeScreen) HandleMsg(
	msg tea.Msg,
) (Screen, tea.Cmd) {
	return s, nil
}

// ── View ────────────────────────────────────────────────

func (s *SystemHomeScreen) View(
	w, h int,
) string {
	isFocused := s.ctx.ContentFocused
	cfg := s.ctx.Cfg
	status := s.ctx.Status

	boxW := w - 4
	if boxW < 30 {
		boxW = 30
	}

	border := theme.AddonBorderNormal
	titleStyle := lipgloss.NewStyle().
		Foreground(theme.ColorPrimary).
		Bold(true)

	// ── Fixed header (version + buttons) ─────────
	var headerLines []string
	headerLines = append(headerLines, "")

	verText := "Virtual Private Node v" +
		s.ctx.Version
	headerLines = append(headerLines,
		centerPad(theme.Action.Render(verText), w))

	if s.hasUpdate() {
		updateText := "Update available: v" +
			s.ctx.LatestVersion
		headerLines = append(headerLines,
			centerPad(
				lipgloss.NewStyle().
					Foreground(theme.ColorUpdate).
					Render(updateText), w))
	}

	headerLines = append(headerLines, "")

	btnLabels := s.buttonLabels()

	headerLines = append(headerLines,
		renderButtons(
			btnLabels, s.btnIdx,
			isFocused &&
				s.focusZone == sysHomeZoneButtons, w))
	headerLines = append(headerLines, "")

	if s.sysConfirm != "" {
		headerLines = append(headerLines,
			" "+theme.Warning.Render(
				fmt.Sprintf("%s? [y/n]",
					s.sysConfirm)))
		headerLines = append(headerLines, "")
	}

	if s.svcResult != "" {
		result := lipgloss.NewStyle().Width(max(1, w-4)).Render(s.svcResult)
		headerLines = append(headerLines, strings.Split(result, "\n")...)
		headerLines = append(headerLines, "")
	}
	if s.pkgResult != "" {
		result := lipgloss.NewStyle().Width(max(1, w-4)).Render(s.pkgResult)
		headerLines = append(headerLines, strings.Split(result, "\n")...)
		headerLines = append(headerLines, "")
	}
	rebootText := s.rebootResult
	if s.rebootPending != nil {
		rebootText = "Requesting reboot..."
	}
	if rebootText != "" {
		result := lipgloss.NewStyle().Width(max(1, w-4)).Render(rebootText)
		headerLines = append(headerLines, strings.Split(result, "\n")...)
		headerLines = append(headerLines, "")
	}
	header := strings.Join(headerLines, "\n")
	headerH := strings.Count(header, "\n") + 1

	// ── Scrollable middle (all cards) ────────────
	var midLines []string

	// Services card
	midLines = append(midLines,
		"  "+border.Render(
			"┌"+strings.Repeat("─", boxW-2)+"┐"))

	svcTitle := " Services"
	svcTitlePad := boxW - 2 - len(svcTitle)
	if svcTitlePad < 0 {
		svcTitlePad = 0
	}
	midLines = append(midLines,
		"  "+border.Render("│")+
			titleStyle.Render(svcTitle)+
			strings.Repeat(" ", svcTitlePad)+
			border.Render("│"))

	names := serviceNames(cfg)
	for i, name := range names {
		dot := theme.Dim.Render("?")
		stateText := "unavailable"
		if status != nil {
			observation := status.Services[name]
			if observation.Known() {
				stateText = ""
				dot = theme.RedDot.Render("●")
				if observation.Value {
					dot = theme.GreenDot.Render("●")
				}
				if !observation.Fresh() {
					dot = theme.Dim.Render("?")
					stateText = "stale"
				}
			}
		}

		isSelected := isFocused &&
			s.focusZone == sysHomeZoneServices &&
			s.svcCursor == i

		prefix := " "
		style := theme.Value
		if isSelected {
			prefix = theme.NavActive.Render("▸")
			style = theme.NavActive
		}

		svcLine := prefix + " " + dot + " " +
			style.Render(name)
		if stateText != "" {
			svcLine += " " + theme.Dim.Render(stateText)
		}
		if name == "lnd" && status != nil &&
			status.WalletState.Fresh() && status.WalletState.Value == lndrpc.WalletStateLocked {
			svcLine += "  " + theme.Warning.Render("locked")
		}

		if s.svcPending != nil && s.svcPending.request.Service() == name {
			svcLine += "  " +
				theme.Dim.Render(servicePendingText(s.svcPending.request))
		} else if isSelected && s.svcPending == nil {
			// Standard service hints, dim
			hint := theme.Dim.Render(
				"  r restart  s stop  a start  l logs")
			// LND-specific destructive hotkeys:
			// the hotkey letter itself is rendered
			// in red so users see they're sensitive,
			// while the description stays dim.
			if name == "lnd" &&
				s.ctx.Cfg.P2PMode == "tor" {
				hint += "  " +
					theme.Warning.Render("p") +
					theme.Dim.Render(" p2p")
			}
			if name == "lnd" &&
				s.ctx.walletExists() {
				uLabel := " unlock"
				if s.ctx.Cfg.AutoUnlock {
					uLabel = " lock"
				}
				hint += "  " +
					theme.Warning.Render("u") +
					theme.Dim.Render(uLabel)
			}
			svcLine += hint
		}

		svcVis := lipgloss.Width(svcLine)
		svcPad := boxW - 2 - svcVis
		if svcPad < 0 {
			svcPad = 0
		}
		midLines = append(midLines,
			"  "+border.Render("│")+
				svcLine+
				strings.Repeat(" ", svcPad)+
				border.Render("│"))
	}

	if s.svcConfirm != nil {
		svc := s.svcConfirm.Service()
		confirmLine := " " + theme.Warning.Render(
			fmt.Sprintf(" %s %s? [y/n]",
				s.svcConfirm.Action(), svc))
		confirmVis := lipgloss.Width(confirmLine)
		confirmPad := boxW - 2 - confirmVis
		if confirmPad < 0 {
			confirmPad = 0
		}
		midLines = append(midLines,
			"  "+border.Render("│")+
				confirmLine+
				strings.Repeat(" ", confirmPad)+
				border.Render("│"))
	}

	midLines = append(midLines,
		"  "+border.Render(
			"└"+strings.Repeat("─", boxW-2)+"┘"))

	midLines = append(midLines, "")

	// Resources card
	midLines = append(midLines,
		"  "+border.Render(
			"┌"+strings.Repeat("─", boxW-2)+"┐"))

	resTitle := " Resources"
	resTitlePad := boxW - 2 - len(resTitle)
	if resTitlePad < 0 {
		resTitlePad = 0
	}
	midLines = append(midLines,
		"  "+border.Render("│")+
			titleStyle.Render(resTitle)+
			strings.Repeat(" ", resTitlePad)+
			border.Render("│"))

	if status != nil {
		resRows := []string{
			" " + theme.Label.Render("Disk: ") +
				theme.Value.Render(
					observationText(status.Disk, fmt.Sprintf("%s / %s (%s)",
						status.Disk.Value.Used,
						status.Disk.Value.Total,
						status.Disk.Value.Percent))),
			" " + theme.Label.Render("RAM:  ") +
				theme.Value.Render(
					observationText(status.Memory, fmt.Sprintf("%s / %s (%s)",
						status.Memory.Value.Used,
						status.Memory.Value.Total,
						status.Memory.Value.Percent))),
			" " + theme.Label.Render("BTC:  ") +
				theme.Value.Render(
					bitcoinSize(status.Bitcoin)),
		}
		if cfg.HasLND() {
			resRows = append(resRows,
				" "+theme.Label.Render("LND:  ")+
					theme.Value.Render(
						observationText(status.LNDSize, status.LNDSize.Value)))
		}
		resRows = append(resRows, securityUpdatesRow(status.Updates, time.Now()))
		if status.Reboot.Value {
			resRows = append(resRows,
				" "+theme.Warning.Render(
					observationText(status.Reboot, "⚠ Reboot required")))
		}

		for _, rl := range resRows {
			rlVis := lipgloss.Width(rl)
			rlPad := boxW - 2 - rlVis
			if rlPad < 0 {
				rlPad = 0
			}
			midLines = append(midLines,
				"  "+border.Render("│")+
					rl+
					strings.Repeat(" ", rlPad)+
					border.Render("│"))
		}
	} else {
		loadLine := " " +
			theme.Dim.Render(" Loading...")
		loadVis := lipgloss.Width(loadLine)
		loadPad := boxW - 2 - loadVis
		if loadPad < 0 {
			loadPad = 0
		}
		midLines = append(midLines,
			"  "+border.Render("│")+
				loadLine+
				strings.Repeat(" ", loadPad)+
				border.Render("│"))
	}

	midLines = append(midLines,
		"  "+border.Render(
			"└"+strings.Repeat("─", boxW-2)+"┘"))

	midLines = append(midLines, "")

	// Bitcoin card
	midLines = append(midLines,
		"  "+border.Render(
			"┌"+strings.Repeat("─", boxW-2)+"┐"))

	btcTitle := " ₿ Bitcoin"
	btcTitlePad := boxW - 2 -
		lipgloss.Width(btcTitle)
	if btcTitlePad < 0 {
		btcTitlePad = 0
	}
	midLines = append(midLines,
		"  "+border.Render("│")+
			theme.Bitcoin.Render(btcTitle)+
			strings.Repeat(" ", btcTitlePad)+
			border.Render("│"))

	if status == nil {
		btcLoad := " " +
			theme.Dim.Render(" Loading...")
		btcLoadVis := lipgloss.Width(btcLoad)
		btcLoadPad := boxW - 2 - btcLoadVis
		if btcLoadPad < 0 {
			btcLoadPad = 0
		}
		midLines = append(midLines,
			"  "+border.Render("│")+
				btcLoad+
				strings.Repeat(" ", btcLoadPad)+
				border.Render("│"))
	} else if !status.Bitcoin.Known() {
		btcErr := " " +
			theme.Warn.Render(" Not responding")
		btcErrVis := lipgloss.Width(btcErr)
		btcErrPad := boxW - 2 - btcErrVis
		if btcErrPad < 0 {
			btcErrPad = 0
		}
		midLines = append(midLines,
			"  "+border.Render("│")+
				btcErr+
				strings.Repeat(" ", btcErrPad)+
				border.Render("│"))
	} else {
		var btcRows []string
		syncVal := theme.Good.Render("synced")
		if !status.Bitcoin.Value.Synced {
			syncVal = theme.Warn.Render("syncing")
		}
		btcRows = append(btcRows,
			" "+theme.Label.Render("Sync:     ")+
				observationText(status.Bitcoin, syncVal))
		btcRows = append(btcRows,
			" "+theme.Label.Render("Height:   ")+
				theme.Value.Render(
					fmt.Sprintf("%d / %d",
						status.Bitcoin.Value.Blocks,
						status.Bitcoin.Value.Headers)))
		if status.Bitcoin.Value.Progress > 0 {
			btcRows = append(btcRows,
				" "+theme.Label.Render("Progress: ")+
					theme.Value.Render(
						bitcoin.FormatProgress(
							status.Bitcoin.Value.Progress)))
		}
		btcRows = append(btcRows,
			" "+theme.Label.Render("Network:  ")+
				theme.Value.Render(cfg.Network))

		for _, bl := range btcRows {
			blVis := lipgloss.Width(bl)
			blPad := boxW - 2 - blVis
			if blPad < 0 {
				blPad = 0
			}
			midLines = append(midLines,
				"  "+border.Render("│")+
					bl+
					strings.Repeat(" ", blPad)+
					border.Render("│"))
		}
	}

	midLines = append(midLines,
		"  "+border.Render(
			"└"+strings.Repeat("─", boxW-2)+"┘"))

	midContent := strings.Join(midLines, "\n")

	// ── Viewport ─────────────────────────────────
	vpH := h - headerH
	if vpH < 1 {
		vpH = 1
	}

	// Services start at line 2 (top border + title)
	cursorLine := 2 + s.svcCursor

	vpRendered := renderViewport(
		midContent, w, vpH, cursorLine,
		len(midLines),
		s.focusZone == sysHomeZoneServices &&
			len(names) > 0)

	// ── Assemble output ──────────────────────────
	return header + "\n" + vpRendered
}

// ── HelpBindings ────────────────────────────────────────

func (s *SystemHomeScreen) HelpBindings() []key.Binding {
	if s.svcConfirm != nil || s.sysConfirm != "" {
		return confirmDialogBindings()
	}
	if s.focusZone == sysHomeZoneServices {
		return s.serviceBindings()
	}
	bindings := homeButtonBindings("services", s.btnIdx, s.ctx.HasTabs)
	actions := s.buttonActions()
	blocked := s.btnIdx < len(actions) &&
		((actions[s.btnIdx] == sysBtnUpdatePkg && s.pkgPending != nil) ||
			(actions[s.btnIdx] == sysBtnReboot && (s.rebootPending != nil || s.rebootAccepted)))
	if blocked {
		for i := range bindings {
			if slices.Contains(bindings[i].Keys(), "enter") {
				bindings = slices.Delete(bindings, i, i+1)
				break
			}
		}
	}
	return bindings
}

func (s *SystemHomeScreen) serviceBindings() []key.Binding {
	if s.svcPending != nil {
		return []key.Binding{bind("↑↓", "services", "up", "down"), bind("l", "logs", "l"), kShiftTabButtons, kSidebar, kBack, kQuit}
	}

	binds := []key.Binding{
		bind("↑↓", "services", "up", "down"),
		bind("r", "restart", "r"),
		bind("s", "stop", "s"),
		bind("a", "start", "a"),
		bind("l", "logs", "l"),
	}
	if s.svcName(s.svcCursor) == "lnd" &&
		s.ctx.Cfg.P2PMode == "tor" {
		binds = append(binds,
			bind("p", "p2p", "p"))
	}
	if s.svcName(s.svcCursor) == "lnd" &&
		s.ctx.walletExists() {
		uHelp := "unlock"
		if s.ctx.Cfg.AutoUnlock {
			uHelp = "lock"
		}
		binds = append(binds,
			bind("u", uHelp, "u"))
	}
	binds = append(binds, kShiftTabButtons, kSidebar, kBack, kQuit)
	return binds
}

// ── Helpers ─────────────────────────────────────────────

func (s *SystemHomeScreen) hasUpdate() bool {
	newer, err := release.Newer(s.ctx.Version, s.ctx.LatestVersion)
	return release.IsStable(s.ctx.LatestVersion) && err == nil && newer
}

func (s *SystemHomeScreen) buttonActions() []sysBtn {
	// Saved updates must remain reachable without release discovery, including
	// after a reboot, failed startup, or a completed VPN binary replacement.
	actions := []sysBtn{sysBtnUpdatePkg, sysBtnAccounts, sysBtnUpdateNode}
	if s.rebootPending != nil || s.rebootAccepted ||
		(s.ctx.Status != nil && s.ctx.Status.Reboot.Value) {
		actions = append(actions, sysBtnReboot)
	}
	return actions
}

var sysBtnLabel = map[sysBtn]string{
	sysBtnUpdatePkg:  "Update Packages",
	sysBtnAccounts:   "Accounts",
	sysBtnUpdateNode: "Node Updates",
	sysBtnReboot:     "Reboot",
}

func (s *SystemHomeScreen) buttonLabels() []string {
	actions := s.buttonActions()
	labels := make([]string, len(actions))
	for i, a := range actions {
		labels[i] = sysBtnLabel[a]
		if a == sysBtnUpdatePkg && s.pkgPending != nil {
			labels[i] = "Updating..."
		}
		if a == sysBtnReboot {
			if s.rebootPending != nil {
				labels[i] = "Reboot..."
			} else if s.rebootAccepted {
				labels[i] = "Requested"
			}
		}
	}
	return labels
}

func (s *SystemHomeScreen) svcCount() int {
	return len(serviceNames(s.ctx.Cfg))
}

func (s *SystemHomeScreen) svcName(i int) string {
	names := serviceNames(s.ctx.Cfg)
	if i >= 0 && i < len(names) {
		return names[i]
	}
	return ""
}

// securityUpdatesRow is the Resources card's line about Debian's automatic
// security updates. It warns once a run is overdue.
func securityUpdatesRow(o app.Observation[system.SecurityUpdates], now time.Time) string {
	label := " " + theme.Label.Render("Security updates: ")
	if !o.Known() {
		return label + theme.Value.Render(observationText(o, ""))
	}
	state, since := app.SecurityUpdatesStatus(o.Value, now)
	when := since.Local().Format("2 Jan 15:04")
	switch state {
	case app.SecurityUpdatesOverdue:
		return " " + theme.Warning.Render(observationText(o, "⚠ Security updates: none since "+when))
	case app.SecurityUpdatesNotRunYet:
		return label + theme.Value.Render(observationText(o, "not run yet"))
	}
	return label + theme.Value.Render(observationText(o, "last run "+when))
}
