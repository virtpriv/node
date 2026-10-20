# Virtual Private Node

A Bitcoin + Lightning Node on Debian Linux.
Bitcoin Core, LND, and Tor, configured and running in minutes.

## Project status

**A new home, new name, and v0.7.0.**

Virtual Private Node moved to github.com/virtpriv/node, and
the move brought every earlier release with it. Those releases predate
the rename: they install a binary called rlvpn, and they are the
previous generation of this project. v0.7.0 is the first release under
the new name and it is what this page describes. The program, its command
and the account you log in with are all called `vpn`, for Virtual Private
Node.

If you are setting up a node now, it is worth waiting for v0.7.0.
v0.7.0 is fresh-install-only: it does not perform an in-place migration from an
existing `rlvpn` installation or an older unmarked `vpn` layout.

If you already run a node, nothing changes until you deliberately plan a move.
[MIGRATION.md](MIGRATION.md) explains the supported fresh-machine procedure and
why the v0.7.0 installer refuses pre-existing project state.

Please do not build and run the main branch on a node that holds
funds.

## Screenshots

<p>
  <img src="docs/images/channels_home_dark.png" width="49%" />
  <img src="docs/images/channels_open_dark.png" width="49%" />
</p>
<p>
  <img src="docs/images/channels_home_light.png" width="49%" />
  <img src="docs/images/system_home_light.png" width="49%" />
</p>

## What gets installed

### Base (automatic)

- **Bitcoin Core:** pruned node, all P2P through Tor, GPG-verified with at least 3 of 5 pinned builder signatures
- **LND:** Lightning Network daemon with Tor hidden services, installed Tor-only by default
- **Tor:** Bitcoin Core, LND, updates and every download once the installer has set up Tor go through Tor. The exceptions are listed under [Privacy & Network Traffic](#privacy--network-traffic)
- **UFW firewall:** deny all incoming except SSH
- **fail2ban:** brute force protection
- **Unattended upgrades:** automatic Debian security updates, with a restart at 04:00 server time when an update needs one
- **NTP clock sync:** accurate time for block timestamps, HTLC timeouts, and macaroon expiry
- **Compressed swap:** when memory runs short, unused memory is compressed and kept in memory, never written to disk (zram, when the kernel supports it)

### Optional (from the TUI)

- **Syncthing:** automatic LND channel backup to your local device

### Requirements

- Fresh Debian 13 amd64 machine
- 2 (v)CPU, 4+ GB RAM, 90+ GB total SSD. LND safety-stops at 10% free,
  leaving about 81 GB of operational usage on a 90 GB filesystem.
- The installer refuses a machine with less than 3.4 GB of memory or 80 GB
  of disk, before it changes anything.
- LND gets memory first. With less than 7.5 GB of memory, Bitcoin Core gets
  a small cache, so its first sync takes longer.

Running at home or on a VPS? See [Hosting Your Node](docs/hosting.md) for what
differs.

### Privacy

- **Private channels by default.** Channel funding transactions are not linked to your node in the public graph. SCID alias hides the real channel ID from route hints when supported by the channel peer. Blinded paths (default on) go further by eliminating route hints entirely.
- **Blinded paths on invoices (default on).** Invoices use encrypted route data instead of plain hop hints. Senders can pay you without learning your node's pubkey, channel partners, or channel funding UTXOs.
- **Coin control for channel opens.** You choose which UTXOs fund each channel. One UTXO in, one channel out. No silent coin consolidation linking your channels on-chain.
- **Taproot channels (default on).** Cooperative channel closes produce a MuSig2 key-path spend, which looks identical to a regular single-sig transaction on-chain. Requires peer support. When a peer closes a channel, your node also offers its own closing transaction and may pay that fee. On a new node Core has no fee estimates yet, so LND uses a fixed 25 sat/vB, and a small channel balance can go entirely to fees.
- **Consistent P2TR address type.** All addresses (receive, change, close delivery, sweep) use the same P2TR format: `bc1p` on mainnet and `tb1p` on the testing profiles. P2TR has a smaller anonymity set than P2WPKH today, but matching LND's internal address type prevents change-detection fingerprints that would link your outputs regardless of anonymity set size.
- **No node alias.** Your node appears in the network graph with only its pubkey. No identifying name broadcast.
- **Tor-only by default.** All LND connections route through Tor hidden services. Your server IP is never published to the Lightning Network unless you explicitly upgrade to hybrid P2P mode.

## Installing

Install on a fresh Debian 13 amd64 server by following
[Release Verification](docs/verifying.md). It installs Tor first, downloads the
release and its signing key through Tor, and checks the signature and the
checksum before you run anything. The installer then asks for the `vpn`
account's password, sets up the node and opens the TUI.

### Bitcoin network profiles

Mainnet is the default. Testnet4 and the default public signet are test
networks whose coins have no value, and the TUI marks them as such.

(VPS)

```bash
./vpn install
./vpn install --testnet4
./vpn install --signet
```

The network is chosen at install and cannot be changed later. Using another
network needs a fresh machine.

## Guides

- [Running Your Node](docs/running.md): logging in, SSH keys, the wallet and
  auto-unlock, the dashboard, Zeus, Hybrid mode
- [Hosting Your Node](docs/hosting.md): what differs between a VPS and a
  machine at home
- [Syncthing Setup Guide](docs/syncthing.md): channel backups to your own
  device
- [Updating Your Node](docs/updating.md): updates from the TUI and how they
  are checked
- [Recovering Your Node](docs/recovering.md): the seed, `channel.backup`, a
  full disk
- [Reproducible builds](docs/reproducing.md): build the release yourself and
  compare
- [Moving from rlvpn](MIGRATION.md): for nodes of the earlier generation

For maintainers and contributors: [How vpn Works Inside](docs/developing.md),
[Releasing](docs/releasing.md) and [CONTRIBUTING.md](CONTRIBUTING.md).

## Security

- The TUI runs unprivileged as the `vpn` owner. Its node operations that need root go through a helper with a fixed set of checked operations. General host maintenance requires sudo authentication. The helper cannot reset the owner password. Software running as `vpn` still has sensitive node credentials and can modify owner files, so this does not protect the owner from a compromised TUI.
- Bitcoin Core, LND, updates and downloads go through Tor (SOCKS5 port 9050). The exceptions are listed under Privacy & Network Traffic below
- IPv6 disabled to prevent Tor bypass
- Stream isolation (separate circuit per connection)
- UFW firewall: SSH only, on the port(s) sshd actually listens on (+ 9735, 8080 for hybrid P2P, 22000 for Syncthing)
- Fail2ban: SSH brute-force protection
- Root SSH disabled by the installer
- SSH password controls apply to `vpn`. Other accounts retain their host password-authentication policy. Keyboard-interactive authentication and X11 forwarding are disabled for `vpn`. Native password changes require the current account password.
- Bitcoin Core, LND, and Syncthing run as separate, non-login system users. Unprivileged `vpn` has no direct access to their private data directories, while authenticated root maintenance can access them
- Tor control-cookie access is granted only inside `bitcoind.service` and `lnd.service`. Syncthing and the backup exporter receive none
- vpn and LND authenticate to Bitcoin Core with separate `rpcauth` identities. Neither reads a Core cookie or Core's data directory
- Syncthing can read only the finished copy of `channel.backup`. It cannot change that copy or read LND's own files
- GPG signature verification for all software, any bad signature is a hard stop
- Unattended security upgrades with an automatic restart at 04:00 server time when an update needs one
- Base packages upgraded during install, through Tor and behind the firewall, to close CVE windows on stale server images
- Syncthing backup sync: mutual TLS device approval, web UI only via Tor
- Bitcoin Core wallet disabled
- All downloads and apt operations route through Tor once Tor is up (verified by a hard gate before any download)
- Atomic config writes with fsync + rename (prevents corruption on power loss)
- Secure temp file creation with O_EXCL (prevents symlink attacks)
- Public IP detection uses kernel routing table (no external network calls)
- Mandatory seed confirmation ("I SAVED MY SEED") during wallet creation
- Auto-unlock (optional) keeps the wallet password in a file only LND can read and never sends it over the network

## Privacy & Network Traffic

The [Release Verification](docs/verifying.md) steps install Tor from
Debian's mirrors first, then download the `vpn` program and its signing key
through Tor. After that, the installer makes two types of network calls:

**Phase 1 (clearnet, unavoidable):**
- `apt-get update`: Debian package index refresh
- one `apt-get install`: Tor, torsocks, ufw and base tools,
  from Debian package mirrors
- NTP time sync enablement: ongoing clock sync queries to the
  Debian NTP pool (continues after install)

**Phase 2 (all through Tor, hard-gated):**
- Debian package index refresh and package upgrade (behind the
  firewall, which is enabled in Phase 1)
- Bitcoin Core and LND downloads
- Syncthing download (when Syncthing is installed)
- All subsequent apt operations

Before the first Phase 2 download, the installer verifies Tor
routing on Tor's own control port and refuses to continue if it
cannot confirm it. It does so on every run, including resumes,
and again after the package upgrade.

After install, the only ongoing clearnet traffic is NTP clock
sync (to the Debian NTP pool), your own SSH logins, Syncthing sync
(port 22000) if you install it, and LND P2P and wallet connections if
you choose hybrid mode. Everything else routes through Tor.

Verify Tor routing after install:
```bash
grep "Tor" /var/log/vpn.log
```

## Software Verification

All software is verified with GPG signatures and SHA256 checksums:

- **Bitcoin Core:** 5 trusted builder keys from
  [bitcoin-core/guix.sigs](https://github.com/bitcoin-core/guix.sigs).
  Requires 3 of 5 valid signatures. A bad signature (BADSIG) from any
  key is a hard stop.
- **LND:** Roasbeef's signing key verified against known fingerprint.
- **Syncthing:** pinned release binary, verified against the Syncthing
  release signing key's known fingerprint. The release checksums are
  clearsigned by the same key. VPN also writes Syncthing's
  entire configuration itself and refuses to start the daemon if any
  privacy setting does not verify.
- **vpn binary:** signed with the project's release key, published on
  keys.openpgp.org and in this repository. You download and verify it
  yourself before running it (see [Release Verification](docs/verifying.md)),
  and the built-in updater performs the same key and checksum
  verification for every later release. The updater reads the key from
  both places, so an outage of either one does not stop updates. It
  trusts only the pinned fingerprint, never a copy itself.

A signature from a key that its owner has revoked is refused for every
download above.

The release signing key fingerprint is
`AFA0 EBAC DC9A 4C4A A7B0  154A C97C E10F 170B A5FE`. The same key
has signed every release of this project and will sign v0.7.0.

Verification failure is a hard stop.

After installation, review the log:

```bash
cat /var/log/vpn.log
```

For manual binary verification before installation, see
[Release Verification](docs/verifying.md).

## License

Copyright (C) 2026 Virtual Private Node

This project is free software licensed under the
[GNU Affero General Public License v3.0](LICENSE).
