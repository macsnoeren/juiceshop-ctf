# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Maurice Snoeren

# The VM builder: Packer and QEMU in a container, so building the CTF VM
# needs nothing on the developer's machine but Docker (and /dev/kvm to be
# fast). deploy/vm/build.sh builds and runs it; see deploy/vm/README.md.
#
#   docker build -t juiceshop-ctf-vm-builder -f deploy/vm/Dockerfile.builder deploy/vm
#
# WHAT IS UNUSUAL HERE:
#
#   - PACKER 1.9.5 ON PURPOSE. It is the last release under the Mozilla
#     Public License 2.0; from 1.10 on Packer is under the Business Source
#     License. 1.9.5 does all this build needs. Moving to a newer one is a
#     licence decision, not a version bump.
#   - QEMU, NOT VirtualBox or VMware. Packer's virtualbox-iso and vmware-iso
#     builders drive a hypervisor installed on the machine itself, and both
#     need kernel modules of their own: they cannot run in a container. So
#     the VM is installed under QEMU (with KVM), and deploy/vm/package.sh
#     turns the one disk into a VirtualBox OVA and a VMware VM.
#   - The QEMU plugin is installed into the image (PACKER_PLUGIN_PATH), so a
#     build downloads only the Alpine ISO, and runs as the calling user.

FROM debian:trixie-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      qemu-system-x86 qemu-utils \
      ca-certificates curl unzip openssh-client python3 \
 && rm -rf /var/lib/apt/lists/*

ARG PACKER_VERSION=1.9.5
ARG PACKER_SHA256=6f8272658a6d606583c2b3deaad272233db6e84a6ee651bf17a0d46d8cea4a9c
RUN curl -fsSLo /tmp/packer.zip "https://releases.hashicorp.com/packer/${PACKER_VERSION}/packer_${PACKER_VERSION}_linux_amd64.zip" \
 && echo "${PACKER_SHA256}  /tmp/packer.zip" | sha256sum -c - \
 && unzip -q /tmp/packer.zip -d /usr/local/bin \
 && rm /tmp/packer.zip

ENV PACKER_PLUGIN_PATH=/opt/packer/plugins \
    PACKER_CHECKPOINT_DISABLE=1
# The plugin block of juiceshop-ctf.pkr.hcl, on its own: `packer init` reads
# only that, and this way changing the build does not reinstall the plugin.
COPY plugins.pkr.hcl /opt/packer/plugins.pkr.hcl
RUN mkdir -p /opt/packer/plugins \
 && packer init /opt/packer/plugins.pkr.hcl \
 && chmod -R a+rX /opt/packer \
 && chmod 0666 /etc/passwd
# /etc/passwd writable: build.sh runs this as the calling user (so dist/ is
# theirs) and adds a line for that uid, which ssh-keygen insists on.
