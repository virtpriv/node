package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/virtualprivatenode/vpn/internal/accountaccess"
	"github.com/virtualprivatenode/vpn/internal/helper"
	"github.com/virtualprivatenode/vpn/internal/sshkeys"
)

func TestAccountImportRechecksSourceBeforeOwnerWrite(t *testing.T) {
	for _, failure := range []string{"", "identity", "home", "shell", "group", "removed", "restricted", "unreadable", "helper", "comment"} {
		t.Run(failure, func(t *testing.T) {
			ssh, line, _ := accessFixture(t)
			key, err := sshkeys.Parse(line)
			if err != nil {
				t.Fatal(err)
			}
			account := accountaccess.Account{Name: "deploy", UID: 1000, GID: 1000, Home: "/home/deploy", Shell: "/bin/bash"}
			detail := accountaccess.Detail{Account: account, Source: sshkeys.Source{User: "deploy", Keys: []sshkeys.Key{key}}}
			r := NewAccountAccess(ssh)
			defer r.Close()
			r.call = func(_ context.Context, verb string, params, result any) error {
				if verb != helper.VerbReadAccount {
					t.Fatalf("verb: %s", verb)
				}
				switch failure {
				case "identity":
					detail.Account.UID++
				case "home":
					detail.Account.Home = "/srv/reassigned"
				case "shell":
					detail.Account.Shell = "/bin/zsh"
				case "group":
					detail.Account.GID++
				case "removed":
					detail.Source.Keys = nil
				case "restricted":
					detail.Source.Keys = nil
					detail.Source.Excluded = 1
				case "unreadable":
					detail.Source.Problem = "unreadable"
				case "helper":
					return errors.New("offline")
				case "comment":
					detail.Source.Keys[0].RawLine += " changed"
				}
				data, _ := json.Marshal(detail)
				return json.Unmarshal(data, result)
			}
			err = r.Import(AccountKeyImport{Account: account, Key: key})
			if failure != "" {
				if err == nil {
					t.Fatal("stale or unobserved source imported")
				}
				if _, err := os.Stat(ssh.Keys.Path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("refusal wrote destination")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := ssh.Keys.Read()
			if err != nil || !strings.Contains(string(data), key.RawLine) {
				t.Fatalf("import: %q %v", data, err)
			}
			if err := r.Import(AccountKeyImport{Account: account, Key: key}); err == nil {
				t.Fatal("duplicate imported")
			}
		})
	}
}

func TestAccountAccessCloseCancelsAndJoinsReaders(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewAccountAccess(NewSSHAccess())
		started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
		r.call = func(ctx context.Context, _ string, _, _ any) error {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			return ctx.Err()
		}
		done := make(chan error, 1)
		go func() { _, err := r.List(); done <- err }()
		<-started
		closed := make(chan struct{})
		go func() { r.Close(); close(closed) }()
		synctest.Wait()
		select {
		case <-canceled:
		default:
			t.Error("Close did not cancel the reader")
		}
		select {
		case <-closed:
			t.Error("Close returned while the reader was still blocked")
		default:
		}
		// Release the reader only after proving cancellation alone cannot finish Close.
		close(release)
		synctest.Wait()
		select {
		case <-closed:
		default:
			t.Error("Close did not return after the reader finished")
		}
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("reader result: %v", err)
			}
		default:
			t.Error("reader did not finish")
		}
		if _, err := r.List(); !errors.Is(err, context.Canceled) {
			t.Errorf("read after Close: %v", err)
		}
	})
}
