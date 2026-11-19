#!/bin/sh
# install.sh: runs on the Alpine live ISO, typed in by Packer's boot_command
# (juiceshop-ctf.pkr.hcl), and installs Alpine to the VM's disk without a
# question. $1 is Packer's HTTP server, where this build's SSH key is.
#
# setup-alpine writes its own answer file first (-c), so the format always
# matches the ISO's alpine-conf; the lines appended below override it,
# because the file is read as shell. ERASE_DISKS answers "erase /dev/vda?".
# -e leaves root without a password for now: Packer logs in with the key,
# and lockdown.sh locks root at the end of the build.
#
# >>> NO SSH UNTIL THE DISK IS INSTALLED. With SSHDOPTS=openssh setup-alpine
# starts sshd on the LIVE system, early, and Packer then logs in there and
# provisions the live system while the disk is still being written — until
# the reboot below cuts it off. So the answer file says none, and the SSH
# server and this build's key go onto the installed disk afterwards.
set -eu
base="$1"

wget -qO /tmp/root.pub "$base/root.pub"

setup-alpine -c /tmp/answers
cat >>/tmp/answers <<EOF
KEYMAPOPTS=none
HOSTNAMEOPTS=juiceshop-ctf
INTERFACESOPTS="auto lo
iface lo inet loopback

auto eth0
iface eth0 inet dhcp
"
TIMEZONEOPTS=UTC
PROXYOPTS=none
APKREPOSOPTS="-1 -c"
USEROPTS=none
SSHDOPTS=none
NTPOPTS=chrony
DISKOPTS="-m sys /dev/vda"
LBUOPTS=none
APKCACHEOPTS=none
EOF

export ERASE_DISKS=/dev/vda SWAP_SIZE=0
setup-alpine -e -f /tmp/answers

# The installed system: its root is the last partition of the disk.
root="$(ls /dev/vda[0-9]* | sort | tail -1)"
mount "$root" /mnt
apk add --root /mnt --repositories-file /mnt/etc/apk/repositories --keys-dir /mnt/etc/apk/keys openssh
ln -sf /etc/init.d/sshd /mnt/etc/runlevels/default/sshd
install -d -m 0700 /mnt/root/.ssh
install -m 0600 /tmp/root.pub /mnt/root/.ssh/authorized_keys
umount /mnt
echo "install.sh: installed, rebooting into the disk"
reboot
