#!/usr/bin/env bash
# test.sh: boots the EXPORTED disks — the one in the VirtualBox OVA and the
# one in the VMware zip — the way those hypervisors present them, and checks
# the CTF answers. Runs in the builder container, from deploy/vm/build.sh.
#
#   test.sh OUTDIR VERSION
#
# The hardware is VirtualBox's and VMware's, not QEMU's favourite: the disk
# on an AHCI (SATA) controller, two Intel e1000 adapters, the first with the
# way out. That is what proves the initramfs and the network config of
# provision.sh; the build itself ran on virtio. -snapshot: the files under
# test are never written.
#
# Checked per disk: /ctf/ answers and asks to join (nothing of anyone in
# it), its central server is the image's default, /juiceshop/ is Juice Shop,
# and there is no SSH server.
#
# What this cannot check: the import into VirtualBox or VMware itself (the
# OVF and the .vmx). That is the operator's check in deploy/vm/README.md.
set -euo pipefail

out="$1" version="$2"
name="juiceshop-ctf"
accel="${ACCEL:-kvm}"
work="$(mktemp -d)"
qpid=""
trap '[ -n "$qpid" ] && kill "$qpid" 2>/dev/null; rm -rf "$work"' EXIT

fail() { echo "FAIL: $*" >&2; [ -f "$work/console.log" ] && tail -40 "$work/console.log" >&2; exit 1; }
ok() { echo "   ok: $*"; }

boot() { # boot DISK FORMAT
    local disk="$1" fmt="$2"
    : >"$work/console.log"
    qemu-system-x86_64 -accel "$accel" -machine pc -m 2048 -smp 2 -snapshot \
        -device ahci,id=ahci \
        -drive if=none,id=d0,file="$disk",format="$fmt" -device ide-hd,drive=d0,bus=ahci.0 \
        -netdev user,id=n0,hostfwd=tcp:127.0.0.1:18580-:80,hostfwd=tcp:127.0.0.1:18522-:22 -device e1000,netdev=n0 \
        -netdev user,id=n1,net=192.168.56.0/24 -device e1000,netdev=n1 \
        -display none -serial file:"$work/console.log" &
    qpid=$!
}

halt() { kill "$qpid" 2>/dev/null || true; wait "$qpid" 2>/dev/null || true; qpid=""; }

check() { # check WHAT
    local what="$1" B=http://127.0.0.1:18580 page
    echo "== $what"
    for _ in $(seq 300); do
        [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$B/ctf/")" = 200 ] && break
        kill -0 "$qpid" 2>/dev/null || fail "$what: QEMU stopped"
        sleep 1
    done
    page="$(curl -s --max-time 5 "$B/ctf/")"
    grep -q 'Join the CTF' <<<"$page" || fail "$what: /ctf/ does not answer, or does not ask to join"
    ok "/ctf/ answers and asks to join"
    grep -q 'neo.jmnl.nl/juiceshop-ctf' <<<"$page" || fail "$what: the default central server is not neo's"
    ok "the central server is the image's default"
    # Captured first, then grepped: `curl | grep -q` under pipefail fails
    # when grep stops early and curl gets SIGPIPE on the rest of the page.
    for _ in $(seq 120); do
        page="$(curl -s --max-time 3 "$B/juiceshop/" || true)"
        grep -q '<title>OWASP Juice Shop' <<<"$page" && break
        sleep 1
    done
    grep -q '<title>OWASP Juice Shop' <<<"$page" || fail "$what: /juiceshop/ is not Juice Shop"
    ok "/juiceshop/ is Juice Shop"
    banner="$(timeout 5 bash -c 'exec 3<>/dev/tcp/127.0.0.1/18522; head -c 4 <&3' 2>/dev/null || true)"
    [ "$banner" != "SSH-" ] || fail "$what: an SSH server answers"
    ok "no SSH server"
}

tar -xf "$out/$name-$version-virtualbox.ova" -C "$work" "$name-disk1.vmdk"
boot "$work/$name-disk1.vmdk" vmdk
check "the VirtualBox OVA's disk, on SATA with two e1000"
halt

python3 -c 'import sys, zipfile; zipfile.ZipFile(sys.argv[1]).extractall(sys.argv[2])' "$out/$name-$version-vmware.zip" "$work"
grep -q 'ethernet1.connectionType = "hostonly"' "$work/$name/$name.vmx" || fail "the .vmx has no host-only adapter 2"
boot "$work/$name/$name.vmdk" vmdk
check "the VMware disk, on SATA with two e1000"
halt

echo
echo "ALL PASSED"
