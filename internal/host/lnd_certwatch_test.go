package host

import (
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/paths"
)

// The certificate watch is what keeps the staged TLS cert copy
// current when LND regenerates the cert on its own
// (tlsautorefresh at startup); pin the units' load-bearing
// lines.
func TestLNDCertWatchUnits(t *testing.T) {
	pathUnit, serviceUnit := lndCertWatchUnits()

	// The path unit watches LND's OWN certificate file (the
	// source), not the staged copy, and triggers the stage
	// service.
	assertSingleUnitDirective(t, pathUnit, "Path", "PathChanged", paths.LNDTLSCert)
	assertSingleUnitDirective(t, pathUnit, "Path", "Unit", paths.LNDCertStageServiceName)
	assertSingleUnitDirective(t, pathUnit, "Install", "WantedBy", "multi-user.target")

	// The service is a oneshot running the installed binary's
	// stage command as root (no User= line).
	assertSingleUnitDirective(t, serviceUnit, "Service", "Type", "oneshot")
	assertSingleUnitDirective(t, serviceUnit, "Service", "ExecStart", paths.BinaryPath+" stage-lnd-cert")
	if strings.Contains(serviceUnit, "User=") {
		t.Error("stage service must run as root " +
			"(it writes the root-owned board)")
	}
}

// Check the generated directive's section, full value and uniqueness so an
// ignored setting or a later override cannot satisfy a security assertion.
func assertSingleUnitDirective(t *testing.T, unit, section, key, want string) {
	t.Helper()
	currentSection, count := "", 0
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		count++
		if currentSection != section || strings.TrimSpace(value) != want {
			t.Errorf("[%s] %s=%s, want [%s] %s=%s", currentSection, key, value, section, key, want)
		}
	}
	if count != 1 {
		t.Errorf("%s appears %d times, want exactly once", key, count)
	}
}
