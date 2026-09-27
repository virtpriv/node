// internal/helperd/matrix.go

package helperd

import (
	"fmt"

	"github.com/virtualprivatenode/vpn/internal/helper"
	"github.com/virtualprivatenode/vpn/internal/host"
	"github.com/virtualprivatenode/vpn/internal/paths"
)

// ── The freshness matrix ─────────────────────────────────
//
// The staging board's one failure mode is staleness: an
// operation changes reality and the board file carrying the old
// fact survives. The defense is this table. It maps each verb
// to the board facts that verb invalidates, and restage(verb)
// — called by every handler as part of its postcondition — is
// driven BY the table, so the mapping is the mechanism, not
// documentation that can drift from it.
//
// Reading the matrix: a verb not listed here invalidates no
// staged fact (a package upgrade changes no staged credential).
// One entry is conditional: service-action invalidates the
// staged LND TLS certificate only when the unit acted on is
// lnd — the handler applies the entry under that condition,
// because the verb-keyed table cannot see verb parameters. The
// unit tests in matrix_test.go walk every cell: every listed
// fact has a stager, every stager is reachable from a verb or
// the installer's staging step, and the sets match this table
// exactly.

// stagers maps each board file to the function that refreshes
// it from current reality. All run as root.
var stagers = map[string]func() error{
	paths.StateLNDTLSCert:      host.StageLNDTLSCert,
	paths.StateLNDMacaroon:     host.StageLNDMacaroon,
	paths.StateSyncthingAPIKey: host.StageSyncthingAPIKey,
}

// freshnessMatrix: verb → board facts the verb invalidates and
// therefore re-stages on success.
var freshnessMatrix = map[string][]string{
	// The LND client's repair path cannot know whether a failed connection
	// reflects a stale certificate, macaroon, or both, so it refreshes both.
	helper.VerbStageLNDCredentials: {
		paths.StateLNDTLSCert,
		paths.StateLNDMacaroon,
	},
	// Wallet creation mints only the admin macaroon. It must not fail because
	// an unrelated TLS certificate refresh was attempted.
	helper.VerbStageLNDMacaroon: {
		paths.StateLNDMacaroon,
	},
	// A P2P mode change alters the cert's contents (LND
	// regenerates it via tlsautorefresh), so the staged copy
	// is stale the moment LND restarts.
	helper.VerbUpgradeP2PToHybrid: {
		paths.StateLNDTLSCert,
	},
	// A fresh Syncthing install generates a new identity and
	// API key.
	helper.VerbSyncthingInstall: {
		paths.StateSyncthingAPIKey,
	},
	// An lnd start or restart can replace an expired TLS
	// certificate; tlsautorefresh also detects configured SAN
	// changes at startup, so the staged copy must
	// follow. CONDITIONAL: the handler applies this entry only
	// when the unit acted on is lnd and the action is not stop
	// — no other unit changes a staged fact.
	helper.VerbServiceAction: {
		paths.StateLNDTLSCert,
	},
}

// ── Freshness declarations ───────────────────────────────
//
// Every fact the TUI consumes has exactly one declared
// freshness story, and matrix_test.go fails on any board file
// without one — a contributor adding a new fact is stopped by
// a red test asking "how does this stay fresh?". The stories:
//
//   - watched: the fact's source can change autonomously (no
//     operation of ours involved), and a systemd path unit on
//     the source triggers a re-stage within seconds, before
//     any human shows up to read a stale copy.
//   - healed: the fact is a credential that fails CLOSED at
//     the moment of use when stale (the cryptography rejects
//     it, in front of the calling code), and the consumer
//     repairs the failure by asking for a re-stage and
//     retrying once, rate-limited process-wide.
//   - live-read: no copy exists at all. The fact is a display
//     or safety observation whose staleness would produce no
//     failure to repair from, so screens read it through a
//     read-only verb at human cadence (liveReadFacts below).
//   - static-by-decision: the fact changes only through
//     covered operations (install, a menu verb); foreign
//     change is possible for root but is not promisable, and
//     divergence surfaces as an honest, named error — never a
//     silent false success.
const (
	freshWatched  = "watched"
	freshHealed   = "healed"
	freshLiveRead = "live-read"
	freshStatic   = "static-by-decision"
)

// freshness declares the story for every BOARD file. These are
// the machine-cadence facts (consumed per RPC call / per dial)
// — the only ones that justify a cached copy at all.
var freshness = map[string]string{
	// Watched (the path unit), and additionally healed as the
	// backstop: the connection self-heal in the TUI's LND
	// client re-stages on persistent connection failure.
	paths.StateLNDTLSCert: freshWatched,
	// LND rejects a stale macaroon on every call
	// (Unauthenticated); the client reconnects and the dial-
	// time repair re-stages both LND credentials.
	paths.StateLNDMacaroon: freshHealed,
	// Regenerated only by a Syncthing (re)install, which the
	// syncthing-install verb covers. The one foreign surface
	// that can change it — the Syncthing web UI — produces an
	// HTTP 403 that every REST wrapper now fails loudly on,
	// so divergence is an immediate honest error.
	paths.StateSyncthingAPIKey: freshStatic,
	// Generated and staged by the one Syncthing install operation. Syncthing
	// stores only a hash, so there is no later plaintext source to re-stage.
	paths.StateSyncthingWebPassword: freshStatic,
	// Written by the installer, fresh pair per install; if
	// root replaces the server-side half by hand, bitcoind
	// answers 401 and the TUI's error names the
	// divergence. That residue is accepted, not healed.
	paths.StateBitcoindRPCPass: freshStatic,
}

// liveReadFacts pins the fourth story: facts with NO board
// copy, served live by a read-only verb. The map value is the
// verb that serves the fact; matrix_test.go asserts each is on
// the menu, so retiring a verb without a replacement story is
// a red test too.
var liveReadFacts = map[string]string{
	"local-accounts":       helper.VerbReadAccounts,
	"local-account-access": helper.VerbReadAccount,
	"onion-addresses":      helper.VerbReadNodeAddresses,
	"syncthing-device-id":  helper.VerbReadNodeAddresses,
	"ssh-password-auth":    helper.VerbReadSSHAuth,
	"wallet-existence":     helper.VerbReadWalletState,
	"key-verification":     helper.VerbReadKeyVerificationState,
}

// restage refreshes every fact the given verb invalidates.
// Failures here fail the VERB: an operation that succeeded but
// left a stale board would be exactly the silent divergence the
// board's contract forbids — better to surface it now, with the
// journal naming the file.
func restage(verb string) error {
	for _, file := range freshnessMatrix[verb] {
		st, ok := stagers[file]
		if !ok {
			return fmt.Errorf(
				"no stager for %s (defect: matrix and stagers "+
					"disagree)", file)
		}
		if err := st(); err != nil {
			return fmt.Errorf("restage %s: %w", file, err)
		}
	}
	return nil
}
