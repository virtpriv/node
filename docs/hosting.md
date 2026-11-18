# Hosting Your Node

A Virtual Private Node (Node) runs either on a rented server (a VPS) or on a
machine in your home. The software is the same. What differs is who holds the
machine and how you reach it.

## The short version

| | VPS | At home |
| --- | --- | --- |
| Who holds the machine | The provider | You |
| SSH administration | From anywhere | Only from your home network |
| Restarts | Provider maintenance, security restarts when an update needs one | Power cuts, internet outages, security restarts when an update needs one |

## Privacy and control

On a VPS, the provider runs the machine and can access its disk, and it knows
who pays for the server. On a machine at home, the hardware is yours.

Updates and downloads go through Tor in both cases. Your internet provider at
home sees that the machine uses Tor. The few connections that do not use Tor
are listed in the README under
[Privacy & Network Traffic](../README.md#privacy--network-traffic).

## Reaching the Node to administer it

You administer the Node over SSH as `vpn`.

- **VPS:** the server has a public IP address, so you can log in from anywhere.
  SSH is one of the connections that do not use Tor.
- **At home:** your router does not let outside connections in, so you can log
  in only from a device on your home network. Forwarding a port on your router
  to reach SSH from outside would show your home IP address to anyone who
  connects.

## Restarts and outages

The Node checks for Debian's updates every day, security fixes included,
installs them by itself and restarts right after when an update needs it. The
Node's services start by themselves after every restart.

- **VPS:** the provider may also restart the server for maintenance.
- **At home:** power cuts and internet outages stop the Node too. Many
  machines have a setting to switch on again by themselves when power
  returns.

## Practical steps at home

- **Find the Node's address on your home network.** On the Node, `hostname -I`
  prints it, for example `192.168.1.20`. Your router's list of connected
  devices shows it too. Use this address for SSH.
- **Keep that address fixed.** In your router, reserve the address for the
  Node (often called a DHCP reservation). Otherwise the router may give it a
  new address after a restart, and SSH on your devices stops finding it.
- **Open no ports on your router.** Tor needs none, and SSH is meant for
  your home network only.

## Requirements

Both need a fresh Debian 13 amd64 machine with 2 CPUs, 4 GB of RAM or more and
90 GB of SSD storage or more. The installer refuses a machine with less than
3.4 GB of memory or 80 GB of disk. Its software must come only from Debian.
The installer checks all of this before it changes anything but its own log,
`/var/log/vpn.log`, and a lock file under `/run` that a restart clears. At
home, use a machine dedicated to the Node. See [Release
Verification](verifying.md) to install.

These sizes are the ones the Bitcoin and Lightning node needed. What the
base needs has not been measured yet.

When the kernel supports it, the installer also adds compressed swap in
memory (zram, from Debian's systemd-zram-generator package). When memory
runs short, the kernel compresses memory that is not in use instead of
stopping a program. It never writes to the disk. It is a cushion, not more
memory.

If your provider set up swap on disk, the installer leaves it. Memory moved
there stays on that disk. To switch it off, find its name
with `swapon --show`, run `sudo swapoff` with that name, for example
`sudo swapoff /swap.img`, and remove its line from `/etc/fstab`. Do not use
`swapoff -a`, which also switches off the compressed swap.
