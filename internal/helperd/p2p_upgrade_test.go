package helperd

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/p2p"
)

func TestP2PReviewedIntentAndFreshnessDispatch(t *testing.T) {
	oldUpgrade, oldStage := upgradeP2P, restageFacts
	t.Cleanup(func() { upgradeP2P, restageFacts = oldUpgrade, oldStage })
	called, staged := 0, 0
	failure := errors.New("TLS staging failed")
	restageFacts = func(verb string) error {
		if verb != helper.VerbUpgradeP2PToHybrid {
			t.Fatal("wrong staging scope")
		}
		staged++
		return failure
	}
	upgradeP2P = func(request p2p.UpgradeRequest, stage func() error, progress func(int)) error {
		called++
		if request.Address() != "203.0.113.7" {
			t.Fatal("lost reviewed address")
		}
		return stage()
	}
	for _, invalid := range []string{
		"", "null", "{}", `{"mode":"tor"}`, `{"expected_ipv4":""}`,
		`{"expected_ipv4":"10.1.2.3"}`, `{"expected_ipv4":"127.0.0.1"}`,
		`{"expected_ipv4":"0.0.0.0"}`, `{"expected_ipv4":"224.0.0.1"}`,
		`{"expected_ipv4":"255.255.255.255"}`, `{"expected_ipv4":"169.254.1.1"}`,
		`{"expected_ipv4":"2001:db8::1"}`, `{"expected_ipv4":"::ffff:203.0.113.7"}`,
		`{"expected_ipv4":"203.0.113.7\nexternalhosts=other"}`,
		`{"expected_ipv4":"203.0.113.7","mode":"tor"}`,
	} {
		if _, err := verbUpgradeP2PToHybrid(&verbCtx{}, json.RawMessage(invalid)); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
	if called != 0 {
		t.Fatal("invalid or old parameterless request reached root operation")
	}
	_, err := verbUpgradeP2PToHybrid(&verbCtx{}, raw(t, helper.UpgradeP2PParams{ExpectedIPv4: "203.0.113.7"}))
	if !errors.Is(err, failure) || called != 1 || staged != 1 {
		t.Fatalf("dispatch lost staging failure: %v", err)
	}
}
