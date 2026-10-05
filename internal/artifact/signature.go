// Package artifact verifies signatures using caller-selected trust anchors.
package artifact

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/virtpriv/node/internal/logger"
)

// Result is what one signature file proved.
type Result struct {
	// Signers counts distinct pinned signers whose signature is accepted.
	Signers int
	// Bad reports a signature that does not match the data.
	Bad bool
	// Revoked lists, in sorted order, pinned primary fingerprints that signed
	// only with a revoked key. Such a signer is never counted.
	Revoked []string
	// Imported reports, for each key file, whether gpg took a pinned key or
	// an update to one from it. A file that is unreadable, is not a key, or
	// holds only other keys is false.
	Imported map[string]bool
}

// VerifySignature verifies a GPG signature inside an ephemeral keyring. It
// imports the key files in the order given and reads gpg's machine-readable
// output; gpg's exit code and its messages for people are not used.
//
// A pinned signer counts once, by primary-key fingerprint, when gpg reports
// its signature as good. A signature made with a revoked key or revoked
// signing subkey never counts, whatever reason or date the revocation gives
// and whichever key file carried the revocation. An expired key still counts:
// expiry is not treated as a withdrawal of trust. A signature that has itself
// expired does not count.
//
// GPG may exit unsuccessfully when another signature uses an unavailable key,
// as in Syncthing's dual-signed releases. Callers must require their pinned
// signer threshold and reject Bad, rather than infer trust from exit status.
//
// dataFile == "" means sigFile is CLEARSIGNED (data and signature in one
// file, e.g. Syncthing's sha256sum.txt.asc). Otherwise the signature is
// detached and gpg gets both arguments.
func VerifySignature(
	keyFiles []string,
	sigFile, dataFile string,
	pinnedFPs map[string]bool,
) (Result, error) {
	// Ephemeral GPG home: 0700, random path, cleaned up on return.
	gpgHome, err := os.MkdirTemp("", "vpn-gpg-")
	if err != nil {
		return Result{}, fmt.Errorf(
			"create ephemeral gpg home: %w", err)
	}
	defer os.RemoveAll(gpgHome)

	// Import order matters for one case: a copy of a key without a user ID
	// only adds its revocations and subkeys to a key that is already there.
	imported := map[string]bool{}
	for _, kf := range keyFiles {
		status, err := gpgStatus(gpgHome, "--import", kf)
		if err != nil {
			return Result{}, err
		}
		for _, fields := range statusLines(status) {
			if fields[1] == "IMPORT_OK" && len(fields) >= 4 && pinnedFPs[fields[3]] {
				imported[kf] = true
			}
		}
		if !imported[kf] {
			logger.Verify("SKIP key file %s: no pinned key in it",
				filepath.Base(kf))
		}
	}

	// gpg reports a signature from a key that is both expired and revoked
	// only as made by an expired key. The key listing names every revoked
	// key and subkey regardless of expiry, so it decides revocation.
	// A listing that cannot be read must not pass for one without revoked
	// keys. An empty keyring lists nothing and exits successfully.
	listing, err := gpgRun(gpgHome, false, "--with-colons", "--list-keys")
	if err != nil {
		return Result{}, fmt.Errorf("list imported keys: %w", err)
	}
	revokedKeys := revokedFingerprints(listing)

	args := []string{"--verify", sigFile}
	if dataFile != "" {
		args = append(args, dataFile)
	}
	status, err := gpgStatus(gpgHome, args...)
	if err != nil {
		return Result{}, err
	}
	result := readStatus(status, pinnedFPs, revokedKeys)
	result.Imported = imported
	return result, nil
}

// gpgStatus runs gpg and returns its status lines. They arrive alone on
// standard output. Messages for people go to standard error and can contain
// text chosen by whoever made a key, so they are not read.
//
// An unsuccessful exit is expected for a file that is not a key and for a
// signature from an unavailable key, so the status lines decide.
func gpgStatus(gpgHome string, args ...string) (string, error) {
	return gpgRun(gpgHome, true, append([]string{"--status-fd", "1"}, args...)...)
}

// gpgRun returns gpg's standard output. Not being able to run gpg at all is
// always an error: it must not look like a missing signature.
func gpgRun(gpgHome string, allowFailedExit bool, args ...string) (string, error) {
	cmd := exec.Command("gpg", append([]string{"--homedir", gpgHome, "--batch"}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		if _, exited := errors.AsType[*exec.ExitError](err); !exited || !allowFailedExit {
			return "", fmt.Errorf("run gpg: %w", err)
		}
	}
	return out.String(), nil
}

func statusLines(status string) [][]string {
	var lines [][]string
	for _, line := range strings.Split(status, "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "[GNUPG:]" {
			lines = append(lines, fields)
		}
	}
	return lines
}

// revokedFingerprints reads gpg's colon listing. A revoked primary key takes
// all of its subkeys with it.
func revokedFingerprints(listing string) map[string]bool {
	revoked := map[string]bool{}
	primaryRevoked, keyRevoked := false, false
	for _, line := range strings.Split(listing, "\n") {
		f := strings.Split(line, ":")
		switch {
		case f[0] == "pub" && len(f) > 1:
			primaryRevoked = f[1] == "r"
			keyRevoked = primaryRevoked
		case f[0] == "sub" && len(f) > 1:
			keyRevoked = primaryRevoked || f[1] == "r"
		case f[0] == "fpr" && len(f) > 9:
			// Each fpr record belongs to the pub or sub record before it.
			if keyRevoked {
				revoked[f[9]] = true
			}
		}
	}
	return revoked
}

// readStatus judges each signature by its own status lines. KEYEXPIRED lines
// are not used: gpg also prints them for old subkeys that did not make the
// signature being checked.
func readStatus(status string, pinnedFPs, revokedKeys map[string]bool) Result {
	const (
		unknown = iota
		good
		revoked
	)
	var result Result
	accepted := map[string]bool{}
	refused := map[string]bool{}
	standing := unknown
	for _, fields := range statusLines(status) {
		switch fields[1] {
		case "NEWSIG", "ERRSIG", "EXPSIG":
			standing = unknown
		case "GOODSIG", "EXPKEYSIG":
			standing = good
		case "REVKEYSIG":
			standing = revoked
		case "BADSIG":
			standing = unknown
			result.Bad = true
			logger.Verify("BADSIG: %s", strings.Join(fields, " "))
		case "VALIDSIG":
			// VALIDSIG <signing key> ... <primary key>, twelve fields in all.
			// Pins are primary fingerprints, also when a subkey signed.
			if len(fields) < 12 {
				standing = unknown
				continue
			}
			signing, primary := fields[2], fields[len(fields)-1]
			if revokedKeys[signing] || revokedKeys[primary] {
				standing = revoked
			}
			switch {
			case !pinnedFPs[primary]:
				logger.Verify("VALIDSIG unpinned (ignored): %s", primary)
			case standing == good:
				accepted[primary] = true
				logger.Verify("VALIDSIG pinned: %s", primary)
			case standing == revoked:
				refused[primary] = true
				logger.Verify("REVOKED pinned signer (not counted): %s", primary)
			default:
				logger.Verify("VALIDSIG pinned but not in good standing (not counted): %s", primary)
			}
			standing = unknown
		}
	}
	result.Signers = len(accepted)
	for primary := range refused {
		if !accepted[primary] {
			result.Revoked = append(result.Revoked, primary)
		}
	}
	slices.Sort(result.Revoked)
	return result
}
