package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func fixture() Manifest {
	return Manifest{Protocol: 2, Platform: "debian-13-amd64", Version: "0.7.1", Summary: "Test release", MinimumFreeMiB: 2048,
		Sources: []Versions{{VPN: "0.7.0"}}}
}

func TestReleaseAdmissionUsesExactTestedSource(t *testing.T) {
	m := fixture()
	source := m.Sources[0]
	if err := m.Admit(source, ""); err != nil {
		t.Fatal(err)
	}
	if m.Admit(Versions{VPN: "0.6.9"}, "") == nil {
		t.Fatal("accepted an untested installed version")
	}
	failed := strings.Repeat("b", 64)
	if m.Admit(source, failed) == nil {
		t.Fatal("ordinary update bypassed failed migration admission")
	}
	m.RecoveryFrom = []string{failed}
	if err := m.Admit(source, failed); err != nil {
		t.Fatal(err)
	}
	if m.Admit(source, strings.Repeat("c", 64)) == nil {
		t.Fatal("recovery authorization applied to a different failed release")
	}
}

func TestReleasePlanRejectsUntrustedShapesAndDowngrades(t *testing.T) {
	for name, change := range map[string]func(*Manifest){
		"later protocol":     func(m *Manifest) { m.Protocol++ },
		"earlier protocol":   func(m *Manifest) { m.Protocol-- },
		"platform":           func(m *Manifest) { m.Platform = "debian-12-amd64" },
		"VPN downgrade":      func(m *Manifest) { m.Version = "0.6.3" },
		"no source":          func(m *Manifest) { m.Sources = nil },
		"terminal injection": func(m *Manifest) { m.Summary = "test\x1b[2J" },
		"command step":       func(m *Manifest) { m.HostSteps = []string{"sh -c anything"} },
		"duplicate step":     func(m *Manifest) { m.HostSteps = []string{"later-step-v1", "later-step-v1"} },
		"recovery digest":    func(m *Manifest) { m.RecoveryFrom = []string{"abc"} },
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
	// The full reader refuses a field it does not implement. These are the
	// fields a plan held while vpn installed Bitcoin Core and LND.
	for _, field := range []string{
		`"command":"anything"`,
		`"networks":["mainnet"]`,
		`"bitcoin":{"version":"31.2","sha256":"` + strings.Repeat("a", 64) + `"}`,
		`"host_services":["lnd"]`,
	} {
		with := append(append([]byte{}, b[:len(b)-1]...), []byte(","+field+"}")...)
		if _, err := Decode(with); err == nil {
			t.Fatalf("accepted the field %s", field)
		}
	}
	// A source is an object, so a later release can name a program beside
	// vpn. The full reader refuses a name it does not implement.
	withProgram := strings.Replace(string(b), `{"vpn":"0.7.0"}`, `{"vpn":"0.7.0","tor":"0.5.0"}`, 1)
	if _, err := Decode([]byte(withProgram)); err == nil {
		t.Fatal("accepted a source with a program it does not implement")
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
			if err := m.Admit(source, ""); err != nil {
				t.Fatal(err)
			}
			other := source
			other.VPN = "0.6.9-rc.1"
			if m.Admit(other, "") == nil {
				t.Fatal("RC support bypassed exact source admission")
			}
			failed := strings.Repeat("b", 64)
			if m.Admit(source, failed) == nil {
				t.Fatal("RC support bypassed failed-update admission")
			}
			m.RecoveryFrom = []string{failed}
			if err := m.Admit(source, failed); err != nil {
				t.Fatal(err)
			}
			m.Version, m.Sources[0].VPN = versions[0], versions[1]
			if m.Validate() == nil {
				t.Fatal("RC support admitted a downgrade")
			}
		})
	}
}

// laterPlan is a plan as a later release might publish it: every field this
// build knows, plus fields it has never heard of.
func laterPlan(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	b, err := json.Marshal(fixture())
	if err != nil {
		t.Fatal(err)
	}
	var plan map[string]any
	if err := json.Unmarshal(b, &plan); err != nil {
		t.Fatal(err)
	}
	plan["snapshots"] = map[string]any{"keep": 2}
	plan["programs"] = map[string]any{"tor": map[string]any{"version": "0.5.0", "sha256": strings.Repeat("a", 64)}}
	plan["sources"].([]any)[0].(map[string]any)["tor"] = "0.5.0"
	plan["host_steps"] = []any{"later-step-v1"}
	if change != nil {
		change(plan)
	}
	b, err = json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInstalledSideAdmitsLaterPlanWithoutWeakeningAdmission(t *testing.T) {
	source := fixture().Sources[0]
	failed := strings.Repeat("b", 64)
	for _, tc := range []struct {
		name     string
		change   func(map[string]any)
		selected string
		current  string
		source   string
		failed   string
		first    string
		refused  string
	}{
		{name: "added fields are ignored"},
		{name: "untested version", source: "0.6.9", refused: "no tested transition"},
		{name: "plan for another release", selected: "0.7.2", refused: "differs from selected release"},
		{name: "known field made unsafe", change: func(p map[string]any) { p["summary"] = "x\x1b[2J" }, refused: "control characters"},
		{name: "trailing content", refused: "trailing"},
		{name: "older node is sent through the bridge", change: func(p map[string]any) { p["bridge"] = "0.7.0" },
			current: "0.6.9", source: "0.6.9", first: "0.7.0"},
		{name: "failed update needs repair, not a bridge", change: func(p map[string]any) { p["bridge"] = "0.7.0" },
			current: "0.6.9", source: "0.6.9", failed: failed, refused: "no tested transition"},
		{name: "later protocol still names its bridge", change: func(p map[string]any) { p["protocol"] = 3; p["bridge"] = "0.7.0"; delete(p, "sources") },
			current: "0.6.9", source: "0.6.9", first: "0.7.0"},
		{name: "later protocol cannot route a stable release through a candidate", change: func(p map[string]any) { p["protocol"] = 3; p["bridge"] = "0.7.0-rc.1" },
			current: "0.6.9", source: "0.6.9", refused: "newer VPN"},
		{name: "later protocol without a usable bridge", change: func(p map[string]any) { p["protocol"] = 3 }, refused: "newer VPN"},
		// The plan of a Bitcoin and Lightning release, protocol 1.
		{name: "earlier protocol", change: func(p map[string]any) {
			p["protocol"] = 1
			p["networks"] = []any{"mainnet"}
			p["sources"] = []any{map[string]any{"vpn": "0.7.0", "bitcoin": "31.2", "lnd": "0.21.8-beta", "syncthing": ""}}
		}, refused: "older VPN"},
		{name: "bridge must be older than its release", change: func(p map[string]any) { p["bridge"] = "0.7.1" }, refused: "bridge"},
		{name: "stable release cannot route through a candidate", change: func(p map[string]any) { p["bridge"] = "0.7.1-rc.1" }, refused: "bridge"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := laterPlan(t, tc.change)
			if tc.name == "trailing content" {
				data = append(data, []byte("{}")...)
			}
			selected, current, from := "0.7.1", "0.7.0", source
			if tc.selected != "" {
				selected = tc.selected
			}
			if tc.current != "" {
				current = tc.current
			}
			if tc.source != "" {
				from.VPN = tc.source
			}
			m, first, err := Admission(data, selected, current, from, tc.failed)
			if tc.refused != "" {
				if err == nil || first != "" || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("wrong refusal: first=%q err=%v", first, err)
				}
				return
			}
			if err != nil || first != tc.first {
				t.Fatalf("first=%q err=%v", first, err)
			}
			if first == "" && m.Admit(from, "") != nil {
				t.Fatal("returned a plan that does not admit this node")
			}
		})
	}
	// Release authoring and the worker keep reading the whole plan strictly.
	if _, err := Decode(laterPlan(t, nil)); err == nil {
		t.Fatal("strict reader accepted fields it does not implement")
	}
}
