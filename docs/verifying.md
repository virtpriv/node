## Release Verification

Install a release only after you have checked its signature and its checksum.
The steps below install vpn on a fresh Debian 13 server and download the
release and its signing key through Tor.

After installation, the node checks every later release itself, with the same
key and checksum rules, when you select Review, before you can install it.
Managed release archives also contain `update.json`. Its integrity is covered
by the same signed archive checksum. The TUI verifies that plan before review
and checks each selected component against the exact archive hash pinned in
the release. See [Updating Your Node](updating.md).

Releases published before v0.7.0 were made under the project's previous name
and ship a program called rlvpn. They are not installed with these steps.

### 1. Install Tor

Log in to the fresh server and open a root shell (`sudo -i` if you logged in as
another account), then run:

```bash
apt-get update
apt-get install -y tor torsocks gnupg wget
```

These packages come from Debian's mirrors over the normal internet, because Tor
is not on the server yet. The downloads below go through Tor. The installer
later configures Tor for the node. Before it does, it refreshes Debian's
package list and installs its base tools over the normal internet once more,
as listed in the README under
[Privacy & Network Traffic](../README.md#privacy--network-traffic).

### 2. Check that Tor works

```bash
torsocks wget -qO- https://check.torproject.org/api/ip && echo
```

The answer must contain `"IsTor":true`. If Tor is still starting, wait a minute
and run it again.

### 3. Download the release and the signing key

```bash
VERSION="0.7.0"
cd /root
torsocks wget -q "https://github.com/virtpriv/node/releases/download/v${VERSION}/vpn-${VERSION}-x86_64.tar.gz"
torsocks wget -q "https://github.com/virtpriv/node/releases/download/v${VERSION}/vpn-${VERSION}-manifest.txt"
torsocks wget -q "https://github.com/virtpriv/node/releases/download/v${VERSION}/vpn-${VERSION}-manifest.txt.asc"
torsocks wget -qO ripsline.asc "https://keys.openpgp.org/vks/v1/by-fingerprint/AFA0EBACDC9A4C4AA7B0154AC97CE10F170BA5FE"
gpg --import ripsline.asc
```

If the keyserver cannot be reached, the same public key is kept in this
repository as [`keys/ripsline.asc`](../keys/ripsline.asc):

```bash
torsocks wget -qO ripsline.asc "https://raw.githubusercontent.com/virtpriv/node/main/keys/ripsline.asc"
gpg --import ripsline.asc
```

### 4. Verify the signature

```bash
gpg --verify "vpn-${VERSION}-manifest.txt.asc" "vpn-${VERSION}-manifest.txt"
```

A good result names the project key as the primary key:

```text
gpg: Good signature from "..."
Primary key fingerprint: AFA0 EBAC DC9A 4C4A A7B0  154A C97C E10F 170B A5FE
```

The line `using ... key` may show a different, shorter-lived signing subkey.
That is expected. The primary key fingerprint is the one to compare.

Do not install the release if gpg prints
`WARNING: This key has been revoked by its owner!` or
`WARNING: This subkey has been revoked by its owner!`. gpg still reports
"Good signature" in both cases, so read the whole output. A warning that the
key is not certified with a trusted signature is normal for a key you imported
yourself.

### 5. Verify the checksum

```bash
sha256sum --check --ignore-missing "vpn-${VERSION}-manifest.txt"
```

It must print `vpn-0.7.0-x86_64.tar.gz: OK`. Stop if it prints anything else.

### 6. Install

```bash
tar -xzf "vpn-${VERSION}-x86_64.tar.gz" vpn
./vpn version
```

`./vpn version` prints the version you downloaded. Then start the installer.
It copies itself to `/usr/local/bin/vpn`:

```bash
./vpn install
```

For the test networks, use `./vpn install --testnet4` or
`./vpn install --signet` instead. The network cannot be changed later. See the
[README](../README.md#bitcoin-network-profiles).

If the install stops, or you close it, it shows the step it reached. To
continue:

```bash
sudo vpn install
```

### Verify on your own computer instead

You can run steps 3 to 5 and the `tar` line of step 6 on your own computer.
Leave out `cd /root` and use an empty folder of your own. Then copy `vpn` to
the server (for example with `scp`) and run `./vpn install` there as root. On
your own computer the downloads go through Tor only if you use `torsocks` or
another Tor client.

### Bring your own Bitcoin Core and LND

The install downloads Bitcoin Core and LND through Tor. You can bring them
instead, after checking them with each project's own steps: Bitcoin Core's
[download page](https://bitcoincore.org/en/download/) and LND's
[releases](https://github.com/lightningnetwork/lnd/releases). The node then
checks each against the exact hash this vpn release pins, as it checks a
download.

Before you run the install, as root, make the folder and put the archives
there:

```bash
mkdir -m 700 /root/releases
```

For this release the names are `bitcoin-31.2-x86_64-linux-gnu.tar.gz` and
`lnd-linux-amd64-v0.21.6-beta.tar.gz`. You can bring one of them, the
other is downloaded. The install step then says, for example, `Taking
Bitcoin Core 31.2 from /root/releases`.

A file that does not match the pinned hash stops the install, and the error
names it. Replace it, or remove it to download instead, and run
`sudo vpn install` again. Once the install is complete, vpn removes the
archives it used from `/root/releases`.
