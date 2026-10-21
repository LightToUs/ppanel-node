#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
installer="${repo_root}/scripts/install.sh"
work_dir="$(mktemp -d /tmp/ppanel-node-tuning-test.XXXXXX)"
trap 'rm -rf "$work_dir"' EXIT

mkdir -p "$work_dir/bin" "$work_dir/sysctl.d" "$work_dir/modules-load.d" "$work_dir/state"

cat > "$work_dir/bin/uname" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "-s" ]; then
    echo Linux
else
    echo x86_64
fi
EOF

cat > "$work_dir/bin/modprobe" <<'EOF'
#!/bin/sh
exit 0
EOF

cat > "$work_dir/bin/sysctl" <<'EOF'
#!/bin/sh
set -eu

lookup() {
    awk -F= -v key="$1" '
        $1 == key {
            print substr($0, index($0, "=") + 1)
            found = 1
            exit
        }
        END { if (!found) exit 1 }
    ' "$FAKE_SYSCTL_STATE"
}

set_value() {
    key="$1"
    value="$2"
    temp="${FAKE_SYSCTL_STATE}.tmp"
    awk -F= -v key="$key" '$1 != key' "$FAKE_SYSCTL_STATE" > "$temp"
    printf '%s=%s\n' "$key" "$value" >> "$temp"
    mv "$temp" "$FAKE_SYSCTL_STATE"
}

case "${1:-}" in
    -n)
        lookup "$2"
        ;;
    -p)
        while IFS= read -r line; do
            case "$line" in
                ""|\#*) continue ;;
            esac
            key="$(printf '%s' "${line%%=*}" | sed 's/[[:space:]]*$//')"
            value="$(printf '%s' "${line#*=}" | sed 's/^[[:space:]]*//')"
            set_value "$key" "$value"
        done < "$2"
        ;;
    *)
        exit 1
        ;;
esac
EOF

chmod +x "$work_dir/bin/uname" "$work_dir/bin/modprobe" "$work_dir/bin/sysctl"

cat > "$work_dir/sysctl.state" <<'EOF'
fs.file-max=1000000
fs.nr_open=1048576
net.core.somaxconn=4096
net.core.netdev_max_backlog=1000
net.core.rmem_max=212992
net.core.wmem_max=212992
net.core.optmem_max=20480
net.ipv4.tcp_max_syn_backlog=4096
net.ipv4.tcp_mtu_probing=0
net.ipv4.tcp_rmem=4096 131072 6291456
net.ipv4.tcp_wmem=4096 16384 4194304
net.ipv4.tcp_syncookies=1
net.ipv4.tcp_fastopen=1
net.ipv4.ip_local_port_range=32768 60999
net.ipv4.udp_rmem_min=4096
net.ipv4.udp_wmem_min=4096
net.netfilter.nf_conntrack_max=65536
net.ipv4.tcp_available_congestion_control=reno cubic bbr
net.core.default_qdisc=fq_codel
net.ipv4.tcp_congestion_control=cubic
EOF

cat > "$work_dir/meminfo" <<'EOF'
MemTotal:        4000000 kB
EOF

export PATH="$work_dir/bin:$PATH"
export FAKE_SYSCTL_STATE="$work_dir/sysctl.state"
export PPNODE_SYSCTL_DIR="$work_dir/sysctl.d"
export PPNODE_MODULES_LOAD_DIR="$work_dir/modules-load.d"
export PPNODE_TUNING_STATE_DIR="$work_dir/state"
export PPNODE_MEMINFO_FILE="$work_dir/meminfo"

red=""
green=""
yellow=""
plain=""
SKIP_SYSTEM_TUNING=false
eval "$(sed -n '/^tune_system()/,/^}/p' "$installer")"

checksum_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

tune_system >/dev/null

grep -qx 'net.core.somaxconn = 65535' "$work_dir/sysctl.d/99-ppanel-node.conf"
grep -qx 'net.netfilter.nf_conntrack_max = 250000' "$work_dir/sysctl.d/99-ppanel-node.conf"
grep -qx 'net.ipv4.tcp_congestion_control = bbr' "$work_dir/sysctl.d/99-ppanel-node.conf"
grep -qx 'net.core.somaxconn = 4096' "$work_dir/state/sysctl.original.conf"
grep -qx 'net.ipv4.tcp_congestion_control = cubic' "$work_dir/state/sysctl.original.conf"
grep -qx 'net.ipv4.tcp_congestion_control=bbr' "$work_dir/sysctl.state"
grep -qx 'tcp_bbr' "$work_dir/modules-load.d/ppanel-node.conf"
test "$(stat -f '%Lp' "$work_dir/state/sysctl.original.conf" 2>/dev/null || stat -c '%a' "$work_dir/state/sysctl.original.conf")" = "600"

config_checksum="$(checksum_file "$work_dir/sysctl.d/99-ppanel-node.conf")"
original_checksum="$(checksum_file "$work_dir/state/sysctl.original.conf")"
tune_system >/dev/null
test "$config_checksum" = "$(checksum_file "$work_dir/sysctl.d/99-ppanel-node.conf")"
test "$original_checksum" = "$(checksum_file "$work_dir/state/sysctl.original.conf")"

echo "system tuning tests passed"
