package theme

import (
	"charm.land/lipgloss/v2"
)

// ── Theme state ────────────────────────────────────────
//
// dark tracks the current mode. Call Init(dark) once at
// startup, then Toggle() to flip. The theme colors and
// styles are reassigned by applyColors/applyStyles.

var dark = true

// Init sets the initial theme mode and rebuilds all
// colors and styles. Call once when a program starts.
func Init(isDark bool) {
	dark = isDark
	applyColors()
	applyStyles()
}

// Toggle flips the theme and returns the new mode name
// ("dark" or "light") for config persistence.
func Toggle() string {
	dark = !dark
	applyColors()
	applyStyles()
	if dark {
		return "dark"
	}
	return "light"
}

// IsDark returns the current mode.
func IsDark() bool { return dark }

// ThemeIcon returns the icon for the theme toggle. It
// shows the theme a press switches to, ☼ in dark for
// light and ☽ in light for dark.
func ThemeIcon() string {
	if dark {
		return "☼"
	}
	return "☽"
}

// ── Color palette ───────────────────────────────────────
//
// Every color used in the TUI and the installer lives here.
// The theme colors below start with their dark values and
// are reassigned by applyColors() for the current mode. The
// installer's colors after them are dark only. No file
// outside theme.go should contain a raw lipgloss.Color
// literal.

var (
	ColorPrimary = lipgloss.Color("255")
	ColorLabel   = lipgloss.Color("253")
	ColorDim     = lipgloss.Color("246")
	ColorGrayed  = lipgloss.Color("243")
	ColorFaint   = lipgloss.Color("240")
	ColorBorder  = lipgloss.Color("248")
	ColorSuccess = lipgloss.Color("34")
	ColorDanger  = lipgloss.Color("196")
	ColorSpinner = lipgloss.Color("172")
	ColorBtnBg   = lipgloss.Color("237")
	ColorBtnFg   = lipgloss.Color("255")
	ColorCursor  = lipgloss.Color("251")

	ColorUpdate = lipgloss.Color("34")

	// ColorBackground and ColorText are the window's own colors while the
	// program runs: the terminal's background and its default text color,
	// which text with no role of its own takes. Bubble Tea sets them through
	// the view and gives the terminal its own back on exit and around every
	// hand over.
	ColorBackground = lipgloss.Color("234")
	ColorText       = lipgloss.Color("253")
)

// The installer's shaded boxes (the selected password field and the login
// command, then the other field) and the hint text in the selected field,
// lighter than ColorDim because its box is lighter. The installer is always
// dark, so these have dark values only.
var (
	ColorInstallShade    = lipgloss.Color("238")
	ColorInstallShadeDim = lipgloss.Color("235")
	ColorInstallHint     = lipgloss.Color("251")
)

func applyColors() {
	if dark {
		ColorPrimary = lipgloss.Color("255")
		ColorLabel = lipgloss.Color("253")
		ColorDim = lipgloss.Color("246")
		ColorGrayed = lipgloss.Color("243")
		ColorFaint = lipgloss.Color("240")
		ColorBorder = lipgloss.Color("248")
		ColorSuccess = lipgloss.Color("34")
		ColorDanger = lipgloss.Color("196")
		ColorSpinner = lipgloss.Color("172")
		ColorBtnBg = lipgloss.Color("237")
		ColorBtnFg = lipgloss.Color("255")
		ColorCursor = lipgloss.Color("251")
		ColorUpdate = lipgloss.Color("34")
		ColorBackground = lipgloss.Color("234")
		ColorText = lipgloss.Color("253")
	} else {
		ColorPrimary = lipgloss.Color("232")
		ColorLabel = lipgloss.Color("234")
		ColorDim = lipgloss.Color("241")
		ColorGrayed = lipgloss.Color("244")
		ColorFaint = lipgloss.Color("247")
		ColorBorder = lipgloss.Color("238")
		ColorSuccess = lipgloss.Color("28")
		ColorDanger = lipgloss.Color("160")
		ColorSpinner = lipgloss.Color("130")
		ColorBtnBg = lipgloss.Color("252")
		ColorBtnFg = lipgloss.Color("232")
		ColorCursor = lipgloss.Color("236")
		ColorUpdate = lipgloss.Color("28")
		ColorBackground = lipgloss.Color("255")
		ColorText = lipgloss.Color("234")
	}
}

// ── Core styles ─────────────────────────────────────────

var (
	Header  lipgloss.Style
	Label   lipgloss.Style
	Value   lipgloss.Style
	Mono    lipgloss.Style
	Dim     lipgloss.Style
	Grayed  lipgloss.Style
	Success lipgloss.Style
	Warning lipgloss.Style
	Action  lipgloss.Style
)

// ── Status indicators ───────────────────────────────────

var (
	GreenDot lipgloss.Style
	RedDot   lipgloss.Style
)

// ── Buttons ─────────────────────────────────────────────

var (
	BtnNormal  lipgloss.Style
	BtnFocused lipgloss.Style
)

// ── Layout constants ────────────────────────────────────

// The node TUI's frame. The installer draws the same shape, so both read the
// measurements here and the two cannot drift apart.
//
// From the top, the frame has its border, the top bar, a line, the sidebar
// beside the content, and the border. The sidebar holds the sections from
// its top, each SectionRows tall and closed by a line, and the theme icon in
// its last row. Screens draw in every row beside it, so their last row is the
// one just above the frame's bottom border. ContentRows is a number of its
// own, because the screens are laid out for it.
const (
	FrameWidth   = 82
	SidebarWidth = 12
	TopBarRows   = 2
	SectionRows  = 5
	ContentRows  = 31
	FrameHeight  = 1 + TopBarRows + 1 + ContentRows + 1
)

// Sections are the sidebar's sections, top to bottom.
var Sections = [1]string{"System"}

// ── Help bar ────────────────────────────────────────────

var (
	HelpKey  lipgloss.Style
	HelpDesc lipgloss.Style
	HelpSep  lipgloss.Style
)

// ── Inline style helpers ────────────────────────────────

var (
	NavItem   lipgloss.Style
	NavActive lipgloss.Style
	NavCursor lipgloss.Style

	TableHeader lipgloss.Style

	FrameBorder lipgloss.Style

	CardBorder lipgloss.Style
)

func applyStyles() {
	// Core
	Header = lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	Label = lipgloss.NewStyle().Foreground(ColorLabel)
	Value = lipgloss.NewStyle().Foreground(ColorPrimary)
	Mono = lipgloss.NewStyle().Foreground(ColorPrimary)
	Dim = lipgloss.NewStyle().Foreground(ColorDim)
	Grayed = lipgloss.NewStyle().Foreground(ColorGrayed)
	Success = lipgloss.NewStyle().Foreground(ColorSuccess).Bold(true)
	Warning = lipgloss.NewStyle().Foreground(ColorDanger).Bold(true)
	Action = lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)

	// Status
	GreenDot = lipgloss.NewStyle().Foreground(ColorSuccess)
	RedDot = lipgloss.NewStyle().Foreground(ColorDanger)

	// Buttons
	BtnNormal = lipgloss.NewStyle().
		Foreground(ColorBtnFg).
		Background(ColorBtnBg).
		Bold(true).
		Padding(0, 1)
	BtnFocused = lipgloss.NewStyle().
		Foreground(ColorBtnFg).
		Background(ColorBtnBg).
		Reverse(true).
		Bold(true).
		Padding(0, 1)

	// Help bar
	HelpKey = lipgloss.NewStyle().
		Foreground(ColorPrimary).
		Bold(true)
	HelpDesc = lipgloss.NewStyle().
		Foreground(ColorDim)
	HelpSep = lipgloss.NewStyle().
		Foreground(ColorFaint)

	// Nav
	NavItem = lipgloss.NewStyle().Foreground(ColorPrimary)
	NavActive = lipgloss.NewStyle().
		Foreground(ColorPrimary).
		Reverse(true).
		Bold(true)
	NavCursor = lipgloss.NewStyle().Foreground(ColorCursor).Bold(true)

	// Table
	TableHeader = lipgloss.NewStyle().Foreground(ColorLabel).Bold(true)

	// Frame
	FrameBorder = lipgloss.NewStyle().Foreground(ColorBorder)

	// Cards
	CardBorder = lipgloss.NewStyle().Foreground(ColorGrayed)
}

// init sets up the default dark theme.
func init() {
	applyColors()
	applyStyles()
}
