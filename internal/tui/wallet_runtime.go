package tui

import (
	"context"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/theme"
)

type walletRuntime interface {
	Read(context.Context) app.WalletObservation
	Open(context.Context, bool) app.WalletClientResult
	Claim(*lndrpc.Client) bool
	Discard(*lndrpc.Client)
	Close()
}

func (c *ScreenContext) walletRuntime() walletRuntime {
	if c.WalletRuntime == nil {
		c.WalletRuntime = app.NewWalletRuntime()
	}
	return c.WalletRuntime
}

type refreshWalletStateMsg struct{ owner *ScreenContext }

type walletStateRequest struct {
	owner      *ScreenContext
	generation uint64
	network    string
	cancel     context.CancelFunc
}

type walletStateMsg struct {
	request *walletStateRequest
	result  app.WalletObservation
}

type walletClientRequest struct {
	owner        *ScreenContext
	generation   uint64
	network      string
	cancel       context.CancelFunc
	runtime      walletRuntime
	creation     *WalletCreateScreen
	attempt      *walletCreationAttempt
	finalization uint64
}

type walletClientMsg struct {
	request *walletClientRequest
	result  app.WalletClientResult
}

func fetchWalletStateCmd(owner *ScreenContext) tea.Cmd {
	return func() tea.Msg { return refreshWalletStateMsg{owner: owner} }
}

func (c *ScreenContext) cancelWalletRead() {
	if c.walletRead != nil {
		c.walletRead.cancel()
		c.walletRead = nil
	}
	c.walletReadPending = false
}

func (c *ScreenContext) cancelWalletClient() {
	if c.walletClient != nil {
		c.walletClient.cancel()
		c.walletClient = nil
	}
}

// Startup, timer, resume and explicit retry all use one admission point.
func (m *Model) admitWalletState() tea.Cmd {
	c := m.screenCtx
	if walletCreationBusy(c.walletCreationOwner) {
		return nil
	}
	if c.walletRead != nil {
		c.walletReadPending = true
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	request := &walletStateRequest{owner: c, generation: c.walletGeneration, network: c.Cfg.Network, cancel: cancel}
	c.walletRead = request
	runtime := c.walletRuntime()
	return func() tea.Msg { return walletStateMsg{request: request, result: runtime.Read(ctx)} }
}

func (m *Model) completeWalletState(msg walletStateMsg) tea.Cmd {
	c, request := m.screenCtx, msg.request
	if request == nil || request != c.walletRead || request.owner != c {
		return nil
	}
	pending := c.walletReadPending
	c.cancelWalletRead()
	if request.generation != c.walletGeneration || request.network != c.Cfg.Network || walletCreationBusy(c.walletCreationOwner) {
		return m.admitWalletState()
	}
	cmd := m.publishWalletObservation(msg.result)
	if pending {
		return tea.Batch(cmd, m.admitWalletState())
	}
	return cmd
}

func (m *Model) publishWalletObservation(result app.WalletObservation) tea.Cmd {
	c := m.screenCtx
	err := result.Err
	if err == nil && result.Presence != app.WalletAbsent && result.Presence != app.WalletPresent {
		err = errors.New("wallet state is unknown")
	}
	if err != nil {
		m.state.WalletKnown = false
		c.cancelWalletClient()
		if c.ChannelHistory != nil {
			c.ChannelHistory.Closed.Err = err
		}
		if c.PaymentHistory != nil {
			c.PaymentHistory.PaymentHistorySnapshot = c.PaymentHistory.Unavailable(err)
		}
		if c.OnChain != nil {
			c.OnChain.OnChainSnapshot = c.OnChain.Unavailable(err)
		}
		if c.Status != nil {
			snapshot := c.Status.WalletUnavailable(err)
			c.Status = &snapshot
		}
		logger.TUI("read live wallet state: %v", err)
		return nil
	}
	exists := result.Presence == app.WalletPresent
	recovered := !m.state.WalletKnown || m.state.WalletExists != exists
	if m.state.WalletExists != exists {
		c.invalidateWalletObservations()
	}
	m.state.WalletExists, m.state.WalletKnown = exists, true
	var lists tea.Cmd
	if recovered {
		lists = m.visibleWalletListsCmd()
	}
	return tea.Batch(lists, m.initializeWalletClient(nil))
}

func (m *Model) initializeWalletClient(creation *WalletCreateScreen) tea.Cmd {
	c := m.screenCtx
	if m.lndClient != nil || c.walletClient != nil || !c.walletExists() || !c.Cfg.HasLND() {
		return nil
	}
	if creation == nil && c.walletCreationOwner != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	request := &walletClientRequest{owner: c, generation: c.walletGeneration, network: c.Cfg.Network,
		cancel: cancel, runtime: c.walletRuntime(), creation: creation}
	if creation != nil {
		request.attempt, request.finalization = creation.attempt, creation.attempt.finalization
		creation.step = walletInitializing
	}
	c.walletClient = request
	return func() tea.Msg {
		return walletClientMsg{request: request, result: request.runtime.Open(ctx, creation != nil)}
	}
}

func (m Model) completeWalletClient(msg walletClientMsg) (Model, tea.Cmd) {
	c, request := m.screenCtx, msg.request
	if request == nil {
		return m, nil
	}
	// Discard is harmless after Claim, including duplicate completion delivery.
	defer request.runtime.Discard(msg.result.Client)
	if request != c.walletClient || request.owner != c {
		return m, nil
	}
	c.cancelWalletClient()
	if request.generation != c.walletGeneration || request.network != c.Cfg.Network || !c.walletExists() || m.lndClient != nil {
		return m, nil
	}
	s := request.creation
	if s != nil {
		mounted := false
		for _, tab := range m.tabs {
			mounted = mounted || tab.Screen == s
		}
		if !mounted || c.walletCreationOwner != s || s.step != walletInitializing || s.attempt != request.attempt || s.attempt.finalization != request.finalization {
			return m, nil
		}
	} else if c.walletCreationOwner != nil {
		return m, nil
	}
	err := msg.result.Err
	if err == nil && !request.runtime.Claim(msg.result.Client) {
		err = errors.New("wallet client initialization was canceled")
	}
	if err == nil {
		m.lndClient, c.LndClient = msg.result.Client, msg.result.Client
	}
	if s != nil {
		s.step = walletResult
		if err != nil {
			s.clientErr = fmt.Errorf("wallet credentials were staged, but the TUI client could not be initialized: %w", err)
			s.result.Err = errors.Join(s.result.Err, s.clientErr)
		}
		if s.result.Err == nil && s.canContinue() {
			return m.continueWalletCreation(s)
		}
	} else if err != nil {
		logger.TUI("initialize wallet client: %v", err)
	}
	if err == nil {
		return m, tea.Batch(requestStatusCmd, m.visibleWalletListsCmd())
	}
	return m, nil
}

func (c *ScreenContext) walletClientUnavailable() bool {
	return c.walletExists() && c.Cfg.HasLND() && c.LndClient == nil
}

func (c *ScreenContext) walletConnectionNotice(width int) string {
	p := newPane(width)
	title := "Wallet Connection Unavailable"
	if c.walletClient != nil {
		title = "Connecting to LND"
	}
	p.title(theme.Header, title)
	p.dim("Your wallet exists. Its connection is not available yet.")
	p.dim("Retrying automatically. Press Enter to retry.")
	return p.render()
}
