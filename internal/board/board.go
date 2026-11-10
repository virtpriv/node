// Package board reads the staging board, the root-written files under
// /var/lib/vpn/state that carry deliberately staged credentials and fixed
// facts to the admin user, with no privileged code on the read path.
package board

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ── The staging board ────────────────────────────────────
//
// /var/lib/vpn/state holds one file per privileged fact the TUI needs to
// use: the staged bitcoind RPC password, copies of LND's TLS certificate and
// admin macaroon, Syncthing's API key, and the generated Syncthing web
// password. Root writes them, the admin user (group vpn) reads them, nothing
// else can see them.
//
// The contract that keeps the board trustworthy:
//
//   - every file is written again by whatever operation changes the fact it
//     carries, at install and afterwards by the job or vpnd request that
//     makes the change. The full map is in internal/jobs/matrix.go and its
//     tests.
//   - readers never guess. A missing or unreadable file shows the feature as
//     unavailable with a logged reason, not a stale or default value.
//   - nothing wallet-authoritative lives here. The seed, LND's databases and
//     the auto-unlock password file are not board facts. The admin macaroon
//     copy is a revocable credential the admin user needs to operate the
//     node, the same authority every wallet screen already uses.

// Read reads one staging board file. The error names the file and points at
// vpnd's journal, because a missing board file means a staging step failed
// or was skipped, a real defect to report.
func Read(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf(
			"staged fact %s unavailable (%v). A change on the node should "+
				"have staged it. Check: journalctl -t vpnd and the install log",
			filepath.Base(path), err)
	}
	return data, nil
}

// ReadString reads a board file as a trimmed string (hostname files and the
// API key are single lines).
func ReadString(path string) (string, error) {
	data, err := Read(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
