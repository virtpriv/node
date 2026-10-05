# Reproducible builds

**These steps do not currently reproduce the published v0.6.3 binary.**
Three differences are known, all confirmed by reading the build metadata
out of the release itself. It was built with Go 1.26.3 rather than the
version go.mod declares. It was built with no ldflags at all, rather
than the ldflags shown below. And it was built from a commit that did
not yet carry the v0.6.3 tag, so a clone standing on the tag stamps a
different module version into the binary. The page is kept because the
rest of the recipe does match the release, including trimpath, CGO
disabled, the target platform and a clean working tree. It will be
corrected and verified against v0.7.0, the first release under the
project's new name.

Reproducibility is a goal of the Virtual Private Node project. The
intent is that anyone can recreate the exact binary published in the
GitHub releases.

## Current release packaging

For v0.7.0 and later, use the Go version pinned in `go.mod` and run the release
command from the repository root.

(local PC)

```sh
GOTOOLCHAIN=local go run ./cmd/release 0.7.0
```

Substitute the version being released. The command checks the exact compiler
version, builds for Linux amd64 with CGO disabled, creates the archive and
checksums, and signs locally with the VPN release key. GPG must be installed
and that key available in your local keyring. Signing can prompt through your
normal GPG setup. Verification uses an isolated keyring and the same pinned
signer policy as the updater.

Output is `release/0.7.0/`, containing `vpn-0.7.0-amd64.tar.gz`, `SHA256SUMS`
and `SHA256SUMS.asc`. The archive contains the executable `vpn`. Existing
version directories are refused, so earlier signed releases stay intact.
An ordinary failure removes only the new incomplete directory. After a forced
termination, inspect any leftover directory before removing it and retrying.
Never replace files already published under a release tag.

Versions must be canonical `MAJOR.MINOR.PATCH` or `MAJOR.MINOR.PATCH-rc.N`,
where N is a positive integer without leading zeros. RCs use the same packaging
and signing process as stable releases. Archive member metadata is fixed so
local timestamps and user/group IDs do not change its checksum; GPG signatures
have their own creation timestamps.

Node updates additionally require a tested plan as the second argument.

(local PC)

```sh
GOTOOLCHAIN=local go run ./cmd/release VERSION path/to/tested-update-plan.json
```

The command validates the plan against the build's component pins and supported
host operations, then includes those exact bytes as `update.json` in the signed
archive. See [managed updates](updating.md) for the release contract and
required integration validation. The tool prepares files locally; publication
on GitHub is a separate step.

This packaging contract matches the VPN updater. Reproduction against the
published v0.7.0 artifact still needs release verification. The v0.6.3 recipe
and its known limitations below remain historical reference.

Because the project is a single statically-linked Go binary with no
bundled runtime, reproducibility is straightforward compared to
projects that bundle a JVM or native installers.

## Reproducing a release

### Prerequisites

On Debian 13+:

```bash
apt install -y git wget curl sudo
```

### Install Go

Because Go embeds its version in the binary, it is essential to have
the same version of Go installed when recreating the release. The
required version is specified in the `go.mod` file at the root of
the repository.

Note: Do not install Go using a system package manager (e.g. apt,
snap, brew). Distribution packages may patch the toolchain or use
different default flags, which will produce a non-reproducible build.

#### Go from the official downloads

Go is available for all supported platforms from
[go.dev/dl](https://go.dev/dl/).

```bash
GO_VERSION="1.26.8"  # check go.mod for exact version
wget "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf "go${GO_VERSION}.linux-amd64.tar.gz"
export PATH=$PATH:/usr/local/go/bin
```

#### Verify the installation

```bash
go version
```

### Building the binary

First, assign a temporary variable in your shell for the specific
release you want to build:

```bash
GIT_TAG="v0.6.3"
```

The project can then be cloned as follows:

```bash
git clone --branch "${GIT_TAG}" --depth 1 \
    https://github.com/virtualprivatenode/vpn.git
```

If you already have the repo cloned, fetch all new updates and
checkout the release:

```bash
cd vpn
git fetch --all --tags
git checkout "${GIT_TAG}"
```

Change into the project folder and build:

```bash
cd vpn
VERSION="${GIT_TAG#v}"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o rlvpn ./cmd/
```

The binary will be placed in the current directory as `rlvpn`.

### Verifying the binary is identical

Download the released binary from
[GitHub Releases](https://github.com/virtualprivatenode/vpn/releases):

```bash
VERSION="0.6.3"

wget -q "https://github.com/virtualprivatenode/vpn/releases/download/v${VERSION}/rlvpn-${VERSION}-amd64.tar.gz"
wget -q "https://github.com/virtualprivatenode/vpn/releases/download/v${VERSION}/SHA256SUMS"
wget -q "https://github.com/virtualprivatenode/vpn/releases/download/v${VERSION}/SHA256SUMS.asc"
```

Import the release signing key from the OpenPGP keyserver:

```bash
gpg --keyserver hkps://keys.openpgp.org --recv-keys AFA0EBACDC9A4C4AA7B0154AC97CE10F170BA5FE
```

Verify the signature:

```bash
gpg --verify SHA256SUMS.asc SHA256SUMS
```

Compare the primary key fingerprint in the output, and stop if gpg warns that
the key has been revoked. See [Release Verification](verifying.md).

Verify the checksum:

```bash
sha256sum --check --ignore-missing SHA256SUMS
```

Extract the released binary and compare:

```bash
tar -xzf "rlvpn-${VERSION}-amd64.tar.gz"
mv rlvpn rlvpn-release

sha256sum rlvpn-release rlvpn
```

Both hashes should be identical.

If there is output showing different hashes, please open an issue
with detailed instructions to reproduce, including build system
platform and Go version.

## Troubleshooting

If the checksums do not match, check the following:

| Cause | Fix |
| --- | --- |
| Different Go version | Check `go.mod` and use the exact version listed |
| Missing `-trimpath` | Local filesystem paths get embedded in the binary |
| CGO enabled | Set `CGO_ENABLED=0` explicitly |
| Different `ldflags` | Must include `-s -w -X main.version=VERSION` |
| OS/arch mismatch | Must build with `GOOS=linux GOARCH=amd64` |
| Go installed via apt/snap | Use the official tarball from go.dev/dl |
| Missing `-X main.version` | Version string will differ in the binary |

You can inspect the build metadata embedded in each binary:

```bash
go version -m rlvpn-release
go version -m rlvpn
```

Both outputs should be identical.

## What this proves

| Check | What it verifies |
| --- | --- |
| GPG signature | Release was signed by the project maintainer |
| SHA256 checksum | Download was not corrupted or tampered with |
| Reproducible build | Binary was built from the published source code |

All three together prove the binary you are running is exactly what
the source code says it is, signed by who it claims to be from, and
delivered without modification.
