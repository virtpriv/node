// internal/installer/identity.go

package installer

// Installer owns confirmed decisions and generated password delivery.
// Host owns account creation, password application and login-shell writes.

import (
	"crypto/rand"
	"fmt"

	"github.com/virtualprivatenode/vpn/internal/host"
	"github.com/virtualprivatenode/vpn/internal/loginpassword"
)

// ── Applying the decisions ───────────────────────────────

// InstallDecisions carries the wizard's answers into the engine
// steps. Collected before the steps run (interactively or by the
// unattended defaults) and read by step closures at execution.
type InstallDecisions struct {
	// Password is validated using the initial login-password policy.
	Password loginpassword.Password
	// GeneratedPassword is set only on the --unattended path
	// (ruling vii: random generation survives only there);
	// printed once at the end of the run, never logged.
	GeneratedPassword string
	// PasswordApplied is set by the identity step after
	// chpasswd succeeded. The generated password is printed
	// ONLY when this is set: on a resume that ledger-skips the
	// identity step (or an --until=bake run that filters it
	// out), this pass generated a password that was never
	// applied — printing it would hand the operator a
	// credential that does not work.
	PasswordApplied bool
	// DbCacheMB is the hardware-fit step's confirmed dbcache
	// (ruling viii).
	DbCacheMB int
}

// applyIdentityAccess coordinates access provisioning and password delivery.
// Record successful password application before the marker and shell setup so
// a later failure cannot obscure the credential that was already applied.
func applyIdentityAccess(dec *InstallDecisions) error {
	return provisionIdentityAccess(dec, identityAccessOps{
		create: host.CreateOperatorAccount, password: host.SetLoginPassword,
		markPending: markPasswordPending, sudo: host.ConfigureOperatorSudo,
		autoLaunch: host.ConfigureOperatorAutoLaunch,
	})
}

// Keep password application and its delivery marker ahead of sudo and shell
// setup. A failed password operation must never reach the sudo grant.
type identityAccessOps struct {
	create                        func() error
	password                      func(loginpassword.Password) error
	markPending, sudo, autoLaunch func() error
}

func provisionIdentityAccess(dec *InstallDecisions, ops identityAccessOps) error {
	if err := ops.create(); err != nil {
		return err
	}
	if err := ops.password(dec.Password); err != nil {
		return fmt.Errorf("set owner password: %w", err)
	}
	dec.PasswordApplied = true
	if dec.GeneratedPassword != "" {
		if err := ops.markPending(); err != nil {
			return fmt.Errorf("record password-pending marker: %w", err)
		}
	}
	if err := ops.sudo(); err != nil {
		return err
	}
	return ops.autoLaunch()
}

// generateAdminPassword returns a random alphanumeric password
// for the --unattended fallback: 25 characters from a 62-symbol
// alphabet (~148 bits). Uses rejection sampling for uniformity.
func generateAdminPassword() string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
		"abcdefghijklmnopqrstuvwxyz0123456789"
	const length = 25
	out := make([]byte, 0, length)
	buf := make([]byte, 64)
	for len(out) < length {
		rand.Read(buf)
		for _, b := range buf {
			// Reject bytes that would bias the modulus.
			if int(b) >= 248 { // 248 = 4*62
				continue
			}
			out = append(out, alphabet[int(b)%62])
			if len(out) == length {
				break
			}
		}
	}
	return string(out)
}
