package host

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// Compressed swap in memory comes from Debian's zram package. vpn adds it
// beside any swap the host already has and never touches that swap. A kernel
// without zram installs as before, with one line in the install log. Any
// other failure fails the step, so a resume tries again.
func TestCompressedSwap(t *testing.T) {
	const (
		header   = "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n"
		noSwap   = header
		diskSwap = header + "/swap.img                               file\t\t524284\t\t485684\t\t-2\n"
		zramOn   = header + "/dev/zram0                              partition\t1996796\t\t0\t\t100\n"
	)
	errApt := errors.New("apt failed")
	errStart := errors.New("start failed")
	errUnitRead := errors.New("systemctl failed")
	for _, tc := range []struct {
		name      string
		module    bool
		before    string
		after     string
		unit      string // the zram unit's state, loaded unless given
		unitErr   error
		install   error
		start     error
		wantCalls []string
		wantErr   error
		wantLog   string
	}{
		{name: "kernel without zram", module: false, before: noSwap,
			wantCalls: nil, wantLog: "this kernel has no zram, no compressed swap"},
		{name: "no swap yet", module: true, before: noSwap, after: zramOn,
			wantCalls: []string{"install", "start"}},
		{name: "provider disk swap is not zram", module: true, before: diskSwap, after: diskSwap + zramOn[len(header):],
			wantCalls: []string{"install", "start"}},
		{name: "zram already on, from a provider or an earlier run", module: true, before: zramOn,
			wantCalls: nil, wantLog: "compressed swap is already on"},
		// zram built into a provider's kernel can be on while modinfo finds
		// no module. It is still left as it is, and the log says so.
		{name: "zram already on without a zram module", module: false, before: zramOn,
			wantCalls: nil, wantLog: "compressed swap is already on"},
		// zram switched off by the kernel option systemd.zram=0, by a
		// settings file, or a container. The package makes no zram unit.
		{name: "zram switched off on this machine", module: true, before: noSwap, unit: "not-found",
			wantCalls: []string{"install"}, wantLog: "zram is switched off on this machine, no compressed swap"},
		{name: "zram unit masked by the owner", module: true, before: noSwap, unit: "masked",
			wantCalls: []string{"install"}, wantLog: "zram is switched off on this machine, no compressed swap"},
		// A unit in any other state is broken, which must not pass as done.
		{name: "zram unit broken", module: true, before: noSwap, unit: "bad-setting",
			wantCalls: []string{"install"}, wantErr: errZramUnit},
		{name: "zram unit cannot be read", module: true, before: noSwap, unitErr: errUnitRead,
			wantCalls: []string{"install"}, wantErr: errUnitRead},
		{name: "package install fails", module: true, before: noSwap, install: errApt,
			wantCalls: []string{"install"}, wantErr: errApt},
		{name: "start fails", module: true, before: noSwap, start: errStart,
			wantCalls: []string{"install", "start"}, wantErr: errStart},
		{name: "started but no zram swap", module: true, before: noSwap, after: noSwap,
			wantCalls: []string{"install", "start"}, wantErr: errZramNotOn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls, logs []string
			reads := 0
			err := setUpCompressedSwap(zramOps{
				moduleAvailable: func() bool { return tc.module },
				swaps: func() (string, error) {
					reads++
					if reads == 1 {
						return tc.before, nil
					}
					return tc.after, nil
				},
				install: func() error { calls = append(calls, "install"); return tc.install },
				unitState: func() (string, error) {
					if tc.unit == "" {
						return "loaded", tc.unitErr
					}
					return tc.unit, tc.unitErr
				},
				start: func() error { calls = append(calls, "start"); return tc.start },
				log:   func(line string) { logs = append(logs, line) },
			})
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("err=%v, want %v", err, tc.wantErr)
			}
			if !slices.Equal(calls, tc.wantCalls) {
				t.Fatalf("calls=%v, want %v", calls, tc.wantCalls)
			}
			if tc.wantLog != "" && !slices.ContainsFunc(logs, func(l string) bool { return strings.Contains(l, tc.wantLog) }) {
				t.Fatalf("logs=%v, want a line with %q", logs, tc.wantLog)
			}
		})
	}
}
