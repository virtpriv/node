package tui

import (
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
	"github.com/virtpriv/node/internal/theme"
)

// ── Character filters ────────────────────────────────────
// These accept incomplete input while typing. They do not establish semantic
// validity. Operation boundaries must validate the completed values.

func validatePrintableASCII(s string) error {
	for _, ch := range s {
		if ch < 32 || ch >= 127 {
			return errInvalidChar{}
		}
	}
	return nil
}

// errInvalidChar is a sentinel returned by validators to reject input.
type errInvalidChar struct{}

func (errInvalidChar) Error() string { return "invalid character" }

// ── Input styles ─────────────────────────────────────────

// applyInputStyles gives a field the theme's colors. Typed text and the prompt
// take the value color whether the field has focus or not, so only hints are
// dim. The installer's password fields use the same typed text, hint and
// cursor colors. The cursor, and the marker on screens that draw one beside a
// field, show which field has focus.
func applyInputStyles(ti *textinput.Model) {
	s := textinput.DefaultStyles(theme.IsDark())
	for _, st := range []*textinput.StyleState{&s.Focused, &s.Blurred} {
		st.Text = lipgloss.NewStyle().Foreground(theme.ColorPrimary)
		st.Prompt = lipgloss.NewStyle().Foreground(theme.ColorPrimary)
		st.Placeholder = st.Placeholder.Foreground(theme.ColorDim)
	}
	s.Cursor.Color = theme.ColorCursor
	ti.SetStyles(s)
}

// restyleScreenFields gives an open screen's fields the current theme's
// colors. A field keeps the colors it was made with, so a theme switch passes
// every open screen through here. A screen that gets a field joins this list.
// Only the colors change. The text, the cursor position and the focus stay.
func restyleScreenFields(screen Screen) {
	var fields []*textinput.Model
	switch s := screen.(type) {
	case *SSHKeyAddScreen:
		fields = []*textinput.Model{&s.keyInput}
	case *SettingsScreen:
		if s.release != nil {
			fields = []*textinput.Model{s.release}
		}
	}
	for _, field := range fields {
		applyInputStyles(field)
	}
}

// ── Factory functions ────────────────────────────────────

func newSSHKeyInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "ssh-ed25519 AAAA..."
	ti.CharLimit = 2000
	ti.SetWidth(60)
	ti.Prompt = "  "
	applyInputStyles(&ti)
	ti.Focus()
	return ti
}
