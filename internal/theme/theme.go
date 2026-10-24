package theme

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// ── Theme state ────────────────────────────────────────
//
// dark tracks the current mode. Call Init(dark) once at
// startup, then Toggle() to flip. All color vars and
// style vars are reassigned by applyColors/applyStyles.

var dark = true

// Init sets the initial theme mode and rebuilds all
// colors and styles. Call once from NewModel.
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

// ThemeIcon returns the icon for the theme toggle.
// Shows what you'd switch TO: ☼ when dark (switch to
// light), ☽ when light (switch to dark).
func ThemeIcon() string {
	if dark {
		return "☼"
	}
	return "☽"
}

// ── Color palette ───────────────────────────────────────
//
// Every color used in the TUI lives here. All colors are
// reassigned by applyColors() based on the current mode.
// No file outside theme.go should contain a raw
// lipgloss.Color literal.

var (
	ColorPrimary       = lipgloss.Color("255")
	ColorLabel         = lipgloss.Color("253")
	ColorDim           = lipgloss.Color("246")
	ColorGrayed        = lipgloss.Color("243")
	ColorFaint         = lipgloss.Color("240")
	ColorBorder        = lipgloss.Color("248")
	ColorSuccess       = lipgloss.Color("34")
	ColorDanger        = lipgloss.Color("196")
	ColorBitcoin       = lipgloss.Color("172")
	ColorLightning     = lipgloss.Color("135")
	ColorBtnBg         = lipgloss.Color("255")
	ColorBtnFg         = lipgloss.Color("232")
	ColorTabActiveBg   = lipgloss.Color("252")
	ColorTabActiveFg   = lipgloss.Color("232")
	ColorTabInactiveBg = lipgloss.Color("242")
	ColorTabInactiveFg = lipgloss.Color("251")
	ColorTitleFg       = lipgloss.Color("232")
	ColorTitleBg       = lipgloss.Color("252")
	ColorCursor        = lipgloss.Color("251")

	// Channel bar
	ColorChanLocal     = lipgloss.Color("34")
	ColorChanRemote    = lipgloss.Color("63")
	ColorChanLocalDim  = lipgloss.Color("22")
	ColorChanRemoteDim = lipgloss.Color("237")

	ColorCheck  = lipgloss.Color("34")
	ColorUpdate = lipgloss.Color("34")

	// ColorBackground and ColorText are the window's own colors while the
	// program runs: the terminal's background and its default text color,
	// which text with no role of its own takes. Bubble Tea sets them through
	// the view and gives the terminal its own back on exit and around every
	// hand over.
	ColorBackground = lipgloss.Color("234")
	ColorText       = lipgloss.Color("253")
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
		ColorBitcoin = lipgloss.Color("172")
		ColorLightning = lipgloss.Color("135")
		ColorBtnBg = lipgloss.Color("237")
		ColorBtnFg = lipgloss.Color("255")
		ColorTabActiveBg = lipgloss.Color("252")
		ColorTabActiveFg = lipgloss.Color("232")
		ColorTabInactiveBg = lipgloss.Color("242")
		ColorTabInactiveFg = lipgloss.Color("251")
		ColorTitleFg = lipgloss.Color("232")
		ColorTitleBg = lipgloss.Color("252")
		ColorCursor = lipgloss.Color("251")
		ColorChanLocal = lipgloss.Color("34")
		ColorChanRemote = lipgloss.Color("63")
		ColorChanLocalDim = lipgloss.Color("22")
		ColorChanRemoteDim = lipgloss.Color("240")
		ColorCheck = lipgloss.Color("34")
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
		ColorBitcoin = lipgloss.Color("130")
		ColorLightning = lipgloss.Color("91")
		ColorBtnBg = lipgloss.Color("252")
		ColorBtnFg = lipgloss.Color("232")
		ColorTabActiveBg = lipgloss.Color("247")
		ColorTabActiveFg = lipgloss.Color("235")
		ColorTabInactiveBg = lipgloss.Color("245")
		ColorTabInactiveFg = lipgloss.Color("238")
		ColorTitleFg = lipgloss.Color("235")
		ColorTitleBg = lipgloss.Color("247")
		ColorCursor = lipgloss.Color("236")
		ColorChanLocal = lipgloss.Color("34")
		ColorChanRemote = lipgloss.Color("63")
		ColorChanLocalDim = lipgloss.Color("114")
		ColorChanRemoteDim = lipgloss.Color("245")
		ColorCheck = lipgloss.Color("28")
		ColorUpdate = lipgloss.Color("28")
		ColorBackground = lipgloss.Color("255")
		ColorText = lipgloss.Color("234")
	}
}

// ── Core styles ─────────────────────────────────────────

var (
	Title   lipgloss.Style
	Header  lipgloss.Style
	Label   lipgloss.Style
	Value   lipgloss.Style
	Mono    lipgloss.Style
	Dim     lipgloss.Style
	Grayed  lipgloss.Style
	Good    lipgloss.Style
	Success lipgloss.Style
	Warn    lipgloss.Style
	Warning lipgloss.Style
	Action  lipgloss.Style
	Footer  lipgloss.Style
)

// ── Branded ─────────────────────────────────────────────

var (
	Bitcoin   lipgloss.Style
	Lightning lipgloss.Style
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

// ── Tabs ────────────────────────────────────────────────

var (
	ActiveTab   lipgloss.Style
	InactiveTab lipgloss.Style
)

// ── Borders ─────────────────────────────────────────────

var (
	Box            lipgloss.Style
	SelectedBorder lipgloss.Style
	NormalBorder   lipgloss.Style
	GrayedBorder   lipgloss.Style
)

// ── Install progress ────────────────────────────────────

var (
	ProgTitle   lipgloss.Style
	ProgBox     lipgloss.Style
	ProgDone    lipgloss.Style
	ProgRunning lipgloss.Style
	ProgPending lipgloss.Style
	ProgFail    lipgloss.Style
)

// ── Setup wizard ────────────────────────────────────────

var (
	SetupSection    lipgloss.Style
	SetupSelected   lipgloss.Style
	SetupUnselected lipgloss.Style
	SetupValue      lipgloss.Style
	SummaryKey      lipgloss.Style
	SummaryVal      lipgloss.Style
)

// ── Layout constants ────────────────────────────────────

// The node TUI's frame. The installer draws the same shape, so both read the
// measurements here and the two cannot drift apart.
//
// From the top: the frame's border, the top bar, a line, then the sidebar
// beside the content, a bottom row under the sidebar for the theme icon, and
// the border. The sidebar holds the five sections, each SectionRows tall and
// closed by a line, so its rows match the content's.
const (
	FrameWidth   = 82
	SidebarWidth = 12
	TopBarRows   = 2
	SectionRows  = 5
	ContentRows  = len(Sections) * (SectionRows + 1)
	FrameHeight  = 1 + TopBarRows + 1 + ContentRows + 1 + 1
)

// Sections are the sidebar's five sections, top to bottom.
var Sections = [5]string{"Channels", "Wallet", "On-Chain", "Add-On", "System"}

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
	TableDim    lipgloss.Style

	FrameBorder lipgloss.Style
	QRCode      lipgloss.Style

	AddonBorderNormal lipgloss.Style
	AddonTitleNormal  lipgloss.Style
	AddonTitleActive  lipgloss.Style
)

func applyStyles() {
	// Core
	Title = lipgloss.NewStyle().Bold(true).
		Foreground(ColorTitleFg).
		Background(ColorTitleBg).Padding(0, 2)
	Header = lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	Label = lipgloss.NewStyle().Foreground(ColorLabel)
	Value = lipgloss.NewStyle().Foreground(ColorPrimary)
	Mono = lipgloss.NewStyle().Foreground(ColorPrimary)
	Dim = lipgloss.NewStyle().Foreground(ColorDim)
	Grayed = lipgloss.NewStyle().Foreground(ColorGrayed)
	Good = lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	Success = lipgloss.NewStyle().Foreground(ColorSuccess).Bold(true)
	Warn = lipgloss.NewStyle().Foreground(ColorLabel)
	Warning = lipgloss.NewStyle().Foreground(ColorDanger).Bold(true)
	Action = lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	Footer = lipgloss.NewStyle().Foreground(ColorDim)

	// Branded
	Bitcoin = lipgloss.NewStyle().Foreground(ColorBitcoin).Bold(true)
	Lightning = lipgloss.NewStyle().Foreground(ColorLightning).Bold(true)

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

	// Tabs
	ActiveTab = lipgloss.NewStyle().Bold(true).
		Foreground(ColorTabActiveFg).
		Background(ColorTabActiveBg).Padding(0, 2)
	InactiveTab = lipgloss.NewStyle().
		Foreground(ColorTabInactiveFg).
		Background(ColorTabInactiveBg).Padding(0, 2)

	// Borders
	Box = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder)
	SelectedBorder = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary)
	NormalBorder = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder)
	GrayedBorder = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorGrayed)

	// Progress
	ProgTitle = lipgloss.NewStyle().Bold(true).
		Foreground(ColorTitleFg).
		Background(ColorTitleBg).Padding(0, 2)
	ProgBox = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).Padding(1, 2)
	ProgDone = lipgloss.NewStyle().Foreground(ColorPrimary)
	ProgRunning = lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	ProgPending = lipgloss.NewStyle().Foreground(ColorDim)
	ProgFail = lipgloss.NewStyle().Foreground(ColorDanger).Bold(true)

	// Setup
	SetupSection = lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	SetupSelected = lipgloss.NewStyle().
		Foreground(ColorPrimary).
		Reverse(true).
		Bold(true)
	SetupUnselected = lipgloss.NewStyle().Foreground(ColorLabel)
	SetupValue = lipgloss.NewStyle().Foreground(ColorPrimary)
	SummaryKey = lipgloss.NewStyle().Foreground(ColorLabel).
		Width(16).Align(lipgloss.Right)
	SummaryVal = lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)

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
	TableDim = lipgloss.NewStyle().Foreground(ColorGrayed)

	// Frame
	FrameBorder = lipgloss.NewStyle().Foreground(ColorBorder)

	// QR codes are black on white in both themes, for scanners.
	QRCode = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color("16"))

	// Addon cards
	AddonBorderNormal = lipgloss.NewStyle().Foreground(ColorGrayed)
	AddonTitleNormal = lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	AddonTitleActive = lipgloss.NewStyle().
		Foreground(ColorPrimary).
		Reverse(true).
		Bold(true)
}

// init sets up the default dark theme.
func init() {
	applyColors()
	applyStyles()
}

// Hints draws a help line from key and description pairs: the key bright,
// its description dim, thin bars between them.
func Hints(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, HelpKey.Render(pairs[i])+" "+HelpDesc.Render(pairs[i+1]))
	}
	return strings.Join(parts, HelpSep.Render(" │ "))
}
