#!/bin/bash
set -u -o pipefail

# Read-only Debian 13 validation for private LND and Bitcoin Core files.
# Run as root on an unfunded disposable candidate after both daemons have
# started and created state under their service identities.

if [[ ${EUID} -ne 0 ]]; then
    echo "ERROR: run as root on the disposable Debian fixture" >&2
    exit 2
fi

if (( $# != 0 )); then
    echo "Usage: $0" >&2
    exit 2
fi

passes=0
failures=0

pass() {
    passes=$((passes + 1))
    echo "PASS $1"
}

fail() {
    failures=$((failures + 1))
    echo "FAIL $1: $2"
}

assert_equal() {
    local label=$1
    local got=$2
    local want=$3
    if [[ ${got} == "${want}" ]]; then
        pass "${label}"
    else
        fail "${label}" "got '${got}', want '${want}'"
    fi
}

assert_excludes() {
    local label=$1
    local got=$2
    local forbidden=$3
    if [[ ${got} != *"${forbidden}"* ]]; then
        pass "${label}"
    else
        fail "${label}" "forbidden value is present"
    fi
}

assert_metadata() {
    local label=$1
    local target=$2
    local expected_owner=$3
    local expected_group=$4
    local expected_mode=$5
    local got_owner got_group got_mode

    if [[ -L ${target} ]]; then
        fail "${label}" "${target} is a symbolic link"
        return
    fi
    if [[ ! -d ${target} ]]; then
        fail "${label}" "${target} is not a directory"
        return
    fi
    got_owner=$(stat -c '%U' -- "${target}")
    got_group=$(stat -c '%G' -- "${target}")
    got_mode=$(stat -c '%a' -- "${target}")
    if [[ ${got_owner} == "${expected_owner}" &&
          ${got_group} == "${expected_group}" &&
          ${got_mode} == "${expected_mode}" ]]; then
        pass "${label}"
    else
        fail "${label}" \
            "owner/group/mode is ${got_owner}:${got_group} ${got_mode}"
    fi
}

assert_private_identity_tree() {
    local label=$1
    local root=$2
    local identity=$3
    local sample violations

    if [[ ! -d ${root} || -L ${root} ]]; then
        fail "${label}" "${root} is unavailable or a symbolic link"
        return
    fi

    sample=$(find "${root}" -mindepth 1 -user "${identity}" \
        -print -quit 2>/dev/null)
    if [[ -z ${sample} ]]; then
        fail "${label}" "no ${identity}-owned daemon state exists"
        return
    fi

    # Symbolic-link mode bits do not govern access and are excluded.
    # The owning identity is the scope because root-staged inputs such as
    # LND's wallet-password file follow their own explicit access contract.
    violations=$(find "${root}" -mindepth 1 -user "${identity}" \
        ! -type l -perm /077 -printf '%M %u:%g %p\n' 2>/dev/null)
    if [[ -z ${violations} ]]; then
        pass "${label}"
    else
        fail "${label}" "group/world permission found: ${violations}"
    fi
}

os_id=$(sed -n 's/^ID=//p' /etc/os-release | tr -d '"')
os_version=$(sed -n 's/^VERSION_ID=//p' /etc/os-release | tr -d '"')
architecture=$(dpkg --print-architecture)
assert_equal "ENV Debian" "${os_id}" "debian"
assert_equal "ENV Debian release" "${os_version}" "13"
assert_equal "ENV architecture" "${architecture}" "amd64"
assert_equal "ENV systemd PID 1" "$(cat /proc/1/comm)" "systemd"

for unit in bitcoind.service lnd.service; do
    if systemd-analyze verify "${unit}" >/dev/null 2>&1; then
        pass "SYS verify ${unit}"
    else
        fail "SYS verify ${unit}" "systemd-analyze verify failed"
    fi
done

assert_equal "LND unit user" \
    "$(systemctl show -p User --value lnd.service)" lnd
assert_equal "LND unit group" \
    "$(systemctl show -p Group --value lnd.service)" lnd
assert_equal "LND private umask" \
    "$(systemctl show -p UMask --value lnd.service)" 0077
assert_excludes "LND lacks export group" \
    "$(systemctl show -p SupplementaryGroups --value lnd.service)" \
    vpn-lnd-backup

assert_equal "Bitcoin unit user" \
    "$(systemctl show -p User --value bitcoind.service)" bitcoin
assert_equal "Bitcoin unit group" \
    "$(systemctl show -p Group --value bitcoind.service)" bitcoin
assert_equal "Bitcoin private umask" \
    "$(systemctl show -p UMask --value bitcoind.service)" 0077
assert_excludes "Bitcoin lacks export group" \
    "$(systemctl show -p SupplementaryGroups --value bitcoind.service)" \
    vpn-lnd-backup
assert_excludes "Bitcoin lacks Tor control group" \
    "$(systemctl show -p SupplementaryGroups --value bitcoind.service)" \
    debian-tor

assert_metadata "FS LND data root" /var/lib/lnd lnd lnd 750
assert_metadata "FS Bitcoin data root" /var/lib/bitcoin bitcoin bitcoin 750
assert_private_identity_tree "FS LND-owned state is private" /var/lib/lnd lnd
assert_private_identity_tree "FS Bitcoin-owned state is private" \
    /var/lib/bitcoin bitcoin

echo "SUMMARY pass=${passes} fail=${failures}"
if (( failures > 0 )); then
    exit 1
fi
