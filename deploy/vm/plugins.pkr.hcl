# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Maurice Snoeren

# The Packer version and plugin the VM build needs. Kept in a file of its own
# so Dockerfile.builder can install the plugin into the builder image; the
# same block is in juiceshop-ctf.pkr.hcl and the two must say the same.
packer {
  required_version = "= 1.9.5"
  required_plugins {
    qemu = {
      source  = "github.com/hashicorp/qemu"
      version = "= 1.1.0"
    }
  }
}
