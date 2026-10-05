package tui

import (
	"bytes"
	"strings"

	"charm.land/lipgloss/v2"
	qrterminal "github.com/mdp/qrterminal/v3"
	"rsc.io/qr"

	"github.com/virtpriv/node/internal/theme"
)

// ── Fullscreen overlays ────────────────────────────────
// These are Model-owned views that take over the entire
// screen (not section content). Triggered by setting
// m.subview to svQR or svFullURL.

func (m Model) viewQR() string {
	if m.connectionDisplayUnavailable() {
		return m.viewConnectionUnavailable()
	}
	if m.qrCopyOverlay != nil && !m.qrCopyDisplayCurrent(m.qrCopyOverlay) {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			theme.Warn.Render("QR information changed or is unavailable.\nPress enter to return."))
	}
	uri := m.urlTarget
	label := m.qrLabel

	if uri == "" {
		return lipgloss.Place(
			m.width, m.height,
			lipgloss.Center, lipgloss.Center,
			theme.Warn.Render(
				"QR not available."))
	}

	code := renderQRCode(uri)
	var lines []string
	lines = append(lines,
		theme.Header.Render(label))
	lines = append(lines, "")
	if code != "" {
		lines = append(lines, code)
	} else {
		lines = append(lines, theme.Warn.Render("QR not available for this text."))
	}
	lines = append(lines, "")
	lines = append(lines, theme.Footer.Render(
		"enter back"))
	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		lipgloss.JoinVertical(
			lipgloss.Left, lines...))
}

func (m Model) viewFullURL() string {
	if m.connectionDisplayUnavailable() {
		return m.viewConnectionUnavailable()
	}
	title := theme.Header.Render(
		"Full URL - Copy and paste into Tor Browser")
	hint := theme.Dim.Render(
		"Select and copy. Press enter to go back.")
	content := lipgloss.JoinVertical(lipgloss.Left,
		"", title, "", hint, "", m.urlTarget, "")
	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center, content)
}

func (m Model) viewConnectionUnavailable() string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		theme.Warn.Render("Connection information changed or is unavailable.\nPress enter to return."))
}

// ── QR and encoding utilities ──────────────────────────

// qrterminal discards encoding errors before dereferencing the code. Check with
// its pinned encoder first so oversized payloads cannot panic the TUI.
func renderQRCode(data string) string {
	if _, err := qr.Encode(data, qr.L); err != nil {
		return ""
	}
	var buf bytes.Buffer
	config := qrterminal.Config{
		Level:          qrterminal.L,
		Writer:         &buf,
		HalfBlocks:     true,
		BlackChar:      qrterminal.BLACK_BLACK,
		WhiteChar:      qrterminal.WHITE_WHITE,
		BlackWhiteChar: qrterminal.BLACK_WHITE,
		WhiteBlackChar: qrterminal.WHITE_BLACK,
		QuietZone:      2,
	}
	qrterminal.GenerateWithConfig(data, config)
	return strings.TrimRight(buf.String(), "\n")
}
