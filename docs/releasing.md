# Releasing

For maintainers and contributors. This page covers how a vpn release is built
and published, the update plan it carries, and how a managed update runs on a
node. Owners
updating their node read [Updating Your Node](updating.md).

A managed update replaces the `vpn` program on Debian 13 amd64. A plan can
also name changes to the machine, called host steps. This release implements
none.

## Release authoring

The release archive contains `vpn` and `update.json`. The vpn signature over
the release manifest authenticates both through the archive checksum.
Discovery of a GitHub release alone never authorizes installation.

The signature check follows one rule. The pinned signer counts only when gpg
reports its signature as good. A signature made with a revoked key, or a
revoked signing subkey, never counts, whatever date the signature carries. An
expired key still counts.

The node reads the vpn release key from two places on every review: the file
`keys/ripsline.asc` in this repository and keys.openpgp.org. It uses both
together, so a revocation or a new signing subkey published in either one is
seen, and only material signed by the pinned key has any effect. If the
keyserver cannot be reached, the review continues with the repository file and
says so on the review screen. If neither can be fetched, the review is refused.
Both addresses are fixed in installed nodes. Keep the repository file current
whenever the key changes, and keep at least one of the two addresses working
until nodes have installed a release that knows a replacement.

Build a managed release from the repository root:

(local PC)

```sh
GOTOOLCHAIN=local go run ./cmd/release -sign VERSION path/to/tested-update-plan.json
```

The command refuses a Git checkout with changes or with files Git does not
ignore. It validates the plan against the version being built and refuses a
plan that names a host step, then builds and packages vpn. It ignores Go
settings saved with `go env -w`, so set any you need, such as `GOPROXY`, in
the shell. The exact reviewed plan bytes go into the archive. A build without
a plan produces a bootstrap archive without a managed plan.

With `-sign` the command signs the manifest with the release key in your local
GPG keyring, which can prompt through your normal GPG setup, then checks the
signature with an isolated keyring and the updater's pinned signer policy. It
writes `vpn-VERSION-x86_64.tar.gz`, its manifest `vpn-VERSION-manifest.txt`
and, with `-sign`, the signature `vpn-VERSION-manifest.txt.asc` to
`release/VERSION/`, refusing an existing version directory. An ordinary failure
removes only the new incomplete directory. After a forced termination, inspect
any leftover directory before removing it and retrying.

The base requires a fresh installation. This protocol does not provide an
upgrade from v0.6.x, from a Bitcoin and Lightning node or from an unmarked
installation. The first test baseline must itself contain the managed updater.

The plan has these fields:

| Field | Meaning |
| --- | --- |
| `protocol` | `2`. Number 1 was the plan of the Bitcoin and Lightning node |
| `version` | Canonical vpn version without `v`: `MAJOR.MINOR.PATCH` or `MAJOR.MINOR.PATCH-rc.N`, where N is a positive integer without leading zeros |
| `platform` | `debian-13-amd64` |
| `summary` | Short plain-text release explanation |
| `minimum_free_mib` | Free space, at least 1024 MiB, that must remain where the update's files are kept, under `/var/lib/vpn/private/updates`. Checked before the program is replaced |
| `sources` | The exact tested versions this release updates from, each an object with `vpn` |
| `host_steps` | Ordered operation IDs implemented in the target worker. This release implements none |
| `recovery_from` | Optional exact vpn archive digests this release can repair |
| `bridge` | Optional earlier vpn version. A node this plan does not admit is told to install that release first. The bridge of a stable release must be stable |

Do not mark a transition tested merely because it compiles. Unit tests do not
certify a transition. Each supported source needs its own update test on a
node. Older or equal vpn releases are refused.

Each stable release admits the stable release directly before it and names
that release as its `bridge`. A node that skipped releases is guided through
them one at a time. Every hop is a full reviewed update with its own download.
When the releases in between made no host step, a release should also admit
those older sources, so that a node behind by several releases updates in one
step. Each admitted source still needs its own transition test, which is light
when no host change is skipped.

There is no host operation in this release. The release command and the
worker both refuse a plan that names one. A host operation requires
implementation in the target worker, including repeat-safe preconditions and
postconditions. The plan cannot supply shell commands, file destinations,
signing keys, or arbitrary URLs.

## Release candidates and publication

Publication is separate from preparation: the release command does not push
tags or upload files. GitHub Releases holds the signed release assets. A future
website can link to those same assets and release notes, giving browser downloads
and TUI updates one release source without another packaging process.

Publish numbered candidates, with a tag that ends in `-rc.N`, as GitHub
prereleases in the existing vpn repository. Use the normal release signing
key, archive layout and signed plan. Upload all three release assets to a
draft before publishing. The RC suffix and GitHub's prerelease flag are both
required by the publishing process. Keep published tags and assets unchanged.
Any revised candidate receives a new RC number.

Normal discovery uses GitHub's latest stable release and rejects RC tags,
drafts and prereleases. It caches stable discovery for up to 24 hours. Each
check makes up to three tries of 10 s, each on its own Tor circuit. After a
check that found nothing, the TUI asks again every 10 minutes while it stays
open. To test an exact release, open System > Settings > Test release and
enter its tag. This bypasses latest-release discovery, including that cache,
but retains the normal Tor download, signature, source admission and explicit
approval path. No alternate repository, signing key or persistent testing
channel is used. An RC selection is not kept as a setting. Its review is kept
as a job: a Node Updates screen opened later waits for a review that still
runs, and shows a finished one this TUI has not shown yet, for 24 hours. A
saved job retains its exact release and remains available for Retry after
reconnect.

A node also takes a release's three files from the owner's releases folder,
`/home/vpn/releases`, in place of the download, with the same checks. Prepare
copies them into its private folder before checking them, follows no link out
of the folder and reads only regular files, the archive up to 2 GiB and the
other files up to 1 MiB. The owner's `ripsline.asc` there is used only when
neither key source gives the pinned key. Every installed node reads that path
and the release file names, so they never change.

Use only disposable test nodes. For the first release, fresh-install an RC
baseline, then use the TUI to update to a later RC. For subsequent releases,
test each supported stable source directly to the new RC. RC-to-RC success
alone does not certify the update from the stable source. If a candidate
fails after the program may have been replaced, the same corrective-release
requirements apply as for stable updates.

After RC qualification, build and sign the final stable version and its exact
plan from the approved source. Its version and plan make this a different
archive. Publish it initially with GitHub's prerelease flag still set and use
Test release to validate that exact stable tag on the test node. Keep it out
of normal discovery until its required checks pass, then clear the prerelease
flag and mark it latest without replacing its assets. A failed final artifact
must be superseded by a new version, not overwritten. Test the actual
supported source-to-stable transitions. Updating an RC node only checks that
RC-to-stable transition. Returning from an RC to an older stable release is a
downgrade and remains refused.

## Runtime and recovery

Prepare Update is a job of the vpn API: `vpnd` starts it in its own service,
which verifies the vpn archive before the TUI shows its plan. Leaving the
screen does not stop it, and the review comes back from its record. Approval
binds the archive, the installed vpn version, the configuration hash, and any
previous failed update. Each review has its own token, so a second terminal
cannot silently change what the first terminal approves. A new review replaces
every earlier one, so Install on an older review in another terminal is
refused.

On acceptance, vpn saves the update's record under
`/var/lib/vpn/private/updates` with its job ID, retains the working launcher,
and starts `vpn-update.service`. The verified target binary implements the
worker. The TUI can close and reconnect to the same saved job. The worker
holds the operations lock for its run, and `vpnd` takes it briefly to accept
each change, both waiting up to 10 seconds for it before refusing. Reads
remain available. An unfinished record keeps every other change out even if
its worker has exited, and Start and Retry are refused while another job runs.
Root's independent maintenance authority remains.

The worker runs three steps, and every run does all three. "Check the node"
requires that the node's settings load and are the ones the review was made
for. "Check free space" counts each filesystem once: the plan's free space
where the update's files are kept, and 1024 MiB plus the new program where
`vpn` is installed. "Finish VPN update" checks the approved program against
its hash, copies it over `/usr/local/bin/vpn` and checks the hash again. The
two checks change nothing, and copying the same bytes again is safe, so a run
after a power cut or a Retry starts again at the first step. An update does
not touch Tor or SSH and does not request a host reboot. A `vpnd` still in
memory after it refuses its next request and exits, and the socket starts the
new program.

A refusal at one of the two checks leaves the node as it was. It can be
retried, after freeing space for example, or canceled. After a complete update
the release plan and the job record remain.

A new review first removes every earlier review's record, the files of
earlier reviews that never became an update and the folder of a review that
was stopped. The folders of updates stay. Every installed node's review does
this, so a later release keeps everything its update needs inside its own
job's folder, makes no other folder there named by a 64 character digest or
starting with `prepare-`, and needs no review record after Start.

The worker records that host changes have begun before it replaces the
program. Until then a failed or refused update can be canceled, releasing the
maintenance restriction without changing the running node. Afterwards
cancellation is refused, and completion or repair is required instead. A
corrective job starts with the failed update's record of this, so it stays
uncancellable.

A failed update waits for an explicit Retry, which uses the same approved
release. After a restart of the machine, the launcher hands an unfinished
update that has not failed to its worker again. Corrective releases must
acknowledge the exact failed release in `recovery_from` and the installed
version in `sources`. A corrective release is not a generic force-update or
rollback option.

## Installed vpnd and later releases

`vpnd` and the launcher on a node are older than the worker of every update
they start. They read only these fields of the job record: `schema`,
`review.digest`, `review.manifest.version`, `job_id`, `worker_hash`,
`config_hash`, `phase`, `step`, `error` and `host_changes_begun`. The
installed side writes `job_id` and `accepted` at Start, and the worker writes
`ended` when the update completes. They act on three phase words, `failed`,
`complete` and `canceled`, and treat any other as work in progress. Retry only
sets `phase` to `retry`, and the worker decides where to resume. Cancel sets
`phase` to `canceled`, the step text and `ended`. Both remove `error`, `next`
and `advice`, write `updated` and keep every other field as the worker wrote
it. A worker may add fields, phases and steps, but must keep these meanings
for as long as its plan admits a source with this `vpnd`.

For Node Updates only, `vpnd` also passes `steps`, `step_number`,
`step_starts`, `step_ends`, `next` and `advice`, each only in a shape it can
read, and the last two only with an error. `step_starts` and `step_ends` hold
when each listed step began and ended, no more entries than steps, none more
than a minute ahead of the node's clock, no end before its start, and no end
for a step that has not begun. `advice` is at most 400 characters of printable
text and line breaks, its last part, after an empty line, the sentence that
names the button. A worker sends `next` and `advice` together. `next` is
`continue`, `retry` or `cancel`, the button the advice names, and `cancel`
only before host changes began. Each value keeps its meaning in every release.
No worker of this release sends `continue`. A later worker may add `next`
values: `vpnd` passes them, and a screen that does not know the value, or did
not get both, uses its own release's next step and words for the failure. The
screen scrolls the list, so a worker may list as many steps as it needs.

Once a job exists, the launcher, Retry and Cancel no longer check the Debian
version or read the node configuration. They hand the job to the worker, which
makes its own checks. A later worker can therefore change either part way
through an update and still be resumed.

`vpnd` likewise ignores plan fields it does not know, and still enforces the
exact source, newer-version and recovery checks. The worker reads the approved
plan bytes in full and refuses a plan it does not completely understand. A
plan with a later `protocol` number than `vpnd` knows is refused with "this
release needs a newer VPN first, see its release notes". A plan with an
earlier number, such as a plan of the Bitcoin and Lightning node on a base
node, is refused with "this release was made for an older VPN and cannot
update this node". A plan that names a `bridge` the node can install first
gets neither sentence. The node is told to install that release first. A
corrective worker can read the failed update's retained record, `result.json`
in that update's folder.

## Limits

Local tests exercise release admission, approval identity, selected tar members,
durable replacement, shared locking, and interruption before and after the
program is replaced. They simulate process loss and reboot, but do not prove
native systemd behavior or a released artifact's signature.

This worker does not yet implement Debian package migrations, replacement of
the pinned release key, or reboot-requiring host changes. Those need explicit
release operations and their own validation.
