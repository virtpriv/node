package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
)

type screenPaymentClient struct {
	sent   []string
	result *lndrpc.SendPaymentResult
	err    error
}

func (c *screenPaymentClient) DecodePayReq(request string) (*lndrpc.DecodedPayReq, error) {
	return &lndrpc.DecodedPayReq{AmountSats: 42, Description: request}, nil
}

func (c *screenPaymentClient) SendPayment(request string) (*lndrpc.SendPaymentResult, error) {
	c.sent = append(c.sent, request)
	return c.result, c.err
}

func paymentScreen(client *screenPaymentClient) *SendScreen {
	s := NewSendScreen(&ScreenContext{Cfg: config.Default(), State: &RuntimeState{WalletKnown: true, WalletExists: true}})
	s.payments = client
	return s
}

func submitInvoice(t *testing.T, s *SendScreen, invoice string) tea.Cmd {
	t.Helper()
	s.sendInput.SetValue(invoice)
	_, decode := s.preparePaymentConfirmation()
	if decode == nil {
		t.Fatalf("invoice rejected before decode: %s", s.inputError)
	}
	return decode
}

func TestPaymentConfirmationSubmitsPreparedInvoiceOnce(t *testing.T) {
	client := &screenPaymentClient{result: &lndrpc.SendPaymentResult{Status: "SUCCEEDED"}}
	s := paymentScreen(client)
	decode := submitInvoice(t, s, "lightning:lnbc1approved")
	s.HandleMsg(decode())
	if s.step != sendStepConfirm || len(client.sent) != 0 {
		t.Fatal("invoice was not prepared for confirmation")
	}
	// Even an unrelated later form mutation must not replace the reviewed invoice.
	s.sendInput.SetValue("lnbc1different")
	_, send := s.HandleKey("enter", tea.KeyPressMsg{})
	if send == nil {
		t.Fatal("confirmation did not schedule payment")
	}
	if _, duplicate := s.HandleKey("enter", tea.KeyPressMsg{}); duplicate != nil {
		t.Fatal("repeated confirmation scheduled another payment")
	}
	s.HandleMsg(send())
	if len(client.sent) != 1 || client.sent[0] != "lnbc1approved" || s.result.Status != "SUCCEEDED" {
		t.Fatalf("sent=%v status=%s", client.sent, s.result.Status)
	}
}

func TestPaymentScreenIgnoresSupersededDecode(t *testing.T) {
	client := &screenPaymentClient{}
	s := paymentScreen(client)
	old := submitInvoice(t, s, "lnbc1old")
	current := submitInvoice(t, s, "lnbc1new")
	s.HandleMsg(old())
	if s.step != sendStepInput || s.inputError != "" {
		t.Fatal("old response changed the current input step")
	}
	s.HandleMsg(current())
	if s.step != sendStepConfirm || s.prepared.Details().Description != "lnbc1new" {
		t.Fatal("current response did not reach confirmation")
	}
	s.HandleMsg(old())
	if s.prepared.Details().Description != "lnbc1new" {
		t.Fatal("old response replaced confirmed details")
	}
}

func TestPaymentInputEditRejectsPendingDecode(t *testing.T) {
	s := paymentScreen(&screenPaymentClient{})
	decode := submitInvoice(t, s, "lnbc1old")
	s.sendInput.SetValue("lnbc1edited")
	s.HandleMsg(decode())
	if s.step != sendStepInput {
		t.Fatal("edited input accepted its previous decode")
	}
}

func TestPaymentResponseCannotCrossScreenInstances(t *testing.T) {
	client := &screenPaymentClient{result: &lndrpc.SendPaymentResult{Status: "SUCCEEDED"}}
	old := paymentScreen(client)
	decodeOld := submitInvoice(t, old, "lnbc1same")
	old.HandleMsg(decodeOld())
	_, sendOld := old.handleConfirmKey("enter")
	current := paymentScreen(client)
	decodeCurrent := submitInvoice(t, current, "lnbc1same")
	current.HandleMsg(decodeOld())
	if current.step != sendStepInput {
		t.Fatal("a closed screen's decode reached its replacement")
	}
	current.HandleMsg(decodeCurrent())
	current.HandleKey("enter", tea.KeyPressMsg{})
	current.HandleMsg(sendOld())
	if current.step != sendStepInFlight {
		t.Fatal("a closed screen's result reached its replacement")
	}
}

func TestPaymentOutcomesReachHiddenWalletAndRefreshHistory(t *testing.T) {
	theme.Init(true)
	for _, tc := range []struct {
		name, status, heading string
		err                   error
	}{
		{"success", "SUCCEEDED", "Payment Sent", nil},
		{"failure", "FAILED", "Payment Failed", nil},
		{"unresolved", "IN_FLIGHT", "Payment Still In Flight", nil},
		{"unknown status", "FUTURE_STATUS", "Payment Status Unknown", nil},
		{"transport error", "", "Payment Status Unknown", errors.New("connection lost")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &screenPaymentClient{result: &lndrpc.SendPaymentResult{Status: tc.status}, err: tc.err}
			s := paymentScreen(client)
			r := &historyTestReader{next: historySnapshot(nil, nil)}
			s.ctx.PaymentHistory = &paymentHistoryContext{reader: r, scope: s.ctx.walletObservationScope()}
			m := Model{nav: NewNavSidebar(), screenCtx: s.ctx, tabs: []openTab{{Kind: tabSend, Section: secWallet, Screen: s}}}
			decode := submitInvoice(t, s, "lnbc1example")
			statusUpdate(&m, decode())
			if s.step != sendStepConfirm {
				t.Fatal("hidden payment tab lost its decoded invoice")
			}
			_, send := s.handleConfirmKey("enter")
			result := send()
			changed := statusUpdate(&m, result)
			if !isHistoryChange(changed) {
				t.Fatal("accepted payment result omitted history invalidation")
			}
			read := statusUpdate(&m, changed())
			statusUpdate(&m, read())
			if r.calls != 1 || !s.ctx.PaymentHistory.Fresh() || statusUpdate(&m, result) != nil {
				t.Fatal("hidden payment result lost history or duplicate triggered refresh")
			}
			view := s.View(82, 34)
			if !strings.Contains(view, tc.heading) {
				t.Fatalf("missing %q in %s", tc.heading, view)
			}
			if tc.status != "FAILED" && strings.Contains(view, "Payment Failed") {
				t.Fatal("invented failure")
			}
			if tc.status != "SUCCEEDED" && strings.Contains(view, "Payment Sent") {
				t.Fatal("invented success")
			}
			if len(client.sent) != 1 {
				t.Fatal("retried a payment")
			}
		})
	}
}
