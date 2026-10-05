package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
)

type stubWalletRuntime struct {
	read      func(context.Context) app.WalletObservation
	open      func(context.Context, bool) app.WalletClientResult
	mu        sync.Mutex
	clients   map[*lndrpc.Client]bool
	discarded int
}

func (r *stubWalletRuntime) Read(ctx context.Context) app.WalletObservation {
	if r.read != nil {
		return r.read(ctx)
	}
	return app.WalletObservation{Presence: app.WalletPresent}
}
func (r *stubWalletRuntime) Open(ctx context.Context, staged bool) app.WalletClientResult {
	var result app.WalletClientResult
	if r.open != nil {
		result = r.open(ctx, staged)
	} else {
		result.Client = &lndrpc.Client{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.clients == nil {
		r.clients = make(map[*lndrpc.Client]bool)
	}
	if result.Client != nil {
		r.clients[result.Client] = true
	}
	return result
}
func (r *stubWalletRuntime) Claim(client *lndrpc.Client) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	owned := r.clients[client]
	delete(r.clients, client)
	return owned
}
func (r *stubWalletRuntime) Discard(client *lndrpc.Client) {
	if r.Claim(client) {
		r.discarded++
	}
}
func (*stubWalletRuntime) Close() {}

// Existing screen regressions supply controlled observations after entering
// through the real admission gate. No helper I/O is run by these tests.
func walletObservationMsg(t *testing.T, m *Model, exists bool, err error) walletStateMsg {
	t.Helper()
	c := m.screenCtx
	if c.WalletRuntime == nil {
		c.WalletRuntime = &stubWalletRuntime{}
	}
	if c.Cfg == nil {
		c.Cfg = config.Default()
	}
	if c.walletRead == nil {
		statusUpdate(m, fetchWalletStateCmd(c)())
	}
	if c.walletRead == nil {
		t.Fatal("test observation has no admitted request")
	}
	presence := app.WalletAbsent
	if exists {
		presence = app.WalletPresent
	}
	if err != nil {
		presence = app.WalletUnknown
	}
	return walletStateMsg{request: c.walletRead, result: app.WalletObservation{Presence: presence, Err: err}}
}

func walletRuntimeModel(t *testing.T) (Model, *stubWalletRuntime) {
	t.Helper()
	r := &stubWalletRuntime{}
	m := NewModel(config.Default(), &config.Preferences{}, &RuntimeState{}, "test")
	m.screenCtx.WalletRuntime = r
	t.Cleanup(func() {
		m.screenCtx.cancelWalletRead()
		m.screenCtx.cancelWalletClient()
		m.screenCtx.ChannelHistory.reader.Close()
		m.screenCtx.OnChain.reader.Close()
		m.screenCtx.PaymentHistory.reader.Close()
		m.statusCollector.Close()
		m.screenCtx.HelperWorkflows.Close()
		m.screenCtx.Syncthing.Close()
	})
	return m, r
}

func TestWalletReadAdmissionCoalescesAndRejectsObsoleteResults(t *testing.T) {
	m, r := walletRuntimeModel(t)
	calls := 0
	r.read = func(context.Context) app.WalletObservation {
		calls++
		return app.WalletObservation{Presence: app.WalletAbsent}
	}
	cmd := statusUpdate(&m, fetchWalletStateCmd(m.screenCtx)())
	for range 5 {
		if next := statusUpdate(&m, fetchWalletStateCmd(m.screenCtx)()); next != nil {
			t.Fatal("overlapping wallet read admitted")
		}
	}
	first := cmd()
	follow := statusUpdate(&m, first)
	if !m.state.WalletKnown || m.state.WalletExists || follow == nil || calls != 1 {
		t.Fatal("absence or coalesced follow-up lost")
	}
	if next := statusUpdate(&m, first); next != nil {
		t.Fatal("duplicate result admitted work")
	}
	if next := statusUpdate(&m, follow()); next != nil || calls != 2 {
		t.Fatal("coalesced reads did not drain after one follow-up")
	}
	cmd = statusUpdate(&m, fetchWalletStateCmd(m.screenCtx)())
	late := cmd()
	m.screenCtx.invalidateWalletObservations()
	m.state.WalletKnown = false
	statusUpdate(&m, late)
	if m.state.WalletKnown {
		t.Fatal("prior lifecycle published wallet state")
	}
	cmd = statusUpdate(&m, fetchWalletStateCmd(m.screenCtx)())
	late = cmd()
	m.cfg.Network = config.NetworkTestnet4
	if next := statusUpdate(&m, late); next == nil || m.state.WalletKnown {
		t.Fatal("old network published or failed to request current observation")
	}
}

func TestWalletInitializationKeepsEventLoopResponsiveAndOwnsPublication(t *testing.T) {
	m, r := walletRuntimeModel(t)
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	r.open = func(ctx context.Context, staged bool) app.WalletClientResult {
		if staged {
			t.Error("ordinary startup selected creation policy")
		}
		entered <- ctx
		<-release
		return app.WalletClientResult{Client: &lndrpc.Client{}}
	}
	cmd := statusUpdate(&m, walletObservationMsg(t, &m, true, nil))
	if m.lndClient != nil || cmd == nil {
		t.Fatal("observation initialized synchronously")
	}
	completed := make(chan tea.Msg, 1)
	go func() { completed <- cmd() }()
	ctx := <-entered
	defer func() {
		if release != nil {
			close(release)
			<-completed
		}
	}()
	for _, sec := range []int{secOnChain, secWallet, secChannels} {
		m.nav.Cursor, m.nav.Focused = sec, true
		statusUpdate(&m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.nav.ActiveSection() != sec || !m.contentFocused {
			t.Fatal("initialization prevented navigation into a wallet section")
		}
		view := m.renderActiveTabContent(67, 24)
		if !strings.Contains(view, "Connecting to LND") || strings.Contains(view, "Create Wallet") {
			t.Fatal("initialization lost presence or exposed wallet forms")
		}
		retry := statusUpdate(&m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if retry == nil {
			t.Fatal("pending connection lost its retry action")
		}
		refresh, ok := retry().(refreshWalletStateMsg)
		if !ok || len(m.tabs) != 0 {
			t.Fatal("pending client opened a form")
		}
		read := statusUpdate(&m, refresh)
		if read == nil || statusUpdate(&m, read()) != nil {
			t.Fatal("routine observation scheduled overlapping initialization")
		}
	}
	if ctx.Err() != nil {
		t.Fatal("routine observations canceled initialization")
	}
	close(release)
	msg := <-completed
	release = nil
	statusUpdate(&m, msg)
	client := m.lndClient
	if client == nil || m.screenCtx.LndClient != client {
		t.Fatal("client did not publish")
	}
	statusUpdate(&m, msg)
	if m.lndClient != client || r.discarded != 0 {
		t.Fatal("duplicate completion closed the published client")
	}
}

func TestWalletInitializationFailureRetriesAndObsoleteClientCloses(t *testing.T) {
	m, r := walletRuntimeModel(t)
	r.open = func(context.Context, bool) app.WalletClientResult {
		return app.WalletClientResult{Err: errors.New("staged files unavailable")}
	}
	cmd := statusUpdate(&m, walletObservationMsg(t, &m, true, nil))
	statusUpdate(&m, cmd())
	if !m.state.WalletKnown || !m.state.WalletExists || m.lndClient != nil {
		t.Fatal("initialization failure changed wallet presence")
	}
	r.open = nil
	cmd = statusUpdate(&m, walletObservationMsg(t, &m, true, nil))
	late := cmd()
	statusUpdate(&m, walletObservationMsg(t, &m, false, errors.New("LND unavailable")))
	statusUpdate(&m, late)
	if m.lndClient != nil || r.discarded != 1 || m.state.WalletKnown {
		t.Fatal("late initialization published after loss of presence")
	}
	cmd = statusUpdate(&m, walletObservationMsg(t, &m, true, nil))
	statusUpdate(&m, cmd())
	if m.lndClient == nil {
		t.Fatal("recovered observation did not retry initialization")
	}
}
