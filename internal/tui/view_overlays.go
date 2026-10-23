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
// m.subview to svQR or svFullURL. View centres them.

func (m Model) viewQR() string {
	if m.connectionDisplayUnavailable() {
		return m.viewConnectionUnavailable()
	}
	if m.qrCopyOverlay != nil && !m.qrCopyDisplayCurrent(m.qrCopyOverlay) {
		return theme.Warn.Render("QR information changed or is unavailable.\nPress enter to return.")
	}
	uri := m.urlTarget
	label := m.qrLabel

	if uri == "" {
		return theme.Warn.Render("QR not available.")
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
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (m Model) viewFullURL() string {
	if m.connectionDisplayUnavailable() {
		return m.viewConnectionUnavailable()
	}
	title := theme.Header.Render(
		"Full URL, copy and paste into Tor Browser")
	hint := theme.Dim.Render(
		"Select and copy. Press enter to go back.")
	content := lipgloss.JoinVertical(lipgloss.Left,
		"", title, "", hint, "", m.urlTarget, "")
	return content
}

func (m Model) viewConnectionUnavailable() string {
	return theme.Warn.Render("Connection information changed or is unavailable.\nPress enter to return.")
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
	// The half blocks draw light modules in the foreground colour and dark
	// ones in the background. Both are set, so the code reads the same on
	// the theme's background and in any terminal.
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for i, l := range lines {
		style := theme.QRCode
		// Two module rows share a line, and a code has an odd count, so the
		// last line's lower half lies below the code. It keeps the screen's
		// background instead of a black strip.
		if i == len(lines)-1 && strings.Trim(l, qrterminal.WHITE_BLACK) == "" {
			style = style.UnsetBackground()
		}
		lines[i] = style.Render(l)
	}
	return strings.Join(lines, "\n")
}
