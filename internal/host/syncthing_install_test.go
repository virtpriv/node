package host

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/component"
	"github.com/virtpriv/node/internal/config"
	"github.com/virtpriv/node/internal/helper"
)

// The client must wait for every provisioning step, credential staging and
// publication before accepting completion. Display wording is not the protocol.
func TestSyncthingProvisioningMatchesClientProgress(t *testing.T) {
	cfg := config.Default()
	cfg.SyncthingEnabled = true
	steps, _ := syncthingInstallSteps(cfg)
	if got, want := len(steps)+2, len(helper.SyncthingInstallStepNames(component.SyncthingVersion)); got != want {
		t.Fatalf("root reports %d steps, client waits for %d", got, want)
	}
}

func TestSyncthingRefusesBeforeProvisioning(t *testing.T) {
	failure := errors.New("observation failed")
	for _, cause := range []string{"config", "enabled", "residue", "unknown residue", "prerequisite"} {
		t.Run(cause, func(t *testing.T) {
			cfg := config.Default()
			cfg.SyncthingEnabled = cause == "enabled"
			before := *cfg
			ops := syncthingInstallOps{
				load: func() (*config.AppConfig, error) {
					if cause == "config" {
						return nil, failure
					}
					return cfg, nil
				},
				residue: func() (bool, error) {
					if cause == "enabled" {
						t.Fatal("enabled installation reached fresh admission")
					}
					if cause == "unknown residue" {
						return false, failure
					}
					return cause == "residue", nil
				},
				prerequisites: func(*config.AppConfig) error {
					if cause != "prerequisite" {
						t.Fatal("refused state reached prerequisite work")
					}
					return failure
				},
				steps: func(*config.AppConfig) ([]syncthingInstallStep, string) {
					t.Fatal("refused state reached provisioning")
					return nil, ""
				},
			}
			err := installSyncthing(ops, func(int) { t.Fatal("refusal reported completed work") })
			if err == nil || !reflect.DeepEqual(*cfg, before) {
				t.Fatalf("refusal changed configuration or succeeded: %v", err)
			}
			if cause != "enabled" && cause != "residue" && !errors.Is(err, failure) {
				t.Fatalf("observation failure lost: %v", err)
			}
		})
	}
}

func TestSyncthingPublishesOnlyAfterProvisioningAndBothCredentials(t *testing.T) {
	failure := errors.New("injected operation failure")
	const password = "private generated web password"
	for _, failAt := range []string{"first step", "second step", "API staging", "password staging", "save", ""} {
		t.Run(failAt, func(t *testing.T) {
			cfg := config.Default()
			cfg.Network = config.NetworkPublicSignet
			cfg.P2PMode = "hybrid"
			before := *cfg
			var applied []string
			var progress []int
			published := false
			apply := func(name string) error {
				if name == failAt {
					return failure
				}
				applied = append(applied, name)
				return nil
			}
			ops := syncthingInstallOps{
				load:          func() (*config.AppConfig, error) { return cfg, nil },
				residue:       func() (bool, error) { return false, nil },
				prerequisites: func(*config.AppConfig) error { return nil },
				steps: func(proposed *config.AppConfig) ([]syncthingInstallStep, string) {
					if !proposed.SyncthingEnabled || cfg.SyncthingEnabled {
						t.Fatal("proposal was absent or published before provisioning")
					}
					return []syncthingInstallStep{
						{name: "first step", run: func() error { return apply("first step") }},
						{name: "second step", run: func() error { return apply("second step") }},
					}, password
				},
				stage: func() error {
					if !slices.Equal(applied, []string{"first step", "second step"}) {
						t.Fatal("API staging preceded successful provisioning")
					}
					return apply("API staging")
				},
				stagePassword: func(got string) error {
					if got != password || !slices.Contains(applied, "API staging") {
						t.Fatal("credential staging lost its secret or order")
					}
					return apply("password staging")
				},
				save: func(proposed *config.AppConfig) error {
					if !slices.Equal(applied, []string{"first step", "second step", "API staging", "password staging"}) {
						t.Fatal("desired setting published before both credentials")
					}
					want := before
					want.SyncthingEnabled = true
					if !reflect.DeepEqual(*proposed, want) {
						t.Fatal("publication changed unrelated authoritative settings")
					}
					if failAt == "save" {
						return failure
					}
					published = true
					return nil
				},
			}
			err := installSyncthing(ops, func(i int) { progress = append(progress, i) })
			if (failAt == "") != published || (err != nil) != (failAt != "") {
				t.Fatalf("publication=%v error=%v", published, err)
			}
			if err != nil && (!errors.Is(err, failure) || strings.Contains(err.Error(), password)) {
				t.Fatal("failure lost its cause or exposed the password")
			}
			completed := map[string]int{"first step": 0, "second step": 1, "API staging": 2, "password staging": 2, "save": 3, "": 4}[failAt]
			if len(progress) != completed {
				t.Fatalf("failed work counted as completed: %v", progress)
			}
			for i, got := range progress {
				if got != i {
					t.Fatalf("client cannot consume progress %v", progress)
				}
			}
			if !reflect.DeepEqual(*cfg, before) {
				t.Fatal("operation changed the loaded snapshot before durable publication")
			}
		})
	}
}
