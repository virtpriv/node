# Updating Your Node

An update installs a new release of vpn. You review it and approve it in the
TUI. Nothing is installed without your approval.

## How to update

1. Open **System → Updates → Node Updates**. It shows the newer release as
   "Available", or "No update available."
2. Select **Review**. The node downloads the release through Tor and checks
   its signature. Then it shows the current and new version of vpn and the
   release's summary.
3. Select **Install** to approve it. The update checks the node and the free
   space, then replaces the `vpn` program.
4. You can close the screen while the update runs. It continues, and the
   screen shows it again when you come back. While it runs, the node holds
   off restarts. Debian's daily restart waits, and **Reboot** is refused. A
   restart from your provider's panel may cut the power instead. The update
   then continues by itself when the node is back.
5. When the screen says "Update complete.", select **Relaunch** to open the
   new version.

## Updating from files

You can bring a release's files to the node yourself, so it downloads no
release file from GitHub. Use this to check each release on your own
computer first, or when GitHub does not answer over Tor. The node checks the
files exactly as it checks a download.

Put the release's three files in `/home/vpn/releases`, the releases folder
the install made. Either download them on the node, logged in as `vpn`:

```bash
VERSION="X.Y.Z"
cd ~/releases
torsocks wget -q "https://github.com/virtpriv/node/releases/download/v${VERSION}/vpn-${VERSION}-x86_64.tar.gz"
torsocks wget -q "https://github.com/virtpriv/node/releases/download/v${VERSION}/vpn-${VERSION}-manifest.txt"
torsocks wget -q "https://github.com/virtpriv/node/releases/download/v${VERSION}/vpn-${VERSION}-manifest.txt.asc"
```

Or copy them from your own computer, after checking them there as in
[Verify on your own computer instead](verifying.md#verify-on-your-own-computer-instead):

```bash
scp vpn-X.Y.Z-x86_64.tar.gz vpn-X.Y.Z-manifest.txt vpn-X.Y.Z-manifest.txt.asc vpn@YOUR_SERVER:releases/
```

Node Updates then shows the release as "Available" with "In your releases
folder.", and the review says the release comes from that folder. All three
vpn files must be there. With one or two the review is
refused and names the file that is missing. If a check fails while a copy is
still running, review again when it ends.

The node reads the release key from this repository and from
keys.openpgp.org. If neither gives the key, it uses `ripsline.asc` in the
releases folder, if you put a copy there. Save it under that name as in
[Release Verification](verifying.md). The review then says the key server was
not reached.

After an update is complete and you relaunch, vpn removes its own release
files that are not newer than the version now running. Newer ones and other
files stay, `ripsline.asc` too. If the folder is missing, make it again,
logged in as `vpn`:

```bash
mkdir -m 700 ~/releases
```

## If an update stops

A failed step shows its error under the step. The screen then says whether
the update changed anything:

- **Nothing was changed:** select **Retry**, or **Cancel update** to leave
  the node as it was.
- **The update may have changed this machine:** select **Retry**. Cancel is
  no longer offered, because the update must finish or be repaired.

If the screen says "The update is not running. Retry continues it.", select
**Retry**. Press `l` to read the update log.

## How updates are checked

- **The vpn release:** signed with the same release key you checked at
  install, with the pinned fingerprint
  `AFA0 EBAC DC9A 4C4A A7B0  154A C97C E10F 170B A5FE`. The node reads the
  key both from this repository and from keys.openpgp.org, so a revocation
  published in either place is seen. A signature from a revoked key is
  refused. If the keyserver cannot be reached, the review says "Key server
  not reached. A new key revocation would be missed."
- **Which release:** the node offers the newest stable release, from GitHub's
  latest release or your releases folder, and remembers GitHub's answer for
  up to a day. Release candidates are never offered by themselves.
- **Older nodes:** a node that is several releases behind is guided through
  them one at a time, each one its own reviewed update.

## Testing a release candidate

On a test node only, open **System → Settings → Test release** and enter the
exact release tag of the candidate. This selects one update. If that
release's files are in your releases folder, they are used. Normal updates
keep offering stable releases only.

## What an update does not do

An update does not install or remove Debian packages or restart the server.
Debian's own security updates continue separately, see
[Running Your Node](running.md#updates-and-restarts).

Do not upgrade Debian to a new major version yourself. Updates need Debian 13,
so a node you upgraded takes no further update.

How releases and updates work inside is in [Releasing](releasing.md).
