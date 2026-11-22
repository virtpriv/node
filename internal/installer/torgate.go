// internal/installer/torgate.go
//
// The Tor check, the install step "Verifying the Tor connection". It comes
// after "Setting up Tor" and before "Sending apt package downloads through
// Tor". As a gate it runs again on every pass, so no download through Tor
// runs in a pass whose Tor was not checked. The step that updates Debian
// waits for Tor again with waitForTor, because an upgrade can restart Tor.
//
// Two layers:
//
//  1. The gate, with no outside dependency. Wait until Tor reports
//     bootstrap PROGRESS=100 on its own control socket (cookie
//     authentication). A started unit only says Tor runs.
//     PROGRESS=100 says Tor can route.
//
//  2. A best-effort outside check: check.torproject.org/api/ip fetched
//     through torsocks.
//       IsTor:true   routing confirmed end to end, continue.
//       IsTor:false  the site did not see the request arrive through Tor.
//                    Either torsocks is present but not intercepting (a
//                    broken preload library, for example), or the site
//                    does not know the relay the request left through. The
//                    step fails. This is the only check that sees a torsocks
//                    that fails open.
//       unreachable  warn and continue. Tor reported itself ready, and
//                    torsocks refuses to connect when it cannot reach
//                    Tor. The install never depends on the outside site.

package installer

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/failure"
	"github.com/virtpriv/node/internal/logger"
	"github.com/virtpriv/node/internal/paths"
	"github.com/virtpriv/node/internal/system"
)

const (
	// The wait has no rule about progress. A Tor start can stand still for
	// minutes and go on by itself. Tor drops a directory download that
	// stopped answering only after 5 minutes and then asks another relay.
	// The limit leaves room for that and still ends a start that never
	// connects.
	torConnectLimit = 10 * time.Minute
	// A Tor that gives no answer may not be running. The restart rule has
	// systemd start a crashed Tor again after 30 seconds, so this leaves
	// room for a crash and the start after it.
	torSilenceLimit = 90 * time.Second
	torAskEvery     = 3 * time.Second

	torProbeURL      = "https://check.torproject.org/api/ip"
	torProbeAttempts = 2
)

// verifyTorRouting is the "Verifying the Tor connection" install step. A
// pause asked through ctx ends its waits.
func verifyTorRouting(ctx context.Context, say func(string)) error {
	if _, err := exec.LookPath("torsocks"); err != nil {
		return &failure.Failure{Cause: err,
			Text: "torsocks was not found, so the test request cannot be sent through Tor."}
	}

	if err := waitForTor(ctx, say); err != nil {
		return err
	}

	say("Sending a test request through Tor")
	return runTorExitProbe(ctx)
}

// waitForTor asks Tor every few seconds until it reports that it is
// connected, PROGRESS=100. A percentage that stands still ends nothing. The
// wait ends without a connected Tor in three ways. Tor gave no answer for
// torSilenceLimit, counted from its last answer or from the start, since
// right after host.EnableAndRestartTor the control socket may not be there
// yet. Or torConnectLimit has passed. Or a pause is asked through ctx, which
// ends the wait with ctx's own error and never under a sentence, so that it
// is read as a pause.
//
// Tor is asked once before the wait gives way to a pause, so a Tor that is
// ready passes even then, and the step it ends is recorded. say tells the
// owner how far Tor is, that Tor gives no answer, and that Tor reports a
// problem while its own advice about it is "warn". The problem never ends
// the wait. Tor replaces that line as soon as it makes progress, and a
// problem left from before this run must not fail it. Tor's own words for
// it are in Tor's log.
func waitForTor(ctx context.Context, say func(string)) error {
	start := time.Now()
	lastAnswer := start
	var last torStatus
	said := ""

	for {
		status, err := askTor()
		now := time.Now()
		line := "Waiting for Tor to answer"
		if err == nil {
			if status.progress >= 100 {
				logger.Install("Tor bootstrap 100%% CONFIRMED (via control socket)")
				return nil
			}
			last, lastAnswer = status, now
			line = fmt.Sprintf("Waiting for Tor to connect: %d%%", status.progress)
			if status.warn {
				line += ", Tor reports a problem"
			}
		}
		if line != said {
			say(line)
			said = line
		}

		switch {
		// Only an ask that failed can be this long after the last answer.
		case now.Sub(lastAnswer) >= torSilenceLimit:
			return torWaitFailure(fmt.Sprintf("Tor did not answer for %d seconds. It may not be running.",
				int(torSilenceLimit.Seconds())), err)
		case now.Sub(start) >= torConnectLimit:
			text := fmt.Sprintf("Tor has not connected to the Tor network after %d minutes. Its last answer was %d%%",
				int(torConnectLimit.Minutes()), last.progress)
			if last.warn {
				text += " and reported a problem"
			}
			// Tor's last line, and why the ask just made failed when it did.
			return torWaitFailure(text+".", errors.Join(errors.New(last.line), err))
		}
		if err := system.Sleep(ctx, torAskEvery); err != nil {
			return err
		}
	}
}

// torWaitFailure is a wait that ended without a connected Tor. The log
// names where Tor's own words are. That line names tor@default, the real
// Tor. Debian's tor.service only runs /bin/true.
func torWaitFailure(text string, cause error) error {
	logger.Install("Tor's own log: journalctl -u tor@default")
	return &failure.Failure{Text: text, Cause: cause}
}

// torStatus is Tor's answer to GETINFO status/bootstrap-phase.
type torStatus struct {
	progress int
	// warn is set while Tor's last line is a problem that Tor itself advises
	// to tell the user about. Tor does so from its tenth failed connection
	// to a relay on, or at the first one when there is no route or the clock
	// is wrong.
	warn bool
	// line is Tor's own line, for the log.
	line string
}

// askTor asks Tor how far its start is. A test stands in for Tor.
var askTor = queryTorStatus

// queryTorStatus performs one round trip on the control socket:
// AUTHENTICATE with the cookie, GETINFO status/bootstrap-phase, read the
// status. The cookie is re-read on every attempt because Tor regenerates it
// on restart.
func queryTorStatus() (torStatus, error) {
	// The installer runs as root, and reads the cookie through the wrapper
	// that requires root.
	cookie, err := system.ReadFileRoot(paths.TorControlCookie)
	if err != nil {
		return torStatus{}, fmt.Errorf("read control cookie: %w", err)
	}

	conn, err := net.DialTimeout("unix", paths.TorControlSocket, 5*time.Second)
	if err != nil {
		return torStatus{}, fmt.Errorf("dial control socket: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	r := bufio.NewReader(conn)

	fmt.Fprintf(conn, "AUTHENTICATE %s\r\n", hex.EncodeToString(cookie))
	line, err := r.ReadString('\n')
	if err != nil {
		return torStatus{}, fmt.Errorf("read auth reply: %w", err)
	}
	if !strings.HasPrefix(line, "250") {
		return torStatus{}, fmt.Errorf("control auth refused: %s",
			strings.TrimSpace(line))
	}

	fmt.Fprintf(conn, "GETINFO status/bootstrap-phase\r\n")
	var status torStatus
	found := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return torStatus{}, fmt.Errorf("read GETINFO reply: %w", err)
		}
		if s, ok := readTorStatus(line); ok {
			status, found = s, true
		}
		if strings.HasPrefix(line, "250 ") ||
			strings.HasPrefix(line, "5") {
			break
		}
	}
	fmt.Fprintf(conn, "QUIT\r\n")

	if !found {
		return torStatus{}, fmt.Errorf("no PROGRESS field in bootstrap-phase reply")
	}
	return status, nil
}

// readTorStatus reads a status line of Tor's control protocol, e.g.:
//
//	250-status/bootstrap-phase=NOTICE BOOTSTRAP PROGRESS=100 TAG=done SUMMARY="Done"
//	250-status/bootstrap-phase=WARN BOOTSTRAP PROGRESS=14 TAG=handshake SUMMARY="Handshaking with a relay" WARNING="Connection refused" REASON=CONNECTREFUSED COUNT=11 RECOMMENDATION=warn HOSTID="…" HOSTADDR="…"
//
// Tor copies a problem's words into WARNING as they are, so its advice is
// taken from the last RECOMMENDATION of the line. A pure function, tested
// on its own.
func readTorStatus(line string) (torStatus, bool) {
	_, rest, ok := strings.Cut(line, "PROGRESS=")
	if !ok {
		return torStatus{}, false
	}
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return torStatus{}, false
	}
	status := torStatus{progress: n, line: strings.TrimSpace(line)}
	if _, own, ok := strings.Cut(status.line, "status/bootstrap-phase="); ok {
		status.line = own
	}
	if i := strings.LastIndex(rest, " RECOMMENDATION="); i >= 0 {
		advice, _, _ := strings.Cut(rest[i+len(" RECOMMENDATION="):], " ")
		status.warn = strings.TrimSpace(advice) == "warn"
	}
	return status, true
}

// torProbeResult is what one outside check showed. torProbeVerdict, a
// pure function tested on its own, decides it.
type torProbeResult int

const (
	probeTor         torProbeResult = iota // IsTor:true, confirmed
	probeNotTor                            // IsTor:false, not seen as Tor
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
		return probeNotTor
	}
	return probeUnreachable
}

// runTorExitProbe runs the best-effort outside check. Only an answer of
// IsTor:false fails the step. An unreachable site warns and the install
// continues. A pause asked through ctx ends the wait between two tries.
func runTorExitProbe(ctx context.Context) error {
	for attempt := 0; attempt < torProbeAttempts; attempt++ {
		output, err := system.RunOutputWithTimeout(15*time.Second,
			"torsocks", "curl", "-s", "--max-time", "10",
			torProbeURL)
		switch torProbeVerdict(output, err) {
		case probeTor:
			logger.Install(
				"Tor routing CONFIRMED via check.torproject.org")
			return nil
		case probeNotTor:
			return &failure.Failure{
				Text: "check.torproject.org did not see the test request arrive through Tor. " +
					"Either torsocks is not sending traffic through Tor, or the site does not know the relay the request left through. " +
					"The install stops here so that nothing is downloaded outside Tor.",
				Cause: errors.New("check.torproject.org answered: " + output)}
		case probeUnreachable:
			if attempt < torProbeAttempts-1 {
				if err := system.Sleep(ctx, 5*time.Second); err != nil {
					return err
				}
			}
		}
	}
	logger.Install("WARNING: the outside Tor check could not be reached. " +
		"Continuing, because Tor reported itself ready and torsocks " +
		"refuses to connect without Tor. To check later: torsocks curl -s " +
		torProbeURL)
	return nil
}
