# How vpn Works Inside

For maintainers and contributors. This page gives the exact behavior behind
the owner guides, where an owner guide says only what to do. Releases and managed updates are in
[Releasing](releasing.md). The development and test gates are in
[CONTRIBUTING.md](../CONTRIBUTING.md).

## Installer

The installer takes a transient whole-run lock, checks the environment, and
refuses with a full report before starting durable installation work if the
machine is not one it can trust. It then asks for the owner account's password,
sets up the machine in 16 steps, with every package after the Tor check coming
through Tor, and then shows how to log in as `vpn` over SSH, where the TUI
opens.

During interactive installation, Ctrl+C asks for a pause. A step that is
waiting, for Tor or for Debian's own update, stops waiting at once. A command
that is running, such as apt, is never interrupted. The pause then comes when
the step ends, or, in the two steps that run more than one apt command, before
the next one. A step that was paused part way is not recorded, so the next run
does it again.
Keep the terminal open while it pauses. The installer keeps its lock until
that work settles, and changes are not undone. If the last step finishes, the
installer still publishes completion. Forced process death can leave
unrecorded work that a later run must repeat.

If a step fails or installation stops, inspect the reported result before
running it again. When the installer can prove that it is resuming the same
recognizable fresh-install lifecycle, it continues from the incomplete work. A
completed base installation reports already installed and stops.

A failed step shows one plain sentence where the installer has one. The failing
program's own words are in `/var/log/vpn.log`, in the lines marked "cause:"
under the step's FAILED line.

During installation, password paste preserves spaces and accepts one trailing
newline. Input that would be altered or exceed the field's 128-character limit
is rejected and cleared, rather than silently changing the password.

The installer never opens the TUI. When installation finishes, it shows the
`ssh` command and reads SSH's log every few seconds for a `vpn` login made
after the installer started. Enter or Ctrl+C closes the installer and prints
the command again. Before a login is seen, the installer first asks if you
want to leave, because root can no longer log in over SSH.

## Owner account and access

vpn leaves sudo credential caching at the host default. The TUI runs
unprivileged as `vpn`. Its privileged node operations go through the vpn API,
served by `vpnd`, a socket-activated root service with fixed, validated
requests and kernel peer-identity checks. Reads answer at once. Each change is
a job: `vpnd` records it under `/var/lib/vpn/private/jobs/`, starts it in its
own systemd service (`vpn-job-<id>.service`, through `systemd-run`) and
answers with its ID, and the TUI reads the job's record until it ends. One
change runs at a time, and a second is refused at once, naming the job in the
way. `journalctl -t vpnd` shows `vpnd` and every job.

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

## System actions and outcomes

System service actions keep their confirmed target while you navigate.
Completion means systemd reported `active` after Start or Restart, or
`inactive` after Stop. These checks do not establish network reachability.

An unconfirmed outcome means a job ended without a result, for example killed
or cut by a power cut, or that the TUI lost `vpnd`'s answers. It is never
retried. A job's outcome stays readable after the TUI closes and after a
restart: a running job always shows on the screen of its kind, a completed one
for 24 hours, a failed or unknown one until a newer job of its kind. Every
request carries the API number its TUI speaks, and `vpnd` refuses a number it
does not serve, so a TUI left open across an update may be asked to open
again.

Update Packages does not start while Debian's own update runs. It says so,
changes nothing, and the owner tries again a few minutes later. Otherwise it
refreshes the package lists, runs Debian's `unattended-upgrade` with vpn's rules
and ends with a `dpkg --audit` consistency check. A failed refresh stops the
upgrade. A failed step shows one plain sentence where vpn has one. The failing
program's own words are in `journalctl -t vpnd`, in the lines marked "cause:"
under the step's FAILED line. Install step 10 and the nightly job run the same
program with the same rules, which keep a configuration file the owner changed.

After every package change apt runs `vpn reboot-check`, from
`/etc/apt/apt.conf.d/99-vpn-reboot-check`. It raises Debian's restart flag,
`/var/run/reboot-required`, when the running kernel is older than the newest one
installed, or when Tor, SSH or fail2ban still runs a program or library file an
update replaced. vpn carries its libraries inside its program, so it is not
checked. Install step 10 and Update Packages also run the check directly,
since with nothing to install apt runs no hook. The check never makes apt fail. When the flag is up, Debian's nightly job
restarts the node right after its run. The install, Update Packages and a node
update hold a systemd block inhibitor, so a restart then waits for the next
night.

Reboot reports "Reboot requested" only after systemd accepts the request. This
confirms neither shutdown nor a successful restart. The outcome stays on System
across navigation, and pending or accepted requests cannot be submitted again in
that terminal session. If the reply is lost, including when SSH closes during
shutdown, the outcome is unconfirmed. Closing the TUI cannot undo a reboot
already accepted by systemd.

## Directory layout

Everything vpn writes on the machine is in the three tables below. They hold
vpn's own files, what Debian's packages and tools set up when vpn asks them,
and the one file of Debian's that vpn changes.

vpn's own files.

| Path | Contents |
| --- | --- |
| /usr/local/bin/vpn | The program. A Node Update replaces it |
| /etc/vpn/config.json | Root-owned, written when the install finishes, root:vpn mode 0640. It holds its schema number alone |
| /etc/apt/apt.conf.d/99-vpn-apt-tor | apt through Tor only, with plain http and https off, and apt pointed at vpn's folder of sources |
| /etc/apt/vpn.sources.d/vpn-debian.sources | Debian's archives over tor+https, the only sources apt reads. The machine's own source files stay as they were, unread |
| /etc/apt/apt.conf.d/99-vpn-nightly | vpn's values for Debian's nightly job. It is read after the two files of Debian's unattended-upgrades package, which stay as they were, and clears what they set for the job first |
| /etc/apt/apt.conf.d/99-vpn-reboot-check | apt's hook for the reboot check |
| /etc/sysctl.d/99-disable-ipv6.conf | IPv6 turned off |
| /etc/logrotate.d/vpn | Rotation of vpn's log |
| /etc/fail2ban/jail.local | The SSH jail |
| /etc/ssh/sshd_config.d/00-vpn-hardening.conf | Root's SSH login off, and the `vpn` account's logins |
| /etc/sudoers.d/vpn | sudo for the `vpn` account, with its password |
| /etc/systemd/system/tor@default.service.d/90-vpn-restart.conf | Tor restarted 30 seconds after a crash |
| /etc/systemd/journald.conf.d/zz-vpn-journal.conf | System logs kept on disk |
| /etc/systemd/system/vpnd.socket and vpnd.service | The units of vpnd, the node's root service |
| /etc/systemd/system/vpn-update.service | The unit a Node Update runs in, written by the first Node Update |
| /home/vpn/.bash_profile | Opens the TUI at an SSH login |
| /home/vpn/.ssh/authorized_keys | The SSH keys the owner adds on System, Accounts |
| /home/vpn/.config/vpn/preferences.json | User-owned TUI preferences (theme), vpn:vpn mode 0600 |
| /home/vpn/.cache/vpn/latest-version | The newest stable release found, kept for a while so the next look does not ask again |
| /home/vpn/releases/ | The owner's releases folder, vpn:vpn mode 0700. Its contents are read as untrusted |
| /var/lib/vpn-install-bootstrap-service-layout-2 | Short-lived root-only indication while initial lifecycle authority is published |
| /var/lib/vpn/private/layout-version | Root-only supported-layout generation record |
| /var/lib/vpn/private/install-state.json | Root-only bounded base-install progress and completion ledger |
| /var/lib/vpn/private/password-pending | Root-only interrupted unattended-password delivery marker, present only while needed |
| /var/lib/vpn/private/firewall-rules | Root-only copy of the firewall's rules as the install set them, to compare with |
| /var/lib/vpn/private/jobs/ | Root-only records of the vpn API's jobs |
| /var/lib/vpn/private/updates/ | Root-only managed update jobs and their records |
| /run/vpn/install.lock | Stable root-only runtime lock for one active base installer |
| /run/vpnd.sock | vpnd's socket, root:vpn mode 0660, made by systemd from vpnd.socket |
| /run/vpn/operations.lock | Root-only lock for accepting one change at a time, held by a Node Update while it runs |
| /var/log/vpn.log | Application log, its lines marked install, verify, tui or system, rotated by /etc/logrotate.d/vpn past 10 MB, copied then emptied in place, four compressed copies |

Set up by Debian's packages and tools when vpn asks, and two places of
Debian's own that vpn writes the way Debian does.

| What vpn asks | What Debian sets up |
| --- | --- |
| `apt-get` installs sudo, gnupg, tor, torsocks, apt-transport-tor, wget, curl, ufw, logrotate, ca-certificates, a system bus and a clock service where the machine has none, libnss-myhostname, unattended-upgrades, fail2ban, nftables and systemd-zram-generator | Each package's own files and settings. libnss-myhostname adds itself to /etc/nsswitch.conf, so the machine's name resolves with no line in /etc/hosts. unattended-upgrades keeps its /etc/apt/apt.conf.d/20auto-upgrades and 50unattended-upgrades |
| `adduser`, `chpasswd` and `usermod` | The `vpn` account, its group, its home /home/vpn, its password and its place in the systemd-journal group, in Debian's account files |
| `ufw default`, `ufw allow` and `ufw enable` | ufw's rules and state under /etc/ufw, and the default policies in /etc/default/ufw |
| `systemctl enable` for Tor, fail2ban and vpnd's socket, and `timedatectl set-ntp` | systemd's links under /etc/systemd/system |
| vpn makes /var/log/journal where it is missing, then `systemd-tmpfiles` and a restart of journald | The journal's folder with its group and rights, as Debian's systemd README has it |
| `sysctl --system` | The settings of /etc/sysctl.d applied |
| vpn's reboot check writes /run/reboot-required and /run/reboot-required.pkgs | Debian's own restart flag, which Debian's kernel packages raise too. Debian's nightly job restarts the machine when it is up |

The one file of Debian's that vpn changes.

| Path | Change |
| --- | --- |
| /etc/default/ufw | `IPV6=yes` becomes `IPV6=no`, so the firewall drops all IPv6. To undo it, set `IPV6=yes` there and run `ufw reload` |

vpn does not write /etc/tor/torrc, Debian's own file. The install refuses a
machine where it holds settings.

`/etc/vpn/config.json` is written once every install step is recorded. It is
how the TUI and the update tell a finished install. The install record,
`install-state.json`, stays the authority, which only root reads.
