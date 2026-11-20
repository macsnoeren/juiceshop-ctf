#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Maurice Snoeren

# package.sh: the one disk Packer made (QEMU, qcow2) as the two things a
# student imports. Runs in the builder container, from deploy/vm/build.sh.
#
#   package.sh QCOW2 OUTDIR VERSION
#
#   OUTDIR/juiceshop-ctf-VERSION-virtualbox.ova   File → Import Appliance
#   OUTDIR/juiceshop-ctf-VERSION-vmware.zip       unzip, File → Open the .vmx
#
# Both describe the same machine (deploy/vm/README.md): 2 CPUs, 2 GB, the disk
# on SATA, two Intel e1000 adapters — 1 on NAT, 2 on host-only — and BIOS.
#
# WHY TWO FORMATS AND NOT ONE OVA: an OVF can only name its networks, and
# each hypervisor maps those names its own way. VirtualBox maps the
# connection names "NAT" and "HostOnly" onto exactly those attachments
# (so the OVA below uses them); VMware's importer does not, and would put
# both adapters on one network. A .vmx says connectionType outright, so the
# VMware VM is a .vmx with its disk, which Workstation, Player and Fusion
# open as is.
set -euo pipefail

qcow="$1" out="$(cd "$2" && pwd)" version="$3"
name="juiceshop-ctf"
work="$(mktemp -d "$out/.package.XXXXXX")"
trap 'rm -rf "$work"' EXIT

capacity="$(qemu-img info --output=json "$qcow" | python3 -c 'import json,sys; print(json.load(sys.stdin)["virtual-size"])')"

# ---- VirtualBox: an OVA ---------------------------------------------------------
# OVF 1.0 with VirtualBox's system type; the disk streamOptimized, as an
# OVA requires. The rasd elements of each Item are in alphabetical order,
# as the CIM schema wants and VirtualBox's own exports have them.
vbox="$work/vbox"
mkdir -p "$vbox"
qemu-img convert -O vmdk -o subformat=streamOptimized "$qcow" "$vbox/$name-disk1.vmdk"

cat >"$vbox/$name.ovf" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<Envelope ovf:version="1.0" xml:lang="en-US" xmlns="http://schemas.dmtf.org/ovf/envelope/1" xmlns:ovf="http://schemas.dmtf.org/ovf/envelope/1" xmlns:rasd="http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_ResourceAllocationSettingData" xmlns:vssd="http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_VirtualSystemSettingData" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:vbox="http://www.virtualbox.org/ovf/machine">
  <References>
    <File ovf:id="file1" ovf:href="$name-disk1.vmdk"/>
  </References>
  <DiskSection>
    <Info>List of the virtual disks used in the package</Info>
    <Disk ovf:capacity="$capacity" ovf:diskId="vmdisk1" ovf:fileRef="file1" ovf:format="http://www.vmware.com/interfaces/specifications/vmdk.html#streamOptimized"/>
  </DiskSection>
  <NetworkSection>
    <Info>Logical networks used in the package</Info>
    <Network ovf:name="NAT">
      <Description>Adapter 1: the way out, to the central server</Description>
    </Network>
    <Network ovf:name="HostOnly">
      <Description>Adapter 2: the lab network, shared with Kali</Description>
    </Network>
  </NetworkSection>
  <VirtualSystem ovf:id="$name">
    <Info>A virtual machine</Info>
    <ProductSection>
      <Info>Meta-information about the installed software</Info>
      <Product>Juice Shop CTF</Product>
      <Vendor>juiceshop-ctf</Vendor>
      <Version>$version</Version>
      <ProductUrl>https://github.com/macsnoeren/juiceshop-ctf</ProductUrl>
    </ProductSection>
    <AnnotationSection>
      <Info>A human-readable annotation</Info>
      <Annotation>Juice Shop CTF $version: OWASP Juice Shop and the CTF on port 80. Adapter 1 NAT, adapter 2 the host-only network of Kali. Open http://&lt;address&gt;/ctf/ from Kali.</Annotation>
    </AnnotationSection>
    <OperatingSystemSection ovf:id="101">
      <Info>The kind of installed guest operating system</Info>
      <Description>Linux26_64</Description>
      <vbox:OSType ovf:required="false">Linux26_64</vbox:OSType>
    </OperatingSystemSection>
    <VirtualHardwareSection>
      <Info>Virtual hardware requirements for a virtual machine</Info>
      <System>
        <vssd:ElementName>Virtual Hardware Family</vssd:ElementName>
        <vssd:InstanceID>0</vssd:InstanceID>
        <vssd:VirtualSystemIdentifier>$name</vssd:VirtualSystemIdentifier>
        <vssd:VirtualSystemType>virtualbox-2.2</vssd:VirtualSystemType>
      </System>
      <Item>
        <rasd:Caption>2 virtual CPU</rasd:Caption>
        <rasd:Description>Number of virtual CPUs</rasd:Description>
        <rasd:ElementName>2 virtual CPU</rasd:ElementName>
        <rasd:InstanceID>1</rasd:InstanceID>
        <rasd:ResourceType>3</rasd:ResourceType>
        <rasd:VirtualQuantity>2</rasd:VirtualQuantity>
      </Item>
      <Item>
        <rasd:AllocationUnits>MegaBytes</rasd:AllocationUnits>
        <rasd:Caption>2048 MB of memory</rasd:Caption>
        <rasd:Description>Memory Size</rasd:Description>
        <rasd:ElementName>2048 MB of memory</rasd:ElementName>
        <rasd:InstanceID>2</rasd:InstanceID>
        <rasd:ResourceType>4</rasd:ResourceType>
        <rasd:VirtualQuantity>2048</rasd:VirtualQuantity>
      </Item>
      <Item>
        <rasd:Address>0</rasd:Address>
        <rasd:Caption>sataController0</rasd:Caption>
        <rasd:Description>SATA Controller</rasd:Description>
        <rasd:ElementName>sataController0</rasd:ElementName>
        <rasd:InstanceID>3</rasd:InstanceID>
        <rasd:ResourceSubType>AHCI</rasd:ResourceSubType>
        <rasd:ResourceType>20</rasd:ResourceType>
      </Item>
      <Item>
        <rasd:AutomaticAllocation>true</rasd:AutomaticAllocation>
        <rasd:Caption>Ethernet adapter on 'NAT'</rasd:Caption>
        <rasd:Connection>NAT</rasd:Connection>
        <rasd:ElementName>Ethernet adapter on 'NAT'</rasd:ElementName>
        <rasd:InstanceID>4</rasd:InstanceID>
        <rasd:ResourceSubType>E1000</rasd:ResourceSubType>
        <rasd:ResourceType>10</rasd:ResourceType>
      </Item>
      <Item>
        <rasd:AutomaticAllocation>true</rasd:AutomaticAllocation>
        <rasd:Caption>Ethernet adapter on 'HostOnly'</rasd:Caption>
        <rasd:Connection>HostOnly</rasd:Connection>
        <rasd:ElementName>Ethernet adapter on 'HostOnly'</rasd:ElementName>
        <rasd:InstanceID>5</rasd:InstanceID>
        <rasd:ResourceSubType>E1000</rasd:ResourceSubType>
        <rasd:ResourceType>10</rasd:ResourceType>
      </Item>
      <Item>
        <rasd:AddressOnParent>0</rasd:AddressOnParent>
        <rasd:Caption>disk1</rasd:Caption>
        <rasd:Description>Disk Image</rasd:Description>
        <rasd:ElementName>disk1</rasd:ElementName>
        <rasd:HostResource>/disk/vmdisk1</rasd:HostResource>
        <rasd:InstanceID>6</rasd:InstanceID>
        <rasd:Parent>3</rasd:Parent>
        <rasd:ResourceType>17</rasd:ResourceType>
      </Item>
    </VirtualHardwareSection>
  </VirtualSystem>
</Envelope>
EOF

# The manifest, and the OVA: a plain tar with the descriptor FIRST (the OVF
# standard requires it; importers stream the file and read it before the disk).
(cd "$vbox" && for f in "$name.ovf" "$name-disk1.vmdk"; do
    printf 'SHA256(%s)= %s\n' "$f" "$(sha256sum "$f" | cut -d' ' -f1)"
done >"$name.mf")
ova="$out/$name-$version-virtualbox.ova"
tar --format=ustar --owner=0 --group=0 -cf "$ova" -C "$vbox" "$name.ovf" "$name-disk1.vmdk" "$name.mf"

# ---- VMware: a .vmx and its disk -------------------------------------------------
# monolithicSparse: one growable file Workstation, Player and Fusion use as
# is (streamOptimized is only for OVF). ddb.adapterType ide is what VMware
# writes for SATA disks too.
vmw="$work/$name"
mkdir -p "$vmw"
qemu-img convert -O vmdk -o subformat=monolithicSparse,adapter_type=ide "$qcow" "$vmw/$name.vmdk"
cat >"$vmw/$name.vmx" <<EOF
.encoding = "UTF-8"
config.version = "8"
virtualHW.version = "14"
displayName = "Juice Shop CTF $version"
annotation = "Juice Shop CTF $version|0AAdapter 1 NAT, adapter 2 host-only (the network of Kali).|0AOpen http://<address>/ctf/ from Kali."
guestOS = "otherlinux-64"
firmware = "bios"
memsize = "2048"
numvcpus = "2"
pciBridge0.present = "TRUE"
pciBridge4.present = "TRUE"
pciBridge4.virtualDev = "pcieRootPort"
pciBridge4.functions = "8"
pciBridge5.present = "TRUE"
pciBridge5.virtualDev = "pcieRootPort"
pciBridge5.functions = "8"
pciBridge6.present = "TRUE"
pciBridge6.virtualDev = "pcieRootPort"
pciBridge6.functions = "8"
pciBridge7.present = "TRUE"
pciBridge7.virtualDev = "pcieRootPort"
pciBridge7.functions = "8"
sata0.present = "TRUE"
sata0:0.present = "TRUE"
sata0:0.deviceType = "disk"
sata0:0.fileName = "$name.vmdk"
ethernet0.present = "TRUE"
ethernet0.virtualDev = "e1000"
ethernet0.connectionType = "nat"
ethernet0.addressType = "generated"
ethernet0.startConnected = "TRUE"
ethernet1.present = "TRUE"
ethernet1.virtualDev = "e1000"
ethernet1.connectionType = "hostonly"
ethernet1.addressType = "generated"
ethernet1.startConnected = "TRUE"
usb.present = "FALSE"
sound.present = "FALSE"
floppy0.present = "FALSE"
tools.syncTime = "FALSE"
uuid.action = "create"
EOF
# uuid.action = "create": every copy answers VMware's "moved or copied?" with
# "copied", so it gets MAC addresses of its own, and so its own DHCP address.
zip="$out/$name-$version-vmware.zip"
rm -f "$zip"
(cd "$work" && python3 - "$zip" "$name" <<'PY'
import sys, zipfile, os
z, name = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(z, "w", zipfile.ZIP_DEFLATED, allowZip64=True) as f:
    for n in sorted(os.listdir(name)):
        f.write(os.path.join(name, n), os.path.join(name, n))
PY
)

(cd "$out" && sha256sum "$(basename "$ova")" "$(basename "$zip")" >"$name-$version.sha256")
ls -l "$ova" "$zip"
