# The CTF VM for VirtualBox

How the operator makes the VM that every student imports next to Kali
(docs/VERSION-1-1.md §3, §4.2; the student's side is [STUDENT.md](STUDENT.md)).

The VM is the Flatcar node **`cybersecurity-1`** of `jmnl-external-servers`,
built like every other node, with two containers on it (decision D9):
`juiceshop-ctf` (Juice Shop + ctf-node, publishing port 80) and
`logging-proxy`. Nothing in it identifies a student or holds a secret.

## Making it

1. Build the USB/ISO image in `jmnl-external-servers/flatcar-deployment`:
   `./create-flatcar-files.sh cybersecurity.conf` (its README has the details).
2. In VirtualBox, a new VM: Linux / Other 64-bit, 2 CPUs, 3 GB RAM, a 20 GB
   disk on **SATA** (`nodes/cybersecurity-1/node.conf` installs to `/dev/sda`).
3. **Adapters** — nothing to configure inside the VM, both get their address
   from VirtualBox's DHCP:

   | Adapter | Attached to | Why |
   |---------|-------------|-----|
   | 1 | NAT | the way out, to `neo.jmnl.nl` |
   | 2 | Host-only Adapter (`vboxnet0`) | the lab, shared with Kali |

   **Not bridged**: the shop would be on the classroom network.
4. Boot the install image, let it install and reboot from the disk. Wait
   until `http://<host-only address>/ctf/` answers; find the address from the
   host with `nmap -p 80 <host-only network>` (e.g. `192.168.56.0/24`).

## Checking it before export

From the host, on the host-only network:

1. `/ctf/` shows **Join the CTF** — no name, no game.
2. Make a test game in admin, join it from the VM's page, solve
   `/juiceshop/ftp/acquisitions.md`, see it on the test board.
3. Press **Forget** on the VM's page (type `forget`). The page shows
   **Join the CTF** again; the test participant is *left* in admin.
4. Delete the test game in admin.

**Forget must be the last thing you do on the VM.** A VM that is exported
while joined carries that participant's token into every copy; the central
server flags it (`two_boots`), but every copy then reports as one student.

## Exporting

Shut the VM down (from VirtualBox: *ACPI shutdown*), then *File → Export
Appliance…* → OVF 2.0, MAC address policy **Strip all network adapter MAC
addresses**, → `juiceshop-ctf.ova`. Every import then gets MAC addresses of
its own, and so its own DHCP addresses.

Import it twice yourself and join each with a different name: they must
appear as two participants, and neither may show the test join.

## A new version

There is no update path into thirty VirtualBoxes: a new version is a new
OVA. Admin shows the version each VM reports.
