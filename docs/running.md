# Running Your Node

This guide covers everyday use of a Virtual Private Node (Node) after
installation: logging in, creating the wallet, the dashboard, connecting a
phone wallet and sharing your node. To install, see
[Release Verification](verifying.md). For backups and restores, see
[Recovering Your Node](recovering.md).

## Logging in

The installer creates the `vpn` owner account. Choose a login password of at
least 16 characters, preferably generated and saved in a password manager.
When the install finishes, it shows the `ssh` command and waits until you log
in as `vpn` from a new terminal. Your node opens there.
Unattended installation generates a password and shows it at completion. Save
it for your first login.

Installation enables password SSH for `vpn`, also when the provider switched
password login off for the server. Use that password for your first SSH login
as `vpn`. Installation does not look for, accept or copy SSH keys. Add a key
after your first login, as described below.

Every SSH login as `vpn` opens the TUI. Exiting it returns to the `vpn` shell,
where the node commands and sudo are available. The same account password
authorizes unrestricted sudo for server maintenance. The shell and sudo stay available when
the TUI or its helper does not work. Accounts your provider set up are kept.

If the TUI gets stuck when you log in, this command skips it:

```bash
ssh -t vpn@YOUR_SERVER /bin/bash --noprofile --norc
```

When you start the TUI from the `vpn` shell, Ctrl+Z pauses it and `fg` brings
it back. Root SSH is switched off, but the root account stays. Keep your
provider's console or rescue access available.

## SSH keys and password login

Use **System → Accounts** to find supported SSH keys on other accounts on the
server, review one and import it into `vpn`. Or add a key directly from
**System → Accounts → vpn → Add Key**. Keys already set up for `vpn` need no
import.

After you have logged in with your key in a new SSH connection, you can switch
off password SSH for `vpn` from **System → Accounts → vpn**. The login banner
confirms a `vpn` SSH login, which may still use a password. It does not check
a particular key or sudo access.

To change the account password, use **System → Accounts → vpn → Change
Password**. You type the current password, then the new one twice, in
Debian's own `passwd` prompts, and Debian's password rules apply. This
password is separate from the LND wallet password and the recovery seed.

## Creating the wallet

On the first start of the TUI you go straight to wallet creation:

1. Read the privacy and seed warnings, press Proceed
2. Wait for LND to become ready
3. Type a wallet password
4. Write down your 24-word seed on paper
5. Type `I SAVED MY SEED` to confirm

The confirmation phrase is required. There is no skip. Pressing Ctrl+C at the
password prompt cancels safely, because no seed exists yet and nothing is
written. Once you have seen your seed, Ctrl+C is blocked by design. The only
way forward is typing the confirmation phrase.

After the seed, the TUI continues to auto-unlock, so you do not have to
unlock the wallet by hand after every restart. vpn restarts LND once to check
the password and reports success only when LND accepted it. If the check
fails, LND stays locked and the same form lets you try again.

## Auto-unlock

Auto-unlock is strongly encouraged. The node installs Debian security updates
by itself and restarts at 04:00 server time when an update needs it. Without
auto-unlock, LND stays locked after such a restart. Lightning then stops, and
the node cannot protect your channels until you log in and unlock the wallet.
Nothing tells you that this happened.

To switch auto-unlock on or off later, open **System**, select lnd and press
`u`. The password is kept in a file only LND can read and never leaves the
server.

## The dashboard

The TUI has a sidebar with five sections and a dark and light theme toggle:

- **Channels:** open channels with coin control, close and manage channels,
  Node Info (pubkey, URIs, QR codes for sharing), channel history
- **Wallet:** send and receive Lightning payments, payment history, pairing a
  phone wallet
- **On-Chain:** send and receive on-chain, coin control, transaction history
  with anchor sweep detection
- **Add-On:** install and manage Syncthing for channel backups
- **System:** services and logs, accounts and SSH keys, auto-unlock, P2P
  mode, settings, [updates](updating.md)

Detail views open in tabs within each section. Press Ctrl+C to quit to the
`vpn` shell, where the node commands work:

```bash
bitcoin-cli getblockchaininfo
bitcoin-cli getpeerinfo

lncli getinfo
lncli walletbalance

systemctl status bitcoind
systemctl status tor@default
systemctl status lnd
systemctl status syncthing
```

## Services

On **System**, select a service with the arrow keys. Press `a` to start it,
`s` to stop it, `r` to restart it and `l` to read its log. Start, stop and
restart ask you to confirm first. Tor's controls and
log are those of `tor@default`, the Tor that does the work.

A finished Start or Restart means systemd reports the service running, and a
finished Stop means it reports it stopped. That does not mean the wallet is
unlocked, Bitcoin Core is synced or the network is reachable.

LND can stop by itself, for example when Bitcoin Core or Tor stops answering
for several minutes. systemd does not start it again after such a clean stop.
If System shows LND stopped, select lnd and press `a`.

If an action says its outcome is unconfirmed, the server may still have done
the work. Check the service status and log before you try again. Leaving the
TUI does not undo an action the server accepted.

## Updates and restarts

**System → Updates** holds two actions:

- **Node Updates** updates vpn, Bitcoin Core, LND and Syncthing together. See
  [Updating Your Node](updating.md).
- **Update Packages** refreshes Debian's package lists, upgrades the packages
  and keeps your existing configuration where a package asks. Its result stays
  on the Updates screen when you leave it and come back. A finished run means
  those commands succeeded. It does not mean every service is healthy or that
  no restart is needed. Closing the TUI does not stop a package update the
  server already started. If the outcome is unconfirmed, check the package
  state and log before you try again.

When a restart is needed, **System** shows a **Reboot** button. "Reboot
requested" means systemd accepted the request, not that the server has
restarted. If SSH closes during the shutdown, reconnect after the server is
back and check its state before you request another restart.

## Connecting Zeus

Open **Wallet → Pairing** to connect Zeus. Scan a QR code or copy the
connection details. Tor pairing always works, and clearnet pairing is added
when the node is in Hybrid mode.

The details are read each time you open the pairing tab or return to it. If
reading fails, **Retry** shows, and no earlier details are reused. Showing the
details does not prove that Zeus can reach or log in to LND.

### Tor only (default)

1. Open **Wallet → Pairing**
2. In Zeus: Advanced Set-Up → LND (REST)
3. Scan the QR code, or copy the server address, the REST port (8080) and the
   macaroon

### Clearnet and Tor (Hybrid mode)

1. Switch to Hybrid mode, see P2P mode below
2. Open **Wallet → Pairing**
3. Both the clearnet (IP:8080) and the Tor connection details are shown
4. On the first clearnet connection, accept the self-signed certificate
   warning. The connection is encrypted with LND's TLS certificate, which LND
   renews by itself.

Clearnet is faster. Tor is more private. Both use the same macaroon. See
Zeus's own documentation for setting it up on your phone.

## Sharing your node

**Channels → Node Info** shows what a peer needs to open a channel with you:

- Node alias, pubkey, LND version
- Peer count, active channels, node capacity
- Outbound, inbound, on-chain and total spendable balances
- QR codes for your advertised URIs (Tor, clearnet or both)
- A **Copy URIs** button that shows the URIs in a terminal view, labelled
  clearnet and Tor, ready to copy

## P2P mode

LND is installed Tor only. To switch to Hybrid mode, open **System**, select
lnd and press `p`:

- **Tor only:** the most private, all LND connections through Tor
- **Hybrid (Tor and clearnet):** better routing, your server's IP address is
  published to the Lightning Network

The switch is one way. Once your IP address is in the network's gossip, it
cannot be taken back. Hybrid mode is meant for a VPS, never for a node at
home. See [Hosting Your Node](hosting.md#hybrid-mode).

## Where files live

| Path | Contents |
| --- | --- |
| /etc/bitcoin/bitcoin.conf | Bitcoin Core configuration |
| /etc/lnd/lnd.conf | LND configuration |
| /etc/syncthing/ | Syncthing configuration |
| /etc/vpn/config.json | The node's settings chosen in vpn |
| /var/lib/bitcoin/ | Bitcoin Core data |
| /var/lib/lnd/ | LND data, wallet and `channel.backup` |
| /var/lib/syncthing/ | Syncthing data |
| /var/log/vpn.log | vpn's log (install, verification, status) |

vpn writes these configuration files itself, and an update can write
`lnd.conf` again. Changes made by hand are not supported and may be lost. The full
layout is in [How vpn Works Inside](developing.md#directory-layout).
