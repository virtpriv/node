package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
)

type receiveAddressClient struct {
	address string
	err     error
	calls   int
}

func (c *receiveAddressClient) GetNewAddress() (*lndrpc.OnChainAddress, error) {
	c.calls++
	return &lndrpc.OnChainAddress{Address: c.address}, c.err
}

func receiveAddress(t *testing.T, index byte) string {
	t.Helper()
	key := make([]byte, 32)
	key[0] = index
	address, err := btcutil.NewAddressTaproot(key, &chaincfg.MainNetParams)
	if err != nil {
		t.Fatal(err)
	}
	return address.EncodeAddress()
}

func receiveAddressModel(client *receiveAddressClient) (Model, *OCReceiveScreen) {
	s := NewOCReceiveScreen(&ScreenContext{Cfg: config.Default(), State: &RuntimeState{WalletKnown: true, WalletExists: true}, ContentFocused: true})
	s.addresses = client
	m := Model{nav: NewNavSidebar(), screenCtx: s.ctx,
		tabs: []openTab{{Kind: tabOCReceive, Section: secOnChain, Screen: s}}}
	return m, s
}

func updateReceiveWithoutCommand(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	updated, cmd := m.Update(msg)
	if cmd != nil {
		t.Fatalf("%T scheduled unexpected follow-up work", msg)
	}
	return updated.(Model)
}

func TestOnChainReceivePreservesAddressAcrossFailureAndExplicitRetry(t *testing.T) {
	theme.Init(true)
	first, second := receiveAddress(t, 1), receiveAddress(t, 2)
	client := &receiveAddressClient{address: first, err: errors.New("wallet locked")}
	m, s := receiveAddressModel(client)
	initial := s.Init()
	if s.Init() != nil {
		t.Fatal("initialization scheduled a second derivation")
	}
	if _, cmd := s.HandleKey("enter", tea.KeyPressMsg{}); cmd != nil {
		t.Fatal("repeated Enter scheduled a second derivation")
	}
	// Channels is visible; both failure and success must reach the hidden owner.
	m = updateReceiveWithoutCommand(t, m, initial())
	if s.requesting || s.errMsg == "" || !strings.Contains(s.View(82, 34), "Retry") {
		t.Fatal("initial failure did not offer explicit recovery")
	}
	client.err = nil
	_, retry := s.HandleKey("enter", tea.KeyPressMsg{})
	initialResult := retry()
	m = updateReceiveWithoutCommand(t, m, initialResult)
	if s.address != first || s.requesting || client.calls != 2 {
		t.Fatal("hidden retry did not display its address")
	}
	m.nav.SetActive(secOnChain)
	m.activeTab = 1
	_, qr := s.HandleKey("enter", tea.KeyPressMsg{})
	oldQR := qr()
	m = updateReceiveWithoutCommand(t, m, oldQR)
	if m.subview != svQR || m.urlTarget != first {
		t.Fatal("QR differs from the displayed address")
	}
	m.subview = svNone
	s.HandleKey("right", tea.KeyPressMsg{})
	_, replace := s.HandleKey("enter", tea.KeyPressMsg{})
	if s.address != first || !strings.Contains(s.View(82, 34), first) {
		t.Fatal("pending replacement hid the previous address")
	}
	m = updateReceiveWithoutCommand(t, m, initialResult)
	if !s.requesting || s.address != first {
		t.Fatal("old completion consumed the replacement attempt")
	}
	client.err = errors.New("replacement unavailable")
	failedResult := replace()
	m = updateReceiveWithoutCommand(t, m, failedResult)
	if s.requesting || s.address != first || s.errMsg == "" || client.calls != 3 {
		t.Fatal("failed replacement lost the usable address or retried automatically")
	}
	m = updateReceiveWithoutCommand(t, m, oldQR)
	if m.subview == svQR {
		t.Fatal("old QR action survived a newer request")
	}
	client.address, client.err = second, nil
	_, retry = s.HandleKey("enter", tea.KeyPressMsg{})
	m = updateReceiveWithoutCommand(t, m, retry())
	m = updateReceiveWithoutCommand(t, m, failedResult)
	if s.address != second || s.errMsg != "" || s.requesting || client.calls != 4 {
		t.Fatal("late failure replaced the successful explicit retry")
	}
	_, qr = s.HandleKey("enter", tea.KeyPressMsg{})
	m = updateReceiveWithoutCommand(t, m, qr())
	if m.urlTarget != second {
		t.Fatal("replacement QR retained the old address")
	}
	m.subview = svNone
	m.nav.SetActive(secWallet)
	m = updateReceiveWithoutCommand(t, m, qr())
	if m.subview == svQR {
		t.Fatal("delayed QR action interrupted another section")
	}
}

func TestOnChainReceiveCloseReopenAndNavigationOwnResults(t *testing.T) {
	client := &receiveAddressClient{address: receiveAddress(t, 1)}
	m, old := receiveAddressModel(client)
	pending := old.Init()
	m.nav.SetActive(secOnChain)
	updated, _ := m.closeTab(1)
	m = updated.(Model)
	if len(m.tabs) != 0 {
		t.Fatal("address generation prevented deliberate close")
	}
	current := NewOCReceiveScreen(old.ctx)
	current.addresses = client
	updated, create := m.Update(openTabMsg{Kind: tabOCReceive, Screen: current})
	m = updated.(Model)
	m = updateReceiveWithoutCommand(t, m, pending())
	if current.address != "" || !current.requesting {
		t.Fatal("closed owner's completion populated the reopened screen")
	}
	client.address = receiveAddress(t, 2)
	result := create()
	m = updateReceiveWithoutCommand(t, m, result)
	if current.address != client.address || current.requesting {
		t.Fatal("reopened screen did not accept its own address")
	}
	updated, cmd := m.Update(openTabMsg{Kind: tabOCReceive, Screen: NewOCReceiveScreen(old.ctx)})
	m = updated.(Model)
	if cmd != nil || len(m.tabs) != 1 || m.tabs[0].Screen != current || client.calls != 2 {
		t.Fatal("returning to Receive replaced its address or requested another")
	}
	duplicate := result.(newAddressMsg)
	duplicate.err = errors.New("duplicate completion")
	m = updateReceiveWithoutCommand(t, m, duplicate)
	if current.errMsg != "" || current.address != client.address {
		t.Fatal("duplicate completion changed the finished request")
	}
	_, qr := current.HandleKey("enter", tea.KeyPressMsg{})
	updated, _ = m.closeTab(1)
	m = updated.(Model)
	m = updateReceiveWithoutCommand(t, m, qr())
	if m.subview == svQR {
		t.Fatal("closed screen's QR action opened an overlay")
	}
}
