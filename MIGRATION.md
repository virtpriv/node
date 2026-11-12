# Moving from `rlvpn` to `vpn` for v0.7.0

v0.7.0 does **not** support an in-place migration from an existing `rlvpn`
installation or an older unmarked `vpn` layout.

The service-identity design changes Linux accounts, directory ownership,
credentials, service units, helper authority, and project state boundaries.
An automatic conversion of a live Lightning node has not been designed or
validated across those changes.

Do not copy the old `/etc/rlvpn/config.json` into `/etc/vpn`, do not run the
v0.7.0 installer over the old node, and do not manually change ownership below
Bitcoin Core, LND, Tor, or Syncthing data directories to make the installer
continue.

## Supported path for an existing node

Plan a move to a clean Debian 13 amd64 installation. This can be a newly
provisioned machine or an existing machine that has been fully reimaged. It
must not contain the old project installation.

1. Keep the existing node healthy and online while planning the move.
2. Cooperatively close Lightning channels.
3. Wait for every close to confirm and for the resulting funds to become
   spendable.
4. Move funds to a wallet you control and verify the result independently.
5. Preserve the seed and required disaster-recovery material securely.
6. Provision or reimage a fresh supported Debian 13 amd64 machine.
7. Install v0.7.0, create a new LND wallet with a new seed, and reopen channels
   deliberately.

Do not use a static channel backup as the normal way to move a healthy node.
Static channel backup recovery is an emergency procedure that force-closes
channels. It is not a substitute for cooperative closes. Never operate two LND
instances from the same seed.

If the old node cannot be operated safely enough to close channels, stop and
follow LND's disaster-recovery guidance for the exact failure instead of
improvising a migration.

## Fresh-install interruption is different

The v0.7.0 installer can conservatively resume a recognizable interruption of
the fresh installation that it started. That recovery applies only to the
current supported layout and valid base-install progress state.

It is not an in-place migration, a general reinstall facility, or a repair tool
for pre-existing node state. A completed base installation reports that it is
already installed and stops. Optional add-ons and later settings are managed
through their dedicated interfaces.

## Future migration support

In-place migration remains future architecture work. It requires a separately
reviewed design covering source versions, backups, preconditions, service stop
order, ownership and path changes, rollback, interruption recovery, wallet and
channel integrity, Tor identities, Syncthing state, and operator recovery. No
future release should be assumed to support migration unless its release notes
and documentation explicitly say so.
