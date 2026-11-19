#!/usr/bin/env bash
# build.sh: the CTF VM for VirtualBox and VMware, built in Docker. Nothing
# to install but Docker; /dev/kvm makes it fast (about six minutes), without
# it QEMU emulates (an hour or more). See deploy/vm/README.md.
#
#   deploy/vm/build.sh            (or: make vm)
#
#   dist/vm/juiceshop-ctf-<version>-virtualbox.ova
#   dist/vm/juiceshop-ctf-<version>-vmware.zip
#   dist/vm/juiceshop-ctf-<version>.sha256
#
# On the host it builds two images and starts the builder:
#
#   juiceshop-ctf:<version>      deploy/docker/Dockerfile.node, the image the VM runs
#   juiceshop-ctf-vm-builder     deploy/vm/Dockerfile.builder: Packer 1.9.5 and QEMU
#
# and in the builder (this script again, with --inside):
#
#   packer build  juiceshop-ctf.pkr.hcl   Alpine + Docker + the image, under QEMU
#   package.sh                            the disk as an OVA and as a .vmx + .vmdk
#   test.sh                               both disks booted on SATA/e1000, checked
#
# The builder runs as you (the files in dist/ are yours) with /dev/kvm
# passed in. The Alpine ISO is cached in dist/vm/cache between builds.
#
# Environment: VERSION (default: git describe), CENTRAL_URL (the central
# server the VM joins unless the student picks another; default none, see
# deploy/docker/Dockerfile.node), ALPINE_VERSION (default in
# juiceshop-ctf.pkr.hcl), VM_SKIP_TEST=1 to skip test.sh, VM_DEBUG=1 to keep
# a VM whose build failed running for half an hour, to look at its screen.
set -euo pipefail

if [ "${1:-}" = "--inside" ]; then
    # ---- in the builder container, at the repository root --------------------
    # The calling user's uid has no name in the container; ssh-keygen wants one.
    getent passwd "$(id -u)" >/dev/null || echo "builder:x:$(id -u):$(id -g)::/tmp:/bin/sh" >>/etc/passwd
    out=dist/vm
    work="$out/work"
    rm -rf "$work/http" "$out/qemu" "$work/build_key" "$work/build_key.pub"
    mkdir -p "$work/http" "$out/cache"
    export PACKER_CACHE_DIR="$out/cache" HOME=/tmp

    # This build's SSH key: Packer logs in with it, lockdown.sh removes it.
    ssh-keygen -q -t ed25519 -N '' -C "juiceshop-ctf VM build" -f "$work/build_key"
    cp deploy/vm/http/install.sh "$work/http/"
    cp "$work/build_key.pub" "$work/http/root.pub"

    accel=kvm
    [ -w /dev/kvm ] || { accel=tcg; echo ">>> no /dev/kvm: QEMU emulates, this takes an hour or more"; }
    vars=(-var "version=$VERSION" -var "accelerator=$accel" -var "ssh_key=$work/build_key"
          -var "http_dir=$work/http" -var "image_tar=$work/juiceshop-ctf-image.tar.gz" -var "output_dir=$out/qemu"
          -var "monitor=$work/monitor.sock")
    [ -n "${ALPINE_VERSION:-}" ] && vars+=(-var "alpine_version=$ALPINE_VERSION")

    echo "== packer build"
    onerr=cleanup; [ -n "${VM_DEBUG:-}" ] && onerr=abort
    if ! packer build -color=false -on-error="$onerr" "${vars[@]}" deploy/vm/juiceshop-ctf.pkr.hcl; then
        if [ -n "${VM_DEBUG:-}" ]; then
            echo ">>> VM_DEBUG: the failed VM keeps running for 30 minutes; its monitor is $work/monitor.sock (README: when the build hangs)"
            sleep 1800
        fi
        exit 1
    fi
    echo "== package"
    bash deploy/vm/package.sh "$out/qemu/juiceshop-ctf.qcow2" "$out" "$VERSION"
    if [ "${VM_SKIP_TEST:-}" != 1 ]; then
        echo "== test"
        ACCEL="$accel" bash deploy/vm/test.sh "$out" "$VERSION" "${CENTRAL_URL:-}"
    fi
    rm -rf "$out/qemu" "$work"
    echo
    echo "Done:"
    ls -l "$out"/*.ova "$out"/*.zip "$out"/*.sha256
    exit 0
fi

# ---- on the host ------------------------------------------------------------------
cd "$(dirname "$0")/../.."
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
mkdir -p dist/vm/work

CENTRAL_URL="${CENTRAL_URL:-}"
echo "== the CTF image, juiceshop-ctf:$VERSION, central server: ${CENTRAL_URL:-(none)}"
docker build -q -f deploy/docker/Dockerfile.node --build-arg VERSION="$VERSION" --build-arg CENTRAL_URL="$CENTRAL_URL" -t "juiceshop-ctf:$VERSION" . >/dev/null
docker save "juiceshop-ctf:$VERSION" | gzip -1 >dist/vm/work/juiceshop-ctf-image.tar.gz

echo "== the builder, juiceshop-ctf-vm-builder"
docker build -q -t juiceshop-ctf-vm-builder -f deploy/vm/Dockerfile.builder deploy/vm >/dev/null

kvm=()
if [ -e /dev/kvm ]; then
    kvm=(--device /dev/kvm --group-add "$(stat -c %g /dev/kvm)")
fi
tty=(); [ -t 1 ] && tty=(-t)
exec docker run --rm -i "${tty[@]}" "${kvm[@]}" \
    --user "$(id -u):$(id -g)" \
    -e VERSION="$VERSION" -e CENTRAL_URL="$CENTRAL_URL" -e ALPINE_VERSION="${ALPINE_VERSION:-}" -e VM_SKIP_TEST="${VM_SKIP_TEST:-}" -e VM_DEBUG="${VM_DEBUG:-}" \
    -v "$PWD:/src" -w /src \
    juiceshop-ctf-vm-builder bash deploy/vm/build.sh --inside
