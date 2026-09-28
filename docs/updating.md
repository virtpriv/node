# Managed node updates

VPN's managed updater coordinates VPN, Bitcoin Core, LND, installed Syncthing,
and explicitly implemented host changes on Debian 13 amd64. This is a local
implementation awaiting disposable Debian integration validation. No actual
component transition is certified by the unit tests or by this document.

## Release authoring

The release archive contains `vpn` and `update.json`. The existing VPN signature
over `SHA256SUMS` authenticates both through the archive checksum. Component
archives are downloaded through Tor, checked against upstream signing pins, and
also checked against the exact archive hashes in the VPN plan. Discovery of a
GitHub release alone never authorizes installation.

Build a managed release from the repository root:

(local PC)

```sh
GOTOOLCHAIN=local go run ./cmd/release VERSION path/to/tested-update-plan.json
```

The command validates the plan against the checkout's component pins and
implemented host operations, builds and packages VPN, and signs locally with
the existing VPN release key. It produces the archive, `SHA256SUMS` and
`SHA256SUMS.asc` in `release/VERSION/`, refusing an existing version directory.
The exact reviewed plan bytes go into the archive. A one-argument build produces
a bootstrap archive without a managed plan. v0.7.0 requires a fresh installation; this
protocol does not provide an upgrade from v0.6.x or an unmarked installation.
The first test baseline must itself contain the managed updater. Once running
this implementation, the legacy binary-only helper request is refused so it
cannot bypass component and host changes.

The plan has these fields:

| Field | Meaning |
| --- | --- |
| `protocol` | `1` |
| `version` | Canonical VPN version without `v`: `MAJOR.MINOR.PATCH` or `MAJOR.MINOR.PATCH-rc.N`, where N is a positive integer without leading zeros |
| `platform` | `debian-13-amd64` |
| `summary` | Short plain-text release explanation |
| `minimum_free_mib` | Tested free-space requirement, at least 1024 MiB; checked on staging and affected data filesystems |
| `sources` | Exact tested objects containing `vpn`, `bitcoin`, `lnd`, `syncthing`; an empty Syncthing version means absent |
| `networks` | Explicitly supported installed profiles: `mainnet`, `testnet4`, `public-signet` |
| `bitcoin`, `lnd`, `syncthing` | Each has `version` and the upstream archive's lowercase `sha256` |
| `host_steps` | Ordered operation IDs implemented in the target worker |
| `host_services` | Optional affected component names for host steps; stopping Core also includes LND |
| `recovery_from` | Optional exact VPN archive digests this release can repair |

Do not invent checksums or mark a transition tested merely because it compiles.
Each supported source combination needs release-specific compatibility and data
migration testing. Sources are complete combinations, not independently mixed
version ranges. Component downgrades and older/equal VPN releases are refused.

The initial host operation is `lnd-service-v1`. It installs the reviewed LND
systemd template, preserves the configured auto-unlock choice, validates the unit,
and reloads systemd. Additional host operations require implementation in the
target worker, including repeat-safe preconditions and postconditions. The plan
cannot supply shell commands, file destinations, signing keys, or arbitrary URLs.

## Release candidates and publication

Publication is separate from preparation: the release command does not push
tags or upload files. GitHub Releases holds the signed release assets. A future
website can link to those same assets and release notes, giving browser downloads
and TUI updates one release source without another packaging process.

Publish numbered candidates such as `v0.7.0-rc.1` as GitHub prereleases in the
existing VPN repository. Use the normal release signing key, archive layout,
component verification and signed plan. Upload all three release assets to a
draft before publishing. The RC suffix and GitHub's prerelease flag are both
required by the publishing process. Keep published tags and assets unchanged;
any revised candidate receives a new RC number.

Normal discovery uses GitHub's latest stable release and rejects RC tags,
drafts and prereleases. It caches stable discovery for up to 24 hours. To test
an exact release, open System > Node Updates > Select release and enter its
tag. This bypasses latest-release discovery, including that cache, but retains
the normal Tor download, signature, source admission and explicit approval
path. No alternate repository, signing key or persistent testing channel is
used. An RC selection does not carry into a newly opened update screen. A saved
job retains its exact release and remains available for Retry after reconnect.

Use only disposable nodes without funds. For the first release, fresh-install
an RC baseline with the chosen source component versions, then use the TUI to
update to a later RC containing the intended component and host changes. For
subsequent releases, test each supported stable source directly to the new RC.
RC-to-RC success alone does not certify the stable source's database migration.
If a candidate fails after a component may have started, the same database
preservation and corrective-release requirements apply as for stable updates.

After RC qualification, build and sign the final stable version and its exact
plan from the approved source. Its version and manifest make this a different
artifact. Publish it initially with GitHub's prerelease flag still set and use
Select release to validate that exact stable tag on the test node. Keep it out
of normal discovery until its required checks pass, then clear the prerelease
flag and mark it latest without replacing its assets. A failed final artifact
must be superseded by a new version, not overwritten. Test the actual supported
source-to-stable transitions; updating an already migrated RC node only checks
that RC-to-stable transition. Returning from an RC to an older stable release
is a downgrade and remains refused.

## Runtime and recovery

The root helper verifies the VPN archive before the TUI shows its plan. Approval
binds the archive, observed component versions, network, configuration hash, and
any previous failed update. Each review has its own token, so a second terminal
cannot silently change what the first terminal approves.

On acceptance, VPN saves the job under `/var/lib/vpn/private/updates`, retains
the working launcher, and starts `vpn-update.service`. The verified target binary
implements the worker. The TUI can close and reconnect to the same saved job.
All helper mutations share a lock with the worker; read-only status remains
available. An unfinished record continues to block conflicting helper mutations
even if its worker has exited. Root's independent maintenance authority remains.

The worker stages and verifies all affected artifacts before downtime. It stops
LND before Core, atomically replaces each selected executable, then starts and
checks Core before LND. Syncthing is included only where installed. Core's daemon
and CLI are always covered; companion tools installed by earlier VPN versions
are updated too, without installing additional tools. No component data directory
is copied back, deleted, or restored.

Temporary systemd drop-ins disable automatic restarts and require a permit under
`/run` before each start. The worker durably records that a component may run
before creating that permit. It removes the permit after the start attempt.
A reboot clears permits. This prevents a half-installed component from starting
independently of recovery. Known failures stay stopped until reviewed and retried.
An uncertain start is checked against the surviving process; if the process is
absent, an explicit Retry is required. Retry uses the same approved release and
preserves the history that database changes may have occurred.

A failed download or verification can be cancelled before staging completes,
releasing the maintenance restriction without changing the running node. Once
host changes may have begun, cancellation is refused; completion or repair is
required instead. A corrective job also retains this restriction if its download
fails, because the earlier update may already have changed the node.

Health checks include the running executable, Core's version and full network
identity, LND's version/network and wallet readiness, and Syncthing's version,
device identity and privacy settings. A locked LND wallet keeps the job at
“Unlock wallet to finish”; native `lncli unlock` handles the password without
storing it in update records. Chain synchronization is separate runtime progress.
VPN's installed binary is replaced last. Normal updates restart services; they
do not request a host reboot.

Ordinary component updates require affected services to be running before
acceptance. A failed managed update instead uses its recorded wallet/device
identity. Corrective releases must acknowledge the exact failed release and the
observed executable combination; retained databases are used by the corrective
release. A corrective release is not a generic force-update or rollback option.

## Validation still required before release

Local tests exercise release admission, approval identity, selected tar members,
durable replacement, shared locking, service order, and interruption before and
after worker side effects. They simulate process loss and reboot; they do not
prove native systemd behavior, upstream migrations, or a released artifact's
signature.

Review a disposable Debian 13 Signet test slate before preparing a VPS:

1. A signed release that changes VPN, Core, LND and installed Syncthing, plus the
   LND unit step; repeat with Syncthing absent and with a VPN-only release.
2. Two TUIs, disconnect/reconnect, duplicate approval, and configuration drift
   between review and acceptance.
3. Process termination and reboot around service gating, each binary replacement,
   each first start, and final VPN publication.
4. Locked wallet, failed startup, bad signatures/checksums, insufficient space,
   explicit retry, and a signed corrective release preserving the same databases.
5. Confirm component identities, settings, companion binaries, credential staging,
   and restored normal service behavior after success.

This worker does not yet implement arbitrary Debian package migrations, automatic
database restoration, channel recovery, signing-key rotation, or reboot-requiring
host changes. Those need explicit release operations and their own validation.
