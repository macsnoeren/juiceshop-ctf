#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Maurice Snoeren

# lockdown.sh: the last thing that runs in the VM while it is built. Packer
# runs it as its shutdown_command (juiceshop-ctf.pkr.hcl), over the SSH
# session it is about to take away, and waits for the VM to power off.
#
#   - the SSH server goes, with this build's key: the VM has no remote login
#   - root's password is locked: no console login either (setup-alpine -e
#     left it empty)
#   - caches and logs of the build go, and the free blocks are given back,
#     so the exported disk is small and holds nothing of the build
#
# The running session survives removing the SSH server: it is its own
# process. The poweroff at the end is what Packer waits for.
set -ux

rc-update del sshd default
apk del openssh openssh-server openssh-client >/dev/null 2>&1 || apk del openssh
rm -rf /root/.ssh /etc/ssh

passwd -l root

rm -rf /var/cache/apk/* /tmp/* /var/tmp/*
find /var/log -type f -exec truncate -s 0 {} +
rm -f /root/.ash_history /root/lockdown.sh

sync
fstrim -v / || true   # busybox fstrim: one mount point at a time
fstrim -v /boot || true
poweroff
