# Juice Shop CTF — version 1.1

What the first student lab showed, why each problem happens (with the code it
comes from), and what version 1.1 changes. The design of 1.0 is in
[DESIGN.md](DESIGN.md) and stays valid where this document does not say
otherwise. The work is broken into small, testable tasks in
[TASK-1-1.md](TASK-1-1.md).

**How the lab runs.** The CTF node is not a server. It is a **VM**, made once
from the Flatcar node `cybersecurity-1`, that every student imports into
their own VirtualBox next to **Kali Linux**. That pair is the student's lab:
they attack the Juice Shop in the CTF VM from Kali, over a VirtualBox network
the two share and nobody else is on. The CTF VM has one more way out — to the
central server on `neo.jmnl.nl`, where the operator follows every student.

```
 the student's laptop (VirtualBox)
 ┌──────────────────────────────────────────────────────────┐
 │  Kali Linux ── lab network (host-only) ──► CTF VM        │       neo.jmnl.nl
 │   browser, Burp, nmap      (DHCP)          juiceshop-ctf ─┼─NAT──► /juiceshop-ctf/
 │                                            (one container)│       board, admin
 └──────────────────────────────────────────────────────────┘
```

**In one paragraph.** Version 1.0 identifies a node by configuration: the
instance id and its token are written into the node's files, and the central
server learns them from an env file (`CTF_SEED`). Thirty VMs imported from
one image are therefore **one instance** to the central server, and its rule
"a new registration ends the previous participant of the instance" makes each
new student wipe the previous one off the board. Version 1.1 gives every VM
its identity **at the moment the student joins**: the central server leads,
from its SQLite database, with any number of **games** at the same time, each
with a **join code**; the student types their name and that code on the CTF
page, the VM asks the central server, and gets back a token **for that
participant**, which it stores and uses for every report after. The VM image
then carries no identity and no secret at all. Juice Shop and the CTF agent
become **one container**, `juiceshop-ctf`. The VM remembers everything in
SQLite, keeps working without the central server, and the door that made
students re-enter a code after every cookie attack goes away: in their own
lab, nobody else can reach the Juice Shop.

---

## 1. The feedback, and where it comes from

| # | Observed | Root cause in 1.0 | 1.1 |
|---|----------|-------------------|-----|
| F1 | The leaderboard shows only one student | All VMs share one instance id and token; a new registration ends the previous participant (§1.1) | Identity per participant, issued at join (§2) |
| F6 | Participants were reset for no reason | The same rule: every new sign-up in the room ended everyone before it (§1.1) | Nothing ends a participant but the operator |
| F2 | After attacking cookies, students had to enter the access code again | The gate cookie is the only memory; re-entry needs the shared code (§1.2) | No gate in the student's own lab; remembered in SQLite (§4.2) |
| F3 | Admin should show the messages that come in | Reports are merged and forgotten (§1.3) | Event feed in `/admin/` (§5.4) |
| F4 | Admin should clean a session and remove students | Only *hide* and *reset*, per row (§1.3) | End/clear a game, remove a participant (§5.3) |
| F5 | The access code should be made in admin, as CTF labs | `CTF_ACCESS_CODE` is an env var of the node (§1.4) | Games with join codes in `/admin/` (§2) |
| F9 | The central server should lead, from SQLite, not env files | `CTF_SEED` creates instances from env (§1.4) | Games and participants live in the database (§2, §5) |
| F10 | The node must work when the central server is unknown | `CTF_CENTRAL_URL` and the token are required (§1.5) | Central optional; join queued, local progress (§4.3) |
| F11 | Remember everything once the code was entered, in SQLite | One JSON state file, no record of the join (§1.2) | Node database (§4.1) |
| F7 | Repository and software not well structured | Three binaries, a flat `internal/`, test setup mixed with images (§1.6) | New layout (§7) |
| F8 | Leaderboard could be more spectacular, with click-through | Plain tables (§1.7) | New board (§6) |
| — | Found while analysing: an admin reset reaches a random VM | Commands are queued per instance, and all VMs are that instance (§1.1) | Commands per participant (§2.2) |
| — | Found while analysing: a class behind NAT is one address to neo | The 1.0 door limits attempts per IP; 1.1's join endpoint must not (§1.8) | Limits per code and per participant (§5.6) |

### 1.1 Thirty VMs, one instance (F1, F6)

The node's compose part sets `CTF_INSTANCE_ID: cybersecurity-1`
(jmnl-external-servers, `nodes/cybersecurity-1/compose.d/60-juiceshop-ctf.yml`)
and every VM imported from the one image carries the same `agent.env`, so every
agent reports as the same instance with the same token. In `MergeReport` (`internal/store/merge.go`), a
report whose registration id the central server does not know yet runs

```sql
UPDATE participants SET ended_at = ? WHERE instance_id = ? AND ended_at IS NULL
```

before inserting the new participant, and the board only shows participants
with `ended_at IS NULL` (`internal/store/board.go`). So:

- the first report of every new student **ends every student before them** —
  the board shows whoever signed up last ("only one student", F1);
- the others' agents go on reporting, and their solves are merged into a
  participant that is ended and invisible ("reset for no reason", F6);
- the rule was meant for one node that is handed from one student to the
  next, which is not how the nodes were used;
- an admin *reset* is a command for the instance (`commands.instance_id`), and
  whichever VM reports next applies it — a random student's progress.

There is no code path that resets a registration by itself; the agent applies
each command once (`report.go`, `CommandsApplied`). Everything observed follows
from the shared identity.

### 1.2 Cookies are the only memory (F2, F11)

The door's only record of a student in the browser is the gate cookie
`jmnl_ctf_gate` (`Path=/`, HttpOnly, 14 days; `setGateCookie` in
`internal/status/server.go`). Juice Shop's challenges make students edit and
delete cookies — the `token` JWT, cookie editors, "clear site data" — and the
gate cookie goes with them. Getting back in then takes the **shared** access
code (`handleEnter`). The gate exists to keep *other* people out of a Juice
Shop; in a lab where only the student's own Kali can reach it (§4.2) it
protects nothing and only gets in the way.

### 1.3 Admin sees only the result (F3, F4)

`MergeReport` folds a report into the tables and keeps nothing of the report
itself: no record of who reported when, nothing about rejected reports (401,
bad body), nothing about commands. Admin offers *hide*, *reset* and *rotate
token* per row and nothing that acts on a whole session.

### 1.4 Configuration in env files (F5, F9)

The access code is `CTF_ACCESS_CODE` in ctf-status's environment
(`cmd/ctf-status/main.go`); the central server's instances come from
`CTF_SEED` (`cmd/ctf-central/main.go`), generated for both sides by
`make-keys.sh` in jmnl-external-servers. Changing a code means a bundle, and
the database the central server already has is not the source of truth.

### 1.5 No central, no node (F10)

`cmd/ctf-agent/main.go` requires `CTF_CENTRAL_URL` and `CTF_CENTRAL_TOKEN`
(`env.Required`, `env.Secret`). Without them the agent does not start, and
without the agent the door is closed (it fails closed, DESIGN.md §10). An
*unreachable* central server is handled — reports back off and the shop stays
open — but an *unknown* one is not.

### 1.6 Structure (F7)

- Three binaries, two of which (agent and status) only work as a pair, over an
  internal API with a shared token.
- `internal/` is flat: node packages, central packages and shared ones side by
  side, with nothing saying which is which.
- `web/` holds the templates of both programs in one embed.
- `docker/` mixes production Dockerfiles, the test compose, sample env files,
  nginx snippets and the start/stop scripts.
- The design documents live at the root with the code.

### 1.7 The board (F8)

A table of rank, name and score, a recent-solves list, and a beamer page that
is the same table larger. A participant page exists (`/p/<id>`), but nothing
leads to a challenge, a category or a moment in time.

### 1.8 One address for the whole room (new)

`internal/status/limiter.go` allows 5 attempts per minute per client address.
On a student's own VM that no longer matters, but the same reflex on the
central server's new join endpoint would: a school's NAT puts the whole room behind one
address, and thirty students join within the same minute.

---

## 2. Games, join codes and a token per participant

The central server **leads**. A **game** is one CTF: a group of students, a
time window, a scoring table and a board. The operator makes games in
`/admin/`; any number can run at the same time, side by side, on the same
central server.

| Field | |
|-------|-|
| name, slug | "Class 2A — 19 Nov"; the board is at `/g/<slug>/` |
| join code | short, readable (10 characters from `abcdefghjkmnpqrstuvwxyz23456789`), shown in admin, regenerable; stored as a hash |
| state | `draft` → `open` (joining allowed) → `running` → `closed` (board frozen) |
| time window | optional start and end; outside it the board shows a countdown or the final result |
| scoring | per game; default as in 1.0 (100/250/450/700/1000/1350) |
| board options | public or hidden, names or initials, beamer theme |

**Joining.** On the CTF page of their own VM (from Kali, at the address the
student finds on the host-only network — part of the exercise) the student
types a **display name and the join
code**. The VM sends them to the central server, which finds the game,
creates the participant and returns a **participant token** — the token is
"filled in per participant at the client", the operator's idea. The node keeps
it in its database and sends it with every report. Different students can be
in different games; a node is in exactly one game at a time.

```
student ── name + join code ──► ctf-node ── POST /api/v2/join ──► ctf-central
                                   ▲                                 │ game found, participant made
                                   └──── participant id + token ─────┘
                                   (node.db; every later report carries the token)
```

### 2.1 What the token does and does not prove

The VM image the students import is identical for everyone and holds **no
secret**: the first identity a VM has is the one it gets at join. That is what
fixes §1.1, and it also means there is nothing to protect in the exported VM.

It also means the honest limit of 1.1 has to be written down. The student owns
the laptop and the VM, and two ways to a false score are open to them:

- **through the disk** — mount the VM's disk from Kali or the host, read the
  participant token and post any report with it;
- **through Juice Shop itself** — Juice Shop keeps its solved challenges in its
  own SQLite database, and some challenges hand the student real code
  execution in the Juice Shop process (the SSTi challenge does). Code in that
  process can mark challenges solved in that database, and the agent, which
  reads Juice Shop's API, cannot tell. **That was already true in 1.0**, with
  the agent in a container of its own.

  **Measured on 20.3.0 (T1.1-02).** In the Docker build every challenge that
  hands out code execution is switched off (`disabledEnv: "Docker"`: SSTi,
  both RCEs, file write, LFR, the NoSQL ones), and they really are off: the
  SSTi payload `#{7*7}` in the profile name comes back literally. So the route
  *through* Juice Shop is closed in practice. If code does run as Juice Shop's
  user, it can fake a solve: an `UPDATE Challenges SET solved=1` on
  `data/juiceshop.sqlite` is reported as solved by `/api/Challenges` at once,
  **without** a webhook and **without** entering the continue code. The disk
  route stays open regardless.

No scheme on a machine the player controls prevents either. What 1.1 does
instead:

- **the damage is limited to one's own score** — a token belongs to one
  participant in one game, and the join code admits nobody to anybody else's;
- **it is visible** — Juice Shop's own cheat score is still reported, and the
  central server flags what an honest agent never sends (§5.5): a solve before
  its prerequisite, ten challenges within a second, solves after the game
  ended, one token reported from two different Juice Shop boots at once;
- **the operator decides** — the event feed shows it and *remove participant*
  ends it.

For a lab this is the right trade. For a CTF with prizes, Juice Shop has to run
somewhere the students cannot reach the disk of — a shared server, which 1.0's
design describes — and even then its code-execution challenges stay a way to a
false solve.

### 2.2 Protocol v2 (sketch)

Still one request each way, so the node needs nothing but outbound HTTPS.

```
POST /api/v2/join                                      (no token yet)
{ "protocol": 2, "name": "Zoë", "code": "4ffpq7tq9f",
  "node": { "uid": "…random, made at this join…", "version": "1.1.0", "juiceshop": "20.3.0" } }

201 { "participant": 41, "token": "ctfp_…", "game": {"id": 7, "name": "Class 2A", "slug": "2a-19nov",
      "state": "running", "ends_at": "…"}, "board": "https://neo.jmnl.nl/juiceshop-ctf/g/2a-19nov/" }
403 { "error": "unknown join code" }        409 { "error": "game closed" }

POST /api/v2/report                                    Authorization: Bearer ctfp_…
{ "protocol": 2, "sent_at": "…", "boot": "…", "solves": [ {"key": "…", "solved_at": "…", "cheat": 0.1} ],
  "restore_failed": false, "commands_applied": ["…"] }

200 { "standing": {"rank": 2, "of": 18, "score": 1350},
      "game": {"state": "running", "ends_at": "…"},
      "commands": [ {"id": "…", "type": "forget"} ] }

POST /api/v2/leave                                     Authorization: Bearer ctfp_…
{ "protocol": 2, "reason": "forget" }                  → 204; the token stops working
```

Commands are per **participant**, so an admin action reaches exactly that
student's VM. `forget` replaces 1.0's `reset_registration` and does on the VM
exactly what the student's own *Forget* button does (§4.4). `/api/v2/leave` is
the other direction: the VM telling the central server that the student
pressed *Forget*.

---

## 3. One VM per student, one container per VM

The lab model makes several things 1.0 carried unnecessary:

- **No seats or pools.** One VM, one Juice Shop, one student.
- **No per-VM configuration.** Nothing in the image differs between
  students: no instance id, no token, no access code. One image for the
  whole class, exported once.
- **No capacity planning on the central server** beyond reports: 30 VMs
  sending a full snapshot each minute is 30 small requests a minute.

### 3.1 Juice Shop and the agent in one container

Version 1.0 runs Juice Shop, ctf-agent and ctf-status as three containers on
three networks, so that code execution inside Juice Shop could not reach the
agent's token or the internet. That isolation was designed for a **shared
server**, where the token is the operator's. In the lab it buys little: the
token belongs to the student, the VM is theirs, and code execution in Juice
Shop can already fake solves through Juice Shop's own database (§2.1). So 1.1
puts them together, for a deployment that is one image and one container:

```
 juiceshop-ctf  (one container; FROM bkimminich/juice-shop:v20.3.0)
 ┌────────────────────────────────────────────────────────────────┐
 │ ctf-node  (PID 1, Go, static)                       uid 10001 │
 │   :80  ─┬─ /ctf/        join, progress, leave       (student) │
 │         ├─ /juiceshop/  reverse proxy ──► 127.0.0.1:3000      │
 │         │               refuses …/continue-code*/apply/       │
 │         └─ /assets/     the same                              │
 │   supervises ──► node build/app.js                  uid 65532 │
 │                  Juice Shop, as a child process               │
 │   polls 127.0.0.1:3000, receives its webhook                  │
 │   /var/lib/ctf/node.db  (0700, uid 10001)  ── HTTPS ──► neo   │
 └────────────────────────────────────────────────────────────────┘
```

- **ctf-node is the entrypoint**, binds port 80 while it is still root, and
  starts Juice Shop as a child. It is the
  supervisor, so it can also **restart** Juice Shop — which is how a student
  who leaves a game, or is removed, gets a clean shop (Juice Shop wipes its
  database on start). 1.0 needed the Docker socket for that and never had it.
- **ctf-node is also the web server.** It serves the student's page and
  reverse-proxies the shop (Go's `httputil.ReverseProxy` passes WebSocket
  upgrades, which socket.io needs), so no nginx sits between Kali and Juice
  Shop. The `…/continue-code*/apply/` refusal of 1.0's nginx moves into it.
- **Three processes, two unprivileged users.** The container starts as root
  with only `SETUID` and `SETGID`. That root process is the **keeper**: PID 1,
  and nothing but a supervisor — it starts Juice Shop as uid 65532 (Juice
  Shop's own user), restarts it when it dies or when asked, and starts the
  real ctf-node as uid **10001 with no capabilities at all**, handing it the
  listening socket for port 80 and a pipe for "restart Juice Shop". ctf-node
  does everything else: the web server, `node.db`, the reports. `node.db`, the
  token and the continue codes are in `/var/lib/ctf`, mode 0700, owned by
  10001. Code running as Juice Shop then cannot read the token, cannot write
  the node's state, and cannot signal or trace ctf-node or the keeper (other
  uids, and `no-new-privileges`). It can still kill Juice Shop itself; the
  keeper starts it again and ctf-node restores the progress, as after any
  restart.
- **Why a keeper, and not one process that drops to 10001** (T1.1-04). A
  process that keeps `SETUID` after `setresuid(10001)` — which it must, to
  start Juice Shop again later — can simply `setuid(0)` back; the spike did
  exactly that. And root without `CAP_KILL` may not signal another uid at
  all: the spike's root got `EPERM` sending SIGTERM to Juice Shop. So the
  keeper stops Juice Shop through a helper it starts *as 65532* (the same
  binary, `ctf-node -signal`), which may signal its own uid, and no
  capability beyond `SETUID`/`SETGID` is needed anywhere. The part that talks
  to the network keeps nothing.
- **What Juice Shop writes** (T1.1-03): `data/juiceshop.sqlite`, the
  regenerated `i18n/*.json`, `frontend/dist`, `ftp/legal.md`, `logs/` and
  `.well-known/csaf`, all of them image content owned by 65532. The root
  filesystem therefore stays writable (a tmpfs over those directories would
  hide the files Juice Shop ships), and only `logs/` gets a tmpfs. Juice Shop
  answers 3.0 s after it is started and 2.8 s after a restart, with zero
  solves: it recreates its database on every start.
- **What is given up:** Juice Shop now shares a network namespace with
  ctf-node, which needs the internet, so Juice Shop's SSRF can reach the
  internet as well. In a lab where Kali already has the internet, that adds
  nothing a student did not have.
- **Upgrading Juice Shop** is a rebuild of this image with a new `FROM` tag,
  with the fixtures captured again (as in 1.0) and the e2e run.

Running it anywhere, for a test or without Flatcar at all:

```sh
docker run -d --name juiceshop-ctf -p 80:80 \
  -v juiceshop-ctf:/var/lib/ctf \
  -e CTF_CENTRAL_URL=https://neo.jmnl.nl/juiceshop-ctf \
  --cap-drop ALL --cap-add SETUID --cap-add SETGID --security-opt no-new-privileges \
  juiceshop-ctf:1.1
```

---

## 4. The node (`ctf-node`)

### 4.1 One program, one database (F11)

The agent and the status page become **one program, `ctf-node`**, inside the
container of §3.1, with one SQLite database (`/var/lib/ctf/node.db`, WAL, the
same driver as the central server):

| Table | Holds |
|-------|-------|
| `membership` | the game, the participant id and **token**, the display name, the node uid, joined at; or nothing |
| `solves` | the first time each challenge was seen solved, with cheat score |
| `codes` | the continue codes for restoring Juice Shop (DESIGN.md §6.2) |
| `baseline` | what was solved before joining (DESIGN.md §6.2a) |
| `outbox` | a join waiting for the central server (§4.3) |
| `standing` | the last rank and score the central server sent |

**Join once, then always the token.** The student types name and join code
once; ctf-node sends them to the central server, stores the participant id
and token it gets back, and from then on uses **only the token**: every
report, every restart, every reboot of the VM. The join code is not kept. The
central server knows the participant by that token for the rest of the game.

**An exported VM must not have joined.** The node uid and the token are made
at the first join, not at first boot, so a VM the operator boots and tests
before exporting carries no identity — **unless the operator joins a game
with it**. The export procedure therefore ends with **Forget** on the VM's own
page (§4.4), and the central server flags one token reporting from two Juice
Shop boots in alternation (§5.5), which is what a VM exported after joining
looks like.

### 4.2 The lab network, and no door (F2)

The gate of 1.0 kept other people out of a Juice Shop on a shared server. In
the lab the only machine that should reach the CTF VM is the student's own
Kali, and that is VirtualBox's job:

| Adapter on the CTF VM | VirtualBox mode | For |
|-----------------------|-----------------|-----|
| 1 | **NAT** | the way out, to `neo.jmnl.nl` |
| 2 | **Host-only**, shared with Kali | the lab |

- **Nothing is configured for either.** Both get their address from
  VirtualBox's DHCP; checked by the operator on the lab image. Finding the CTF
  VM's address on the host-only network from Kali (`ip a`, `nmap`,
  `netdiscover`) is the student's first task.
- **Not bridged.** A bridged adapter puts the Juice Shop on the classroom
  network, where every classmate can solve challenges for another or break
  their shop.

With that, 1.1 has **no gate**: `/juiceshop/` is open on the VM, and `/ctf/`
is the student's own page — join, progress, rank, link to the board, and the
two buttons of §4.4. Nothing is lost when a student wipes every cookie, because nothing
depends on one.

For a node that *is* reachable by others (a bridged adapter, or a shared
server), the gate stays available as an option, `CTF_GATE=session`, now
remembered server-side in `node.db`, with a personal **return code** shown at
join for re-entry instead of the shared code.

### 4.3 Without a central server (F10)

`CTF_CENTRAL_URL` becomes optional; it is not a secret and the image may
carry neo's URL as the default.

- **Central configured but unreachable at join:** the node accepts the name
  and code, puts the join in the outbox, and the student plays straight away.
  Solves are recorded locally. On the first successful join the participant
  token arrives and the next report carries every solve so far, with its time.
  If the code turns out to be wrong, the student's page says so and asks for
  it again — the solves stay.
- **Central unknown:** the node runs on its own — Juice Shop, the student's
  progress page, the score by the scoring table — and simply never reports.
- **Unreachable later:** as 1.0, back-off and a full snapshot when it returns.
- **Another central server.** `CTF_CENTRAL_URL` is only the default. The
  student page has a *Central server* section where the address can be
  changed — to another central server, back to the default, or to none
  (central-less) — as long as the VM has not joined a game; after joining it
  is fixed to the server that issued the token, and *Forget* frees it again.
  It is stored in `node.db`, so one exported VM image can serve a lab that
  runs its own central server without a rebuild. Pointing one's own VM at
  another server affects nobody else's score.

The student's own page never waits for the central server.

### 4.4 Forget, and restarting Juice Shop, on the student's page

Two buttons on `/ctf/`, both handled by ctf-node, which supervises Juice Shop
(§3.1) and can therefore restart it — 1.0 could not.

| Button | What it does | For |
|--------|--------------|-----|
| **Restart Juice Shop** | restarts the Juice Shop process; ctf-node then puts the progress back from the continue codes, as after any restart | a shop the student has broken — Juice Shop even has a DoS challenge |
| **Forget** | wipes **all** student information from `node.db` — membership, participant token, name, solves, continue codes, baseline, outbox, standing — and restarts Juice Shop with nothing to restore, so its database starts empty; tells the central server (`/api/v2/leave`) when it is reachable | starting over; a VM handed to someone else; the last step before exporting the VM |

- **Forget asks first**, by typing `forget`, and says what will be lost. It is
  a POST with a CSRF token, like every form on the page.
- **What stays on the central server:** the solves already reported. A solve
  is never removed there (DESIGN.md §8.2). The participant is marked *left*,
  taken off the board, and kept for the operator, who sees *left (forget)* in
  the event feed and can delete it. Joining again afterwards makes a new
  participant.
- **When the central server is unreachable,** Forget still wipes everything
  locally and the shop starts empty. The participant on the central server
  stays as it was — its token is gone with the VM's memory, so nothing will
  report under it again — and the operator can remove it.
- **The browser forgets nothing by itself,** and does not need to: the old
  continue-code cookie in Kali's browser cannot put progress back, because
  ctf-node refuses `…/continue-code*/apply/` (§3.1).
- **An admin *remove participant*** sends the `forget` command, so the VM does
  exactly the same as the button.

---

## 5. The central server

### 5.1 The database leads (F9)

New tables beside 1.0's: `games`, `participants` gains `game_id`, `token_hash`
and `node_uid` and loses `instance_id` and its "end the previous one" rule;
`commands` points at a participant; `events` holds what came in (§5.4).
`instances` and `CTF_SEED` are removed. Everything an operator configures is
in the database and changed in `/admin/`; the environment holds only where the
database file is and where to listen.

### 5.2 Admin: games (F5)

Create, edit, open/close, regenerate the join code (shown once per
regeneration, stored hashed), scoring, time window, board options. A game page
lists its participants, their last report, their node version, and links to
the board.

### 5.3 Admin: cleaning up (F4)

| Action | Effect |
|--------|--------|
| Remove participant | off the game and the board; the token stops working; the VM is told `forget` (§4.4) |
| Hide participant | off the board, kept in the data (1.0's *hide*) |
| End game | state `closed`: no joins, no solves counted after the end, board frozen as the final result |
| Clear game | delete all participants and solves of the game, keep the game and its code; every VM in it is told `forget` |
| Delete game | the above, and the game |

Destructive actions are confirmed by typing the game's name, and written to
`events`.

### 5.4 Admin: the messages that come in (F3)

An **event feed**, live, filterable by game, participant and kind:

- joins, accepted or refused with the reason (unknown code, game closed);
- reports — collapsed to one line per participant per minute unless something
  changed;
- solves, with the challenge, the time and the cheat score;
- flags from §5.5;
- rejected requests (401, malformed body, protocol mismatch) with the address;
- commands queued, delivered, confirmed; every admin action.

Stored with a retention (30 days by default), newest first, refreshed like the
board, exportable as CSV.

### 5.5 Flags (§2.1)

Raised as events and shown next to the participant, never acted on
automatically:

- a solve whose prerequisite challenge is not solved (a small table, for the
  challenges that build on each other);
- more than N solves within a few seconds;
- a solve time after the game's end, or before the join;
- one participant token reporting from two different Juice Shop boots in
  alternation (a copied VM or a shared token);
- a high Juice Shop cheat score.

### 5.6 Limits on the join endpoint

Per **join code** (e.g. 300 joins per minute, enough for a lecture hall) and
per **wrong code** (slowing down guessing), never per address alone: a school
puts the whole room behind one.

### 5.7 Admin access

Still guarded by nginx (allow list + password) as on neo today. With
destructive game actions, a login of its own becomes worth having — decision
D4.

> **Done after all, in 1.1** (`internal/central/auth.go`): admin accounts in
> the database, the first one made on a setup page while there is none, a
> users page to add and remove the others, and "keep me logged in" for 30
> days. No TOTP. nginx's allow-list stays in front on neo, and is what keeps
> the setup page to the operator on a fresh server.

---

## 6. The leaderboard (F8)

Per game, at `/g/<slug>/`, server-rendered with a small script and no external
assets (the CSP stays `default-src 'self'`); charts are inline SVG.

- **Main board:** a podium for the top three; rows with rank, name, score,
  solved count and a category bar (one colour per category, filled by share
  solved); animated rank changes; a short flash on the row that just scored.
- **Live ticker:** solves as they arrive, **first blood** marked for the first
  solver of each challenge in the game. A **marking only, worth no points**
  (decision D10): speed already breaks ties in the ranking, and a bonus would
  reward faking an early time on a VM the student controls. Who was first is
  decided by when the central server **received** the solve, not by the time
  the VM reports, so the mark cannot be backdated and never changes hands
  afterwards; a student who was offline at the time does not get it.
- **Score over time:** one line per top-10 participant across the game.
- **Click-through:** participant → categories, timeline, first bloods,
  difficulty spread; challenge → who solved it, in what order, when;
  category → the challenge matrix.
- **Beamer mode:** full screen, large type, countdown to the end, ticker at the
  bottom, and a final-result view when the game closes.
- Every colour paired with a label or position; light and dark.

---

## 7. Repository structure (F7)

```
cmd/
  ctf-node/            the node: supervisor, proxy, student page, reports   (was ctf-agent + ctf-status)
  ctf-central/         the central server
internal/
  node/                watcher, student page, node store (SQLite), reporter
  central/             http, admin, games, events, flags, central store, board
  shared/              protocol (v2), names, httpx, config, health, version
  juiceshop/           the client and its fixtures
web/
  node/  central/  static/        templates per program, one embed each
deploy/
  docker/              Dockerfile.node (juiceshop-ctf: Juice Shop + ctf-node), Dockerfile.central
  compose/             the test setup: three student VMs as containers + central, start.sh, stop.sh
  virtualbox/          the lab: the two adapters, Kali, exporting the VM (Forget first), the student sheet
docs/
  DESIGN.md  VERSION-1-1.md  TASK-1-1.md  PROTOCOL.md  OPERATIONS.md
tests/
  e2e.sh               several nodes, two games, offline join, cleanup
scripts/test.sh
Makefile               test, e2e, images, run
CHANGELOG.md
```

Versioning: tag the state that ran in the lab as **v1.0.0**, develop 1.1 on a
branch, and let `VERSION` (ldflags) come from `git describe`, as the
deployment repository already does.

---

## 8. Migration from 1.0

- **Central database:** a numbered migration creates `games` and `events`,
  puts every existing participant into one closed game "1.0" (re-opening the
  ones §1.1 ended, so the lab's real results become visible), and drops
  `instances` once nothing points at it.
- **Node:** on first start, ctf-node imports `state.json` if it finds one: the
  solves and continue codes come along, the registration does not (it belonged
  to the shared identity). The student joins a game with the join code.
- **jmnl-external-servers, the CTF node:** one CTF container instead of three
  (`juiceshop-ctf`, publishing port 80, decision D9 for what stays beside it);
  `agent.env` and `status.env` go — the image needs no secret;
  `CTF_INSTANCE_ID` and the gate go; the README gets the export procedure.
  Nothing changes for the network: both adapters already work from
  VirtualBox's DHCP.
- **jmnl-external-servers, neo:** `central.env` (`CTF_SEED`) and the seed half
  of `make-keys.sh` go; the vhost and backup of `87-juiceshop-ctf.yml` stay.
- **Updating the students' VMs:** there is no path from neo into thirty
  VirtualBoxes, and there does not need to be. A new version is a new VM to
  import; the version each VM runs is in its reports and shown in admin, so an
  old VM is visible.

---

## 9. Decisions to take before building

| | Question | Recommendation |
|-|----------|----------------|
| D1 | Drop the gate in the student's lab | **Yes**, with the lab network of §4.2; `CTF_GATE=session` as an option |
| D2 | Merge agent and status into `ctf-node` | **Yes** (§4.1) |
| D3 | What "remove participant" does on the VM | The same as the student's *Forget* (§4.4) |
| D4 | Admin login of its own | accounts of its own in 1.1 after all (§5.7), nginx's allow-list kept in front; TOTP not planned |
| D5 | Flags: act automatically or only show | **Only show** (§5.5); the operator decides |
| D6 | Can a VM switch games | Yes, after *Forget*; one game at a time |
| D7 | Juice Shop and the agent in one container | **Yes** (§3.1), with the two-user split inside it |
| D8 | The lab network | **Decided:** NAT + host-only, both from VirtualBox's DHCP, nothing configured; the student finds the address (§4.2) |
| D10 | First blood: points or a mark | **Decided:** a mark only, by time of receipt (§6) |
| D9 | What else runs on a student VM | `juiceshop-ctf` only, on Alpine Linux with Docker and chrony. *Revised in 1.1:* the VM is no longer a Flatcar node of jmnl-external-servers but built by the project itself (`make vm`, deploy/vm/README.md): one command, nothing to install but Docker, for VirtualBox and VMware alike; and nobody reaches thirty VMs' logs anyway |

---

## 10. Tasks

The work is in [TASK-1-1.md](TASK-1-1.md): 60 small tasks in nine phases, each
with the check that says it is done, in the order to build them — from the
spikes that settle the open questions of §2.1 and §3.1, through the central
server, admin, the node and the board, to the end-to-end tests and the
deployment in jmnl-external-servers.
