#!/usr/bin/env bash
# Build and install the Blockbook package for one coin over an existing install, then verify it.
#
#   sudo contrib/scripts/deploy-blockbook.sh COIN [options]
#
# Steps: report host capabilities, record the running instance, build the package with the
# production settings (through build-with-swap.sh when RAM is short), back up the installed
# files, install with --force-confold (keeps the server's blockchaincfg.json), restart and
# verify. A failed check restores the backup unless --no-rollback is given.
#
# Only the blockbook-<coin> package is built and installed. The backend package is never
# built, installed or restarted, and the index database is never touched.
#
# Options:
#   --check              print capabilities and the running instance, change nothing
#   --skip-build         install the package already in build/ instead of building
#   --sync-timeout SEC   how long to wait for inSync and the previous height (default 1800)
#   --api-timeout SEC    how long to wait for the API after the restart (default 900)
#   --no-rollback        leave the new version in place when a check fails
#
# Environment:
#   BLOCKBOOK_MIN_BUILD_MEM_MIB   RAM + free swap needed to build without extra swap (default 8192)
#   BLOCKBOOK_SWAP_MIB            temporary swap size passed to build-with-swap.sh (default 4096)
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

coin=''
check_only=0
skip_build=0
rollback=1
sync_timeout=1800
api_timeout=900
while [[ $# -gt 0 ]]; do
    case "$1" in
        --check) check_only=1 ;;
        --skip-build) skip_build=1 ;;
        --no-rollback) rollback=0 ;;
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

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
config=$repo/configs/coins/$coin.json
[[ -f $config ]] || fail "Unknown coin: $coin"
[[ $(uname -s) == Linux ]] || fail 'This script requires a Linux host.'
[[ $(id -u) == 0 ]] || fail 'Run this script as root (sudo).'
for tool in python3 git dpkg dpkg-deb systemctl curl awk df nproc; do
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
systemctl daemon-reload
if [[ -f $backup/unit.service ]] && ! diff -q "$backup/unit.service" "$unit_file" >/dev/null; then
    log 'Service unit changed:'
    diff -u "$backup/unit.service" "$unit_file" || true
fi
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
