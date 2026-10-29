#!/usr/bin/env bash
# Run real kernel VPN tests in a disposable Linux guest, retaining its serial
# console even if the guest kernel stalls. Never touch production services.
set -euo pipefail
mode="${1:?expected l2tp or combined}"
case "$mode" in l2tp|combined) ;; *) exit 2 ;; esac
[ "$(id -u)" -eq 0 ] || { echo 'Run as root' >&2; exit 2; }
: "${RUNNER_TEMP:?}"
: "${ANTIMAGE_L2TP_TEST_BINARY:?}"
: "${ANTIMAGE_L2TP_PANEL_TEST_BINARY:?}"
: "${ANTIMAGE_L2TP_API_TEST_BINARY:?}"
: "${ANTIMAGE_NODE_HELPER_BINARY:?}"
test -c /dev/kvm || { echo 'KVM is required for the native Linux guest' >&2; exit 1; }
root="$(mktemp -d "$RUNNER_TEMP/native-linux-guest.XXXXXX")"
evidence="$RUNNER_TEMP/antimage-guest-failure"
mkdir -p "$evidence" "$root/package/scripts" "$root/package/bin"
vm_pid=''
test_pid=''
cleanup() {
  local rc=$?
  trap - EXIT
  if [ -n "$vm_pid" ]; then
    kill -TERM "$vm_pid" 2>/dev/null || true
    for _ in $(seq 1 50); do
      state="$(ps -o stat= -p "$vm_pid" 2>/dev/null | tr -d ' ' || true)"
      if [ -z "$state" ] || [[ "$state" == Z* ]]; then break; fi
      sleep .1
    done
    kill -KILL "$vm_pid" 2>/dev/null || true
    wait "$vm_pid" 2>/dev/null || true
  fi
  if [ -n "$test_pid" ]; then
    kill -TERM "$test_pid" 2>/dev/null || true
    kill -KILL "$test_pid" 2>/dev/null || true
    wait "$test_pid" 2>/dev/null || true
  fi
  # The image and SSH key are disposable, never evidence artifacts.
  rm -f -- "$root/key" "$root/key.pub" "$root/disk.qcow2" "$root/seed.img" "$root/base.img"
  chmod -R a+rX "$evidence"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
image='https://cloud-images.ubuntu.com/jammy/20261025/jammy-server-cloudimg-amd64.img'
curl --fail --location --retry 3 "$image" -o "$root/base.img"
echo "58be26aa7452f3005a4f7370a43928bfb33a7f35a77a8f3b264bf00266a17ca0  $root/base.img" | sha256sum -c -
qemu-img create -f qcow2 -F qcow2 -b "$root/base.img" "$root/disk.qcow2" 16G
ssh-keygen -q -t ed25519 -N '' -f "$root/key"
cat >"$root/user-data" <<EOF
#cloud-config
disable_root: false
ssh_pwauth: false
users:
  - name: root
    lock_passwd: true
    ssh_authorized_keys:
      - $(cat "$root/key.pub")
EOF
printf 'instance-id: antimage-native\nlocal-hostname: antimage-native\n' >"$root/meta-data"
cloud-localds "$root/seed.img" "$root/user-data" "$root/meta-data"
port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
qemu-system-x86_64 -enable-kvm -cpu host -smp 2 -m 3072 -display none -no-reboot \
  -serial "file:$evidence/serial.log" \
  -drive "file=$root/disk.qcow2,format=qcow2,if=virtio" \
  -drive "file=$root/seed.img,format=raw,if=virtio" \
  -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:$port-:22" -device virtio-net-pci,netdev=net0 \
  >"$evidence/qemu.log" 2>&1 &
vm_pid=$!
ssh_args=(-i "$root/key" -p "$port" -o BatchMode=yes -o StrictHostKeyChecking=yes
  -o UserKnownHostsFile="$root/known_hosts" -o ConnectTimeout=5)
# Trust only the fresh guest's host key, obtained through its local forwarded
# port. This key and port are never reused for a production host.
ready=0
for _ in $(seq 1 120); do
  kill -0 "$vm_pid" 2>/dev/null || { cat "$evidence/qemu.log"; exit 1; }
  if ssh-keyscan -T 2 -p "$port" 127.0.0.1 >"$root/known_hosts" 2>/dev/null &&
    ssh "${ssh_args[@]}" root@127.0.0.1 'test -f /var/lib/cloud/instance/boot-finished'; then ready=1; break; fi
  sleep 2
done
[ "$ready" -eq 1 ] || { tail -100 "$evidence/serial.log"; exit 1; }
ssh "${ssh_args[@]}" root@127.0.0.1 'set -eu; uname -a; apt-get update; DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends iproute2 iptables nftables iputils-ping tcpdump ppp xl2tpd strongswan pptp-linux pptpd wireguard-tools openvpn openssl netcat-openbsd python3 "linux-modules-extra-$(uname -r)"' \
  2>&1 | tee "$evidence/provision.log"
cp -a scripts/ci "$root/package/scripts/"
install -m 0755 "$ANTIMAGE_L2TP_TEST_BINARY" "$root/package/bin/nodeagent.test"
install -m 0755 "$ANTIMAGE_L2TP_PANEL_TEST_BINARY" "$root/package/bin/nodecontroller.test"
install -m 0755 "$ANTIMAGE_L2TP_API_TEST_BINARY" "$root/package/bin/api.test"
install -m 0755 "$ANTIMAGE_NODE_HELPER_BINARY" "$root/package/bin/node-helper"
cat >"$root/package/run.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
cd /root/antimage-native
export RUNNER_TEMP="$PWD/evidence"
mkdir -p "$RUNNER_TEMP"
export ANTIMAGE_L2TP_TEST_BINARY="$PWD/bin/nodeagent.test"
export ANTIMAGE_L2TP_PANEL_TEST_BINARY="$PWD/bin/nodecontroller.test"
export ANTIMAGE_L2TP_API_TEST_BINARY="$PWD/bin/api.test"
export ANTIMAGE_NODE_HELPER_BINARY="$PWD/bin/node-helper"
export ANTIMAGE_WIREGUARD_TEST_BINARY="$ANTIMAGE_L2TP_TEST_BINARY"
export ANTIMAGE_OPENVPN_TEST_BINARY="$ANTIMAGE_L2TP_TEST_BINARY"
export ANTIMAGE_VPN_PANEL_TEST_BINARY="$ANTIMAGE_L2TP_PANEL_TEST_BINARY"
export ANTIMAGE_VPN_API_TEST_BINARY="$ANTIMAGE_L2TP_API_TEST_BINARY"
export ANTIMAGE_PPTP_TEST_BINARY="$ANTIMAGE_L2TP_TEST_BINARY"
export ANTIMAGE_PPTP_PANEL_TEST_BINARY="$ANTIMAGE_L2TP_PANEL_TEST_BINARY"
export ANTIMAGE_PPTP_API_TEST_BINARY="$ANTIMAGE_L2TP_API_TEST_BINARY"
python3 scripts/ci/test-native-isolated-run.py
if [ "$1" = combined ]; then python3 scripts/ci/native-isolated-run.py bash scripts/ci/native-vpn-drivers.sh; fi
python3 scripts/ci/native-isolated-run.py bash scripts/ci/native-l2tp-driver.sh
if [ "$1" = combined ]; then python3 scripts/ci/native-isolated-run.py bash scripts/ci/native-pptp-driver.sh; fi
EOF
tar -C "$root/package" -cf - . | ssh "${ssh_args[@]}" root@127.0.0.1 'mkdir -p /root/antimage-native; tar -C /root/antimage-native -xf -'
set +e
ssh "${ssh_args[@]}" root@127.0.0.1 "bash /root/antimage-native/run.sh $mode" >"$evidence/test.log" 2>&1 &
test_pid=$!
while kill -0 "$test_pid" 2>/dev/null; do
  if ! kill -0 "$vm_pid" 2>/dev/null || grep -Eq 'Kernel panic|BUG: soft lockup|watchdog:.*hard LOCKUP' "$evidence/serial.log"; then
    echo 'Guest kernel failed; retaining serial console evidence' >&2
    tail -100 "$evidence/serial.log"
    kill -TERM "$test_pid" 2>/dev/null
    break
  fi
  sleep 1
done
wait "$test_pid"
rc=$?
test_pid=''
cat "$evidence/test.log"
set -o pipefail
ssh "${ssh_args[@]}" root@127.0.0.1 \
  'tar -C /root/antimage-native/evidence -cf - .' |
  tar -C "$evidence" -xf -
set -e
exit "$rc"
