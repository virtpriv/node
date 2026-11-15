// Package component owns the component versions pinned by VPN.
package component

const (
	BitcoinCoreVersion = "31.2"
	LNDVersion         = "0.21.6-beta"
	SyncthingVersion   = "2.1.5"
)

// SHA-256 of the upstream Linux amd64 archive for each pinned version, printed
// by go run ./cmd/pincheck after it checked upstream's signatures. Change a
// hash only together with its version. Nodes check these programs by this
// hash alone. A release plan must name these hashes, and an update refuses
// any other archive.
const (
	BitcoinCoreArchiveSHA256 = "2f38996afaa74dfa469a23bf4727887133dfc09784df1f21ddf1c74d0dc82300"
	LNDArchiveSHA256         = "47db47290df2d183393259a7b313ddcfc7376e9cd941bd691858250246fc763e"
	SyncthingArchiveSHA256   = "3d222b609f7ab2944e02748cb10488b4160d446b49e0eafc107ef2a525ab3486"
)
