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
# dotted IPv4 BLOCKBOOK_INSIGHT_HOST other than 0.0.0.0 or when that address is not assigned to
# the host (ip -o -4 addr show), when the package in build/ lacks the Insight flag, and, checked
# only after the build and just before the stop, when the port is held by a process other than
# this coin's running blockbook (ss -ltnp). --check runs all of these at once.
#
# First switch from the Node converter (it holds the port): the port check sits right before the
# service stop, after the build, so the converter keeps serving through the build but must be gone
# by then. Order: build (or run this script once, which builds and then refuses at the port check
# with nothing stopped), stop and disable the converter, then deploy with --skip-build. The port
# is dark from the converter stop until the new listener is ready (it answers 503 until -sync
# readiness). A rollback of this script restores the previous Blockbook but does NOT restart the
# converter, so the port stays dark until the owner re-enables the converter.
#
# After the restart the listener is probed (GET /api/status, 200 or 503);
# a refused connection is a failed check and triggers the rollback. The check also runs with
# --skip-build and --use-package-unit.
#
# Effective Insight port: a host unit or ExecStart drop-in kept without --use-package-unit never
# gets the package's flag; it runs its own command line. The port check and the probe then use the
# -insight=HOST:PORT of the ExecStart that will run after the restart, read from the unit and
# drop-in files on disk (a host may keep Blockbook on another port on purpose while a legacy
# explorer still holds the config port). A kept ExecStart without -insight opens no listener, so
# the Insight port check and probe are skipped. With --use-package-unit, or without any override,
# the port comes from the coin config. The script prints the port and where it came from.
#
# CPU instruction sets: RocksDB in the build image is compiled with -march=native unless the image
# was built with PORTABLE=1 (make passes PORTABLE only when it (re)builds the image), so a package
# can need AVX2/BMI2 and die with SIGILL on an older CPU. The build records what the package needs
# in build/<package>.deb.isa (from the image's RocksDB flags, expanded by its gcc); --skip-build
# without that file reads the local build image. --check and the step before the stop compare it
# with /proc/cpuinfo and refuse when a flag is missing.
#
# Rollback: restores the files, clears systemd's start limit (reset-failed), starts the service
# and waits up to 60 s for it to stay active, then says whether it runs. dpkg keeps listing the
# new version; the script warns and names any copy of the previous package it finds.
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
    # Optional third argument: the effective port from a kept ExecStart, for the message only.
    port=${3:-$port}
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

# insight_local_check HOST: refuse a bind that cannot succeed: HOST must be assigned to this
# machine. Needs ip. Prints the reason on failure.
insight_local_check() {
    local host=$1
    if ! command -v ip >/dev/null; then
        echo 'ip is required to check the Insight bind address.' >&2
        return 1
    fi
    if ! ip -o -4 addr show | insight_host_assigned "$host"; then
        echo "BLOCKBOOK_INSIGHT_HOST=$host is not an address assigned to this host (ip -o -4 addr show); Blockbook would fail to bind. Nothing was stopped or installed." >&2
        return 1
    fi
}

# insight_port_check PORT OWNPID: PORT must be free or held by OWNPID (the running blockbook of
# this coin on a redeploy). Needs ss. Prints the reason on failure. The deploy runs it just before
# the service stop, after the build, so the converter can keep serving while the build runs.
insight_port_check() {
    local port=$1 own=$2 listeners holders
    if ! command -v ss >/dev/null; then
        echo 'ss is required to check the Insight port.' >&2
        return 1
    fi
    # ss must succeed: an empty listing from a failed ss would read as a free port.
    if ! listeners=$(ss -ltnp); then
        echo "ss -ltnp failed, so the holder of port $port cannot be checked. Nothing was stopped or installed." >&2
        return 1
    fi
    holders=$(insight_port_holders "$port" "$own" <<<"$listeners" | paste -sd, -) || return 1
    if [[ -n $holders ]]; then
        echo "Port $port is already held by $holders, so Blockbook's Insight listener could not bind. If that is the insight-to-blockbook converter (Node), stop and disable it first, then run this script again (with --skip-build when the package is already built). Nothing was stopped or installed." >&2
        return 1
    fi
}

# insight_runtime_check HOST PORT OWNPID: both checks above, as --check reports them.
insight_runtime_check() {
    insight_local_check "$1" && insight_port_check "$2" "$3"
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

# ---- Effective ExecStart and Insight address -----------------------------------------------
# A host unit or ExecStart drop-in that the deploy keeps decides which -insight address Blockbook
# binds after the restart. These read the unit files on disk (what the next daemon-reload loads),
# not the running unit, so a drop-in written since the last reload counts too.

# Unit directories in systemd's precedence order (a unit in an earlier one masks later ones).
unit_dirs=(/etc/systemd/system /run/systemd/system /usr/lib/systemd/system /lib/systemd/system)

# unit_files SERVICE [ROOT]: print "fragment PATH" for the unit file systemd loads and then
# "dropin PATH" for each SERVICE.service.d/*.conf in the order systemd applies them (sorted by file
# name; a name in an earlier directory masks the same name later). ROOT prefixes every path (tests).
unit_files() {
    local svc=$1 root=${2:-} d f name seen=' '
    for d in "${unit_dirs[@]}"; do
        if [[ -f $root$d/$svc.service ]]; then echo "fragment $root$d/$svc.service"; break; fi
    done
    for d in "${unit_dirs[@]}"; do
        for f in "$root$d/$svc.service.d"/*.conf; do
            [[ -f $f ]] || continue
            name=${f##*/}
            [[ $seen == *" $name "* ]] && continue
            seen+="$name "
            printf '%s\t%s\n' "$name" "$f"
        done
    done | LC_ALL=C sort -t $'\t' -k1,1 | cut -f2- | sed 's/^/dropin /'
}

# unit_exec_start FILE...: the [Service] ExecStart that results from reading FILEs in order (an
# empty ExecStart= clears it, a later one replaces it; Blockbook units are Type=simple with one).
# Prints the file that set it on the first line and the command line on the second; both are empty
# when no file sets one. Joins lines continued with a trailing backslash.
unit_exec_start() {
    awk '
        function take(l,   v) {
            if (l ~ /^[ \t]*\[/) { sect = l; gsub(/[ \t]/, "", sect); return }
            if (sect != "[Service]" || l ~ /^[ \t]*[#;]/) return
            if (match(l, /^[ \t]*ExecStart[ \t]*=/)) {
                v = substr(l, RLENGTH + 1); sub(/^[ \t]+/, "", v); sub(/[ \t]+$/, "", v)
                cmd = v; src = (v == "" ? "" : FILENAME)
            }
        }
        FNR == 1 { sect = ""; buf = "" }
        {
            l = $0; sub(/\r$/, "", l)
            if (l ~ /\\$/) { buf = buf substr(l, 1, length(l) - 1) " "; next }
            take(buf l); buf = ""
        }
        END { print src; print cmd }' "$@"
}

# exec_insight_addr CMDLINE: print "PORT HOST" of the last -insight=HOST:PORT (or -insight HOST:PORT,
# --insight) on CMDLINE, as Go's flag package keeps the last one. HOST may be empty or a systemd
# variable reference. Prints nothing when the flag is absent or empty; returns 2 when its value
# has no :PORT.
exec_insight_addr() {
    local -a words
    local i v='' found=0
    read -r -a words <<<"$1"
    for ((i = 0; i < ${#words[@]}; i++)); do
        case ${words[i]} in
            -insight=*|--insight=*) v=${words[i]#*=}; found=1 ;;
            -insight|--insight) v=${words[i+1]:-}; found=1 ;;
        esac
    done
    (( found )) && [[ -n $v ]] || return 0
    [[ $v =~ ^(.*):([0-9]{1,5})$ ]] || return 2
    printf '%s %s\n' "${BASH_REMATCH[2]}" "${BASH_REMATCH[1]}"
}

# insight_effective SERVICE CONFIG_PORT USE_PACKAGE_UNIT [ROOT]: print "PORT|HOST|SOURCE" for the
# Insight listener that will run after the restart.
#   - --use-package-unit, or no host unit and no ExecStart drop-in: the package unit runs, so the
#     coin config port with ${BLOCKBOOK_INSIGHT_HOST}.
#   - otherwise the kept ExecStart decides; PORT and HOST are empty when it has no -insight flag.
# Returns 1 with a message when the kept ExecStart has an -insight value without a port.
insight_effective() {
    local svc=$1 cfgport=$2 usepkg=$3 root=${4:-} kind f frag='' kept_unit='' src cmd addr port host
    local -a files=() dropins=()
    while read -r kind f; do
        files+=("$f")
        if [[ $kind == fragment ]]; then
            frag=$f
        elif grep -q '^[[:space:]]*ExecStart[[:space:]]*=' "$f" 2>/dev/null; then
            dropins+=("$f")
        fi
    done < <(unit_files "$svc" "$root")
    # A fragment outside /lib and /usr/lib is a host unit that replaces the package's.
    [[ -n $frag && $frag != "$root"/lib/* && $frag != "$root"/usr/lib/* ]] && kept_unit=$frag
    # shellcheck disable=SC2016 # the literal systemd variable reference is the host spec
    if (( usepkg )) || [[ -z $kept_unit && ${#dropins[@]} == 0 ]]; then
        printf '%s|%s|%s\n' "$cfgport" '${BLOCKBOOK_INSIGHT_HOST}' 'ports.blockbook_insight in the coin config (package unit)'
        return 0
    fi
    { read -r src; read -r cmd; } < <(unit_exec_start "${files[@]}")
    [[ -n $src ]] || src=${kept_unit:-${dropins[0]}}
    if ! addr=$(exec_insight_addr "$cmd"); then
        echo "The ExecStart kept from $src has an -insight value without :PORT." >&2
        return 1
    fi
    if [[ -z $addr ]]; then
        printf '||kept ExecStart in %s\n' "$src"
        return 0
    fi
    read -r port host <<<"$addr"
    printf '%s|%s|-insight in the kept ExecStart in %s\n' "$port" "$host" "$src"
}

# ---- CPU instruction-set preflight ---------------------------------------------------------
# Go code is built with the default GOAMD64=v1 (nothing in the build sets it) and the Go runtime
# checks its level itself, and jemalloc comes from the distribution. The part that can need newer
# instructions is RocksDB, compiled in the build image with -march=native unless the image was
# built with PORTABLE=1 (build_detect_platform then sets no -march). Its flags are recorded in
# /opt/rocksdb/make_config.mk (PLATFORM_CXXFLAGS).

# isa_march FLAGS: the value of the last -march= in a compiler flag string (empty when none).
isa_march() {
    local -a words
    local w m=''
    read -r -a words <<<"$1"
    for w in "${words[@]}"; do [[ $w == -march=* ]] && m=${w#-march=}; done
    printf '%s\n' "$m"
}

# isa_from_gcc: read `gcc -Q --help=target -march=X` on stdin and print the enabled instruction
# set extensions beyond baseline x86-64 as /proc/cpuinfo flag names, space separated and sorted.
isa_from_gcc() {
    awk '
        BEGIN {
            n = split("sse3:pni ssse3:ssse3 sse4.1:sse4_1 sse4.2:sse4_2 popcnt:popcnt pclmul:pclmulqdq " \
                      "aes:aes avx:avx avx2:avx2 fma:fma f16c:f16c bmi:bmi1 bmi2:bmi2 lzcnt:abm " \
                      "movbe:movbe adx:adx sha:sha_ni avx512f:avx512f avx512cd:avx512cd " \
                      "avx512bw:avx512bw avx512dq:avx512dq avx512vl:avx512vl", t, " ")
            for (i = 1; i <= n; i++) { split(t[i], kv, ":"); map[kv[1]] = kv[2] }
        }
        $NF == "[enabled]" && $1 ~ /^-m/ { o = substr($1, 3); if (o in map) print map[o] }' |
        LC_ALL=C sort -u | paste -sd' ' -
}

# isa_cpu_flags: read /proc/cpuinfo on stdin and print the flags of the first CPU.
isa_cpu_flags() { awk -F: '$1 ~ /^flags[ \t]*$/ { sub(/^[ \t]+/, "", $2); print $2; exit }'; }

# isa_missing REQUIRED HAVE: print the words of REQUIRED that are not in HAVE (space separated).
isa_missing() {
    local w out=''
    for w in $1; do [[ " $2 " == *" $w "* ]] || out+="${out:+ }$w"; done
    printf '%s\n' "$out"
}

# isa_probe_image IMAGE: print "MARCH|FLAGS" for RocksDB in the build image (both empty for a
# portable build). -march=native is expanded by the image's gcc on this host, which is the CPU
# that built the image when it was built here (make builds it locally).
isa_probe_image() {
    local img=$1 cxx march out
    cxx=$(docker run --rm "$img" sed -n 's/^PLATFORM_CXXFLAGS=//p' /opt/rocksdb/make_config.mk 2>/dev/null) || return 1
    [[ -n $cxx ]] || return 1
    march=$(isa_march "$cxx")
    if [[ -z $march ]]; then echo '|'; return 0; fi
    out=$(docker run --rm "$img" gcc -Q --help=target "-march=$march" 2>/dev/null) || return 1
    printf '%s|%s\n' "$march" "$(isa_from_gcc <<<"$out")"
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

# Insight: the env file must name a usable address that is assigned to this host (before any
# build). The port check runs later, just before the service stop (step 5), so the converter keeps
# serving during the build; --check runs both now. insight_host and insight_port stay empty for
# other coins and when the kept ExecStart has no -insight flag.
#
# The port is the one that will be bound after the restart: a kept host unit or ExecStart drop-in
# may run Blockbook on another port than the coin config (see Effective Insight port above).
insight_host=''
insight_port=''
insight_msg=''
insight_local=1   # 0: the bind host is a wildcard or a name, so the "assigned here" check is skipped
cfg_insight_port=$(jq -r '(.ports.blockbook_insight // 0) | tostring' "$config")
if [[ $cfg_insight_port != 0 ]]; then
    insight_eff=$(insight_effective "$service" "$cfg_insight_port" "$use_package_unit" 2>&1) ||
        fail "$insight_eff Nothing was stopped or installed."
    IFS='|' read -r insight_port insight_spec insight_src <<<"$insight_eff"
    if [[ -z $insight_port ]]; then
        log "Insight: the $insight_src has no -insight flag, so Blockbook opens no Insight listener; the Insight port check and probe are skipped (--use-package-unit would run the package unit on port $cfg_insight_port)."
    else
        log "Insight: port $insight_port from the $insight_src"
        # shellcheck disable=SC2016 # literal systemd variable references
        if [[ $insight_spec == '${BLOCKBOOK_INSIGHT_HOST}' || $insight_spec == '$BLOCKBOOK_INSIGHT_HOST' ]]; then
            insight_host_src="BLOCKBOOK_INSIGHT_HOST from $insight_env_file"
            if insight_msg=$(insight_preflight "$config" "$insight_env_file" "$insight_port" 2>&1); then
                insight_host=$(insight_env_value "$insight_env_file")
            fi
        elif insight_host_valid "$insight_spec"; then
            insight_host=$insight_spec
            insight_host_src="literal address in the kept ExecStart"
        else
            # Empty or 0.0.0.0 binds every address, so probe loopback; a name is probed as given.
            insight_local=0
            insight_host=$insight_spec
            [[ -n $insight_host && $insight_host != 0.0.0.0 ]] || insight_host=127.0.0.1
            insight_host_src="kept ExecStart binds '${insight_spec}', probed at $insight_host"
        fi
        if [[ -n $insight_host ]]; then
            own_pid=$(num "$(systemctl show -p MainPID --value "$service")")
            if (( check_only )); then
                if (( insight_local )); then
                    insight_msg=$(insight_runtime_check "$insight_host" "$insight_port" "$own_pid" 2>&1) || true
                else
                    insight_msg=$(insight_port_check "$insight_port" "$own_pid" 2>&1) || true
                fi
                [[ -n $insight_msg ]] || log "Insight: $insight_host:$insight_port is bindable ($insight_host_src)"
            elif (( insight_local )); then
                insight_msg=$(insight_local_check "$insight_host" 2>&1) || true
                [[ -n $insight_msg ]] || log "Insight: $insight_host is assigned to this host ($insight_host_src); port $insight_port is checked before the stop"
            else
                log "Insight: $insight_host_src; port $insight_port is checked before the stop"
            fi
        fi
        if [[ -z $insight_host ]] || [[ -n $insight_msg ]]; then
            if (( check_only )); then log "Warning: $insight_msg"; else fail "$insight_msg"; fi
        fi
    fi
fi

# CPU: the package's RocksDB may need instruction sets this CPU lacks (see CPU instruction sets
# above). Checked here for --check and again before the stop, against the package that is installed.
bin_image=blockbook-build
# cpu_check DEB: return 1 with the reason in cpu_msg when this CPU lacks a flag the package needs.
# Logs which flags it needs and where that came from; unknown needs are only a warning.
cpu_msg=''
cpu_check() {
    local deb_file=$1 src march req missing line
    if [[ -f $deb_file.isa ]]; then
        march=$(sed -n 's/^march=//p' "$deb_file.isa")
        req=$(sed -n 's/^requires=//p' "$deb_file.isa")
        src="$deb_file.isa, recorded when the package was built"
    elif (( docker_ok )) && line=$(isa_probe_image "$bin_image"); then
        march=${line%%|*}
        req=${line#*|}
        src="RocksDB flags in the local $bin_image image; assumes ${deb_file##*/} was built from it here"
    else
        log "Warning: cannot tell which instruction sets ${deb_file##*/} needs (no $deb_file.isa and the $bin_image image cannot be read); CPU check skipped."
        return 0
    fi
    if [[ -z $march ]]; then
        log "CPU: RocksDB was built portable (no -march; $src); any x86-64 CPU runs it"
        return 0
    fi
    missing=$(isa_missing "$req" "$(isa_cpu_flags < /proc/cpuinfo)")
    if [[ -n $missing ]]; then
        cpu_msg="CPU lacks $missing, which ${deb_file##*/} needs (RocksDB built with -march=$march; $src). Blockbook would die with SIGILL (invalid opcode). Rebuild the build image portable (make build-images PORTABLE=1) and build the package again. Nothing was stopped or installed."
        return 1
    fi
    log "CPU: has every instruction set the package needs (RocksDB -march=$march: ${req:-none beyond x86-64}; $src)"
}
if (( check_only )); then
    cpu_check "$deb" || log "Warning: $cpu_msg"
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
    rm -f -- "$repo/build/${package}_"*.deb "$repo/build/${package}_"*.deb.isa
    if (( use_swap )); then
        BB_BUILD_ENV=prod "$repo/contrib/scripts/build-with-swap.sh" "$coin"
    else
        (cd "$repo" && BB_BUILD_ENV=prod make "release-deb-blockbook-$coin" PORTABLE=1)
    fi
    # Record what the package's RocksDB needs, so a later --skip-build (or a copy of the package
    # with this file on another host) is checked against the CPU that built it. PORTABLE=1 only
    # takes effect when make (re)builds the image; an older image keeps its -march.
    if [[ -f $deb ]] && isa_line=$(isa_probe_image "$bin_image"); then
        printf 'march=%s\nrequires=%s\nimage=%s\n' "${isa_line%%|*}" "${isa_line#*|}" \
            "$(docker image inspect -f '{{.Id}}' "$bin_image" 2>/dev/null || true)" > "$deb.isa"
        [[ -z ${isa_line%%|*} ]] ||
            log "Warning: the $bin_image image has RocksDB built with -march=${isa_line%%|*} (PORTABLE=1 does not rebuild an existing image); the package needs ${isa_line#*|}. Recorded in $deb.isa."
    else
        log "Warning: could not read the RocksDB flags of the $bin_image image; $deb.isa not written."
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
    systemctl daemon-reload || true
    # A failed new version (a crash loop, SIGILL) leaves the unit at systemd's start limit, and the
    # start below would be refused ("Start request repeated too quickly"), leaving Blockbook down.
    systemctl reset-failed "$service" || true
    systemctl start "$service" || log "systemctl start $service failed; checking its state"
    # Running means active on two polls in a row, so a unit that crash-loops (active, then
    # activating again) does not count.
    local state='' streak=0 waited
    for ((waited = 0; waited <= 60; waited += 5)); do
        state=$(systemctl is-active "$service" || true)
        if [[ $state == active ]]; then
            streak=$((streak + 1))
            (( streak >= 2 )) && break
        else
            streak=0
        fi
        sleep 5
    done
    if (( streak >= 2 )); then
        log "Rollback: $service is running (active) on the restored files."
    else
        log "Rollback: $service is NOT running (state '$state' after ${waited}s). Start it by hand: systemctl reset-failed $service && systemctl start $service; journalctl -u $service shows why."
    fi
    # dpkg is not rolled back: reinstalling a package in this failure path would run its
    # maintainer scripts again on a host in an unknown state, and no copy of the previous package
    # can be trusted (the build removes build/${package}_*.deb, dpkg -i installs are not cached by
    # apt, and the version string often stays the same across commits).
    local new_version prev_deb=''
    new_version=$(dpkg-deb -f "$deb" Version)
    # apt keeps a package it downloaded under this name (an epoch's ':' is stored as %3a).
    if [[ -f /var/cache/apt/archives/${package}_${installed_version//:/%3a}_amd64.deb ]]; then
        prev_deb=/var/cache/apt/archives/${package}_${installed_version//:/%3a}_amd64.deb
    fi
    log "Warning: dpkg now lists $package $new_version (from $deb), but the files on disk are the restored $installed_version from $backup."
    if [[ -n $prev_deb ]]; then
        log "A copy of the previous package is at $prev_deb; dpkg --force-confold -i it to align dpkg."
    else
        log "No copy of the previous $installed_version package was found; to align dpkg, build commit ${pre_commit:-<previous commit>} and install it with dpkg --force-confold -i. The restored files are in $backup."
    fi
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
# The Insight port check runs here, after the build and just before the stop, so the converter on
# a first switch keeps serving through the build. A failure leaves everything running.
if [[ -n $insight_host ]]; then
    own_pid=$(num "$(systemctl show -p MainPID --value "$service")")
    if ! insight_msg=$(insight_port_check "$insight_port" "$own_pid" 2>&1); then fail "$insight_msg"; fi
    log "Insight: port $insight_port is free or held by $service ($insight_src)"
fi
cpu_check "$deb" || fail "$cpu_msg"
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
    (( probed )) || verify_failed "Insight listener $insight_host:$insight_port ($insight_src) does not answer /api/status"
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
