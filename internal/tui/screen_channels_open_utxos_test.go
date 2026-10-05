package tui

import (
	"context"
	"errors"
	"math"
	"testing"
	"testing/synctest"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
)

type channelCoinSource struct {
	coins func(context.Context, int32, int32) ([]lndrpc.UTXO, error)
	txs   func(context.Context) ([]lndrpc.OnChainTx, error)
}

func (s channelCoinSource) ListUnspentContext(ctx context.Context, min, max int32) ([]lndrpc.UTXO, error) {
	return s.coins(ctx, min, max)
}
func (s channelCoinSource) GetTransactionsContext(ctx context.Context) ([]lndrpc.OnChainTx, error) {
	return s.txs(ctx)
}

// Keep the real app collector and replace only its daemon source. No additional
// production dependency is needed to exercise the screen-to-reader lifecycle.
type channelCoinTestReader struct {
	*app.OnChainReader
	source app.OnChainSource
}

func (r channelCoinTestReader) ReadUnspent(ctx context.Context, _ app.OnChainSource, min, max int32) app.Observation[[]lndrpc.UTXO] {
	return r.OnChainReader.ReadUnspent(ctx, r.source, min, max)
}
func (r channelCoinTestReader) ReadTransactions(ctx context.Context, _ app.OnChainSource) app.Observation[[]lndrpc.OnChainTx] {
	return r.OnChainReader.ReadTransactions(ctx, r.source)
}

func channelCoinReplies(cmd tea.Cmd) []tea.Msg {
	commands := cmd().(tea.BatchMsg)
	var messages []tea.Msg
	for _, read := range commands {
		messages = append(messages, read())
	}
	return messages
}

func installChannelCoinSource(t *testing.T, s *ChannelOpenScreen, source app.OnChainSource) {
	t.Helper()
	r := channelCoinTestReader{OnChainReader: app.NewOnChainReader(), source: source}
	s.ctx.OnChain.reader = r
	t.Cleanup(r.Close)
}

func TestChannelCoinReadsCoalesceAndEndWithForm(t *testing.T) {
	for _, action := range []string{"close", "hidden close", "replace", "clear", "submit", "scope change"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, _ := channelScreen(t)
				entered := make(chan context.Context, 2)
				release := make(chan struct{})
				block := func(ctx context.Context) error {
					entered <- ctx
					<-ctx.Done()
					<-release
					return ctx.Err()
				}
				installChannelCoinSource(t, s, channelCoinSource{
					coins: func(ctx context.Context, min, max int32) ([]lndrpc.UTXO, error) {
						if min != 0 || max != math.MaxInt32 {
							t.Error("Channel Open confirmation range changed")
						}
						return nil, block(ctx)
					},
					txs: func(ctx context.Context) ([]lndrpc.OnChainTx, error) { return nil, block(ctx) },
				})
				command := s.refreshCoins()
				request := s.refresh
				done := make(chan tea.Msg, 2)
				for _, read := range command().(tea.BatchMsg) {
					go func() { done <- read() }()
				}
				reads := []context.Context{<-entered, <-entered}
				for range 3 {
					if s.refreshCoins() != nil || s.refresh != request {
						t.Fatal("overlapping refresh admitted another collection")
					}
				}
				m := Model{nav: NewNavSidebar(), screenCtx: s.ctx, activeTab: 1,
					tabs: []openTab{{Kind: tabOpenChannel, Section: secChannels, Screen: s}}}
				m.nav.ActiveItem = secChannels
				switch action {
				case "close":
					updated, _ := m.closeTab(1)
					m = updated.(Model)
				case "hidden close":
					m.nav.ActiveItem = secSystem
					updated, _ := m.closeScreenTab(s)
					m = updated.(Model)
				case "replace":
					updated, _ := m.Update(openTabMsg{Kind: tabOpenChannel, Screen: NewChannelOpenScreen(s.ctx), Replace: true})
					m = updated.(Model)
				case "clear":
					s.focusZone, s.btnIdx = coZoneButtons, 0
					s.HandleKey("enter", tea.KeyPressMsg{})
				case "submit":
					confirmChannel(t, s)
				case "scope change":
					s.ctx.walletGeneration++
					s.refreshCoins()
				}
				synctest.Wait()
				for _, ctx := range reads {
					if !errors.Is(ctx.Err(), context.Canceled) {
						t.Error("obsolete form read was not canceled")
					}
				}
				close(release)
				m.Update(<-done)
				m.Update(<-done)
				if s.utxoErr != nil || s.refresh == request {
					t.Fatal("obsolete result changed the form or retained its request")
				}
			})
		})
	}
}

func TestChannelCoinPartialFailureAndHiddenPublication(t *testing.T) {
	s, client := channelScreen(t)
	coin := client.coins[0]
	s.txs = []lndrpc.OnChainTx{{Txid: coin.Txid, Label: "retained"}}
	failure := errors.New("history unavailable")
	installChannelCoinSource(t, s, channelCoinSource{
		coins: func(context.Context, int32, int32) ([]lndrpc.UTXO, error) { return client.coins, nil },
		txs:   func(context.Context) ([]lndrpc.OnChainTx, error) { return nil, failure },
	})
	m := Model{nav: NewNavSidebar(), screenCtx: s.ctx,
		tabs: []openTab{{Kind: tabOpenChannel, Section: secChannels, Screen: s}}}
	m.nav.ActiveItem = secSystem
	for _, msg := range channelCoinReplies(s.refreshCoins()) {
		m.Update(msg)
	}
	if s.utxoErr != nil || !s.selection.Contains(coin) || s.txs[0].Label != "retained" || s.feeInput.Sats() != 9 {
		t.Fatal("independent history failure lost coins, selection, labels or edited fee")
	}
	installChannelCoinSource(t, s, channelCoinSource{
		coins: func(context.Context, int32, int32) ([]lndrpc.UTXO, error) { return nil, failure },
		txs: func(context.Context) ([]lndrpc.OnChainTx, error) {
			return []lndrpc.OnChainTx{{Txid: coin.Txid, Label: "fresh"}}, nil
		},
	})
	failed := channelCoinReplies(s.refreshCoins())
	for _, msg := range failed {
		m.Update(msg)
	}
	s.prepareChannelOpenConfirmation()
	if !errors.Is(s.utxoErr, failure) || !s.selection.Contains(coin) || s.txs[0].Label != "fresh" || s.step == coStepConfirm || len(client.sent) != 0 {
		t.Fatal("failed coins allowed preparation or discarded independent history")
	}
	installChannelCoinSource(t, s, channelCoinSource{
		coins: func(context.Context, int32, int32) ([]lndrpc.UTXO, error) { return client.coins, nil },
		txs:   func(context.Context) ([]lndrpc.OnChainTx, error) { return nil, nil },
	})
	for _, msg := range channelCoinReplies(s.refreshCoins()) {
		m.Update(msg)
	}
	for _, msg := range failed {
		m.Update(msg)
	}
	s.prepareChannelOpenConfirmation()
	if s.utxoErr != nil || len(s.txs) != 0 || s.step != coStepConfirm || len(client.sent) != 0 {
		t.Fatal("recovery or known-empty history was lost to a duplicate failure")
	}
}

func TestChannelCoinsPublishBeforeSlowMetadata(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, client := channelScreen(t)
		s.utxos, s.txs, s.utxoFetched = nil, nil, false
		entered, release := make(chan struct{}, 1), make(chan struct{})
		installChannelCoinSource(t, s, channelCoinSource{
			coins: func(context.Context, int32, int32) ([]lndrpc.UTXO, error) { return client.coins, nil },
			txs: func(ctx context.Context) ([]lndrpc.OnChainTx, error) {
				entered <- struct{}{}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
					return []lndrpc.OnChainTx{{Txid: client.coins[0].Txid, Label: "ready"}}, nil
				}
			},
		})
		done := make(chan tea.Msg, 2)
		for _, read := range s.refreshCoins()().(tea.BatchMsg) {
			go func() { done <- read() }()
		}
		<-entered
		s.HandleMsg(<-done)
		if !s.utxoFetched || len(s.utxos) != 1 || len(s.txs) != 0 || s.refresh == nil || s.refreshCoins() != nil {
			t.Fatal("ready coins waited for metadata or partial completion admitted overlapping reads")
		}
		close(release)
		s.HandleMsg(<-done)
		if len(s.txs) != 1 || s.txs[0].Label != "ready" || s.refresh != nil {
			t.Fatal("later metadata did not complete the same refresh")
		}
	})
}

func TestChannelCoinScopeChangeRejectsOldResultsAndConfirmation(t *testing.T) {
	for _, change := range []string{"network", "client", "wallet"} {
		t.Run(change, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, client := channelScreen(t)
				s.prepareChannelOpenConfirmation()
				pending := s.refreshCoins()
				old := s.refresh
				switch change {
				case "network":
					s.ctx.Cfg.Network = config.NetworkPublicSignet
				case "client":
					s.ctx.LndClient = &lndrpc.Client{}
				case "wallet":
					s.ctx.walletGeneration++
				}
				s.confirmBtnIdx = 1
				if _, cmd := s.HandleKey("enter", tea.KeyPressMsg{}); cmd != nil || len(client.sent) != 0 {
					t.Fatal("old wallet confirmation scheduled funding")
				}
				// A command queued before scope replacement must not start RPC work.
				messages := channelCoinReplies(pending)
				if !errors.Is(messages[0].(channelCoinsMsg).snapshot.Utxos.Err, context.Canceled) || !errors.Is(messages[1].(channelCoinsMsg).snapshot.OnChainTxs.Err, context.Canceled) {
					t.Fatal("superseded queued read was admitted")
				}
				s.refreshCoins()
				if s.refresh == old || s.selection.Len() != 0 || s.step == coStepConfirm || s.attempt != nil {
					t.Fatal("new scope retained old selected or reviewed intent")
				}
				s.HandleMsg(channelCoinsMsg{refresh: old, snapshot: app.OnChainSnapshot{
					Utxos: freshStatus(client.coins), OnChainTxs: freshStatus([]lndrpc.OnChainTx{{Txid: "old"}}),
				}})
				if len(s.utxos) != 0 || len(s.txs) != 0 {
					t.Fatal("old scope published into the replacement read")
				}
				if change == "client" {
					publishChannelCoins(s, client.coins, nil)
					s.toggleUtxoSelection(0)
					s.returnFromCoinControl(true)
					result := confirmChannel(t, s)().(channelOpenResultMsg).result
					// The replacement client is disconnected. The old test client
					// would fund successfully if the form kept using it.
					if len(client.sent) != 0 || result.State != app.ChannelNotSubmitted {
						t.Fatal("renewed review submitted through the previous wallet client")
					}
				}
			})
		})
	}
}
