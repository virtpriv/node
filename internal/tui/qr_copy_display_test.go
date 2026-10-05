package tui

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
)

func qrCopyDisplayModel(t *testing.T, action string) (Model, Screen, string) {
	t.Helper()
	theme.Init(true)
	cfg := config.Default()
	cfg.SyncthingEnabled = true
	ctx := &ScreenContext{Cfg: cfg, State: &RuntimeState{WalletKnown: true, WalletExists: true}, ContentFocused: true,
		Status: &app.StatusSnapshot{Node: freshStatus(lndrpc.NodeInfo{Pubkey: "key", URIs: []string{"key@original.onion:9735", "key@203.0.113.1:9735"}})}}
	m := Model{cfg: cfg, state: ctx.State, screenCtx: ctx, nav: NewNavSidebar(), width: 100, height: 40}
	var screen Screen
	var want string
	switch action {
	case "node clear", "node tor", "node copy":
		_, open := NewChannelsHomeScreen(ctx).openNodeInfo()
		statusUpdate(&m, open())
		s := m.tabs[0].Screen.(*NodeInfoScreen)
		screen, want = s, "key@203.0.113.1:9735"
		if action == "node tor" {
			s.buttonIdx, want = 1, "key@original.onion:9735"
		} else if action == "node copy" {
			s.buttonIdx, want = 2, "key@original.onion:9735"
		}
	case "invoice qr", "invoice copy":
		m.nav.SetActive(secWallet)
		s := NewReceiveScreen(ctx)
		s.invoices = &screenInvoiceClient{}
		s.amountInput.SetSats(42)
		statusUpdate(&m, openTabMsg{Kind: tabReceive, Screen: s})
		_, create := s.submitInvoice()
		statusUpdate(&m, create())
		if action == "invoice copy" {
			s.buttonIdx = 1
		}
		screen, want = s, "lntbs1invoice1"
	case "syncthing qr":
		m.nav.SetActive(secAddons)
		s := NewSyncthingPairScreen(ctx)
		statusUpdate(&m, openTabMsg{Kind: tabSyncthingPair, Screen: s})
		s.step, s.attempt = syncPairStepPairing, 1
		s.HandleMsg(syncthingPairedMsg{owner: s, attempt: 1,
			result: app.SyncthingResult{Outcome: app.SyncthingComplete, LocalID: "LOCAL-DEVICE-ID"}})
		screen, want = s, "LOCAL-DEVICE-ID"
	default:
		t.Fatal("unknown display fixture")
	}
	return m, screen, want
}

func displayAction(t *testing.T, screen Screen) tea.Msg {
	t.Helper()
	_, command := screen.HandleKey("enter", tea.KeyPressMsg{})
	if command == nil {
		t.Fatal("current display action was not offered")
	}
	return command()
}

func dismissQRCopyOverlay(m *Model) {
	updated, _ := m.handleGenericSubviewKey("enter")
	*m = updated.(Model)
}

func TestQRCopyDisplaysRejectHiddenRetiredAndReplayedActions(t *testing.T) {
	for _, action := range []string{"node clear", "node tor", "node copy", "invoice qr", "invoice copy", "syncthing qr"} {
		t.Run(action, func(t *testing.T) {
			m, s, want := qrCopyDisplayModel(t, action)
			late := displayAction(t, s)
			section := m.nav.ActiveSection()
			m.nav.SetActive(secSystem)
			if cmd := statusUpdate(&m, late); cmd != nil || m.subview != svNone {
				t.Fatal("hidden owner interrupted another section")
			}
			m.nav.SetActive(section)
			updated, _ := m.closeTab(1)
			m = updated.(Model)
			replacement, current, _ := qrCopyDisplayModel(t, action)
			m.tabs, m.activeTab = replacement.tabs, 1
			if cmd := statusUpdate(&m, late); cmd != nil || m.subview != svNone {
				t.Fatal("retired owner affected the replacement in its old slot")
			}
			msg := displayAction(t, current)
			cmd := statusUpdate(&m, msg)
			request := msg.(qrCopyDisplayMsg).request
			if request.copy {
				if cmd == nil || !strings.Contains(request.text, want) {
					t.Fatal("copy handoff lost its exact payload")
				}
				if _, duplicate := current.HandleKey("enter", tea.KeyPressMsg{}); duplicate != nil {
					t.Fatal("pending terminal handoff allowed another request")
				}
				statusUpdate(&m, qrCopyDisplayDoneMsg{request: request})
			} else {
				if cmd != nil || m.subview != svQR || m.urlTarget != want || !strings.ContainsAny(m.viewQR(), "▄▀█") {
					t.Fatal("current QR does not contain the requested payload")
				}
				dismissQRCopyOverlay(&m)
			}
			if cmd := statusUpdate(&m, msg); cmd != nil || m.subview != svNone {
				t.Fatal("completed display request was replayed")
			}
		})
	}
}

func TestNodeDisplaysRequireCurrentAdvertisements(t *testing.T) {
	for _, action := range []string{"node clear", "node tor", "node copy"} {
		t.Run(action, func(t *testing.T) {
			m, screen, _ := qrCopyDisplayModel(t, action)
			s := screen.(*NodeInfoScreen)
			late := displayAction(t, s)
			m.screenCtx.Status.Node.Err = errors.New("offline")
			if cmd := statusUpdate(&m, late); cmd != nil || m.subview != svNone {
				t.Fatal("queued display shared a stale advertisement")
			}
			if _, cmd := s.HandleKey("enter", tea.KeyPressMsg{}); cmd != nil {
				t.Fatal("stale view offered a new sharing action")
			}
			m.screenCtx.Status.Node.Err = nil
			// Change the original backing slice to exercise immutable Copy capture.
			m.screenCtx.Status.Node.Value.URIs[0] = "key@changed.onion:9735"
			m.screenCtx.Status.Node.Value.URIs[1] = "key@203.0.113.2:9735"
			if cmd := statusUpdate(&m, late); cmd != nil || m.subview != svNone {
				t.Fatal("old action survived a changed advertisement")
			}
			msg := displayAction(t, s)
			cmd := statusUpdate(&m, msg)
			if action == "node copy" {
				if cmd == nil || !strings.Contains(msg.(qrCopyDisplayMsg).request.text, "changed.onion") {
					t.Fatal("copy did not recover with current advertisements")
				}
				return
			}
			if m.subview != svQR {
				t.Fatal("current advertisement did not recover")
			}
			m.screenCtx.Status.Node.Err = errors.New("offline again")
			if !strings.Contains(m.viewQR(), "unavailable") {
				t.Fatal("open QR retained stale advertisement")
			}
			m.screenCtx.Status.Node.Err = nil
			m.screenCtx.Status.Node.Value.Pubkey = "replacement-node"
			if !strings.Contains(m.viewQR(), "unavailable") {
				t.Fatal("open QR ignored changed node identity")
			}
		})
	}
}

func TestInvoiceQRRetainsUnknownOutcomeAndRetiresTerminalOutcome(t *testing.T) {
	for _, outcome := range []app.InvoiceState{app.InvoicePaid, app.InvoiceExpired, app.InvoiceCanceled, app.InvoiceMissing} {
		m, screen, want := qrCopyDisplayModel(t, "invoice qr")
		s := screen.(*ReceiveScreen)
		statusUpdate(&m, displayAction(t, s))
		statusUpdate(&m, invoiceStatusMsg{attempt: s.attempt, err: errors.New("lookup unavailable")})
		if m.urlTarget != want || strings.Contains(m.viewQR(), "unavailable") {
			t.Fatal("lookup failure invalidated the issued invoice")
		}
		statusUpdate(&m, invoiceCheckMsg{attempt: s.attempt})
		statusUpdate(&m, invoiceStatusMsg{attempt: s.attempt, state: outcome})
		if !strings.Contains(m.viewQR(), "unavailable") {
			t.Fatal("terminal invoice outcome left its QR visible")
		}
		dismissQRCopyOverlay(&m)
		if m.subview != svNone {
			t.Fatal("invalidated QR could not be dismissed")
		}
		for _, copy := range []bool{false, true} {
			late := &qrCopyDisplayRequest{owner: s, text: want, invoice: s.attempt, wallet: s.attempt.wallet, copy: copy}
			if cmd := statusUpdate(&m, s.display.command(late)()); cmd != nil || m.subview != svNone {
				t.Fatal("retired invoice readmitted QR or Copy")
			}
		}
	}
	for _, action := range []string{"invoice qr", "invoice copy"} {
		m, s, _ := qrCopyDisplayModel(t, action)
		late := displayAction(t, s)
		m.screenCtx.walletGeneration++
		if cmd := statusUpdate(&m, late); cmd != nil || m.subview != svNone {
			t.Fatal("queued invoice display crossed a wallet change")
		}
		if cmd := statusUpdate(&m, displayAction(t, s)); cmd != nil || m.subview != svNone {
			t.Fatal("old wallet invoice was readmitted in a new wallet session")
		}
	}
}

func TestInvoiceCopyReturnDoesNotReviveMissingInvoice(t *testing.T) {
	m, screen, _ := qrCopyDisplayModel(t, "invoice copy")
	s := screen.(*ReceiveScreen)
	request := displayAction(t, s).(qrCopyDisplayMsg).request
	if statusUpdate(&m, qrCopyDisplayMsg{request: request}) == nil {
		t.Fatal("Copy was not admitted")
	}
	// A queued lookup may complete while terminal I/O has suspended rendering.
	s.invoices.(*screenInvoiceClient).err = lndrpc.ErrInvoiceNotFound
	result := checkInvoiceCmd(s.invoices, s.attempt, s.invoice)()
	statusUpdate(&m, qrCopyDisplayDoneMsg{request: request})
	statusUpdate(&m, result)
	if m.copyTerminal != nil || s.step != recvStepMissing || request.current() {
		t.Fatal("return from Copy retained a missing invoice")
	}
	view := s.View(82, 40)
	if !strings.Contains(view, "Invoice unavailable") || strings.Contains(view, "Payment Received") || strings.Contains(view, "Invoice Expired") {
		t.Fatal("missing invoice claimed a payment outcome")
	}
	// An obsolete completion cannot revive a retired request or change its result.
	statusUpdate(&m, qrCopyDisplayDoneMsg{request: request, err: errors.New("late failure")})
	if s.step != recvStepMissing || s.display.failed {
		t.Fatal("obsolete Copy completion changed the retired invoice")
	}
}

func TestSyncthingQRUsesSuccessfulPairingSnapshot(t *testing.T) {
	m, screen, _ := qrCopyDisplayModel(t, "syncthing qr")
	s := screen.(*SyncthingPairScreen)
	late := displayAction(t, s)
	s.attempt++
	if cmd := statusUpdate(&m, late); cmd != nil || m.subview != svNone {
		t.Fatal("old pairing admitted QR")
	}
	statusUpdate(&m, displayAction(t, s))
	m.screenCtx.State.SyncthingDevicesErr = errors.New("offline")
	if strings.Contains(m.viewQR(), "unavailable") {
		t.Fatal("device-list outage invalidated the pairing snapshot")
	}
	s.result.LocalID = "REPLACEMENT-ID"
	if !strings.Contains(m.viewQR(), "unavailable") {
		t.Fatal("open QR ignored changed local identity")
	}
	dismissQRCopyOverlay(&m)
	m.cfg.SyncthingEnabled = false
	if cmd := statusUpdate(&m, displayAction(t, s)); cmd != nil || m.subview != svNone {
		t.Fatal("disabled add-on admitted QR")
	}
}

func TestCopyTerminalFailureOwnershipAndRetry(t *testing.T) {
	for _, action := range []string{"node copy", "invoice copy"} {
		m, s, _ := qrCopyDisplayModel(t, action)
		first := displayAction(t, s)
		statusUpdate(&m, first)
		request := first.(qrCopyDisplayMsg).request
		section := m.nav.ActiveSection()
		m.nav.SetActive(secSystem)
		m.screenCtx.Status.Node.Err = errors.New("observation changed during copy")
		statusUpdate(&m, qrCopyDisplayDoneMsg{request: request, err: io.EOF})
		if !strings.Contains(s.View(82, 40), "did not complete") {
			t.Fatal("hidden owner lost terminal failure")
		}
		m.nav.SetActive(section)
		m.screenCtx.Status.Node.Err = nil
		retry := displayAction(t, s)
		if cmd := statusUpdate(&m, retry); cmd == nil {
			t.Fatal("failed copy could not retry")
		}
		statusUpdate(&m, qrCopyDisplayDoneMsg{request: request, err: io.EOF})
		if strings.Contains(s.View(82, 40), "did not complete") {
			t.Fatal("old completion overwrote newer handoff")
		}
		statusUpdate(&m, qrCopyDisplayDoneMsg{request: retry.(qrCopyDisplayMsg).request})
		if strings.Contains(s.View(82, 40), "did not complete") {
			t.Fatal("successful retry retained failure")
		}
		last := displayAction(t, s)
		statusUpdate(&m, last)
		updated, _ := m.closeTab(1)
		m = updated.(Model)
		replacement, next, _ := qrCopyDisplayModel(t, action)
		m.tabs, m.activeTab = replacement.tabs, 1
		statusUpdate(&m, qrCopyDisplayDoneMsg{request: last.(qrCopyDisplayMsg).request, err: io.EOF})
		if strings.Contains(next.View(82, 40), "did not complete") {
			t.Fatal("retired terminal owner published to a replacement")
		}
		if cmd := statusUpdate(&m, displayAction(t, next)); cmd == nil {
			t.Fatal("retired completion left terminal admission blocked")
		}
	}
}

type failingCopyWriter struct{ attempts bytes.Buffer }

func (w *failingCopyWriter) Write(p []byte) (int, error) {
	w.attempts.Write(p)
	return 0, io.ErrClosedPipe
}

func TestCopyTerminalClearsDisplayOnSuccessAndFailure(t *testing.T) {
	for _, input := range []string{"\n", ""} {
		var output bytes.Buffer
		d := &copyTextDisplay{text: "lntbs1invoice1"}
		d.SetStdin(strings.NewReader(input))
		d.SetStdout(&output)
		err := d.Run()
		if (input == "\n" && err != nil) || (input == "" && !errors.Is(err, io.EOF)) {
			t.Fatalf("acknowledgement: %v", err)
		}
		text := output.String()
		if !strings.Contains(text, "lntbs1invoice1") ||
			!strings.HasPrefix(text, "\x1b[2J\x1b[3J\x1b[H") || !strings.HasSuffix(text, "\x1b[2J\x1b[3J\x1b[H") {
			t.Fatal("copy payload lost or display cleanup skipped")
		}
	}
	writer := &failingCopyWriter{}
	d := &copyTextDisplay{text: "invoice", in: strings.NewReader("\n"), out: writer}
	if !errors.Is(d.Run(), io.ErrClosedPipe) || !strings.HasSuffix(writer.attempts.String(), "\x1b[2J\x1b[3J\x1b[H") {
		t.Fatal("write failure was hidden or cleanup skipped")
	}
}

func TestQRCopyDisplaysRejectSupersededRequestsAndTerminalInvoice(t *testing.T) {
	for _, action := range []string{"node tor", "node copy", "invoice qr", "invoice copy", "syncthing qr"} {
		m, s, _ := qrCopyDisplayModel(t, action)
		old := displayAction(t, s)
		current := displayAction(t, s)
		if cmd := statusUpdate(&m, old); cmd != nil || m.subview != svNone {
			t.Fatal("superseded request opened a display")
		}
		if invoice, ok := s.(*ReceiveScreen); ok {
			statusUpdate(&m, invoiceStatusMsg{attempt: invoice.attempt, state: app.InvoicePaid})
			if cmd := statusUpdate(&m, current); cmd != nil || m.subview != svNone {
				t.Fatal("queued display survived observed settlement")
			}
		} else {
			cmd := statusUpdate(&m, current)
			if cmd == nil && m.subview != svQR {
				t.Fatal("current request was lost")
			}
		}
	}
}

func TestOversizedQRRemainsDismissible(t *testing.T) {
	theme.Init(true)
	m := Model{width: 100, height: 40, subview: svQR, urlTarget: strings.Repeat("x", 4000), qrLabel: "Invoice"}
	if !strings.Contains(m.viewQR(), "QR not available") {
		t.Fatal("encoding failure lacked a usable fallback")
	}
	dismissQRCopyOverlay(&m)
	if m.subview != svNone {
		t.Fatal("encoding failure trapped the overlay")
	}
}

// Exercise Bubble Tea's actual command and callback wiring. This checks I/O and
// result ownership; SSH terminal selection/clearing still needs the live slate.
type copyExecutionModel struct {
	model   Model
	command tea.Cmd
	done    bool
	err     error
}

func (m copyExecutionModel) Init() tea.Cmd  { return m.command }
func (m copyExecutionModel) View() tea.View { return tea.NewView("") }
func (m copyExecutionModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case qrCopyDisplayDoneMsg:
		m.err = msg.err
	case connectionDisplayDoneMsg:
		m.err = msg.err
	default:
		return m, nil
	}
	updated, _ := m.model.Update(msg)
	m.model, m.done = updated.(Model), true
	return m, tea.Quit
}

func TestCopyHandoffRendersAcceptedSnapshotAndDeliversFailure(t *testing.T) {
	for _, action := range []string{"node copy", "invoice copy", "macaroon copy"} {
		t.Run(action, func(t *testing.T) {
			var m Model
			var screen Screen
			var want string
			if action == "macaroon copy" {
				var init tea.Cmd
				m, screen, _, init = connectionModel(t, false)
				statusUpdate(&m, init())
				screen.HandleKey("right", tea.KeyPressMsg{})
				screen.HandleKey("right", tea.KeyPressMsg{})
				want = hex.EncodeToString([]byte("synthetic-secret"))
			} else {
				m, screen, want = qrCopyDisplayModel(t, action)
			}
			request := displayAction(t, screen)
			command := statusUpdate(&m, request)
			if command == nil {
				t.Fatal("copy was not admitted")
			}
			// A result arriving between admission and terminal execution must not
			// substitute new text or discard the accepted display's failure.
			if action == "macaroon copy" {
				connectionState(screen).info.Credential = "replacement-secret"
			} else if action == "invoice copy" {
				screen.(*ReceiveScreen).invoice = app.LightningInvoice{}
			} else {
				m.screenCtx.Status.Node.Value.URIs[0] = "key@replacement.onion:9735"
			}
			m.screenCtx.Status.Node.Err = errors.New("unavailable after admission")
			m.nav.SetActive(secSystem)
			var output bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p := tea.NewProgram(copyExecutionModel{model: m, command: command},
				tea.WithContext(ctx), tea.WithInput(strings.NewReader("")), tea.WithOutput(&output),
				tea.WithoutRenderer(), tea.WithoutSignalHandler())
			final, err := p.Run()
			if err != nil {
				t.Fatal(err)
			}
			result := final.(copyExecutionModel)
			// Pairing prioritizes its offline view. Once availability recovers,
			// the owning screen must still report the terminal failure.
			m.screenCtx.Status.Node.Err = nil
			if !result.done || !errors.Is(result.err, io.EOF) ||
				!strings.Contains(screen.View(82, 40), "did not complete") {
				t.Fatal("terminal failure did not reach the owning screen")
			}
			if text := output.String(); !strings.Contains(text, want) ||
				strings.Contains(text, "replacement.onion") || strings.Contains(text, "replacement-secret") {
				t.Fatal("terminal handoff changed its accepted payload")
			}
			if action == "node copy" && !strings.Contains(output.String(), "key@203.0.113.1:9735") {
				t.Fatal("copy dropped the accepted clearnet URI")
			}
		})
	}
}
