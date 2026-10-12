# Syncthing Setup Guide

Syncthing automatically syncs your LND `channel.backup` file from
your Virtual Private Node (Node) to your local device. If your Node
dies, your 24-word seed phrase and this file can recover settled channel
funds through LND's emergency recovery procedure. Recovery asks reachable
peers to force-close the channels. It does not restore them for continued use.

Syncthing encrypts all connections using mutual TLS authentication.
Only devices you explicitly approve can connect. The `channel.backup`
file is useless without your seed phrase.

### Where your device can sync

Your device connects straight to the Node's IP address on port 22000.
Discovery, relays and NAT traversal stay off, so no other server helps the
two find each other.

- **Node on a VPS with a public IP address:** your device can sync from
  anywhere.
- **Node at home:** your device can reach the Node only while it is on the
  same home network. The backup reaches a phone only when the phone is at
  home. Pair a device that stays at home, such as a desktop computer, so a
  current backup is always kept off the Node.

See [Hosting Your Node](hosting.md) for the other differences between a Node
at home and on a VPS.

### Step 1. Install Syncthing on Your Node

1. SSH into your Node as `vpn`
2. Open the **Add-On** section in the sidebar
3. Select **Syncthing** and press Enter to install

### Step 2. Install Syncthing on Your Local Computer or Phone

Download Syncthing for your operating system from
[syncthing.net](https://syncthing.net/downloads/).

- **macOS:** Download the `.dmg` or `brew install syncthing`
- **Windows:** Download the installer
- **Linux:** Install from your package manager
- **Android:** [researchxxl/syncthing-android](https://github.com/researchxxl/syncthing-android)

Start Syncthing. It opens a web UI at `http://127.0.0.1:8384`.

### Step 3. Configure Local Syncthing

In your local Syncthing web UI, go to **Actions → Settings →
Connections** and switch these off:

- **Global Discovery**
- **Local Discovery**
- **Relaying**
- **NAT Traversal**

Click **Save**. Your Syncthing now connects only directly
to your Node by IP address, with no third-party relay or discovery
servers involved.

### Step 4. Get Your Local Device ID

In your local Syncthing web UI:

1. Click **Actions** (top right)
2. Click **Show ID**
3. Copy the Device ID (looks like `XXXXXXX-XXXXXXX-...`)

### Step 5. Pair Your Device on the Node

In the Node dashboard:

1. Open the **Add-On** section in the sidebar
2. Select **Syncthing** and press Enter to open the management tab
3. Select **Pair Device** and press Enter
4. Paste your local Device ID
5. Confirm with the Pair button

The Node adds your device and shares the backup folder. Completion confirms a
read-back of the Node's configuration. The receiver still needs steps 6 and 7 below. Avoid editing
the Node's Syncthing configuration in its web UI while pairing or removing a
device in the TUI.

If pairing reports an incomplete or unconfirmed outcome, inspect the device and
`lnd-backup` share in the Node's web UI before retrying. A device may have been
added even when sharing was not confirmed. An explicit pairing retry preserves
that device's settings and completes a missing backup share. It does not
reinstall Syncthing or roll back earlier changes.

Removal identifies the selected device by its complete Device ID. It removes
that device and its folder shares on the Node. Remote files remain intact.
An unconfirmed removal requires checking the current device list before retrying.

### Step 6. Add the Node in Your Local Syncthing

In your local Syncthing web UI:

1. Click **Add Remote Device**
2. Paste the **Node Device ID** shown on the Syncthing post-pair
   screen in the TUI (or scan the QR code with a phone)
3. Under **Addresses**, replace `dynamic` with `tcp://<node-ip>:22000`
   using the same IP you SSH into. For a Node at home, that is its address
   on your home network.
4. Click **Save**

### Step 7. Accept the Backup Folder

After the devices connect, your local Syncthing will prompt you
to accept a shared folder called `lnd-backup`.

1. Click **Accept** (or **Add**)
2. Choose a local folder path, for example:
    - macOS: `~/lnd-backup`
    - Windows: `C:\Users\YourName\lnd-backup`
    - Linux: `~/lnd-backup`
3. Set the folder to **Receive Only**. Your device then never changes the
   backup, and the Node's copy stays the newest one.
4. Click **Save**

### Done

Your `channel.backup` file will sync automatically whenever:

- LND creates or updates the backup when it starts or a channel opens or closes
- Your local device is online and connected

The sync happens in seconds. You don't need to keep your computer
on all the time. Syncthing catches up the next time both
devices are online, and for a Node at home, the next time your device is on
the home network.

### Check the Backup in the TUI

Open the Syncthing management tab in the **Add-On** section. It refreshes
about every five seconds and after pairing or removal.

**Backup copy** is the Node's own copy in the shared folder. Whenever LND
writes a new backup, and every minute, the Node compares the copy with LND's
backup and replaces it when they differ.

- **up to date, checked 14:05:00:** the copy matched LND's backup at that time
- **checking now** or **not checked since boot:** wait a minute
- **checks are late**, **a check has been running since …** or **last check
  failed:** the copy may be behind LND. Read
  `journalctl -u lnd-backup-export`
- **checks are off:** start them with
  `sudo systemctl enable --now lnd-backup-check.timer`
- **unavailable:** the Node could not read the state of its checks

The **Backup** column shows, for each paired device, whether it holds the
Node's copy:

- **Up to date:** the device holds the Node's current copy
- **Receiving** with a percentage: the device is copying it now
- **Not connected:** the device is not connected right now. Syncthing knows a
  device's state only while it is connected, so an offline device never
  shows Up to date. The device's details show when it was last seen.
- **Not accepted:** the device is connected but has not accepted the
  `lnd-backup` folder (step 7)
- **Checking:** the device is connected and has not yet reported its copy
- **Paused:** the device or its folder is paused
- **Out of sync:** a device changed or deleted its copy, so the newest
  version in Syncthing is no longer the Node's. See Troubleshooting below.
- **Not shared:** the backup folder is not shared with this device
- **Unavailable:** the Node could not read Syncthing's status. It retries by
  itself.

Read both. A copy that is behind LND can still be Up to date on every
device. Browser changes appear after a successful refresh. The API cannot
prevent simultaneous edits from overwriting one another.

### Verify It's Working

Check that `channel.backup` appears in your local folder:

- **macOS/Linux:** `ls ~/lnd-backup/`
- **Windows:** Open the folder in Explorer

In the Node TUI, the Syncthing management tab shows **Backup copy: up to
date** and **Up to date** for your device. The Syncthing service shows as
running in the System section.

### Security

- Syncthing uses mutual TLS authentication. Only devices you
  approve can connect.
- The sync port (22000) rejects all unapproved devices immediately
- The `channel.backup` file is encrypted by LND and useless
  without your 24-word seed phrase
- The backup contains static recovery information, not current balances or
  commitment state. Restoring it closes channels and depends on reachable peers.
- The Node publishes only a completed copy through its own
  `/var/lib/vpn/exports` boundary. No temporary file is placed in
  the synchronized folder.
- Syncthing can read that final export but cannot write it, enter
  the private staging directory, or read LND's source data
- Discovery and relay servers are disabled. Your device connects
  directly to the Node by IP address.
- The Syncthing web UI on the Node is only accessible via Tor
  (not exposed to clearnet)

### Troubleshooting

**Privacy or API-key check failed:**

The TUI refuses pairing when it cannot verify the Node's privacy settings or
send-only backup boundary. Ask the host administrator to inspect the existing
configuration and staged credentials. Reinstalling over retained Syncthing state
is not a supported repair. During installation, a failed privacy check triggers
stop and disable. Any failure of those actions is reported separately.

**Devices not connecting:**

- Verify both devices are running (green dot in web UI)
- Check that the Node address is correct: `tcp://<node-ip>:22000`
- For a Node at home, check that your device is on the home network
- Check firewall: `sudo ufw status` should show port 22000 open

**Folder not syncing:**

- Check that the folder is shared with both devices
- Node side should be **Send Only**
- Local side should be **Receive Only**
- Check the Node's backup copy job and its timer with
  `sudo systemctl status lnd-backup-export.service lnd-backup-check.timer`
- Check Syncthing logs: **Actions → Logs** in the web UI

**Out of sync:**

A device whose folder is not Receive Only can change or delete its copy. The
Node's folder is Send Only, so it keeps its own copy, but Syncthing's newest
version is then the device's.

1. On each device, set the `lnd-backup` folder to **Receive Only**. If the
   folder shows **Revert Local Changes**, click it.
2. In the Node's Syncthing web UI (see below), open the `lnd-backup` folder
   and click **Override Changes**. The Node's copy then wins on every device.

**Web UI access on Node:**

The Syncthing web UI is available over Tor for advanced
configuration. Open the Syncthing management tab in the Add-On
section and select **Web UI**. The onion address and credentials
are displayed there. Use Tor Browser to access it.
