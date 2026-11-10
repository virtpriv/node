package jobs

import (
	"fmt"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/host"
	"github.com/virtpriv/node/internal/paths"
)

// ── The freshness matrix ─────────────────────────────────
//
// The staging board's one failure mode is staleness: a change alters reality
// and the board file carrying the old fact survives. The defense is this
// table. It maps each job kind and each credential copy to the board facts it
// invalidates, and Restage(name), called by each of them as its last step
// before it reports success, is driven by the table. The mapping is the
// mechanism, not documentation that can drift from it.
//
// A name not listed here invalidates no staged fact (a package upgrade
// changes no staged credential). One entry is conditional: a service action
// invalidates the staged LND TLS certificate only when it starts or restarts
// lnd. The job applies the entry under that condition, because the table
// cannot see parameters. The tests in matrix_test.go walk every cell, so
// every listed fact has a stager, every stager is reachable from a job, a
// copy or the installer's staging step, and the sets match this table.

// stagers maps each board file to the function that refreshes
// it from current reality. All run as root.
var stagers = map[string]func() error{
	paths.StateLNDTLSCert:      host.StageLNDTLSCert,
	paths.StateLNDMacaroon:     host.StageLNDMacaroon,
	paths.StateSyncthingAPIKey: host.StageSyncthingAPIKey,
}

// freshnessMatrix: a job kind or credential copy → the board facts it
// invalidates and therefore stages again on success.
var freshnessMatrix = map[string][]string{
	// The LND client's repair path cannot know whether a failed connection
	// reflects a stale certificate, macaroon, or both, so it refreshes both.
	api.VerbStageLNDCredentials: {
		paths.StateLNDTLSCert,
		paths.StateLNDMacaroon,
	},
	// Wallet creation mints only the admin macaroon. It must not fail because
	// an unrelated TLS certificate refresh was attempted.
	api.VerbStageLNDMacaroon: {
		paths.StateLNDMacaroon,
	},
	// A P2P mode change alters the cert's contents (LND
	// regenerates it via tlsautorefresh), so the staged copy
	// is stale the moment LND restarts.
	api.KindP2PUpgrade: {
		paths.StateLNDTLSCert,
	},
	// A fresh Syncthing install generates a new identity and
	// API key.
	api.KindSyncthingInstall: {
		paths.StateSyncthingAPIKey,
	},
	// An lnd start or restart can replace an expired TLS
	// certificate, and tlsautorefresh also detects configured SAN
	// changes at startup, so the staged copy must
	// follow. CONDITIONAL: the job applies this entry only
	// when the unit acted on is lnd and the action is not stop.
	// No other unit changes a staged fact.
	api.KindServiceAction: {
		paths.StateLNDTLSCert,
	},
}

// ── Freshness declarations ───────────────────────────────
//
// Every fact the TUI consumes has exactly one declared
// freshness story, and matrix_test.go fails on any board file
// without one. A contributor adding a new fact is stopped by
// a red test asking "how does this stay fresh?". There are
// four stories.
//
//   - watched: the fact's source can change autonomously (no
//     operation of ours involved), and a systemd path unit on
//     the source triggers a re-stage within seconds, before
//     any human shows up to read a stale copy.
//   - healed: the fact is a credential that fails CLOSED at
//     the moment of use when stale (the cryptography rejects
//     it, in front of the calling code). The consumer asks for
//     a re-stage and retries once, rate-limited process-wide,
//     whenever the first call of a new connection fails.
//   - live-read: no copy exists at all. The fact is a display
//     or safety observation whose staleness would produce no
//     failure to repair from, so screens read it through a
//     read of vpnd at human cadence (vpnd lists them).
//   - static-by-decision: the fact changes only through
//     covered operations (install, a job). Foreign
//     change is possible for root but is not promisable, and
//     divergence surfaces as an honest, named error, never a
//     silent false success.
const (
	freshWatched  = "watched"
	freshHealed   = "healed"
	freshLiveRead = "live-read"
	freshStatic   = "static-by-decision"
)

// freshness declares the story for every BOARD file. These are
// the machine-cadence facts (consumed per RPC call or per dial),
// the only ones that justify a cached copy at all.
var freshness = map[string]string{
	// Watched (the path unit), and additionally healed as the
	// backstop. LND replaces its certificate only when it
	// starts, which ends the TUI's link. The new connection's
	// first call then fails the TLS handshake and, rate limit
	// permitting, re-stages both LND credentials.
	paths.StateLNDTLSCert: freshWatched,
	// Re-staged by the repair on connect, at TUI startup,
	// after a job or copy and after a lost link. LND
	// refuses a stale macaroon with gRPC code Unknown, which an
	// open connection does not treat as a lost link, so it is
	// repaired at the next connection.
	paths.StateLNDMacaroon: freshHealed,
	// Regenerated only by a Syncthing (re)install, which the
	// Syncthing install job covers. The one foreign surface
	// that can change it, the Syncthing web UI, produces an
	// HTTP 403 that every REST wrapper now fails loudly on,
	// so divergence is an immediate honest error.
	paths.StateSyncthingAPIKey: freshStatic,
	// Generated and staged by the one Syncthing install operation. Syncthing
	// stores only a hash, so there is no later plaintext source to re-stage.
	paths.StateSyncthingWebPassword: freshStatic,
	// Written by the installer, fresh pair per install. If
	// root replaces the server-side half by hand, bitcoind
	// answers 401 and the TUI's error names the
	// divergence. That residue is accepted, not healed.
	paths.StateBitcoindRPCPass: freshStatic,
}

// Restage refreshes every fact the named job kind or copy invalidates. A
// failure here fails the job or the copy. A change that succeeded but left a
// stale board would be exactly the silent divergence the board's contract
// forbids. Better to surface it now, with the journal naming the file.
func Restage(name string) error {
	for _, file := range freshnessMatrix[name] {
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
