// internal/installer/passwordpending.go

package installer

// The password-pending marker closes a fail-then-resume hole on
// the unattended path (live-run finding): the identity step
// APPLIES the generated admin password early in the run, but the
// password is PRINTED only at the very end of a completed run. A
// run that fails between the two leaves the box holding a
// recovery password nobody has ever seen — and the resuming pass
// ledger-skips the identity step, so without outside help it
// would complete without ever showing a working credential.
//
// The marker is that outside help: a root-owned flag file whose
// presence means "a generated admin password is applied on this
// box but was never displayed". A completing unattended pass
// that finds it re-applies its own freshly generated password
// and prints that one instead. Only installer completion clears
// the marker after successful delivery. Native passwd changes
// do not alter installation state. The file carries no secret;
// plaintext passwords exist only in process memory.

import (
	"fmt"
	"os"

	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

const passwordPendingNote = `An unattended install applied a generated owner login password
that has not been displayed yet. Resume the unattended installation
to deliver a working password and clear this marker.
Native password changes do not clear installation state.
`

// markPasswordPending records that a generated password was
// applied but not yet displayed. Failure is returned, not
// swallowed: this marker is what guarantees the credential can
// still reach the operator if the run dies before the print.
func markPasswordPending() error {
	return system.WriteFileRoot(
		paths.PasswordPendingMarker,
		[]byte(passwordPendingNote), 0600)
}

// passwordPending reports whether the marker is present.
func passwordPending() bool {
	_, err := os.Lstat(paths.PasswordPendingMarker)
	return err == nil
}

// clearPasswordPendingMarkerStrict is used only by installer completion. A
// terminal ledger may never be published while this marker remains, so cleanup
// failure is an installation failure rather than a warning.
func clearPasswordPendingMarkerStrict() error {
	if err := os.Remove(paths.PasswordPendingMarker); err != nil &&
		!os.IsNotExist(err) {
		return fmt.Errorf("remove password-pending marker %s: %w",
			paths.PasswordPendingMarker, err)
	}
	return nil
}

// needsPasswordReapply decides whether a completing unattended
// pass must re-apply its generated password so the printed
// credential is one that works. True only when this pass
// generated a password (unattended), the identity step did NOT
// apply it (ledger-skipped on a resume), and the marker says an
// earlier pass left one undisplayed. A re-run after a completed
// install has no marker, so it re-applies and prints nothing —
// the password the operator saved (or later changed) stands.
// Pure — unit-tested.
func needsPasswordReapply(
	generated string, appliedThisPass, pendingOnDisk bool,
) bool {
	return generated != "" && !appliedThisPass && pendingOnDisk
}
