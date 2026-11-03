package system

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeInhibit puts a stand-in systemd-inhibit first on PATH. It accepts only
// a block inhibitor for shutdown, and the marker file exists while it holds
// one, that is while the program it runs has not ended. The real lock is
// logind's, shown on a node.
func fakeInhibit(t *testing.T, script string) (marker string) {
	t.Helper()
	dir := t.TempDir()
	marker = filepath.Join(dir, "held")
	if script == "" {
		script = `#!/bin/sh
[ "$1" = --what=shutdown ] && [ "$2" = --mode=block ] && [ "$3" = --who=vpn ] || exit 3
case "$4" in --why=?*) ;; *) exit 3 ;; esac
shift 4
echo $$ > "$VPN_TEST_INHIBIT_MARKER"
"$@"
rm -f "$VPN_TEST_INHIBIT_MARKER"
`
	}
	if err := os.WriteFile(filepath.Join(dir, "systemd-inhibit"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VPN_TEST_INHIBIT_MARKER", marker)
	return marker
}

func held(marker string) bool {
	_, err := os.Stat(marker)
	return err == nil
}

// waitReleased waits for the stand-in to end, which a real inhibitor does as
// soon as its program ends.
func waitReleased(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for held(marker) {
		if time.Now().After(deadline) {
			t.Fatal("the inhibitor is still held")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestShutdownInhibitorHeldUntilReleased(t *testing.T) {
	marker := fakeInhibit(t, "")
	inh, err := InhibitShutdown("installing")
	if err != nil {
		t.Fatal(err)
	}
	if !held(marker) {
		t.Fatal("InhibitShutdown returned before the inhibitor was held")
	}
	if err := inh.Release(); err != nil {
		t.Fatal(err)
	}
	if held(marker) {
		t.Fatal("still held after Release returned")
	}
	if err := inh.Release(); err != nil {
		t.Fatalf("a second Release: %v", err)
	}
}

func TestShutdownInhibitorRefused(t *testing.T) {
	fakeInhibit(t, "#!/bin/sh\necho 'Failed to inhibit: Access denied' >&2\nexit 1\n")
	inh, err := InhibitShutdown("installing")
	if err == nil {
		inh.Release()
		t.Fatal("a refused inhibitor counted as held")
	}
	if !strings.Contains(err.Error(), "Access denied") {
		t.Fatalf("error %v does not carry systemd-inhibit's message", err)
	}
}

// When vpn is killed, nothing runs Release. The inhibitor must still end.
func TestShutdownInhibitorEndsWhenTheHolderIsKilled(t *testing.T) {
	marker := fakeInhibit(t, "")
	holder := exec.Command(os.Args[0], "-test.run=^TestShutdownInhibitorHolderProcess$")
	holder.Env = append(os.Environ(), "VPN_TEST_INHIBIT_HOLDER=1")
	out, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "held\n" {
		holder.Process.Kill()
		holder.Wait()
		t.Fatalf("holder said %q, %v", line, err)
	}
	if !held(marker) {
		t.Fatal("the holder reported held without the inhibitor")
	}
	if err := holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	holder.Wait()
	waitReleased(t, marker)
}

// TestShutdownInhibitorHolderProcess is the process the test above kills. It
// does nothing in a normal test run.
func TestShutdownInhibitorHolderProcess(t *testing.T) {
	if os.Getenv("VPN_TEST_INHIBIT_HOLDER") != "1" {
		return
	}
	if _, err := InhibitShutdown("installing"); err != nil {
		os.Stdout.WriteString("failed: " + err.Error() + "\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("held\n")
	time.Sleep(time.Minute)
	os.Exit(0)
}
