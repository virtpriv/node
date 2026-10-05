package tui

import (
	"encoding/base64"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/lndrpc"
	"github.com/virtpriv/node/internal/theme"
)

type connectionReaderFixture struct {
	info  app.ConnectionInfo
	calls int
}

func (r *connectionReaderFixture) Read(target app.ConnectionTarget) app.ConnectionInfo {
	r.calls++
	result := r.info
	result.Target = target
	return result
}
func (*connectionReaderFixture) Close() {}

// Open through the production parents and Model, including their Init command.
func connectionModel(t *testing.T, web bool) (Model, Screen, *connectionReaderFixture, tea.Cmd) {
	t.Helper()
	theme.Init(true)
	cfg := config.Default()
	cfg.P2PMode, cfg.SyncthingEnabled = "hybrid", true
	state := &RuntimeState{WalletKnown: true, WalletExists: true}
	r := &connectionReaderFixture{info: app.ConnectionInfo{Address: "original.onion", Credential: "synthetic-secret"}}
	ctx := &ScreenContext{Cfg: cfg, State: state, Status: &app.StatusSnapshot{Node: freshStatus(lndrpc.NodeInfo{}), PublicIP: freshStatus("203.0.113.1")}, ConnectionInfo: r, ContentFocused: true}
	m := Model{cfg: cfg, state: state, screenCtx: ctx, nav: NewNavSidebar(), width: 100, height: 40}
	var open tea.Cmd
	if web {
		m.nav.SetActive(secAddons)
		parent := NewSyncthingDetailScreen(ctx)
		parent.btnIdx = 1
		_, open = parent.HandleKey("enter", tea.KeyPressMsg{})
	} else {
		m.nav.SetActive(secWallet)
		_, open = NewWalletHomeScreen(ctx).openPairing()
	}
	if open == nil {
		t.Fatal("parent did not open connection display")
	}
	init := statusUpdate(&m, open())
	if len(m.tabs) != 1 || init == nil {
		t.Fatal("connection display not initialized")
	}
	return m, m.tabs[0].Screen, r, init
}

func TestConnectionInfoHiddenRefreshCoalescesAndRejectsOldResults(t *testing.T) {
	for _, web := range []bool{false, true} {
		name := "Zeus"
		if web {
			name = "Web UI"
		}
		t.Run(name, func(t *testing.T) {
			m, s, r, init := connectionModel(t, web)
			if _, cmd := s.HandleKey("enter", tea.KeyPressMsg{}); cmd != nil {
				t.Fatal("pending screen admitted action")
			}
			first := init()
			for range 3 {
				if _, cmd := s.HandleMsg(tabActivatedMsg{}); cmd != nil {
					t.Fatal("overlapping activation started another read")
				}
			}
			// A completion must reach its mounted owner while another section is visible.
			m.nav.SetActive(secSystem)
			next := statusUpdate(&m, first)
			if next == nil || connectionState(s).loaded {
				t.Fatal("overlapping triggers did not schedule one fresh read")
			}
			if cmd := statusUpdate(&m, first); cmd != nil || connectionState(s).loaded {
				t.Fatal("old completion consumed new request")
			}
			r.info.Address = "current.onion"
			current := next()
			if cmd := statusUpdate(&m, current); cmd != nil {
				t.Fatal("refresh loop after coalesced read")
			}
			if r.calls != 2 || !strings.Contains(s.View(82, 34), "current.onion") {
				t.Fatal("hidden owner did not publish fresh data")
			}
			duplicate := current.(connectionInfoResultMsg)
			duplicate.info = app.ConnectionInfo{AddressErr: errors.New("late failure")}
			statusUpdate(&m, duplicate)
			if !strings.Contains(s.View(82, 34), "current.onion") {
				t.Fatal("duplicate replaced published observation")
			}
			if web {
				m.nav.SetActive(secAddons)
			} else {
				m.nav.SetActive(secWallet)
			}
			_, oldAction := s.HandleKey("enter", tea.KeyPressMsg{})
			// Model deduplicates a repeated open request and refreshes its owner.
			refresh := statusUpdate(&m, openTabMsg{Kind: m.tabs[0].Kind, Screen: s})
			if refresh == nil || len(m.tabs) != 1 {
				t.Fatal("returning to open display did not refresh same owner")
			}
			r.info.Credential = "new-secret"
			statusUpdate(&m, refresh())
			// The host and screen are unchanged: only request identity can
			// reject this delayed action after a successful refresh.
			statusUpdate(&m, oldAction())
			if m.subview != svNone {
				t.Fatal("old action survived a newer successful read")
			}
			_, action := s.HandleKey("enter", tea.KeyPressMsg{})
			statusUpdate(&m, action())
			if web {
				if m.subview != svFullURL || m.urlTarget != "http://current.onion:8384" {
					t.Fatal("refreshed Web UI action lost its endpoint")
				}
				updated, _ := m.handleGenericSubviewKey("enter")
				m = updated.(Model)
				s.HandleKey("right", tea.KeyPressMsg{})
				s.HandleKey("enter", tea.KeyPressMsg{})
				if !strings.Contains(s.View(67, 24), "new-secret") {
					t.Fatal("reactivation retained previous password")
				}
			} else {
				uri, err := url.Parse(m.urlTarget)
				if err != nil {
					t.Fatal(err)
				}
				credential, err := base64.RawURLEncoding.DecodeString(uri.Query().Get("macaroon"))
				if m.subview != svQR || err != nil || string(credential) != "new-secret" {
					t.Fatal("QR did not use refreshed credential")
				}
			}
			if r.calls != 3 {
				t.Fatal("reactivation or display performed extra reads")
			}
		})
	}
}

func TestConnectionInfoCloseReopenOwnsResultsAndActions(t *testing.T) {
	for _, tc := range []struct {
		name               string
		web, readCompleted bool
	}{
		{"Zeus pending read", false, false}, {"Web UI pending read", true, false},
		{"Zeus delayed action", false, true}, {"Web UI delayed action", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, s, r, init := connectionModel(t, tc.web)
			late := init()
			if tc.readCompleted {
				statusUpdate(&m, late)
				_, action := s.HandleKey("enter", tea.KeyPressMsg{})
				if action == nil {
					t.Fatal("usable connection offered no display")
				}
				late = action()
			} else {
				// A queued reactivation must not start another read after close.
				s.HandleMsg(tabActivatedMsg{})
			}
			updated, _ := m.closeTab(1)
			m = updated.(Model)
			var replacement Screen = NewPairingScreen(m.screenCtx)
			kind := tabPairing
			if tc.web {
				replacement = NewSyncthingWebUIScreen(m.screenCtx)
				kind = tabSyncthingWebUI
			}
			fresh := statusUpdate(&m, openTabMsg{Kind: kind, Screen: replacement})
			// The replacement occupies the same visible slot. The old action's
			// request is still current for its retired owner, isolating owner identity.
			if cmd := statusUpdate(&m, late); cmd != nil {
				t.Fatal("retired owner scheduled more work")
			}
			if connectionState(replacement).loaded || m.subview != svNone {
				t.Fatal("retired owner published a result or opened an overlay")
			}
			r.info.Address = "replacement.onion"
			statusUpdate(&m, fresh())
			_, currentAction := replacement.HandleKey("enter", tea.KeyPressMsg{})
			statusUpdate(&m, currentAction())
			if !strings.Contains(m.urlTarget, "replacement.onion") {
				t.Fatal("new owner could not display its result")
			}
			// Already-open overlays must stop displaying data when their source scope changes.
			if tc.web {
				m.cfg.SyncthingEnabled = false
			} else {
				m.screenCtx.walletGeneration++
			}
			view := m.viewQR()
			if tc.web {
				view = m.viewFullURL()
			}
			if !strings.Contains(view, "unavailable") || strings.Contains(view, "replacement.onion") {
				t.Fatal("open overlay retained invalid connection information")
			}
			updated, _ = m.handleGenericSubviewKey("enter")
			m = updated.(Model)
			if m.subview != svNone || m.urlTarget != "" {
				t.Fatal("could not dismiss invalid overlay")
			}
		})
	}
}

func TestConnectionInfoScopeChangesAndCredentialFailureRequireFreshRead(t *testing.T) {
	m, s, r, init := connectionModel(t, false)
	first := init()
	m.cfg.P2PMode = "tor"
	follow := statusUpdate(&m, first)
	if follow == nil || connectionState(s).loaded {
		t.Fatal("old mode published as current")
	}
	statusUpdate(&m, follow())
	_, oldAction := s.HandleKey("enter", tea.KeyPressMsg{})
	// Complete a routine observation while the pairing action is queued.
	statusUpdate(&m, walletObservationMsg(t, &m, true, nil))
	statusUpdate(&m, oldAction())
	if m.subview != svQR {
		t.Fatal("routine observation invalidated current pairing")
	}
	updated, _ := m.handleGenericSubviewKey("enter")
	m = updated.(Model)
	_, refresh := s.HandleMsg(tabActivatedMsg{})
	r.info.Credential = ""
	r.info.CredentialErr = errors.New("missing file")
	statusUpdate(&m, refresh())
	statusUpdate(&m, oldAction())
	view := s.View(67, 24)
	if m.subview != svNone || !strings.Contains(view, "Retry") || !strings.Contains(view, "unavailable") || strings.Contains(view, "Copyable Macaroon") {
		t.Fatal("credential failure retained old action or hid recovery")
	}
	_, retry := s.HandleKey("enter", tea.KeyPressMsg{})
	if retry == nil {
		t.Fatal("credential failure cannot be retried")
	}
	r.info.Credential, r.info.CredentialErr = "recovered", nil
	statusUpdate(&m, retry())
	_, action := s.HandleKey("enter", tea.KeyPressMsg{})
	m.screenCtx.Status.Node.Err = errors.New("disconnected")
	statusUpdate(&m, action())
	if m.subview != svNone {
		t.Fatal("delayed action ignored unavailable node")
	}
}

func TestConnectionInfoWebPartialFailureAndDisplaySizes(t *testing.T) {
	m, s, r, init := connectionModel(t, true)
	password := "synthetic-password-visible-only-on-request"
	onion := strings.Repeat("a", 56) + ".onion"
	r.info.Address, r.info.Credential = onion, app.ConnectionCredential(password)
	statusUpdate(&m, init())
	for _, size := range [][2]int{{67, 24}, {82, 34}} {
		view := ansi.Strip(s.View(size[0], size[1]))
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatal("connection display exceeds supported pane width")
			}
		}
		if len(strings.Split(view, "\n")) > size[1] {
			t.Fatal("connection display exceeds supported pane height")
		}
		if strings.Contains(view, password) || !strings.Contains(strings.Join(strings.Fields(view), ""), "http://"+onion+":8384") {
			t.Fatal("hidden password leaked or public URL hidden")
		}
	}
	s.HandleKey("right", tea.KeyPressMsg{})
	s.HandleKey("enter", tea.KeyPressMsg{})
	if !strings.Contains(s.View(67, 24), password) {
		t.Fatal("password toggle did not reveal staged password")
	}
	_, refresh := s.HandleMsg(tabActivatedMsg{})
	r.info.Address, r.info.AddressErr = "", errors.New("Tor unavailable")
	statusUpdate(&m, refresh())
	if strings.Contains(s.View(67, 24), password) {
		t.Fatal("reactivation retained password reveal")
	}
	s.HandleKey("enter", tea.KeyPressMsg{})
	if !strings.Contains(s.View(67, 24), password) || !strings.Contains(s.View(67, 24), "Retry") {
		t.Fatal("onion failure discarded independent password or recovery")
	}
}

func TestConnectionMacaroonTerminalFailureOwnership(t *testing.T) {
	m, s, _, init := connectionModel(t, false)
	statusUpdate(&m, init())
	request := connectionState(s).request
	statusUpdate(&m, connectionDisplayDoneMsg{request: request, err: io.EOF})
	if !strings.Contains(s.View(82, 34), "did not complete") {
		t.Fatal("terminal failure invisible")
	}
	_, refresh := s.HandleMsg(tabActivatedMsg{})
	statusUpdate(&m, refresh())
	statusUpdate(&m, connectionDisplayDoneMsg{request: request, err: io.EOF})
	if strings.Contains(s.View(82, 34), "did not complete") {
		t.Fatal("old terminal failure affected new observation")
	}
}

func TestConnectionInfoActionsFollowVisibleOwnerAndEndpoint(t *testing.T) {
	for _, index := range []int{0, 1, 2} {
		m, s, _, init := connectionModel(t, false)
		statusUpdate(&m, init())
		for range index {
			s.HandleKey("right", tea.KeyPressMsg{})
		}
		_, action := s.HandleKey("enter", tea.KeyPressMsg{})
		if action == nil {
			t.Fatal("usable action not offered")
		}
		msg := action()
		m.nav.SetActive(secSystem)
		if cmd := statusUpdate(&m, msg); cmd != nil || m.subview != svNone {
			t.Fatal("delayed action interrupted another section")
		}
		m.nav.SetActive(secWallet)
		cmd := statusUpdate(&m, msg)
		if index == 2 {
			if cmd == nil {
				t.Fatal("explicit macaroon display was not handed to terminal")
			}
		} else if m.subview != svQR {
			t.Fatal("current QR not displayed")
		}
		if index == 1 {
			if !strings.Contains(m.urlTarget, "203.0.113.1:8080") {
				t.Fatal("clearnet QR uses wrong address")
			}
			m.screenCtx.Status.PublicIP = freshStatus("203.0.113.2")
			if !strings.Contains(m.viewQR(), "unavailable") {
				t.Fatal("open QR retained previous public IP")
			}
			updated, _ := m.handleGenericSubviewKey("enter")
			m = updated.(Model)
			statusUpdate(&m, msg)
			if m.subview != svNone {
				t.Fatal("delayed QR accepted previous public IP")
			}
		}
		updated, _ := m.handleGenericSubviewKey("enter")
		m = updated.(Model)
		m.screenCtx.Status.PublicIP = freshStatus("203.0.113.1")
		m.screenCtx.walletGeneration++
		if cmd := statusUpdate(&m, msg); cmd != nil || m.subview != svNone {
			t.Fatal("retired wallet admitted credential display")
		}
	}
}
