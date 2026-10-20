# Recovering Your Node

What to keep safe, what a restore can and cannot do, and what to do when the
disk fills up.

## What to keep safe

- **Your 24-word seed,** written on paper when you created the wallet.
- **`channel.backup`,** LND's static channel backup. The Syncthing add-on
  keeps a copy on your own device, updated whenever it changes and
  your device is connected. See the
  [Syncthing Setup Guide](syncthing.md).

The seed and `channel.backup` are the supported way to recover from a lost
Node. Together they can recover your settled channel funds. The backup is
useless without the seed.

## What a restore does

A static channel backup does not bring your channels back. It asks the peers
you can reach to force close each channel, so your settled funds can be swept
back to your wallet on-chain. A peer that cannot be reached cannot close its
channel this way.

vpn has no restore screen, and it does not copy LND's databases or offer a
way to copy them while LND runs. Read LND's
[operational safety](https://github.com/lightningnetwork/lnd/blob/master/docs/safety.md)
and [recovery](https://github.com/lightningnetwork/lnd/blob/master/docs/recovery.md)
guides before you attempt a restore or move a whole node.

Never run two LND nodes from the same seed at the same time.

## Moving a healthy node

Do not use the static channel backup to move a node that still works. It
force closes every channel. Close the channels cooperatively instead, wait for
the closes to confirm, move the funds to a wallet you control, then install a
fresh Node and open new channels.
[MIGRATION.md](../MIGRATION.md) gives these steps for a node of the earlier
rlvpn generation.

## When the disk is nearly full

LND checks the free disk space every 12 hours. When the filesystem is down to
10% free, it checks twice and then stops itself cleanly. systemd does not
start it again after such a stop. Free some space, then open **System**,
select lnd and press `a` to start it.

vpn never deletes, recreates or repairs a database by itself.
