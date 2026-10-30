package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/virtpriv/node/internal/release"
	"github.com/virtpriv/node/internal/theme"
	"github.com/virtpriv/node/internal/update/protocol"
)

// SettingsScreen opens from the System tab's Settings button. Its one button
// for now, Test release, asks the node to review a release named by its tag
// and hands the answer to Node Updates, where it is approved and installed
// like any other update. Node Updates stays under Updates, its own home.
// The button keeps the width it has beside a second one, so the row has
// room for the next setting.
type SettingsScreen struct {
	ctx    *ScreenContext
	button int
	// release is the Test release form's field, nil while the form is closed.
	release *textinput.Model
	// checking is set while the node prepares the review.
	checking bool
	err      string
	attempt  uint64
}

const settingsTestReleaseButton = "Test release"

// settingsReviewMsg carries the node's answer for a test release back to its
// form: whether an update is still on its way, else the review or the error.
type settingsReviewMsg struct {
	owner      *SettingsScreen
	attempt    uint64
	version    string
	unfinished bool
	review     protocol.Review
	err        error
}

func openSettingsCmd(ctx *ScreenContext) tea.Cmd {
	return func() tea.Msg {
		return openTabMsg{Kind: tabSettings, Label: "Settings", Screen: &SettingsScreen{ctx: ctx}}
	}
}

func (s *SettingsScreen) Init() tea.Cmd { return nil }

func (s *SettingsScreen) HandleMsg(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case tea.PasteMsg:
		if s.release == nil || !s.release.Focused() || s.checking {
			return s, nil
		}
		value := strings.TrimSpace(m.Content)
		if len(value) > s.release.CharLimit || validatePrintableASCII(value) != nil {
			s.err = "Enter a release tag, such as v0.7.0-rc.1."
			return s, nil
		}
		s.release.SetValue(value)
	case settingsReviewMsg:
		if m.owner != s || m.attempt != s.attempt || s.release == nil {
			return s, nil
		}
		s.checking = false
		if m.err != nil {
			s.err = m.err.Error()
			return s, s.release.Focus()
		}
		s.closeForm()
		if m.unfinished {
			// The update on its way is shown. Its tab, when open, is kept.
			screen := NewNodeUpdateScreen(s.ctx)
			return s, func() tea.Msg {
				return openTabMsg{Kind: tabNodeUpdates, Label: "Node Updates", Screen: screen, Parent: tabUpdates}
			}
		}
		screen := newReviewedRelease(s.ctx, m.version, m.review)
		return s, func() tea.Msg {
			return openTabMsg{Kind: tabNodeUpdates, Label: "Node Updates", Screen: screen, Parent: tabUpdates, Replace: true}
		}
	}
	return s, nil
}

func (s *SettingsScreen) closeForm() {
	s.release, s.err, s.button = nil, "", 0
}

func (s *SettingsScreen) HandleKey(k string, msg tea.KeyPressMsg) (Screen, tea.Cmd) {
	if s.release != nil {
		return s.handleFormKey(k, msg)
	}
	switch k {
	case "ctrl+c":
		return s, tea.Quit
	case "backspace":
		return s, emitFocusParent
	case "left":
		return s, emitFocusSidebar
	case "up":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
	case "enter":
		input := textinput.New()
		input.Placeholder = "v0.7.0-rc.1"
		input.CharLimit = 64
		input.SetWidth(40)
		input.Validate = validatePrintableASCII
		input.Prompt = "  "
		applyInputStyles(&input)
		s.release, s.err = &input, ""
		return s, input.Focus()
	}
	return s, nil
}

// handleFormKey moves between the field and the Cancel and Review buttons
// below it. Backspace on the buttons goes back to Settings.
func (s *SettingsScreen) handleFormKey(k string, msg tea.KeyPressMsg) (Screen, tea.Cmd) {
	if k == "ctrl+c" {
		return s, tea.Quit
	}
	if s.checking {
		// Preparing can take minutes over Tor. Cancel leaves it, and the
		// node's late answer, for an attempt no longer current, is dropped.
		if k == "enter" || k == "backspace" {
			s.checking = false
			s.attempt++
			s.closeForm()
		}
		return s, nil
	}
	if s.release.Focused() {
		switch k {
		case "tab", "down", "enter":
			s.release.Blur()
			s.button = 1
			return s, nil
		}
		var cmd tea.Cmd
		*s.release, cmd = s.release.Update(msg)
		return s, cmd
	}
	switch k {
	case "up", "shift+tab":
		return s, s.release.Focus()
	case "left":
		if s.button == 0 {
			return s, emitFocusSidebar
		}
		s.button = 0
	case "right", "tab":
		s.button = 1
	case "backspace":
		s.closeForm()
	case "enter":
		if s.button == 0 {
			s.closeForm()
			return s, nil
		}
		return s, s.review()
	}
	return s, nil
}

// review asks the node for the named release's review. A tag that is not a
// newer release is refused here. An update still on its way, or paused at
// Continue, is not reviewed over: the node is not asked and Node Updates
// shows that update. After an update that needs Retry, or when the status
// cannot be read, the node decides: it refuses a review while an unfinished
// update needs a repair release.
func (s *SettingsScreen) review() tea.Cmd {
	version := strings.TrimPrefix(strings.TrimSpace(s.release.Value()), "v")
	if newer, err := release.Newer(s.ctx.Version, version); err != nil || !newer {
		s.err = "Enter a release newer than v" + s.ctx.Version + "."
		if !release.ValidVersion(s.ctx.Version) {
			s.err = "This development build cannot install a release."
		}
		return s.release.Focus()
	}
	s.checking, s.err = true, ""
	s.attempt++
	msg := settingsReviewMsg{owner: s, attempt: s.attempt, version: version}
	client := s.ctx.NodeUpdates
	return func() tea.Msg {
		if client == nil {
			msg.err = errNodeUpdatesUnavailable
			return msg
		}
		if st, err := client.Status(); err == nil && st.Active && (st.Phase != "failed" || expectedStop(st)) {
			msg.unfinished = true
			return msg
		}
		msg.review, msg.err = client.Prepare(version)
		return msg
	}
}

func (s *SettingsScreen) View(w, h int) string {
	p := newPane(w)
	if s.release != nil {
		p.title(theme.Header, "Test Release")
		p.input("Release tag:", s.release.View(), s.release.Focused())
		switch {
		case s.checking:
			p.blank()
			p.line(" Downloading and verifying the release plan...")
		case s.err != "":
			p.blank()
			p.fitText(lipgloss.NewStyle(), s.err, nodeUpdateActionRows)
		}
		if s.checking {
			return p.renderWithBottomButtons([]string{"Cancel"}, 0, s.ctx.ContentFocused, h)
		}
		return p.renderWithBottomButtons([]string{"Cancel", "Review"}, s.button,
			s.ctx.ContentFocused && !s.release.Focused(), h)
	}
	p.title(theme.Header, "Settings")
	p.line(renderButtons([]string{settingsTestReleaseButton}, s.button, s.ctx.ContentFocused, (w-4)/2+2))
	p.blank()
	p.field("Test release: ", "install a release by its tag.")
	return p.render()
}

func (s *SettingsScreen) HelpBindings() []key.Binding {
	if s.release != nil {
		switch {
		case s.checking:
			return []key.Binding{kEnter, kBack, kQuit}
		case s.release.Focused():
			return []key.Binding{kTabButtons, kQuit}
		}
		return append(buttonNav(s.button), kEnter, kShiftTabInput, kBack, kQuit)
	}
	binds := []key.Binding{kSidebar, kEnter}
	if s.ctx.HasTabs {
		binds = append(binds, kUpTabBar)
	}
	return append(binds, kBack, kQuit)
}
