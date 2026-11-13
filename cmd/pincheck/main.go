// Command pincheck checks the Bitcoin Core, LND and Syncthing releases vpn
// pins against their developers' signatures, the check nodes no longer make.
// The maintainer runs it at each bump, on their own computer, and pins the
// hash it prints. Anyone can run it to repeat the check.
//
// With no flags it checks the versions this checkout pins. -bitcoin, -lnd or
// -syncthing with a version checks that version instead.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/virtpriv/node/internal/component"
	"github.com/virtpriv/node/internal/upstream"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, upstream.Programs, download); err != nil {
		fmt.Fprintln(os.Stderr, "pincheck:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer, programs []upstream.Program, fetch upstream.Fetch) error {
	usage := errors.New("usage: go run ./cmd/pincheck [-bitcoin VERSION] [-lnd VERSION] [-syncthing VERSION]")
	flags := flag.NewFlagSet("pincheck", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asked := map[string]*string{}
	for _, p := range programs {
		asked[string(p.Component)] = flags.String(string(p.Component), "", "the "+p.Name+" version to check")
	}
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		return usage
	}
	chosen := false
	for _, v := range asked {
		chosen = chosen || *v != ""
	}
	// A hash is printed only when every rule held for every program checked,
	// so a run with any FAIL line gives nothing to pin.
	var lines []line
	failed := false
	for _, p := range programs {
		version := *asked[string(p.Component)]
		pinnedVersion, pinnedHash := component.Pinned(p.Component)
		if version == "" {
			if chosen {
				continue
			}
			version = pinnedVersion
		}
		checked, ok := check(p, version, pinnedVersion, pinnedHash, fetch)
		lines = append(lines, checked...)
		failed = failed || !ok
	}
	for _, l := range lines {
		if !l.hash || !failed {
			fmt.Fprintln(out, l.text)
		}
	}
	if failed {
		return errors.New("a check failed, pin nothing")
	}
	return nil
}

type line struct {
	text string
	hash bool
}

// check returns one program's lines and whether every rule held.
func check(p upstream.Program, version, pinnedVersion, pinnedHash string, fetch upstream.Fetch) ([]line, bool) {
	fail := func(format string, args ...any) ([]line, bool) {
		return []line{{text: "FAIL: " + fmt.Sprintf(format, args...)}}, false
	}
	work, err := os.MkdirTemp("", "pincheck-")
	if err != nil {
		return fail("%s %s: %v", p.Name, version, err)
	}
	defer os.RemoveAll(work)
	checked, err := p.Check(version, fetch, work)
	if err != nil {
		return fail("%s %s: %v", p.Name, version, err)
	}
	lines := []line{
		{text: fmt.Sprintf("OK %s %s signed by %d of %s's listed %s: %s",
			p.Name, version, len(checked.Signers), p.Name, p.SignersAre, strings.Join(checked.Signers, ", "))},
		{text: fmt.Sprintf("OK the archive matches %s's signed list", p.Name)},
		{text: "Hash to pin: " + checked.Hash, hash: true},
	}
	if version != pinnedVersion {
		return lines, true
	}
	if checked.Hash != pinnedHash {
		return append(lines, line{text: fmt.Sprintf("FAIL: %s %s differs from the pin", p.Name, version)}), false
	}
	return append(lines, line{text: fmt.Sprintf("OK %s %s matches the pin", p.Name, version)}), true
}

// download saves url as path over HTTPS, through the proxy the environment
// names if any. The largest archive vpn pins is about 91 MB.
func download(url, path string) error {
	client := &http.Client{Timeout: 15 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", url, resp.Status)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	const limit = 2 << 30
	n, err := io.Copy(f, io.LimitReader(resp.Body, limit+1))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil && n > limit {
		err = fmt.Errorf("%s is larger than 2 GiB", url)
	}
	return err
}
