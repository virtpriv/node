package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/theme"
)

// ── Character filters ────────────────────────────────────
// These accept incomplete input while typing. They do not establish semantic
// validity. Operation boundaries must validate the completed values.

func isBolt11Char(ch rune) bool {
	return (ch >= 'a' && ch <= 'z') ||
		(ch >= '0' && ch <= '9') ||
		(ch >= 'A' && ch <= 'Z')
}

func validateBolt11Chars(s string) error {
	for _, ch := range s {
		if !isBolt11Char(ch) {
			return errInvalidChar{}
		}
	}
	return nil
}

func validateHexChars(s string) error {
	for _, ch := range s {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			return errInvalidChar{}
		}
	}
	return nil
}

func validateHostChars(s string) error {
	for _, ch := range s {
		if ch < 32 || ch > 126 {
			return errInvalidChar{}
		}
	}
	return nil
}

func validateSyncthingIDChars(s string) error {
	for _, ch := range s {
		upper := ch
		if ch >= 'a' && ch <= 'z' {
			upper = ch - 32
		}
		if !((upper >= 'A' && upper <= 'Z') || (upper >= '0' && upper <= '9') || upper == '-') {
			return errInvalidChar{}
		}
	}
	return nil
}

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
	case *OnChainSendScreen:
		fields = []*textinput.Model{&s.addrInput, &s.amtInput.ti, &s.labelInput, &s.feeInput.ti}
	case *OnChainHomeScreen:
		fields = []*textinput.Model{&s.labelInput}
	case *SendScreen:
		fields = []*textinput.Model{&s.sendInput}
	case *ReceiveScreen:
		fields = []*textinput.Model{&s.amountInput.ti, &s.memoInput}
	case *ChannelOpenScreen:
		fields = []*textinput.Model{&s.pubkeyInput, &s.hostInput, &s.amountInput.ti, &s.feeInput.ti}
	case *ChannelCloseScreen:
		fields = []*textinput.Model{&s.feeInput.ti}
	case *ChannelDetailScreen:
		if s.closeScreen != nil {
			restyleScreenFields(s.closeScreen)
		}
	case *SyncthingPairScreen:
		fields = []*textinput.Model{&s.input}
	case *SSHKeyAddScreen:
		fields = []*textinput.Model{&s.keyInput}
	case *AutoUnlockScreen:
		fields = []*textinput.Model{&s.pw1, &s.pw2}
	case *P2PUpgradeScreen:
		fields = []*textinput.Model{&s.input}
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

func newSendPayReqInput(network string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "invoice..."
	if profile, err := config.NetworkConfigFromName(network); err == nil {
		ti.Placeholder = profile.InvoicePlaceholder
	}
	ti.CharLimit = 1500
	ti.SetWidth(58)
	ti.Validate = validateBolt11Chars
	ti.Prompt = "  "
	applyInputStyles(&ti)
	ti.Focus()
	return ti
}

func newRecvMemoInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "optional memo"
	ti.CharLimit = 100
	ti.SetWidth(40)
	ti.Validate = validatePrintableASCII
	ti.Prompt = "  "
	applyInputStyles(&ti)
	return ti
}

func newChanPubkeyInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "pubkey (66 hex chars)"
	ti.CharLimit = 66
	ti.SetWidth(66)
	ti.Validate = validateHexChars
	ti.Prompt = "  "
	applyInputStyles(&ti)
	ti.Focus()
	return ti
}

func newChanHostInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "host:port"
	ti.CharLimit = 80
	ti.SetWidth(50)
	ti.Validate = validateHostChars
	ti.Prompt = "  "
	applyInputStyles(&ti)
	return ti
}

func newSyncthingIDInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX"
	ti.CharLimit = 63
	ti.SetWidth(63)
	ti.Validate = validateSyncthingIDChars
	ti.Prompt = "  "
	applyInputStyles(&ti)
	ti.Focus()
	return ti
}

func newOnChainAddrInput(network string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "address..."
	if profile, err := config.NetworkConfigFromName(network); err == nil {
		ti.Placeholder = profile.AddressPlaceholder
	}
	ti.CharLimit = 90
	ti.SetWidth(62)
	ti.Validate = validateOnChainAddrChars
	ti.Prompt = "  "
	applyInputStyles(&ti)
	ti.Focus()
	return ti
}

func newOCSendLabelInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "optional label"
	ti.CharLimit = 64
	ti.SetWidth(40)
	ti.Validate = validatePrintableASCII
	ti.Prompt = "  "
	applyInputStyles(&ti)
	return ti
}

func NewFeeInput() AmountInput {
	ti := textinput.New()
	ti.Placeholder = "sat/vB"
	ti.CharLimit = 10
	ti.SetWidth(12)
	ti.Prompt = "  "
	applyInputStyles(&ti)
	return AmountInput{ti: ti}
}

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

func validateOnChainAddrChars(s string) error {
	for _, ch := range s {
		if !((ch >= 'a' && ch <= 'z') ||
			(ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9')) {
			return errInvalidChar{}
		}
	}
	return nil
}

// newDetailField creates a field for editing a value in
// place, such as a transaction's label, at the width the
// caller gives. It starts without focus.
func newDetailField(value string, w int) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 0 // no limit
	ti.SetWidth(w)
	applyInputStyles(&ti)
	ti.SetValue(value)
	ti.Blur()
	return ti
}

// ── Helpers ──────────────────────────────────────────────

// syncthingIDValue returns the uppercased value of the syncthing input.
func syncthingIDValue(ti textinput.Model) string {
	return strings.ToUpper(ti.Value())
}
