# How vpn Works Inside

For maintainers and contributors. This page gives the exact behavior behind
the owner guides, where an owner guide says only what to do. Releases and managed updates are in
[Releasing](releasing.md). The development and test gates are in
[CONTRIBUTING.md](../CONTRIBUTING.md).

## Installer

The installer takes a transient whole-run lock, checks the environment, and
refuses with a full report before starting durable installation work if the
machine is not one it can trust. It then asks for the owner account's password,
installs Bitcoin Core and LND with every download after Tor routed through Tor
and verified before install, and then shows how to log in as `vpn` over SSH,
where the TUI opens.

During interactive installation, Ctrl+C requests a stop after the active step
and its progress record finish. Keep the terminal open while it stops. The
installer keeps its lock until that work settles. This does not kill an active
subprocess or undo changes. If the last step finishes, the installer still
publishes completion. Forced process death can leave unrecorded work that a
later run must repeat.

If a step fails or installation stops, inspect the reported result before
running it again. When the installer can prove that it is resuming the same
recognizable fresh-install lifecycle, it continues from the incomplete work. A
completed base installation reports already installed and stops. Optional
add-ons and later settings remain available through the TUI.

During installation, password paste preserves spaces and accepts one trailing
newline. Input that would be altered or exceed the field's 128-character limit
is rejected and cleared, rather than silently changing the password.

The installer never opens the TUI. When installation finishes, it shows the
`ssh` command and reads SSH's log every few seconds for a `vpn` login made
after the installer started. Enter or Ctrl+C leaves it, and the command is
printed again.

## Bitcoin network profiles

Mainnet is the default. Testnet4 is the public proof-of-work test network.
Default public signet is for testing only and uses Bitcoin Core and LND's
pinned built-in public-signet parameters. vpn does not override its challenge
or seed nodes.

| vpn profile | Installer selector | Core/LND state directory | Address HRP | BOLT11 prefix |
| --- | --- | --- | --- | --- |
| `mainnet` | none (default) | `mainnet` conventions | `bc` | `lnbc` |
| `testnet4` | `--testnet4` | `testnet4` | `tb` | `lntb` |
| `public-signet` | `--signet` | `signet` | `tb` | `lntbs` |

The chosen profile is recorded before installation work begins and cannot be
changed. A node will not reuse another profile's Core data, LND wallet,
macaroons, channel state, lifecycle authorization, or channel-backup source.
Reusing the machine for another profile requires a new disposable machine.
There is no in-place profile migration or switch.

`controlled-signet-v1`, raw `signet`, custom challenges, and custom seed-node
inputs are rejected and unsupported. vpn also does not install a faucet,
explorer, miner, block signer, second Lightning node, or scenario controller.
Testing profiles remain visibly marked in the TUI, and their coins have no
mainnet value.

## Owner account and access

vpn leaves sudo credential caching at the host default. The TUI runs
unprivileged as `vpn`. Its privileged node operations go through a
socket-activated root helper with fixed, validated operations and kernel
peer-identity checks.

**System → Accounts** lists `vpn` and supported login accounts, plus root as a
protected system account whose keys can also be imported. The screen does not
remove accounts or change their access. The account table summarizes observed
sudo policy. Unfamiliar or mixed policies need review, and failed observations
show as unavailable. **Account information** expands local groups, account IDs
and paths in place. Only conventional `.ssh/authorized_keys` files are
inspected for root, `vpn` and accounts using common interactive shells. Custom
key paths, SSH certificates and provider-managed access are not inventoried.
Restricted key entries are excluded without removing their options, and
incomplete observations are reported. Key discovery runs only when inspecting
accounts after installation. The source account of an imported key is kept.

The TUI changes the account password with Debian's native `passwd` as `vpn`,
without sudo. The TUI resumes when the command finishes. Root can still reset
account passwords through normal host maintenance.

## Wallet password check

After wallet creation, vpn gracefully stops and starts LND once to verify the
password, and reports success only after LND reaches its native `RPC_ACTIVE`
state, or `SERVER_ACTIVE` if it advances between checks. Network-profile
validation remains an independent installer responsibility, and password
verification does not wait for blockchain synchronization. If verification
fails, LND is left stably locked instead of entering a restart loop.

Auto-unlock uses an `lnd:lnd` 0400 local password file and verifies a new LND
invocation before publishing success.

## System actions and outcomes

System service actions keep their confirmed target while you navigate.
Completion means systemd reported `active` after Start or Restart, or
`inactive` after Stop. LND Start and Restart also refresh the staged TLS
certificate. These checks do not establish wallet unlock, blockchain
synchronization or network reachability.

An unconfirmed outcome can mean the helper connection was lost while the host
continued working. An older helper that supplies no verified result also
produces an unconfirmed outcome, so the TUI and helper must come from the same
build.

Update Packages runs a package list refresh, an upgrade that preserves existing
configuration on conflict, and a `dpkg --audit` consistency check. A failed
refresh stops the upgrade.

Reboot reports "Reboot requested" only after systemd accepts the request. This
confirms neither shutdown nor a successful restart. The outcome stays on System
across navigation, and pending or accepted requests cannot be submitted again in
that terminal session. If the reply is lost, including when SSH closes during
shutdown, the outcome is unconfirmed. Closing the TUI cannot undo a reboot
already accepted by systemd.

Zeus connection information, its QR codes and **Copyable Macaroon** use one
observation, read when the pairing tab is opened or revisited.

## LND database

Fresh v0.7.0 installations use LND's local SQLite backend and native SQL
stores. vpn does not migrate an existing bbolt node, and the database settings
must not be changed after initialization. SQLite's WAL, checkpoint, locking,
and durability behavior remains at the pinned LND release's defaults.

LND checks disk space every 12 hours and makes two attempts before gracefully
stopping when filesystem free space reaches 10%. Because `Restart=on-failure`
does not restart that successful safety shutdown, an operator frees space and
starts LND explicitly. vpn does not synchronize the SQLite databases or
advertise a live database-copy procedure.

## Bitcoin Core credentials

vpn and LND authenticate to Bitcoin Core with separate `rpcauth` identities.
Bitcoin Core RPC-cookie generation is disabled for fresh v0.7 installations,
and neither client reads a Core cookie or data directory. Unsupported legacy
layouts are refused rather than modified, so vpn does not delete their
historical cookie files.

## Syncthing

Pairing a device adds it and shares the backup folder. Completion confirms a
read-back of the Node's configuration. A device may have been added even when
sharing was not confirmed. An explicit pairing retry preserves that device's
settings and completes a missing backup share. It does not reinstall Syncthing
or roll back earlier changes. Editing the Node's Syncthing configuration in its
web UI while the TUI pairs or removes a device can overwrite one change with the
other, and Syncthing's API cannot prevent that.

Removal identifies the selected device by its complete Device ID. It removes
that device and its folder shares on the Node. Remote files remain intact. An
unconfirmed removal requires checking the current device list before retrying.

The Node publishes only a completed copy of `channel.backup` through its own
`/var/lib/vpn/exports` boundary, by a dedicated `lnd` publisher. No temporary
file is placed in the synchronized folder. Syncthing can read only that final
export. It cannot write it, enter the private staging directory, or read
`/var/lib/lnd`.

The TUI refuses pairing when it cannot verify the Node's privacy settings or
send-only backup boundary. During installation, a failed privacy check triggers
stop and disable, and any failure of those actions is reported separately.
Reinstalling over retained Syncthing state is not a supported repair.

The **Web UI** tab refreshes the Tor address and staged installation password
when opened or revisited. The URL is visible independently of **Show
Password**, and returning to the tab hides the password again. **Retry**
repeats an unsuccessful read. The displayed password does not track password
changes made in Syncthing's external web UI.

## Directory layout

| Path | Contents |
| --- | --- |
| /etc/bitcoin/bitcoin.conf | Bitcoin Core configuration |
| /etc/lnd/lnd.conf | LND configuration |
| /etc/syncthing/ | Syncthing configuration |
| /etc/vpn/config.json | Root-owned, non-secret desired node configuration, root:vpn mode 0640 |
| /home/vpn/.config/vpn/preferences.json | User-owned TUI preferences (theme), vpn:vpn mode 0600 |
| /var/lib/vpn-install-bootstrap-service-layout-1-{mainnet,testnet4,public-signet}-tor | Short-lived root-only indication while initial lifecycle authority is published |
| /var/lib/vpn/private/layout-version | Root-only supported-layout generation record |
| /var/lib/vpn/private/install-state.json | Root-only bounded base-install progress and completion ledger |
| /var/lib/vpn/private/password-pending | Root-only interrupted unattended-password delivery marker, present only while needed |
| /var/lib/vpn/private/key-verification-pending | Root-only first-login verification marker, present only while needed |
| /var/lib/vpn/private/updates/ | Root-only managed update jobs and their records |
| /run/vpn/install.lock | Stable root-only runtime lock for one active base installer |
| /var/lib/vpn/state/ | Root-written facts and credentials deliberately staged read-only for the TUI, including the Syncthing web password |
| /var/lib/vpn/exports/lnd-backup/ | Read-only Syncthing export of the completed `channel.backup` |
| /var/lib/bitcoin/ | Mainnet Core data at the root, testnet4 under `testnet4/`, public signet under `signet/` |
| /var/lib/lnd/data/chain/bitcoin/{mainnet,testnet4,signet}/ | Profile-specific wallet and macaroon state in `chain.sqlite`, plus credentials and `channel.backup`. `signet/` is default public signet only |
| /var/lib/lnd/data/graph/{mainnet,testnet4,signet}/ | Profile-specific channel/graph KV state in `channel.sqlite` and native invoice, payment, and graph state in `lnd.sqlite` |
| /var/lib/lnd/data/watchtower/bitcoin/{mainnet,testnet4,signet}/ | Profile-specific `watchtower.sqlite`. LND creates it even when the watchtower server is disabled |
| /var/lib/syncthing/ | Syncthing's private data |
| /var/log/vpn.log | Application log (install, verification, status) |
