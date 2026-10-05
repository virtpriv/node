package host

import (
	"errors"
	"fmt"
	"os/user"
	"testing"

	"github.com/virtpriv/node/internal/paths"
)

func TestOperatorAccountLookupRefusesUncertainState(t *testing.T) {
	lookupFailure := errors.New("account database unavailable")
	createFailure := errors.New("account creation failed")
	for _, tt := range []struct {
		name      string
		lookupErr error
		runErr    error
		wantErr   error
		creates   int
	}{
		{name: "existing account"},
		{name: "absent account", lookupErr: user.UnknownUserError(paths.AdminUser), creates: 1},
		{name: "unexpected lookup failure", lookupErr: lookupFailure, wantErr: lookupFailure},
		{name: "wrapped lookup failure", lookupErr: fmt.Errorf("lookup: %w", lookupFailure), wantErr: lookupFailure},
		{name: "creation failure", lookupErr: user.UnknownUserError(paths.AdminUser), runErr: createFailure, wantErr: createFailure, creates: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			creates := 0
			err := ensureOperatorAccount(func(name string) (*user.User, error) {
				if name != paths.AdminUser {
					t.Fatalf("looked up a different identity: %q", name)
				}
				return &user.User{Username: paths.AdminUser}, tt.lookupErr
			}, func(name string, args ...string) error {
				creates++
				if name != "adduser" || len(args) == 0 || args[len(args)-1] != paths.AdminUser {
					t.Fatalf("unexpected mutation: %q %v", name, args)
				}
				return tt.runErr
			})
			if !errors.Is(err, tt.wantErr) || creates != tt.creates {
				t.Fatalf("error=%v creation attempts=%d; want error=%v attempts=%d", err, creates, tt.wantErr, tt.creates)
			}
		})
	}
}
