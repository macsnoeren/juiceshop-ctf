# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Maurice Snoeren

# The CTF VM: Alpine Linux with Docker and the juiceshop-ctf image, installed
# unattended under QEMU. Run through deploy/vm/build.sh, never by hand: it
# makes the image tar, the build's SSH key and the HTTP directory this file
# points at, and then packages the disk for VirtualBox and VMware
# (package.sh). See deploy/vm/README.md.
#
#   QEMU (in the builder container)          the Alpine ISO, booted once
#     boot_command types three lines   ──►   root login, DHCP, fetch install.sh
#     packer's HTTP server             ──►   install.sh: setup-alpine to disk, reboot
#     SSH as root, with this build's key ──► provision.sh: docker, the image, the service
#     shutdown_command                 ──►   lockdown.sh: no SSH, root locked, poweroff
#
# WHAT IS UNUSUAL HERE:
#
#   - The disk is virtio while building, but the VM runs on SATA (VirtualBox,
#     VMware): fstab and the boot loader use UUIDs, and provision.sh puts the
#     SATA and NVMe drivers into the initramfs. deploy/vm/test.sh boots the
#     EXPORTED disk on SATA with e1000 adapters to prove it.
#   - The SSH key exists for this one build only (build.sh makes it, and
#     lockdown.sh removes the key and the SSH server). The finished VM has
#     no SSH and no password: nothing to log in to, as the Flatcar node had.

packer {
  required_version = "= 1.9.5"
  required_plugins {
    qemu = {
      source  = "github.com/hashicorp/qemu"
      version = "= 1.1.0"
    }
  }
}

variable "version" {
  type        = string
  description = "the CTF's version (git describe), shown on the console and in the file names"
}

variable "alpine_version" {
  type    = string
  default = "3.24.4"
}

variable "accelerator" {
  type        = string
  default     = "kvm"
  description = "kvm, or tcg where there is no /dev/kvm (ten times slower)"
}

variable "ssh_key" {
  type        = string
  description = "the private half of this build's key; the public half is served as root.pub"
}

variable "http_dir" {
  type = string
}

variable "image_tar" {
  type        = string
  description = "docker save of juiceshop-ctf:<version>, gzipped"
}

variable "output_dir" {
  type = string
}

variable "monitor" {
  type        = string
  default     = ""
  description = "a unix socket for QEMU's monitor, to look at the screen while it builds (README: when the build hangs)"
}

locals {
  alpine_branch = join(".", slice(split(".", var.alpine_version), 0, 2))
  alpine_iso    = "https://dl-cdn.alpinelinux.org/alpine/v${local.alpine_branch}/releases/x86_64/alpine-standard-${var.alpine_version}-x86_64.iso"
}

source "qemu" "juiceshop-ctf" {
  # The "standard" ISO, not "virt": it installs linux-lts, which has the
  # drivers of VirtualBox's and VMware's hardware (linux-virt is cut down
  # for KVM and Xen).
  iso_url      = local.alpine_iso
  iso_checksum = "file:${local.alpine_iso}.sha256"

  output_directory = var.output_dir
  vm_name          = "juiceshop-ctf.qcow2"
  format           = "qcow2"
  disk_size        = "8G"
  disk_interface   = "virtio"
  # So that lockdown.sh's fstrim gives freed blocks back, and the exported
  # disk holds only what is used.
  disk_discard       = "unmap"
  disk_detect_zeroes = "unmap"
  net_device         = "virtio-net"
  memory             = 2048
  cpus               = 2
  accelerator        = var.accelerator
  headless           = true
  qemuargs           = var.monitor == "" ? [] : [["-monitor", "unix:${var.monitor},server,nowait"]]

  http_directory = var.http_dir
  # The live ISO boots to a login prompt in under 30 s with KVM; tcg needs
  # longer, and build.sh raises it then.
  boot_wait = var.accelerator == "kvm" ? "40s" : "180s"
  boot_command = [
    "root<enter><wait5>",
    "ifconfig eth0 up && udhcpc -i eth0 -q<enter><wait10>",
    "wget -qO /tmp/install.sh http://{{ .HTTPIP }}:{{ .HTTPPort }}/install.sh && sh /tmp/install.sh http://{{ .HTTPIP }}:{{ .HTTPPort }}<enter>",
  ]

  communicator         = "ssh"
  ssh_username         = "root"
  ssh_private_key_file = var.ssh_key
  ssh_timeout          = "30m"
  shutdown_command     = "sh /root/lockdown.sh"
  shutdown_timeout     = "5m"
}

build {
  sources = ["source.qemu.juiceshop-ctf"]

  provisioner "file" {
    source      = var.image_tar
    destination = "/tmp/juiceshop-ctf-image.tar.gz"
  }
  provisioner "file" {
    source      = "deploy/vm/files/juiceshop-ctf.initd"
    destination = "/tmp/juiceshop-ctf.initd"
  }
  provisioner "file" {
    source      = "deploy/vm/files/lockdown.sh"
    destination = "/root/lockdown.sh"
  }
  provisioner "shell" {
    script           = "deploy/vm/provision.sh"
    environment_vars = ["VERSION=${var.version}"]
  }
}
