# Running Your Node

This guide covers everyday use of a Virtual Private Node (Node) after
installation: logging in, SSH keys, the dashboard, services, updates and
restarts. To install, see [Release Verification](verifying.md).

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
where sudo is available. The same account password
authorizes unrestricted sudo for server maintenance. The shell and sudo stay available when
the TUI or its helper does not work. Accounts your provider set up are kept.

The TUI is designed in color. If your computer sets `NO_COLOR`, the screens
lose their colors, the red warnings included, so leave it unset when you log
in as `vpn`.

If the TUI gets stuck when you log in, this command skips it:

```bash
ssh -t vpn@YOUR_SERVER /bin/bash --noprofile --norc
```

Ctrl+Z pauses the TUI, also the one that opens when you log in, and `fg`
brings it back. Root SSH is switched off, but the root account stays. Keep your
provider's console or rescue access available.

## SSH keys and password login

Use **System → Accounts** to find supported SSH keys on other accounts on the
server, review one and import it into `vpn`. Or add a key directly from
**System → Accounts → vpn → Add Key**. Keys already set up for `vpn` need no
import.

After you have logged in with your key in a new SSH connection, you can switch
off password SSH for `vpn` from **System → Accounts → vpn**.

To change the account password, use **System → Accounts → vpn → Change
Password**. You type the current password, then the new one twice, in
Debian's own `passwd` prompts, and Debian's password rules apply.

## The dashboard

The TUI has a sidebar with one section, **System**, and a dark and light
theme toggle. System holds the services and their logs, accounts and SSH
keys, settings and [updates](updating.md).

Detail views open in tabs. Press Ctrl+C to quit to the `vpn` shell.

## Services

**System** lists the node's services. Today that is Tor. Select it with the
arrow keys. Press `a` to start it, `s` to stop it, `r` to restart it and `l`
to read its log. Start, stop and restart ask you to confirm first. Tor's
controls and log are those of `tor@default`, the Tor that does the work.

A finished Start or Restart means systemd reports the service running, and a
finished Stop means it reports it stopped. That does not mean the network is
reachable.

If an action says its outcome is unconfirmed, the server may still have done
the work. Check the service status and log before you try again. Leaving the
TUI does not undo an action the server accepted.

## Updates and restarts

**System → Updates** holds two actions:

- **Node Updates** installs a new release of vpn. See
  [Updating Your Node](updating.md).
- **Update Packages** runs Debian's daily update now and keeps your existing
  configuration. If an update needs a restart, the Updates screen says so, and
  the node restarts during its next daily update, between 06:00 and 07:00
  server time. Select **Reboot** on System to restart sooner. The package
  update's result stays on the Updates screen when you leave it and come
  back. A finished run means
  those commands succeeded. It does not mean every service is healthy or that
  no restart is needed. Closing the TUI does not stop a package update the
  server already started. If the outcome is unconfirmed, check the package
  state and log before you try again.

System shows a Security updates box only when something needs you, such as a
restart that waits or is late, no successful nightly update for two days, or
the nightly updates switched off. Its red lines and a stopped service put a
red dot before System in the sidebar. After Update Packages,
"No updates for N days" stays until the next nightly update succeeds.

When a restart is needed, **System** shows a **Reboot** button. "Reboot
requested" means systemd accepted the request, not that the server has
restarted. If SSH closes during the shutdown, reconnect after the server is
back and check its state before you request another restart.

## Where files live

| Path | Contents |
| --- | --- |
| /etc/tor/torrc | Tor's configuration, written by vpn. Debian's own copy is kept as /etc/tor/torrc.distrib |
| /etc/vpn/config.json | Written when the install finishes. This release has no setting to keep in it |
| /home/vpn/releases/ | Release files you bring to the node yourself, see [Updating Your Node](updating.md#updating-from-files) |
| /var/log/vpn.log | vpn's log (the install, release checks, the TUI and the node's root work). Past 10 MB, once a day, it moves to vpn.log.1.gz. Four such files are kept |

vpn writes `/etc/tor/torrc` itself. Changes made by hand are not supported.
The full layout is in [How vpn Works Inside](developing.md#directory-layout).
