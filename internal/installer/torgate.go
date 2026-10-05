// internal/installer/torgate.go
//
// Install-path Tor hard-gate (IA-2-K). Runs as its own install step
// between "Installing Tor" and "Configuring apt for Tor", so it must
// succeed before ANY Tor-dependent network operation: apt over the
// socks5h proxy (host.EnsureGPG onward) and every DownloadRequireTor site.
//
// Two layers:
//
//  1. HARD GATE — no external dependency. Wait for Tor to report
//     bootstrap PROGRESS=100 on its own control port (127.0.0.1:9051,
//     cookie auth). Local, deterministic; the install step fails on
//     timeout. This is the postcondition for host.EnableAndRestartTor: rc=0 means
//     "the unit started", PROGRESS=100 means "Tor is routing".
//
//  2. TRIPWIRE — best-effort external probe. torsocks-fetch
//     check.torproject.org/api/ip:
//       IsTor:true   -> routing confirmed end-to-end; proceed.
//       IsTor:false  -> PROVEN clearnet exit (torsocks present but not
//                       intercepting, e.g. broken LD_PRELOAD lib) ->
//                       hard fail. This is the only detector for a
//                       fail-open torsocks; presence checks cannot
//                       see it.
//       unreachable  -> warn and proceed. Bootstrap is already
//                       confirmed locally, and torsocks fail-closed
//                       behavior ([LIVE], Gate 0) backstops the leak
//                       half. Install success never depends on the
//                       third-party endpoint being up.

package installer

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/system"
)

// Tor runtime constants. These are Tor-owned values, not Go logic
// paths (same rationale as the HiddenServiceDir strings in tor.go):
// the control port matches host.BuildTorConfig. The cookie path is Debian's
// packaged Tor runtime layout.
const (
	torControlAddr = "127.0.0.1:9051"
	torCookiePath  = "/run/tor/control.authcookie"

	// Stall clock and ceiling (operator ruling at the commit-6
	// live run, replacing the original flat 90s deadline). The
	// flat deadline dated from when the bootstrap script gave
	// Tor a warm-up minute plus an SSH round trip before this
	// gate ever ran; with the script absorbed, the gate fires
	// seconds after Tor first starts, and a healthy slow box
	// was failed at 65% while still advancing. Now: fail only
	// when bootstrap PROGRESS has not moved for the stall
	// window; the ceiling bounds the step against pathological
	// crawl (a bounded install step must end).
	torStallWindow      = 90 * time.Second
	torBootstrapCeiling = 10 * time.Minute
	torBootstrapPoll    = 3 * time.Second

	torProbeURL      = "https://check.torproject.org/api/ip"
	torProbeAttempts = 2
)

// verifyTorRouting is the "Verifying Tor routing" install step.
func verifyTorRouting() error {
	if _, err := exec.LookPath("torsocks"); err != nil {
		return fmt.Errorf("torsocks not found — refusing to proceed: " +
			"all downloads must route through Tor")
	}

	if err := waitForTorBootstrap(
		torStallWindow, torBootstrapCeiling); err != nil {
		return err
	}
	logger.Install("Tor bootstrap 100%% CONFIRMED (via control port)")

	return runTorExitProbe()
}

// waitForTorBootstrap polls Tor's control port until it reports
// bootstrap PROGRESS=100, failing on STALL (no progress change
// for the stall window) or on the absolute ceiling. Any change
// in reported progress resets the stall clock — including a
// drop, because Tor resets its counter to zero when it restarts
// mid-bootstrap, and rebootstrapping is activity, not a stall.
// Poll errors are retried silently within the same clocks —
// immediately after host.EnableAndRestartTor the control port may not be
// listening yet.
func waitForTorBootstrap(
	stallWindow, ceiling time.Duration,
) error {
	start := time.Now()
	lastAdvance := start
	lastProgress := -1
	var lastErr error

	for {
		progress, err := queryTorBootstrapProgress()
		if err == nil {
			if progress >= 100 {
				return nil
			}
			if progress != lastProgress {
				logger.Install("Tor bootstrapping: %d%%", progress)
				lastProgress = progress
				lastAdvance = time.Now()
			}
		} else {
			lastErr = err
		}

		if !keepWaitingForTor(time.Now(), start, lastAdvance,
			stallWindow, ceiling) {
			break
		}
		time.Sleep(torBootstrapPoll)
	}

	if lastProgress < 0 && lastErr != nil {
		return fmt.Errorf("Tor bootstrap check failed within %s "+
			"(control port never answered: %v — is ControlPort "+
			"9051 enabled in torrc?) — check: systemctl status "+
			"tor; journalctl -u tor", stallWindow, lastErr)
	}
	if time.Since(start) >= ceiling {
		return fmt.Errorf("Tor did not finish bootstrapping "+
			"within %s (last progress %d%%) — check: systemctl "+
			"status tor; journalctl -u tor", ceiling, lastProgress)
	}
	return fmt.Errorf("Tor bootstrap STALLED at %d%% for %s — "+
		"check: systemctl status tor; journalctl -u tor",
		lastProgress, stallWindow)
}

// keepWaitingForTor is the gate's clock rule as a pure function
// (unit-tested): keep waiting while the stall window has not
// elapsed since the last progress change AND the absolute
// ceiling has not elapsed since the step started.
func keepWaitingForTor(
	now, start, lastAdvance time.Time,
	stallWindow, ceiling time.Duration,
) bool {
	if now.Sub(lastAdvance) >= stallWindow {
		return false
	}
	if now.Sub(start) >= ceiling {
		return false
	}
	return true
}

// queryTorBootstrapProgress performs one control-port round trip:
// AUTHENTICATE with the cookie, GETINFO status/bootstrap-phase,
// parse PROGRESS. The cookie is re-read on every attempt because Tor
// regenerates it on restart.
func queryTorBootstrapProgress() (int, error) {
	// The installer runs as root; daemon access to this cookie is
	// granted only inside the bitcoind and lnd units through
	// SupplementaryGroups=debian-tor. Read it directly through the
	// root-requiring wrapper here.
	cookie, err := system.ReadFileRoot(torCookiePath)
	if err != nil {
		return 0, fmt.Errorf("read control cookie: %w", err)
	}

	conn, err := net.DialTimeout("tcp", torControlAddr, 5*time.Second)
	if err != nil {
		return 0, fmt.Errorf("dial control port: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	r := bufio.NewReader(conn)

	fmt.Fprintf(conn, "AUTHENTICATE %s\r\n", hex.EncodeToString(cookie))
	line, err := r.ReadString('\n')
	if err != nil {
		return 0, fmt.Errorf("read auth reply: %w", err)
	}
	if !strings.HasPrefix(line, "250") {
		return 0, fmt.Errorf("control auth refused: %s",
			strings.TrimSpace(line))
	}

	fmt.Fprintf(conn, "GETINFO status/bootstrap-phase\r\n")
	progress := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return 0, fmt.Errorf("read GETINFO reply: %w", err)
		}
		if p, ok := parseBootstrapProgress(line); ok {
			progress = p
		}
		if strings.HasPrefix(line, "250 ") ||
			strings.HasPrefix(line, "5") {
			break
		}
	}
	fmt.Fprintf(conn, "QUIT\r\n")

	if progress < 0 {
		return 0, fmt.Errorf("no PROGRESS field in bootstrap-phase reply")
	}
	return progress, nil
}

// parseBootstrapProgress extracts the PROGRESS=<n> value from a
// control-port status line, e.g.:
//
//	250-status/bootstrap-phase=NOTICE BOOTSTRAP PROGRESS=100 TAG=done SUMMARY="Done"
//
// Pure function — unit-tested.
func parseBootstrapProgress(line string) (int, bool) {
	const key = "PROGRESS="
	i := strings.Index(line, key)
	if i < 0 {
		return 0, false
	}
	rest := line[i+len(key):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0, false
	}
	return n, true
}

// torProbeVerdict classifies one tripwire probe result. Pure
// function — unit-tested.
type torProbeResult int

const (
	probeTor         torProbeResult = iota // IsTor:true — confirmed
	probeClearnet                          // IsTor:false — PROVEN leak
	probeUnreachable                       // error / garbage — unknown
)

func torProbeVerdict(output string, err error) torProbeResult {
	if err != nil {
		return probeUnreachable
	}
	if strings.Contains(output, `"IsTor":true`) {
		return probeTor
	}
	if strings.Contains(output, `"IsTor":false`) {
		return probeClearnet
	}
	return probeUnreachable
}

// runTorExitProbe runs the best-effort external tripwire. Only a
// proven clearnet exit (IsTor:false) fails the step; an unreachable
// endpoint warns and proceeds.
func runTorExitProbe() error {
	for attempt := 0; attempt < torProbeAttempts; attempt++ {
		output, err := system.RunOutputWithTimeout(15*time.Second,
			"torsocks", "curl", "-s", "--max-time", "10",
			torProbeURL)
		switch torProbeVerdict(output, err) {
		case probeTor:
			logger.Install(
				"Tor routing CONFIRMED via check.torproject.org")
			return nil
		case probeClearnet:
			return fmt.Errorf("CLEARNET LEAK DETECTED: request via " +
				"torsocks exited on clearnet (IsTor:false) — torsocks " +
				"is present but not intercepting; refusing to proceed")
		case probeUnreachable:
			if attempt < torProbeAttempts-1 {
				time.Sleep(5 * time.Second)
			}
		}
	}
	logger.Install("WARNING: Tor exit probe unreachable — proceeding on " +
		"local bootstrap confirmation (torsocks fail-closed backstops); " +
		"verify later with: torsocks curl -s " + torProbeURL)
	return nil
}
