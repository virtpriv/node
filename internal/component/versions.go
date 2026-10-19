// Package component owns the component versions pinned by VPN.
package component

const (
	BitcoinCoreVersion = "31.1"
	LNDVersion         = "0.21.4-beta"
	SyncthingVersion   = "2.1.5"
)

// SHA-256 of the upstream Linux amd64 archive for each pinned version, taken
// from the upstream checksum list after its signature was verified. Change a
// hash only together with its version. A release plan must name these hashes,
// and an update refuses any other download.
const (
	BitcoinCoreArchiveSHA256 = "b80d9c3e04da78fb6f0569685673418cf686fadba9042d926d13fb87ff503f9e"
	LNDArchiveSHA256         = "beffe4949a5e46410d90c0e6d020a1b685468440b0004fc4d59740a6291178fb"
	SyncthingArchiveSHA256   = "3d222b609f7ab2944e02748cb10488b4160d446b49e0eafc107ef2a525ab3486"
)
