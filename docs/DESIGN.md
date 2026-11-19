# Juice Shop CTF

> **Version 1.1.** This is the design of 1.0. Version 1.1 changes it where
> the first student lab showed it had to; the reasoning is in
> [VERSION-1-1.md](VERSION-1-1.md), the protocol in [PROTOCOL.md](PROTOCOL.md),
> and running a lab in [OPERATIONS.md](OPERATIONS.md). Sections marked
> **1.1** below are superseded; everything else — the watcher (§6), the
> restore (§6.2), the baseline (§6.2a), the merge rules (§8.2) and scoring
> (§8.3) — is still how it works.

Design document for `juiceshop-ctf`: a capture-the-flag on OWASP Juice Shop in
which every student attacks an instance of their own, and a central
leaderboard shows, live and by name, who has solved what.

The starting point is one sentence, and every choice below follows from it:

> A student opens their instance, signs in with their name, and from then on
> every challenge they solve appears on the central leaderboard under that
> name, without anybody typing in a flag.

---

## 1. Objectives

| # | Objective |
|---|-----------|
| O1 | Every student has a **Juice Shop of their own**, so a solved challenge belongs to exactly one person. |
| O2 | The student **signs up on the instance** with their name. The name travels with the progress, so the leaderboard shows names, not hostnames. |
| O3 | A **CTF agent** next to each Juice Shop reports its progress to a **central server**, without the student doing anything. |
| O4 | The central server keeps the **leaderboard** of every instance: score, solved challenges, last activity, a view for a beamer. |
| O5 | The deliberately vulnerable application **cannot reach the rest of the node**: no Zabbix, no logging, no firmware intake, no agent secrets. |
| O6 | Nobody but the registered student can reach the Juice Shop. A scanner on the internet that solves challenges by accident must not score points for anyone. |
| O7 | A restarted Juice Shop does not cost the student their progress. |
| O8 | Everything runs on a laptop with one `docker compose up` (`docker/`), and the same images run unchanged on a Flatcar node. |

Not an objective: preventing students from helping each other, or from handing
their access code to a friend who then solves challenges for them. No technical
measure helps there, and that is a matter for the course, not this design.

---

## 2. The picture

```
           student's browser                          anyone (beamer, teacher)
                  │ https                                       │ https
                  ▼                                             ▼
 ┌─ cybersecurity-1  (http(s)://<ip>/) ──────────┐   ┌─ neo-jmnl-node ─────────────────┐
 │  web-nginx  (jmnl-network + ctf-juice)        │   │  web-nginx                      │
 │    /ctf/       ──────────► ctf-status ─┐      │   │    /        ─► ctf-central      │
 │    /juiceshop/ ─ auth_request ─┘       │      │   │    /admin/  ─► (IP + htpasswd)  │
 │                └─ gate ok ─► juiceshop:3000   │   │    /api/v1/report  ◄──┐         │
 │                              │  ▲      │ API  │   │                       │         │
 │                     webhook  │  │ poll │ 8082 │   │  ctf-central  SQLite  │         │
 │                              ▼  │      ▼      │   │  (ctf-central-db)     │         │
 │                            ctf-agent ──────────────── https, bearer ───────┘         │
 │                            (ctf-agent-state)  │   └─────────────────────────────────┘
 └───────────────────────────────────────────────┘
```

### 2.1 One address, fixed paths

An instance is reached on **its IP address**, so a lab can start before any
DNS exists. nginx serves the paths below on that address, and the same layout
is used everywhere: in `docker/` on a laptop, on a test VM, and on a node.

| Path | Goes to | Gated |
|------|---------|-------|
| `/` | `302` to `/ctf/` | no |
| `/ctf/` | `ctf-status`: sign-up, door, own progress | no |
| `/juiceshop/` | Juice Shop, including its WebSocket (`/juiceshop/socket.io/`) | **yes** (§5.3) |
| `/assets/` | Juice Shop: the one absolute path its frontend uses (translations, one image) | **yes** |
| `/leaderboard/` | `ctf-central`: **only in the `docker/` test setup**, where everything shares one address. In production the central server runs on `neo-jmnl-node` (§13). | no |

All three of our services take a **base path** (`CTF_BASE_PATH`, e.g. `/ctf`)
and generate every link and form action under it, so they never need nginx to
rewrite their HTML.

**Juice Shop under `/juiceshop/`** was the risky part of this layout, because
Juice Shop is an Angular single-page app built to be served from `/`. Three
options were on the table: Juice Shop's own base-path setting, nginx
rewriting a `<base href>`, or falling back to Juice Shop on `/` with the door
beside it. T01 settled it against 20.3.0, in a browser (headless Chrome) and
with curl, and the **first option works**, with two additions:

- Juice Shop 20.3 has `BASE_PATH`, but it is used **only for the redirects
  Juice Shop builds itself** (after a profile update). It does not move the
  routes. So nginx strips the prefix on the way in
  (`rewrite ^/juiceshop/(.*)$ /$1 break`), Juice Shop runs at `/` inside its
  container with `BASE_PATH=/juiceshop`, and `proxy_redirect` puts the prefix
  back on any other `Location` header (the `/api-docs` → `/api-docs/` one).
- The frontend has **no `<base>` tag**: it loads its scripts relative to the
  page, calls its API at `./rest/…`, and even socket.io connects relative to
  the page, at `/juiceshop/socket.io/`. So nothing needs rewriting in the
  HTML, but `/juiceshop/` must allow the WebSocket upgrade, or the "challenge
  solved" pop-ups fall back to polling. The **one** absolute path the frontend
  uses is `/assets/` (the translation files under `/assets/i18n/` and one
  tutorial image). Without it the shop shows translation keys instead of
  text. So `/assets/` is proxied to Juice Shop too, behind the same door.

The directory listings (`/ftp`, `/encryptionkeys`, `/support/logs`) are made
relative by Juice Shop itself, `/redirect?to=` works as on `/`, and so do
logging in, the basket and the Score Board challenge (solved by the frontend
loading `…/1px.png`). The tested nginx lines are
`docker/nginx/ctf-instance.conf`, and they are what a node's vhost carries
over.

**Plain HTTP on an IP address** is fine on a laptop and in a closed lab. On
the internet it is not: the access code and the gate cookie would then cross
the network in the clear. A node on the internet therefore serves the same
paths over HTTPS (open question 1, §13).

Four images, all from this repository except Juice Shop itself:

| Image | Language | Runs on | Role |
|-------|----------|---------|------|
| `bkimminich/juice-shop:v20.3.0` | Node.js (upstream) | every CTF node | the target. Pulled, **pinned to an exact version**, never built. |
| `juiceshop-ctf-agent` | Go | every CTF node | watches Juice Shop, keeps the instance's state, reports to the central server |
| `juiceshop-ctf-status` | Go | every CTF node | the student's door: sign-up, access gate, own progress |
| `juiceshop-ctf-central` | Go | `neo-jmnl-node` | receives the reports, keeps the score, serves the leaderboard and the admin pages |

**Why Go, for all three.** The agent and the status page are small network
services that must run unattended for weeks, and a static binary in a `scratch`
image has nothing in it to patch. Using the same language for the central
server means the report format lives in **one** package (`internal/protocol`)
that both ends compile against, so they cannot disagree about it. The
infrastructure containers in `jmnl-external-servers` (logging,
firmware-update, zabbix, time-sync) are Go for the same reason. The one
dependency is `modernc.org/sqlite` for the central server: SQLite without cgo,
so that image can also be `scratch`.

---

## 3. One student, one instance

> **1.1:** still one Juice Shop per student, but as a VM of their own in VirtualBox next to Kali, all made from one image; a VM gets its identity when the student joins (VERSION-1-1.md §2, §3).

Juice Shop keeps **one** progress per running process: its challenges table
has a single `solved` column, not one per user. Two students on one instance
share a score board, and nobody can tell who solved what. So a solve belongs
to an instance, and an instance must belong to one student at a time.

An **instance** is the unit everything is built around: one `juiceshop`, one
`ctf-agent`, one `ctf-status`, one state volume and one identity at the
central server. Version 1 runs **one instance per node**: `cybersecurity-1`
is one student's. The compose part is written so that a second instance on the
same node is a copy of the part with a suffix (`juiceshop-2`, `ctf-agent-2`,
…) and its own vhost. Nothing in the agent or the central server assumes one
per node. Whether that is worth doing depends on how many students there are
per VPS; see §13.

The instance identity (`CTF_INSTANCE_ID`, e.g. `cybersecurity-1`) is set by the
operator, not chosen by the student, and it is what the central server's token
is issued for.

---

## 4. Keeping the target in its cage

> **1.1:** Juice Shop and ctf-node share one container; the cage is now two users inside it (65532 and 10001) and a keeper that alone holds privilege (VERSION-1-1.md §3.1).

Juice Shop is **meant** to be broken into, and some of its challenges turn the
server into the student's tool:

- **SSRF**: the profile-image-by-URL feature makes the *server* fetch any URL
  the student types. On `jmnl-network` that is a GET from inside the node, to
  every container on it, including `update-receive-endpoint`, the Zabbix
  frontend and `logging-dashboard`.
- **Code execution** challenges (template injection, deserialization) run in
  a sandbox inside Juice Shop, not in the container's real shell. They are
  meant to be safe, but this design does not assume they are.

So Juice Shop is **never on `jmnl-network`**. The node gets two extra networks,
both `internal: true` (no route out of the node at all):

| Network | Members | Purpose |
|---------|---------|---------|
| `ctf-juice` | `juiceshop`, `web-nginx`, `ctf-agent` | nginx proxies to Juice Shop; the agent polls it; Juice Shop sends its webhook to the agent |
| `ctf-internal` | `ctf-status`, `ctf-agent` | the status page reads and writes state through the agent's internal API |

`ctf-status` is also on `jmnl-network` (nginx reaches it there), and so is
`ctf-agent`, because it needs an outbound route to the central server. (In
the `docker/` test setup that network is called `ctf-front`.)
**Juice Shop is on `ctf-juice` only.**

The agent is on three networks, and a server listening on `:8082` would
listen on all three, including `ctf-juice`. So each of its listeners is
bound to a **network alias** instead of a port alone:
`CTF_WEBHOOK_ADDR=ctf-agent-hook:8081`, where `ctf-agent-hook` is the agent's
alias on `ctf-juice`, and `CTF_API_ADDR=ctf-agent-api:8082`, its alias on
`ctf-internal`. Docker resolves an alias, from inside the container, to the
address on that one network, so the internal API is **not even listening**
where Juice Shop can reach it. `tests/e2e.sh` checks this from inside the
Juice Shop container.

What an SSRF from Juice Shop can then reach is exactly:

- **`web-nginx`.** It answers by `Host` header, so the request lands on
  `10-default.conf` or on this instance's own vhost. **>>> No allow-list on
  this node may contain a private or Docker address range**, or an SSRF
  through nginx gets past it. The `allow` lines in `neo-jmnl-nl.conf` name a
  single public address, which is correct.
- **`ctf-agent`'s webhook port.** That port accepts `POST` only, and Juice
  Shop's SSRF is a `GET`. Even a forged `POST` does no harm: the agent treats
  a webhook only as "poll now" and never trusts its body (§6.1).
- **Not `ctf-agent`'s internal API**: it does not listen on `ctf-juice` (see
  above), and if it did, it would still answer only to the bearer token that
  `ctf-status` holds (§9).

`internal: true` also means Juice Shop **cannot reach the internet**. Fetching
a remote profile image then fails. The SSRF challenge itself still works,
because the URL it needs points at Juice Shop's own server. A Juice Shop that
cannot be used as a proxy to attack third parties is the point.

Juice Shop runs with `read_only: false` (it rewrites its SQLite file and
uploads on start), **no volume** (its state is disposable; §6.2), no
capabilities (`cap_drop: [ALL]`), `no-new-privileges`, a memory limit (it
idles at around 300–500 MB), and a `pids` limit.

---

## 5. The door: sign-up is the access gate

> **1.1:** no door in the lab: the host-only network keeps others out. The optional gate is `CTF_GATE=session` with a personal return code (VERSION-1-1.md §4.2).

### 5.1 Why there must be a door

A Juice Shop on a public address is found by scanners within hours, and some
of what they send solves challenges (the 404 and error-handling ones, the
exposed `/ftp`, the `robots.txt`). Without a door those points go to whoever
is registered on the instance. **O6 is about the score being honest, not only
about access.**

### 5.2 Why not basic auth or an IP allow-list

- **HTTP basic auth breaks Juice Shop.** Its frontend sends
  `Authorization: Bearer <jwt>` on every API call once the user logs in to the
  shop. That replaces the browser's basic credentials, nginx answers 401, and
  the shop stops working halfway through the first challenge that needs a
  login.
- **An IP allow-list** works where every student comes from one known
  network, and it is still offered as an extra layer (§13). Students on home
  connections, mobile hotspots and eduroam do not have one known address.

### 5.3 The gate cookie

`ctf-status` owns the door, and nginx asks it about every request:

```
location /juiceshop/    auth_request /_ctf_gate;   → 2xx: proxy to juiceshop:3000
location /assets/       auth_request /_ctf_gate;   → 401: 302 to /ctf/
location = /_ctf_gate   internal; proxy to ctf-status /ctf/gate (no body)
location /ctf/          proxy to ctf-status (never gated)
location = /            302 to /ctf/
```

The full, tested version is `docker/nginx/ctf-instance.conf`. `auth_request`
is in the stock nginx build that `web-nginx` is based on. It also covers the
WebSocket upgrade on `/juiceshop/socket.io/`, which Juice Shop uses for its
"challenge solved" notifications.

`/ctf/` shows one of two forms:

1. **Not registered yet**: *name* + *access code*. A correct code registers
   the instance under that name and sets the gate cookie.
2. **Registered**: *access code* only. This lets the same student in from a
   second browser or after clearing cookies. The name cannot be changed here
   (§5.4).

The **access code** is per instance, set by the operator in the instance's
secrets (§9), and handed to the student together with the URL. It is checked
in constant time and rate-limited per client address (5 attempts per minute),
because it is the only thing between the internet and the shop.

The **cookie** is `jmnl_ctf_gate = <registration_id>.<expiry>.<hmac>`, signed
with `CTF_GATE_SECRET`, `HttpOnly`, `SameSite=Lax`, `Path=/`, and valid for
14 days. It is `Secure` unless `CTF_COOKIE_SECURE=false`. **That switch is
for plain HTTP on an IP address** (§2.1): a browser refuses to store a
`Secure` cookie from `http://<ip>/`, so without it the door would never open
in a lab. Browsers treat `http://localhost` as secure, so the laptop setup
needs the switch only when it is opened by IP from another machine.

Because the `registration_id` is in the cookie, **resetting the registration
closes the door for every cookie issued before**, without a list of sessions
to revoke. `/gate` checks the signature, the expiry and the `registration_id`
against the agent's current registration. It caches the agent's answer for 5
seconds (a failure for 2), and holds a lock while it asks, so that a page load
of 80 requests is one API call.

`HttpOnly` matters here: the student will run XSS against their own shop, on
the same origin as the gate. An injected script cannot read the cookie. It
could still make requests that carry it, but only on the student's own
instance, which they already have access to.

Tools such as Burp and ZAP work through the browser and carry the cookie.
Command-line tools (`sqlmap`, `curl`) need it passed in (`--cookie`). The
status page shows how.

### 5.4 Registration

A registration is `{registration_id (random 128-bit), name, registered_at}`.
The name is 2–40 characters of letters, digits, spaces and `.-'_`, trimmed and
NFC-normalised. It is shown on a public leaderboard, so the form says so.

**The first registration on an instance is final from the student's side.**
Changing it is an operator action at the central server (§8.4). That stops a
student from renaming someone else's instance with its access code, and it
keeps the leaderboard's names stable during a session.

What is stored is a **display name only**. A student number or an e-mail
address is personal data on a public page, and nothing in the CTF needs it.
If the course needs to link a name to a student, that happens outside this
system (see §13).

---

## 6. How the agent knows what is solved

### 6.1 Truth comes from polling; the webhook only says "now"

Juice Shop exposes everything needed without authentication:

- `GET /api/Challenges`: every challenge with `key`, `name`, `category`,
  `difficulty` (1–6), `disabledEnv` and `solved`. Its own score board reads
  this.
- `SOLUTIONS_WEBHOOK=<url>`: when set, Juice Shop `POST`s a JSON body to that
  URL on every solve (`solution.challenge` = the key, `issuedOn`, a cheat
  score, and the issuer's version).

The agent **polls every 30 seconds** and treats the webhook only as a reason
to poll **immediately** (debounced: at most one poll per 2 seconds). The
webhook body is never stored or believed:

- A poll is idempotent and complete. It sees solves the webhook missed while
  the agent was restarting, and it is the only way to see a Juice Shop that
  restarted and lost everything (§6.2).
- A forged webhook (§4) then costs one extra poll and nothing else.

The one thing taken from the webhook is the **cheat score** (Juice Shop's own
estimate of how plausibly fast a solve was). It is shown to the operator, never
used in the score.

**The solve time** is the moment the agent first saw the challenge as solved,
in the agent's clock, which the node's `timesync` keeps correct. With the
webhook that is within a second or two of the solve, and without it within 30
seconds. Juice Shop's own `updatedAt` is not used, because it also changes for
reasons other than solving (coding-challenge progress).

**Disabled challenges.** Inside Docker, Juice Shop disables the challenges
that would be unsafe there (`disabledEnv` is `"Docker"`; 18 of the 116 in
20.3.0). They are reported with `disabled: true` and count neither in the
score nor in the "x of y".

The API details above were captured from the pinned version and are kept as
fixtures in `internal/juiceshop/testdata/`, with the commands that produced
them in its README. The client's tests run against those files. **Upgrading
Juice Shop means capturing them again** and running the tests.

### 6.2 A restarted Juice Shop forgets everything

Juice Shop recreates its database on every start. A container restart (a node
reboot, an app bundle, the OOM killer) puts every student back at zero.

Juice Shop has its own answer, **continue codes**: `GET /rest/continue-code`
returns a short code that encodes the set of solved challenges, and
`PUT /rest/continue-code/apply/<code>` restores it. The coding challenges have
their own pair (`/rest/continue-code-findIt`, `/rest/continue-code-fixIt`).

The agent:

1. fetches the continue codes after every poll in which Juice Shop's progress
   changed (a new solve, or coding-challenge progress, which does not change
   `solved`), and stores them in its state. The `findIt`/`fixIt` endpoints
   answer `{}` when there is nothing to encode, and an empty code is never
   applied;
2. notices a lost progress when Juice Shop no longer has a solve that the
   state knows, **and** a registration exists;
3. then applies the stored codes, polls again, and logs
   `juice shop progress restored`. If something is still missing, it logs a
   warning and reports `restore_failed: true`. It tries **once per Juice Shop
   boot**, and does not loop.

**The boot id.** Juice Shop recreates its database on start, so every
challenge's `createdAt` is the moment it started. The agent uses that as a
boot id: a new value means Juice Shop restarted, even when nothing had been
solved yet. "Once per boot" above is per boot id.

A restore does **not** send the webhook (Juice Shop logs the challenges as
"Restored", not "Solved"), so it never looks like a burst of new solves.

The central server does not depend on any of this: a solve it has recorded
stays recorded (§8.2). The restore exists so that the **student's** shop agrees
with the leaderboard again, because some challenges build on earlier ones.

**Students cannot apply a continue code.** The apply endpoint is the agent's
alone, and nginx refuses `…/rest/continue-code*/apply/` from the outside
(`docker/nginx/ctf-instance.conf`, case-insensitive, because Express routes
that way). Found on the first test node: Juice Shop's own frontend keeps every
code in a cookie (`continueCode`, `-FindIt`, `-FixIt`, valid a year) and PUTs
it back whenever it sees the server start. Left open, that undid every reset,
handed the next student on a shared lab PC the previous one's solves — after
they signed up, so outside the baseline of §6.2a and counted for them — and let
a student paste a classmate's code and take all of their points. The agent
talks to `juiceshop:3000` directly and never through nginx, so its restore is
unaffected. `tests/e2e.sh` applies instance 1's code to instance 2 through
nginx and checks that instance 2 still has nothing.

### 6.2a The baseline: what was solved before the student signed up

When a student registers, the agent records what Juice Shop **already** has
solved at that moment: left over from a previous student on this instance, or
from before the door existed. That set is the **baseline**, and it never counts
for the new student. It applies only to the boot it was taken in: a restarted
Juice Shop has nothing solved, so after a restart every challenge counts again.

This is what makes a reset (§8.4) safe without a restart of Juice Shop: the
next student cannot inherit the previous one's points. The restart is still
worth doing, because without it the next student finds challenges marked solved
that they cannot solve again.

### 6.3 The agent's state

> **1.1:** `node.db` (SQLite) instead of `state.json`; a 1.0 state file is imported once, without its registration (VERSION-1-1.md §4.1, §8).

One JSON file on the volume `ctf-agent-state`, `/var/lib/ctf-agent/state.json`,
written as temp file + `fsync` + `rename`, so a crash leaves the old file or
the new one and never half of one:

```json
{
  "version": 1,
  "registration": {"id": "…", "name": "…", "registered_at": "…"},
  "solved": {"scoreBoardChallenge": "2026-11-18T09:14:03Z"},
  "continue_codes": {"challenges": "…", "find_it": "…", "fix_it": "…"},
  "cheat": {"scoreBoardChallenge": 0.0},
  "baseline": ["…"], "baseline_boot": "…",
  "restore_failed": false,
  "commands_applied": ["…"],
  "standing": {"score": 100, "rank": 1, "of": 12},
  "last_report_at": "…"
}
```

A file that cannot be parsed is moved aside as `state.json.corrupt-<time>`,
never overwritten, and the agent starts empty with a loud log line. The
instance then has forgotten its student, but the central server has not
forgotten a single solve. A file written by a newer agent (a higher
`version`) is refused instead, because reading it would silently lose
whatever the newer version stored.

The agent is the **only writer**. `ctf-status` reads and changes state only
through the agent's API, so there is no file shared between two containers
and no locking.

---

## 7. Agent → central: the report

> **1.1:** protocol v2 — join, report with a participant token, leave — in [PROTOCOL.md](PROTOCOL.md). Still a full snapshot every minute.

### 7.1 Push, full snapshot, every minute

The central server cannot reach the CTF nodes, and should not: a node accepts
nothing from outside but web traffic and its firmware bundles. So the agent
**pushes**, over HTTPS, to `CTF_CENTRAL_URL`.

Every report is the **complete state of the instance**, not a delta. That makes
it idempotent: a lost report is repaired by the next one, a duplicate does
nothing, and the central server needs no sequence numbers. It is sent:

- within 2 seconds of a change (a new solve, a registration), and
- every 60 seconds regardless, which is also the heartbeat by which the
  leaderboard shows an instance as online.

On failure the agent retries with exponential backoff (2 s doubling to 5 min,
with jitter). It never queues reports, because the next snapshot replaces
every one it would have queued.

The full catalogue is about 110 challenges, roughly 15 KB as JSON, sent once a
minute. Nobody needs to optimise that.

### 7.2 The format (`internal/protocol`, version 1)

```http
POST /api/v1/report
Authorization: Bearer <CTF_CENTRAL_TOKEN>
Content-Type: application/json
```

```json
{
  "protocol": 1,
  "instance": "cybersecurity-1",
  "sent_at": "2026-11-18T09:15:00Z",
  "agent_version": "0.1.0",
  "juiceshop": {"version": "20.3.0", "reachable": true, "restore_failed": false},
  "registration": {"id": "9f2c4e1a0b3d5f6e7a8b9c0d1e2f3a4b", "name": "Sanne de Vries", "registered_at": "2026-11-18T09:01:12Z"},
  "challenges": [
    {"key": "scoreBoardChallenge", "name": "Score Board", "category": "Miscellaneous",
     "difficulty": 1, "disabled": false, "solved_at": "2026-11-18T09:14:03Z", "cheat": 0.0},
    {"key": "loginAdminChallenge", "name": "Login Admin", "category": "Injection",
     "difficulty": 2, "disabled": false, "solved_at": null}
  ],
  "commands_applied": ["c-17"]
}
```

This example is also a test (`internal/protocol/protocol_test.go`): it must
validate and survive a JSON round trip unchanged. Change both together.

`registration` is `null` on an instance nobody has signed up for yet. It still
reports, so the operator sees it as online and waiting.

The response may carry **commands**. This is the only channel from the central
server to an instance, and it rides on a request the instance makes itself:

```json
{"ok": true, "commands": [{"id": "c-17", "type": "reset_registration"}],
 "standing": {"score": 350, "rank": 2, "of": 14}}
```

The agent applies a command once (its `id` goes into `commands_applied`) and
confirms it in the next report. The central server repeats a command on every
answer until it is confirmed, so a lost answer delays it and does not lose it.
The only command in version 1 is `reset_registration` (§8.4). An unknown
command type is ignored, so a newer central server can add one without
breaking older agents. Because commands ride on reports, a reset arrives with
the next report: within `REPORT_INTERVAL` (60 s).

`standing` is the participant's score and rank on the board. The status page
shows it, so the agent does not need to know the scoring table.

The central server rejects a report with 400 if `protocol` is unknown, if
`instance` does not match the token, or if the body is over 256 KB. It answers
401 for an unknown token, and the agent then logs that loudly once per hour
instead of once per minute.

---

## 8. The central server

> **1.1:** games, participants per game, commands per participant, events and flags; first blood and ties by time of receipt; admin as in OPERATIONS.md (VERSION-1-1.md §5, §6).

### 8.1 Data model (SQLite, volume `ctf-central-db`)

| Table | Holds |
|-------|-------|
| `instances` | `id`, `label`, `token_hash` (sha256), `created_at`, `last_report_at`, `agent_version`, `juiceshop_version`, `reachable`, `restore_failed` |
| `participants` | `id`, `instance_id`, `registration_id`, `name`, `registered_at`, `ended_at` (set on reset), `hidden` |
| `challenges` | `key`, `name`, `category`, `difficulty`, `disabled`: the catalogue, upserted from reports |
| `solves` | `participant_id`, `challenge_key`, `solved_at`, `cheat`, primary key on the first two |
| `commands` | `id`, `instance_id`, `type`, `created_at`, `delivered_at`, `confirmed_at` |
| `scoring` | `difficulty` → `points` |

### 8.2 Merging a report

A solve belongs to a **participant** (instance + `registration_id`), not to
an instance. That way an instance that is reset and given to a second student
does not hand the first student's points to the second.

- An unknown `registration_id` for the instance creates a new participant (and
  sets `ended_at` on the previous one, if any).
- Every challenge with a `solved_at` is inserted, and on a conflict only a
  missing cheat score is filled in.
  **The first recorded time wins, and a solve is never removed by a report.**
  A report that is missing a recorded solve (Juice Shop reset, restore failed)
  changes nothing in the score and only raises `restore_failed` for the
  operator.
- `solved_at` values in the future (more than 5 minutes past `sent_at`) are
  clamped to the time the report was received.
- The whole merge is one transaction.

### 8.3 Score and leaderboard

Points per difficulty, configurable in `scoring`, with these defaults:

| ★ | 1 | 2 | 3 | 4 | 5 | 6 |
|---|---|---|---|---|---|---|
| points | 100 | 250 | 450 | 700 | 1000 | 1350 |

Rank by **score descending**, then by **the time the participant reached that
score, ascending** (whoever got there first is ahead). Ended and hidden
participants are not on the board.

Pages, all server-rendered, with a small script that refreshes the table every
10 seconds from `GET /api/v1/leaderboard`:

| Path | Who | What |
|------|-----|------|
| `/` | public | rank, name, score, solved / total, last solve, online dot |
| `/beamer` | public | the same, large, with "just solved" lines scrolling past |
| `/p/<participant>` | public | one participant: progress per category, solve list (names and times only) |
| `/admin/` | operator | instances (create, show token once, rotate), participants (hide, reset), cheat scores, CSV export |

Polling rather than SSE, because a refresh every 10 seconds is live enough for
a leaderboard, and long-lived streams through `web-nginx` need buffering
disabled per location. That is one more thing to get wrong for no visible
gain.

### 8.4 Admin

The `/admin/` paths have **no login of their own**. Like `/logging/` and
`/update/` on `neo-jmnl-node`, they sit behind nginx with an IP allow-list
plus an htpasswd in `/etc/jmnl/nginx/secrets`. A password form the central
server wrote itself would be the weakest part of it.

**Reset registration**: the operator ends a participant, and the server queues
`reset_registration` for that instance (once, however often the button is
pressed). On the next report the agent clears the registration and the
continue codes, and old gate cookies stop working (§5.3); the sign-up form is
back. The instance's Juice Shop still holds the previous student's solves until
its container is restarted. Thanks to the baseline (§6.2a) they never count for
the next student, but that student would find them marked solved. Giving an
instance to a new student is therefore *reset, then restart Juice Shop*.
Version 1 accepts that rather than give the agent the Docker socket (see
`dockers/backup/DESIGN.md` §2 in `jmnl-external-servers` for what that socket
costs).

The admin page also shows each instance's last report, versions and the
`restore_failed` / "Juice Shop down" flags, creates instances (the token is
shown **once**, as the lines to paste into `agent.env`), rotates tokens, hides
participants, edits the scoring table, and exports every participant as CSV.
Every form carries a CSRF token. That is not against strangers (nginx keeps
them out) but against a page in the operator's own browser.

---

## 9. Configuration and secrets

> **1.1:** neither program has a secret any more. ctf-central reads only its database path, listen address, base path and log level; ctf-node only the central server's address and timings (their `main.go`).

Nothing secret is in an image, a build arg, git or ignition. On a node, the env
files live in `/etc/jmnl/juiceshop-ctf/` (mode 0700), arrive through the
bundle from `nodes/<node>/keys/juiceshop-ctf/`, and are loaded with
`env_file: required: false`.

**`agent.env`** (only `ctf-agent` reads it)

| Variable | |
|----------|---|
| `CTF_INSTANCE_ID` | e.g. `cybersecurity-1` |
| `CTF_CENTRAL_URL` | e.g. `https://ctf.jmnl.nl` |
| `CTF_CENTRAL_TOKEN` | issued by `/admin/`, shown once |
| `CTF_INTERNAL_TOKEN` | shared with `ctf-status`, guards the internal API |

**`status.env`** (only `ctf-status` reads it)

| Variable | |
|----------|---|
| `CTF_ACCESS_CODE` | what the student types at the door |
| `CTF_GATE_SECRET` | 32 random bytes, signs the gate cookie |
| `CTF_INTERNAL_TOKEN` | the same value as in `agent.env` |

Two files and not one, so that the container facing the student's form input
never holds the central token, and the container holding the central token
never holds the gate secret.

**Not secret**, in the compose part's `environment:`. The complete list, with
defaults, is at the top of each `cmd/*/main.go`; the ones that matter:

| Variable | Service | |
|----------|---------|---|
| `CTF_WEBHOOK_ADDR` | agent | `ctf-agent-hook:8081`: bound to the `ctf-juice` alias (§4) |
| `CTF_API_ADDR` | agent | `ctf-agent-api:8082`: bound to the `ctf-internal` alias (§4) |
| `JUICESHOP_URL` | agent | `http://juiceshop:3000` |
| `POLL_INTERVAL`, `REPORT_INTERVAL`, `REPORT_BACKOFF_MAX` | agent | 30s, 60s, 5m |
| `CTF_AGENT_URL` | status | `http://ctf-agent-api:8082` |
| `CTF_BASE_PATH` | status, central | `/ctf` for the status page; empty (own hostname) or `/leaderboard` for the central server (§2.1) |
| `CTF_JUICESHOP_PATH` | status | `/juiceshop/`: where a student goes through the door |
| `CTF_LEADERBOARD_URL` | status | the link to the board |
| `CTF_COOKIE_SECURE` | status | `true`; `false` only for plain HTTP by IP (§5.3) |
| `CTF_TIMEZONE` | status, central | `Europe/Amsterdam`, for the times on the pages |
| `CTF_DB_PATH` | central | `/data/central.db` on `ctf-central-db` |
| `SOLUTIONS_WEBHOOK`, `BASE_PATH` | Juice Shop | `http://ctf-agent-hook:8081/webhook`, `/juiceshop` |

**Central** (`/etc/jmnl/juiceshop-ctf/central.env` on `neo-jmnl-node`): no
secret is needed in version 1. Instance tokens are created in `/admin/` and
stored as hashes in the database. `CTF_SEED` creates instances with tokens
from the environment, and exists **for the test setup only**.

**Ports** inside the containers: Juice Shop 3000, agent 8081 (webhook, on
`ctf-juice`) and 8082 (internal API, on `ctf-internal`), status 8080, central
8080. None of them is published; everything goes through `web-nginx`.

All three services log one line per event to stdout, which the node's syslog
driver passes to `logging-proxy`. They never log a token, an access code or a
cookie.

---

## 10. When things go wrong

| What fails | What happens | What the student sees |
|------------|--------------|----------------------|
| central server down | the agent retries with backoff; nothing is lost, because the next snapshot carries everything | their shop works; the status page says "leaderboard last reached hh:mm" |
| agent down | Juice Shop keeps working and solves still count, because the first poll after the restart sees them. The gate fails **closed** (`ctf-status` cannot check the registration) | after the gate cache expires: the `/ctf/` page with "temporarily unavailable" |
| `ctf-status` down | nginx `auth_request` fails, so the gate is closed | 502 / the error page |
| Juice Shop restarted | progress reset; the agent restores it with the continue codes (§6.2) | a few seconds of zero, then everything back |
| restore failed | the central score is unaffected; the operator sees `restore_failed` | challenges that build on earlier ones may need redoing |
| node reinstalled | agent state from the backup brings back the registration and continue codes | as above |
| token rotated without updating the node | 401; the agent logs it hourly; the instance shows offline | nothing, until the leaderboard stops moving |

The gate fails **closed** on purpose. A gate that opens when its checker is
down opens for the scanners too (§5.1).

---

## 11. Repository layout

> **1.1:** see the README and VERSION-1-1.md §7.

```
juiceshop-ctf/
  DESIGN.md                 this file
  README.md                 how to run it, for a person who has not read this
  go.mod                    one module, three binaries; one dependency (modernc.org/sqlite)
  cmd/ctf-agent/            each main.go documents its environment variables
  cmd/ctf-status/
  cmd/ctf-central/
  internal/protocol/        the report format, shared by agent and central
  internal/names/           the display-name rule (§5.4)
  internal/juiceshop/       the Juice Shop client + testdata/ fixtures (§6.1)
  internal/agent/           poll, restore, baseline, state file, webhook, API, reporter
  internal/gate/            cookie signing and checking
  internal/status/          the door: pages, /gate, rate limit, agent client
  internal/store/           central SQLite: schema, migrations, merge, ranking
  internal/central/         the central server's HTTP side, admin pages
  internal/config, httpx, health, version   small shared helpers
  web/                      templates (status/, central/) and static/ (stylesheet,
                            leaderboard script), embedded with go:embed
  docker/
    Dockerfile.agent  Dockerfile.status  Dockerfile.central
    docker-compose.yml      the local test setup (§12)
    nginx/ctf-instance.conf the path layout of §2.1 and the gate of §5.3: what a node carries over
    nginx/default.conf, ctf-leaderboard.conf, ctf-admin.htpasswd   the test setup's nginx
    env/                    sample env files for the test setup, no real secrets
    README.md
  scripts/test.sh           gofmt, vet, unit tests (-race); -fuzz for the cookie fuzzer
  tests/e2e.sh              everything in Docker
```

The Dockerfiles live **in the project** (`docker/`), not under
`jmnl-external-servers/dockers/`. They are production Dockerfiles from the
start (multi-stage, `scratch`, non-root user, pinned Go version), in the same
way as drumschool's and intento's.

---

## 12. Testing it locally

> **1.1:** `make run` and `tests/e2e.sh` (deploy/compose/README.md).

`docker/docker-compose.yml` brings up the whole thing on one machine, behind
one nginx (`nginx:1.29-alpine`, `docker/nginx/default.conf`) on port 80,
with the path layout of §2.1: one instance (`juiceshop`, `ctf-agent`,
`ctf-status`) and `ctf-central`. The networks are the same as on the node
(§4), including `internal: true`.

```sh
cd docker && docker compose up --build
# http://<ip>/ctf/           sign up   (access code: test1)
# http://<ip>/juiceshop/     the shop, after signing up
# http://<ip>/leaderboard/   the board
```

`<ip>` is `localhost` on the laptop itself, or the machine's address from
another one. For that second case the test env files set
`CTF_COOKIE_SECURE=false` (§5.3). `docker/README.md` lists every way the test
setup differs from production; the main ones are a faster poll and report
interval.

A second instance, so that the leaderboard has something to rank, is a
compose profile on port 8080 with the same paths:
`docker compose --profile second up` gives `http://<ip>:8080/ctf/` with access
code `test2`.

`CTF_SEED` creates the two instances and their tokens in the central database
at start, so there is no admin round-trip before the first test.

Tests:

- `scripts/test.sh`, no Docker: protocol round trip; name rules; merge rules
  (first time wins, no un-solve, new participant on a new `registration_id`,
  future times clamped); ranking ties; gate cookie (forged, expired, after
  reset, fuzzed); restore, baseline and reset against a fake Juice Shop;
  webhook debounce; the status pages, rate limit and gate cache; the report
  endpoint and the admin pages. The Juice Shop client runs against the
  captured fixtures.
- `tests/e2e.sh`, Docker, about a minute: the door, sign-up on two
  instances, a solve by `curl` (`/juiceshop/ftp/acquisitions.md`, Confidential
  Document), the leaderboard, a Juice Shop restart and the restore, isolation
  from inside the Juice Shop container (agent API, central server, status
  page, internet; webhook `GET` → 405), the central server down and the agent
  down, and a reset from `/admin/`.

---

## 13. Into `jmnl-external-servers`

> **1.1:** neo runs ctf-central without `CTF_SEED`; `cybersecurity-1` is the VM image with `juiceshop-ctf` and `logging-proxy` (decision D9, deploy/virtualbox/README.md).

The usual checklist from that repository's `CLAUDE.md`, applied to both sides:

**New node `nodes/cybersecurity-1/`**: `node.conf` (VPS address, interfaces),
`compose.yml` with the infrastructure parts (logging, update, time, web,
backup) and `compose.d/60-juiceshop-ctf.yml`; Juice Shop in
`DOCKER_PULL_IMAGES`, the agent and status images in `DOCKER_BUILD_IMAGES`;
the networks from §4; a vhost with the gate (§5.3), in two TLS steps;
`ctf-agent-state` in the backup; `/etc/jmnl/juiceshop-ctf` in the ignition
`directories:`; the node added to `NODES` in `flatcar.conf`.

**On `neo-jmnl-node`**: `compose.d/NN-juiceshop-ctf-central.yml`, the image,
monitoring, a vhost with `/admin/` restricted, the certificate,
`ctf-central-db` in the backup (read-write: SQLite).

### Decided for the test phase (answers to questions 1-3)

The first deployment is a TEST, and three questions were answered for it:

- **HTTP, no HTTPS.** Students reach the node by its IP address over plain
  HTTP: `http://<ip>/ctf/`, `/juiceshop/`, `/leaderboard/`. ctf-status runs
  with `CTF_COOKIE_SECURE=false`, because a browser refuses a Secure cookie
  over HTTP. What that costs (access code, gate cookie and admin password in
  the clear) is written down in the node's vhost.
- **The instance on a node of its own, `cybersecurity-1`; the central server
  on `neo-jmnl-node`**, at `https://neo.jmnl.nl/juiceshop-ctf/` (a path on
  the existing TLS name, so no new certificate). The agent reports there over
  HTTPS. The CTF node is built with a configuration of its own
  (`flatcar-deployment/cybersecurity.conf`), so neither node's bundle carries
  the other's images. Both halves of the instance token are made on the build
  machine by `nodes/cybersecurity-1/make-keys.sh` — the agent's `agent.env`
  and neo's `central.env` with `CTF_SEED` — instead of through `/admin/`, so
  one bundle per node is the whole procedure.
- **No Zabbix on the CTF node.** Logging, update, time and web only, and no
  backup part there: the one thing worth keeping, the leaderboard, is on neo,
  in neo's monitoring and backup.

So the plan above changes for now: no TLS steps on the CTF node, no backup
there, and no entry in `flatcar.conf`'s `NODES`.

### Still open

4. **Is there a known student network?** If so, an IP allow-list on top of
   the gate costs one line and keeps scanners out entirely.
5. **Events.** Does the central server need several CTFs side by side (per
   class, per year) with separate boards, or is one board that the operator
   empties between runs enough? Version 1 assumes one board.
6. **Linking a name to a student.** Display name only (§5.4), or is a student
   number needed after all, and then only visible in `/admin/`?
7. **Juice Shop settings.** Default configuration, or a custom one (hide the
   GitHub links and the "solve it yourself" tutorial, a CTF-style theme)
   mounted as `config/jmnl.yml` with `NODE_ENV=jmnl`?
8. **After the test:** HTTPS for the CTF node (a DNS name), and more than one
   instance per VPS (about 0.5 GB each).

---

## 14. Alternatives considered

**`juice-shop-ctf-cli` + CTFd.** The standard way to run a Juice Shop CTF:
Juice Shop shows a flag on every solve (`CTF_KEY`), and the student pastes it
into CTFd, which keeps the board. Rejected. It is the opposite of O3: the
student has to copy every flag. CTFd needs MariaDB and Redis next to it. And a
flag is a string that can be passed to a friend, while a solve recorded by the
agent can only come from the student's own instance.

**OperationRoom (`projects/leaderboard`).** Already on `neo-jmnl-node`, with
teams, games and a live board. Rejected as the home for this. Its model is a
ladder of levels that an operator (or its AI) grades by hand, while this one
has 100+ independent challenges graded automatically by the target itself.
Bending one into the other would make both worse. If a course wants both, a
link from one board to the other is enough.

**Agent and status page in one container.** Less to deploy. Rejected because
of §9: the container that parses what students type should not be the one that
holds the token for the central server, and a crash in one should not take
the other with it (the gate fails closed, the reporting does not).

**Juice Shop with a volume, instead of continue codes.** Juice Shop rebuilds
its database on start regardless, so a volume does not keep its progress.
Continue codes are the mechanism it provides for exactly this.
