package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/bitcoin"
	"github.com/virtpriv/node/internal/config"
)

type feeTestReader struct {
	collect func(context.Context, string) app.FeeSuggestions
}

func (r feeTestReader) Collect(ctx context.Context, network string) app.FeeSuggestions {
	return r.collect(ctx, network)
}
func (feeTestReader) Close() {}

func feeReply(t *testing.T, fees *feeSuggestions, ctx *ScreenContext, rate float64) feeSuggestionsMsg {
	t.Helper()
	ctx.Fees = feeTestReader{collect: func(context.Context, string) app.FeeSuggestions {
		return app.FeeSuggestions{Tiers: [3]bitcoin.FeeEstimate{{Blocks: 2, SatPerVB: rate}}}
	}}
	cmd := fees.refresh(ctx)
	if cmd == nil {
		t.Fatal("fee request not admitted")
	}
	t.Cleanup(fees.cancel)
	return cmd().(feeSuggestionsMsg)
}

func TestFeeOwnershipAcrossFormsAndReplacement(t *testing.T) {
	send, _ := onChainScreen(t)
	send.feeInput.Clear()
	open, _ := channelScreen(t)
	open.ctx = send.ctx
	open.feeInput.Clear()
	old := feeReply(t, &send.fees, send.ctx, 9)
	opening := feeReply(t, &open.fees, open.ctx, 13)
	m := Model{nav: NewNavSidebar(), screenCtx: send.ctx, tabs: []openTab{
		{Kind: tabOnChain, Section: secOnChain, Screen: send},
		{Kind: tabOpenChannel, Section: secChannels, Screen: open},
	}}
	m.nav.ActiveItem = secSystem
	m.Update(opening)
	if open.feeInput.Sats() != 13 || !send.feeInput.Empty() {
		t.Fatal("hidden form lost its suggestion or result crossed form ownership")
	}
	open.feeInput.Clear()
	m.Update(opening)
	if !open.feeInput.Empty() {
		t.Fatal("duplicate result overwrote edited form")
	}
	replacement, _ := onChainScreen(t)
	replacement.ctx = send.ctx
	replacement.feeInput.Clear()
	m.nav.ActiveItem = secOnChain
	m.setTabScreen(1, replacement)
	m.Update(old)
	if !replacement.feeInput.Empty() || replacement.fees.result.Tiers[0].SatPerVB != 0 || send.fees.active != nil {
		t.Fatal("removed form published into replacement or retained its request")
	}
}

func TestFeeSchedulingNetworkCoalescingAndCancellation(t *testing.T) {
	ctx := &ScreenContext{Cfg: config.Default()}
	var network string
	var readCtx context.Context
	ctx.Fees = feeTestReader{collect: func(c context.Context, n string) app.FeeSuggestions {
		network, readCtx = n, c
		return app.FeeSuggestions{Tiers: [3]bitcoin.FeeEstimate{{Blocks: 2, SatPerVB: 7}}}
	}}
	var fees feeSuggestions
	first := fees.refresh(ctx)
	if fees.refresh(ctx) != nil {
		t.Fatal("overlapping activation scheduled another read")
	}
	ctx.Cfg.Network = config.NetworkPublicSignet
	current := fees.refresh(ctx)
	msg := first().(feeSuggestionsMsg)
	if network != config.NetworkMainnet || fees.complete(msg, ctx.Cfg.Network) || fees.hints(ctx.Cfg.Network) != "" {
		t.Fatal("read used mutable configuration or published for another network")
	}
	if readCtx.Err() == nil {
		t.Fatal("completed request retained resources")
	}
	msg = current().(feeSuggestionsMsg)
	fees.cancel()
	if readCtx.Err() == nil || fees.complete(msg, ctx.Cfg.Network) {
		t.Fatal("canceled form accepted a late result")
	}
}

func TestLateFeesPreserveDeliberatelyClearedInput(t *testing.T) {
	send, _ := onChainScreen(t)
	open, _ := channelScreen(t)
	for _, screen := range []Screen{send, open} {
		var ctx *ScreenContext
		var input *AmountInput
		switch s := screen.(type) {
		case *OnChainSendScreen:
			ctx, input = s.ctx, &s.feeInput
			s.step = ocStepFee
		case *ChannelOpenScreen:
			ctx, input = s.ctx, &s.feeInput
			s.step, s.focusZone = coStepInput, coZoneFee
		}
		input.Focus()
		late := feeReply(t, screenFees(screen), ctx, 25)
		screen.HandleKey("backspace", tea.KeyPressMsg{Code: tea.KeyBackspace})
		if !input.Empty() {
			t.Fatal("test did not clear the existing rate")
		}
		screen.HandleMsg(late)
		if !input.Empty() {
			t.Fatal("late suggestion refilled an intentionally cleared rate")
		}
	}
}

func TestFeeFailureRecoveryAndManualRate(t *testing.T) {
	s, _ := onChainScreen(t)
	s.HandleMsg(feeReply(t, &s.fees, s.ctx, 12))
	if s.feeInput.Sats() != 9 || !strings.Contains(s.fees.hints(s.ctx.Cfg.Network), "~2 blocks") {
		t.Fatal("manual rate changed or returned block target was lost")
	}
	failed := feeReply(t, &s.fees, s.ctx, 0)
	failed.result = app.FeeSuggestions{Err: errors.New("RPC unavailable")}
	s.HandleMsg(failed)
	if s.feeInput.Sats() != 9 || s.fees.defaultRate(s.ctx.Cfg.Network) != 0 || s.fees.hints(s.ctx.Cfg.Network) != "Fee estimates unavailable." {
		t.Fatal("failure retained an old default or changed manual input")
	}
	s.feeInput.Clear()
	s.HandleMsg(feeReply(t, &s.fees, s.ctx, 11))
	if s.feeInput.Sats() != 11 || strings.Contains(s.fees.hints(s.ctx.Cfg.Network), "unavailable") {
		t.Fatal("successful retry did not recover suggestions")
	}
}

func TestChannelCloseFeeInputPreservesUserChoice(t *testing.T) {
	detail, client := closeScreenFixture(strings.Repeat("a", 64) + ":0")
	s := detail.closeScreen
	detail.HandleMsg(feeReply(t, &s.fees, s.ctx, 9))
	s.focusZone, s.typeBtnIdx = closeTypeZoneButtons, 1
	detail.HandleKey("enter", tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.step != closeStepFee || s.feeInput.Sats() != 9 {
		t.Fatal("cooperative close did not prefill its suggested rate")
	}

	late := feeReply(t, &s.fees, s.ctx, 25)
	detail.HandleKey("backspace", tea.KeyPressMsg{Code: tea.KeyBackspace})
	detail.HandleMsg(tea.PasteMsg{Content: "14"})
	if s.feeInput.Sats() != 14 {
		t.Fatal("manual rate was not entered")
	}
	detail.HandleMsg(late)
	if s.feeInput.Sats() != 14 || !strings.Contains(s.View(82, 34), "25 sat/vB") {
		t.Fatal("late suggestion replaced manual rate or did not refresh hints")
	}

	late = feeReply(t, &s.fees, s.ctx, 31)
	for range 2 {
		detail.HandleKey("backspace", tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	if !s.feeInput.Empty() {
		t.Fatal("automatic fee policy was not selected by clearing the input")
	}
	detail.HandleMsg(late)
	if !s.feeInput.Empty() || !strings.Contains(s.View(82, 34), "31 sat/vB") {
		t.Fatal("late suggestion replaced automatic policy or did not refresh hints")
	}
	s.focusZone, s.confirmBtnIdx = closeZoneButtons, 1
	detail.HandleKey("enter", tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.step != closeStepReview || s.attempt == nil || s.attempt.prepared.Request().SatPerVbyte != 0 || len(client.sent) != 0 {
		t.Fatal("review lost automatic fee policy or submitted without approval")
	}
}

func TestFeeRequestsEndWithTheirForms(t *testing.T) {
	for _, reason := range []string{"close", "replace", "dismiss close"} {
		t.Run(reason, func(t *testing.T) {
			s, _ := onChainScreen(t)
			var screen Screen = s
			section, kind := secOnChain, tabOnChain
			if reason == "dismiss close" {
				detail, _ := closeScreenFixture(strings.Repeat("a", 64) + ":0")
				screen = detail
				s.ctx = detail.ctx
				section, kind = secChannels, tabChannel
			}
			var readCtx context.Context
			s.ctx.Fees = feeTestReader{collect: func(ctx context.Context, _ string) app.FeeSuggestions {
				readCtx = ctx
				return app.FeeSuggestions{}
			}}
			fees := screenFees(screen)
			msg := fees.refresh(s.ctx)().(feeSuggestionsMsg)
			m := Model{nav: NewNavSidebar(), screenCtx: s.ctx, activeTab: 1,
				tabs: []openTab{{Kind: kind, Section: section, Screen: screen}}}
			m.nav.ActiveItem = section
			switch reason {
			case "close":
				closed, _ := m.closeTab(1)
				m = closed.(Model)
			case "replace":
				replacement, _ := onChainScreen(t)
				m.setTabScreen(1, replacement)
			case "dismiss close":
				screen.HandleKey("backspace", tea.KeyPressMsg{})
			}
			m.Update(msg)
			if readCtx.Err() == nil || fees.active != nil {
				t.Fatal("removed form kept a live fee request")
			}
		})
	}
}
