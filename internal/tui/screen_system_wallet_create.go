package tui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/theme"
)

type walletCreateStep int

const (
	walletConfirm walletCreateStep = iota
	walletWaiting
	walletExec
	walletFinalizing
	walletInitializing
	walletResult
)

type walletCreationAttempt struct {
	execution    app.WalletExecution
	finalization uint64
}

type walletLNDReadyMsg struct {
	owner   *WalletCreateScreen
	attempt *walletCreationAttempt
	network string
	err     error
}

type walletExecDoneMsg struct {
	owner     *WalletCreateScreen
	attempt   *walletCreationAttempt
	execution app.WalletExecution
}

type walletFinalizedMsg struct {
	owner    *WalletCreateScreen
	attempt  *walletCreationAttempt
	revision uint64
	result   app.WalletCreationResult
}

type closeWalletCreateMsg struct {
	owner    *WalletCreateScreen
	attempt  *walletCreationAttempt
	revision uint64
}
type continueWalletCreateMsg struct {
	owner   *WalletCreateScreen
	attempt *walletCreationAttempt
}

// WalletCreateScreen owns presentation and terminal handoff. Application calls
// own readiness and finalization; every result identifies its original attempt.
type WalletCreateScreen struct {
	ctx       *ScreenContext
	step      walletCreateStep
	btnIdx    int
	attempt   *walletCreationAttempt
	result    app.WalletCreationResult
	clientErr error
}

func NewWalletCreateScreen(ctx *ScreenContext) *WalletCreateScreen {
	return &WalletCreateScreen{ctx: ctx, btnIdx: 1}
}

func (s *WalletCreateScreen) Init() tea.Cmd { return nil }

func walletCreationBusy(screen Screen) bool {
	s, ok := screen.(*WalletCreateScreen)
	return ok && s != nil && (s.step == walletWaiting || s.step == walletExec || s.step == walletFinalizing || s.step == walletInitializing)
}

func (s *WalletCreateScreen) closeCmd() tea.Cmd {
	attempt, revision := s.attempt, uint64(0)
	if attempt != nil {
		revision = attempt.finalization
	}
	return func() tea.Msg { return closeWalletCreateMsg{owner: s, attempt: attempt, revision: revision} }
}

func (s *WalletCreateScreen) HandleKey(keyStr string, msg tea.KeyPressMsg) (Screen, tea.Cmd) {
	if keyStr == "ctrl+c" {
		return s, tea.Quit
	}
	if walletCreationBusy(s) {
		switch keyStr {
		case "left":
			return s, emitFocusSidebar
		case "up", "shift+tab":
			return s, emitFocusTabBar
		}
		return s, nil
	}
	if s.step == walletResult {
		switch keyStr {
		case "left", "right":
			if s.hasRecoveryAction() {
				s.btnIdx = 1 - s.btnIdx
			}
		case "enter":
			if s.btnIdx == 1 && s.hasRecoveryAction() {
				if s.retrySetup() {
					return s, s.finalizeCmd()
				}
				return s, func() tea.Msg { return continueWalletCreateMsg{owner: s, attempt: s.attempt} }
			}
			return s, s.closeCmd()
		case "up", "shift+tab":
			return s, emitFocusTabBar
		case "backspace":
			return s, emitFocusParent
		}
		return s, nil
	}
	switch keyStr {
	case "left":
		if s.btnIdx > 0 {
			s.btnIdx--
			return s, nil
		}
		return s, emitFocusSidebar
	case "right":
		s.btnIdx = 1
	case "up", "shift+tab":
		if s.ctx.HasTabs {
			return s, emitFocusTabBar
		}
	case "enter":
		if s.btnIdx == 0 {
			return s, s.closeCmd()
		}
		return s, s.startWaitingForLND()
	case "backspace":
		return s, emitFocusParent
	}
	return s, nil
}

func (s *WalletCreateScreen) startWaitingForLND() tea.Cmd {
	if !s.ctx.walletKnown() || s.ctx.walletExists() {
		s.step = walletResult
		s.btnIdx = 0
		s.result = app.WalletCreationResult{Err: fmt.Errorf("wallet must be known absent before creation")}
		if s.ctx.walletKnown() {
			s.result.Presence = app.WalletPresent
		}
		return nil
	}
	if owner := s.ctx.walletCreationOwner; owner != nil && owner != s {
		return nil
	}
	s.ctx.walletCreationOwner = s
	s.ctx.invalidateWalletObservations()
	s.step = walletWaiting
	s.attempt = &walletCreationAttempt{}
	attempt, network, workflow := s.attempt, s.ctx.Cfg.Network, s.ctx.walletCreation()
	return func() tea.Msg {
		readyNetwork, err := workflow.Prepare(network)
		return walletLNDReadyMsg{owner: s, attempt: attempt, network: readyNetwork, err: err}
	}
}

func (s *WalletCreateScreen) finalizeCmd() tea.Cmd {
	s.step = walletFinalizing
	s.attempt.finalization++
	attempt, revision, execution := s.attempt, s.attempt.finalization, s.attempt.execution
	workflow := s.ctx.walletCreation()
	return func() tea.Msg {
		return walletFinalizedMsg{owner: s, attempt: attempt, revision: revision, result: workflow.Finalize(execution)}
	}
}

func (s *WalletCreateScreen) HandleMsg(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case walletLNDReadyMsg:
		if m.owner != s || m.attempt != s.attempt || s.step != walletWaiting {
			return s, nil
		}
		if m.err != nil {
			s.step = walletResult
			s.btnIdx = 0
			s.result = app.WalletCreationResult{Err: m.err}
			return s, nil
		}
		s.step = walletExec
		attempt := s.attempt
		terminal := newWalletTerminal(m.network)
		return s, tea.Exec(terminal, func(err error) tea.Msg {
			execution := terminal.execution
			if execution.Err == nil && err != nil {
				execution.Err = fmt.Errorf("terminal session: %w", err)
			}
			return walletExecDoneMsg{owner: s, attempt: attempt, execution: execution}
		})
	case walletExecDoneMsg:
		if m.owner != s || m.attempt != s.attempt || s.step != walletExec {
			return s, nil
		}
		s.attempt.execution = m.execution
		return s, s.finalizeCmd()
	case walletFinalizedMsg:
		if !s.acceptsFinalization(m) {
			return s, nil
		}
		s.result = m.result
		s.step = walletResult
		s.btnIdx = 0
	}
	return s, nil
}

func (s *WalletCreateScreen) acceptsFinalization(m walletFinalizedMsg) bool {
	return m.owner == s && s.attempt != nil && m.attempt == s.attempt &&
		s.step == walletFinalizing && m.revision == s.attempt.finalization
}

func (s *WalletCreateScreen) canContinue() bool {
	return s.result.Presence == app.WalletPresent && s.result.CredentialsStaged && s.result.SeedAcknowledged && s.clientErr == nil
}

func (s *WalletCreateScreen) retrySetup() bool {
	return s.result.CanRetryFinalization() || s.clientErr != nil
}

func (s *WalletCreateScreen) hasRecoveryAction() bool {
	return s.attempt != nil && s.attempt.finalization > 0 && (s.retrySetup() || s.canContinue())
}

func (s *WalletCreateScreen) View(w, h int) string {
	switch s.step {
	case walletWaiting, walletExec:
		return renderWaitingForLND(w, h)
	case walletFinalizing, walletInitializing:
		p := newPane(w)
		p.title(theme.Header, "Checking Wallet and Credentials")
		p.dim("Wallet creation will not be repeated.")
		return p.render()
	case walletResult:
		return s.viewResult(w, h)
	default:
		return s.viewConfirm(w, h)
	}
}

func (s *WalletCreateScreen) viewConfirm(
	w, h int,
) string {
	isFocused := s.ctx.ContentFocused
	p := newPane(w)

	p.title(theme.Header,
		"Create Your LND Lightning Wallet")

	p.line(" " + theme.Warning.Render(
		"IMPORTANT — read carefully before"+
			" you proceed."))
	p.blank()
	p.line(" " + theme.Value.Render(
		"LND will display your 24-word seed"+
			" phrase ONCE."))
	p.line(" " + theme.Value.Render(
		"It cannot be shown again. This seed"+
			" is the ONLY way"))
	p.line(" " + theme.Value.Render(
		"to recover your funds if anything"+
			" happens to this"))
	p.line(" " + theme.Value.Render(
		"server. No one can help you if you"+
			" lose it."))
	p.blank()
	p.line(" " + theme.Value.Render(
		"Before you proceed:"))
	p.blank()
	p.line(" " + theme.Value.Render(
		"  • Make sure you are in a private area"))
	p.line(" " + theme.Value.Render(
		"  • Have pen and paper ready, OR"))
	p.line(" " + theme.Value.Render(
		"  • Have an offline password manager"+
			" ready (e.g. KeePass)"))
	p.blank()
	p.line(" " + theme.Value.Render(
		"The wallet creation process will ask"+
			" you to:"))
	p.blank()
	p.line(" " + theme.Value.Render(
		"  • Set a wallet password"))
	p.line(" " + theme.Value.Render(
		"  • Confirm the password"))
	p.line(" " + theme.Value.Render(
		"  • Press 'n' to generate a new seed"))
	p.line(" " + theme.Value.Render(
		"  • Skip the cipher seed passphrase"+
			" (press Enter)"))
	p.line(" " + theme.Value.Render(
		"  • WRITE DOWN your 24 words"))
	p.blank()
	p.dim(
		"Once you press Proceed, this screen will be")
	p.dim(
		"replaced by the wallet creation prompts.")

	return p.renderWithBottomButtons(
		[]string{"Cancel", "Proceed"},
		s.btnIdx, isFocused, h)
}

func (s *WalletCreateScreen) viewResult(w, h int) string {
	p := newPane(w)
	title := "Wallet Creation Not Completed"
	if s.result.Presence == app.WalletPresent {
		title = "Wallet Exists"
	}
	if s.result.Presence == app.WalletUnknown {
		title = "Wallet State Unconfirmed"
	}
	p.title(theme.Header, title)
	if s.result.Err != nil {
		p.warnWrap(s.result.Err.Error())
	}
	if s.hasRecoveryAction() && s.retrySetup() {
		p.dim("Retry checks wallet state and stages credentials.")
		p.dim("It does not run wallet creation again.")
	}
	buttons := []string{"Done"}
	if s.hasRecoveryAction() {
		label := "Continue"
		if s.retrySetup() {
			label = "Retry Setup"
		}
		buttons = append(buttons, label)
	}
	return p.renderWithBottomButtons(buttons, s.btnIdx, s.ctx.ContentFocused, h)
}

func (s *WalletCreateScreen) HelpBindings() []key.Binding {
	if walletCreationBusy(s) {
		return []key.Binding{kSidebar, kUpTabBar, kQuit}
	}
	return tabButtonBindings(s.ctx.HasTabs)
}
