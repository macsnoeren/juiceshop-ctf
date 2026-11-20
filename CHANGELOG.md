# Changelog

## Unreleased

**Repository**
- Licensed under the GNU General Public License v3.0 or later (`LICENSE`):
  every source file says so in an SPDX header, both images carry the licence
  text and the OCI licence label, and the release notes link the source.
- `internal/shared/*` moved up to `internal/*`; the test scripts are all in
  `scripts/`; the students' sheet is `docs/STUDENT.md`.
- The planning documents (`DESIGN.md`, `VERSION-1-1.md`, `TASK-1-1.md`) are
  gone, and so are the references to them in the code.
- `CLAUDE.md` describes the layout and the rules that keep it.

## 1.1.0

The answer to the first student lab.

**The central server leads**
- Games, made in admin, each with a join code (shown once, stored hashed), a
  state (draft → open → running → closed), an optional time window, its own
  scoring and board options (hidden, initials). Any number at the same time.
- A token per participant, issued when the student joins. A report touches
  only its own participant: thirty VMs from one image no longer push each
  other off the board.
- Admin: the game page with every participant's state, version, cheat score
  and flags; remove, hide, end, clear and delete, confirmed by typing the
  game's name; the event feed with filters and CSV.
- Flags for what an honest VM never sends: a missing prerequisite, a burst,
  solves before the join or after the end, two Juice Shop boots on one
  token, a high cheat score. Shown, never acted on.
- Everything in SQLite; `CTF_SEED`, `CTF_TITLE`, `CTF_TIMEZONE` gone. The
  1.0 database migrates: every student of the lab is back, in a closed game "1.0".
- The challenge catalogue is embedded: a report says which challenges, never
  what they are worth.
- Protocol v2 (`/api/v2/join`, `/report`, `/leave`); a 1.0 agent gets 410 and an event.

**One container per VM**
- `juiceshop-ctf`: Juice Shop 20.3.0 and ctf-node in one image, with a keeper
  as PID 1 that alone holds SETUID/SETGID; ctf-node runs as 10001 with no
  capabilities, Juice Shop as 65532, and code in Juice Shop cannot read the
  token or signal ctf-node.
- ctf-node replaces ctf-agent and ctf-status: no gate (the lab network keeps
  others out, nothing depends on a cookie); `CTF_GATE=session` with a
  personal return code where a VM is reachable by others.
- Join online, queued while the central server is unreachable, or with no
  central server at all; the central server can be changed on the page
  before joining, and a VM built without one asks *Which central
  server?* before the join form. Everything remembered in `node.db`.
- *Restart Juice Shop* and *Forget* on the student's page; the operator's
  *remove* does what *Forget* does.

**Admin accounts and the look of Juice Shop**
- Admin has its own login: accounts in the database (PBKDF2-SHA256), the
  first made on a setup page while there is none, a *Users* page to add,
  reset and delete the others, "keep me logged in" for 30 days, login limits
  per username and per address, and *log out everywhere else*. Every admin
  action in the feed names its account; logins are events of kind `auth`.
- The boards, admin and the student's page look like Juice Shop itself: its
  default theme's blue-grey bar, dark cards, pill buttons, green accents and
  blue difficulty stars. Navigation and actions are buttons instead of links.

**The VM, built**
- `make vm` builds the students' VM with nothing but Docker: Packer 1.9.5
  (the last open-source release) and QEMU install Alpine Linux, Docker and
  the `juiceshop-ctf` image, and package the disk as a VirtualBox OVA and a
  VMware `.vmx`. Two adapters by DHCP (NAT, host-only), no SSH, root locked,
  never joined. `deploy/vm/test.sh` boots both exported disks on SATA with
  e1000 adapters and checks the CTF. The central server it joins by default
  is the build's `CENTRAL_URL` (a repository variable in a release); the
  repository names none, and without it the student types the address.

**Releases**
- A `vX.Y.Z` tag releases everything at once (GitHub Actions): the tests,
  the central server on Docker Hub as `macsnoeren/juiceshop-ctf-central`
  (`X.Y.Z`, `X.Y`, `latest`; amd64 and arm64), and the VM for VirtualBox and
  VMware on the GitHub release, all carrying the tag as their version.

**The board**
- Per game at `/g/<slug>/`: podium, category bars, live ticker with first
  blood (a mark, no points; by time of receipt), score over time, pages per
  participant, challenge and category; beamer mode with countdown and final
  result.

**Repository**
- A new repository layout, a Makefile, versions from `git describe`,
  the test setup as three VMs with their Kalis, `tests/e2e.sh` and
  `tests/image.sh`, OPERATIONS.md, PROTOCOL.md and the student sheet.

## 1.0.0 — 2026-11-19

The version that ran in the first student lab: ctf-agent, ctf-status and
ctf-central, a shared access code per instance, the central leaderboard with
its admin page, and the Docker test setup.
