package tui

import (
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/lndrpc"
)

type paymentHistoryReader interface {
	Collect(app.PaymentHistorySource) app.PaymentHistorySnapshot
	Close()
}

type paymentHistoryContext struct {
	app.PaymentHistorySnapshot
	reader   paymentHistoryReader
	scope    walletObservationScope
	active   *paymentHistoryRequest
	pending  bool
	revision uint64
}

type paymentHistoryRequest struct {
	owner    *paymentHistoryContext
	scope    walletObservationScope
	revision uint64
}

type refreshPaymentHistoryMsg struct{ changed bool }
type paymentHistoryResultMsg struct {
	request  *paymentHistoryRequest
	snapshot app.PaymentHistorySnapshot
}

func requestPaymentHistoryCmd() tea.Msg { return refreshPaymentHistoryMsg{} }
func paymentHistoryChangedCmd() tea.Msg { return refreshPaymentHistoryMsg{changed: true} }

func (m Model) visibleWalletListsCmd() tea.Cmd {
	if m.nav.ActiveSection() == secWallet {
		return requestPaymentHistoryCmd
	}
	if m.nav.ActiveSection() == secChannels {
		return m.visibleChannelHistoryCmd()
	}
	return m.visibleOnChainCmd()
}

func (m *Model) admitPaymentHistory() tea.Cmd {
	history := m.screenCtx.PaymentHistory
	if history == nil || history.reader == nil {
		return nil
	}
	current := m.screenCtx.walletObservationScope()
	if history.scope != current {
		history.PaymentHistorySnapshot = app.PaymentHistorySnapshot{}
		history.scope = current
	}
	if history.active != nil {
		history.pending = true
		return nil
	}
	if !m.screenCtx.walletExists() {
		history.PaymentHistorySnapshot = history.Unavailable(errors.New("wallet state unavailable"))
		return nil
	}
	request := &paymentHistoryRequest{owner: history, scope: current, revision: history.revision}
	history.active = request
	return func() tea.Msg {
		var source app.PaymentHistorySource
		if request.scope.client != nil {
			source = request.scope.client
		}
		return paymentHistoryResultMsg{request: request, snapshot: history.reader.Collect(source)}
	}
}

func (m *Model) completePaymentHistory(msg paymentHistoryResultMsg) tea.Cmd {
	history := m.screenCtx.PaymentHistory
	if history == nil || msg.request == nil || msg.request.owner != history || msg.request != history.active {
		return nil
	}
	history.active = nil
	if msg.request.scope == m.screenCtx.walletObservationScope() && msg.request.revision == history.revision {
		msg.snapshot = msg.snapshot.Retain(history.PaymentHistorySnapshot)
		if !m.screenCtx.walletKnown() {
			msg.snapshot = msg.snapshot.Unavailable(errors.New("wallet state unavailable"))
		}
		history.PaymentHistorySnapshot = msg.snapshot
	} else {
		history.pending = true
	}
	if history.pending {
		history.pending = false
		return m.admitPaymentHistory()
	}
	return nil
}

func paymentHistoryKey(entry lndrpc.PaymentEntry) string {
	return fmt.Sprintf("%t:%d", entry.IsIncoming, entry.Index)
}

func (c *ScreenContext) paymentHistory() app.PaymentHistorySnapshot {
	if c.PaymentHistory == nil || c.PaymentHistory.scope != c.walletObservationScope() {
		return app.PaymentHistorySnapshot{}
	}
	return c.PaymentHistory.PaymentHistorySnapshot
}
