# Reproducible builds

The steps below reproduce the `vpn` program inside a published release archive
byte for byte, for v0.7.0 and later. Releases before v0.7.0 shipped a program
called rlvpn, built with other settings, and are not reproduced by these steps.

Reproducibility is a goal of the Virtual Private Node project. The
intent is that anyone can recreate the exact binary published in the
GitHub releases.

## How releases are built

Releases are built with the release command in this repository, `cmd/release`,
using the Go version pinned in `go.mod` and the same settings as the build
command below.

Because the project is a single statically-linked Go binary with no
bundled runtime, reproducibility is straightforward compared to
projects that bundle a JVM or native installers.

## Reproducing a release

### Prerequisites

On Debian 13+:

```bash
apt install -y git wget curl sudo gnupg
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
GO_VERSION="1.26.10"  # check go.mod for exact version
wget "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
```

Compare its SHA-256 with the one go.dev/dl lists for that file, and stop if
they differ:

```bash
sha256sum "go${GO_VERSION}.linux-amd64.tar.gz"
```

Then install it:

```bash
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
GIT_TAG="v0.7.0"
```

The project can then be cloned as follows:

```bash
git clone --branch "${GIT_TAG}" --depth 1 \
    https://github.com/virtpriv/node.git
cd node
```

If you already have the repo cloned, fetch all new updates and
checkout the release:

```bash
cd node
git fetch --all --tags
git checkout "${GIT_TAG}"
```

In the project folder, build with the same settings as the release tool:

```bash
VERSION="${GIT_TAG#v}"
GOTOOLCHAIN=local GOENV=off GOWORK=off GOFLAGS= \
    GOEXPERIMENT= GOFIPS140=off GO_EXTLINK_ENABLED= CGO_ENABLED=0 \
    GOOS=linux GOARCH=amd64 GOAMD64=v1 \
    go build -mod=readonly -trimpath -buildvcs=false \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o vpn ./cmd/
```

The program will be placed in the current directory as `vpn`.

### Verifying the binary is identical

Download the released binary from
[GitHub Releases](https://github.com/virtpriv/node/releases):

```bash
wget -q "https://github.com/virtpriv/node/releases/download/v${VERSION}/vpn-${VERSION}-x86_64.tar.gz"
wget -q "https://github.com/virtpriv/node/releases/download/v${VERSION}/vpn-${VERSION}-manifest.txt"
wget -q "https://github.com/virtpriv/node/releases/download/v${VERSION}/vpn-${VERSION}-manifest.txt.asc"
```

Import the release signing key from the OpenPGP keyserver:

```bash
wget -qO ripsline.asc "https://keys.openpgp.org/vks/v1/by-fingerprint/AFA0EBACDC9A4C4AA7B0154AC97CE10F170BA5FE"
gpg --import ripsline.asc
```

Verify the signature:

```bash
gpg --verify "vpn-${VERSION}-manifest.txt.asc" "vpn-${VERSION}-manifest.txt"
```

Compare the primary key fingerprint in the output, and stop if gpg warns that
the key or its subkey has been revoked. See
[Release Verification](verifying.md).

Verify the checksum:

```bash
sha256sum --check --ignore-missing "vpn-${VERSION}-manifest.txt"
```

Extract the released program and compare:

```bash
mkdir published
tar -xzf "vpn-${VERSION}-x86_64.tar.gz" -C published vpn

sha256sum published/vpn vpn
```

Both hashes should be identical.

If there is output showing different hashes, please open an issue
with detailed instructions to reproduce, including build system
platform and Go version.

## Checking the pinned programs

vpn pins the exact SHA-256 hash of each Bitcoin Core, LND and Syncthing
archive it installs, and nodes check these programs by that hash alone.
Before a hash is pinned, the maintainer checks upstream's signatures with
this tool. Anyone can repeat the check from a clone of this repository, on a
computer with Go and gpg:

```bash
go run ./cmd/pincheck
```

For each program it downloads the pinned version's checksum list,
signatures, signer keys and archive, then prints the signers it counted, the
hash, and whether the hash matches the pin. Bitcoin Core needs 3 of its 5
listed builders, LND 3 of its 11 listed developers, Syncthing its release
key. A revoked key never counts, an expired one does. Any failure prints
`FAIL` and no hash. The downloads go directly over HTTPS, not through Tor.

To check a version before it is pinned:

```bash
go run ./cmd/pincheck -lnd 0.21.8-beta
```

`-bitcoin` and `-syncthing` work the same way.

## Troubleshooting

If the hashes differ, check that your Go is the version in `go.mod`, from
go.dev/dl, and that you ran the build command exactly as shown.

You can inspect the build metadata embedded in each binary:

```bash
go version -m published/vpn
go version -m vpn
```

Apart from the file name on the first line, both outputs should be identical.

## What this proves

| Check | What it verifies |
| --- | --- |
| GPG signature | Release was signed by the project maintainer |
| SHA256 checksum | Download was not corrupted or tampered with |
| Reproducible build | Binary was built from the published source code |

All three together prove the binary you are running is exactly what
the source code says it is, signed by who it claims to be from, and
delivered without modification.
