# Updating Your Node

An update brings a new vpn release together with the Bitcoin Core, LND and
Syncthing versions it was tested with, plus any server change it needs. You
review it and approve it in the TUI. Nothing is installed without your
approval.

## Before you update

- Create the LND wallet and unlock it, or switch on auto-unlock.
- Bitcoin Core and LND should be running. An update refuses to start while a
  service it changes is stopped, and says which one.

## How to update

1. Open **System → Updates → Node Updates**. It shows the newer release as
   "Available", or "No update available."
2. Select **Review**. The node downloads the release through Tor and checks
   its signature. Then it shows the current and new version of vpn and of each
   component, the release's summary and any server change.
3. Select **Install** to approve it. The services it changes restart.
   Downloads use Tor, and your databases are kept.
4. You can close the screen while the update runs. It continues, and the
   screen shows it again when you come back.
5. When the screen says "Update complete", select **Quit**, then type `vpn`
   and press Enter to start the new version.

If your wallet is locked when LND starts again, the screen says "Unlock the
LND wallet to finish." Select **Unlock wallet** and type your wallet
password. The keys you type are not shown.

## If an update stops

A failed step shows its error under the step, and for the cases vpn knows,
the next step to take:

- **LND did not stop:** the screen says this is expected when the wallet is
  locked or not created yet. Select **Retry** once to continue. If it fails
  again, read the error before you retry.
- **The update was interrupted, for example by a restart:** select **Retry**
  to continue.
- **A service is not running, or not running its installed version:** select
  **Cancel update**, start or restart that service on **System**, then review
  again.

For any other failure the screen says whether services were changed:

- **No services were changed:** select **Retry**, or **Cancel update** to
  leave the node as it was.
- **Services may have been changed:** select **Retry**. Cancel is no longer
  offered, because the update must finish or be repaired.

If the screen says "The update is not running. Retry continues it.", select
**Retry**. Press `l` to read the update log. Services the update stopped stay
stopped until it continues.

## How updates are checked

- **The vpn release:** signed with the same release key you checked at
  install, with the pinned fingerprint
  `AFA0 EBAC DC9A 4C4A A7B0  154A C97C E10F 170B A5FE`. The node reads the
  key both from this repository and from keys.openpgp.org, so a revocation
  published in either place is seen. A signature from a revoked key is
  refused. If the keyserver cannot be reached, the review says "Key server
  not reached. A new key revocation would be missed."
- **Bitcoin Core, LND and Syncthing:** checked against their own pinned
  signing keys, as at install, and against the exact archive hashes in the
  signed release plan.
- **Which release:** the node offers only GitHub's latest stable release and
  remembers the answer for up to a day. Release candidates are never offered by
  themselves.
- **Older nodes:** a node that is several releases behind is guided through
  them one at a time, each one its own reviewed update.

## Testing a release candidate

On a node without funds only, open **System → Updates → Node Updates →
Select release** and enter the exact release tag, such as `v0.7.0-rc.1`. This
selects one update. Normal updates keep offering stable releases only.

## What an update does not do

An update does not install or remove Debian packages, restore databases,
recover channels or restart the server. Debian's own security updates
continue separately, see [Running Your Node](running.md#updates-and-restarts).

How releases and updates work inside is in [Releasing](releasing.md).
