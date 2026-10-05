package tui

import (
	"context"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/lndrpc"
)

type onChainReader interface {
	Collect(app.OnChainSource) app.OnChainSnapshot
	ReadUnspent(context.Context, app.OnChainSource, int32, int32) app.Observation[[]lndrpc.UTXO]
	ReadTransactions(context.Context, app.OnChainSource) app.Observation[[]lndrpc.OnChainTx]
	Close()
}

type onChainRequest struct {
	owner    *OnChainContext
	scope    walletObservationScope
	revision uint64
}

type refreshOnChainMsg struct{}
type onChainResultMsg struct {
	request  *onChainRequest
	snapshot app.OnChainSnapshot
}

func requestOnChainCmd() tea.Msg { return refreshOnChainMsg{} }

func (m Model) visibleOnChainCmd() tea.Cmd {
	if m.nav.ActiveSection() == secOnChain {
		return requestOnChainCmd
	}
	return nil
}

// The event loop admits one read and coalesces overlapping triggers. A label
// acknowledgement invalidates older reads without creating concurrent work.
func (m *Model) admitOnChain() tea.Cmd {
	oc := m.screenCtx.OnChain
	if oc == nil || oc.reader == nil {
		return nil
	}
	current := m.screenCtx.walletObservationScope()
	if oc.scope != current {
		oc.OnChainSnapshot = app.OnChainSnapshot{}
		oc.Selection.Clear()
		oc.scope = current
	}
	if oc.active != nil {
		oc.pending = true
		return nil
	}
	if !m.screenCtx.walletExists() {
		oc.OnChainSnapshot = oc.Unavailable(errors.New("wallet state unavailable"))
		return nil
	}
	request := &onChainRequest{owner: oc, scope: current, revision: oc.revision}
	oc.active = request
	return func() tea.Msg {
		var source app.OnChainSource
		if request.scope.client != nil {
			source = request.scope.client
		}
		return onChainResultMsg{request: request, snapshot: oc.reader.Collect(source)}
	}
}

func (m *Model) completeOnChain(msg onChainResultMsg) tea.Cmd {
	oc := m.screenCtx.OnChain
	if oc == nil || msg.request == nil || msg.request.owner != oc || msg.request != oc.active {
		return nil
	}
	oc.active = nil
	current := m.screenCtx.walletObservationScope()
	if msg.request.scope == current && msg.request.revision == oc.revision {
		msg.snapshot = msg.snapshot.Retain(oc.OnChainSnapshot)
		if !m.screenCtx.walletKnown() {
			msg.snapshot = msg.snapshot.Unavailable(errors.New("wallet state unavailable"))
		}
		oc.OnChainSnapshot = msg.snapshot
	} else {
		oc.pending = true
	}
	if oc.pending {
		oc.pending = false
		return m.admitOnChain()
	}
	return nil
}

func onChainListTitle[T any](name string, observation app.Observation[T]) string {
	if observation.Known() && observation.Err != nil {
		return name + " (stale, retrying)"
	}
	return name
}

func (s *OnChainHomeScreen) utxoCountText() string {
	if !s.ocCtx.Utxos.Known() {
		return "  (UTXOs unavailable)"
	}
	if s.ocCtx.Utxos.Err != nil {
		return fmt.Sprintf("  (%d UTXOs, stale)", len(s.ocCtx.Utxos.Value))
	}
	return fmt.Sprintf("  (%d UTXOs)", len(s.ocCtx.Utxos.Value))
}
