// internal/installer/torgate_test.go

package installer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/virtpriv/node/internal/failure"
)

// The lines have the form of Debian 13's Tor 0.4.9.15's answers to GETINFO
// status/bootstrap-phase. Tor copies a problem's words into WARNING as they
// are. Three cases are made up: the words that look like Tor's advice, the
// line cut off and the percentage with no digits.
func TestReadTorStatus(t *testing.T) {
	const phase = "250-status/bootstrap-phase="
	const problem = `WARN BOOTSTRAP PROGRESS=14 TAG=handshake SUMMARY="Handshaking with a relay" `
	cases := []struct {
		name     string
		line     string
		progress int
		warn, ok bool
	}{
		{"starting", phase + `NOTICE BOOTSTRAP PROGRESS=0 TAG=starting SUMMARY="Starting"`, 0, false, true},
		{"on the way", phase + `NOTICE BOOTSTRAP PROGRESS=14 TAG=handshake SUMMARY="Handshaking with a relay"` + "\r\n", 14, false, true},
		{"done", phase + `NOTICE BOOTSTRAP PROGRESS=100 TAG=done SUMMARY="Done"` + "\r\n", 100, false, true},
		{"a problem Tor advises to ignore",
			phase + problem + `WARNING="DONE" REASON=DONE COUNT=9 RECOMMENDATION=ignore HOSTID="AF2334142F98A88716F6E9679BD94279FFF803A5" HOSTADDR="128.112.102.39:443"` + "\r\n",
			14, false, true},
		{"a problem Tor advises to warn about",
			phase + problem + `WARNING="Connection refused" REASON=CONNECTREFUSED COUNT=13 RECOMMENDATION=warn HOSTID="5822CD8E814A810084FB339FFB8575FC710C7F31" HOSTADDR="192.0.2.7:9001"` + "\r\n",
			14, true, true},
		{"a problem whose words look like Tor's advice",
			phase + problem + `WARNING="peer said RECOMMENDATION=warn PROGRESS=99" REASON=TLS_ERROR COUNT=2 RECOMMENDATION=ignore HOSTID="?" HOSTADDR="?"`,
			14, false, true},
		{"a problem line cut off before Tor's advice", phase + problem + `WARNING="Connection ref`, 14, false, true},
		{"no percentage", `250 OK`, 0, false, false},
		{"a percentage with no digits", phase + `NOTICE BOOTSTRAP PROGRESS=x`, 0, false, false},
		{"empty", ``, 0, false, false},
	}
	for _, c := range cases {
		got, ok := readTorStatus(c.line)
		if ok != c.ok || got.progress != c.progress || got.warn != c.warn {
			t.Errorf("%s: got %d%%, warn %v, ok %v, want %d%%, warn %v, ok %v",
				c.name, got.progress, got.warn, ok, c.progress, c.warn, c.ok)
		}
		if ok && got.line != strings.TrimSpace(strings.TrimPrefix(c.line, phase)) {
			t.Errorf("%s: the line kept for the log is not Tor's own line alone: %q", c.name, got.line)
		}
	}
}

func TestTorProbeVerdict(t *testing.T) {
	cases := []struct {
		name   string
		output string
		err    error
		want   torProbeResult
	}{
		{"tor exit", `{"IsTor":true,"IP":"185.220.101.1"}`, nil, probeTor},
		{"an answer of not Tor",
			`{"IsTor":false,"IP":"203.0.113.7"}`, nil, probeNotTor},
		{"curl error", "", errors.New("exit status 6"), probeUnreachable},
		// An error wins even when the output looks like success. A failed
		// command never confirms routing.
		{"error with tor-looking output",
			`{"IsTor":true}`, errors.New("exit status 28"),
			probeUnreachable},
		{"captive portal / garbage", `<html>login required</html>`,
			nil, probeUnreachable},
		{"empty output", ``, nil, probeUnreachable},
	}
	for _, c := range cases {
		if got := torProbeVerdict(c.output, c.err); got != c.want {
			t.Errorf("%s: torProbeVerdict(%q, %v) = %v, want %v",
				c.name, c.output, c.err, got, c.want)
		}
	}
}

// The wait's rule, with a stand-in for Tor that answers by how long the wait
// has run. Time is the test's own, so ten minutes pass at once.
func TestTheTorWait(t *testing.T) {
	ask := askTor
	t.Cleanup(func() { askTor = ask })
	silent := errors.New("no answer")
	at := func(progress int) (torStatus, error) {
		return torStatus{progress: progress, line: "Tor's own line"}, nil
	}
	problem := func(progress int) (torStatus, error) {
		return torStatus{progress: progress, warn: true, line: "Tor's own line"}, nil
	}
	const (
		connected = iota
		notConnected
		// notConnectedAndSilent is the 10 minute limit reached in an ask
		// that failed.
		notConnectedAndSilent
		noAnswer
	)
	cases := []struct {
		name string
		tor  func(since time.Duration) (torStatus, error)
		want int
		// after is when the wait ends. said is how many lines the owner got.
		after time.Duration
		said  int
		// number is Tor's last answer, which a sentence about it names.
		number string
	}{
		// The two recorded failed installs: Tor stood still for longer than
		// 90 seconds and then finished by itself.
		{"a start that stands still for nine minutes and then finishes",
			func(since time.Duration) (torStatus, error) {
				if since < 9*time.Minute {
					return at(52)
				}
				return at(100)
			}, connected, 9 * time.Minute, 1, ""},
		{"a start that never finishes",
			func(time.Duration) (torStatus, error) { return at(99) },
			notConnected, 10 * time.Minute, 1, "99%"},
		{"a Tor that never answers",
			func(time.Duration) (torStatus, error) { return torStatus{}, silent },
			noAnswer, 90 * time.Second, 1, ""},
		{"a Tor that stops answering",
			func(since time.Duration) (torStatus, error) {
				if since <= 4*time.Minute {
					return at(30)
				}
				return torStatus{}, silent
			}, noAnswer, 4*time.Minute + 90*time.Second, 2, ""},
		// systemd starts a crashed Tor again, and the new Tor counts from 0.
		{"a Tor that is away for a minute and starts over",
			func(since time.Duration) (torStatus, error) {
				switch {
				case since < time.Minute:
					return at(30)
				case since < 2*time.Minute:
					return torStatus{}, silent
				case since < 5*time.Minute:
					return at(5)
				}
				return at(100)
			}, connected, 5 * time.Minute, 3, ""},
		// Tor's advice to warn is told and ends nothing. Tor takes the
		// line back when it makes progress.
		{"a problem Tor warns about, then progress",
			func(since time.Duration) (torStatus, error) {
				switch {
				case since < time.Minute:
					return problem(5)
				case since < 2*time.Minute:
					return at(5)
				}
				return at(100)
			}, connected, 2 * time.Minute, 2, ""},
		{"a problem Tor warns about for good",
			func(since time.Duration) (torStatus, error) {
				if since < 15*time.Second {
					return at(5)
				}
				return problem(5)
			}, notConnected, 10 * time.Minute, 2, "5%"},
		{"a problem Tor warns about, then no answer",
			func(since time.Duration) (torStatus, error) {
				if since < time.Minute {
					return problem(5)
				}
				return torStatus{}, silent
			}, noAnswer, 57*time.Second + 90*time.Second, 2, ""},
		// The 10 minutes count from the wait's start, whatever Tor does.
		{"a Tor that starts over every minute and never finishes",
			func(since time.Duration) (torStatus, error) {
				switch phase := since % time.Minute; {
				case phase < 21*time.Second:
					return torStatus{}, silent
				case phase < 40*time.Second:
					return at(5)
				}
				return at(30)
			}, notConnectedAndSilent, 10 * time.Minute, 31, "30%"},
		{"a Tor that stops answering 30 seconds before the 10 minutes",
			func(since time.Duration) (torStatus, error) {
				switch {
				case since < time.Minute:
					return at(80)
				case since <= 9*time.Minute+30*time.Second:
					return at(52)
				}
				return torStatus{}, silent
			}, notConnectedAndSilent, 10 * time.Minute, 3, "52%"},
		{"both limits reached in the same ask",
			func(since time.Duration) (torStatus, error) {
				if since <= 8*time.Minute+30*time.Second {
					return at(52)
				}
				return torStatus{}, silent
			}, noAnswer, 10 * time.Minute, 2, ""},
	}
	// Each case is a test of its own, so one that fails does not hide the
	// cases after it.
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				askTor = func() (torStatus, error) { return c.tor(time.Since(start)) }
				var said []string
				err := waitForTor(context.Background(), func(text string) { said = append(said, text) })
				if got := time.Since(start); got != c.after {
					t.Errorf("the wait ended after %v, want %v", got, c.after)
				}
				if len(said) != c.said {
					t.Errorf("the owner was told %q, want %d lines", said, c.said)
				}
				f, sentence := errors.AsType[*failure.Failure](err)
				if c.want == connected {
					if err != nil {
						t.Error(err)
					}
					return
				}
				if !sentence {
					t.Fatalf("got %v, want a sentence", err)
				}
				// What the log gets under the sentence: Tor's last line after
				// the 10 minutes, why the ask failed after 90 seconds of
				// silence, both when the 10 minutes end in a failed ask.
				torsLine := strings.Contains(strings.Join(f.CauseLines(), "\n"), "Tor's own line")
				failedAsk := errors.Is(err, silent)
				wantLine, wantAsk := c.want != noAnswer, c.want != notConnected
				if strings.Contains(f.Text, "Tor's own line") || strings.Contains(f.Text, silent.Error()) {
					t.Errorf("the sentence %q holds the cause's words", f.Text)
				}
				if !strings.Contains(f.Text, c.number) {
					t.Errorf("the sentence %q does not name Tor's last answer, %s", f.Text, c.number)
				}
				if torsLine != wantLine || failedAsk != wantAsk {
					t.Errorf("Tor's last line under the sentence: %v, want %v. The failed ask: %v, want %v",
						torsLine, wantLine, failedAsk, wantAsk)
				}
			})
		})
	}
}
