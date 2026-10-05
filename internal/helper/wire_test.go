// internal/helper/wire_test.go

package helper

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/virtpriv/node/internal/autounlock"
)

func TestAutoUnlockResultJSONContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result autounlock.Result
		wire   string
	}{
		{"enabled", autounlock.Result{Outcome: autounlock.Enabled},
			`{"outcome":"enabled"}`},
		{"repair", autounlock.Result{Outcome: autounlock.RepairRequired, FailedStep: "restore normal restart policy"},
			`{"outcome":"repair_required","failed_step":"restore normal restart policy"}`},
		{"retry", autounlock.Result{Outcome: autounlock.VerificationFailed, Detail: "LND is locked"},
			`{"outcome":"verification_failed","detail":"LND is locked"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.result)
			if err != nil {
				t.Fatal(err)
			}
			var got, want map[string]string
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.wire), &want); err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(got, want) {
				t.Fatalf("encoded fields = %s, want %s", data, tc.wire)
			}
			var decoded autounlock.Result
			if err := json.Unmarshal([]byte(tc.wire), &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded != tc.result {
				t.Fatalf("decoded result = %+v, want %+v", decoded, tc.result)
			}
		})
	}
}
