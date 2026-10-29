#!/usr/bin/env bash
# Self-contained test for the blockchaincfg.json merge in deploy-blockbook.sh.
# Needs only bash and jq; runs against fixtures in a temp dir, no root, no host changes.
#
#   contrib/scripts/test-deploy-config-merge.sh
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=deploy-blockbook.sh
source "$here/deploy-blockbook.sh"

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
passed=0
failed=0

ok() { passed=$((passed + 1)); echo "ok   - $1"; }
bad() { failed=$((failed + 1)); echo "FAIL - $1" >&2; }
check() { # check NAME COMMAND...
    local name=$1; shift
    if "$@"; then ok "$name"; else bad "$name"; fi
}
absent() { ! grep -q -- "$1" <<<"$2"; } # absent NEEDLE TEXT
jqeq() { [[ $(jq -c "$2" "$1") == "$3" ]]; } # jqeq FILE FILTER EXPECTED

new_case() { # new_case NAME: fresh live/dist/backup fixtures
    case_dir=$work/$1
    mkdir -p "$case_dir"
    live=$case_dir/blockchaincfg.json
    dist=$live.dpkg-dist
    backup=$case_dir/backup
    cat > "$live" <<'EOF'
{
    "coin_name": "Groestlcoin",
    "rpc_url": "http://127.0.0.1:8321",
    "rpc_user": "hostuser",
    "rpc_pass": "hostpass",
    "mempool_workers": 8,
    "host_only_key": "keep me"
}
EOF
    cat > "$dist" <<'EOF'
{
    "coin_name": "Groestlcoin",
    "rpc_url": "http://127.0.0.1:1111",
    "rpc_user": "pkguser",
    "rpc_pass": "pkgpass",
    "rpc_url_ws": "ws://127.0.0.1:8322",
    "mempool_resync_batch_size": 100,
    "mempool_workers": 4,
    "fiat_rates_params": "{\"url\": \"https://api.example\", \"api_key\": \"s3cr3t-nested\"}",
    "api_token": "tok-123"
}
EOF
    chmod 640 "$live"
}

# 1. Added keys are merged, live values win, host-only key stays.
new_case added
rc=0; cfg_merge "$live" "$dist" added "$backup" || rc=$?
check 'added: returns 0' [ "$rc" = 0 ]
check 'added: rpc_url_ws merged' jqeq "$live" '.rpc_url_ws' '"ws://127.0.0.1:8322"'
check 'added: mempool_resync_batch_size merged' jqeq "$live" '.mempool_resync_batch_size' '100'
check 'added: api_token merged' jqeq "$live" '.api_token' '"tok-123"'
check 'added: rpc_url kept' jqeq "$live" '.rpc_url' '"http://127.0.0.1:8321"'
check 'added: rpc_pass kept' jqeq "$live" '.rpc_pass' '"hostpass"'
check 'added: changed key mempool_workers not applied' jqeq "$live" '.mempool_workers' '8'
check 'added: host-only key kept' jqeq "$live" '.host_only_key' '"keep me"'
check 'added: fiat_rates_params merged (missing on host)' jqeq "$live" '.fiat_rates_params | type' '"string"'
check 'added: backup holds the original' jqeq "$backup/blockchaincfg.json.pre-merge" 'has("rpc_url_ws") | not' 'true'
check 'added: dist file untouched' jqeq "$dist" '.rpc_url' '"http://127.0.0.1:1111"'
check 'added: file mode kept' [ "$(stat -c %a "$live" 2>/dev/null || stat -f %Lp "$live")" = 640 ]
check 'added: no temp files left' [ "$(find "$case_dir" -name '*.merge.*' | wc -l | tr -d ' ')" = 0 ]

# 2. A second run changes nothing (idempotent), reports 10.
rc=0; cfg_merge "$live" "$dist" added "$backup" || rc=$?
check 'added: second run is a no-op (10)' [ "$rc" = 10 ]

# 3. none = report only, nothing written.
new_case none
before=$(cat "$live")
rc=0; cfg_merge "$live" "$dist" none "$backup" || rc=$?
check 'none: returns 10' [ "$rc" = 10 ]
check 'none: live file unchanged' [ "$(cat "$live")" = "$before" ]
check 'none: no backup made' [ ! -e "$backup" ]

# 4. pick takes the package value of the named keys, plus the added ones.
new_case pick
rc=0; cfg_merge "$live" "$dist" 'pick:mempool_workers,rpc_url' "$backup" || rc=$?
check 'pick: returns 0' [ "$rc" = 0 ]
check 'pick: mempool_workers from package' jqeq "$live" '.mempool_workers' '4'
check 'pick: rpc_url from package' jqeq "$live" '.rpc_url' '"http://127.0.0.1:1111"'
check 'pick: unpicked rpc_pass keeps host value' jqeq "$live" '.rpc_pass' '"hostpass"'
check 'pick: added keys still merged' jqeq "$live" '.rpc_url_ws' '"ws://127.0.0.1:8322"'

new_case pickmissing
before=$(cat "$live")
rc=0; cfg_merge "$live" "$dist" 'pick:no_such_key' "$backup" 2>/dev/null || rc=$?
check 'pick: unknown key fails' [ "$rc" = 1 ]
check 'pick: unknown key leaves live unchanged' [ "$(cat "$live")" = "$before" ]

# 5. Redaction in the report.
new_case report
out=$(cfg_report "$live" "$dist")
check 'report: lists added rpc_url_ws with value' grep -q 'rpc_url_ws = "ws://127.0.0.1:8322"' <<<"$out"
check 'report: lists changed mempool_workers' grep -q 'mempool_workers: host 8, package 4' <<<"$out"
check 'report: lists host-only key' grep -q -- '- host_only_key' <<<"$out"
check 'report: rpc_pass redacted' grep -q 'rpc_pass: host <redacted>, package <redacted>' <<<"$out"
check 'report: rpc_user redacted' grep -q 'rpc_user: host <redacted>, package <redacted>' <<<"$out"
check 'report: added api_token redacted' grep -q 'api_token = <redacted>' <<<"$out"
for secret in hostpass pkgpass hostuser pkguser tok-123; do
    check "report: $secret not printed" absent "$secret" "$out"
done

new_case nested
cat > "$dist" <<'EOF'
{"coin_name": "Groestlcoin", "fiat_rates_params": {"url": "https://api.example", "api_key": "s3cr3t-nested"}}
EOF
out=$(cfg_report "$live" "$dist")
check 'report: nested api_key redacted' absent s3cr3t-nested "$out"
check 'report: nested non-secret value still shown' grep -q 'https://api.example' <<<"$out"

# 6. Invalid JSON leaves the original untouched.
new_case baddist
printf '{"rpc_url_ws": ' > "$dist"
before=$(cat "$live")
rc=0; cfg_merge "$live" "$dist" added "$backup" 2>/dev/null || rc=$?
check 'invalid dist: fails' [ "$rc" = 1 ]
check 'invalid dist: live unchanged' [ "$(cat "$live")" = "$before" ]
check 'invalid dist: no backup, no temp files' [ ! -e "$backup" ]
check 'invalid dist: no temp files left' [ "$(find "$case_dir" -name '*.merge.*' | wc -l | tr -d ' ')" = 0 ]

new_case badlive
printf 'not json at all' > "$live"
rc=0; cfg_merge "$live" "$dist" added "$backup" 2>/dev/null || rc=$?
check 'invalid live: fails' [ "$rc" = 1 ]
check 'invalid live: live unchanged' [ "$(cat "$live")" = 'not json at all' ]

new_case notobject
echo '[1,2,3]' > "$dist"
before=$(cat "$live")
rc=0; cfg_merge "$live" "$dist" added "$backup" 2>/dev/null || rc=$?
check 'non-object dist: fails' [ "$rc" = 1 ]
check 'non-object dist: live unchanged' [ "$(cat "$live")" = "$before" ]

new_case backupfail
before=$(cat "$live")
: > "$case_dir/blocker"
rc=0; cfg_merge "$live" "$dist" added "$case_dir/blocker/sub" 2>/dev/null || rc=$?
check 'backup failure: fails' [ "$rc" = 1 ]
check 'backup failure: live unchanged' [ "$(cat "$live")" = "$before" ]

# unit_redact: secret-looking Environment= values are masked, others pass through
red=$(printf '%s\n' '+Environment=INFURA_API_KEY=abc' 'Environment="RPC_PASS=x"' 'Environment=MALLOC_CONF=narenas:8' | unit_redact)
check 'unit_redact: masks a key' grep -qx '+Environment=INFURA_API_KEY=<redacted>' <<< "$red"
check 'unit_redact: masks a quoted password' grep -qx 'Environment="RPC_PASS=<redacted>' <<< "$red"
check 'unit_redact: keeps other values' grep -qx 'Environment=MALLOC_CONF=narenas:8' <<< "$red"
check 'unit_redact: no secret left' bash -c '! grep -q "abc\|RPC_PASS=x" <<< "$1"' _ "$red"

# Insight bind host preflight: refuse an Insight-enabled install without BLOCKBOOK_INSIGHT_HOST
mkdir -p "$work/insight"
printf '{"ports":{"blockbook_public":9130,"blockbook_insight":9876}}\n' > "$work/insight/on.json"
printf '{"ports":{"blockbook_public":9130}}\n' > "$work/insight/off.json"
envf=$work/insight/blockbook.env
refuses() { ! insight_preflight "$1" "$2" 2>/dev/null; } # refuses CONFIG ENVFILE
check 'insight preflight: missing env file is refused' refuses "$work/insight/on.json" "$work/insight/none.env"
printf 'MALLOC_CONF=x\n' > "$envf"
check 'insight preflight: variable absent is refused' refuses "$work/insight/on.json" "$envf"
printf 'BLOCKBOOK_INSIGHT_HOST=\n' > "$envf"
check 'insight preflight: empty value is refused' refuses "$work/insight/on.json" "$envf"
printf '# BLOCKBOOK_INSIGHT_HOST=10.100.0.5\n; BLOCKBOOK_INSIGHT_HOST=10.100.0.6\n' > "$envf"
check 'insight preflight: commented value is refused' refuses "$work/insight/on.json" "$envf"
printf 'BLOCKBOOK_INSIGHT_HOST=0.0.0.0\n' > "$envf"
check 'insight preflight: wildcard is refused' refuses "$work/insight/on.json" "$envf"
printf 'BLOCKBOOK_INSIGHT_HOST=10.100.0.300\n' > "$envf"
check 'insight preflight: bad octet is refused' refuses "$work/insight/on.json" "$envf"
printf 'BLOCKBOOK_INSIGHT_HOST=example.com\n' > "$envf"
check 'insight preflight: hostname is refused' refuses "$work/insight/on.json" "$envf"
printf 'BLOCKBOOK_INSIGHT_HOST=10.100.0.5\n' > "$envf"
check 'insight preflight: valid value passes' insight_preflight "$work/insight/on.json" "$envf"
printf 'MALLOC_CONF=x\nBLOCKBOOK_INSIGHT_HOST="10.100.0.7"\n' > "$envf"
check 'insight preflight: quoted value is unquoted' [ "$(insight_env_value "$envf")" = 10.100.0.7 ]
printf 'BLOCKBOOK_INSIGHT_HOST=10.100.0.5\nBLOCKBOOK_INSIGHT_HOST=\n' > "$envf"
check 'insight preflight: last assignment wins (empty)' refuses "$work/insight/on.json" "$envf"
check 'insight preflight: coin without the port ignores the env file' insight_preflight "$work/insight/off.json" "$work/insight/none.env"
check 'insight preflight: real bitcoin config enables it' refuses "$here/../../configs/coins/bitcoin.json" "$work/insight/none.env"
check 'insight preflight: real ethereum config is unaffected' insight_preflight "$here/../../configs/coins/ethereum.json" "$work/insight/none.env"

# Insight bind address and port: parsed from ip/ss output, checked through fake ip/ss/curl
ip_out='2: eth0    inet 10.100.0.5/24 brd 10.100.0.255 scope global eth0
1: lo    inet 127.0.0.1/8 scope host lo'
check 'insight host: assigned address matches' bash -c 'source "$1"; insight_host_assigned 10.100.0.5 <<< "$2"' _ "$here/deploy-blockbook.sh" "$ip_out"
check 'insight host: other address is refused' bash -c 'source "$1"; ! insight_host_assigned 10.100.0.6 <<< "$2"' _ "$here/deploy-blockbook.sh" "$ip_out"
check 'insight host: prefix of an address is refused' bash -c 'source "$1"; ! insight_host_assigned 10.100.0.50 <<< "$2"' _ "$here/deploy-blockbook.sh" "$ip_out"
ss_out='State  Recv-Q Send-Q Local Address:Port Peer Address:Port Process
LISTEN 0      4096   10.100.0.5:9876   0.0.0.0:*   users:(("node",pid=4242,fd=19))
LISTEN 0      4096   10.100.0.5:19876  0.0.0.0:*   users:(("other",pid=7,fd=3))
LISTEN 0      4096            *:9130   *:*   users:(("blockbook",pid=99,fd=5),("blockbook",pid=98,fd=6))
LISTEN 0      4096         [::]:8080   [::]:*
LISTEN 0      128          [::]:9877   [::]:*   users:(("x",pid=5,fd=1))'
holders() { insight_port_holders "$1" "$2" <<< "$ss_out"; } # holders PORT OWNPID
check 'insight port: converter on the port is reported' [ "$(holders 9876 99)" = 'node(4242)' ]
check 'insight port: own process is not a conflict' [ -z "$(holders 9876 4242)" ]
check 'insight port: free port is not a conflict' [ -z "$(holders 9999 1)" ]
check 'insight port: a longer port number is not a match' [ "$(holders 9877 1)" = 'x(5)' ]
check 'insight port: every pid of a shared socket counts' [ "$(holders 9130 99)" = 'blockbook(98)' ]
check 'insight port: no process info is unknown' [ "$(holders 8080 1)" = unknown ]

refused() { ! "$@" 2>/dev/null; } # succeeds when the command fails
fakebin=$work/fakebin
mkdir -p "$fakebin"
printf '#!/bin/sh\nprintf "%%s\\n" "$FAKE_IP_OUT"\n' > "$fakebin/ip"
printf '#!/bin/sh\nprintf "%%s\\n" "$FAKE_SS_OUT"\n' > "$fakebin/ss"
printf '#!/bin/sh\nprintf "%%s" "$FAKE_CURL_CODE"\n[ "$FAKE_CURL_CODE" != 000 ]\n' > "$fakebin/curl"
chmod +x "$fakebin"/ip "$fakebin"/ss "$fakebin"/curl
fake() { PATH=$fakebin:$PATH FAKE_IP_OUT=$ip_out FAKE_SS_OUT=$ss_out FAKE_CURL_CODE=${CURL_CODE:-200} "$@"; }
check 'insight runtime: free port on a local address passes' fake insight_runtime_check 10.100.0.5 9999 1
check 'insight runtime: own listener passes on redeploy' fake insight_runtime_check 10.100.0.5 9876 4242
check 'insight runtime: foreign address is refused' refused fake insight_runtime_check 10.100.0.9 9999 1
check 'insight runtime: converter on the port is refused' refused fake insight_runtime_check 10.100.0.5 9876 1
msg=$(fake insight_runtime_check 10.100.0.5 9876 1 2>&1 || true)
check 'insight runtime: message names the holder and the converter' bash -c 'grep -q "node(4242)" <<< "$1" && grep -q "converter" <<< "$1"' _ "$msg"
msg=$(fake insight_runtime_check 10.100.0.9 9999 1 2>&1 || true)
check 'insight runtime: message names the address' bash -c 'grep -q "10.100.0.9 is not an address" <<< "$1"' _ "$msg"
check 'insight probe: 200 passes' fake insight_probe 10.100.0.5 9876
CURL_CODE=503; check 'insight probe: 503 (not ready) passes' fake insight_probe 10.100.0.5 9876
CURL_CODE=000; check 'insight probe: refused connection fails' refused fake insight_probe 10.100.0.5 9876
CURL_CODE=404; check 'insight probe: 404 fails' refused fake insight_probe 10.100.0.5 9876
unset CURL_CODE

# insight_unit_ok: a fake dpkg-deb streams a tar of the unit; needs GNU tar (--wildcards)
if tar --version 2>/dev/null | grep -q GNU; then
    unitdir=$work/unit/lib/systemd/system
    mkdir -p "$unitdir"
    printf '[Service]\nExecStart=/bin/blockbook -public=:9130 -insight=${BLOCKBOOK_INSIGHT_HOST}:9876 -explorer=\n' > "$unitdir/blockbook-bitcoin.service"
    printf '[Service]\nExecStart=/bin/blockbook -public=:9130 -insight=127.0.0.1:9876\n' > "$unitdir/blockbook-literal.service"
    printf '[Service]\nExecStart=/bin/blockbook -public=:9130\n' > "$unitdir/blockbook-plain.service"
    tar -C "$work/unit" -cf "$work/unit.tar" ./lib
    printf '#!/bin/sh\ncat "$FAKE_TAR"\n' > "$fakebin/dpkg-deb"
    chmod +x "$fakebin/dpkg-deb"
    unitok() { PATH=$fakebin:$PATH FAKE_TAR=$work/unit.tar insight_unit_ok x.deb "$1"; }
    check 'insight unit: env-host flag is found' unitok blockbook-bitcoin
    check 'insight unit: a literal host does not count' refused unitok blockbook-literal
    check 'insight unit: a unit without the flag is refused' refused unitok blockbook-plain
    check 'insight unit: a missing unit is refused' refused unitok blockbook-missing
else
    echo 'skip - insight_unit_ok tests need GNU tar'
fi

echo "$passed passed, $failed failed"
[[ $failed == 0 ]]
