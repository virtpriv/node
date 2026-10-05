package tui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
)

// Drive presence changes and user submission through the model, including
// detail tabs that were already open before the outage.
func exerciseUnavailableWalletAction(t *testing.T, ctx *ScreenContext, screen Screen, kind tabKind, section int, verify func()) {
	t.Helper()
	ctx.ContentFocused = true
	m := Model{screenCtx: ctx, state: ctx.State, nav: NewNavSidebar(), lndClient: &lndrpc.Client{}, contentFocused: true}
	m.nav.ActiveItem, m.nav.Cursor, m.nav.Focused = section, section, false
	if kind == tabMain {
		m.sectionScreens[section] = screen
	} else {
		m.tabs = []openTab{{Kind: kind, Section: section, Screen: screen}}
		m.activeTab = 1
		ctx.HasTabs = true
	}
	statusUpdate(&m, walletObservationMsg(t, &m, false, errors.New("LND stopped")))
	if cmd := statusUpdate(&m, tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("unknown wallet state allowed submission")
	}
	statusUpdate(&m, walletObservationMsg(t, &m, true, nil))
	cmd := statusUpdate(&m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("verified wallet still blocked explicit submission")
	}
	// Work admitted before a later outage still owns its result.
	statusUpdate(&m, walletObservationMsg(t, &m, false, errors.New("presence read failed after submission")))
	statusUpdate(&m, cmd())
	verify()
}

func TestExistingWalletFormsBlockSubmissionAndRecover(t *testing.T) {
	t.Run("on-chain send", func(t *testing.T) {
		s, client := onChainScreen(t)
		s.validateAndConfirm()
		s.confirmBtnIdx = 1
		want := s.attempt.prepared.Request()
		exerciseUnavailableWalletAction(t, s.ctx, s, tabOnChain, secOnChain, func() {
			if !reflect.DeepEqual(client.sent, []lndrpc.SendCoinsRequest{want}) || s.step != ocStepResult {
				t.Fatal("on-chain intent or owned completion was lost")
			}
		})
	})
	t.Run("Lightning send", func(t *testing.T) {
		client := &screenPaymentClient{result: &lndrpc.SendPaymentResult{Status: "SUCCEEDED"}}
		s := paymentScreen(client)
		s.HandleMsg(submitInvoice(t, s, "lnbc1approved")())
		exerciseUnavailableWalletAction(t, s.ctx, s, tabSend, secWallet, func() {
			if len(client.sent) != 1 || client.sent[0] != "lnbc1approved" || s.step != sendStepResult {
				t.Fatal("payment intent or owned completion was lost")
			}
		})
	})
	t.Run("invoice creation", func(t *testing.T) {
		client := &screenInvoiceClient{}
		s := receiveScreen(client)
		exerciseUnavailableWalletAction(t, s.ctx, s, tabReceive, secWallet, func() {
			if len(client.requests) != 1 || client.requests[0] != (app.InvoiceRequest{AmountSats: 42, Memo: "coffee", Blinded: true}) || s.step != recvStepWaiting {
				t.Fatal("invoice draft or owned completion was lost")
			}
		})
	})
	t.Run("new address", func(t *testing.T) {
		client := &receiveAddressClient{address: receiveAddress(t, 1)}
		_, s := receiveAddressModel(client)
		s.HandleMsg(s.Init()())
		client.address = receiveAddress(t, 2)
		s.HandleKey("right", tea.KeyPressMsg{Code: tea.KeyRight})
		exerciseUnavailableWalletAction(t, s.ctx, s, tabOCReceive, secOnChain, func() {
			if client.calls != 2 || s.address != client.address || s.requesting {
				t.Fatal("replacement address or owned completion was lost")
			}
		})
	})
	t.Run("open channel", func(t *testing.T) {
		s, client := channelScreen(t)
		s.prepareChannelOpenConfirmation()
		s.confirmBtnIdx = 1
		want := s.attempt.prepared.Request()
		exerciseUnavailableWalletAction(t, s.ctx, s, tabOpenChannel, secChannels, func() {
			if !reflect.DeepEqual(client.sent, []lndrpc.ChannelOpenRequest{want}) || s.step != coStepResult {
				t.Fatal("channel intent or owned completion was lost")
			}
		})
	})
	t.Run("close channel", func(t *testing.T) {
		detail, client := closeScreenFixture(strings.Repeat("a", 64) + ":0")
		s := detail.closeScreen
		reviewClose(t, s, false, 3)
		s.confirmBtnIdx = 1
		want := s.attempt.prepared.Request()
		exerciseUnavailableWalletAction(t, detail.ctx, detail, tabChannel, secChannels, func() {
			if len(client.sent) != 1 || client.sent[0] != want || s.step != closeStepResult {
				t.Fatal("close intent or owned completion was lost")
			}
		})
	})
	t.Run("save label", func(t *testing.T) {
		_, s, client := labelModel()
		s.openLabelPopup()
		s.labelInput.SetValue("retained draft")
		s.HandleKey("tab", tea.KeyPressMsg{Code: tea.KeyTab})
		wantTxid := s.ocCtx.Utxos.Value[0].Txid
		exerciseUnavailableWalletAction(t, s.ctx, s, tabMain, secOnChain, func() {
			if len(client.requests) != 1 || client.requests[0].Txid.String() != wantTxid || client.requests[0].Label != "retained draft" || s.labelPending || s.labelEditing {
				t.Fatal("label draft or owned completion was lost")
			}
		})
	})
}

func TestWalletInputDraftsStayVisibleDuringOutage(t *testing.T) {
	theme.Init(true)
	payment := paymentScreen(&screenPaymentClient{})
	payment.sendInput.SetValue("lnbc1draft")
	channel, _ := channelScreen(t)
	invoice := receiveScreen(&screenInvoiceClient{})
	for _, test := range []struct {
		name   string
		screen Screen
		ctx    *ScreenContext
		draft  string
	}{
		{"Lightning payment", payment, payment.ctx, "lnbc1draft"},
		{"channel", channel, channel.ctx, "reviewed peer"},
		{"invoice", invoice, invoice.ctx, "coffee"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := test.ctx
			m := Model{screenCtx: ctx, state: ctx.State}
			statusUpdate(&m, walletObservationMsg(t, &m, false, errors.New("LND stopped")))
			if view := ansi.Strip(test.screen.View(82, 40)); !strings.Contains(view, test.draft) {
				t.Fatalf("outage hid the input draft:\n%s", view)
			}
		})
	}
}
