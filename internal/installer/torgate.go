// internal/installer/torgate.go
//
// The Tor check, the install step "Verifying the Tor connection". It comes
// after "Setting up Tor and your onion addresses" and before "Sending apt
// package downloads through Tor". As a gate it runs again on every pass, so
// no download through Tor runs in a pass whose Tor was not checked. The
// package upgrade step waits for Tor again with waitForTorBootstrap, because
// an upgrade can restart Tor.
//
// Two layers:
//
//  1. The gate, with no outside dependency. Wait until Tor reports
//     bootstrap PROGRESS=100 on its own control port (127.0.0.1:9051,
//     cookie authentication). A started unit only says Tor runs.
//     PROGRESS=100 says Tor can route.
//
//  2. A best-effort outside check: check.torproject.org/api/ip fetched
//     through torsocks.
//       IsTor:true   routing confirmed end to end, continue.
//       IsTor:false  a request through torsocks left on clearnet, so
//                    torsocks is present but not intercepting (a broken
//                    preload library, for example). The step fails. This
//                    is the only check that sees a torsocks that fails
//                    open.
//       unreachable  warn and continue. Tor reported itself ready, and
//                    torsocks refuses to connect when it cannot reach
//                    Tor. The install never depends on the outside site.

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

// Tor's own values, not Go logic paths, like the HiddenServiceDir strings
// in tor.go. The control port matches host.BuildTorConfig. The cookie path
// is the one set by Debian's tor-service-defaults-torrc.
const (
	torControlAddr = "127.0.0.1:9051"
	torCookiePath  = "/run/tor/control.authcookie"

	// Fail only when Tor's bootstrap progress has not moved for the
	// stall window. The gate runs seconds after Tor first starts, so a
	// flat deadline would fail a slow machine that is still advancing.
	// The ceiling ends a start that only crawls.
	torStallWindow      = 90 * time.Second
	torBootstrapCeiling = 10 * time.Minute
	torBootstrapPoll    = 3 * time.Second

	torProbeURL      = "https://check.torproject.org/api/ip"
	torProbeAttempts = 2
)

// verifyTorRouting is the "Verifying the Tor connection" install step.
func verifyTorRouting() error {
	if _, err := exec.LookPath("torsocks"); err != nil {
		return fmt.Errorf("torsocks not found, refusing to continue: " +
			"every download must go through Tor")
	}

	if err := waitForTorBootstrap(
		torStallWindow, torBootstrapCeiling); err != nil {
		return err
	}
	logger.Install("Tor bootstrap 100%% CONFIRMED (via control port)")

	return runTorExitProbe()
}

// waitForTorBootstrap polls Tor's control port until it reports
// bootstrap PROGRESS=100. It fails when the progress has not changed for
// the stall window, or at the ceiling. Any change resets the stall clock,
// a drop included, because Tor counts from zero again when it restarts
// during bootstrap, and that is activity, not a stall. Poll errors are
// retried silently within the same clocks, since right after
// host.EnableAndRestartTor the control port may not listen yet. The
// messages name tor@default, the real Tor. Debian's tor.service only runs
// /bin/true.
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
		return fmt.Errorf("Tor's control port did not answer within %s: %v. "+
			"Check: systemctl status tor@default and journalctl -u tor@default",
			stallWindow, lastErr)
	}
	if time.Since(start) >= ceiling {
		return fmt.Errorf("Tor did not finish bootstrapping within %s "+
			"(last progress %d%%). Check: systemctl status tor@default "+
			"and journalctl -u tor@default", ceiling, lastProgress)
	}
	return fmt.Errorf("Tor bootstrap STALLED at %d%% for %s. "+
		"Check: systemctl status tor@default and journalctl -u tor@default",
		lastProgress, stallWindow)
}

// keepWaitingForTor is the gate's clock rule as a pure function, tested on
// its own: keep waiting while the stall window has not passed since the
// last progress change and the ceiling has not passed since the step
// started.
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
	// The installer runs as root. Daemons reach this cookie only inside
	// the bitcoind and lnd units, through SupplementaryGroups=debian-tor.
	// Here it is read through the wrapper that requires root.
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
// A pure function, tested on its own.
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

// torProbeResult is what one outside check showed. torProbeVerdict, a
// pure function tested on its own, decides it.
type torProbeResult int

const (
	probeTor         torProbeResult = iota // IsTor:true, confirmed
	probeClearnet                          // IsTor:false, a proven leak
	probeUnreachable                       // an error or garbage, unknown
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

// runTorExitProbe runs the best-effort outside check. Only a proven
// clearnet exit (IsTor:false) fails the step. An unreachable site warns
// and the install continues.
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
			return fmt.Errorf("CLEARNET LEAK DETECTED: a request " +
				"through torsocks left on clearnet (IsTor:false), so " +
				"torsocks is not intercepting. Refusing to continue")
		case probeUnreachable:
			if attempt < torProbeAttempts-1 {
				time.Sleep(5 * time.Second)
			}
		}
	}
	logger.Install("WARNING: the outside Tor check could not be reached. " +
		"Continuing, because Tor reported itself ready and torsocks " +
		"refuses to connect without Tor. To check later: torsocks curl -s " +
		torProbeURL)
	return nil
}
