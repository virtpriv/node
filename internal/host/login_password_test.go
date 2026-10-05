package host

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/virtpriv/node/internal/loginpassword"
)

const passwordFixture = "test password: with spaces"

// This child replaces chpasswd and never touches an account database.
func TestLoginPasswordProcess(t *testing.T) {
	mode := os.Getenv("VPN_LOGIN_PASSWORD_TEST")
	if mode == "" {
		return
	}
	if mode == "block" {
		time.Sleep(5 * time.Minute)
		os.Exit(3)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil || string(input) != "vpn:"+passwordFixture+"\n" {
		os.Exit(4)
	}
	if mode == "fail" {
		os.Stderr.Write(input)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestLoginPasswordExecution(t *testing.T) {
	password, _ := loginpassword.New(passwordFixture)
	for _, mode := range []string{"success", "fail", "block"} {
		t.Run(mode, func(t *testing.T) {
			var cmd *exec.Cmd
			timeout := 5 * time.Second
			if mode == "block" {
				timeout = 200 * time.Millisecond
			}
			err := runLoginPassword(password, timeout, func(ctx context.Context) *exec.Cmd {
				cmd = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLoginPasswordProcess$")
				cmd.Env = append(os.Environ(), "VPN_LOGIN_PASSWORD_TEST="+mode)
				return cmd
			})
			if (err == nil) != (mode == "success") {
				t.Fatalf("unexpected result: %v", err)
			}
			if mode == "block" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout not reported: %v", err)
			}
			if cmd.ProcessState == nil {
				t.Fatal("child was not reaped")
			}
			if strings.Contains(strings.Join(cmd.Args, " "), passwordFixture) || (err != nil && strings.Contains(err.Error(), passwordFixture)) {
				t.Fatal("secret escaped through argv or diagnostics")
			}
		})
	}
}

func TestLoginPasswordRejectsZeroBeforeExecution(t *testing.T) {
	err := runLoginPassword(loginpassword.Password{}, time.Second, func(context.Context) *exec.Cmd {
		t.Fatal("invalid password reached process execution")
		return nil
	})
	if err == nil {
		t.Fatal("accepted zero password")
	}
}
