# Virtual Private Node

A hardened Debian and Tor base.
Tor, a firewall, SSH login protection and automatic security updates on
Debian 13, set up by one installer.

## Project status

**The `main` branch is a hardened Debian and Tor base. It has no release
yet.**

This page and the guides describe that base. There is nothing to download
for it yet. The install steps apply once its first release is published.

The Bitcoin and Lightning node of this project ended at the tag
`bitcoin-final`. v0.7.0 was not released. Its candidates, `v0.7.0-rc.1` to
`v0.7.0-rc.13`, are test builds of that node.

That node is not maintained. Bitcoin Core and LND get no further updates
from this project. Do not put funds on a node installed from this
repository, from a candidate or from an earlier release.

The earlier releases, up to v0.6.3, install a program called rlvpn. They are
not maintained either. If you run one, [MIGRATION.md](MIGRATION.md) explains
how to move your funds off it.

## What gets installed

- **Tor:** updates and every download once the installer has set up Tor go through Tor. The exceptions are listed under [Privacy & Network Traffic](#privacy--network-traffic). Tor carries outgoing traffic only, the node has no onion address
- **UFW firewall:** deny all incoming except SSH
- **fail2ban:** brute force protection
- **Unattended upgrades:** automatic Debian updates, security fixes included, checked every day, with a restart right after when an update needs one
- **NTP clock sync:** an accurate clock, which Tor and HTTPS need
- **Compressed swap:** when memory runs short, unused memory is compressed and kept in memory (zram, when the kernel supports it). Swap on disk that your provider set up stays in use, see [Hosting Your Node](docs/hosting.md#requirements)
- **The `vpn` account and its TUI:** the account you log in with, and a screen for the node's services, Debian's updates, SSH keys, restarts and new releases of vpn

### Requirements

- A fresh Debian 13 amd64 machine set aside for the node, whose software
  comes only from Debian
- 2 (v)CPU, 4+ GB RAM, 90+ GB total SSD. These sizes are the ones the
  Bitcoin and Lightning node needed. What the base needs has not been
  measured yet.
- The installer refuses a machine with less than 3.4 GB of memory or 80 GB
  of disk. It refuses before it changes anything but its own log,
  `/var/log/vpn.log`, and a lock file under `/run` that a restart clears.
- A container plan (LXC, OpenVZ and the like) is refused. Use a virtual
  machine or a computer of its own.
- A machine without sudo is fine. Log in as root, and the installer adds
  Debian's sudo.

Running at home or on a VPS? See [Hosting Your Node](docs/hosting.md) for what
differs.

## Installing

Install on a fresh Debian 13 amd64 server by following
[Release Verification](docs/verifying.md). It installs Tor first, downloads the
release and its signing key through Tor, and checks the signature and the
checksum before you run anything. The installer then asks for the `vpn`
account's password, sets up the node and shows the command to log in as
`vpn`, where the TUI opens.

## Guides

- [Running Your Node](docs/running.md): logging in, SSH keys, services,
  updates and restarts
- [Hosting Your Node](docs/hosting.md): what differs between a VPS and a
  machine at home
- [Updating Your Node](docs/updating.md): updates from the TUI and how they
  are checked
- [Reproducible builds](docs/reproducing.md): build the release yourself and
  compare
- [Moving your funds off an old node](MIGRATION.md): for a node installed
  from an earlier release or a candidate

For maintainers and contributors: [How vpn Works Inside](docs/developing.md),
[Releasing](docs/releasing.md) and [CONTRIBUTING.md](CONTRIBUTING.md).

## Security

- The TUI runs unprivileged as the `vpn` owner. Its node operations that need root go through a helper with a fixed set of checked operations. General host maintenance requires sudo authentication. The helper cannot reset the owner password. Software running as `vpn` can still modify owner files, so this does not protect the owner from a compromised TUI.
- Updates and downloads go through Tor (SOCKS5 port 9050). The exceptions are listed under Privacy & Network Traffic below
- IPv6 turned off, and the firewall drops all IPv6, to prevent Tor bypass
- Stream isolation: each release check uses a new Tor circuit. apt and vpn's downloads share a circuit
- UFW firewall: SSH only, on the port(s) sshd listens on at install
- Fail2ban: SSH brute-force protection on the same ports. A port you add to sshd later needs its own ufw rule and jail port
- Root SSH disabled by the installer
- SSH password controls apply to `vpn`. Other accounts retain their host password-authentication policy. Keyboard-interactive authentication and X11 forwarding are disabled for `vpn`. Native password changes require the current account password.
- Tor runs with Debian's own settings. Its control socket is open only to root and to Tor itself, and only the installer's Tor check uses it
- vpn is checked by its release signature, before the install and at every update. Everything else the install adds comes from Debian's packages, which apt checks against Debian's keys
- Unattended upgrades of Debian's packages, security fixes included, checked every day, with an automatic restart right after when an update needs one, held off while an install, Update Packages or a node update runs
- Base packages upgraded during install, through Tor and behind the firewall, to close CVE windows on stale server images
- All downloads and apt operations route through Tor once Tor is up (verified by a hard gate before any download)
- Atomic writes with fsync and rename for configuration and program files (prevents corruption on power loss)
- Secure temp file creation with O_EXCL (prevents symlink attacks)
- Public IP detection uses kernel routing table (no external network calls)

## Privacy & Network Traffic

The [Release Verification](docs/verifying.md) steps install Tor from
Debian's mirrors first, then download the `vpn` program and its signing key
through Tor. After that, the installer makes two types of network calls:

**Phase 1 (clearnet, unavoidable):**
- `apt-get update`: Debian package index refresh
- one `apt-get satisfy`: Tor, torsocks, ufw and base tools,
  from Debian package mirrors
- NTP time sync enablement: ongoing clock sync queries to the
  Debian NTP pool (continues after install)

**Phase 2 (all through Tor, hard-gated):**
- Debian package index refresh and package upgrade from
  deb.debian.org over HTTPS (behind the firewall, which is enabled
  in Phase 1). apt's plain http and https are turned off, so a
  source outside Tor fails
- All subsequent apt operations
- One request to check.torproject.org, the installer's own check that
  traffic leaves through Tor

Before the first Phase 2 download, the installer verifies Tor
routing on Tor's own control socket and refuses to continue if it
cannot confirm it. It does so on every run, including resumes,
and again after the package upgrade.

After install, the only ongoing traffic to the internet outside Tor is
NTP clock sync (to the Debian NTP pool) and your own SSH logins. Debian's
updates, vpn's release check and vpn's downloads go through Tor.

vpn's log keeps the install's Tor checks:

```bash
grep "CONFIRMED" /var/log/vpn.log
```

Each line is a Tor check that passed. They do not show where later
traffic went.

## Software Verification

Every program is checked before it is installed:

- **vpn binary:** signed with the project's release key, published on
  keys.openpgp.org and in this repository. You download and verify it
  yourself before running it (see [Release Verification](docs/verifying.md)),
  and the built-in updater performs the same key and checksum
  verification for every later release. The updater reads the key from
  both places, so an outage of either one does not stop updates. It
  trusts only the pinned fingerprint, never a copy itself.
- **Everything else:** Debian's packages, which apt checks against Debian's
  own keys. The installer refuses a machine whose package sources are not
  Debian's, or that has apt's signature check turned off.

The release signing key fingerprint is
`AFA0 EBAC DC9A 4C4A A7B0  154A C97C E10F 170B A5FE`. The same key
has signed every release of this project.

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
