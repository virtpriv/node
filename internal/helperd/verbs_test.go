// internal/helperd/verbs_test.go

package helperd

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/virtualprivatenode/vpn/internal/autounlock"
	"github.com/virtualprivatenode/vpn/internal/config"
	"github.com/virtualprivatenode/vpn/internal/helper"
	"github.com/virtualprivatenode/vpn/internal/servicecontrol"
)

// Every verb has a socket deadline. Execution bounds must also be enforced by
// the operation; a connection deadline alone cannot stop privileged work.
func TestEveryVerbHasDeadline(t *testing.T) {
	if len(verbs) == 0 {
		t.Fatal("empty verb menu")
	}
	for name, def := range verbs {
		if def.deadline <= 0 {
			t.Errorf("%s: no deadline", name)
		}
		if def.handler == nil {
			t.Errorf("%s: no handler", name)
		}
		if def.deadline > time.Hour {
			t.Errorf("%s: deadline %v is implausibly long",
				name, def.deadline)
		}
	}
}

func TestServiceActionDeadlineCoversLNDStopAndNotifiedStart(t *testing.T) {
	// The installed LND unit permits 300 seconds to stop and 1200 seconds
	// to notify readiness during an ordinary restart. The helper socket must
	// remain available for that complete systemd transaction.
	if got := verbs[helper.VerbServiceAction].deadline; got < 25*time.Minute {
		t.Fatalf("service-action deadline = %s, want at least 25m", got)
	}
}

// The menu is closed: exactly the ruled verbs, nothing else.
// Adding a verb must be a deliberate act that updates this
// list too.
func TestVerbMenuIsExactlyTheRuledSet(t *testing.T) {
	want := []string{
		helper.VerbReadAccounts,
		helper.VerbReadAccount,
		helper.VerbServiceAction,
		helper.VerbReboot,
		helper.VerbDirSize,
		helper.VerbStageWalletPassword,
		helper.VerbRemoveWalletPassword,
		helper.VerbStageLNDCredentials,
		helper.VerbStageLNDMacaroon,
		helper.VerbRebuildSSHConfig,
		helper.VerbPackageUpdate,
		helper.VerbSelfUpdate,
		helper.VerbUpgradeP2PToHybrid,
		helper.VerbSyncthingInstall,
		// Live-read verbs have no mutation. Account detail accepts an identity. The verification
		// verb also has no parameters but may clear its private marker. They
		// serve the live-read display facts (onion addresses,
		// the Syncthing device ID, the SSH password-auth
		// answer), which keep no board copy.
		helper.VerbReadNodeAddresses,
		helper.VerbReadSSHAuth,
		helper.VerbReadWalletState,
		helper.VerbReadKeyVerificationState,
		helper.VerbVerifyAdminLogin,
	}
	if len(verbs) != len(want) {
		t.Errorf("verb menu has %d entries, want %d",
			len(verbs), len(want))
	}
	for _, v := range want {
		if _, ok := verbs[v]; !ok {
			t.Errorf("menu is missing %s", v)
		}
	}
}

func withConfigVerbTestDeps(t *testing.T) {
	t.Helper()
	oldLoad := loadSystemConfig
	oldSetup := setupAutoUnlock
	oldDisable := disableAutoUnlock
	oldWallet := walletExists
	oldKeyPending := keyVerificationPending
	oldVerifyLogin := verifyAdminLogin
	oldRestage := restageFacts
	t.Cleanup(func() {
		loadSystemConfig = oldLoad
		setupAutoUnlock = oldSetup
		disableAutoUnlock = oldDisable
		walletExists = oldWallet
		keyVerificationPending = oldKeyPending
		verifyAdminLogin = oldVerifyLogin
		restageFacts = oldRestage
	})
}

func TestAutoUnlockVerbsReturnStructuredTransitionResults(t *testing.T) {
	withConfigVerbTestDeps(t)
	wantEnable := autounlock.Result{
		Outcome: autounlock.VerificationFailed,
	}
	setupAutoUnlock = func(password autounlock.Password) (autounlock.Result, error) {
		if password.Text() != "correct horse" {
			t.Fatalf("password = %q", password)
		}
		return wantEnable, nil
	}
	wantDisable := autounlock.Result{
		Outcome: autounlock.StillEnabled,
	}
	disableAutoUnlock = func() (autounlock.Result, error) {
		return wantDisable, nil
	}

	got, err := verbStageWalletPassword(&verbCtx{}, raw(t,
		helper.StageWalletPasswordParams{Password: "correct horse"}))
	if err != nil || got != wantEnable {
		t.Fatalf("enable result = %#v, %v", got, err)
	}
	got, err = verbRemoveWalletPassword(&verbCtx{}, nil)
	if err != nil || got != wantDisable {
		t.Fatalf("disable result = %#v, %v", got, err)
	}
}

func TestWalletPasswordRefusedBeforeHostDispatch(t *testing.T) {
	withConfigVerbTestDeps(t)
	setupAutoUnlock = func(autounlock.Password) (autounlock.Result, error) {
		t.Fatal("invalid wallet password reached privileged operation")
		return autounlock.Result{}, nil
	}
	for _, password := range []string{"", strings.Repeat("x", 513), "a\nb", "a\rb"} {
		if _, err := verbStageWalletPassword(&verbCtx{}, raw(t, helper.StageWalletPasswordParams{Password: password})); err == nil {
			t.Fatal("invalid wallet password accepted by helper")
		}
	}
}

func TestWalletAndVerificationReadsFailIndependently(t *testing.T) {
	withConfigVerbTestDeps(t)
	cfg := config.Default()
	cfg.Network = "testnet4"
	loadSystemConfig = func() (*config.AppConfig, error) { return cfg, nil }
	walletExists = func(network string) (bool, error) {
		if network != "testnet4" {
			t.Fatalf("wallet observed for %q", network)
		}
		return true, nil
	}
	keyVerificationPending = func() (bool, error) {
		return false, errors.New("marker unreadable")
	}
	result, err := verbReadWalletState(&verbCtx{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := result.(helper.WalletStateResult)
	if !got.WalletExists {
		t.Fatalf("live wallet state = %+v", got)
	}
	walletErr := errors.New("LND state RPC unavailable")
	walletExists = func(string) (bool, error) {
		return false, walletErr
	}
	if _, err := verbReadWalletState(&verbCtx{}, nil); !errors.Is(err, walletErr) {
		t.Fatalf("unknown wallet fact was not preserved: %v", err)
	}
	if _, err := verbReadKeyVerificationState(
		&verbCtx{}, nil); err == nil {
		t.Fatal("unreadable verification marker reported a state")
	}

	keyVerificationPending = func() (bool, error) { return true, nil }
	result, err = verbReadKeyVerificationState(&verbCtx{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	verification := result.(helper.KeyVerificationStateResult)
	if !verification.Pending {
		t.Fatalf("verification state = %+v", verification)
	}

}

func TestVerificationRejectsParametersAndPropagatesEvidenceFailure(t *testing.T) {
	withConfigVerbTestDeps(t)
	calls := 0
	failure := errors.New("journal unavailable")
	verifyAdminLogin = func() (bool, bool, error) { calls++; return true, false, failure }
	keyVerificationPending = func() (bool, error) { calls++; return true, nil }
	for _, invoke := range []func(*verbCtx, json.RawMessage) (any, error){verbReadKeyVerificationState, verbVerifyAdminLogin} {
		if _, err := invoke(&verbCtx{}, json.RawMessage(`{"user":"root","path":"/tmp/other"}`)); err == nil {
			t.Fatal("caller-directed verification accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid parameters reached root operation")
	}
	result, err := verbVerifyAdminLogin(&verbCtx{}, nil)
	if !errors.Is(err, failure) || result != nil || calls != 1 {
		t.Fatalf("failed evidence became success: %v %v", result, err)
	}
}

func raw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Invalid IPC input must be refused before privileged execution.
func TestServiceActionValidation(t *testing.T) {
	old := controlNodeService
	t.Cleanup(func() { controlNodeService = old })
	controlNodeService = func(servicecontrol.Request) (servicecontrol.Completion, error) {
		t.Fatal("invalid request reached privileged execution")
		return servicecontrol.Completion{}, nil
	}

	ctx := &verbCtx{}
	cases := []helper.ServiceActionParams{
		{Unit: "sshd", Action: "restart"},     // not a managed unit
		{Unit: "bitcoind", Action: "disable"}, // not an allowed action
		{Unit: "", Action: ""},
		{Unit: "bitcoind; rm -rf /", Action: "start"},
	}
	for _, c := range cases {
		if _, err := verbServiceAction(ctx, raw(t, c)); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
	// Unknown fields refuse too (strict decoding).
	if _, err := verbServiceAction(ctx, json.RawMessage(
		`{"unit":"tor","action":"restart","extra":1}`)); err == nil {
		t.Error("accepted unknown params field")
	}
}

func TestServiceActionStagesCredentialsOnlyAfterLNDStarts(t *testing.T) {
	oldControl, oldRestage := controlNodeService, restageFacts
	t.Cleanup(func() { controlNodeService, restageFacts = oldControl, oldRestage })
	for _, tc := range []struct {
		unit, action         string
		controlErr, stageErr error
		stage                bool
	}{
		{unit: "lnd", action: "start", stage: true},
		{unit: "lnd", action: "restart", stage: true},
		{unit: "lnd", action: "stop"},
		{unit: "tor", action: "restart"},
		{unit: "lnd", action: "restart", controlErr: errors.New("state unavailable")},
		{unit: "lnd", action: "start", stageErr: errors.New("staging failed"), stage: true},
	} {
		controlled, staged := false, false
		state := "active"
		if tc.action == "stop" {
			state = "inactive"
		}
		unit := tc.unit + ".service"
		if tc.unit == "tor" {
			unit = "tor@default.service"
		}
		completion := servicecontrol.Completion{Unit: unit, Action: tc.action, State: state}
		controlNodeService = func(request servicecontrol.Request) (servicecontrol.Completion, error) {
			if request.Service() != tc.unit || request.Action() != tc.action {
				t.Fatal("helper changed request")
			}
			controlled = true
			return completion, tc.controlErr
		}
		restageFacts = func(verb string) error {
			if !controlled || tc.controlErr != nil || verb != helper.VerbServiceAction {
				t.Fatal("staged before verified control")
			}
			staged = true
			return tc.stageErr
		}
		result, err := verbServiceAction(&verbCtx{}, raw(t, helper.ServiceActionParams{Unit: tc.unit, Action: tc.action}))
		if !controlled || staged != tc.stage || (err != nil) != (tc.controlErr != nil || tc.stageErr != nil) {
			t.Fatalf("%s %s: staged %v, err %v", tc.unit, tc.action, staged, err)
		}
		if err == nil {
			if result != completion {
				t.Fatalf("completion changed: got %+v, want %+v", result, completion)
			}
		} else if result != nil {
			t.Fatal("failed operation returned success evidence")
		}
		if tc.stageErr != nil && !strings.Contains(err.Error(), "lnd is active; credential refresh failed") {
			t.Fatalf("partial success hidden: %v", err)
		}
	}
}

func TestDirSizeValidation(t *testing.T) {
	ctx := &verbCtx{}
	for _, which := range []string{
		"bitcoin", "", "/etc", "../lnd", "lnd/..",
	} {
		if _, err := verbDirSize(ctx, raw(t,
			helper.DirSizeParams{Which: which})); err == nil {
			t.Errorf("accepted which=%q", which)
		}
	}
}

// The self-update gate refuses before any network or disk
// activity: bad target shapes, cross-major targets, and a
// non-release running version all stop at the boundary.
func TestSelfUpdateGate(t *testing.T) {
	previous := updateSelf
	t.Cleanup(func() { updateSelf = previous })
	calls := 0
	updateSelf = func(string, func(int)) error {
		calls++
		return errors.New("update execution must not start")
	}
	ctx := &verbCtx{version: "0.7.0"}
	for _, target := range []string{
		"", "dev", "v0.7.1", "0.7", "1.0.0", "2.3.4",
		"0.7.1-rc1", "0.7.1;curl evil",
	} {
		if _, err := verbSelfUpdate(ctx, raw(t,
			helper.SelfUpdateParams{
				Version: target,
			})); err == nil {
			t.Errorf("gate passed target %q", target)
		}
	}
	// A dev build refuses everything (cannot prove same-major).
	dev := &verbCtx{version: "dev"}
	if _, err := verbSelfUpdate(dev, raw(t,
		helper.SelfUpdateParams{Version: "0.7.1"})); err == nil {
		t.Error("dev build accepted a self-update")
	}
	if calls != 0 || ctx.exitAfterEnd || dev.exitAfterEnd {
		t.Fatalf("refusal executed update or retired helper: calls=%d", calls)
	}
}

func TestSelfUpdateRetiresHelperOnlyAfterSuccess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("helper peer credentials require Linux")
	}
	previous := updateSelf
	t.Cleanup(func() { updateSelf = previous })
	failure := errors.New("binary installation failed")
	for _, tc := range []struct {
		name  string
		err   error
		steps []int
	}{
		{"failure", failure, []int{0, 1, 2}},
		{"success", nil, []int{0, 1, 2, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Real IPC and peer credentials without a listening socket or host path.
			fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
			if err != nil {
				t.Fatal(err)
			}
			clientFile := os.NewFile(uintptr(fds[0]), "update-client")
			serverFile := os.NewFile(uintptr(fds[1]), "update-server")
			defer clientFile.Close()
			defer serverFile.Close()
			client, err := net.FileConn(clientFile)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			clientFile.Close()
			if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			serverConn, err := net.FileConn(serverFile)
			if err != nil {
				t.Fatal(err)
			}
			defer serverConn.Close()
			serverFile.Close()
			conn := serverConn.(*net.UnixConn)

			calls, target := 0, ""
			updateSelf = func(version string, progress func(int)) error {
				calls++
				target = version
				for _, index := range tc.steps {
					progress(index)
				}
				return tc.err
			}
			srv := &server{version: "0.7.0", allowed: map[uint32]bool{uint32(os.Getuid()): true}}
			done := make(chan struct{})
			var retire bool
			go func() {
				defer close(done)
				retire = srv.handleConn(conn)
			}()
			defer func() {
				client.Close()
				conn.Close()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("helper connection did not finish")
				}
			}()
			request := helper.Request{Verb: helper.VerbSelfUpdate, Params: raw(t, helper.SelfUpdateParams{Version: "0.7.1"})}
			if err := json.NewEncoder(client).Encode(request); err != nil {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(client)
			for _, index := range tc.steps {
				var event helper.Event
				if err := decoder.Decode(&event); err != nil || event.Event != "step" || event.Index != index {
					t.Fatalf("progress %d: event=%+v error=%v", index, event, err)
				}
			}
			wantError := ""
			if tc.err != nil {
				wantError = tc.err.Error()
			}
			var end helper.Event
			if err := decoder.Decode(&end); err != nil || end.Event != "end" || end.OK != (tc.err == nil) || end.Error != wantError {
				t.Fatalf("terminal outcome: event=%+v error=%v", end, err)
			}
			if err := decoder.Decode(&helper.Event{}); !errors.Is(err, io.EOF) {
				t.Fatalf("expected connection close after one terminal event, got %v", err)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("helper connection did not return its retirement decision")
			}
			if calls != 1 || target != "0.7.1" || retire != (tc.err == nil) {
				t.Fatalf("wrong dispatch or retirement: calls=%d target=%q retire=%v", calls, target, retire)
			}
		})
	}
}

// decode is strict: unknown fields and malformed JSON refuse.
func TestDecodeStrict(t *testing.T) {
	var p helper.RebuildSSHConfigParams
	if err := decode(json.RawMessage(
		`{"password_auth_disabled":true,"bonus":1}`), &p); err == nil {
		t.Error("accepted unknown field")
	}
	if err := decode(json.RawMessage(`{`), &p); err == nil {
		t.Error("accepted malformed JSON")
	}
	if err := decode(json.RawMessage(
		`{"password_auth_disabled":true}`), &p); err != nil {
		t.Errorf("rejected valid params: %v", err)
	}
}

// A real admitted IPC request must reject the removed endpoint before it can
// reach any host operation, even when the replacement password is valid.
func TestRemovedPasswordResetRefusedOverIPC(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("kernel peer credentials require Linux")
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	connections := make([]net.Conn, 0, 2)
	for _, fd := range fds {
		file := os.NewFile(uintptr(fd), "password-refusal")
		conn, err := net.FileConn(file)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
	}
	client, serverConn := connections[0], connections[1].(*net.UnixConn)
	srv := &server{version: "0.7.0", allowed: map[uint32]bool{uint32(os.Getuid()): true}}
	done := make(chan bool, 1)
	go func() { done <- srv.handleConn(serverConn) }()
	request := helper.Request{Verb: "set-user-password", Params: json.RawMessage(`{"user":"vpn","password":"a valid replacement password"}`)}
	if err := json.NewEncoder(client).Encode(request); err != nil {
		t.Fatal(err)
	}
	var event helper.Event
	if err := json.NewDecoder(client).Decode(&event); err != nil || event.OK || event.Event != "end" || !strings.Contains(event.Error, "unknown verb") {
		t.Fatalf("reset not refused: %+v %v", event, err)
	}
	select {
	case retire := <-done:
		if retire {
			t.Fatal("reset request retired helper")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reset request did not finish")
	}
}
