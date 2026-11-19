# Developing the Juice Shop CTF

For a developer who is new to this repository. After reading it you should
know how the pieces fit together, where to find the code for any part of the
behaviour, how to make a change and check it, and which rules you can break
without noticing. It does not repeat the design: it points to where the design
is written down.

| If you want… | read |
|--------------|------|
| what the CTF is, from the outside | [README.md](README.md) |
| how an operator runs a lab with it (the behaviour you must keep) | [docs/OPERATIONS.md](docs/OPERATIONS.md) |
| why 1.1 is built the way it is | [docs/VERSION-1-1.md](docs/VERSION-1-1.md), sections cited as `§n` in the code |
| the wire format between VM and central server | [docs/PROTOCOL.md](docs/PROTOCOL.md) |
| the 1.0 reasoning, still valid where 1.1 does not say otherwise | [docs/DESIGN.md](docs/DESIGN.md) |
| how 1.1 was built, task by task (`T1.1-nn` in comments) | [docs/TASK-1-1.md](docs/TASK-1-1.md) |

Read the README and OPERATIONS.md first (twenty minutes); open the other
documents when a comment sends you there.

## 1. The system in one picture

```
 student's laptop (VirtualBox)                                           neo.jmnl.nl
 ┌──────┐  host-only   ┌─ CTF VM: one container, juiceshop-ctf ──────┐  NAT   ┌──────────────┐
 │ Kali │ ───────────► │ :80  ctf-node -serve (uid 10001, no caps)   │ ─────► │ ctf-central  │
 └──────┘              │       /ctf/        the student's page       │ HTTPS  │ SQLite       │
                       │       /juiceshop/  proxy ──► Juice Shop     │        │ boards, admin│
                       │                    (uid 65532, :3000)       │        └──────────────┘
                       │  ctf-node, the keeper (PID 1, root,         │
                       │  SETUID+SETGID only): starts the two above  │
                       └─────────────────────────────────────────────┘
```

There are **two programs**, both in Go, in one module:

- **`ctf-node`** runs on every student's VM, inside the Juice Shop image. The
  binary plays three parts. As root it is the **keeper**: PID 1, it binds
  :80 and starts Juice Shop and itself as `-serve`. As **`-serve`** it is the
  unprivileged part that does all the work. As **`-signal`** it is a tiny
  helper the keeper uses to stop Juice Shop.
- **`ctf-central`** runs once, on neo. It holds the games and their
  participants, receives the reports, and serves the boards and the admin.

They talk only through three requests, all from the VM to the central server:
`join`, `report` and `leave` (protocol v2). The central server never connects
to a VM. Anything it wants a VM to do, such as `forget`, travels back in the
answer to the VM's next report.

What happens when a student plays:

1. On the page `/ctf/`, the student types a name and the game's **join
   code**. ctf-node posts them to `/api/v2/join` and gets back a **token**
   for that participant. The token is stored in `node.db`. The join code is
   not stored, unless the central server is unreachable: then it waits in
   the outbox until the join gets through.
2. The **watcher** polls Juice Shop's `/api/Challenges` and receives Juice
   Shop's solve webhook. It records the first time each challenge was seen
   solved, and the continue codes.
3. The **reporter** posts a complete snapshot of the solves to
   `/api/v2/report` every minute, and sooner after a solve.
4. The central server scores each solve from **its own embedded copy** of the
   challenge catalogue, stores the time it received the solve, raises flags,
   and answers with the student's standing and any commands.
5. When Juice Shop restarts (it forgets everything on a restart), the
   watcher sees a new boot time and applies the stored continue codes, so the
   progress comes back.

## 2. Getting started

You need Go 1.26 for the unit tests. For the image, the test setup and the
end-to-end tests you also need Docker with compose, and python3.

```sh
git clone https://github.com/macsnoeren/juiceshop-ctf.git && cd juiceshop-ctf
make test         # gofmt, go vet, all unit tests with -race. No Docker. ~1 min
make run          # the central server and three VMs with their "Kalis", in Docker
make stop         # deploy/compose/stop.sh; `deploy/compose/stop.sh --wipe` also drops the data
```

After `make run`:

- admin is at `http://127.0.0.1:8090/juiceshop-ctf/admin/`;
- the VMs are at `http://127.0.0.1:8091/ctf/` (and 8092, 8093);
- `http://127.0.0.1:8091/juiceshop/ftp/acquisitions.md` solves a challenge.

To run both programs **without Docker**, for a quick loop on a page or a
handler:

```sh
CTF_DB_PATH=/tmp/central.db CTF_ADDR=127.0.0.1:8080 CTF_BASE_PATH=/juiceshop-ctf \
  go run ./cmd/ctf-central

mkdir -p /tmp/ctf-node
CTF_DATA_DIR=/tmp/ctf-node CTF_ADDR=127.0.0.1:8091 \
  CTF_CENTRAL_URL=http://127.0.0.1:8080/juiceshop-ctf JUICESHOP_URL=http://127.0.0.1:3000 \
  go run ./cmd/ctf-node
```

Started as a normal user, ctf-node skips the keeper and runs `-serve`
directly. It logs that *Restart Juice Shop* is then unavailable. For the solves
it needs a Juice Shop on `JUICESHOP_URL`. For example:

```sh
docker run --rm --network host -e BASE_PATH=/juiceshop \
  -e SOLUTIONS_WEBHOOK=http://127.0.0.1:8081/webhook bkimminich/juice-shop:v20.3.0
```

The webhook listener is always `127.0.0.1:8081`. Each `main.go` documents
every environment variable it reads at the top of the file. Nothing in either
program's environment is a secret.

## 3. Where things are

```
cmd/ctf-node/main.go         the three parts of the node binary, its environment
cmd/ctf-central/main.go      the central server's entry point, its environment, the daily prune

internal/shared/protocol     protocol v2: the request and answer types, validation, join codes and tokens
internal/shared/names        the one rule for display names (both sides use it)
internal/shared/httpx        logger, server with timeouts, the security headers and CSP
internal/shared/config       reading the environment, collecting every error at once
internal/shared/health       the -healthcheck flag of both binaries
internal/shared/version      the version string, set by -ldflags from `git describe`

internal/juiceshop           the Juice Shop client, and the embedded catalogue of the pinned version
internal/juiceshop/testdata  real answers captured from Juice Shop 20.3.0 (README.md says how)

internal/node                ctf-node -serve
  node.go                      Node: join, Forget, Restart, the choice of central server
  watcher.go                   polling Juice Shop, the webhook, restoring after a restart
  central.go                   the reporter: join, report, leave, the outbox, back-off, commands
  page.go                      the student's page /ctf/ and its forms, the optional gate
  proxy.go                     /juiceshop/ to Juice Shop, refusing continue-code apply
  store.go                     node.db: schema, migrations, Wipe
  state.go                     the in-memory State and the membership statuses
internal/node/keeper         the root part: supervisor.go (starting and stopping Juice Shop),
                             proc.go (uids, capabilities, process groups), keeper.go (the control pipe)

internal/central             ctf-central's HTTP side
  server.go                    routes, join/report/leave handlers, settings
  limit.go                     the join rate limits
  board.go, chart.go           the public boards per game, the score-over-time SVG
  admin.go                     /admin/: games, participants, events, settings, CSV
  auth.go                      the admin login: setup page, login/logout, sessions, users page
  password.go                  password hashing (PBKDF2-SHA256, standard library)
internal/central/store       ctf-central's SQLite database
  migrations.go                the schema, as an append-only list
  games.go, participants.go    games, join codes, participants, remove/clear/delete
  report.go                    processing a report: scoring, first time kept, the FLAGS
  board.go                     the queries behind the boards
  events.go                    the event feed, settings, retention
  users.go                     admin accounts and their sessions

web/central/*.html, chart.svg  the central server's templates (embedded); layout.html holds the
                               shared parts (bar, mark, difficulty star, board rows, ticker)
web/node/page.html             the student's page (embedded)
web/static/                    style.css (the one stylesheet, Juice Shop's theme), board.js, admin.js:
                               served by both programs

deploy/docker/                 Dockerfile.node (FROM Juice Shop), Dockerfile.central (FROM scratch)
deploy/compose/                the local test setup: start.sh, stop.sh, docker-compose.yml
deploy/vm/             the students' VM: Packer + QEMU in Docker → VirtualBox OVA and VMware .vmx
                               (build.sh, juiceshop-ctf.pkr.hcl, provision.sh, package.sh, test.sh);
                               and the students' sheet

tests/e2e.sh                   the whole CTF in Docker, end to end
tests/image.sh                 the image on its own: users, isolation, respawn
scripts/test.sh                what `make test` runs
```

**Finding the code for a behaviour.** Comments cite the design: `§4.3` is a
section of VERSION-1-1.md, `T1.1-37a` a task in TASK-1-1.md, `D3` a decision
in VERSION-1-1.md, and `F1` a finding from the first lab. Searching for the
reference finds the code that implements it:

```sh
grep -rn 'T1.1-37a\|§4.3' --include=*.go .
```

## 4. Rules that are not obvious

These are what a reasonable change can break without any compiler or reviewer
noticing. Each one has a reason in the code next to it.

**The protocol**
- `internal/shared/protocol` is the **only** definition of the wire format.
  Both programs compile against it, so they cannot disagree about a field.
  A change there is a change to [docs/PROTOCOL.md](docs/PROTOCOL.md) too.
- **VMs in the field are never updated.** A new version is a new image that
  thirty students must import again, so a newer central server will be talking
  to older VMs. Add fields; do not rename or remove them. Unknown command
  types are ignored by design, so a newer server can add commands without
  breaking older VMs. When a change truly cannot be compatible, it needs a new
  protocol version, and the old endpoint answers with something clear, the way
  `/api/v1/report` answers 410 and writes an event.
- **A report is the student's word.** The token lives on a VM the student
  controls, so anything in a report can be forged. That is why the central
  server keeps its own catalogue, decides points itself, stores the time it
  *received* a solve (which decides first blood and ties), and clamps solve
  times that lie in the future. Never take a value from a report that affects
  other participants.

**The central database**
- `migrations.go` is **append-only**. A released migration is never edited: a
  database that already has it would never run the edit, and migration 1 is in
  every database the 1.0 lab left behind. Add migration *n+1*. Migrations run
  with foreign keys off and a `foreign_key_check` before each commit (see
  `migrate` in `store.go`).
- Times are stored as fixed-width UTC text, so sorting the text sorts the
  times. Every store method that depends on the time **takes it as an
  argument**. Never call `time.Now()` inside the store: the tests move the
  clock.
- The database has one connection, on purpose: writes are serialised and
  `SQLITE_BUSY` cannot happen. Keep a transaction short and never make an
  HTTP call while holding one.
- **Configuration lives in the database**: games, scoring, the title, the
  time zone and the retention, all changed in admin. The environment says only
  where the database is and where to listen. Do not add operator settings as
  environment variables.
- **Flags are shown, never acted on** (decision D5). A new flag must stay
  silent for honest play: the end-to-end test checks that a full honest run
  raises none (T1.1-55).

**The node**
- **ctf-node -serve has no privilege at all**, and refuses to start if it has
  any. All privilege sits in the keeper, which reads no network input. The
  only thing it accepts, over the control pipe on fd 4, is `restart`. If a
  feature seems to need root, it goes through that pipe as a new, narrow
  command, never by giving `-serve` a capability.
- Juice Shop runs as a different user (65532) and **must not be able to read
  `node.db`** (0600, in a 0700 directory, uid 10001). The students attack Juice
  Shop, and some of its challenges give them code execution as its user.
  `tests/image.sh` checks this from inside the container.
- **The join code is stored only in the outbox**, and only until the join is
  answered. The token is stored only in `node.db`. Neither is ever logged,
  shown on a page or put into an error message.
- **Forget wipes every table in `studentTables`** (`store.go`). A new table
  that holds anything about the student goes into that list, and into the
  table list in `tests/image.sh`, which checks that Forget really leaves
  nothing behind.
- The proxy **refuses Juice Shop's continue-code apply**. Without that, a
  student could copy a classmate's progress. A change to the proxy's paths is
  checked against the regexp in `proxy.go` and against `proxy_test.go`.
- `node.db` has its own migrations (in `store.go`), with the same rule:
  append only.

**The web pages**
- **No inline script or style anywhere.** The Content-Security-Policy in
  `httpx.SecurityHeaders` forbids both, so an inline `<script>` or `style=""`
  silently does nothing in the browser. Put code in `web/static/*.js` and
  styles in `style.css`. `chart.svg` is its own document with its own CSP.
- Templates are **embedded** (`//go:embed`), so a template change needs a
  rebuild. Use `html/template` only; never build HTML in strings.
- **The look is Juice Shop's own**: its default theme (`bluegrey-lightgreen`),
  dark only, with the colours measured from Juice Shop 20.3.0's
  `styles.css` and named at the top of `style.css`. Keep new pages in it: the
  bar (`appbar`), cards, pill buttons (`.button`, `.secondary`, `.danger`),
  chips, and the blue difficulty star (`{{template "diff" n}}`). Navigation
  and actions are **buttons**; plain links are for names inside tables and
  lists. The CTF's mark is our own drawing, not Juice Shop's logo.
- **`board.js` redraws the board** every ten seconds, so the markup it builds
  for the rows, the podium and the ticker must stay the same as the
  `rows`, `podium` and `ticker` templates in `layout.html`. Change one, change
  the other.
- **Every admin route is wrapped**: a GET in `s.requireUser`, a POST in
  `s.action` (the CSRF token, then the login). A route registered without
  one is open to anybody who can reach the server; `TestAdminNeedsLogin`
  lists the pages it checks, so add a new page there. Admin actions are
  written with `s.admin(r, …)`, which records the account and address.
- **Passwords and sessions are never stored as typed.** Passwords go through
  `hashPassword` (PBKDF2-SHA256, 600 000 iterations; the tests lower it in
  `auth_test.go`), session cookies are stored as their SHA-256. Neither goes
  into an event or a log line.
- **The setup page is open while there is no account.** That is by design
  (the first admin of a fresh server); on neo nginx's allow-list keeps it to
  the operator. Do not add a way to reach "no accounts" again on a running
  server, such as deleting the last user.
- Every POST on the student's page goes through its CSRF wrapper (`s.form`).
- Every page is **complete without JavaScript**: `board.js` and `admin.js`
  only add live refresh and animation, and honour `prefers-reduced-motion`.
- **Nothing depends on a cookie surviving** (T1.1-54). Students wipe cookies
  while attacking Juice Shop, so the student's page keeps its state in
  `node.db`, never in the browser. The only cookie it sets is the CSRF one,
  which is simply made again.

**Docker and the test setup**
- The test setup's compose project is called `juiceshop-ctf-lab`, and
  `tests/e2e.sh` uses `juiceshop-ctf-e2e` and other ports. **Do not rename
  them** to something another setup on your machine uses: `stop.sh --wipe`
  removes the containers it considers orphans, and deletes volumes.
- The node image must stay within **20 MB of Juice Shop's own**
  (`tests/image.sh` checks it). That is why ctf-node does not embed tzdata,
  while ctf-central (`FROM scratch`) does.

## 5. Making common changes

Each recipe lists the places that must change together. Run `make test` after
every step.

**A new field in the central database**
1. Append a migration to `internal/central/store/migrations.go`.
2. Read and write it in the store file it belongs to (`games.go`,
   `participants.go`, …).
3. Add a test in `store_test.go` on a fresh database. If the change touches
   data that came from 1.0, extend `TestMigrationBringsTheLabBack`, which
   migrates a reconstructed 1.0 lab database.

**A change to what the VM reports or receives**
1. Change `protocol.go`, and `validate.go` if the field has limits.
2. Update the round-trip examples in `protocol_test.go`, and the same examples
   in [docs/PROTOCOL.md](docs/PROTOCOL.md).
3. Change the server side (`central/server.go`, `store/report.go`) and the
   node side (`node/central.go`).
4. Ask what an **older VM** sends, and make sure the server still accepts it
   (§4 above).

**A new or changed page on the board**
1. Add a template in `web/central/`, a route and a handler in
   `central/board.go`, and the query in `store/board.go`.
2. `server_test.go` crawls every link from the board and fails on a dead one.
   If the change alters `board.json` or the chart, accept the new golden files
   on purpose, then look at the diff:
   ```sh
   go test ./internal/central -run Golden -update && git diff internal/central/testdata
   ```

**An admin action**
1. Add the action in `handleGameAction` or `handleParticipantAction`
   (`admin.go`), and the store method behind it. A new route is registered
   with `s.action(…)` (POST) or `s.requireUser(…)` (GET).
2. Write an event with `s.admin(r, …)`, so the action shows in the feed
   with who did it.
3. A destructive action asks for the game's name, as end, clear and delete do.
4. If the VM must act on it, queue a command by token hash, the way `remove`
   queues `forget`.
5. Update the table in [docs/OPERATIONS.md](docs/OPERATIONS.md) §3.

**The student's page**
1. Edit `web/node/page.html` and `node/page.go`. The logic goes into `node.go`
   or `central.go`, not into the handler.
2. Test it with the harness in `node/harness_test.go`. It runs a real
   ctf-central in-process and a fake Juice Shop whose continue codes really
   encode its solves. `h.submit("/ctf/join", form)` posts a form the way the
   browser does, CSRF included.
3. If students will see the change, update
   [deploy/vm/STUDENT.md](deploy/vm/STUDENT.md).

**A new flag**
1. Add a constant and the check in `store/report.go` (`solveFlags`, or in the
   report itself for flags such as `two_boots`).
2. Write a store test that raises it, and one that does not raise it for
   honest play.
3. Add a row to the flags table in [docs/OPERATIONS.md](docs/OPERATIONS.md).
4. Run `make e2e`: its *no false flags* step must still pass.

**A new environment variable**
1. Read it in `main.go` through `config.Env`, which collects every error so
   the log names all of them at once.
2. Document it in the comment at the top of that `main.go`.
3. If it belongs in the test setup, set it in `deploy/compose/docker-compose.yml`.
4. It is never a secret, and never something the operator should be able to
   change per game. That goes in the database (§4 above).

**Upgrading Juice Shop**
1. Capture the fixtures again, following
   [internal/juiceshop/testdata/README.md](internal/juiceshop/testdata/README.md),
   and rebuild the catalogue from them (the command is in
   `internal/juiceshop/catalogue.go`).
2. Change the `FROM` tag in `deploy/docker/Dockerfile.node` and
   `CatalogueVersion`.
3. Run `make test`; a failing client test shows what Juice Shop changed.
4. Run `bash tests/image.sh` (users, size, respawn) and `make e2e`.
5. Look at the new Juice Shop next to the board: if its theme changed, the
   colours at the top of `web/static/style.css` and in `chart.svg` follow.
5. Keep the old catalogue's keys in mind: a game that is running still holds
   solves under them.

## 6. Testing

Three levels, from fast to thorough:

| | Needs | Time | Run it |
|-|-------|------|--------|
| `make test` (`scripts/test.sh`) | Go | ~1 min | before every commit |
| `bash tests/image.sh` | Docker | ~2 min | after touching the keeper, the Dockerfile, users, files, or Forget |
| `make e2e` (`tests/e2e.sh`) | Docker, python3 | ~5 min | before a merge or a release; after any change to protocol, flags, Forget, remove or clear |
| `make vm` (`deploy/vm/build.sh`) | Docker, `/dev/kvm` | ~6 min | after touching `deploy/vm/` or the node image; it boots the exported disks and checks them |

- **The unit tests use real components wherever they can**: SQLite on a temp
  file, the real central server in-process behind the node tests, and
  `httptest` servers that answer with Juice Shop's real captured responses.
  The clock is injected everywhere (`SetClock`), so nothing sleeps for long.
- **The golden files** in `internal/central/testdata` pin the board's JSON
  and the chart. Never update them with `-update` without reading the diff.
- **`tests/e2e.sh`** runs the compose setup under its own project name and
  ports, so it never touches a `make run` you have open. Each student's
  requests go from that student's "Kali" container to their own VM, as in
  the lab. On failure it names the step and prints the logs.
  `E2E_KEEP=1 bash tests/e2e.sh` leaves the setup running for a look.
- **`tests/image.sh`** checks, from inside the real container, what no unit
  test can:
  - the uid and capabilities of each process;
  - that code running as Juice Shop's user cannot read `node.db` or signal
    ctf-node;
  - that a killed Juice Shop comes back with its progress;
  - that the container refuses to start with the wrong privileges.
- `go test -race` is always on. The node's watcher, reporter and page share
  state; a race is a bug even when the test passes.

## 7. Workflow and style

- **Branches and versions.** `main` holds the released version. A version is
  developed on its own branch (`v1.1`) and tagged `vX.Y.Z` when released. The
  version in the binaries comes from `git describe --tags --dirty`, so an image
  built from uncommitted work calls itself `…-dirty`, and admin shows each VM's
  version.
- **Releasing** is pushing a tag; GitHub Actions does the rest
  ([.github/workflows/release.yml](.github/workflows/release.yml)):
  1. In [CHANGELOG.md](CHANGELOG.md), replace `## 1.1.0 — unreleased` by the
     date, and commit that on `main`.
  2. `make release-check V=v1.1.0` — the same check the workflow starts with
     (`scripts/release-notes.sh`): a version `vX.Y.Z[-pre]` and a dated
     CHANGELOG section, which becomes the release notes.
  3. `git tag v1.1.0 && git push origin v1.1.0`. (Or *Actions → release →
     Run workflow* with the version: it tags the chosen branch itself.)

  The workflow tests (unit, image, end to end), pushes
  `macsnoeren/juiceshop-ctf-central:1.1.0`, `:1.1` and `:latest` (amd64 and
  arm64), builds the VM with KVM and attaches the OVA, the VMware zip and
  their checksums to the release, and only then publishes it. A failure
  leaves a draft: delete it and the tag, fix, tag again. `v1.2.0-rc.1` makes
  a pre-release, and Docker Hub then gets only `:1.2.0-rc.1`. The workflow
  needs the repository secrets `DH_USERNAME` and `DH_PASSWORD` (a Docker Hub
  access token), as guacamole-datadiode does.
- **Before a commit:** run `make test`. Add a line to
  [CHANGELOG.md](CHANGELOG.md) under the unreleased version. Update the
  document that describes what you changed: PROTOCOL.md, OPERATIONS.md,
  STUDENT.md, or the header comment of the file.
- **Commit subjects** are a plain sentence saying what changed and why
  ("Let the central server lead: games with join codes, …"), with no `feat:`
  prefixes. The body explains the reasoning.
- **Everything is in English**: code, comments, docs and commit messages.
- **Comments explain why**, in full sentences, and say what breaks if the line
  is changed. Emphasis is in CAPITALS; a warning starts with `>>>`. Cite the
  document that holds the full explanation (`§4.3`, `T1.1-31`) instead of
  repeating it. Every file starts with a comment that says what it is and
  what is unusual about it. Keep that comment true when you change the file.
- `gofmt` is enforced by `make test`. There are only two direct
  dependencies (`modernc.org/sqlite`, `golang.org/x/text`); adding a third
  needs a good reason, written in the commit.

## 8. How it reaches production

This repository builds the images; it does not deploy them. Deployment is
done by **jmnl-external-servers**, where this repository is the git submodule
`projects/juiceshop-ctf`:

- **The central server**: the image `juiceshop-ctf-central`, built from
  `deploy/docker/Dockerfile.central`, runs on the node `neo-jmnl-node` behind
  nginx at `https://neo.jmnl.nl/juiceshop-ctf/`.
- **The student's VM** is not deployed from there: `make vm` builds it here
  (Alpine Linux, Docker and the `juiceshop-ctf` image, by Packer under QEMU
  in a container) as a VirtualBox OVA and a VMware `.vmx`, which the
  operator hands out ([deploy/vm/README.md](deploy/vm/README.md)).

The build in jmnl-external-servers reads this submodule's **working tree**, not its pinned
commit. Changes go to neo only in a signed firmware bundle, which only the
operator builds and uploads. Before changing anything there, read that
repository's `CLAUDE.md` and README.

Two consequences for your changes:

- **Migrations run on neo's real database**, which still holds the first lab.
  Before a release that adds a migration, run the new ctf-central once on a
  copy of the latest backup of `ctf-central-db`.
- **A VM change reaches students only through a new OVA**, so keep a central
  server change compatible with the VMs already out there (§4).

## 9. Words used in the code

| Word | Means |
|------|-------|
| game | one CTF session, made in admin: state (draft → open → running → closed), optional start and end, scoring, a board at `/g/<slug>/` |
| join code | 10 characters that let a VM join a game; shown once, stored as a SHA-256 hash |
| participant | a student in a game, created by a join; identified by its **token** (`ctfp_` + 64 hex), stored as a SHA-256 hash |
| node uid | 32 hex characters a VM makes at its first join, never at boot; sent with every report |
| boot | Juice Shop's start time, taken from the challenges' `createdAt`; a new value means Juice Shop restarted. The same token reporting two boots in turn raises the `two_boots` flag |
| continue code | Juice Shop's own encoding of its solved set; ctf-node stores it and applies it after a restart |
| baseline | what Juice Shop had already solved when the student joined; it does not count |
| outbox | a join waiting for an unreachable central server: the one place the join code is ever stored |
| membership status | `local` (no central server), `pending`, `joined`, `refused`, `rejected` (`state.go`) |
| forget | wipe everything about the student on the VM and restart Juice Shop empty: the student's button, or the command an operator's *remove* or *clear* queues |
| first blood | the first participant to solve a challenge, by time of receipt; a mark on the board, no extra points |
| keeper | ctf-node as PID 1, the only part with privilege |
| the gate | the optional `CTF_GATE=session`: a personal return code for VMs that others can reach |
| setup page | `<base>/admin/setup`: makes the first admin account, and exists only while there is none |
| session | an admin login: a cookie, stored as its SHA-256; 30 days with "keep me logged in", else until the browser closes or 12 hours unused |
