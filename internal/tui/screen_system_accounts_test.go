package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/virtpriv/node/internal/accountaccess"
	"github.com/virtpriv/node/internal/app"
	"github.com/virtpriv/node/internal/sshkeys"
	"github.com/virtpriv/node/internal/theme"
)

type accountScreenAccess struct {
	accounts []accountaccess.Account
	detail   app.AccountDetails
	imported []app.AccountKeyImport
}

func (a *accountScreenAccess) List() (accountaccess.Inventory, error) {
	return accountaccess.Inventory{Accounts: a.accounts}, nil
}
func (a *accountScreenAccess) Inspect(accountaccess.Ref) (app.AccountDetails, error) {
	return a.detail, nil
}
func (a *accountScreenAccess) Import(r app.AccountKeyImport) error {
	a.imported = append(a.imported, r)
	return nil
}
func (*accountScreenAccess) Close() {}

func TestAccountImportFreezesReviewAndRoutesHiddenCompletion(t *testing.T) {
	theme.Init(true)
	m, _ := statusModelFixture(t)
	key, err := sshkeys.Parse(screenKeyA + " laptop")
	if err != nil {
		t.Fatal(err)
	}
	a := accountaccess.Account{Name: "deploy", UID: 1000, Home: "/home/deploy", Shell: "/bin/bash"}
	access := &accountScreenAccess{
		accounts: []accountaccess.Account{{Name: "daemon", UID: 1, Shell: "/usr/sbin/nologin"}, a},
		detail:   app.AccountDetails{Detail: accountaccess.Detail{Account: a, Source: sshkeys.Source{User: "deploy", Keys: []sshkeys.Key{key}}}, Authorized: map[string]bool{}},
	}
	m.screenCtx.AccountAccess = access
	s := NewAccountsScreen(m.screenCtx)
	m.nav.ActiveItem, m.nav.Cursor = secSystem, secSystem
	m.tabs = []openTab{{Kind: tabAccounts, Section: secSystem, Screen: s}}
	m.activeTab = 1
	m.focusContent()
	press := func(code rune) tea.Cmd { return statusUpdate(&m, tea.KeyPressMsg{Code: code}) }
	run := func(cmd tea.Cmd) tea.Msg {
		t.Helper()
		if cmd == nil {
			t.Fatal("expected account observation")
		}
		msg := cmd()
		statusUpdate(&m, msg)
		return msg
	}
	run(s.Init())
	press(tea.KeyDown)
	oldRead := run(press(tea.KeyEnter))
	press(tea.KeyDown)
	refresh := press('r')
	if refresh == nil {
		t.Fatal("refresh not admitted")
	}
	// A replay cannot finish the newer pending read or admit review before it completes.
	statusUpdate(&m, oldRead)
	if press(tea.KeyEnter) != nil || s.review != nil {
		t.Fatal("review admitted during pending refresh")
	}
	run(refresh)
	press(tea.KeyEnter)
	if s.review == nil || !strings.Contains(s.View(67, 25), key.Fingerprint) {
		t.Fatal("missing key review")
	}
	// The source can change after observation; navigation and refresh must preserve
	// what the user reviewed. Import's application boundary performs the fresh recheck.
	access.detail.Account.Home = "/srv/reassigned"
	access.detail.Source.Keys = nil
	if press('r') != nil || statusUpdate(&m, openAccountsCmd(m.screenCtx)()) != nil {
		t.Fatal("review admitted a new observation")
	}
	statusUpdate(&m, oldRead)
	// Confirmation defaults to Go Back; merely pressing Enter cannot import.
	if press(tea.KeyEnter) != nil || len(access.imported) != 0 || s.review != nil {
		t.Fatal("default confirmation did not cancel without importing")
	}
	press(tea.KeyEnter)
	press(tea.KeyRight)
	submit := press(tea.KeyEnter)
	if submit == nil {
		t.Fatal("import not admitted")
	}
	if press(tea.KeyEnter) != nil {
		t.Fatal("duplicate submit")
	}
	statusUpdate(&m, closeTabMsg{})
	if len(m.tabs) != 1 || m.tabs[0].Screen != s {
		t.Fatal("pending import tab was closed")
	}
	run(press(tea.KeyLeft))
	press(tea.KeyUp)
	press(tea.KeyEnter)
	if m.nav.ActiveSection() == secSystem {
		t.Fatal("Accounts did not hide")
	}
	statusUpdate(&m, accountImportMsg{owner: s, attempt: s.attempt - 1})
	if s.resultReady {
		t.Fatal("stale completion became a result")
	}
	statusUpdate(&m, submit())
	if !s.resultReady || s.resultErr != nil || s.working || len(access.imported) != 1 ||
		access.imported[0] != (app.AccountKeyImport{Account: a, Key: key}) {
		t.Fatal("hidden completion lost or reviewed account/key changed")
	}
}

func TestAccountScreenRejectsOldReadsAndExplainsUnknownState(t *testing.T) {
	theme.Init(true)
	ctx, _ := sshScreenContext(t)
	ctx.AccountAccess = &accountScreenAccess{}
	s := NewAccountsScreen(ctx)
	s.refresh()
	s.HandleMsg(accountsMsg{owner: s, request: s.request - 1, inventory: accountaccess.Inventory{Accounts: []accountaccess.Account{{Name: "wrong"}}}})
	if len(s.accounts) != 0 {
		t.Fatal("stale list accepted")
	}
	s.HandleMsg(accountsMsg{owner: NewAccountsScreen(ctx), request: s.request, inventory: accountaccess.Inventory{Accounts: []accountaccess.Account{{Name: "wrong"}}}})
	if len(s.accounts) != 0 {
		t.Fatal("foreign list accepted")
	}
	s.HandleMsg(accountsMsg{owner: s, request: s.request, err: errors.New("helper unavailable")})
	view := s.View(67, 25)
	if !strings.Contains(view, "helper unavailable") || strings.Contains(view, "No local accounts") {
		t.Fatal("failure rendered as empty inventory")
	}
}

// Exercise actual arrow handling and viewport rendering: optional information
// must remain reachable after a key list that is taller than the terminal.
func TestAccountKeyNavigationReachesExpandedInformation(t *testing.T) {
	theme.Init(true)
	ctx, _ := sshScreenContext(t)
	a := accountaccess.Account{Name: "test", UID: 1000, Home: "/home/test", Shell: "/bin/bash"}
	detail := &app.AccountDetails{Detail: accountaccess.Detail{Account: a, Source: sshkeys.Source{Path: "/home/test/.ssh/authorized_keys"}}}
	owner := NewSSHKeysScreen(ctx)
	owner.loaded, owner.information, owner.detail = true, true, detail
	for i := range 30 {
		key := sshkeys.Key{Comment: fmt.Sprintf("key-%02d", i), Type: "ssh-ed25519", Fingerprint: fmt.Sprint(i)}
		detail.Source.Keys = append(detail.Source.Keys, key)
		owner.keys = append(owner.keys, app.SSHKey{Comment: key.Comment, Type: key.Type, Fingerprint: key.Fingerprint})
	}
	source := NewAccountsScreen(ctx)
	source.account, source.detail, source.information = &a, detail, true
	for _, screen := range []Screen{source, owner} {
		// First Down enters the table, subsequent presses visit each row.
		for i := range 30 {
			screen.HandleKey("down", tea.KeyPressMsg{Code: tea.KeyDown})
			if !strings.Contains(screen.View(67, 15), fmt.Sprintf("key-%02d", i)) {
				t.Fatalf("%T: selected key %d is hidden", screen, i)
			}
		}
		reached := false
		for range 40 {
			screen.HandleKey("down", tea.KeyPressMsg{Code: tea.KeyDown})
			if strings.Contains(screen.View(67, 15), "authorized_keys") {
				reached = true
				break
			}
		}
		if !reached {
			t.Fatalf("%T: expanded information unreachable with arrows", screen)
		}
		for range 80 {
			screen.HandleKey("up", tea.KeyPressMsg{Code: tea.KeyUp})
			screen.View(67, 15)
		}
		screen.HandleKey("down", tea.KeyPressMsg{Code: tea.KeyDown})
		if !strings.Contains(screen.View(67, 15), "key-00") {
			t.Fatalf("%T: could not return to first key", screen)
		}
	}
}
