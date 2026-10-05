package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/config"
)

func TestP2PObservationOwnershipAndReviewedConsent(t *testing.T) {
	w := app.NewHelperWorkflows("2.1.1")
	w.Close() // Submission has real operation identity without root IPC.
	ctx := &ScreenContext{Cfg: config.Default(), HelperWorkflows: w}
	s := NewP2PUpgradeScreen(ctx)
	other := NewP2PUpgradeScreen(ctx)
	if s.Init() == nil || other.Init() == nil || s.Init() != nil {
		t.Fatal("observation admission failed")
	}
	t.Cleanup(s.cancelAddressRead)
	t.Cleanup(other.cancelAddressRead)
	m := Model{nav: NewNavSidebar(), screenCtx: ctx, tabs: []openTab{
		{Kind: tabP2PUpgrade, Section: secSystem, Screen: s},
	}}
	m.nav.ActiveItem = secWallet
	request := reviewedP2PRequest(t)
	foreign := p2pAddressResultMsg{read: other.addressRead, request: request}
	m.Update(foreign)
	if s.step != p2pLoading {
		t.Fatal("foreign observation changed confirmation")
	}
	if _, cmd := s.HandleKey("enter", tea.KeyPressMsg{}); cmd != nil || s.progress != nil {
		t.Fatal("submitted before observation")
	}
	msg := p2pAddressResultMsg{read: s.addressRead, request: request}
	m.Update(msg)
	if s.step != p2pConfirm || !strings.Contains(s.View(82, 34), request.Address()) {
		t.Fatal("hidden owner lost its observed address")
	}
	msg.err = errors.New("late failure")
	m.Update(msg)
	if s.step != p2pConfirm || s.request != request {
		t.Fatal("replay replaced successful observation")
	}
	// Exercise the actual typed-consent and final-confirmation key paths.
	s.HandleKey("enter", tea.KeyPressMsg{})
	s.HandleKey("enter", tea.KeyPressMsg{})
	if s.step != p2pConfirm || s.progress != nil {
		t.Fatal("accepted missing typed consent")
	}
	s.focusZone = p2pZoneInput
	s.HandleMsg(tea.PasteMsg{Content: "PUBLISH MY IP"})
	s.HandleKey("enter", tea.KeyPressMsg{})
	s.HandleKey("enter", tea.KeyPressMsg{})
	if s.step != p2pConfirm2 || s.confirm2Idx != 0 || !strings.Contains(s.View(82, 34), request.Address()) {
		t.Fatal("final review lost its address or safe default")
	}
	s.HandleKey("enter", tea.KeyPressMsg{})
	if s.step != p2pConfirm || s.progress != nil {
		t.Fatal("Go Back submitted the operation")
	}
	s.HandleKey("enter", tea.KeyPressMsg{})
	s.HandleKey("right", tea.KeyPressMsg{})
	s.HandleKey("enter", tea.KeyPressMsg{})
	if s.step != p2pProgress || s.progress == nil {
		t.Fatal("confirmed request did not start")
	}
	op := s.progress.operation
	s.HandleKey("enter", tea.KeyPressMsg{})
	m.Update(msg)
	if s.Init() != nil || s.progress.operation != op || s.request != request {
		t.Fatal("duplicate action changed the submitted operation")
	}
}

func TestP2PObservationRetryReplacementAndClose(t *testing.T) {
	w := app.NewHelperWorkflows("2.1.1")
	defer w.Close()
	ctx := &ScreenContext{Cfg: config.Default(), HelperWorkflows: w}
	s := NewP2PUpgradeScreen(ctx)
	s.Init()
	first := s.addressRead
	s.HandleMsg(p2pAddressResultMsg{read: first, err: errors.New("no route")})
	if s.step != p2pNoIP || s.request.Address() != "" {
		t.Fatal("failed read retained an actionable address")
	}
	if _, cmd := s.HandleKey("enter", tea.KeyPressMsg{}); cmd == nil {
		t.Fatal("retry unavailable")
	}
	second := s.addressRead
	s.HandleMsg(p2pAddressResultMsg{read: first, request: reviewedP2PRequest(t)})
	if s.step != p2pLoading || s.addressRead != second {
		t.Fatal("older attempt consumed the retry")
	}
	canceled := false
	cancel := second.cancel
	second.cancel = func() { canceled = true; cancel() }
	m := Model{nav: NewNavSidebar(), screenCtx: ctx, tabs: []openTab{
		{Kind: tabP2PUpgrade, Section: secSystem, Screen: s},
	}}
	m.nav.ActiveItem = secSystem
	replacement := NewP2PUpgradeScreen(ctx)
	m.setTabScreen(1, replacement)
	m.Update(p2pAddressResultMsg{read: second, request: reviewedP2PRequest(t)})
	if !canceled || s.addressRead != nil || replacement.request.Address() != "" {
		t.Fatal("replacement retained or accepted an obsolete read")
	}
	replacement.Init()
	read := replacement.addressRead
	m.nav.ActiveItem = secWallet
	updated, _ := m.closeScreenTab(replacement)
	m = updated.(Model)
	m.Update(p2pAddressResultMsg{read: read, request: reviewedP2PRequest(t)})
	if replacement.addressRead != nil || len(m.tabs) != 0 || replacement.request.Address() != "" {
		t.Fatal("closed screen retained or published its read")
	}
}
