# The CTF VM

How the VM that every student imports next to Kali is made
(docs/VERSION-1-1.md §3, §4; the student's side is [STUDENT.md](STUDENT.md)).
It was made for the Minor Cybersecurity *Weerbaar tegen cybercrime* of Avans
University of Applied Sciences, where every student already has a
VirtualBox lab with Kali on a host-only network.

```
 the student's laptop (VirtualBox or VMware)
 Kali ── host-only ──► CTF VM: Alpine Linux, Docker, juiceshop-ctf on :80 ── NAT ──► central server
                        /ctf/        the student's page: join, progress, Restart, Forget
                        /juiceshop/  their own Juice Shop
```

A release (a `vX.Y.Z` tag, [.github/workflows/release.yml](../../.github/workflows/release.yml))
builds it on GitHub and attaches it to the
[release](https://github.com/macsnoeren/juiceshop-ctf/releases); `make vm`
builds the same files locally. One build gives both:

| File | For | The student |
|------|-----|-------------|
| `dist/vm/juiceshop-ctf-<version>-virtualbox.ova` | VirtualBox 6.1 and 7 | *File → Import Appliance…* |
| `dist/vm/juiceshop-ctf-<version>-vmware.zip` | VMware Workstation / Player 14+, Fusion (Intel) | unzip, *File → Open…* the `.vmx`, answer *I Copied It* |
| `dist/vm/juiceshop-ctf-<version>.sha256` | checking a download | |

The VM: Alpine Linux 3.24, 2 CPUs, 2 GB memory, an 8 GB growable disk on
SATA (about 1 GB used), BIOS, two Intel e1000 adapters, **no login** (no SSH,
root locked) and nothing of any student in it. It has never joined a game:
the `juiceshop-ctf` container and its `node.db` are made at the first boot
on the student's machine.

| Adapter | Attached to | Inside the VM | Why |
|---------|-------------|---------------|-----|
| 1 | NAT | `eth0`, DHCP | the way out, to the central server |
| 2 | host-only (VirtualBox `vboxnet0`, VMware `VMnet1`) | `eth1`, DHCP | the lab: Kali reaches port 80 here |

**Not bridged**: the shop would be on the classroom network.

## Building

Needs Docker only. With `/dev/kvm` (Linux, a user in the `kvm` group) it
takes about six minutes (three of them Packer); without it QEMU emulates the CPU and it takes an
hour or more.

```sh
make vm                                # = deploy/vm/build.sh
VERSION=v1.1.0 make vm                 # the version in the file names and on the console (default: git describe)
ALPINE_VERSION=3.24.5 make vm          # another Alpine release
VM_SKIP_TEST=1 make vm                 # without the boot test
```

What it does (the comments in each file say why):

| Step | Where | |
|------|-------|-|
| the image | host | `juiceshop-ctf:<version>` from `deploy/docker/Dockerfile.node`, saved as a tar |
| the builder | host | [`Dockerfile.builder`](Dockerfile.builder): Packer **1.9.5** and QEMU |
| install | builder | [`juiceshop-ctf.pkr.hcl`](juiceshop-ctf.pkr.hcl): boots the Alpine ISO under QEMU, types three lines, and [`http/install.sh`](http/install.sh) installs Alpine unattended |
| provision | builder → VM | [`provision.sh`](provision.sh): Docker, the image, the [service](files/juiceshop-ctf.initd), both adapters, the SATA/NVMe drivers |
| lock down | builder → VM | [`files/lockdown.sh`](files/lockdown.sh): SSH removed, root locked, caches gone, free space trimmed |
| package | builder | [`package.sh`](package.sh): the disk as an OVA (VirtualBox) and as a `.vmx` + `.vmdk` (VMware) |
| test | builder | [`test.sh`](test.sh): both exported disks booted on SATA with two e1000, `/ctf/` and `/juiceshop/` checked, no SSH |

**Why not Packer's `virtualbox-iso` and `vmware-iso`?** Those builders drive
a VirtualBox or VMware installed on the machine that runs Packer, and both
need kernel modules of their own: they cannot run in a container. So the VM
is installed once under QEMU, and the one disk is packaged for both.

**Why Packer 1.9.5?** It is the last release under an open-source licence
(MPL 2.0); from 1.10 on Packer is under the Business Source License.

## Checking it before handing it out

`test.sh` proves the disks boot and the CTF answers, but not the import into
VirtualBox and VMware themselves — neither runs in a container. Once per new
version, on a machine with VirtualBox (and VMware, if students use it):

1. Import the OVA. *Settings → Network*: adapter 1 **NAT**, adapter 2
   **Host-only Adapter**. Start it; the console shows *Juice Shop CTF
   <version>* and no login.
2. From Kali (or the host) on the host-only network:
   `http://<address>/ctf/` shows **Join the CTF**; find the address with
   `nmap -p 80 192.168.56.0/24`.
3. Join a test game, solve `/juiceshop/ftp/acquisitions.md`, see it on the
   board. Then **Forget** on the VM's page and delete the test game.
4. Import it a second time: a new VM, with its own MAC addresses and
   address, that has never joined anything.

Unlike the old way (one VM, exported by hand), there is no "Forget before
exporting": the files are built, and a built VM has never been used.

## When the build hangs

Packer waits for SSH for at most 30 minutes, and the install runs on a
screen nobody sees. While it runs, QEMU's monitor is at
`dist/vm/work/monitor.sock`, so a screenshot shows where it is:

```sh
python3 - dist/vm/work/monitor.sock <<'EOF'
import socket, sys, time
s = socket.socket(socket.AF_UNIX); s.connect(sys.argv[1]); time.sleep(.3); s.recv(4096)
s.sendall(b"screendump /src/dist/vm/work/screen.ppm\n"); time.sleep(1)
EOF
# dist/vm/work/screen.ppm, an image any viewer opens
```

The usual causes: a new Alpine release whose `setup-alpine` asks something
the answer file does not answer (`http/install.sh`), or a slow mirror.

## A new version

There is no update path into thirty VirtualBoxes: a new version is a new
VM, imported again. Admin shows the version each VM reports. Students who
are mid-game keep their VM; their progress is in it.
