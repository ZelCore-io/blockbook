#!/usr/bin/env bash
# Self-contained test for the effective Insight port and the CPU instruction-set preflight in
# deploy-blockbook.sh. Needs only bash and awk; runs against fixtures in a temp dir, no root, no
# host changes.
#
#   contrib/scripts/test-deploy-blockbook-checks.sh
# The fixtures hold literal ${BLOCKBOOK_INSIGHT_HOST} references on purpose.
# shellcheck disable=SC2016
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=SCRIPTDIR/deploy-blockbook.sh
source "$here/deploy-blockbook.sh"

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
passed=0
failed=0

ok() { passed=$((passed + 1)); echo "ok   - $1"; }
bad() { failed=$((failed + 1)); echo "FAIL - $1" >&2; }
eq() { # eq NAME GOT EXPECTED
    if [[ $2 == "$3" ]]; then ok "$1"; else bad "$1 (got '$2', expected '$3')"; fi
}

svc=blockbook-zero
pkg_exec='/opt/coins/blockbook/zero/bin/blockbook -blockchaincfg=/opt/coins/blockbook/zero/config/blockchaincfg.json -public=:9198 -insight=${BLOCKBOOK_INSIGHT_HOST}:3001 -explorer='

new_root() { # new_root NAME: a fake filesystem root with the package unit only
    root=$work/$1
    mkdir -p "$root/lib/systemd/system"
    printf '[Unit]\nDescription=Blockbook daemon (Zero)\n\n[Service]\nExecStart=%s\nUser=blockbook-zero\n' "$pkg_exec" > "$root/lib/systemd/system/$svc.service"
}

# ---- effective Insight port --------------------------------------------------------------

# 1. Package unit only: the coin config port.
new_root pkgonly
eq 'insight: package unit uses the config port' \
    "$(insight_effective "$svc" 3001 0 "$root")" \
    '3001|${BLOCKBOOK_INSIGHT_HOST}|ports.blockbook_insight in the coin config (package unit)'

# 2. Kept ExecStart drop-in on another port (the legacy explorer holds the config port).
new_root dropin
mkdir -p "$root/etc/systemd/system/$svc.service.d"
dropin=$root/etc/systemd/system/$svc.service.d/insight-9876-until-swap.conf
printf '[Service]\nExecStart=\nExecStart=%s\n' "${pkg_exec/:3001/:9876}" > "$dropin"
printf '[Service]\nLimitNOFILE=65536\n' > "$root/etc/systemd/system/$svc.service.d/limits.conf"
eq 'insight: kept drop-in decides the port' \
    "$(insight_effective "$svc" 3001 0 "$root")" \
    "9876|\${BLOCKBOOK_INSIGHT_HOST}|-insight in the kept ExecStart in $dropin"
eq 'insight: --use-package-unit keeps the config port' \
    "$(insight_effective "$svc" 3001 1 "$root" | cut -d'|' -f1)" 3001

# 3. Drop-ins apply in file-name order: a later one replaces the ExecStart.
printf '[Service]\nExecStart=\nExecStart=/bin/blockbook -insight=10.0.0.7:7000\n' > "$root/etc/systemd/system/$svc.service.d/zz-later.conf"
eq 'insight: the last drop-in by name wins (literal host)' \
    "$(insight_effective "$svc" 3001 0 "$root" | cut -d'|' -f1,2)" '7000|10.0.0.7'
rm -f -- "$root/etc/systemd/system/$svc.service.d/zz-later.conf"

# 4. A host unit in /etc without -insight: no listener, so the runtime checks are skipped.
new_root hostunit
mkdir -p "$root/etc/systemd/system"
printf '[Service]\nExecStart=/opt/coins/blockbook/zero/bin/blockbook \\\n    -public=:9198 -explorer=\n' > "$root/etc/systemd/system/$svc.service"
eq 'insight: kept host unit without -insight gives no port' \
    "$(insight_effective "$svc" 3001 0 "$root")" \
    "||kept ExecStart in $root/etc/systemd/system/$svc.service"

# 5. A host unit whose -insight sits on a continuation line.
printf '[Service]\nExecStart=/opt/coins/blockbook/zero/bin/blockbook \\\n    -public=:9198 \\\n    -insight=${BLOCKBOOK_INSIGHT_HOST}:9876\n' > "$root/etc/systemd/system/$svc.service"
eq 'insight: continuation lines are joined' \
    "$(insight_effective "$svc" 3001 0 "$root" | cut -d'|' -f1)" 9876

# 6. Same drop-in name in /etc masks the one in /lib.
new_root masked
mkdir -p "$root/etc/systemd/system/$svc.service.d" "$root/lib/systemd/system/$svc.service.d"
printf '[Service]\nExecStart=\nExecStart=/bin/blockbook -insight=:5555\n' > "$root/lib/systemd/system/$svc.service.d/x.conf"
printf '[Service]\nExecStart=\nExecStart=/bin/blockbook -insight=:6666\n' > "$root/etc/systemd/system/$svc.service.d/x.conf"
eq 'insight: /etc drop-in masks the same name in /lib (empty host)' \
    "$(insight_effective "$svc" 3001 0 "$root" | cut -d'|' -f1,2)" '6666|'

# 7. An -insight value without a port is refused.
printf '[Service]\nExecStart=\nExecStart=/bin/blockbook -insight=10.0.0.7\n' > "$root/etc/systemd/system/$svc.service.d/x.conf"
if insight_effective "$svc" 3001 0 "$root" >/dev/null 2>&1; then bad 'insight: value without port is refused'; else ok 'insight: value without port is refused'; fi

# exec_insight_addr forms
eq 'addr: separate value, last flag wins' "$(exec_insight_addr '/bin/bb -insight 10.0.0.1:1 -x --insight=10.0.0.2:2')" '2 10.0.0.2'
eq 'addr: empty value means no listener' "$(exec_insight_addr '/bin/bb -insight= -public=:9198')" ''
eq 'addr: no flag' "$(exec_insight_addr '/bin/bb -public=:9198')" ''

# unit_exec_start: comments and other sections are ignored
f=$work/sections.service
printf '[Unit]\nExecStart=/wrong\n[Service]\n# ExecStart=/commented\nExecStart=/right -a\n[Install]\nWantedBy=multi-user.target\n' > "$f"
eq 'exec_start: only [Service] counts' "$(unit_exec_start "$f" | sed -n 2p)" '/right -a'

# ---- CPU instruction-set preflight --------------------------------------------------------

eq 'isa: no -march (PORTABLE=1)' "$(isa_march '-DROCKSDB_PLATFORM_POSIX -fno-builtin-memcmp -DZLIB')" ''
eq 'isa: -march=native' "$(isa_march '-DROCKSDB_PLATFORM_POSIX -march=native -DZLIB')" native
eq 'isa: last -march wins' "$(isa_march '-march=x86-64 -O2 -march=haswell')" haswell

gcc_out='The following options are target specific:
  -m64                                  [enabled]
  -maes                                 [enabled]
  -mavx                                 [enabled]
  -mavx2                                [enabled]
  -mavx512f                             [disabled]
  -mbmi                                 [enabled]
  -mbmi2                                [enabled]
  -mfma                                 [enabled]
  -mlzcnt                               [enabled]
  -mpclmul                              [enabled]
  -mpopcnt                              [enabled]
  -msse2                                [enabled]
  -msse4.2                              [enabled]
  -march=                               haswell'
eq 'isa: gcc options map to cpuinfo names' "$(isa_from_gcc <<<"$gcc_out")" \
    'abm aes avx avx2 bmi1 bmi2 fma pclmulqdq popcnt sse4_2'

qemu_cpuinfo='processor	: 0
vendor_id	: AuthenticAMD
model name	: QEMU Virtual CPU version 2.5+
flags		: fpu de pse tsc msr pae mce cx8 apic sep mtrr pge mca cmov pat pse36 clflush mmx fxsr sse sse2 syscall nx lm nopl cpuid pni cx16 x2apic hypervisor lahf_lm
processor	: 1
flags		: fpu other'
have=$(isa_cpu_flags <<<"$qemu_cpuinfo")
eq 'isa: first CPU flags are read' "${have%% *}" fpu
eq 'isa: QEMU 2.5+ lacks the haswell set' "$(isa_missing 'abm aes avx avx2 bmi1 bmi2 fma pclmulqdq popcnt sse4_2' "$have")" \
    'abm aes avx avx2 bmi1 bmi2 fma pclmulqdq popcnt sse4_2'
eq 'isa: a CPU with every flag passes' "$(isa_missing 'avx avx2 bmi2' 'fpu avx avx2 bmi1 bmi2 sse4_2')" ''
eq 'isa: nothing required passes' "$(isa_missing '' "$have")" ''
eq 'isa: no partial-name matches' "$(isa_missing 'avx' 'avx2 avx512f')" avx

echo "$passed passed, $failed failed"
[[ $failed == 0 ]]
