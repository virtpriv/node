package jobs

import (
	"errors"
	"slices"
	"testing"

	"github.com/virtpriv/node/internal/api"
	"github.com/virtpriv/node/internal/paths"
)

// Refresh selection is a behavioral contract: wallet creation needs only the
// macaroon, while connection repair needs both LND credentials in order.

var expectedMatrix = map[string][]string{
	api.VerbStageLNDCredentials: {
		paths.StateLNDTLSCert,
		paths.StateLNDMacaroon,
	},
	api.VerbStageLNDMacaroon: {
		paths.StateLNDMacaroon,
	},
	api.KindP2PUpgrade: {
		paths.StateLNDTLSCert,
	},
	api.KindSyncthingInstall: {
		paths.StateSyncthingAPIKey,
	},
	// Applied by the handler only for the lnd unit (start or
	// restart): LND can regenerate its TLS certificate during
	// startup, so the staged copy must follow.
	api.KindServiceAction: {
		paths.StateLNDTLSCert,
	},
	// Deliberately absent: rebuild-tor-config and
	// rebuild-ssh-config no longer touch any board fact —
	// onion addresses and the password-auth answer are
	// live-read (no copy exists to go stale).
}

func TestRestageSelectsOnlyRequiredCredentials(t *testing.T) {
	original := stagers
	t.Cleanup(func() { stagers = original })
	for verb, want := range expectedMatrix {
		t.Run(verb, func(t *testing.T) {
			var calls []string
			stagers = make(map[string]func() error)
			for file := range original {
				stagers[file] = func() error { calls = append(calls, file); return nil }
			}
			if err := Restage(verb); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(calls, want) {
				t.Fatalf("refreshed %v, want %v", calls, want)
			}
		})
	}
	for verb := range freshnessMatrix {
		if _, ok := expectedMatrix[verb]; !ok {
			t.Errorf("unreviewed refresh selection for %s", verb)
		}
	}
}

func TestRestageStopsOnMissingOrFailedStager(t *testing.T) {
	original := stagers
	t.Cleanup(func() { stagers = original })
	failure := errors.New("certificate publication failed")
	for _, missing := range []bool{false, true} {
		called := false
		stagers = map[string]func() error{
			paths.StateLNDMacaroon: func() error { called = true; return nil },
		}
		if !missing {
			stagers[paths.StateLNDTLSCert] = func() error { return failure }
		}
		err := Restage(api.VerbStageLNDCredentials)
		if err == nil || called {
			t.Fatalf("missing=%v error=%v later stager called=%v", missing, err, called)
		}
		if !missing && !errors.Is(err, failure) {
			t.Fatalf("publication error lost: %v", err)
		}
	}
}

// expectedBoardFiles restates the complete board INDEPENDENTLY
// of paths.go and matrix.go: the machine-cadence credential
// facts, and nothing else. Display facts (onion addresses, the
// Syncthing device ID, the SSH password-auth answer) are
// deliberately NOT here — they are live-read, with no copy.
var expectedBoardFiles = map[string]bool{
	paths.StateBitcoindRPCPass:      true,
	paths.StateLNDTLSCert:           true,
	paths.StateLNDMacaroon:          true,
	paths.StateSyncthingAPIKey:      true,
	paths.StateSyncthingWebPassword: true,
}

// Every fact in the matrix must have a stager, every verb in
// the matrix must exist on the verb menu, and every stager key
// must be a real board path.
func TestFreshnessMatrixIsClosed(t *testing.T) {
	boardFiles := expectedBoardFiles
	for verb, files := range freshnessMatrix {
		_, isKind := kinds[verb]
		isCopy := verb == api.VerbStageLNDCredentials || verb == api.VerbStageLNDMacaroon
		if !isKind && !isCopy {
			t.Errorf("matrix entry %s is neither a job kind nor a credential copy", verb)
		}
		for _, f := range files {
			if !boardFiles[f] {
				t.Errorf("%s re-stages unknown file %s", verb, f)
			}
			if _, ok := stagers[f]; !ok {
				t.Errorf("%s: no stager registered for %s", verb, f)
			}
		}
	}
	for f := range stagers {
		if !boardFiles[f] {
			t.Errorf("stager registered for unknown file %s", f)
		}
	}
}

// ── Freshness-declaration tests ──────────────────────────
//
// The rule: every board fact declares exactly one freshness
// story.
// Adding board fact number five without deciding how it stays
// fresh — or retiring a read verb without replacing the story
// it serves — fails here.

func TestEveryBoardFactDeclaresFreshness(t *testing.T) {
	for f := range expectedBoardFiles {
		story, ok := freshness[f]
		if !ok {
			t.Errorf("%s has no freshness declaration — how "+
				"does this fact stay fresh? Declare watched, "+
				"healed, live-read, or static-by-decision in "+
				"matrix.go", f)
			continue
		}
		switch story {
		case freshWatched, freshHealed, freshStatic:
		case freshLiveRead:
			t.Errorf("%s declares live-read but has a board "+
				"file — a live-read fact keeps no copy", f)
		default:
			t.Errorf("%s declares unknown story %q", f, story)
		}
	}
	for f := range freshness {
		if !expectedBoardFiles[f] {
			t.Errorf("freshness declares %s, which is not a "+
				"board file — update BOTH deliberately", f)
		}
	}
	for f := range stagers {
		if _, ok := freshness[f]; !ok {
			t.Errorf("stager registered for %s without a "+
				"freshness declaration", f)
		}
	}
}

// The watched and healed stories both depend on a stager
// existing (the path unit and the self-heal each end in a
// re-stage); a declaration without the mechanism is a lie.
func TestFreshnessStoriesHaveTheirMechanism(t *testing.T) {
	for f, story := range freshness {
		if story == freshWatched || story == freshHealed {
			if _, ok := stagers[f]; !ok {
				t.Errorf("%s declares %s but has no stager",
					f, story)
			}
		}
	}
}
