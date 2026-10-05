package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
)

// renderViewport creates a local viewport, sets content,
// auto-scrolls to keep cursorLine visible, and returns the
// rendered view with scroll indicators overlaid.
//
// Parameters:
//
//	content    - fully rendered string of all lines
//	w          - viewport width
//	vpH        - viewport height (visible lines)
//	cursorLine - which line the cursor is on (0-based)
//	totalLines - total number of lines in content
//	active     - whether auto-scroll should be applied
func renderViewport(
	content string, w, vpH, cursorLine int,
	totalLines int, active bool,
) string {
	vp := viewport.New(
		viewport.WithWidth(w),
		viewport.WithHeight(vpH),
	)
	vp.FillHeight = true
	vp.SetContent(content)

	// Auto-scroll to keep cursor visible
	if active && totalLines > vpH {
		offset := vp.YOffset()
		if cursorLine < offset {
			vp.SetYOffset(cursorLine)
		}
		if cursorLine >= offset+vpH {
			vp.SetYOffset(cursorLine - vpH + 1)
		}
	}

	vpView := vp.View()
	vpLines := strings.Split(vpView, "\n")

	hasAbove := vp.YOffset() > 0
	hasBelow := vp.YOffset()+vpH < totalLines

	// Overlay scroll indicators on the right edge
	// of the first/last line. We truncate the line
	// to make room so total width stays within bounds.
	if hasAbove && len(vpLines) > 0 {
		line := vpLines[0]
		lineW := lipgloss.Width(line)
		arrow := theme.Dim.Render(" ▲")
		if lineW > w-2 {
			// Truncate line to make room
			r := []rune(line)
			for lipgloss.Width(string(r)) > w-2 && len(r) > 0 {
				r = r[:len(r)-1]
			}
			line = string(r)
			lineW = lipgloss.Width(line)
		}
		pad := w - 2 - lineW
		if pad < 0 {
			pad = 0
		}
		vpLines[0] = line +
			strings.Repeat(" ", pad) + arrow
	}
	if hasBelow && len(vpLines) > 0 {
		idx := len(vpLines) - 1
		line := vpLines[idx]
		lineW := lipgloss.Width(line)
		arrow := theme.Dim.Render(" ▼")
		if lineW > w-2 {
			r := []rune(line)
			for lipgloss.Width(string(r)) > w-2 && len(r) > 0 {
				r = r[:len(r)-1]
			}
			line = string(r)
			lineW = lipgloss.Width(line)
		}
		pad := w - 2 - lineW
		if pad < 0 {
			pad = 0
		}
		vpLines[idx] = line +
			strings.Repeat(" ", pad) + arrow
	}

	return strings.Join(vpLines, "\n")
}

// ── Balance summary (shared utility) ────────────────────
// Used by ChannelsHomeScreen and WalletHomeScreen (and
// their legacy counterparts during migration).

func balanceSummaryLines(
	status *statusSnapshot, w int,
) []string {
	if status == nil {
		return nil
	}

	var totalCap, totalLocal, totalRemote int64
	activeCount, inactiveCount := 0, 0
	for _, ch := range status.Channels.Value.Channels {
		if ch.Pending {
			continue
		}
		totalCap += ch.Capacity
		totalLocal += ch.LocalBalance
		totalRemote += ch.RemoteBalance
		if ch.Active {
			activeCount++
		} else {
			inactiveCount++
		}
	}

	localPct, remotePct := 0, 0
	if totalCap > 0 {
		localPct = int(
			float64(totalLocal) * 100 /
				float64(totalCap))
		remotePct = 100 - localPct
	}

	labelW := 11
	leftColW := labelW + 18
	boxW := w - leftColW - 2
	if boxW < 22 {
		boxW = 22
	}

	barInnerW := boxW - 4
	if barInnerW < 8 {
		barInnerW = 8
	}
	localBarW := barInnerW * localPct / 100
	if localBarW < 0 {
		localBarW = 0
	}
	if localBarW > barInnerW {
		localBarW = barInnerW
	}
	remoteBarW := barInnerW - localBarW

	barLocal := lipgloss.NewStyle().
		Foreground(theme.ColorChanLocal).
		Render(strings.Repeat("█", localBarW))
	barRemote := lipgloss.NewStyle().
		Foreground(theme.ColorChanRemote).
		Render(strings.Repeat("█", remoteBarW))
	barLine := " " + barLocal + barRemote + " "

	barVis := lipgloss.Width(barLine)
	barPadR := boxW - 2 - barVis
	if barPadR < 0 {
		barPadR = 0
	}

	chLabel := fmt.Sprintf("%d channels",
		activeCount+inactiveCount)
	if inactiveCount > 0 {
		chLabel = fmt.Sprintf("%d on, %d off",
			activeCount, inactiveCount)
	}

	pctInner := fmt.Sprintf("Out %d%%    In %d%%",
		localPct, remotePct)
	if len(pctInner) > boxW-2 {
		pctInner = fmt.Sprintf("Out %d%% In %d%%",
			localPct, remotePct)
	}

	capStr := formatSats(totalCap) + " sats"

	boxTop := "┌" + strings.Repeat("─", boxW-2) + "┐"
	boxLabel := "│" +
		centerPad(chLabel, boxW-2) + "│"
	boxBar := "│" + barLine +
		strings.Repeat(" ", barPadR) + "│"
	boxPct := "│" + theme.Dim.Render(
		centerPad(pctInner, boxW-2)) + "│"
	boxCap := "│" + theme.Dim.Render(
		centerPad(capStr, boxW-2)) + "│"
	boxBot := "└" + strings.Repeat("─", boxW-2) + "┘"

	boxLines := []string{
		boxTop, boxLabel, boxBar,
		boxPct, boxCap, boxBot,
	}

	if !status.Channels.Known() {
		boxLines = []string{"", "Channels unavailable"}
	}

	leftLines := []string{
		"",
		" " + theme.Label.Render("Outbound: ") +
			theme.Value.Render(
				channelAmountText(status, totalLocal)),
		"",
		" " + theme.Label.Render("Inbound:  ") +
			theme.Value.Render(
				channelAmountText(status, totalRemote)),
		"",
		" " + theme.Label.Render("On-chain: ") +
			theme.Value.Render(
				onChainBalanceText(status)),
	}

	maxH := len(boxLines)
	if len(leftLines) > maxH {
		maxH = len(leftLines)
	}
	for len(leftLines) < maxH {
		leftLines = append(leftLines, "")
	}
	for len(boxLines) < maxH {
		boxLines = append(boxLines, "")
	}

	var result []string
	for i := 0; i < maxH; i++ {
		lft := leftLines[i]
		lftW := lipgloss.Width(lft)
		if lftW < leftColW {
			lft += strings.Repeat(" ",
				leftColW-lftW)
		}
		result = append(result, lft+" "+boxLines[i])
	}
	if notice := statusNotice(status); notice != "" {
		result = append([]string{notice}, result...)
	}
	return result
}

// ── Channel formatting ─────────────────────────────────

func renderLiquidityBar(
	local, remote, capacity int64, width int,
) string {
	if capacity <= 0 {
		return theme.Dim.Render(
			strings.Repeat("░", width))
	}
	lw := int(float64(local) / float64(capacity) *
		float64(width))
	if lw < 0 {
		lw = 0
	}
	if lw > width {
		lw = width
	}
	rw := width - lw
	return lipgloss.NewStyle().
		Foreground(theme.ColorChanLocal).
		Render(strings.Repeat("█", lw)) +
		lipgloss.NewStyle().
			Foreground(theme.ColorChanRemote).
			Render(strings.Repeat("█", rw))
}

func p2pModeLabel(mode string) string {
	if mode == "hybrid" {
		return "Tor + clearnet"
	}
	return "Tor only"
}

func formatSatsCompact(sats int64) string {
	if sats >= 100000000 {
		btc := float64(sats) / 100000000
		if btc == float64(int64(btc)) {
			return fmt.Sprintf("%.0fBTC", btc)
		}
		return fmt.Sprintf("%.1fBTC", btc)
	}
	if sats >= 1000000 {
		m := float64(sats) / 1000000
		if m == float64(int64(m)) {
			return fmt.Sprintf("%.0fM", m)
		}
		return fmt.Sprintf("%.1fM", m)
	}
	if sats >= 1000 {
		k := float64(sats) / 1000
		if k == float64(int64(k)) {
			return fmt.Sprintf("%.0fk", k)
		}
		return fmt.Sprintf("%.1fk", k)
	}
	return fmt.Sprintf("%d", sats)
}

func formatSats(sats int64) string {
	if sats < 0 {
		return fmt.Sprintf("%d", sats)
	}
	s := fmt.Sprintf("%d", sats)
	if len(s) <= 3 {
		return s
	}
	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}

// ── Route diagram ──────────────────────────────────────

func renderRouteDiagram(
	hops []lndrpc.RouteHop, w int,
) string {
	if len(hops) == 0 {
		return ""
	}
	var lines []string
	lines = append(lines, " You")
	lines = append(lines, "  │")
	for i, hop := range hops {
		name := hop.Alias
		if name == "" {
			if len(hop.PubKey) > 12 {
				name = hop.PubKey[:12] + "..."
			} else {
				name = hop.PubKey
			}
		}
		if len(name) > 16 {
			name = name[:16]
		}
		feeStr := ""
		if hop.FeeSats > 0 {
			feeStr = fmt.Sprintf(" (fee: %s)",
				formatSats(hop.FeeSats))
		}
		if i < len(hops)-1 {
			lines = append(lines,
				fmt.Sprintf("  ├── %s%s",
					theme.Value.Render(name),
					theme.Dim.Render(feeStr)))
			lines = append(lines, "  │")
		} else {
			lines = append(lines,
				fmt.Sprintf("  └── %s%s",
					theme.Success.Render(name),
					theme.Dim.Render(feeStr)))
		}
	}
	return strings.Join(lines, "\n")
}

// ── Timestamp helpers ──────────────────────────────────

func formatTimestampFull(unix int64) string {
	if unix == 0 {
		return "—"
	}
	return time.Unix(unix, 0).
		Format("2006-01-02 15:04:05")
}

func formatTimestampTable(unix int64) string {
	if unix == 0 {
		return "—"
	}
	return time.Unix(unix, 0).
		Format("2006-01-02 15:04")
}

// ── On-chain helpers ───────────────────────────────────

// formatDateShort returns YYYY-MM-DD for a unix timestamp.
func formatDateShort(unix int64) string {
	if unix == 0 {
		return "—"
	}
	return time.Unix(unix, 0).Format("2006-01-02")
}

// confIndicator returns a single-char confirmation
// progress indicator for the transaction table.
func confIndicator(confs int32) string {
	switch {
	case confs >= 100:
		return " "
	case confs == 0:
		return "○"
	case confs <= 2:
		return "◔"
	case confs <= 4:
		return "◑"
	case confs <= 6:
		return "◕"
	default:
		return "●"
	}
}

// ── Wallet creation prompt ──────────────────────────────
// Shared across all three section home screens when no
// wallet exists. Renders a centered call-to-action with
// a focusable "Create Wallet" button.

func renderWalletPrompt(
	w, h int, focused bool,
) string {
	boxW := w - 8
	if boxW > 50 {
		boxW = 50
	}
	if boxW < 30 {
		boxW = 30
	}

	border := theme.AddonBorderNormal

	var lines []string
	lines = append(lines,
		border.Render(
			"┌"+strings.Repeat("─", boxW-2)+"┐"))

	padLine := border.Render("│") +
		strings.Repeat(" ", boxW-2) +
		border.Render("│")
	lines = append(lines, padLine)

	msg1 := "Create your Lightning wallet"
	msg2 := "to start sending and receiving"
	msg3 := "Bitcoin over the Lightning Network."

	centerInBox := func(text string) string {
		rendered := theme.Value.Render(text)
		vis := lipgloss.Width(rendered)
		pad := boxW - 2 - vis
		lp := pad / 2
		rp := pad - lp
		if lp < 0 {
			lp = 0
		}
		if rp < 0 {
			rp = 0
		}
		return border.Render("│") +
			strings.Repeat(" ", lp) +
			rendered +
			strings.Repeat(" ", rp) +
			border.Render("│")
	}

	lines = append(lines, centerInBox(msg1))
	lines = append(lines, centerInBox(msg2))
	lines = append(lines, centerInBox(msg3))
	lines = append(lines, padLine)

	// Button
	btnLabel := "Create Wallet"
	var btnRendered string
	if focused {
		btnRendered = theme.BtnFocused.
			Render(" " + btnLabel + " ")
	} else {
		btnRendered = theme.BtnNormal.
			Render(" " + btnLabel + " ")
	}
	btnVis := lipgloss.Width(btnRendered)
	btnPad := boxW - 2 - btnVis
	btnLP := btnPad / 2
	btnRP := btnPad - btnLP
	if btnLP < 0 {
		btnLP = 0
	}
	if btnRP < 0 {
		btnRP = 0
	}
	btnLine := border.Render("│") +
		strings.Repeat(" ", btnLP) +
		btnRendered +
		strings.Repeat(" ", btnRP) +
		border.Render("│")
	lines = append(lines, btnLine)

	lines = append(lines, padLine)
	lines = append(lines,
		border.Render(
			"└"+strings.Repeat("─", boxW-2)+"┘"))

	// Center vertically in available space
	content := strings.Join(lines, "\n")
	contentH := len(lines)
	topPad := (h - contentH) / 2
	if topPad < 1 {
		topPad = 1
	}

	var out []string
	for i := 0; i < topPad; i++ {
		out = append(out, "")
	}

	// Center horizontally
	boxVis := lipgloss.Width(lines[0])
	leftPad := (w - boxVis) / 2
	if leftPad < 1 {
		leftPad = 1
	}
	prefix := strings.Repeat(" ", leftPad)
	for _, l := range lines {
		out = append(out, prefix+l)
	}

	_ = content // suppress unused
	return strings.Join(out, "\n")
}

func renderWalletStateUnavailable(w, h int) string {
	p := newPane(w)
	p.title(theme.Warning, "Wallet State Unavailable")
	p.warn("The TUI could not verify whether an LND wallet exists.")
	p.blank()
	p.dim("Wallet actions are disabled rather than guessing.")
	p.dim("Check: journalctl -u vpn-helperd")
	return p.renderWithBottomButtons(
		[]string{"Unavailable"}, 0, false, h)
}

// renderWaitingForLND renders a vertically and
// horizontally centered "Waiting for LND..." message.
func renderWaitingForLND(w, h int) string {
	msg := theme.Dim.Render("Waiting for LND...")
	msgW := lipgloss.Width(msg)
	lp := (w - msgW) / 2
	if lp < 0 {
		lp = 0
	}
	line := strings.Repeat(" ", lp) + msg
	topPad := h / 2
	if topPad < 1 {
		topPad = 1
	}
	var out []string
	for i := 0; i < topPad; i++ {
		out = append(out, "")
	}
	out = append(out, line)
	return strings.Join(out, "\n")
}
