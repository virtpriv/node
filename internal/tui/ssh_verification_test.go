package tui

import (
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/virtpriv/node/internal/app"
)

type loginVerifierStub struct {
	calls int
	next  app.SSHVerification
}

func (r *loginVerifierStub) Verify() app.SSHVerification { r.calls++; return r.next }
func (*loginVerifierStub) Close()                        {}

func TestSSHVerificationTimerOrderingAndMountedView(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, _ := statusModelFixture(t)
		reader := &loginVerifierStub{next: app.SSHVerification{Pending: true, Address: "203.0.113.7"}}
		m.screenCtx.SSHVerification = reader
		m.nav.SetActive(secSystem)
		m.width, m.height = 110, 42
		system := NewSystemHomeScreen(m.screenCtx)
		m.sectionScreens[secSystem] = system
		read := statusUpdate(&m, requestSSHVerificationCmd())
		if read == nil || reader.calls != 0 {
			t.Fatal("refresh did not admit deferred verification")
		}
		// Hold a real status request outstanding. Verification must still refresh;
		// executing this timer branch emits messages without external helper I/O.
		statusUpdate(&m, requestStatusCmd())
		for range 3 {
			triggers := 0
			for _, cmd := range statusUpdate(&m, tickMsg(time.Now()))().(tea.BatchMsg) {
				switch msg := cmd().(type) {
				case refreshSSHVerificationMsg:
					triggers++
					if statusUpdate(&m, msg) != nil {
						t.Fatal("timer admitted overlapping verification")
					}
				case refreshStatusMsg, tickMsg:
				default:
					t.Fatalf("unexpected System timer result %T", msg)
				}
			}
			if triggers != 1 {
				t.Fatalf("timer verification triggers=%d, want one", triggers)
			}
		}
		old := read()
		followup := statusUpdate(&m, old)
		view := func() string { return ansi.Strip(m.View().Content) }
		if !strings.Contains(view(), "Key verification pending") || !strings.Contains(view(), "ssh vpn@203.0.113.7") {
			t.Fatalf("observed pending state not rendered: %s", view())
		}
		if followup == nil || reader.calls != 1 {
			t.Fatal("overlap did not coalesce into one follow-up")
		}
		if statusUpdate(&m, old) != nil {
			t.Fatal("replayed completion changed admission")
		}
		reader.next = app.SSHVerification{}
		if statusUpdate(&m, followup()) != nil || reader.calls != 2 {
			t.Fatal("overlap became an unbounded queue")
		}
		statusUpdate(&m, old)
		if strings.Contains(view(), "Key verification") || !m.state.KeyVerificationKnown || m.state.KeyVerificationPending {
			t.Fatal("late pending reply restored warning after verification cleared it")
		}
		if m.sectionScreens[secSystem] != system {
			t.Fatal("publication replaced already-open screen")
		}
		// A terminal-scoped request cannot publish in a successor terminal.
		other, _ := statusModelFixture(t)
		statusUpdate(&other, old)
		if other.state.KeyVerificationKnown {
			t.Fatal("foreign terminal result published")
		}

		publish := func(result app.SSHVerification) {
			t.Helper()
			reader.next = result
			cmd := statusUpdate(&m, requestSSHVerificationCmd())
			if cmd == nil {
				t.Fatal("next observation not admitted")
			}
			statusUpdate(&m, cmd())
		}
		publish(app.SSHVerification{Pending: true, Address: "203.0.113.7"})
		if !strings.Contains(view(), "ssh vpn@203.0.113.7") {
			t.Fatal("pending address was not published")
		}
		publish(app.SSHVerification{Pending: true})
		if !strings.Contains(view(), "ssh vpn@<server-ip>") || strings.Contains(view(), "203.0.113.7") {
			t.Fatal("address failure reused stale address or hid verification")
		}
		publish(app.SSHVerification{Err: errors.New("journal unavailable")})
		if !strings.Contains(view(), "Key verification status unavailable") || strings.Contains(view(), "Key verification pending") || !m.state.WalletKnown || !m.state.WalletExists {
			t.Fatal("unknown evidence disguised as pending or invalidated wallet")
		}
		publish(app.SSHVerification{})
		if strings.Contains(view(), "Key verification") {
			t.Fatal("warning did not recover in place")
		}
	})
}
