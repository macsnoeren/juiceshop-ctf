# Juice Shop CTF 1.1 — tasks

Small, testable steps that build version 1.1 as [VERSION-1-1.md](VERSION-1-1.md)
describes it. Every task names the section it implements and ends with
**Done when**: the check that says it is finished. A task is done when that
check passes, not when the code is written.

## Status

All tasks are done. **T1.1-60**, the VM image, was done differently from
its wording: the VM is no longer a Flatcar node installed and exported by
hand, but built by `make vm` (Packer and QEMU in Docker) as a VirtualBox OVA
and a VMware `.vmx` ([deploy/vm/README.md](../deploy/vm/README.md)); the
import into VirtualBox and VMware themselves stays the operator's check. Where the result differs from the wording of a task:

- **T1.1-04** was settled with the real ctf-node instead of a throwaway
  spike, and changed the design: a process that drops to 10001 but keeps
  SETUID can become root again, so a root **keeper** starts Juice Shop (65532)
  and ctf-node (10001, no capabilities). `tests/image.sh` holds the checks of
  T1.1-04, -31, -38 and -39 on the real image.
- **T1.1-11** is tested on a database built the way 1.0 built the lab's (one
  instance, five students, four ended by the fifth), not on a copy of a real one.
  Before 1.1 replaces 1.0 on a server: run the 1.1 ctf-central once on a
  copy of its `central.db`.
- **T1.1-15**: a wrong-code budget that is used up is a 429 *before* the code
  is checked, so it slows a guesser down; a correct code never uses it up.
- **T1.1-22**: the cheat-score flag needs an average above 0.9 over at least
  five scored solves; Juice Shop scores any quick second solve high.
- **T1.1-23**: besides the database file and the listen address, ctf-central
  reads `CTF_BASE_PATH` (where it is served) and `LOG_LEVEL`; title and time
  zone moved into the database (admin → Settings).
- **T1.1-38**: only `/juice-shop/logs` gets a tmpfs; the other paths Juice
  Shop writes hold files the image ships (T1.1-03).
- **T1.1-58/59** change the deployment, which is not in this repository.

## Conventions

- `go vet ./...` and `go test -race ./...` pass after every task
  (`scripts/test.sh`); table tests where a rule has cases.
- No network in unit tests: `httptest` only. Juice Shop in tests is the fake
  built from the captured fixtures, except where a task says *against the real
  image*.
- Times are UTC, RFC 3339, in the protocol and the databases.
- No token, join code, participant name or cookie value in any log line;
  names may appear in the central server's event feed, which is the operator's.
- Schema changes are numbered migrations; a migration that ran is never edited.
- The **order** below is the order to build in. Each phase leaves the
  repository working: the 1.0 test setup keeps running until Phase G replaces
  it.

Decisions D1–D10 of VERSION-1-1.md §9 are taken as recorded there.

---

## Phase 0 — settle the unknowns

Spikes: throwaway code, the result written into VERSION-1-1.md.

- [x] **T1.1-01 Tag and branch.** Tag the commit that ran in the lab as
  `v1.0.0`, start branch `v1.1`.
  **Done when** `git describe` on the branch says `v1.0.0-…`.

- [x] **T1.1-02 Can code in Juice Shop fake a solve?** (§2.1) Against the
  real 20.3.0 image: get code execution through the SSTi challenge, mark an
  unsolved challenge solved in Juice Shop's SQLite database, and see whether
  `/api/Challenges` reports it solved — and whether the webhook fires.
  **Done when** the result is written into VERSION-1-1.md §2.1, either way.

- [x] **T1.1-03 Juice Shop as a child process.** (§3.1) In a throwaway image
  `FROM bkimminich/juice-shop:v20.3.0`, a small Go program as entrypoint that
  starts `/nodejs/bin/node build/app.js` in `/juice-shop` as uid 65532, waits
  for port 3000, stops it with SIGTERM and starts it again.
  **Done when** after a restart `/api/Challenges` shows zero solves, the
  restart takes under 30 s, and the paths Juice Shop writes to on start are
  listed (they decide the volumes and tmpfs of T1.1-38).

- [x] **T1.1-04 Dropping privileges in Go.** (§3.1) In the same spike: start
  as root with only `SETUID` and `SETGID`, bind :80, start the child as 65532,
  then `syscall.Setuid(10001)` / `Setgid` for the program itself, under
  `no-new-privileges`.
  **Done when** `/proc/<pid>/status` shows the two uids, port 80 still answers,
  and from a shell as 65532 the program's files (0700, uid 10001) cannot be
  read and the program cannot be signalled.

---

## Phase A — structure (no change in behaviour)

- [x] **T1.1-05 New layout.** (§7) Move the packages into
  `internal/{node,central,shared,juiceshop}`, the templates into
  `web/{node,central,static}`, the Dockerfiles and test setup into
  `deploy/{docker,compose}`, the documents into `docs/`. The three 1.0
  programs in `cmd/` stay as they are until Phase E merges two of them.
  Imports rewritten, nothing else.
  **Done when** `scripts/test.sh` and `tests/e2e.sh` pass unchanged.

- [x] **T1.1-06 Makefile and changelog.** `make test`, `make e2e`,
  `make images`, `make run`; `CHANGELOG.md` with 1.0.0 and an open 1.1.0.
  **Done when** each target works from a clean checkout.

- [x] **T1.1-07 Version from git.** Images built with
  `VERSION=$(git describe --tags --always --dirty)`; the binaries print it
  with `-version` and in their first log line.
  **Done when** a build from the branch prints `v1.0.0-<n>-g<hash>`.

---

## Phase B — protocol v2

- [x] **T1.1-08 Types.** (§2.2) `internal/shared/protocol`: join request and
  answer, report and answer, leave, commands (`forget`), standing, game state.
  **Done when** the examples of §2.2 round-trip through JSON unchanged.

- [x] **T1.1-09 Validation.** Protocol version, name rules (1.0's
  `names.Normalize`), join-code alphabet and length, node uid format, at most
  500 solves, solve keys non-empty, cheat score 0–1.
  **Done when** each rule rejects its bad case in a table test, and the valid
  examples pass.

- [x] **T1.1-10 Join codes and tokens.** Generate a join code (10 characters
  from `abcdefghjkmnpqrstuvwxyz23456789`, unbiased), a participant token
  (`ctfp_` + 64 hex), and their stored form (SHA-256); constant-time compare.
  **Done when** 10 000 generated codes use only the alphabet with no
  character more than 5 % off its expected share, and a hash never equals its
  input.

---

## Phase C — the central server leads

- [x] **T1.1-11 Migration: games and participants.** (§5.1, §8) Tables
  `games`; `participants` with `game_id`, `token_hash`, `node_uid`, `left_at`;
  `commands` pointing at a participant; existing participants moved into a
  closed game "1.0", **with `ended_at` cleared** so the lab's students come
  back; `instances` dropped.
  **Done when** a copy of the lab's database migrates, every student of the lab
  is on the "1.0" board with their solves, and migrating twice is a no-op.

- [x] **T1.1-12 Migration: events.** (§5.4) Table `events` (time, kind, game,
  participant, address, text) with an index on time; retention setting.
  **Done when** events older than the retention are deleted by a daily job,
  tested with an injected clock.

- [x] **T1.1-13 Games in the store.** (§2) Create, edit, state transitions
  `draft → open → running → closed`, time window, scoring per game (default
  1.0's table), board options, slug unique.
  **Done when** every allowed transition works, every other one is refused,
  and two games with the same name get different slugs.

- [x] **T1.1-14 `POST /api/v2/join`.** (§2.2) Finds the game by code hash,
  refuses `draft` and `closed` (409), creates the participant, returns id,
  token and game; stores only the token hash.
  **Done when** handler tests cover 201, unknown code (403), closed game (409),
  invalid name (400), and the token works for a report afterwards.

- [x] **T1.1-15 Join limits.** (§5.6) Per join code (300 per minute) and per
  wrong code per address (slowing guessing), never per address alone.
  **Done when** 30 joins from one address within a minute all succeed, and the
  21st wrong code from one address in a minute is 429.

- [x] **T1.1-16 `POST /api/v2/report`.** (§2.2, §5.1) Bearer token → the
  participant; merge solves (first time kept, never removed, future times
  clamped as in 1.0); store the **time of receipt** of each new solve; no
  solves counted after the game's end; answer with standing, game state and
  pending commands. **No rule ends another participant.**
  **Done when** five participants of one game report in any order and all five
  stay on the board — the lab's F1 as a test — and a solve reported after the
  end is stored but not counted.

- [x] **T1.1-17 Commands per participant.** (§2.2) Queue `forget`, deliver on
  every report until confirmed in `commands_applied`, confirm once.
  **Done when** a `forget` for one participant reaches only that participant
  in a game of five, and stops being sent once confirmed.

- [x] **T1.1-18 `POST /api/v2/leave`.** (§4.4) Marks the participant `left`,
  off the board, token disabled; writes an event `left (forget)`.
  **Done when** the token is 401 afterwards and the participant shows in admin
  as left, not on the board.

- [x] **T1.1-19 The 1.0 endpoint.** A 1.0 agent reporting to
  `/api/v1/report` gets `410 Gone` with a message, and an event says which
  address still runs 1.0.
  **Done when** a test with the 1.0 example report gets 410 and one event.

- [x] **T1.1-20 Standings per game.** (§6) Rank by score, ties by the time the
  score was reached; hidden and left participants excluded; first blood per
  challenge by time of receipt.
  **Done when** two games with three participants each rank separately, and a
  solve with an earlier VM time but a later receipt does not take first blood.

- [x] **T1.1-21 Events written.** (§5.4) Joins (accepted, refused + reason),
  reports (collapsed: one per participant per minute unless something
  changed), solves with cheat score, rejected requests (401, bad body,
  protocol) with the address, commands, leaves.
  **Done when** a scripted run produces exactly the expected events, in order.

- [x] **T1.1-22 Flags.** (§5.5) Each as its own small check, raised as an
  event and shown next to the participant: prerequisite missing (table of
  dependent challenges), more than N solves in M seconds, solve after the end
  or before the join, one token from two Juice Shop boots in alternation, cheat
  score above a threshold.
  **Done when** each flag fires on its case, and none fires on the honest
  reports of the e2e run (T1.1-55).

- [x] **T1.1-23 Remove `CTF_SEED` and instances.** Configuration of the central
  server is the database file and the listen address only.
  **Done when** `cmd/ctf-central` reads no other environment variable and its
  `main.go` header says so.

---

## Phase D — admin

- [x] **T1.1-24 Games pages.** (§5.2) List, create, edit, open/close; the join
  code shown once per regeneration with a *regenerate* button.
  **Done when** after leaving the page the code cannot be shown again, only
  regenerated, and every form has a CSRF token.

- [x] **T1.1-25 Game page.** (§5.2) Participants with last report, node
  version, score, flags, state (active / hidden / left), link to the board.
  **Done when** a test game with five participants in the three states renders
  each correctly.

- [x] **T1.1-26 Cleaning up.** (§5.3) Remove participant (+ `forget`), hide,
  end game, clear game (+ `forget` to all), delete game; destructive ones
  confirmed by typing the game's name; each written to `events`.
  **Done when** a wrong name refuses the action, *clear game* leaves other
  games untouched, and every action leaves exactly one event.

- [x] **T1.1-27 Event feed.** (§5.4) Newest first, filters by game,
  participant and kind, 10-second refresh, CSV export.
  **Done when** each filter narrows a seeded feed correctly and the CSV is safe
  against formula injection (1.0's `csvSafe`).

---

## Phase E — the node (`ctf-node`)

- [x] **T1.1-28 `node.db`.** (§4.1) Tables `membership`, `solves`, `codes`,
  `baseline`, `outbox`, `standing`; WAL; numbered migrations; file mode 0600 in
  a 0700 directory.
  **Done when** a fresh database migrates, migrating twice is a no-op, and the
  modes are checked by the test.

- [x] **T1.1-29 Import 1.0 state.** (§8) On first start, `state.json` if
  present: solves and continue codes in, registration **not**; the file
  renamed `state.json.imported`.
  **Done when** a 1.0 state's solves come back and no membership exists.

- [x] **T1.1-30 Supervisor.** (§3.1) Start Juice Shop as a child (uid 65532),
  restart it when it dies (with back-off), `Restart()` and `Stop()` for the
  buttons, stop it cleanly on SIGTERM.
  **Done when** with a fake child: a crash is restarted, three crashes in a row
  back off, SIGTERM stops the child before the program exits.

- [x] **T1.1-31 Privileges.** (§3.1, T1.1-04) Bind :80 as root, start the
  child, drop to 10001 before serving.
  **Done when** a test in the container (T1.1-38) shows the uids, and the
  program refuses to start if it cannot drop.

- [x] **T1.1-32 Reverse proxy.** (§3.1) `/juiceshop/` → `127.0.0.1:3000` with
  the prefix stripped and put back on redirects, `/assets/` unstripped,
  WebSocket upgrades passed; `…/continue-code*/apply…` refused with 403,
  case-insensitive.
  **Done when** tests against an `httptest` backend cover a redirect,
  a WebSocket echo, and the four spellings of the continue-code path from 1.0's
  e2e.

- [x] **T1.1-33 Watcher on the database.** (DESIGN.md §6) 1.0's polling,
  webhook, solve detection, boot id, restore and baseline, moved onto
  `node.db`; the webhook listens on loopback only.
  **Done when** 1.0's agent tests pass against the new store.

- [x] **T1.1-34 Join.** (§2.2, §4.1) Name + code from the page → `/api/v2/join`;
  id and token stored, code not; the node uid made at this moment.
  **Done when** after a join `node.db` holds the token and no trace of the
  code (checked by searching the file), and a 403 from the central server shows
  "unknown join code" on the page.

- [x] **T1.1-35 Offline join.** (§4.3) Central unreachable: the join goes to
  the outbox, the student plays; retried with back-off; a later 403 asks for the
  code again and keeps the solves.
  **Done when** with the central server down a student joins and solves, and
  after it returns the solves arrive with their original times.

- [x] **T1.1-36 Reporter v2.** (§2.2) Full snapshot with the token, within 2 s
  of a change and every 60 s; back-off as 1.0; standing stored; `forget`
  applied once.
  **Done when** 1.0's reporter tests pass in their v2 form, and a 401 (token
  removed by the operator) stops reporting and shows the join form again.

- [x] **T1.1-37 Central-less mode.** (§4.3) No `CTF_CENTRAL_URL`: no join to
  send, the page shows the local score by the scoring table, nothing is
  reported.
  **Done when** the node runs a full session without any outbound request
  (checked with a test transport that fails on any call).

- [x] **T1.1-37a Another central server.** (§4.3) The student page shows the
  central server in use and lets it be changed (another URL, the default, or
  none) while the VM has not joined; stored in `node.db`; refused while
  joined.
  **Done when** a node built with one central server's URL joins a game on another central
  server after the address is changed on its page, and the form is refused
  while joined.

- [x] **T1.1-38 The `juiceshop-ctf` image.** (§3.1) `FROM
  bkimminich/juice-shop:v20.3.0`, ctf-node as entrypoint, `/var/lib/ctf` as the
  volume, tmpfs for the paths T1.1-03 found; healthcheck through the binary.
  **Done when** `docker run` as in §3.1 serves `/ctf/` and `/juiceshop/`, and
  the image is the Juice Shop image plus less than 20 MB.

- [x] **T1.1-39 Isolation inside the container.** (§3.1) From a process
  running as 65532 in the container: read `/var/lib/ctf/node.db`, signal
  ctf-node, connect to the webhook from outside loopback.
  **Done when** all three fail, and killing the Juice Shop process makes
  ctf-node start it again with the progress restored.

---

## Phase E2 — the student's page

- [x] **T1.1-40 Page.** (§4.2) `/ctf/`: join form when not joined; otherwise
  name, game, score, rank (from the last standing), solved per category, link
  to the board, and the two buttons. No gate.
  **Done when** with every cookie deleted the page and the shop behave exactly
  the same.

- [x] **T1.1-41 Restart Juice Shop.** (§4.4) Button → `Restart()`, then the
  restore of DESIGN.md §6.2.
  **Done when** after the button the shop has its solves back within 60 s.

- [x] **T1.1-42 Forget.** (§4.4) Confirmation by typing `forget`, CSRF, then:
  `/api/v2/leave` if reachable, every table emptied, Juice Shop restarted with
  nothing to restore.
  **Done when** `node.db` has no row left in any student table (checked table
  by table), the shop shows zero solves, the central participant is *left*;
  with the central server down it still empties the VM; and the operator's
  *remove participant* (T1.1-26) has the same effect on the node.

- [x] **T1.1-43 Optional gate.** (§4.2) `CTF_GATE=session`: server-side
  sessions in `node.db`, a personal return code at join, re-entry with name +
  return code, 5 wrong codes per student per 5 minutes.
  **Done when** a student with deleted cookies is back in with name + return
  code, and with the gate off nothing of it is active.

---

## Phase F — the leaderboard

All pages server-rendered, small script, no external assets; CSP unchanged.

- [x] **T1.1-44 Board data.** (§6) Per-game JSON: rows with category shares,
  podium, ticker with first blood, score-over-time series for the top 10.
  **Done when** a seeded game gives the expected JSON in a golden-file test.

- [x] **T1.1-45 Main board.** (§6) `/g/<slug>/`: podium, rows with category
  bar, rank-change animation, flash on a new score.
  **Done when** the page renders a seeded game, names are escaped, and the
  animation is off with `prefers-reduced-motion`.

- [x] **T1.1-46 Ticker and first blood.** (§6, D10) Newest solves, first blood
  marked, no points for it.
  **Done when** the mark follows T1.1-20's rule and the score is unaffected.

- [x] **T1.1-47 Score over time.** (§6) Inline SVG, one line per top-10
  participant, labelled, readable in light and dark.
  **Done when** a seeded game draws the expected polylines (golden file).

- [x] **T1.1-48 Participant page.** (§6) Categories, solve timeline, first
  bloods, difficulty spread.
  **Done when** every link from the board resolves.

- [x] **T1.1-49 Challenge and category pages.** (§6) Who solved a challenge, in
  order, when; the challenge matrix of a category.
  **Done when** every link resolves and a challenge nobody solved says so.

- [x] **T1.1-50 Beamer mode.** (§6) Full screen, large type, countdown to the
  end, ticker at the bottom, final-result view when the game closes.
  **Done when** a game in each state (running, ended) renders its view.

- [x] **T1.1-51 Board options.** (§2) Hidden board, initials instead of names.
  **Done when** a hidden game's board is 404 and initials show nowhere a full
  name.

---

## Phase G — test setup and end-to-end

- [x] **T1.1-52 Test setup.** (§7) `deploy/compose`: central server plus three
  `juiceshop-ctf` containers as three students' VMs, each with a small
  container on its network playing Kali (curl); `start.sh` / `stop.sh`.
  **Done when** `start.sh` brings it up healthy and prints the addresses.

- [x] **T1.1-53 e2e: games and joins.** Two games; three nodes join, two in
  one game, one in the other; solves on each; all three on their game's board.
  **Done when** it passes, and every student stays on the board throughout.

- [x] **T1.1-54 e2e: the rest.** Cookie wipe changes nothing; Restart Juice
  Shop restores; Forget empties; offline join arrives later; remove
  participant; clear game; a continue code from another node refused; a copied
  node flagged.
  **Done when** it passes twice in a row.

- [x] **T1.1-55 e2e: no false flags.** The honest runs of T1.1-53 and -54
  raise no flag (T1.1-22).
  **Done when** the event feed after the run holds no flag event.

---

## Phase H — documents

- [x] **T1.1-56 Docs.** `docs/PROTOCOL.md` (v2 as built), `docs/OPERATIONS.md`
  (making a game, the lab, exporting the VM with *Forget* first, cleaning up),
  `DESIGN.md` updated where 1.1 changed it, README and CHANGELOG.
  **Done when** a colleague can run a lab from OPERATIONS.md alone.

- [x] **T1.1-57 Student sheet.** `deploy/vm/`: importing the VM next
  to Kali, the two adapters (NAT and host-only, nothing to configure), and
  that finding the CTF VM is their first task.
  **Done when** it fits on one page.

---

## Phase I — deployment

These change the deployment, which lives outside this repository. Nothing
reaches a server without the operator's explicit go.

- [x] **T1.1-58 The central server.** Deployed without `CTF_SEED`, the image
  from 1.1, `central.env` and the script that made its seed removed; vhost
  and backup unchanged.
  **Done when** the deployment's own checks pass.

- [x] **T1.1-59 The CTF VM.** `juiceshop-ctf` and `logging-proxy` only (D9),
  no secrets, no `CTF_INSTANCE_ID`, no gate.
  **Done when** the node's end-to-end test, adapted to 1.1, passes.

- [x] **T1.1-60 The VM image.** *(Built by `make vm` instead: never joined, so
  no Forget before export; `deploy/vm/test.sh` boots both exported disks.)* Install the node in VirtualBox from the build,
  join a test game, *Forget*, export the OVA, import it twice.
  **Done when** the two imported VMs join as two participants and neither has
  a trace of the test join.
