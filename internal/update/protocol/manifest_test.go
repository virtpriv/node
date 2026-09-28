package protocol

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func fixture() Manifest {
	h := strings.Repeat("a", 64)
	return Manifest{Protocol: 1, Platform: "debian-13-amd64", Version: "0.7.1", Summary: "Test release", MinimumFreeMiB: 2048,
		Sources: []Versions{{VPN: "0.7.0", Bitcoin: "29.2", LND: "0.21.1-beta", Syncthing: "2.1.4"}}, Networks: []string{"public-signet"},
		Bitcoin: Artifact{"29.3", h}, LND: Artifact{"0.21.2-beta", h}, Syncthing: Artifact{"2.1.5", h}, HostSteps: []string{"lnd-service-v1"}}
}

func TestReleaseAdmissionUsesExactTestedCombination(t *testing.T) {
	m := fixture()
	source := m.Sources[0]
	if err := m.Admit(source, "public-signet", ""); err != nil {
		t.Fatal(err)
	}
	other := source
	other.Bitcoin = "29.1"
	if m.Admit(other, "public-signet", "") == nil {
		t.Fatal("accepted an untested installed combination")
	}
	if m.Admit(source, "mainnet", "") == nil {
		t.Fatal("promoted a signet-only release to mainnet")
	}
	failed := strings.Repeat("b", 64)
	if m.Admit(source, "public-signet", failed) == nil {
		t.Fatal("ordinary update bypassed failed migration admission")
	}
	m.RecoveryFrom = []string{failed}
	if err := m.Admit(source, "public-signet", failed); err != nil {
		t.Fatal(err)
	}
	if m.Admit(source, "public-signet", strings.Repeat("c", 64)) == nil {
		t.Fatal("recovery authorization applied to a different failed release")
	}
	// Individually supported versions do not authorize an untested mixture.
	other.LND = "0.21.0-beta"
	m.Sources = append(m.Sources, other)
	mixed := source
	mixed.Bitcoin = other.Bitcoin
	if m.Admit(mixed, "public-signet", "") == nil {
		t.Fatal("combined versions from separate tested source configurations")
	}
}

func TestReleasePlanRejectsUntrustedShapesAndDowngrades(t *testing.T) {
	for name, change := range map[string]func(*Manifest){
		"protocol":            func(m *Manifest) { m.Protocol++ },
		"platform":            func(m *Manifest) { m.Platform = "debian-12-amd64" },
		"VPN downgrade":       func(m *Manifest) { m.Version = "0.6.3" },
		"Core downgrade":      func(m *Manifest) { m.Bitcoin.Version = "28.0" },
		"LND downgrade":       func(m *Manifest) { m.LND.Version = "0.20.0-beta" },
		"Syncthing downgrade": func(m *Manifest) { m.Syncthing.Version = "2.0.0" },
		"path version":        func(m *Manifest) { m.LND.Version = "../../lnd" },
		"unknown digest":      func(m *Manifest) { m.Bitcoin.SHA256 = "" },
		"terminal injection":  func(m *Manifest) { m.Summary = "test\x1b[2J" },
		"command step":        func(m *Manifest) { m.HostSteps = []string{"sh -c anything"} },
		"duplicate step":      func(m *Manifest) { m.HostSteps = append(m.HostSteps, m.HostSteps[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			m := fixture()
			change(&m)
			if m.Validate() == nil {
				t.Fatal("accepted unsafe release")
			}
		})
	}
	b, _ := json.Marshal(fixture())
	if _, err := Decode(b); err != nil {
		t.Fatal("rejected a valid release plan", err)
	}
	if _, err := Decode(append(b, []byte("{}")...)); err == nil {
		t.Fatal("accepted trailing JSON")
	}
	b = append(b[:len(b)-1], []byte(`,"command":"anything"}`)...)
	if _, err := Decode(b); err == nil {
		t.Fatal("accepted unknown field")
	}
}

func TestDependencyOrderAndOptionalSyncthing(t *testing.T) {
	m := fixture()
	source := m.Sources[0]
	if got := m.Affected(source); !slices.Equal(got, []Component{Bitcoin, LND, Syncthing}) {
		t.Fatal(got)
	}
	source.Syncthing = ""
	m.HostSteps = nil
	source.LND = m.LND.Version
	if got := m.Affected(source); !slices.Equal(got, []Component{Bitcoin, LND}) {
		t.Fatal("Core must restart its LND dependent, without installing Syncthing", got)
	}
	if m.Target(source).Syncthing != "" {
		t.Fatal("enabled an absent addon")
	}
	source.Bitcoin = m.Bitcoin.Version
	if len(m.Affected(source)) != 0 {
		t.Fatal("VPN-only release needlessly restarts components")
	}
	for _, tc := range []struct {
		name      string
		steps     []string
		services  []Component
		syncthing string
		want      []Component
	}{
		{"LND unit only", []string{"lnd-service-v1"}, nil, "", []Component{LND}},
		{"Core host change includes LND", nil, []Component{Bitcoin}, "", []Component{Bitcoin, LND}},
		{"LND host change", nil, []Component{LND}, "", []Component{LND}},
		{"Syncthing host change", nil, []Component{Syncthing}, m.Syncthing.Version, []Component{Syncthing}},
		{"host change cannot enable absent addon", nil, []Component{Syncthing}, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.HostSteps, m.HostServices = tc.steps, tc.services
			source.Syncthing = tc.syncthing
			if got := m.Affected(source); !slices.Equal(got, tc.want) {
				t.Fatalf("unchanged binaries: affected=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestCandidatePlansKeepExactSourceAndRecoveryAdmission(t *testing.T) {
	for _, versions := range [][2]string{
		{"0.7.0-rc.1", "0.7.0-rc.2"},
		{"0.7.0", "0.7.1-rc.1"},
		{"0.7.1-rc.2", "0.7.1"},
	} {
		t.Run(versions[0]+"_to_"+versions[1], func(t *testing.T) {
			m := fixture()
			m.Sources[0].VPN, m.Version = versions[0], versions[1]
			data, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			m, err = Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			source := m.Sources[0]
			if err := m.Admit(source, "public-signet", ""); err != nil {
				t.Fatal(err)
			}
			other := source
			other.VPN = "0.6.9-rc.1"
			if m.Admit(other, "public-signet", "") == nil {
				t.Fatal("RC support bypassed exact source admission")
			}
			failed := strings.Repeat("b", 64)
			if m.Admit(source, "public-signet", failed) == nil {
				t.Fatal("RC support bypassed failed-update admission")
			}
			m.RecoveryFrom = []string{failed}
			if err := m.Admit(source, "public-signet", failed); err != nil {
				t.Fatal(err)
			}
			m.Version, m.Sources[0].VPN = versions[0], versions[1]
			if m.Validate() == nil {
				t.Fatal("RC support admitted a downgrade")
			}
		})
	}
}
