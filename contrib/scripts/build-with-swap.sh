#!/usr/bin/env bash
# Linux-only release build using temporary swap. Never installs packages or changes fstab.
set -euo pipefail

fail() { echo "Error: $*" >&2; exit 1; }
if [[ $# -lt 1 ]]; then
    echo "Usage: sudo $0 COIN [additional make arguments]" >&2
    exit 1
fi
coin=$1
shift
[[ $coin =~ ^[a-z0-9][a-z0-9_-]*$ ]] || fail 'Invalid coin identifier.'
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
[[ -f "$repo/configs/coins/$coin.json" ]] || fail "Unknown coin: $coin"
[[ $(uname -s) == Linux ]] || fail 'This script requires a Linux build host.'
[[ $(id -u) == 0 ]] || fail 'Run this script as root (sudo).'
for tool in flock findmnt df awk mktemp dd mkswap swapon swapoff make free; do
    command -v "$tool" >/dev/null || fail "Required command is missing: $tool"
done
size=${BLOCKBOOK_SWAP_MIB:-4096}
reserve=${BLOCKBOOK_SWAP_RESERVE_MIB:-4096}
[[ $size =~ ^[0-9]{1,6}$ && $reserve =~ ^[0-9]{1,6}$ ]] || fail 'Swap size and disk reserve must be integer MiB.'
size=$((10#$size))
reserve=$((10#$reserve))
(( size >= 1 && size <= 65536 && reserve >= 1024 )) || fail 'Swap must be 1–65536 MiB; reserve must be at least 1024 MiB.'
# /run is root-controlled; the lock lasts until this wrapper and inheriting children exit.
exec 9>/run/blockbook-build-swap.lock
flock -n 9 || fail 'Another temporary-swap build is running.'
filesystem=$(findmnt -n -o FSTYPE -T /var/tmp)
case "$filesystem" in
    ext2|ext3|ext4|xfs) ;;
    *) fail "Swap files on $filesystem are not supported by this wrapper (use ext4 or XFS)." ;;
esac
available=$(df -B1 --output=avail /var/tmp | awk 'NR==2 {print $1}')
[[ $available =~ ^[0-9]+$ ]] || fail 'Could not read free disk space.'
required=$(((size + reserve) * 1024 * 1024))
(( available >= required )) || fail "Need $((size + reserve)) MiB free on /var/tmp ($size MiB swap + $reserve MiB build reserve)."

swap_dir=''
swap_file=''
active=0
cleanup() {
    local status=$? used=0 available_ram=0 retained=0
    trap - EXIT HUP INT TERM
    if [[ -n $swap_file ]]; then
        # Check the kernel too, covering an interruption immediately after successful swapon.
        if (( active )) || awk -v file="$swap_file" '$1==file {found=1} END {exit !found}' /proc/swaps; then
            used=$(awk -v file="$swap_file" '$1==file {print $4}' /proc/swaps)
            used=${used:-0}
            available_ram=$(awk '$1=="MemAvailable:" {print $2}' /proc/meminfo)
            available_ram=${available_ram:-0}
            if (( used > 0 && available_ram < used + 262144 )); then
                echo 'Not enough available RAM to safely remove temporary swap now.' >&2
                retained=1
            elif ! swapoff "$swap_file"; then
                echo 'Could not disable temporary swap; leaving its file intact.' >&2
                retained=1
            fi
        fi
        if (( retained )); then
            printf 'Manual cleanup after freeing RAM: swapoff %q && rm -- %q && rmdir -- %q\n' "$swap_file" "$swap_file" "$swap_dir" >&2
            (( status != 0 )) || status=1
        else
            if ! rm -f -- "$swap_file" || ! rmdir -- "$swap_dir"; then
                echo "Temporary swap is disabled, but remove leftover directory: $swap_dir" >&2
                (( status != 0 )) || status=1
            else
                echo 'Temporary build swap removed.'
            fi
        fi
    fi
    exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
umask 077
swap_dir=$(mktemp -d /var/tmp/blockbook-build-swap.XXXXXX)
swap_file=$swap_dir/swap
printf 'Creating %s MiB temporary swap at %s\n' "$size" "$swap_file"
# dd writes real blocks (no sparse file); the private directory prevents path replacement.
dd if=/dev/zero of="$swap_file" bs=1M count="$size" conv=excl status=progress
mkswap "$swap_file"
swapon "$swap_file"
active=1
free -h
cd "$repo"
export BB_BUILD_ENV=${BB_BUILD_ENV:-prod}
make "release-deb-blockbook-$coin" PORTABLE=1 'BB_RPC_ENV=-e GOFLAGS="-p=1 -timeout=45m"' "$@"
