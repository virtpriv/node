package tui

import (
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
)

type channelHistoryReader interface {
	Collect(app.ChannelHistorySource) app.ChannelHistorySnapshot
	Close()
}

type channelHistoryContext struct {
	app.ChannelHistorySnapshot
	reader   channelHistoryReader
	scope    walletObservationScope
	active   *channelHistoryRequest
	pending  bool
	revision uint64
}

type channelHistoryRequest struct {
	owner    *channelHistoryContext
	scope    walletObservationScope
	revision uint64
}

type refreshChannelHistoryMsg struct{ changed bool }
type channelHistoryResultMsg struct {
	request  *channelHistoryRequest
	snapshot app.ChannelHistorySnapshot
}

func requestChannelHistoryCmd() tea.Msg { return refreshChannelHistoryMsg{} }
func channelHistoryChangedCmd() tea.Msg { return refreshChannelHistoryMsg{changed: true} }

func (m *Model) admitChannelHistory() tea.Cmd {
	history := m.screenCtx.ChannelHistory
	if history == nil || history.reader == nil {
		return nil
	}
	current := m.screenCtx.walletObservationScope()
	if history.scope != current {
		history.ChannelHistorySnapshot = app.ChannelHistorySnapshot{}
		history.scope = current
	}
	if history.active != nil {
		history.pending = true
		return nil
	}
	if !m.screenCtx.walletExists() {
		history.Closed.Err = errors.New("wallet state unavailable")
		return nil
	}
	request := &channelHistoryRequest{owner: history, scope: current, revision: history.revision}
	history.active = request
	return func() tea.Msg {
		var source app.ChannelHistorySource
		if request.scope.client != nil {
			source = request.scope.client
		}
		return channelHistoryResultMsg{request: request, snapshot: history.reader.Collect(source)}
	}
}

func (m *Model) completeChannelHistory(msg channelHistoryResultMsg) tea.Cmd {
	history := m.screenCtx.ChannelHistory
	if history == nil || msg.request == nil || msg.request.owner != history || msg.request != history.active {
		return nil
	}
	history.active = nil
	if msg.request.scope == m.screenCtx.walletObservationScope() && msg.request.revision == history.revision {
		msg.snapshot = msg.snapshot.Retain(history.ChannelHistorySnapshot)
		if !m.screenCtx.walletKnown() {
			msg.snapshot.Closed.Err = errors.New("wallet state unavailable")
		}
		history.ChannelHistorySnapshot = msg.snapshot
	} else {
		history.pending = true
	}
	if history.pending {
		history.pending = false
		return m.admitChannelHistory()
	}
	return nil
}

func (m Model) visibleChannelHistoryCmd() tea.Cmd {
	if m.nav.ActiveSection() != secChannels {
		return nil
	}
	for _, tab := range m.tabs {
		if history, ok := tab.Screen.(*ChannelHistoryScreen); ok && history.current() {
			return requestChannelHistoryCmd
		}
	}
	return nil
}

func (c *ScreenContext) channelObservation() app.Observation[app.ChannelStatus] {
	if c.Status == nil {
		return app.Observation[app.ChannelStatus]{}
	}
	return c.Status.Channels
}
