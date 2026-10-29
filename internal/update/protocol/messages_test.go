package protocol

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// A v0.7.0 node keeps this advice for its first update, used when the worker
// sends none. A failure named with a code gets its exact next step only
// where that step is true. The locked wallet explains only an lnd stop, and
// a service is named as the System tab shows it, where the operator starts
// or restarts it. Anything else, a code from a later release included, gets
// the general advice.
func TestAdviseGivesStepsOnlyWhereTheyApply(t *testing.T) {
	_, general := Advise(nil, false)
	for _, tc := range []struct {
		code    FailureCode
		service Component
		step    bool
		name    string // the service a start or restart step names
	}{
		{FailureStopFailed, LND, true, ""},
		{FailureStopFailed, Bitcoin, false, ""},
		{FailureStopFailed, Syncthing, false, ""},
		{FailureStartInterrupted, LND, true, ""},
		{FailureStartInterrupted, Bitcoin, true, ""},
		{FailureServiceNotRunning, Bitcoin, true, "bitcoind"},
		{FailureServiceNotRunning, Syncthing, true, "syncthing"},
		{FailureServiceChanged, LND, true, "lnd"},
		{FailureServiceChanged, Bitcoin, true, "bitcoind"},
		{FailureServiceNotRunning, "tor", false, ""},
		{FailureServiceNotRunning, "", false, ""},
		{"snapshot_failed", LND, false, ""},
	} {
		t.Run(fmt.Sprintf("%s %s", tc.code, tc.service), func(t *testing.T) {
			_, words := Advise(&Failure{Code: tc.code, Service: tc.service}, false)
			if (words != general) != tc.step {
				t.Fatalf("advice %q, want an exact step %v", words, tc.step)
			}
			names := strings.FieldsFunc(words, func(r rune) bool { return r == ' ' || r == '\n' || r == '.' || r == ',' })
			if tc.name != "" && !slices.Contains(names, tc.name) {
				t.Fatalf("advice %q does not name %q", words, tc.name)
			}
			if tc.service == Bitcoin && slices.Contains(names, "bitcoin") {
				t.Fatalf("advice %q uses a name the System tab does not show", words)
			}
		})
	}
}
