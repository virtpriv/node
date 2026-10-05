package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
)

type feeReader interface {
	Collect(context.Context, string) app.FeeSuggestions
	Close()
}

type feeRequest struct {
	network string
	cancel  context.CancelFunc
}

type feeSuggestionsMsg struct {
	request *feeRequest
	result  app.FeeSuggestions
}

// feeSuggestions belongs to one form, including an embedded channel close.
// Only the event loop admits requests and publishes their immutable results.
type feeSuggestions struct {
	active  *feeRequest
	network string
	result  app.FeeSuggestions
	edited  bool
}

// An edited blank is intentional too, particularly for LND's automatic policy.
func (f *feeSuggestions) edit(input *AmountInput, msg tea.Msg) tea.Cmd {
	before := input.ti.Value()
	cmd := input.Update(msg)
	if input.ti.Value() != before {
		f.edited = true
	}
	return cmd
}

func (f *feeSuggestions) refresh(owner *ScreenContext) tea.Cmd {
	network := owner.Cfg.Network
	if f.active != nil && f.active.network == network {
		return nil
	}
	f.cancel()
	if f.network != network {
		f.result = app.FeeSuggestions{}
		f.network = network
	}
	if owner.Fees == nil {
		owner.Fees = app.NewFeeReader()
	}
	reader := owner.Fees
	ctx, cancel := context.WithCancel(context.Background())
	request := &feeRequest{network: network, cancel: cancel}
	f.active = request
	return func() tea.Msg {
		return feeSuggestionsMsg{request: request, result: reader.Collect(ctx, network)}
	}
}

func (f *feeSuggestions) cancel() {
	if f.active != nil {
		f.active.cancel()
		f.active = nil
	}
}

func (f *feeSuggestions) complete(msg feeSuggestionsMsg, network string) bool {
	if msg.request == nil || msg.request != f.active {
		return false
	}
	f.cancel()
	if msg.request.network != network {
		f.result = app.FeeSuggestions{}
		return false
	}
	f.result = msg.result
	return msg.result.Err == nil
}

func (f *feeSuggestions) defaultRate(network string) int64 {
	if f.network != network || f.result.Err != nil {
		return 0
	}
	return int64(f.result.Tiers[0].SatPerVB)
}

func (f *feeSuggestions) hints(network string) string {
	if f.network != network {
		return ""
	}
	if f.result.Err != nil {
		return "Fee estimates unavailable."
	}
	var parts []string
	for _, tier := range f.result.Tiers {
		if tier.SatPerVB > 0 {
			parts = append(parts, fmt.Sprintf("~%d blocks %.0f sat/vB", tier.Blocks, tier.SatPerVB))
		}
	}
	return strings.Join(parts, "  ·  ")
}

func screenFees(screen Screen) *feeSuggestions {
	switch s := screen.(type) {
	case *OnChainSendScreen:
		return &s.fees
	case *ChannelOpenScreen:
		return &s.fees
	case *ChannelCloseScreen:
		return &s.fees
	case *ChannelDetailScreen:
		if s.closeScreen != nil {
			return &s.closeScreen.fees
		}
	}
	return nil
}

func cancelScreenFees(screen Screen) {
	if fees := screenFees(screen); fees != nil {
		fees.cancel()
	}
}
