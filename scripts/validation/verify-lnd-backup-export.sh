#!/bin/bash
set -u -o pipefail

# Read-only Debian 13 validation for the LND backup export boundary.
# Run as root on an unfunded disposable candidate after the Syncthing
# add-on has completed and an LND channel.backup exists, then run it
# again after reboot to re-prove the folder's runtime health.

if [[ ${EUID} -ne 0 ]]; then
    echo "ERROR: run as root on the disposable Debian fixture" >&2
    exit 2
fi

network=${1:-}
case "${network}" in
    mainnet|testnet4) lnd_network=${network} ;;
    public-signet) lnd_network=signet ;;
    *)
        echo "Usage: $0 <mainnet|testnet4|public-signet>" >&2
        exit 2
        ;;
esac

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

assert_contains() {
    local label=$1
    local got=$2
    local want=$3
    if [[ ${got} == *"${want}"* ]]; then
        pass "${label}"
    else
        fail "${label}" "required value is absent"
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
    local expected_type=$3
    local expected_owner=$4
    local expected_group=$5
    local expected_mode=$6
    local got_type got_owner got_group got_mode

    if [[ -L ${target} ]]; then
        fail "${label}" "${target} is a symbolic link"
        return
    fi
    if [[ ! -e ${target} ]]; then
        fail "${label}" "${target} does not exist"
        return
    fi
    got_type=$(stat -c '%F' -- "${target}")
    got_owner=$(stat -c '%U' -- "${target}")
    got_group=$(stat -c '%G' -- "${target}")
    got_mode=$(stat -c '%a' -- "${target}")
    if [[ ${got_type} == "${expected_type}" &&
          ${got_owner} == "${expected_owner}" &&
          ${got_group} == "${expected_group}" &&
          ${got_mode} == "${expected_mode}" ]]; then
        pass "${label}"
    else
        fail "${label}" \
            "type/owner/group/mode is ${got_type} ${got_owner}:${got_group} ${got_mode}"
    fi
}

identity_ids() {
    local name=$1
    local passwd identity_uid identity_gid
    passwd=$(getent passwd "${name}") || return 1
    IFS=: read -r _ _ identity_uid identity_gid _ <<<"${passwd}"
    printf '%s %s\n' "${identity_uid}" "${identity_gid}"
}

assert_identity_access() {
    local label=$1
    local identity=$2
    local supplementary=$3
    local operator=$4
    local target=$5
    local expected=$6
    local identity_uid identity_gid
    local actual

    if ! read -r identity_uid identity_gid \
        < <(identity_ids "${identity}"); then
        fail "${label}" "identity ${identity} does not exist"
        return
    fi

    local -a group_args
    if [[ ${supplementary} == "clear" ]]; then
        group_args=(--clear-groups)
    else
        group_args=(--groups "${supplementary}")
    fi

    if setpriv --reuid "${identity_uid}" --regid "${identity_gid}" \
        "${group_args[@]}" --no-new-privs -- \
        test "${operator}" "${target}"; then
        actual=allow
    else
        actual=deny
    fi
    assert_equal "${label}" "${actual}" "${expected}"
}

os_id=$(sed -n 's/^ID=//p' /etc/os-release | tr -d '"')
os_version=$(sed -n 's/^VERSION_ID=//p' /etc/os-release | tr -d '"')
architecture=$(dpkg --print-architecture)
assert_equal "ENV Debian" "${os_id}" "debian"
assert_equal "ENV Debian release" "${os_version}" "13"
assert_equal "ENV architecture" "${architecture}" "amd64"
assert_equal "ENV systemd PID 1" "$(cat /proc/1/comm)" "systemd"

export_root=/var/lib/vpn/exports
stage_dir=${export_root}/lnd-backup-stage
final_dir=${export_root}/lnd-backup
marker_name=.vpn-export-ready
marker_dir=${final_dir}/${marker_name}
source_file=/var/lib/lnd/data/chain/bitcoin/${lnd_network}/channel.backup
final_file=${final_dir}/channel.backup

assert_metadata "FS Syncthing config private" /etc/syncthing directory \
    syncthing syncthing 700
assert_metadata "FS Syncthing data private" /var/lib/syncthing directory \
    syncthing syncthing 700
assert_metadata "FS export root" "${export_root}" directory \
    root vpn-lnd-backup 750
assert_metadata "FS private stage" "${stage_dir}" directory lnd lnd 700
assert_metadata "FS final export" "${final_dir}" directory \
    lnd vpn-lnd-backup 750
assert_metadata "FS export readiness marker" "${marker_dir}" directory \
    root vpn-lnd-backup 750
assert_metadata "BKP source" "${source_file}" "regular file" lnd lnd 600
assert_metadata "BKP final" "${final_file}" "regular file" \
    lnd vpn-lnd-backup 640

folder_config=$(python3 - <<'PY'
import xml.etree.ElementTree as ET

root = ET.parse("/etc/syncthing/config.xml").getroot()
folders = [
    folder for folder in root.findall("folder")
    if folder.get("id") == "lnd-backup"
]
if len(folders) != 1:
    print(f"count={len(folders)}")
else:
    folder = folders[0]
    print("|".join((
        folder.get("path", ""),
        folder.get("type", ""),
        folder.findtext("markerName", default=""),
    )))
PY
)
assert_equal "CFG Syncthing backup folder" "${folder_config}" \
    "${final_dir}|sendonly|${marker_name}"

syncthing_runtime=$(python3 - <<'PY'
import json
import time
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET

root = ET.parse("/etc/syncthing/config.xml").getroot()
api_key = root.findtext("./gui/apikey", default="")
headers = {"X-API-Key": api_key}
base_url = "http://127.0.0.1:8384"

def get_json(endpoint, query):
    url = base_url + endpoint + "?" + urllib.parse.urlencode(query)
    request = urllib.request.Request(url, headers=headers)
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.load(response)

last = "request-error||-1"
try:
    for _ in range(30):
        status = get_json("/rest/db/status", {"folder": "lnd-backup"})
        errors = get_json("/rest/folder/errors", {"folder": "lnd-backup"})
        state = str(status.get("state", ""))
        invalid = str(status.get("invalid", ""))
        error_count = len(errors.get("errors") or [])
        last = f"{state}|{invalid}|{error_count}"
        if state == "idle" and not invalid and error_count == 0:
            break
        time.sleep(1)
except Exception as error:
    last = f"request-error:{type(error).__name__}||-1"
print(last)
PY
)
assert_equal "RUN Syncthing backup folder healthy" \
    "${syncthing_runtime}" "idle||0"

for unit in lnd.service lnd-backup-export.service \
    lnd-backup-watch.path lnd-backup-check.timer syncthing.service; do
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
assert_excludes "LND lacks export group" \
    "$(systemctl show -p SupplementaryGroups --value lnd.service)" \
    vpn-lnd-backup

assert_equal "Publisher unit user" \
    "$(systemctl show -p User --value lnd-backup-export.service)" lnd
assert_equal "Publisher unit group" \
    "$(systemctl show -p Group --value lnd-backup-export.service)" lnd
assert_equal "Publisher umask" \
    "$(systemctl show -p UMask --value lnd-backup-export.service)" 0027
assert_contains "Publisher export group" \
    "$(systemctl show -p SupplementaryGroups --value lnd-backup-export.service)" \
    vpn-lnd-backup
assert_contains "Publisher fixed network command" \
    "$(systemctl show -p ExecStart --value lnd-backup-export.service)" \
    "/usr/local/bin/vpn publish-lnd-backup ${network}"
assert_contains "Watcher fixed source" \
    "$(systemctl cat lnd-backup-watch.path 2>/dev/null)" \
    "PathChanged=${source_file}"
assert_contains "Watcher targets exporter" \
    "$(systemctl cat lnd-backup-watch.path 2>/dev/null)" \
    "Unit=lnd-backup-export.service"

assert_equal "Syncthing unit user" \
    "$(systemctl show -p User --value syncthing.service)" syncthing
assert_equal "Syncthing unit group" \
    "$(systemctl show -p Group --value syncthing.service)" syncthing
assert_contains "Syncthing export group" \
    "$(systemctl show -p SupplementaryGroups --value syncthing.service)" \
    vpn-lnd-backup

assert_equal "Backup watcher active" \
    "$(systemctl is-active lnd-backup-watch.path 2>/dev/null || true)" active
# The watcher can miss a change, so a timer starts the same publisher every
# minute. The publisher pulls the watcher back in and has no start limit, so
# a burst of backup changes cannot leave the watcher failed.
assert_equal "Backup check timer active" \
    "$(systemctl is-active lnd-backup-check.timer 2>/dev/null || true)" active
assert_equal "Backup check timer enabled" \
    "$(systemctl is-enabled lnd-backup-check.timer 2>/dev/null || true)" enabled
assert_equal "Backup check timer targets exporter" \
    "$(systemctl show -p Unit --value lnd-backup-check.timer)" \
    lnd-backup-export.service
assert_contains "Publisher pulls in the watcher" \
    "$(systemctl show -p Wants --value lnd-backup-export.service)" \
    lnd-backup-watch.path
assert_equal "Publisher has no start limit" \
    "$(systemctl show -p StartLimitIntervalUSec --value lnd-backup-export.service)" 0
assert_equal "Publisher last result" \
    "$(systemctl show -p Result --value lnd-backup-export.service)" success

backup_gid=$(getent group vpn-lnd-backup | cut -d: -f3)
if [[ -z ${backup_gid} ]]; then
    fail "ID backup group" "vpn-lnd-backup does not exist"
else
    pass "ID backup group"
    assert_identity_access "ACCESS publisher reads source" lnd \
        "${backup_gid}" -r "${source_file}" allow
    assert_identity_access "ACCESS publisher writes stage" lnd \
        "${backup_gid}" -w "${stage_dir}" allow
    assert_identity_access "ACCESS publisher writes final directory" lnd \
        "${backup_gid}" -w "${final_dir}" allow
    assert_identity_access "ACCESS normal LND denied export root" lnd \
        clear -x "${export_root}" deny
    assert_identity_access "ACCESS Syncthing reads final" syncthing \
        "${backup_gid}" -r "${final_file}" allow
    assert_identity_access "ACCESS Syncthing denied source" syncthing \
        "${backup_gid}" -r "${source_file}" deny
    assert_identity_access "ACCESS Syncthing denied stage" syncthing \
        "${backup_gid}" -x "${stage_dir}" deny
    assert_identity_access "ACCESS Syncthing denied export writes" syncthing \
        "${backup_gid}" -w "${final_dir}" deny
    assert_identity_access "ACCESS Syncthing traverses export marker" \
        syncthing "${backup_gid}" -x "${marker_dir}" allow
    assert_identity_access "ACCESS Syncthing denied marker writes" \
        syncthing "${backup_gid}" -w "${marker_dir}" deny
fi

for identity in vpn bitcoin; do
    assert_identity_access "ACCESS ${identity} denied source" \
        "${identity}" clear -r "${source_file}" deny
    assert_identity_access "ACCESS ${identity} denied final" \
        "${identity}" clear -r "${final_file}" deny
done

if cmp -s -- "${source_file}" "${final_file}"; then
    pass "BKP source and final byte equality"
else
    fail "BKP source and final byte equality" "files differ"
fi

unexpected_final=$(find "${final_dir}" -mindepth 1 -maxdepth 1 \
    ! -name channel.backup ! -name "${marker_name}" \
    -printf '%f\n' 2>/dev/null)
if [[ ! -d ${final_dir} || -L ${final_dir} ]]; then
    fail "BKP export contains only marker and channel.backup" \
        "final export directory is unavailable"
elif [[ -z ${unexpected_final} ]]; then
    pass "BKP export contains only marker and channel.backup"
else
    fail "BKP export contains only marker and channel.backup" \
        "unexpected object exists"
fi

stage_temps=$(find "${stage_dir}" -mindepth 1 -maxdepth 1 \
    -name '.channel.backup.tmp-*' -printf '%f\n' 2>/dev/null)
if [[ ! -d ${stage_dir} || -L ${stage_dir} ]]; then
    fail "BKP no publisher temporary remains" \
        "private stage directory is unavailable"
elif [[ -z ${stage_temps} ]]; then
    pass "BKP no publisher temporary remains"
else
    fail "BKP no publisher temporary remains" \
        "owned temporary remains in private stage"
fi

echo "SUMMARY pass=${passes} fail=${failures}"
if (( failures > 0 )); then
    exit 1
fi
