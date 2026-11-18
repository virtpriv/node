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
)

// Protocol is 2 since a plan stopped naming Bitcoin Core, LND and Syncthing.
// A plan with the number 1 is for a Bitcoin and Lightning node.
const Protocol = 2

// Versions is what a tested source runs. It is an object so that a later
// release can name a program's version beside vpn's.
type Versions struct {
	VPN string `json:"vpn"`
}

type Manifest struct {
	Protocol       int        `json:"protocol"`
	Version        string     `json:"version"`
	Platform       string     `json:"platform"`
	Summary        string     `json:"summary"`
	MinimumFreeMiB int64      `json:"minimum_free_mib"`
	Sources        []Versions `json:"sources"`
	// HostSteps names changes to the machine that the release's worker makes.
	// This release knows none, and its worker refuses a plan that names one.
	HostSteps []string `json:"host_steps"`
	// RecoveryFrom identifies exact failed VPN archive digests. A corrective
	// release must explicitly acknowledge the interrupted release as well as
	// the observed source in Sources.
	RecoveryFrom []string `json:"recovery_from,omitempty"`
	// Bridge optionally names one earlier release. A node this plan does not
	// admit is told to install that release first.
	Bridge string `json:"bridge,omitempty"`
}

var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var hostStep = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

func ValidDigest(s string) bool { return digest.MatchString(s) }

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

// Validate checks every field this release knows. Release authoring and the
// worker reach it through Decode, which also refuses a field they do not
// know. The installed side reaches it through Admission, which ignores one.
func (m Manifest) Validate() error {
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
	if len(m.Sources) == 0 || len(m.Sources) > 64 {
		return errors.New("release needs explicit tested sources")
	}
	for _, v := range m.Sources {
		if newer, err := release.Newer(v.VPN, m.Version); err != nil || !newer {
			return errors.New("target VPN must be newer than every supported source")
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
	if m.Bridge != "" {
		older, err := release.Newer(m.Bridge, m.Version)
		if err != nil || !older || (release.IsStable(m.Version) && !release.IsStable(m.Bridge)) {
			return errors.New("bridge must be an earlier release, and a stable one for a stable release")
		}
	}
	return nil
}

// Admission is how the installed side reads a signed plan. It stays in
// service for later releases, so it ignores fields it does not know. The
// target worker reads the same plan in full with Decode. Admission returns
// either a plan that admits this node, or the release to install first.
func Admission(data []byte, selected, current string, source Versions, failed string) (Manifest, string, error) {
	var m Manifest
	if len(data) > 32<<10 {
		return m, "", errors.New("release plan exceeds 32 KiB")
	}
	// These three fields keep their meaning in every protocol, so a plan the
	// installed side cannot otherwise read can still name its bridge.
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
		if head.Protocol < Protocol {
			return m, "", errors.New("this release was made for an older VPN and cannot update this node")
		}
		return m, "", errors.New("this release needs a newer VPN first, see its release notes")
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, "", err
	}
	if err := m.Validate(); err != nil {
		return m, "", err
	}
	if !slices.Contains(m.Sources, source) {
		if first := bridge(); first != "" {
			return m, first, nil
		}
	}
	return m, "", m.admit(source, failed)
}

// Admit reports whether a valid plan has a tested way from source, and from
// the failed release when an update is unfinished.
func (m Manifest) Admit(source Versions, failedRelease string) error {
	if err := m.Validate(); err != nil {
		return err
	}
	return m.admit(source, failedRelease)
}

func (m Manifest) admit(source Versions, failedRelease string) error {
	if !slices.Contains(m.Sources, source) {
		return errors.New("this installed version has no tested transition in this release")
	}
	if failedRelease != "" && !slices.Contains(m.RecoveryFrom, failedRelease) {
		return errors.New("this release cannot repair the unfinished update, select Retry first")
	}
	return nil
}
