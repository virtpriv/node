// Package protocol defines the signed, versioned managed-release contract.
// It contains no host execution or caller-selected commands and paths.
package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"

	"github.com/virtpriv/node/internal/release"
	"golang.org/x/mod/semver"
)

const Protocol = 1

type Component string

const (
	Bitcoin   Component = "bitcoin"
	LND       Component = "lnd"
	Syncthing Component = "syncthing"
)

var Components = []Component{Bitcoin, LND, Syncthing}

// Versions is an exact tested combination, not independently combinable ranges.
// Syncthing is empty only when the optional component is absent.
type Versions struct {
	VPN       string `json:"vpn"`
	Bitcoin   string `json:"bitcoin"`
	LND       string `json:"lnd"`
	Syncthing string `json:"syncthing"`
}

func (v Versions) Get(c Component) string {
	switch c {
	case Bitcoin:
		return v.Bitcoin
	case LND:
		return v.LND
	case Syncthing:
		return v.Syncthing
	}
	return ""
}

// Artifact hashes bind upstream downloads to the maintainer's tested bytes.
// Upstream signatures are also checked before any service is stopped.
type Artifact struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

type Manifest struct {
	Protocol       int         `json:"protocol"`
	Version        string      `json:"version"`
	Platform       string      `json:"platform"`
	Summary        string      `json:"summary"`
	MinimumFreeMiB int64       `json:"minimum_free_mib"`
	Sources        []Versions  `json:"sources"`
	Networks       []string    `json:"networks"`
	Bitcoin        Artifact    `json:"bitcoin"`
	LND            Artifact    `json:"lnd"`
	Syncthing      Artifact    `json:"syncthing"`
	HostSteps      []string    `json:"host_steps"`
	HostServices   []Component `json:"host_services,omitempty"`
	// RecoveryFrom identifies exact failed VPN archive digests. A corrective
	// release must explicitly acknowledge the interrupted release as well as
	// the observed executable combination in Sources.
	RecoveryFrom []string `json:"recovery_from,omitempty"`
	// Bridge optionally names one earlier release. A node this plan does not
	// admit is told to install that release first.
	Bridge string `json:"bridge,omitempty"`
}

func (m Manifest) Artifact(c Component) Artifact {
	switch c {
	case Bitcoin:
		return m.Bitcoin
	case LND:
		return m.LND
	case Syncthing:
		return m.Syncthing
	}
	return Artifact{}
}

var stable = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var core = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(\.(0|[1-9][0-9]*))?$`)
var lnd = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-beta$`)
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var hostStep = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

func ValidDigest(s string) bool { return digest.MatchString(s) }
func ValidVersion(c Component, s string) bool {
	switch c {
	case Bitcoin:
		return core.MatchString(s)
	case LND:
		return lnd.MatchString(s)
	case Syncthing:
		return stable.MatchString(s)
	}
	return false
}

func Decode(data []byte) (Manifest, error) {
	var m Manifest
	if len(data) > 32<<10 {
		return m, errors.New("release plan exceeds 32 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return m, errors.New("release plan has trailing content")
	}
	return m, m.Validate()
}

// Validate is the full check used by release authoring and by the worker.
func (m Manifest) Validate() error { return m.validate(true) }

// validate with strict false is the installed helper's check. It accepts
// network and service names added by later releases: the helper needs only its
// own network, and the newer worker checks every name it acts on.
func (m Manifest) validate(strict bool) error {
	if m.Protocol != Protocol || m.Platform != "debian-13-amd64" || !release.ValidVersion(m.Version) {
		return errors.New("unsupported release protocol, platform or version")
	}
	if len(m.Summary) == 0 || len(m.Summary) > 500 {
		return errors.New("release summary must contain 1 to 500 bytes")
	}
	if m.MinimumFreeMiB < 1024 || m.MinimumFreeMiB > 1<<30 {
		return errors.New("release must declare at least 1024 MiB of free space")
	}
	for _, r := range m.Summary {
		if r < 32 || r == 127 {
			return errors.New("release summary contains control characters")
		}
	}
	if len(m.Sources) == 0 || len(m.Sources) > 64 || len(m.Networks) == 0 {
		return errors.New("release needs explicit tested sources and networks")
	}
	for _, n := range m.Networks {
		if strict && n != "mainnet" && n != "testnet4" && n != "public-signet" {
			return fmt.Errorf("unknown network %q", n)
		}
	}
	for _, c := range Components {
		a := m.Artifact(c)
		if !ValidVersion(c, a.Version) || !ValidDigest(a.SHA256) {
			return fmt.Errorf("invalid %s artifact", c)
		}
	}
	for _, v := range m.Sources {
		if newer, err := release.Newer(v.VPN, m.Version); err != nil || !newer {
			return errors.New("target VPN must be newer than every supported source")
		}
		for _, c := range Components {
			if c == Syncthing && v.Syncthing == "" {
				continue
			}
			if !ValidVersion(c, v.Get(c)) || semver.Compare("v"+m.Artifact(c).Version, "v"+v.Get(c)) < 0 {
				return fmt.Errorf("unsupported source or downgrade for %s", c)
			}
		}
	}
	seen := map[string]bool{}
	for _, step := range m.HostSteps {
		if !hostStep.MatchString(step) || seen[step] {
			return fmt.Errorf("invalid or duplicate host step %q", step)
		}
		seen[step] = true
	}
	for _, h := range m.RecoveryFrom {
		if !ValidDigest(h) {
			return errors.New("invalid recovery release digest")
		}
	}
	for _, c := range m.HostServices {
		if strict && !slices.Contains(Components, c) {
			return errors.New("unknown host-step service")
		}
	}
	if m.Bridge != "" {
		older, err := release.Newer(m.Bridge, m.Version)
		if err != nil || !older || (release.IsStable(m.Version) && !release.IsStable(m.Bridge)) {
			return errors.New("bridge must be an earlier release, and a stable one for a stable release")
		}
	}
	return nil
}

// Admission is how an installed helper reads a signed plan. The helper stays
// in service for later releases, so it ignores fields it does not know; the
// target worker reads the same plan in full with Decode. Admission returns
// either a plan that admits this node, or the release to install first.
func Admission(data []byte, selected, current string, source Versions, network, failed string) (Manifest, string, error) {
	var m Manifest
	if len(data) > 32<<10 {
		return m, "", errors.New("release plan exceeds 32 KiB")
	}
	// These three fields keep their meaning in every protocol, so a plan this
	// helper cannot otherwise read can still name its bridge.
	var head struct {
		Protocol int    `json:"protocol"`
		Version  string `json:"version"`
		Bridge   string `json:"bridge"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(&head); err != nil {
		return m, "", err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return m, "", errors.New("release plan has trailing content")
	}
	if head.Version != selected {
		return m, "", errors.New("signed plan version differs from selected release")
	}
	// A failed update needs its repair release, never a detour.
	bridge := func() string {
		if failed != "" {
			return ""
		}
		if newer, err := release.Newer(current, head.Bridge); err != nil || !newer {
			return ""
		}
		if older, err := release.Newer(head.Bridge, head.Version); err != nil || !older {
			return ""
		}
		if release.IsStable(head.Version) && !release.IsStable(head.Bridge) {
			return ""
		}
		return head.Bridge
	}
	if head.Protocol != Protocol {
		if first := bridge(); first != "" {
			return m, first, nil
		}
		return m, "", errors.New("this release needs a newer VPN first, see its release notes")
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, "", err
	}
	if err := m.validate(false); err != nil {
		return m, "", err
	}
	if !slices.Contains(m.Sources, source) {
		if first := bridge(); first != "" {
			return m, first, nil
		}
	}
	return m, "", m.admit(source, network, failed)
}

func (m Manifest) Admit(source Versions, network, failedRelease string) error {
	if err := m.Validate(); err != nil {
		return err
	}
	return m.admit(source, network, failedRelease)
}

// AdmitInstalled is Admit for a plan the installed helper read with Admission.
func (m Manifest) AdmitInstalled(source Versions, network, failedRelease string) error {
	if err := m.validate(false); err != nil {
		return err
	}
	return m.admit(source, network, failedRelease)
}

func (m Manifest) admit(source Versions, network, failedRelease string) error {
	if !slices.Contains(m.Sources, source) || !slices.Contains(m.Networks, network) {
		return errors.New("this installed combination and network have no tested transition in this release")
	}
	if failedRelease != "" && !slices.Contains(m.RecoveryFrom, failedRelease) {
		return errors.New("release does not declare recovery from the unfinished update")
	}
	return nil
}

func (m Manifest) Target(source Versions) Versions {
	v := Versions{VPN: m.Version, Bitcoin: m.Bitcoin.Version, LND: m.LND.Version}
	if source.Syncthing != "" {
		v.Syncthing = m.Syncthing.Version
	}
	return v
}

// Affected includes unchanged dependencies that must be stopped and restarted.
func (m Manifest) Affected(source Versions) []Component {
	var out []Component
	coreChanged := source.Bitcoin != m.Bitcoin.Version || slices.Contains(m.HostServices, Bitcoin)
	if coreChanged {
		out = append(out, Bitcoin)
	}
	if coreChanged || source.LND != m.LND.Version || slices.Contains(m.HostServices, LND) || slices.Contains(m.HostSteps, "lnd-service-v1") {
		out = append(out, LND)
	}
	if source.Syncthing != "" && (source.Syncthing != m.Syncthing.Version || slices.Contains(m.HostServices, Syncthing)) {
		out = append(out, Syncthing)
	}
	return out
}
