package component

import (
	"strings"

	"github.com/virtpriv/node/internal/update/protocol"
	"golang.org/x/mod/semver"
)

// Name is how screens and logs name c.
func Name(c protocol.Component) string {
	switch c {
	case protocol.Bitcoin:
		return "Bitcoin Core"
	case protocol.LND:
		return "LND"
	case protocol.Syncthing:
		return "Syncthing"
	}
	return string(c)
}

// ArchiveName is the file name upstream gives the Linux amd64 archive of c at
// version. The node looks for exactly this name in a releases folder.
func ArchiveName(c protocol.Component, version string) string {
	switch c {
	case protocol.Bitcoin:
		return "bitcoin-" + version + "-x86_64-linux-gnu.tar.gz"
	case protocol.LND:
		return "lnd-linux-amd64-v" + version + ".tar.gz"
	case protocol.Syncthing:
		return "syncthing-linux-amd64-v" + version + ".tar.gz"
	}
	return ""
}

// ReleaseURL is where upstream publishes file, one of version's release
// files.
func ReleaseURL(c protocol.Component, version, file string) string {
	switch c {
	case protocol.Bitcoin:
		return "https://bitcoincore.org/bin/bitcoin-core-" + version + "/" + file
	case protocol.LND:
		return "https://github.com/lightningnetwork/lnd/releases/download/v" + version + "/" + file
	case protocol.Syncthing:
		return "https://github.com/syncthing/syncthing/releases/download/v" + version + "/" + file
	}
	return ""
}

// ArchiveURL is where upstream publishes the archive of c at version.
func ArchiveURL(c protocol.Component, version string) string {
	return ReleaseURL(c, version, ArchiveName(c, version))
}

// ArchiveVersion returns the component and version an exact archive name
// belongs to, for the names ArchiveName gives and nothing else.
func ArchiveVersion(name string) (protocol.Component, string, bool) {
	for _, c := range protocol.Components {
		prefix, suffix, _ := strings.Cut(ArchiveName(c, "\x00"), "\x00")
		rest, ok := strings.CutPrefix(name, prefix)
		if !ok {
			continue
		}
		if version, ok := strings.CutSuffix(rest, suffix); ok && protocol.ValidVersion(c, version) {
			return c, version, true
		}
	}
	return "", "", false
}

// UsedArchive reports the component of an exact archive name whose version is
// the one this release pins or older. A node that runs this release has
// installed it or moved past it, so the file is no longer needed.
func UsedArchive(name string) (protocol.Component, bool) {
	c, version, ok := ArchiveVersion(name)
	if !ok {
		return "", false
	}
	pinned, _ := Pinned(c)
	return c, semver.Compare("v"+version, "v"+pinned) <= 0
}

// Pinned is the version and archive SHA-256 this release pins for c.
func Pinned(c protocol.Component) (version, sha256 string) {
	switch c {
	case protocol.Bitcoin:
		return BitcoinCoreVersion, BitcoinCoreArchiveSHA256
	case protocol.LND:
		return LNDVersion, LNDArchiveSHA256
	case protocol.Syncthing:
		return SyncthingVersion, SyncthingArchiveSHA256
	}
	return "", ""
}
