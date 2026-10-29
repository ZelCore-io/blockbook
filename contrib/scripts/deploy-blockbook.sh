#!/usr/bin/env bash
# Build and install the Blockbook package for one coin over an existing install, then verify it.
#
#   sudo contrib/scripts/deploy-blockbook.sh COIN [options]
#
# Steps: report host capabilities, record the running instance, build the package with the
# production settings (through build-with-swap.sh when RAM is short), back up the installed
# files, install with --force-confold (keeps the server's blockchaincfg.json), merge new
# settings of the package's blockchaincfg.json into it, restart and verify. A failed check
# restores the backup unless --no-rollback is given.
#
# Config drift: --force-confold keeps the host's blockchaincfg.json and drops the package's
# version next to it as blockchaincfg.json.dpkg-dist. After the install the script compares the
# two at the top level and prints three lists: keys the package added, keys whose value
# differs, and keys only the host has. Values of keys named like rpc_pass, rpc_user, password,
# secret, key or token are shown as <redacted>. By default the added keys are merged into the
# live file before the restart (the live value wins for every key that already exists); changed
# keys are only reported. The live file is backed up first, written through a temp file plus
# mv, and checked to be valid JSON. After a merge the .dpkg-dist file is renamed to
# .dpkg-dist.applied-<timestamp>. A failed merge keeps the original file and counts as a
# failed check.
#
# Service unit: a unit in /etc/systemd/system, or a drop-in that sets ExecStart, replaces the
# package's command line, so service changes from the package (such as the explorer feed flag)
# never reach the host; the script warns about both. --use-package-unit moves them into the
# backup directory after printing their diff against the package unit, so the package unit runs;
# a rollback puts them back.
#
# Insight listener: a coin whose config has ports.blockbook_insight (bitcoin, zcash) gets
# -insight=${BLOCKBOOK_INSIGHT_HOST}:PORT in the package unit. The host address comes from
# /etc/blockbook/blockbook.env (override the path with BLOCKBOOK_ENV_FILE). The script refuses
# to build or install, before stopping anything, when that file does not define a non-empty
# dotted IPv4 BLOCKBOOK_INSIGHT_HOST other than 0.0.0.0, when that address is not assigned to the
# host (ip -o -4 addr show), when the port is held by a process other than this coin's running
# blockbook (ss -ltnp; stop and disable the Node converter first), and when the package in build/
# lacks the Insight flag. After the restart the listener is probed (GET /api/status, 200 or 503);
# a refused connection is a failed check and triggers the rollback. The check also runs with
# --skip-build and --use-package-unit. Note that a host unit or ExecStart drop-in kept without
# --use-package-unit never gets the flag.
#
# Only the blockbook-<coin> package is built and installed. The backend package is never
# built, installed or restarted, and the index database is never touched.
#
# Options:
#   --check              print capabilities, the running instance and the config diff against an
#                        existing blockchaincfg.json.dpkg-dist, change nothing
#   --skip-build         install the package already in build/ instead of building
#   --sync-timeout SEC   how long to wait for inSync and the previous height (default 1800)
#   --api-timeout SEC    how long to wait for the API after the restart (default 900)
#   --no-rollback        leave the new version in place when a check fails
#   --use-package-unit   move a host unit in /etc/systemd/system and ExecStart drop-ins into the
#                        backup so the package's unit runs (see Service unit above)
#   --config-merge=MODE  what to do with the package's blockchaincfg.json (default added)
#                          added           merge keys missing on the host (live values win)
#                          none            report the differences only
#                          pick:KEY1,KEY2  as added, and also take the package value of the
#                                          named top-level keys, e.g. pick:fiat_rates_params
#
# Environment:
#   BLOCKBOOK_MIN_BUILD_MEM_MIB   RAM + free swap needed to build without extra swap (default 8192)
#   BLOCKBOOK_SWAP_MIB            temporary swap size passed to build-with-swap.sh (default 4096)
#   BLOCKBOOK_ENV_FILE            service env file with BLOCKBOOK_INSIGHT_HOST (default /etc/blockbook/blockbook.env)
#
# Requires jq on the host for the config comparison.
#
# Long builds: run under nohup and follow the log printed at start, e.g.
#   nohup contrib/scripts/deploy-blockbook.sh bitcoin > /root/bb-deploy.out 2>&1 &
set -euo pipefail

fail() { echo "Error: $*" >&2; exit 1; }
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }

usage() {
    sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed -e '$d' -e 's/^# \{0,1\}//'
    exit "${1:-0}"
}

# ---- blockchaincfg.json drift handling ----------------------------------------------------
# Top-level key comparison of the live config against the package's .dpkg-dist copy. These
# functions only need jq, so contrib/scripts/test-deploy-config-merge.sh can source this file.

# Key names whose values are never printed (case-insensitive).
cfg_secret_re='rpc_pass|rpc_user|password|secret|key|token'

# unit_redact: mask the value of Environment= lines whose name looks secret (stdin to stdout).
unit_redact() { sed -E "s/^([-+ ]?Environment=\"?[A-Za-z0-9_]*($cfg_secret_re)[A-Za-z0-9_]*=).*/\1<redacted>/I"; }

# cfg_validate FILE: succeed when FILE holds one JSON object.
cfg_validate() { jq -e 'type == "object"' "$1" >/dev/null 2>&1; }

# cfg_report LIVE DIST: print the added, changed and host-only top-level keys. Values of keys
# matching cfg_secret_re (also nested ones) are replaced by <redacted>; long values are cut.
cfg_report() {
    jq -r -n --slurpfile live "$1" --slurpfile dist "$2" --arg re "$cfg_secret_re" '
        def secret: test($re; "i");
        def hide: walk(if type == "object"
                       then with_entries(if (.key | secret) then .value = "<redacted>" else . end)
                       else . end);
        def show($k; $v): if ($k | secret) then "<redacted>" else ($v | hide | tojson | .[0:200]) end;
        $live[0] as $l | $dist[0] as $d |
        ($d | keys_unsorted | map(select(. as $k | $l | has($k) | not))) as $added |
        ($d | keys_unsorted | map(select(. as $k | ($l | has($k)) and $l[$k] != $d[$k]))) as $changed |
        ($l | keys_unsorted | map(select(. as $k | $d | has($k) | not))) as $only |
        "Added by the package, missing on the host (\($added | length)):",
        ($added[] | . as $k | "  + \($k) = \(show($k; $d[$k]))"),
        "Changed, host value differs from the package (\($changed | length)); never applied automatically:",
        ($changed[] | . as $k | "  ~ \($k): host \(show($k; $l[$k])), package \(show($k; $d[$k]))"),
        "Host-only, not in the package (\($only | length)):",
        ($only[] | "  - \(.)")'
}

# cfg_merge LIVE DIST MODE BACKUP_DIR
#   MODE is added, none or pick:KEY1,KEY2. Writes LIVE atomically (temp file in the same
#   directory with the mode and owner of LIVE, then mv) after copying it into BACKUP_DIR.
#   Returns 0 when LIVE was replaced, 10 when nothing was changed, 1 on any failure
#   (LIVE is left untouched).
cfg_merge() {
    local live=$1 dist=$2 mode=$3 backup_dir=$4 picks='' picks_json missing tmp
    case $mode in
        added) ;;
        none) return 10 ;;
        pick:*) picks=${mode#pick:} ;;
        *) echo "Error: bad config merge mode: $mode" >&2; return 1 ;;
    esac
    cfg_validate "$live" || { echo "Error: $live is not a valid JSON object" >&2; return 1; }
    cfg_validate "$dist" || { echo "Error: $dist is not a valid JSON object" >&2; return 1; }

    picks_json=$(jq -c -n --arg p "$picks" '$p | split(",") | map(select(length > 0))')
    missing=$(jq -r -n --slurpfile d "$dist" --argjson picks "$picks_json" \
        '$picks | map(select(. as $k | $d[0] | has($k) | not)) | join(",")')
    [[ -z $missing ]] || { echo "Error: pick: key(s) not in the package config: $missing" >&2; return 1; }

    tmp=$(mktemp "$live.merge.XXXXXX") || return 1
    # cp -p gives the temp file the mode and owner of the live file; its content is replaced.
    if ! cp -p "$live" "$tmp" ||
       ! jq --indent 4 --slurpfile d "$dist" --argjson picks "$picks_json" '
            . as $l | $d[0] as $d
            | $l
            + ($d | with_entries(select(.key as $k | $l | has($k) | not)))
            + ($d | with_entries(select(.key as $k | $picks | index($k) != null)))' "$live" > "$tmp"; then
        rm -f -- "$tmp"; echo 'Error: jq merge failed' >&2; return 1
    fi
    if ! cfg_validate "$tmp"; then
        rm -f -- "$tmp"; echo 'Error: merged config is not valid JSON' >&2; return 1
    fi
    # Same data as before: leave the file alone (no rewrite, no new backup).
    if [[ $(jq -S -c . "$tmp") == "$(jq -S -c . "$live")" ]]; then
        rm -f -- "$tmp"; return 10
    fi
    if ! { mkdir -p "$backup_dir" && cp -p "$live" "$backup_dir/blockchaincfg.json.pre-merge"; }; then
        rm -f -- "$tmp"; echo "Error: could not back up $live to $backup_dir" >&2; return 1
    fi
    if ! mv -f -- "$tmp" "$live"; then
        rm -f -- "$tmp"; echo "Error: could not replace $live" >&2; return 1
    fi
    return 0
}

# ---- Insight bind host preflight -----------------------------------------------------------
# A coin config with ports.blockbook_insight renders -insight=${BLOCKBOOK_INSIGHT_HOST}:PORT into
# the package's service unit. systemd fills the variable from EnvironmentFile=-/etc/blockbook/
# blockbook.env. When it is missing or empty the host part is empty and Blockbook refuses to
# start, so the install is refused up front, before anything is stopped. Coins without the port
# are not checked.

insight_env_file=${BLOCKBOOK_ENV_FILE:-/etc/blockbook/blockbook.env}

# insight_env_value FILE: the value of BLOCKBOOK_INSIGHT_HOST the way systemd reads FILE (the
# last assignment wins, '#' and ';' lines are comments, one pair of matching quotes is removed).
# Prints nothing when the file or the variable is missing.
insight_env_value() {
    [[ -r $1 ]] || return 0
    awk -v q="'" '
        /^[ \t]*[#;]/ { next }
        match($0, /^[ \t]*BLOCKBOOK_INSIGHT_HOST[ \t]*=/) {
            v = substr($0, RLENGTH + 1)
            sub(/^[ \t]+/, "", v); sub(/[ \t\r]+$/, "", v)
            n = length(v); c = substr(v, 1, 1)
            if (n >= 2 && (c == "\"" || c == q) && substr(v, n, 1) == c) v = substr(v, 2, n - 2)
            last = v
        }
        END { print last }' "$1"
}

# insight_host_valid VALUE: a dotted IPv4 address that is not the wildcard 0.0.0.0. The listener
# is meant for the private 10.100.0.x interface, so a wildcard bind is refused.
insight_host_valid() {
    local o1 o2 o3 o4
    [[ $1 =~ ^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})$ ]] || return 1
    o1=$((10#${BASH_REMATCH[1]})); o2=$((10#${BASH_REMATCH[2]}))
    o3=$((10#${BASH_REMATCH[3]})); o4=$((10#${BASH_REMATCH[4]}))
    (( o1 <= 255 && o2 <= 255 && o3 <= 255 && o4 <= 255 )) || return 1
    (( o1 + o2 + o3 + o4 > 0 ))
}

# insight_preflight COIN_CONFIG ENV_FILE: succeed when the coin does not enable Insight or the
# host env file holds a usable BLOCKBOOK_INSIGHT_HOST. Prints the reason and fails otherwise.
insight_preflight() {
    local cfg=$1 envfile=$2 port host
    port=$(jq -r '(.ports.blockbook_insight // 0) | tostring' "$cfg") || { echo "cannot read $cfg" >&2; return 1; }
    [[ $port =~ ^[0-9]+$ && $port != 0 ]] || return 0
    host=$(insight_env_value "$envfile")
    if [[ -z $host ]]; then
        echo "$cfg enables Insight on port $port, but $envfile does not define a non-empty BLOCKBOOK_INSIGHT_HOST. Add the host's private address, e.g. BLOCKBOOK_INSIGHT_HOST=10.100.0.x, and run again; nothing was stopped or installed." >&2
        return 1
    fi
    if ! insight_host_valid "$host"; then
        echo "BLOCKBOOK_INSIGHT_HOST='$host' in $envfile is not a usable bind address (need a dotted IPv4 address other than 0.0.0.0); nothing was stopped or installed." >&2
        return 1
    fi
}

# insight_host_assigned HOST: reads `ip -o -4 addr show` output on stdin and succeeds when HOST is
# an IPv4 address assigned to this machine (the bind would otherwise fail at start).
insight_host_assigned() {
    awk -v h="$1" '$3 == "inet" { split($4, a, "/"); if (a[1] == h) found = 1 } END { exit !found }'
}

# insight_port_holders PORT OWNPID: reads `ss -ltnp` output on stdin and prints one line per
# listener on PORT whose process is not OWNPID (name(pid), or "unknown" when ss shows no process).
# Prints nothing when the port is free or held only by OWNPID.
insight_port_holders() {
    awk -v port="$1" -v own="$2" '
        $1 == "LISTEN" {
            n = split($4, l, ":")
            if (l[n] != port) next
            rest = $0; name = ""; pids = ""
            if (match(rest, /users:\(\(.*\)\)/)) {
                u = substr(rest, RSTART, RLENGTH)
                if (match(u, /"[^"]*"/)) name = substr(u, RSTART + 1, RLENGTH - 2)
                while (match(u, /pid=[0-9]+/)) {
                    pids = pids " " substr(u, RSTART + 4, RLENGTH - 4)
                    u = substr(u, RSTART + RLENGTH)
                }
            }
            if (pids == "") { print "unknown"; next }
            m = split(pids, p, " ")
            for (i = 1; i <= m; i++) if (p[i] != own) print name "(" p[i] ")"
        }'
}

# insight_runtime_check HOST PORT OWNPID: refuse a bind that cannot succeed, before anything is
# stopped: HOST must be assigned to this machine, and PORT must be free or held by OWNPID (the
# running blockbook of this coin on a redeploy). Needs ip and ss. Prints the reason on failure.
insight_runtime_check() {
    local host=$1 port=$2 own=$3 listeners holders
    if ! command -v ip >/dev/null || ! command -v ss >/dev/null; then
        echo 'ip and ss are required to check the Insight bind address and port.' >&2
        return 1
    fi
    if ! ip -o -4 addr show | insight_host_assigned "$host"; then
        echo "BLOCKBOOK_INSIGHT_HOST=$host is not an address assigned to this host (ip -o -4 addr show); Blockbook would fail to bind. Nothing was stopped or installed." >&2
        return 1
    fi
    # ss must succeed: an empty listing from a failed ss would read as a free port.
    if ! listeners=$(ss -ltnp); then
        echo "ss -ltnp failed, so the holder of port $port cannot be checked. Nothing was stopped or installed." >&2
        return 1
    fi
    holders=$(insight_port_holders "$port" "$own" <<<"$listeners" | paste -sd, -) || return 1
    if [[ -n $holders ]]; then
        echo "Port $port is already held by $holders, so Blockbook's Insight listener could not bind. If that is the insight-to-blockbook converter (Node), stop and disable it first, then run this script again. Nothing was stopped or installed." >&2
        return 1
    fi
}

# insight_probe HOST PORT: succeed when the Insight listener answers /api/status with 200, or
# 503 while it is not ready yet. A refused connection (curl prints 000) fails.
insight_probe() {
    local code
    code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://$1:$2/api/status" 2>/dev/null) || code=000
    [[ $code == 200 || $code == 503 ]]
}

# insight_unit_ok DEB PACKAGE: succeed when the package's service unit carries the Insight flag.
insight_unit_ok() {
    local unit
    unit=$(dpkg-deb --fsys-tarfile "$1" | tar -xO --wildcards "*/$2.service" 2>/dev/null) || return 1
    # shellcheck disable=SC2016 # the literal systemd variable reference is what we look for
    grep -qF -- ' -insight=${BLOCKBOOK_INSIGHT_HOST}:' <<<"$unit"
}

# Sourced (by the test): stop here, only the functions above are wanted.
[[ ${BASH_SOURCE[0]} == "$0" ]] || return 0

coin=''
check_only=0
skip_build=0
rollback=1
use_package_unit=0
config_merge=added
sync_timeout=1800
api_timeout=900
while [[ $# -gt 0 ]]; do
    case "$1" in
        --check) check_only=1 ;;
        --skip-build) skip_build=1 ;;
        --no-rollback) rollback=0 ;;
        --use-package-unit) use_package_unit=1 ;;
        --config-merge=*) config_merge=${1#--config-merge=} ;;
        --sync-timeout) sync_timeout=${2:-}; shift ;;
        --api-timeout) api_timeout=${2:-}; shift ;;
        -h|--help) usage 0 ;;
        -*) fail "Unknown option: $1" ;;
        *) [[ -z $coin ]] || fail 'Give exactly one coin.'; coin=$1 ;;
    esac
    shift
done
[[ -n $coin ]] || usage 1
[[ $coin =~ ^[a-z0-9][a-z0-9_-]*$ ]] || fail 'Invalid coin identifier.'
[[ $sync_timeout =~ ^[0-9]+$ && $api_timeout =~ ^[0-9]+$ ]] || fail 'Timeouts must be whole seconds.'
[[ $config_merge == added || $config_merge == none ||
   $config_merge =~ ^pick:[A-Za-z0-9_.-]+(,[A-Za-z0-9_.-]+)*$ ]] ||
    fail "Invalid --config-merge value '$config_merge' (use added, none or pick:KEY1,KEY2)."

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
config=$repo/configs/coins/$coin.json
[[ -f $config ]] || fail "Unknown coin: $coin"
[[ $(uname -s) == Linux ]] || fail 'This script requires a Linux host.'
[[ $(id -u) == 0 ]] || fail 'Run this script as root (sudo).'
for tool in python3 git dpkg dpkg-deb systemctl curl awk df nproc jq; do
    command -v "$tool" >/dev/null || fail "Required command is missing: $tool"
done

# Values from the coin and environment configs.
read -r package alias port_public version install_root < <(python3 -I - "$config" "$repo/configs/environ.json" <<'EOF'
import json, sys
coin = json.load(open(sys.argv[1]))
env = json.load(open(sys.argv[2]))
print(coin["blockbook"]["package_name"], coin["coin"]["alias"], coin["ports"]["blockbook_public"],
      env["version"], env["blockbook_install_path"])
EOF
) || fail "Could not read $config."
[[ -n $package && -n $alias && $port_public =~ ^[0-9]+$ ]] || fail "Could not read $config."
service=$package
install_dir=$install_root/$alias
deb=$repo/build/${package}_${version}_amd64.deb
min_mem_mib=${BLOCKBOOK_MIN_BUILD_MEM_MIB:-8192}
[[ $min_mem_mib =~ ^[0-9]+$ ]] || fail 'BLOCKBOOK_MIN_BUILD_MEM_MIB must be whole MiB.'

stamp=$(date -u +%Y%m%dT%H%M%SZ)
log_dir=/var/log/blockbook-deploy
mkdir -p "$log_dir"
log_file=$log_dir/$coin-$stamp.log
exec > >(tee -a "$log_file") 2>&1
log "Log: $log_file"

# Local API, public port. Blockbook serves TLS with its own certificate, or plain HTTP.
api_get() {
    local path=$1
    curl -fsSk --max-time 20 "https://127.0.0.1:$port_public$path" 2>/dev/null ||
        curl -fsS --max-time 20 "http://127.0.0.1:$port_public$path" 2>/dev/null
}
api_field() { # api_field JSON KEY  (key inside .blockbook, or backend.blocks)
    python3 -I -c '
import json, sys
d = json.loads(sys.argv[1]); k = sys.argv[2]
v = d.get("backend", {}).get("blocks") if k == "backendBlocks" else d.get("blockbook", {}).get(k)
print("" if v is None else str(v).lower() if isinstance(v, bool) else v)' "$1" "$2" 2>/dev/null || true
}

# Digits only: values from the API port or systemctl never reach bash arithmetic unchecked.
num() { if [[ $1 =~ ^[0-9]{1,12}$ ]]; then echo "$((10#$1))"; else echo 0; fi; }

# 1. Capabilities
mem_total=$(awk '$1=="MemTotal:" {print int($2/1024)}' /proc/meminfo)
mem_avail=$(awk '$1=="MemAvailable:" {print int($2/1024)}' /proc/meminfo)
swap_free=$(awk '$1=="SwapFree:" {print int($2/1024)}' /proc/meminfo)
disk_repo=$(df -BM --output=avail "$repo" | awk 'NR==2 {print int($1)}')
disk_tmp=$(df -BM --output=avail /var/tmp | awk 'NR==2 {print int($1)}')
docker_ok=0
if command -v docker >/dev/null && docker info >/dev/null 2>&1; then docker_ok=1; fi
git_head=$(git -C "$repo" rev-parse HEAD)
git_dirty=$(git -C "$repo" status --porcelain --untracked-files=normal | wc -l)
build_mem=$((mem_avail + swap_free))
use_swap=0
(( build_mem >= min_mem_mib )) || use_swap=1

log "Host $(hostname): $(nproc) CPUs, RAM ${mem_total} MiB (${mem_avail} available), swap free ${swap_free} MiB"
log "Disk free: repo ${disk_repo} MiB, /var/tmp ${disk_tmp} MiB; docker usable: $([[ $docker_ok == 1 ]] && echo yes || echo no)"
log "Checkout $repo at ${git_head:0:12} ($git_dirty uncommitted paths)"
if (( skip_build )); then
    log "Build: skipped, using $deb"
elif (( use_swap )); then
    log "Build: ${build_mem} MiB < ${min_mem_mib} MiB, building through build-with-swap.sh"
else
    log "Build: ${build_mem} MiB is enough, building without temporary swap"
fi

# 2. Running instance
dpkg-query -W -f='${Status}' "$package" 2>/dev/null | grep -q 'install ok installed' ||
    fail "$package is not installed here; this script only upgrades an existing install."
systemctl cat "$service" >/dev/null 2>&1 || fail "Service $service not found."
installed_version=$(dpkg-query -W -f='${Version}' "$package")
pre_json=$(api_get /api/ || true)
pre_commit=$(api_field "$pre_json" gitCommit)
pre_height=$(api_field "$pre_json" bestHeight)
pre_restarts=$(num "$(systemctl show -p NRestarts --value "$service")")
log "Installed: $package $installed_version, service $(systemctl is-active "$service" || true)"
if [[ -n $pre_json ]]; then
    log "Running: version $(api_field "$pre_json" version), commit ${pre_commit:0:12}, height $pre_height, inSync $(api_field "$pre_json" inSync)"
else
    log "Running: API on port $port_public not reachable; height check after install is skipped."
fi
pre_height=$(num "$pre_height")

config_live=$install_dir/config/blockchaincfg.json
config_dist=$config_live.dpkg-dist
log "Config merge mode: $config_merge"
if [[ -f $config_dist ]]; then
    log "Pending package config $config_dist vs the live $config_live:"
    if cfg_validate "$config_live" && cfg_validate "$config_dist"; then
        cfg_report "$config_live" "$config_dist"
    else
        log 'Cannot compare: one of the files is not a valid JSON object.'
    fi
else
    log "No $config_dist yet; the diff is printed after the install."
fi

# Insight: the env file must name a usable address (before any build), and that address and port
# must be bindable (before any stop). insight_host and insight_port stay empty for other coins.
insight_host=''
insight_port=$(jq -r '(.ports.blockbook_insight // 0) | tostring' "$config")
[[ $insight_port != 0 ]] || insight_port=''
insight_msg=''
if [[ -n $insight_port ]]; then
    if insight_msg=$(insight_preflight "$config" "$insight_env_file" 2>&1); then
        insight_host=$(insight_env_value "$insight_env_file")
        own_pid=$(num "$(systemctl show -p MainPID --value "$service")")
        insight_msg=$(insight_runtime_check "$insight_host" "$insight_port" "$own_pid" 2>&1) &&
            log "Insight: $insight_host:$insight_port is bindable (BLOCKBOOK_INSIGHT_HOST from $insight_env_file)"
    fi
    if [[ -z $insight_host ]] || [[ -n $insight_msg ]]; then
        if (( check_only )); then log "Warning: $insight_msg"; else fail "$insight_msg"; fi
    fi
fi

if (( check_only )); then
    log 'Check only: nothing changed.'
    exit 0
fi

# 3. Build
if (( ! skip_build )); then
    (( docker_ok )) || fail 'Docker is required to build (docker info failed).'
    (( git_dirty == 0 )) || fail 'The checkout has uncommitted or untracked files; release builds need a clean tree.'
    (( disk_repo >= 8192 )) || fail "Need at least 8192 MiB free next to the checkout (have $disk_repo)."
    rm -f -- "$repo/build/${package}_"*.deb
    if (( use_swap )); then
        BB_BUILD_ENV=prod "$repo/contrib/scripts/build-with-swap.sh" "$coin"
    else
        (cd "$repo" && BB_BUILD_ENV=prod make "release-deb-blockbook-$coin" PORTABLE=1)
    fi
fi
[[ -f $deb ]] || fail "Package not found: $deb"
[[ $(dpkg-deb -f "$deb" Package) == "$package" ]] || fail "$deb is not a $package package."
log "Package: $deb ($(dpkg-deb -f "$deb" Version))"
if jq -e '(.ports.blockbook_insight // 0) > 0' "$config" >/dev/null && ! insight_unit_ok "$deb" "$package"; then
    fail "$deb was built without the Insight flag (-insight=\${BLOCKBOOK_INSIGHT_HOST}:PORT in its unit) although $config enables it; rebuild it, nothing was stopped."
fi

# 4. Backup of everything the package overwrites
backup=/var/backups/blockbook-deploy/$coin-$stamp
mkdir -p "$backup"
unit_file=$(systemctl show -p FragmentPath --value "$service")
for item in bin static cert config openapi.yaml; do
    [[ -e $install_dir/$item ]] && cp -a "$install_dir/$item" "$backup/"
done
[[ -n $unit_file && -f $unit_file ]] && cp -a "$unit_file" "$backup/unit.service"
printf '%s\n' "$installed_version" > "$backup/package-version"
log "Backup: $backup"

restore_backup() {
    log 'Restoring the previous version from the backup'
    systemctl stop "$service" || true
    for item in bin static cert config openapi.yaml; do
        if [[ -e $backup/$item ]]; then
            rm -rf -- "${install_dir:?}/$item"
            cp -a "$backup/$item" "$install_dir/"
        fi
    done
    [[ -f $backup/unit.service ]] && cp -a "$backup/unit.service" "$unit_file"
    if [[ -f $backup/dropins.list ]]; then
        while IFS= read -r dropin; do
            [[ -f $backup/dropins/$(basename "$dropin") ]] && cp -a "$backup/dropins/$(basename "$dropin")" "$dropin"
        done < "$backup/dropins.list"
    fi
    systemctl daemon-reload
    systemctl start "$service"
    sleep 10
    log "Service after restore: $(systemctl is-active "$service" || true)"
    log "dpkg still lists $package $(dpkg-deb -f "$deb" Version); reinstall $installed_version later to align it."
}

verify_failed() {
    log "Check failed: $*"
    journalctl -u "$service" -n 40 --no-pager || true
    if (( rollback )); then
        restore_backup
    else
        log "--no-rollback: leaving the new version in place. Backup: $backup"
    fi
    exit 1
}

# 5. Install
log "Stopping $service"
systemctl stop "$service"
log 'Installing (existing blockchaincfg.json is kept)'
if ! DEBIAN_FRONTEND=noninteractive dpkg --force-confold -i "$deb"; then
    verify_failed 'dpkg install failed'
fi

# Settings the new package ships for blockchaincfg.json land in .dpkg-dist and would be ignored.
# Report and merge them while the service is stopped. The pre-merge copy goes into the backup.
if [[ -f $config_dist ]]; then
    log "Package config differs from the host config ($config_dist), mode $config_merge:"
    if ! cfg_validate "$config_live" || ! cfg_validate "$config_dist"; then
        verify_failed "$config_live or $config_dist is not a valid JSON object"
    fi
    cfg_report "$config_live" "$config_dist" || verify_failed "could not compare $config_live with $config_dist"
    rc=0
    cfg_merge "$config_live" "$config_dist" "$config_merge" "$backup" || rc=$?
    case $rc in
        0)
            log "Merged into $config_live (previous file: $backup/blockchaincfg.json.pre-merge)"
            mv -f -- "$config_dist" "$config_dist.applied-$stamp"
            log "Package config kept as $config_dist.applied-$stamp"
            ;;
        10) log "Config merge ($config_merge): $config_live left as it is; $config_dist left in place." ;;
        *) verify_failed "config merge failed, $config_live is unchanged" ;;
    esac
else
    log "No $config_dist: the host config already matches the package or the package config did not change."
fi
systemctl daemon-reload
if [[ -f $backup/unit.service ]] && ! diff -q "$backup/unit.service" "$unit_file" >/dev/null; then
    log 'Service unit changed:'
    diff -u "$backup/unit.service" "$unit_file" | unit_redact || true
fi
# A unit in /etc/systemd/system or a drop-in that sets ExecStart replaces the package's command
# line, so new flags from the package (such as -explorercontenturl) do not reach the service.
pkg_unit=/lib/systemd/system/$service.service
live_unit=$(systemctl show -p FragmentPath --value "$service")
# /lib is a link to /usr/lib on current Debian and Ubuntu, and systemd reports either path.
if [[ -n $live_unit && -f $pkg_unit && $(readlink -f -- "$live_unit") != "$(readlink -f -- "$pkg_unit")" ]]; then
    if (( use_package_unit )); then
        log "--use-package-unit: moving $live_unit into the backup; differences from $pkg_unit:"
        diff -u "$live_unit" "$pkg_unit" | unit_redact || true
        rm -f -- "$live_unit"
    else
        log "Warning: $live_unit overrides $pkg_unit; the package's ExecStart is not used (see --use-package-unit)."
    fi
fi
for dropin in $(systemctl show -p DropInPaths --value "$service"); do
    grep -q '^ExecStart=' "$dropin" 2>/dev/null || continue
    if (( use_package_unit )); then
        mkdir -p "$backup/dropins"
        [[ -e $backup/dropins/$(basename "$dropin") ]] && verify_failed "two drop-ins named $(basename "$dropin")"
        cp -a "$dropin" "$backup/dropins/"
        printf '%s\n' "$dropin" >> "$backup/dropins.list"
        log "--use-package-unit: moving drop-in $dropin into the backup:"
        unit_redact < "$dropin" | sed 's/^/    /'
        rm -f -- "$dropin"
    else
        log "Warning: drop-in $dropin sets ExecStart; the package's ExecStart is not used (see --use-package-unit)."
    fi
done
if (( use_package_unit )); then
    systemctl daemon-reload
    log "Service unit now: $(systemctl show -p FragmentPath --value "$service")"
fi
# Count restarts from here: a unit switch or daemon-reload resets NRestarts, and only automatic
# restarts after this start count as a failed check.
pre_restarts=$(num "$(systemctl show -p NRestarts --value "$service")")
systemctl start "$service"

# 6. Verify
log "Waiting up to ${api_timeout}s for the API"
post_json=''
for ((waited = 0; waited < api_timeout; waited += 5)); do
    systemctl is-active --quiet "$service" || [[ $(systemctl is-active "$service") == activating ]] ||
        verify_failed "service is $(systemctl is-active "$service")"
    post_json=$(api_get /api/ || true)
    [[ -n $post_json ]] && break
    sleep 5
done
[[ -n $post_json ]] || verify_failed "API not reachable after ${api_timeout}s"

post_commit=$(api_field "$post_json" gitCommit)
[[ -n $post_commit && $git_head == "$post_commit"* ]] ||
    verify_failed "running commit '${post_commit}' is not the built ${git_head:0:12}"
log "Running commit ${post_commit:0:12}, version $(api_field "$post_json" version)"

if [[ -n $insight_host ]]; then
    probed=0
    for ((waited = 0; waited < 60; waited += 5)); do
        if insight_probe "$insight_host" "$insight_port"; then probed=1; break; fi
        systemctl is-active --quiet "$service" || break
        sleep 5
    done
    (( probed )) || verify_failed "Insight listener $insight_host:$insight_port does not answer /api/status (a host unit or ExecStart drop-in that replaces the package unit needs --use-package-unit)"
    log "Insight listener answers on $insight_host:$insight_port"
fi

log "Waiting up to ${sync_timeout}s for inSync at height >= $pre_height"
synced=0
height=0
for ((waited = 0; waited < sync_timeout; waited += 15)); do
    systemctl is-active --quiet "$service" || verify_failed 'service stopped while syncing'
    post_json=$(api_get /api/ || true)
    height=$(num "$(api_field "$post_json" bestHeight)")
    if [[ $(api_field "$post_json" inSync) == true ]] && (( height >= pre_height )); then
        synced=1
        break
    fi
    sleep 15
done
(( synced )) || verify_failed "not in sync at height >= $pre_height after ${sync_timeout}s (now $height)"
log "In sync at height $height (backend $(api_field "$post_json" backendBlocks))"

restarts=$(num "$(systemctl show -p NRestarts --value "$service")")
[[ $restarts == "$pre_restarts" ]] || verify_failed "service restarted $((restarts - pre_restarts)) times"

home=$(api_get / || true)
[[ $home == *hub-page* ]] || verify_failed 'home page does not render the explorer layout'
for banned in 'Trezor Bitcoin Explorer' 'trezor.io/terms-of-use' 'Trezor? Get one'; do
    [[ $home != *"$banned"* ]] || verify_failed "home page still contains '$banned'"
done
api_get /status >/dev/null || verify_failed '/status does not answer'

log "Deployed $package $(dpkg-deb -f "$deb" Version) at ${git_head:0:12}. Backup kept at $backup"
