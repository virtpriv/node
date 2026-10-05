package host

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/p2p"
)

func TestHybridTransitionRefusalAndCompletionBoundary(t *testing.T) {
	failure := errors.New("injected failure")
	request, err := p2p.NewUpgradeRequest("203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, mode, address, fail string
		zero                      bool
		completed                 int
	}{
		{name: "success", completed: 7},
		{name: "unreviewed", zero: true},
		{name: "already Hybrid", mode: "hybrid"},
		{name: "configuration unavailable", fail: "load"},
		{name: "address unavailable", fail: "address"},
		{name: "reviewed address changed", address: "203.0.113.8"},
		{name: "private observation", address: "10.1.2.3"},
		{name: "inactive firewall", fail: "firewall"},
		{name: "configuration write failed", fail: "configure", completed: 1},
		{name: "rules failed", fail: "allow", completed: 2},
		{name: "restart failed", fail: "restart", completed: 3},
		{name: "SAN absent", fail: "tls", completed: 4},
		{name: "staging failed", fail: "stage", completed: 5},
		{name: "publication failed", fail: "save", completed: 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Network, cfg.AutoUnlock, cfg.SyncthingEnabled = "public-signet", true, true
			if tc.mode != "" {
				cfg.P2PMode = tc.mode
			}
			before := *cfg
			var events []int
			var changes []string
			action := func(name string) error {
				changes = append(changes, name)
				if tc.fail == name {
					return failure
				}
				return nil
			}
			ops := p2pUpgradeOps{
				load: func() (*config.AppConfig, error) {
					if tc.zero {
						t.Fatal("unreviewed request reached host state")
					}
					if tc.fail == "load" {
						return nil, failure
					}
					return cfg, nil
				},
				address: func(context.Context) (string, error) {
					if tc.mode != "" {
						t.Fatal("address read before mode refusal")
					}
					if tc.fail == "address" {
						return "", failure
					}
					if tc.address != "" {
						return tc.address, nil
					}
					return request.Address(), nil
				},
				firewall: func() error {
					if tc.fail == "firewall" {
						return failure
					}
					return nil
				},
				configure: func(proposed *config.AppConfig, address string) error {
					want := before
					want.P2PMode = "hybrid"
					if *proposed != want || address != request.Address() {
						t.Fatal("unreviewed setting or address")
					}
					return action("configure")
				},
				allow:   func() error { return action("allow") },
				restart: func() error { return action("restart") },
				verifyTLS: func(address string) error {
					if address != request.Address() {
						t.Fatal("verified a different SAN")
					}
					return action("tls")
				},
				stage: func() error { return action("stage") },
				save: func(proposed *config.AppConfig) error {
					want := before
					want.P2PMode = "hybrid"
					if *proposed != want {
						t.Fatal("changed an unrelated desired setting")
					}
					return action("save")
				},
			}
			r := request
			if tc.zero {
				r = p2p.UpgradeRequest{}
			}
			err := upgradeP2PToHybrid(r, ops, func(i int) { events = append(events, i) })
			if (err == nil) != (tc.name == "success") {
				t.Fatalf("result: %v", err)
			}
			if tc.fail != "" && !errors.Is(err, failure) {
				t.Fatalf("lost failure: %v", err)
			}
			if *cfg != before {
				t.Fatal("changed loaded configuration before publication")
			}
			if len(events) != tc.completed {
				t.Fatalf("completed %v, expected %d", events, tc.completed)
			}
			for i, event := range events {
				if i != event {
					t.Fatal("nonsequential progress")
				}
			}
			if tc.completed == 0 && len(changes) != 0 {
				t.Fatalf("mutated before refusal: %v", changes)
			}
			if tc.completed > 0 {
				want := []string{"configure", "allow", "restart", "tls", "stage", "save"}
				n := min(tc.completed, len(want))
				if !reflect.DeepEqual(changes, want[:n]) {
					t.Fatalf("crossed failure boundary: %v", changes)
				}
			}
			if err == nil && len(events) != len(helper.UpgradeP2PToHybridStepNames()) {
				t.Fatal("helper progress protocol cannot reach its success terminator")
			}
		})
	}
}
