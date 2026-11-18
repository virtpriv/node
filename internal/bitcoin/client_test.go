// internal/bitcoin/client_test.go

package bitcoin

import (
	"encoding/json"
	"testing"
)

func TestSignetIdentityFieldsParsing(t *testing.T) {
	raw := `{"chain":"signet","signet_challenge":"5121","blocks":1}`
	var resp blockchainInfoResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Chain != "signet" || resp.SignetChallenge != "5121" {
		t.Fatalf("identity fields: %+v", resp)
	}
}
