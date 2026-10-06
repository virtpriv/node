package host

import (
	"strings"
	"testing"

	"github.com/virtpriv/node/internal/update/protocol"
)

// The outputs below were printed by the released programs themselves. A plan
// lists exact installed versions, so each must keep reading as it does today.
// A later format must still be read: after a failed update the installed
// helper has to name a program that update already replaced.
func TestComponentVersionReadsReleasedAndLaterFormats(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    protocol.Component
		out  string
		want string
	}{
		{"Core 29.3", protocol.Bitcoin, "Bitcoin Core daemon version v29.3.0\nCopyright (C) 2009-2025 The Bitcoin Core developers\n", "29.3"},
		{"Core 29.4", protocol.Bitcoin, "Bitcoin Core daemon version v29.4.0\nCopyright (C) 2009-2025 The Bitcoin Core developers\n", "29.4"},
		{"LND 0.21.2", protocol.LND, "lnd version 0.21.2-beta commit=v0.21.2-beta\n", "0.21.2-beta"},
		{"LND 0.21.4", protocol.LND, "lnd version 0.21.4-beta commit=v0.21.4-beta\n", "0.21.4-beta"},
		{"Syncthing 2.1.5", protocol.Syncthing, `syncthing v2.1.5 "Hafnium Hornet" (go1.27.1 linux-amd64) builder@github.syncthing.net 2026-09-08 06:57:55 UTC`, "2.1.5"},
		{"LND without its suffix", protocol.LND, "lnd version 0.22.0 commit=v0.22.0\n", "0.22.0"},
		{"Core candidate naming", protocol.Bitcoin, "Bitcoin Core daemon version v31.2.1rc1\nCopyright (C) 2009-2027 The Bitcoin Core developers\n", "31.2.1rc1"},
		{"Syncthing candidate naming", protocol.Syncthing, `syncthing v3.0.0-rc.1 "Name" (go1.28.0 linux-amd64)`, "3.0.0-rc.1"},
		{"no version at all", protocol.LND, "lnd version unknown commit=\n", ""},
		{"only a year range", protocol.Bitcoin, "Copyright (C) 2009-2025 The Bitcoin Core developers\n", ""},
		{"text unsafe to show", protocol.LND, "lnd version 1.0/../../x\n", ""},
		{"text too long to show", protocol.LND, "lnd version 1." + strings.Repeat("9", 40) + "\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := componentVersion(tc.c, tc.out); got != tc.want {
				t.Fatalf("read %q, want %q", got, tc.want)
			}
		})
	}
}
