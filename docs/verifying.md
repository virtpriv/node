## Release Verification

Verify signatures before installation.

These steps apply to the releases published so far, which were made
under the project's previous name and ship a binary called rlvpn. They
use `rlvpn-VERSION-amd64.tar.gz`. Starting with v0.7.0, the archive is
`vpn-VERSION-amd64.tar.gz` and contains `vpn`. The signature and checksum
filenames stay the same. The example below is for the historical v0.6.3 release.

Managed release archives also contain `update.json`. Its integrity is covered
by the same signed archive checksum. The TUI verifies that plan before review
and checks each selected component against its pinned upstream signature and
the plan's exact archive hash. See [managed updates](updating.md).

### Import the release signing key

```bash
gpg --keyserver hkps://keys.openpgp.org --recv-keys AFA0EBACDC9A4C4AA7B0154AC97CE10F170BA5FE
```

### Download the release files

```bash
VERSION="0.6.3"
wget -q "https://github.com/virtualprivatenode/vpn/releases/download/v${VERSION}/rlvpn-${VERSION}-amd64.tar.gz"
wget -q "https://github.com/virtualprivatenode/vpn/releases/download/v${VERSION}/SHA256SUMS"
wget -q "https://github.com/virtualprivatenode/vpn/releases/download/v${VERSION}/SHA256SUMS.asc"
```

### Verify the signature

```bash
gpg --verify SHA256SUMS.asc SHA256SUMS
```

A good result names the project key as the primary key:

```text
gpg: Good signature from "..."
Primary key fingerprint: AFA0 EBAC DC9A 4C4A A7B0  154A C97C E10F 170B A5FE
```

The line `using ... key` may show a different, shorter-lived signing subkey.
That is expected; the primary key fingerprint is the one to compare.

Do not install the release if gpg prints
`WARNING: This key has been revoked by its owner!`. gpg still reports
"Good signature" in that case, so read the whole output. A warning that the
key is not certified with a trusted signature is normal for a key you imported
yourself.

The same public key is kept in this repository as
[`keys/ripsline.asc`](../keys/ripsline.asc). It can be imported with
`gpg --import` when the keyserver cannot be reached. Check that the imported
key has the fingerprint above.

### Verify the checksum

```bash
sha256sum --check --ignore-missing SHA256SUMS
```

The bootstrap script performs this verification automatically during
installation. This section is for users who want to verify manually
before running the one-liner.
