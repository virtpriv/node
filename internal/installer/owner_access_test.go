package installer

import (
	"errors"
	"strings"
	"testing"

	"github.com/virtualprivatenode/vpn/internal/loginpassword"
)

func TestOwnerSudoFollowsConfirmedPasswordAndDeliveryMarker(t *testing.T) {
	for _, failure := range []string{"create", "password", "marker", "sudo", "profile", ""} {
		t.Run("failure="+failure, func(t *testing.T) {
			injected := errors.New("injected failure")
			var events []string
			record := func(step string) error {
				events = append(events, step)
				if step == failure {
					return injected
				}
				return nil
			}
			password, _ := loginpassword.New("initial test password")
			dec := &InstallDecisions{Password: password, GeneratedPassword: password.Text()}
			ops := identityAccessOps{
				create: func() error { return record("create") },
				password: func(got loginpassword.Password) error {
					if got.Text() != password.Text() {
						t.Fatal("changed initial password")
					}
					return record("password")
				},
				markPending: func() error {
					if !dec.PasswordApplied {
						t.Fatal("marker before applied password")
					}
					return record("marker")
				},
				sudo: func() error { return record("sudo") }, autoLaunch: func() error { return record("profile") },
			}
			err := provisionIdentityAccess(dec, ops)
			if (failure == "") != (err == nil) || (err != nil && !errors.Is(err, injected)) {
				t.Fatalf("result %v", err)
			}
			expected := []string{"create", "password", "marker", "sudo", "profile"}
			if failure != "" {
				for i, step := range expected {
					if step == failure {
						expected = expected[:i+1]
						break
					}
				}
			}
			if strings.Join(events, ",") != strings.Join(expected, ",") {
				t.Fatalf("unsafe order: %v", events)
			}
			if dec.PasswordApplied != (failure != "create" && failure != "password") {
				t.Fatal("lost applied-password state after partial failure")
			}
		})
	}
}
