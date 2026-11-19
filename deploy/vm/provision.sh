#!/bin/sh
# provision.sh: runs over SSH as root in the freshly installed Alpine
# (juiceshop-ctf.pkr.hcl), and turns it into the CTF VM: Docker, the
# juiceshop-ctf image, the service that runs it, and the network and drivers
# of VirtualBox's and VMware's hardware. lockdown.sh finishes it at shutdown.
#
# What the VM is in the end (docs/VERSION-1-1.md §3; deploy/vm/README.md):
#
#   adapter 1 (eth0)  NAT        DHCP   the way out, to the central server
#   adapter 2 (eth1)  host-only  DHCP   the lab: Kali reaches :80 here
#   :80  the juiceshop-ctf container: /ctf/ and /juiceshop/
#   no SSH, no password, nothing of a student until they join
set -eux

# ---- packages -----------------------------------------------------------------
# Docker is in "community"; setup-alpine -c should have enabled it, but an
# older alpine-conf does not know -c, so make sure.
if ! grep -q '^[^#].*/community$' /etc/apk/repositories; then
    main="$(grep -m1 '^[^#].*/main$' /etc/apk/repositories)"
    echo "${main%/main}/community" >>/etc/apk/repositories
fi
apk update
apk upgrade --available
apk add docker
rc-update add docker default

# ---- drivers ------------------------------------------------------------------
# The build ran on virtio; VirtualBox and VMware give the disk as SATA (or
# NVMe in newer VMware defaults). Without these features in the initramfs
# the VM stops at "mounting root" on its first boot outside QEMU.
conf=/etc/mkinitfs/mkinitfs.conf
for f in ata scsi nvme virtio ext4; do
    grep -Eq "^features=\"([^\"]* )?$f( |\")" "$conf" || sed -i "s/^features=\"/features=\"$f /" "$conf"
done
for k in /lib/modules/*; do mkinitfs "$(basename "$k")"; done

# ---- network --------------------------------------------------------------------
# Both adapters by DHCP; nothing to configure in the VM. eth1 is absent
# while building (one adapter), which is why setup-alpine only got eth0.
cat >/etc/network/interfaces <<'EOF'
# The CTF VM's two adapters (projects/juiceshop-ctf deploy/vm/README.md).
# Both by DHCP from the hypervisor: adapter 1 is NAT (the way to the
# central server), adapter 2 host-only (the lab network Kali is on).
auto lo
iface lo inet loopback

auto eth0
iface eth0 inet dhcp

auto eth1
iface eth1 inet dhcp
EOF

# ---- the CTF ----------------------------------------------------------------------
rc-service docker start
i=0
until docker info >/dev/null 2>&1; do
    i=$((i + 1)); [ "$i" -lt 60 ] || { echo "docker did not start"; exit 1; }
    sleep 1
done
gunzip -c /tmp/juiceshop-ctf-image.tar.gz | docker load
# The service runs a fixed tag, so it never depends on the version string.
docker tag "juiceshop-ctf:${VERSION}" juiceshop-ctf:vm
docker rmi "juiceshop-ctf:${VERSION}"
rm /tmp/juiceshop-ctf-image.tar.gz

install -m 0755 /tmp/juiceshop-ctf.initd /etc/init.d/juiceshop-ctf
rm /tmp/juiceshop-ctf.initd
rc-update add juiceshop-ctf default
# NOT started here: the container, its volume and node.db are made at the
# VM's first boot, on the student's machine. A VM built this way has never
# joined anything, so no copy can carry someone's membership.
rc-service docker stop

# ---- the console ----------------------------------------------------------------------
# No address on the console on purpose: finding the VM from Kali is the
# students' first task (deploy/vm/STUDENT.md).
cat >/etc/issue <<EOF
Juice Shop CTF ${VERSION}

This VM has no login. Open http://<its address>/ctf/ from Kali.

EOF
cp /etc/issue /etc/motd
