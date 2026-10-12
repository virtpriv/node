# Hosting Your Node

A Virtual Private Node (Node) runs either on a rented server (a VPS) or on a
machine in your home. The software is the same. What differs is who holds the
machine, how you reach it and where your backups can sync.

## The short version

| | VPS | At home |
| --- | --- | --- |
| Who holds the machine | The provider | You |
| SSH administration | From anywhere | Only from your home network |
| Syncthing backups | Sync from anywhere | Sync only on your home network |
| Wallet from a phone (Zeus over Tor) | Works anywhere | Works anywhere |
| Hybrid mode | Available | Not available, and not for home use |
| Restarts | Provider maintenance, 04:00 security restarts | Power cuts, internet outages, 04:00 security restarts |

## Privacy and control

On a VPS, the provider runs the machine and can access its disk, and it knows
who pays for the server. On a machine at home, the hardware is yours.

Bitcoin Core, LND, updates and downloads go through Tor in both cases. Your
internet provider at home sees that the machine uses Tor, but not that it runs
a Bitcoin node. The few connections that do not use Tor are listed in the
README under
[Privacy & Network Traffic](../README.md#privacy--network-traffic).

## Reaching the Node to administer it

You administer the Node over SSH as `vpn`.

- **VPS:** the server has a public IP address, so you can log in from anywhere.
  SSH is one of the connections that do not use Tor.
- **At home:** your router does not let outside connections in, so you can log
  in only from a device on your home network. Forwarding a port on your router
  to reach SSH from outside would show your home IP address to anyone who
  connects.

## Channel backups with Syncthing

Your device connects straight to the Node's address on port 22000, without
relays.

- **VPS:** your device syncs the backup from anywhere.
- **At home:** your device syncs only while it is on your home network. Pair a
  device that stays at home, such as a desktop computer, so a current backup
  is always kept off the Node.

See the [Syncthing Setup Guide](syncthing.md).

## Your wallet from a phone

Zeus connects to your Node over Tor, through LND's onion address. This works
from anywhere, also for a Node at home, because Tor needs no open port on your
router. See Zeus's own documentation for setting it up on your phone, and the
README under
[Connecting Zeus Wallet](../README.md#connecting-zeus-wallet) for the pairing
screen.

## Hybrid mode

Hybrid mode publishes the server's IP address to the Lightning Network for
better connectivity. It is meant for a VPS with a public IP address, and the
change cannot be undone. vpn refuses it when the server's own IPv4 address is a
private one, such as `192.168.1.20` behind a home router. If a machine at home
gets an address directly from your internet provider that is not private,
which is rare, vpn treats it like a VPS. Do not use Hybrid mode at home. It would publish your
home address.

## Restarts and outages

The Node installs Debian security updates by itself and restarts at 04:00
server time when an update needs it. Without auto-unlock, LND stays locked
after any restart, Lightning stops and the Node cannot protect your channels
until you unlock the wallet. Auto-unlock is strongly encouraged, for a Node at
home even more, because you can reach it only from your home network.

- **VPS:** the provider may also restart the server for maintenance.
- **At home:** power cuts and internet outages stop the Node too. When the
  machine starts again, the Node's services start by themselves, and with
  auto-unlock LND unlocks without you. Many machines have a setting to switch
  on again by themselves when power returns.

## Practical steps at home

- **Find the Node's address on your home network.** On the Node, `hostname -I`
  prints it, for example `192.168.1.20`. Your router's list of connected
  devices shows it too. Use this address for SSH and in Syncthing.
- **Keep that address fixed.** In your router, reserve the address for the
  Node (often called a DHCP reservation). Otherwise the router may give it a
  new address after a restart, and SSH and Syncthing on your devices stop
  finding it.
- **Open no ports on your router.** Tor and LND need none, and SSH and
  Syncthing are meant for your home network only.

## Requirements

Both need a fresh Debian 13 amd64 machine with 2 CPUs, 4 GB of RAM or more
and 90 GB of SSD storage or more. At home, use a machine dedicated
to the Node. See [Release Verification](verifying.md) to install.
