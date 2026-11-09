#!/usr/bin/env bash
# Self-contained test for the effective Insight port and host, the rollback "running" test and the
# CPU instruction-set preflight in deploy-blockbook.sh. Needs only bash and awk; runs against fixtures in a temp dir, no root, no
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

# Newer extensions map to the kernel's names; baseline and ABI/tuning options are ignored.
gcc_new='  -mavx512vnni                          [enabled]
  -mavx512vbmi                          [enabled]
  -mavx512vbmi2                         [enabled]
  -mavxvnni                             [enabled]
  -mgfni                                [enabled]
  -mvaes                                [enabled]
  -mamx-tile                            [enabled]
  -msha                                 [enabled]
  -mrdseed                              [enabled]
  -mmmx                                 [enabled]
  -msse                                 [enabled]
  -msse4                                [enabled]
  -mfxsr                                [enabled]
  -mred-zone                            [enabled]
  -mieee-fp                             [enabled]
  -mvzeroupper                          [enabled]
  -mavx512fp16                          [disabled]'
eq 'isa: newer extensions map, baseline and tuning options are ignored' "$(isa_from_gcc <<<"$gcc_new")" \
    'amx_tile avx512_vbmi2 avx512_vnni avx512vbmi avx_vnni gfni rdseed sha_ni vaes'
eq 'isa: nothing unknown in the mapped set' "$(isa_unknown_from_gcc <<<"$gcc_new")" ''
# Fail closed: an enabled extension the map lacks is kept by gcc's name, never dropped.
gcc_unk='  -mavx512vnni                          [enabled]
  -mfuturext                            [enabled]
  -mavx10.1-512                         [enabled]
  -mother-tuning                        [disabled]'
eq 'isa: unmapped enabled extensions are reported by gcc name' "$(isa_unknown_from_gcc <<<"$gcc_unk")" '-mavx10.1-512 -mfuturext'
eq 'isa: mapped ones still map beside unmapped ones' "$(isa_from_gcc <<<"$gcc_unk")" 'avx512_vnni'

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

# ---- quoted and unreadable -insight words -------------------------------------------------

eq 'addr: double-quoted word' "$(exec_insight_addr '/opt/bb "-insight=${BLOCKBOOK_INSIGHT_HOST}:9876" -public=:9198')" '9876 ${BLOCKBOOK_INSIGHT_HOST}'
eq "addr: single-quoted separate value" "$(exec_insight_addr "/opt/bb -insight '10.0.0.5:7000'")" '7000 10.0.0.5'
eq 'addr: -insightui is not the address flag' "$(exec_insight_addr '/opt/bb -insight=:3001 -insightui')" '3001 '
for w in '-insight:9876' '"-insight=10.0.0.5:1' '-insights=10.0.0.5:1'; do
    rc=0; exec_insight_addr "/opt/bb $w" >/dev/null || rc=$?
    eq "addr: unreadable word $w is refused" "$rc" 3
done
new_root quoted
mkdir -p "$root/etc/systemd/system/$svc.service.d"
printf '[Service]\nExecStart=\nExecStart=/opt/bb "-insight=${BLOCKBOOK_INSIGHT_HOST}:9876"\n' > "$root/etc/systemd/system/$svc.service.d/q.conf"
eq 'insight: quoted -insight in a kept drop-in is read' "$(insight_effective "$svc" 3001 0 "$root" | cut -d'|' -f1)" 9876
printf '[Service]\nExecStart=\nExecStart=/opt/bb -insight:9876\n' > "$root/etc/systemd/system/$svc.service.d/q.conf"
if msg=$(insight_effective "$svc" 3001 0 "$root" 2>&1); then bad 'insight: unreadable -insight word is refused'; else eq 'insight: unreadable -insight word is refused' "${msg/*mentions insight*/refused}" refused; fi
# Stand-in for "the build/install continues": the word refusal also stops the main flow (fail).

# ---- drop-in locations and symlinked units ------------------------------------------------

eq 'dropin dirs: specific, dash prefixes, type' "$(unit_dropin_dirs blockbook-zero-test | paste -sd' ' -)" \
    'blockbook-zero-test.service.d blockbook-zero-.service.d blockbook-.service.d service.d'
new_root prefix
mkdir -p "$root/etc/systemd/system/blockbook-.service.d"
printf '[Service]\nExecStart=\nExecStart=/opt/bb -insight=:9100\n' > "$root/etc/systemd/system/blockbook-.service.d/all.conf"
eq 'insight: dash-prefix drop-in counts' "$(insight_effective "$svc" 3001 0 "$root" | cut -d'|' -f1)" 9100
f=$root/etc/systemd/system/blockbook-.service.d/all.conf
if unit_dropin_shared "$svc" "$f"; then ok 'dropin: prefix directory is shared'; else bad 'dropin: prefix directory is shared'; fi
if unit_dropin_shared "$svc" "$root/etc/systemd/system/$svc.service.d/a.conf"; then bad 'dropin: own directory is not shared'; else ok 'dropin: own directory is not shared'; fi
# The same name: the earlier unit directory (/etc) wins even over the unit's own directory in /lib.
mkdir -p "$root/lib/systemd/system/$svc.service.d"
printf '[Service]\nExecStart=\nExecStart=/opt/bb -insight=:9200\n' > "$root/lib/systemd/system/$svc.service.d/all.conf"
eq 'insight: /etc prefix directory masks the same name in the /lib own directory' "$(insight_effective "$svc" 3001 0 "$root" | cut -d'|' -f1)" 9100
# systemd lists directories one unit directory at a time (own name, then dash prefixes), then the
# type directory per unit directory (dropin.c unit_file_find_dropin_paths); the first with a name wins.
win() { insight_effective "$svc" 3001 0 "$root" | cut -d'|' -f1; }
wr() { mkdir -p "${1%/*}"; printf '[Service]\nExecStart=\nExecStart=/opt/bb -insight=:%s\n' "$2" > "$1"; }
new_root prec
wr "$root/usr/lib/systemd/system/$svc.service.d/x.conf" 9401
wr "$root/etc/systemd/system/blockbook-.service.d/x.conf" 9402
eq 'prec: /etc prefix dir beats /usr/lib own dir (unit directory first)' "$(win)" 9402
wr "$root/etc/systemd/system/$svc.service.d/x.conf" 9403
eq 'prec: same unit directory: own dir beats prefix dir' "$(win)" 9403
rm -rf -- "$root/etc/systemd/system/$svc.service.d" "$root/etc/systemd/system/blockbook-.service.d"
wr "$root/etc/systemd/system/service.d/x.conf" 9404
eq 'prec: /usr/lib own dir beats /etc service.d (type dir last)' "$(win)" 9401
rm -rf -- "$root/usr/lib/systemd/system/$svc.service.d"
wr "$root/usr/lib/systemd/system/blockbook-.service.d/x.conf" 9405
eq 'prec: /usr/lib prefix dir beats /etc service.d' "$(win)" 9405
rm -rf -- "$root/usr/lib/systemd/system/blockbook-.service.d"
eq 'prec: service.d is used when it is the only one' "$(win)" 9404
new_root prec2
wr "$root/run/systemd/generator/$svc.service.d/x.conf" 9411
wr "$root/etc/systemd/system/blockbook-.service.d/x.conf" 9412
eq 'prec: /etc prefix dir beats /run/systemd/generator own dir' "$(win)" 9412
wr "$root/run/systemd/transient/$svc.service.d/x.conf" 9413
eq 'prec: transient own dir beats /etc prefix dir' "$(win)" 9413
wr "$root/etc/systemd/system.control/service.d/x.conf" 9414
eq 'prec: system.control service.d still loses to a name dir elsewhere' "$(win)" 9413
new_root runctl
for d in run/systemd/system.control run/systemd/transient run/systemd/system run/systemd/generator etc/systemd/system.control; do
    mkdir -p "$root/$d/$svc.service.d"
done
printf '[Service]\nExecStart=\nExecStart=/opt/bb -insight=:9301\n' > "$root/run/systemd/generator/$svc.service.d/50-gen.conf"
eq 'insight: generator drop-in counts' "$(insight_effective "$svc" 3001 0 "$root" | cut -d'|' -f1)" 9301
printf '[Service]\nExecStart=\nExecStart=/opt/bb -insight=:9302\n' > "$root/run/systemd/transient/$svc.service.d/50-gen.conf"
eq 'insight: transient masks generator for the same name' "$(insight_effective "$svc" 3001 0 "$root" | cut -d'|' -f1)" 9302
printf '[Service]\nExecStart=\nExecStart=/opt/bb -insight=:9303\n' > "$root/etc/systemd/system.control/$svc.service.d/60-ctl.conf"
eq 'insight: system.control drop-in sorts later and wins' "$(insight_effective "$svc" 3001 0 "$root" | cut -d'|' -f1)" 9303
printf '[Service]\nExecStart=\nExecStart=/opt/bb -insight=:9304\n' > "$root/run/systemd/system/$svc.service.d/70-run.conf"
eq 'insight: /run/systemd/system drop-in counts' "$(insight_effective "$svc" 3001 0 "$root" | cut -d'|' -f1)" 9304

new_root symlink
mkdir -p "$root/etc/systemd/system"
ln -s "../../../lib/systemd/system/$svc.service" "$root/etc/systemd/system/$svc.service"
eq 'insight: /etc symlink to the package unit is not a kept host unit' "$(insight_effective "$svc" 3001 0 "$root" | cut -d'|' -f1,3)" \
    '3001|ports.blockbook_insight in the coin config (package unit)'
if unit_in_package_dir "$root/etc/systemd/system/$svc.service" "$root"; then ok 'unit: symlink resolves into /lib'; else bad 'unit: symlink resolves into /lib'; fi
mkdir -p "$root/usr/local/lib/systemd/system"
if unit_in_package_dir "$root/usr/local/lib/systemd/system/x.service" "$root"; then bad 'unit: /usr/local/lib is a host unit'; else ok 'unit: /usr/local/lib is a host unit'; fi

# ---- one ExecStart test; Environment= and EnvironmentFile= --------------------------------

f=$work/spaced.conf
printf '[Service]\n  ExecStart = /opt/bb -x\n' > "$f"
if unit_sets_exec_start "$f"; then ok 'exec_start: indented ExecStart = is a replacement'; else bad 'exec_start: indented ExecStart = is a replacement'; fi
printf '[Service]\n#ExecStart=/opt/bb\nExecStartPre=/bin/true\n' > "$f"
if unit_sets_exec_start "$f"; then bad 'exec_start: comment and ExecStartPre are not'; else ok 'exec_start: comment and ExecStartPre are not'; fi

insight_env_file=$work/default.env
printf 'BLOCKBOOK_INSIGHT_HOST=10.0.0.1\n' > "$insight_env_file"
new_root envs-pkg
new_root envs
dd=$root/etc/systemd/system/$svc.service.d
mkdir -p "$dd" "$root/etc/bb"
printf '[Service]\nExecStart=\nExecStart=/opt/bb -insight=${BLOCKBOOK_INSIGHT_HOST}:9876\n' > "$dd/10-exec.conf"
eq 'env: kept drop-in uses the variable, nothing sets it, no EnvironmentFile: empty, refused' \
    "$(insight_host_setting "$svc" 0 "$root" | cut -d'|' -f1)" ''
if insight_host_setting "$svc" 0 "$root" | grep -qF "add EnvironmentFile=$insight_env_file to the override"; then ok 'env: the refusal tells the operator what to add'; else bad 'env: the refusal tells the operator what to add'; fi
eq 'env: package unit only (no host files kept) reads the default env file' "$(insight_host_setting "$svc" 0 "$work/envs-pkg")" "10.0.0.1|$insight_env_file"
mkdir -p "$root${insight_env_file%/*}"; cp "$insight_env_file" "$root$insight_env_file"
printf '[Service]\nEnvironmentFile=-%s\n' "$insight_env_file" > "$dd/05-default.conf"
eq 'env: a kept file naming the default env file reads it' "$(insight_host_setting "$svc" 0 "$root")" "10.0.0.1|$insight_env_file (EnvironmentFile= in $dd/05-default.conf)"
rm -f -- "$dd/05-default.conf" "$root$insight_env_file"
printf '[Service]\nEnvironment="BLOCKBOOK_INSIGHT_HOST=10.0.0.4" "OTHER=x"\n' > "$dd/20-env.conf"
eq 'env: quoted Environment= in a drop-in' "$(insight_host_setting "$svc" 0 "$root")" "10.0.0.4|Environment= in $dd/20-env.conf"
printf '[Service]\nEnvironment=BLOCKBOOK_INSIGHT_HOST=10.0.0.5\n' > "$dd/30-env.conf"
eq 'env: the last Environment= wins' "$(insight_host_setting "$svc" 0 "$root" | cut -d'|' -f1)" 10.0.0.5
printf 'BLOCKBOOK_INSIGHT_HOST="10.0.0.9"\n' > "$root/etc/bb/custom.env"
printf '[Service]\nEnvironmentFile=-/etc/bb/custom.env\nEnvironmentFile=-/etc/bb/missing.env\n' > "$dd/15-file.conf"
eq 'env: EnvironmentFile= overrides Environment=' "$(insight_host_setting "$svc" 0 "$root")" "10.0.0.9|/etc/bb/custom.env (EnvironmentFile= in $dd/15-file.conf)"
printf '[Service]\nEnvironmentFile=\nEnvironment=\n' > "$dd/90-reset.conf"
eq 'env: empty settings clear them: nothing sets it, refused' "$(insight_host_setting "$svc" 0 "$root" | cut -d'|' -f1)" ''
rm -f -- "$dd/90-reset.conf"
printf '[Service]\nEnvironment=BLOCKBOOK_INSIGHT_HOST=\n' > "$dd/30-env.conf"
rm -f -- "$dd/15-file.conf"
eq 'env: an empty assignment is an empty value' "$(insight_host_setting "$svc" 0 "$root")" "|Environment= in $dd/30-env.conf"
eq 'env: --use-package-unit reads the default env file' "$(insight_host_setting "$svc" 1 "$root")" "10.0.0.1|$insight_env_file"
if insight_host_check '' "Environment= in $dd/30-env.conf" cfg.json 9876 2>/dev/null; then bad 'env: empty value is refused'; else ok 'env: empty value is refused'; fi

# cpu_check with an unmapped extension warns (never a silent pass) and still checks the mapped ones.
udeb=$work/unk.deb
printf 'flags\t\t: fpu avx2\n' > "$work/cpuinfo-avx2"
printf 'flags\t\t: fpu sse2\n' > "$work/cpuinfo-sse2"
printf 'rocksdb-isa 1\nflags=-march=native \nThe following options are target specific:\n  -mavx2   [enabled]\n  -mfuturext   [enabled]\n' > "$udeb.isa"
rc=0; out=$(cpu_check "$udeb" "$work/cpuinfo-avx2") || rc=$?
eq 'cpu: unmapped extension: mapped ones met passes' "$rc" 0
if [[ $out == *'unknown extension(s) -mfuturext'*'CPU check cannot confirm'* ]]; then ok 'cpu: unmapped extension is warned about'; else bad "cpu: unmapped extension is warned about ($out)"; fi
if [[ $out == *'any x86-64 CPU runs it'* ]]; then bad 'cpu: unmapped extension is not called portable'; else ok 'cpu: unmapped extension is not called portable'; fi
rc=0; out=$(cpu_check "$udeb" "$work/cpuinfo-sse2") || rc=$?
eq 'cpu: unmapped extension: a missing mapped one still fails' "$rc" 1
printf 'rocksdb-isa 1\nflags=-march=native \nThe following options are target specific:\n  -mfuturext   [enabled]\n' > "$udeb.isa"
rc=0; out=$(cpu_check "$udeb" "$work/cpuinfo-sse2") || rc=$?
eq 'cpu: only unmapped extensions: warning, not a failure' "$rc" 0
if [[ $out == *'unknown extension(s) -mfuturext'* && $out != *'any x86-64 CPU runs it'* ]]; then ok 'cpu: only unmapped extensions: warned, not called portable'; else bad "cpu: only unmapped extensions: warned, not called portable ($out)"; fi

# ---- rollback: running means a steady MainPID and NRestarts --------------------------------

stable() { if svc_stable "$@"; then echo yes; else echo no; fi; }
eq 'rollback: steady for 15 s' "$(stable '0 active 100 0' '5 active 100 0' '10 active 100 0' '15 active 100 0')" yes
eq 'rollback: 10 s is too short' "$(stable '0 active 100 0' '5 active 100 0' '10 active 100 0')" no
eq 'rollback: two polls over 15 s are too few' "$(stable '0 active 100 0' '15 active 100 0')" no
eq 'rollback: crash loop (MainPID changes) is not running' \
    "$(stable '0 active 100 3' '5 active 120 4' '10 active 140 5' '15 active 160 6' '20 active 180 7')" no
eq 'rollback: NRestarts change is not running' "$(stable '0 active 100 1' '5 active 100 2' '10 active 100 3' '15 active 100 3')" no
eq 'rollback: steady after an early restart' \
    "$(stable '0 activating 0 0' '5 active 200 1' '10 active 200 1' '15 active 200 1' '20 active 200 1')" yes
eq 'rollback: last poll not active' "$(stable '0 active 100 0' '5 active 100 0' '10 active 100 0' '15 active 100 0' '20 activating 0 1')" no
eq 'rollback: MainPID 0 is not running' "$(stable '0 active 0 0' '5 active 0 0' '10 active 0 0' '15 active 0 0')" no

# ---- ISA record (written in the build image where RocksDB is compiled) --------------------

native_rec='rocksdb-isa 1
flags=-march=native 
The following options are target specific:
  -mlzcnt                               [enabled]
  -maes                                 [enabled]
  -mavx                                 [enabled]
  -mavx2                                [enabled]
  -mbmi                                 [enabled]
  -mbmi2                                [enabled]
  -mfma                                 [enabled]
  -mpopcnt                              [enabled]
  -msse4.2                              [enabled]
  -mavx512f                             [disabled]
  -march=                               x86-64'
portable_rec='rocksdb-isa 1
flags=
The following options are target specific:
  -m64                                  [enabled]
  -mavx                                 [disabled]
  -msse2                                [enabled]'
eq 'isa record: native' "$(isa_record_parse <<<"$native_rec")" '-march=native|abm aes avx avx2 bmi1 bmi2 fma popcnt sse4_2|'
eq 'isa record: portable needs nothing' "$(isa_record_parse <<<"$portable_rec")" '||'
for junk in '' 'march=native
requires=avx avx2' 'rocksdb-isa 1
flags=-march=native'; do
    if isa_record_parse <<<"$junk" >/dev/null; then bad "isa record: rejects '${junk%%$'\n'*}'"; else ok "isa record: rejects '${junk%%$'\n'*}'"; fi
done

deb=$work/blockbook-zero_1.0_amd64.deb
cpu=$work/cpuinfo
printf '%s\n' "$qemu_cpuinfo" > "$cpu"
rc=0; out=$(cpu_check "$deb" "$cpu") || rc=$?
eq 'cpu: no record is a warning, not a pass' "$rc|${out/*required instruction sets unknown*/unknown}" '0|unknown'
printf 'march=native\nrequires=\n' > "$deb.isa"   # the earlier format, derived on the deploy host
rc=0; out=$(cpu_check "$deb" "$cpu") || rc=$?
eq 'cpu: an old-format .isa is not trusted' "$rc|${out/*required instruction sets unknown*/unknown}" '0|unknown'
printf '%s\n' "$native_rec" > "$deb.isa"
rc=0; cpu_check "$deb" "$cpu" >/dev/null || rc=$?
eq 'cpu: AVX2 package on QEMU 2.5+ is refused' "$rc|${cpu_msg%%,*}" '1|CPU lacks abm aes avx avx2 bmi1 bmi2 fma popcnt sse4_2'
printf 'flags\t\t: fpu sse2 pni ssse3 sse4_1 sse4_2 popcnt aes avx avx2 bmi1 bmi2 fma abm\n' > "$work/cpuinfo-new"
rc=0; out=$(cpu_check "$deb" "$work/cpuinfo-new") || rc=$?
eq 'cpu: a CPU with the recorded sets passes' "$rc|${out/*has every instruction set*/has}" '0|has'
printf '%s\n' "$portable_rec" > "$deb.isa"
rc=0; out=$(cpu_check "$deb" "$cpu") || rc=$?
eq 'cpu: portable record passes anywhere' "$rc|${out/*built portable*/portable}" '0|portable'

echo "$passed passed, $failed failed"
[[ $failed == 0 ]]
