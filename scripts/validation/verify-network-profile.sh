#!/bin/bash
set -u -o pipefail

# Read-only Debian 13 amd64 validation for one immutable VPN network profile.
# Run as root on a disposable, freshly installed candidate. This proves the
# generated daemon configuration and the live Bitcoin Core identity. Wallet,
# address, invoice, payment, channel, backup, reboot, and Tor reachability
# scenarios remain explicit certification-matrix steps.

if [[ ${EUID} -ne 0 ]]; then
    echo "ERROR: run as root on the disposable Debian fixture" >&2
    exit 2
fi

profile=${1:-}
case "${profile}" in
    mainnet)
        core_chain=main
        core_dir=.
        lnd_network=mainnet
        rpc_port=8332
        p2p_port=8333
        zmq_block=28332
        zmq_tx=28333
        genesis=000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f
        challenge=
        core_selector=
        core_cli_flag=
        lnd_selector=bitcoin.mainnet=true
        invoice_prefix=lnbc
        ;;
    testnet4)
        core_chain=testnet4
        core_dir=testnet4
        lnd_network=testnet4
        rpc_port=48332
        p2p_port=48333
        zmq_block=28334
        zmq_tx=28335
        genesis=00000000da84f2bafbbc53dee25a72ae507ff4914b867c565be350b0da8bf043
        challenge=
        core_selector=testnet4=1
        core_cli_flag=-testnet4
        lnd_selector=bitcoin.testnet4=true
        invoice_prefix=lntb
        ;;
    public-signet)
        core_chain=signet
        core_dir=signet
        lnd_network=signet
        rpc_port=38332
        p2p_port=38333
        zmq_block=28336
        zmq_tx=28337
        genesis=00000008819873e925422c1ff0f99f7cc9bbb232af63a077a480a3633bee1ef6
        challenge=512103ad5e0edad18cb1f0fc0d28a3d4f1f3e445640337489abb10404f2d1e086be430210359ef5021964fe22d6f8e05b2463c9540ce96883fe3b278760f048f5189f2e6c452ae
        core_selector=signet=1
        core_cli_flag=-signet
        lnd_selector=bitcoin.signet=true
        invoice_prefix=lntbs
        ;;
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

assert_file_contains() {
    local label=$1
    local file=$2
    local value=$3
    if grep -Fqx -- "${value}" "${file}" 2>/dev/null; then
        pass "${label}"
    else
        fail "${label}" "${file} lacks exact line '${value}'"
    fi
}

assert_file_excludes() {
    local label=$1
    local file=$2
    local value=$3
    if ! grep -Fq -- "${value}" "${file}" 2>/dev/null; then
        pass "${label}"
    else
        fail "${label}" "${file} contains '${value}'"
    fi
}

assert_file_excludes_line_prefix() {
    local label=$1
    local file=$2
    local value=$3
    if ! grep -Eq -- "^${value}" "${file}" 2>/dev/null; then
        pass "${label}"
    else
        fail "${label}" "${file} contains a line beginning '${value}'"
    fi
}

assert_file_prefix_count() {
    local label=$1
    local file=$2
    local value=$3
    local want=$4
    local got
    got=$(grep -Ec -- "^${value}" "${file}" 2>/dev/null || true)
    assert_equal "${label}" "${got}" "${want}"
}

os_id=$(sed -n 's/^ID=//p' /etc/os-release | tr -d '"')
os_version=$(sed -n 's/^VERSION_ID=//p' /etc/os-release | tr -d '"')
assert_equal "ENV Debian" "${os_id}" "debian"
assert_equal "ENV Debian release" "${os_version}" "13"
assert_equal "ENV architecture" "$(dpkg --print-architecture)" "amd64"
assert_equal "ENV systemd PID 1" "$(cat /proc/1/comm)" "systemd"

recorded_profile=$(python3 - <<'PY'
import json
with open("/etc/vpn/config.json", encoding="utf-8") as handle:
    print(json.load(handle)["network"])
PY
)
assert_equal "CFG immutable profile" "${recorded_profile}" "${profile}"

ledger_profile=$(python3 - <<'PY'
import json
with open("/var/lib/vpn/private/install-state.json", encoding="utf-8") as handle:
    print(json.load(handle)["context"]["network"])
PY
)
assert_equal "LIFE ledger profile" "${ledger_profile}" "${profile}"

bitcoin_conf=/etc/bitcoin/bitcoin.conf
lnd_conf=/etc/lnd/lnd.conf
assert_file_contains "BTC cookie disabled" "${bitcoin_conf}" "norpccookiefile=1"
assert_file_excludes_line_prefix "BTC no cookie path" "${bitcoin_conf}" \
    "rpccookiefile="
assert_file_prefix_count "BTC two rpcauth identities" "${bitcoin_conf}" \
    "rpcauth=" 2
assert_file_contains "BTC RPC port" "${bitcoin_conf}" "rpcport=${rpc_port}"
assert_file_contains "BTC block ZMQ" "${bitcoin_conf}" \
    "zmqpubrawblock=tcp://127.0.0.1:${zmq_block}"
assert_file_contains "BTC transaction ZMQ" "${bitcoin_conf}" \
    "zmqpubrawtx=tcp://127.0.0.1:${zmq_tx}"
assert_file_excludes "BTC no custom signet challenge" "${bitcoin_conf}" \
    "signetchallenge="
assert_file_excludes "BTC no custom signet seed" "${bitcoin_conf}" \
    "signetseednode="
if [[ -n ${core_selector} ]]; then
    assert_file_contains "BTC selected profile" "${bitcoin_conf}" "${core_selector}"
else
    assert_file_excludes "BTC mainnet excludes testnet4" "${bitcoin_conf}" "testnet4=1"
    assert_file_excludes "BTC mainnet excludes signet" "${bitcoin_conf}" "signet=1"
fi

assert_file_contains "LND selected profile" "${lnd_conf}" "${lnd_selector}"
assert_file_contains "LND RPC port" "${lnd_conf}" \
    "bitcoind.rpchost=127.0.0.1:${rpc_port}"
assert_file_contains "LND block ZMQ" "${lnd_conf}" \
    "bitcoind.zmqpubrawblock=tcp://127.0.0.1:${zmq_block}"
assert_file_contains "LND transaction ZMQ" "${lnd_conf}" \
    "bitcoind.zmqpubrawtx=tcp://127.0.0.1:${zmq_tx}"
assert_file_contains "LND SQLite backend" "${lnd_conf}" \
    "db.backend=sqlite"
assert_file_contains "LND native SQL" "${lnd_conf}" \
    "db.use-native-sql=true"
assert_file_contains "LND disk safety threshold" "${lnd_conf}" \
    "healthcheck.diskspace.diskrequired=0.10"
assert_file_excludes "LND no bbolt tuning" "${lnd_conf}" "db.bolt."
assert_file_excludes "LND no custom SQLite pragmas" "${lnd_conf}" \
    "db.sqlite.pragmaoptions="
assert_file_excludes "LND no skipped native migration" "${lnd_conf}" \
    "skip-native-sql-migration"
assert_file_excludes "LND no Bitcoin cookie" "${lnd_conf}" "bitcoind.rpccookie="
assert_file_excludes "LND no custom signet challenge" "${lnd_conf}" \
    "bitcoin.signetchallenge="
assert_file_excludes "LND no custom signet seed" "${lnd_conf}" \
    "bitcoin.signetseednode="

onion_bind_port=$((p2p_port + 1))
assert_file_contains "TOR Bitcoin P2P port" /etc/tor/torrc \
    "HiddenServicePort ${p2p_port} 127.0.0.1:${onion_bind_port}"
assert_file_contains "BTC no onion of its own" "${bitcoin_conf}" "listenonion=0"
assert_file_contains "BTC onion bind" "${bitcoin_conf}" \
    "bind=127.0.0.1:${onion_bind_port}=onion"
core_onion=$(tr -d '[:space:]' 2>/dev/null < /var/lib/tor/bitcoin-p2p/hostname)
if [[ -n ${core_onion} ]]; then
    assert_file_contains "BTC onion address" "${bitcoin_conf}" \
        "externalip=${core_onion}"
else
    fail "BTC onion address" "/var/lib/tor/bitcoin-p2p/hostname is missing or empty"
fi

declare -a bitcoin_cli
bitcoin_cli=(/usr/local/bin/bitcoin-cli
    -rpcconnect=127.0.0.1
    -rpcport="${rpc_port}"
    -rpcuser=vpn
    -stdinrpcpass)
if [[ -n ${core_cli_flag} ]]; then
    bitcoin_cli+=("${core_cli_flag}")
fi

chain_info=$("${bitcoin_cli[@]}" getblockchaininfo \
    < /var/lib/vpn/state/bitcoind-rpc.pass 2>/dev/null)
rpc_identity=$(python3 -c '
import json, sys
value = json.load(sys.stdin)
print(value.get("chain", "") + "|" + value.get("signet_challenge", ""))
' <<<"${chain_info}")
assert_equal "LIVE Core chain and challenge" "${rpc_identity}" \
    "${core_chain}|${challenge}"

live_genesis=$("${bitcoin_cli[@]}" getblockhash 0 \
    < /var/lib/vpn/state/bitcoind-rpc.pass 2>/dev/null)
assert_equal "LIVE Core genesis" "${live_genesis}" "${genesis}"

if [[ ${core_dir} == . ]]; then
    cookie_path=/var/lib/bitcoin/.cookie
else
    cookie_path=/var/lib/bitcoin/${core_dir}/.cookie
fi
for cookie_artifact in "${cookie_path}" "${cookie_path}.tmp"; do
    if [[ ! -e ${cookie_artifact} && ! -L ${cookie_artifact} ]]; then
        pass "LIVE no Bitcoin RPC cookie artifact ${cookie_artifact}"
    else
        fail "LIVE no Bitcoin RPC cookie artifact ${cookie_artifact}" \
            "unexpected path exists"
    fi
done

if [[ ${profile} == mainnet ]]; then
    for foreign_dir in testnet4 signet; do
        if [[ ! -e /var/lib/bitcoin/${foreign_dir} &&
              ! -L /var/lib/bitcoin/${foreign_dir} ]]; then
            pass "STATE no ${foreign_dir} Core reuse"
        else
            fail "STATE no ${foreign_dir} Core reuse" \
                "unexpected /var/lib/bitcoin/${foreign_dir} exists"
        fi
    done
else
    if [[ ! -e /var/lib/bitcoin/chainstate &&
          ! -L /var/lib/bitcoin/chainstate ]]; then
        pass "STATE no mainnet Core reuse"
    else
        fail "STATE no mainnet Core reuse" \
            "unexpected /var/lib/bitcoin/chainstate exists"
    fi
    foreign_dir=testnet4
    if [[ ${core_dir} == testnet4 ]]; then
        foreign_dir=signet
    fi
    if [[ ! -e /var/lib/bitcoin/${foreign_dir} &&
          ! -L /var/lib/bitcoin/${foreign_dir} ]]; then
        pass "STATE no ${foreign_dir} Core reuse"
    else
        fail "STATE no ${foreign_dir} Core reuse" \
            "unexpected /var/lib/bitcoin/${foreign_dir} exists"
    fi
fi

assert_equal "RUN bitcoind active" \
    "$(systemctl is-active bitcoind.service 2>/dev/null || true)" active
assert_equal "RUN lnd active" \
    "$(systemctl is-active lnd.service 2>/dev/null || true)" active
assert_equal "RUN Tor active" \
    "$(systemctl is-active tor.service 2>/dev/null || true)" active

state_json=$(/usr/local/bin/lncli \
    --rpcserver=127.0.0.1:10009 \
    --tlscertpath=/var/lib/lnd/tls.cert \
    state 2>/dev/null || true)
wallet_state=$(python3 -c '
import json, sys
try:
    print(json.load(sys.stdin).get("state", ""))
except Exception:
    print("")
' <<<"${state_json}")
case "${wallet_state}" in
    NON_EXISTING)
        pass "STATE LND reports no wallet"
        ;;
    LOCKED|UNLOCKED|RPC_ACTIVE|SERVER_ACTIVE)
        pass "STATE LND reports existing wallet (${wallet_state})"
        ;;
    *)
        fail "STATE LND wallet fact is known" \
            "State/GetState returned '${wallet_state:-unavailable}'"
        ;;
esac
for foreign in mainnet testnet4 signet; do
    if [[ ${foreign} == "${lnd_network}" ]]; then
        continue
    fi
    for foreign_state in \
        /var/lib/lnd/data/chain/bitcoin/${foreign} \
        /var/lib/lnd/data/graph/${foreign} \
        /var/lib/lnd/data/watchtower/bitcoin/${foreign}; do
        if [[ ! -e ${foreign_state} && ! -L ${foreign_state} ]]; then
            pass "STATE no ${foreign} reuse at ${foreign_state}"
        else
            fail "STATE no ${foreign} reuse at ${foreign_state}" \
                "unexpected path exists"
        fi
    done
done

assert_file_contains "CLI bitcoin wrapper RPC port" /home/vpn/.bashrc \
    "        -rpcport=${rpc_port} \\"
if [[ -n ${core_cli_flag} ]]; then
    assert_file_contains "CLI bitcoin wrapper profile" /home/vpn/.bashrc \
        "        ${core_cli_flag} \\"
fi
if [[ ${lnd_network} == mainnet ]]; then
    assert_file_excludes "CLI mainnet lncli has no alternate selector" \
        /home/vpn/.bashrc "        --network="
else
    assert_file_contains "CLI lncli profile" /home/vpn/.bashrc \
        "        --network=${lnd_network} \\"
fi

echo "INFO expected BOLT11 prefix=${invoice_prefix}"
echo "SUMMARY pass=${passes} fail=${failures}"
if (( failures > 0 )); then
    exit 1
fi
