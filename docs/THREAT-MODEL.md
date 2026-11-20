# Threat model

The threat model of the Juice Shop CTF: what the system is, what can go wrong
with it, what is done about each threat, and how we know it holds. It is
written for the maintainer, for anyone who changes the code, and for
reviewers.

It follows the four questions of the
[Threat Modeling Manifesto](https://www.threatmodelingmanifesto.org/), with
a data flow diagram and trust boundaries for question 1, STRIDE per element
for question 2, and a qualitative likelihood × impact rating (as in the
[OWASP Risk Rating Methodology](https://owasp.org/www-community/OWASP_Risk_Rating_Methodology))
for each threat. Weaknesses are named by their
[CWE](https://cwe.mitre.org/) where one fits.

| | |
|-|-|
| System | Juice Shop CTF, ctf-node and ctf-central, as on the branch it is in |
| Last reviewed | 2026-11-20 |
| Review again | see [When to review it again](#5-when-to-review-it-again) |

The code never refers to this document or to its threat ids. Comments in the
code explain each defence on their own terms; this document maps them.

## 1. What are we working on?

### Purpose and assets

A capture-the-flag on OWASP Juice Shop for a class. Each student runs a VM
with their own Juice Shop on their laptop and attacks it from Kali. A central
server shows who solved what. Worth protecting, in order:

| Asset | Why it matters |
|-------|----------------|
| **A1 The fairness of the board**: points, first blood, ranks | the point of the game; the students are being trained to attack |
| **A2 A student's progress**: membership, Juice Shop state, place on the board | hours of work, lost by one *Forget* |
| **A3 Admin of ctf-central**: games, join codes, participants, accounts | whoever holds it controls every game |
| **A4 Personal data**: students' names, addresses in the event feed | names of (often young) students on a public page |
| **A5 Availability of ctf-central** during a lesson | thirty students wait when it stalls |
| **A6 The release**: the OVA and the central image | every VM and every server runs what it contains |

### Actors

| Actor | Trust | Reach |
|-------|-------|-------|
| **Student**, on their own VM | untrusted for A1, trusted for their own A2 | full control of the VM: console, disk, hypervisor; Kali on the host-only network; code execution in Juice Shop as uid 65532, for real: Juice Shop runs with its safety mode off, so SSTi evaluates their code and file write and read work |
| **Classmate** | untrusted | can send links and messages; on a misconfigured lab network, reaches other VMs |
| **Internet** | untrusted | ctf-central's public endpoints |
| **Network attacker** (school Wi-Fi, LAN) | untrusted | the traffic between VMs and ctf-central |
| **Operator** (teacher, admin) | trusted | admin of ctf-central, the server and its logs |
| **Maintainer and CI** | trusted | the repository, the release workflow, Docker Hub |

### Data flow diagram

```
                     TB1 laptop: host-only network                        TB4 internet / LAN
 ┌──────────────┐          ┌──────────────── CTF VM (Alpine, Docker) ─────────────────┐
 │ Kali browser │──DF1───► │ :80  shop ──► proxy ──DF3──► Juice Shop :3000 (uid 65532)│
 │ Burp, curl   │──DF2───► │ :8080 page  (ctf-node -serve, uid 10001)                 │
 └──────────────┘          │      ▲  TB2 origin          │   ▲ DF5 poll, restore      │
                           │      │                      │   └──────── 127.0.0.1:3000 │
                           │  node.db (0600, 0700 dir)   │ ◄─DF4── webhook :8081 (lo) │
                           │      TB3 uid                │                            │
                           │  keeper (PID 1, root, SETUID+SETGID only) ◄── socketpair │
                           │  firewall: no NEW from eth0 │                            │
                           └─────────────────────────────┼────────────────────────────┘
                                                         │ DF6 join / report / leave
                                                         ▼ (HTTPS, bearer token)
 ┌──────────────┐  DF7 HTTPS   ┌───────────────┐ DF8  ┌──────────────────────────────┐
 │ operator,    │────────────► │ reverse proxy │────► │ ctf-central (uid 10001)      │
 │ public,      │              │ TLS, X-Real-IP│      │ boards, admin, API           │
 │ beamer       │              └───────────────┘      │ central.db (SQLite)          │
 └──────────────┘                   TB5 proxy         └──────────────────────────────┘
                                                        TB6 CI: GitHub Actions → Docker Hub, release
```

| Flow | From → to | What crosses |
|------|-----------|--------------|
| DF1 | browser → shop (:80) | the student's attacks on Juice Shop |
| DF2 | browser → page (:8080) | join (name, join code), *Restart*, *Forget*, central server address |
| DF3 | proxy → Juice Shop | every shop request except the continue-code apply |
| DF4 | Juice Shop → ctf-node webhook | "a challenge was solved", cheat score |
| DF5 | ctf-node → Juice Shop | polling the challenges, restoring continue codes |
| DF6 | ctf-node → ctf-central | join (name, code → token), reports (solves), leave |
| DF7 | browsers → reverse proxy | admin logins and forms, public boards |
| DF8 | reverse proxy → ctf-central | DF7, with X-Real-IP and X-Forwarded-Proto |

### Trust boundaries

| Boundary | Between | Held by |
|----------|---------|---------|
| **TB1** | the host-only network and everything else | the hypervisor's network settings; the VM's firewall on eth0 |
| **TB2** | the shop's origin (:80) and the page's origin (:8080) | two ports, form tokens script cannot make, origin checks |
| **TB3** | Juice Shop's user (65532) and ctf-node (10001) and the keeper (root) | uids, file modes, capabilities, a socketpair |
| **TB4** | the VM and ctf-central | a bearer token over HTTPS; the server decides everything that counts |
| **TB5** | the internet and ctf-central | the reverse proxy, the admin login, CSRF tokens, limits |
| **TB6** | the outside world and the release | pinned actions and images, scoped workflow permissions |

### Data stores

| Store | Holds | Protection |
|-------|-------|------------|
| `node.db` (VM) | the token, the join code while waiting in the outbox, solves, continue codes | 0600 in a 0700 directory of uid 10001; wiped by *Forget* |
| `central.db` | games (join code as hash and readable), participants (token hashes), solves, events with addresses, accounts (PBKDF2), sessions (SHA-256) | the server's volume; never on a public page |
| ctf-central's log | the setup code while there is no account | readable only by whoever runs the server |

### Assumptions

- The hypervisor keeps the host-only network to the student's own machines.
- The operator runs ctf-central behind a reverse proxy with HTTPS that
  overwrites `X-Real-IP` and `X-Forwarded-Proto`, or sets
  `CTF_TRUSTED_PROXIES=none` without one.
- Browsers send `Sec-Fetch-Site` (all current ones do); without it, the form
  token and `Origin` still have to be right.
- Juice Shop 20.3.0 matches its routes on the encoded path, case
  insensitively, and its continue-code apply routes are the three under
  `/rest/continue-code*/apply`.
- Juice Shop runs with `NODE_ENV=unsafe` (its safety mode off), set by the
  keeper in the environment it starts Juice Shop with. Every defence of TB3
  is meant to hold against code a student runs as 65532; none relies on
  Juice Shop keeping its dangerous challenges off.
- Nobody but the operator reads ctf-central's log.

### Out of scope

Juice Shop's own vulnerabilities (they are the exercise), the student's
laptop and Kali, the operating system and reverse proxy of the operator's
server, and the security of the GitHub and Docker Hub accounts.

## 2. What can go wrong?

STRIDE per element. **L** is likelihood, **I** is impact, **R** is the
risk that remains with the mitigations of section 3 in place (H, M, L).

### Spoofing

| Id | Threat | Element | CWE | L | I | R |
|----|--------|---------|-----|---|---|---|
| S1 | A student sends forged reports with their own token (from the VM's disk, or code execution in Juice Shop), claiming solves they never made | DF6 | CWE-345 | H | M | **M, accepted** |
| S2 | A student joins under a look-alike of a classmate's name (`Аnna` with a Cyrillic А) | DF6, board | CWE-1007 | M | L | L |
| S3 | A client poses as another address (`X-Real-IP`) to escape the limits or to falsify the feed | DF8 | CWE-348 | H | M | L |
| S4 | Whoever finds a fresh ctf-central first makes the first admin account | DF7 | CWE-1188 | M | H | L |
| S5 | A network attacker reads a token or join code and acts as a student (report, leave) | DF6 | CWE-319 | M | M | L (HTTPS) / M (plain HTTP on a LAN) |
| S6 | A student's VM is pointed at a central server of an attacker's, which collects the join code and hands out the token | DF2, DF6 | CWE-345 | L | M | L |

### Tampering

| Id | Threat | Element | CWE | L | I | R |
|----|--------|---------|-----|---|---|---|
| T1 | A student applies a continue code (a classmate's, or one made by hand) and gets every challenge in it: through the proxy, or from inside Juice Shop with code execution (SSTi), past the proxy | DF3 | CWE-284, CWE-180 | H | H | L (proxy) / **M, accepted (from inside)** |
| T2 | Script in the shop (Juice Shop's XSS, set off by a classmate's link) presses *Forget* or changes the central server on the page: in the browser, or by going on to SSTi, whose code runs inside the VM and sends no Origin | TB2 | CWE-352, CWE-346 | M | H | L |
| T3 | A page on another site (or a sibling subdomain that can set cookies) makes an admin's browser post an admin form | DF7 | CWE-352 | L | H | L |
| T4 | A report changes another participant's score, solves or state | DF6 | CWE-639 | M | H | L |
| T5 | Code in Juice Shop (uid 65532) reads or changes `node.db`, signals ctf-node, or takes the page's port | TB3 | CWE-269 | H | H | L |
| T6 | Code in Juice Shop posts a cheat score of 0 for every challenge first, to hide the real one | DF4 | CWE-345 | M | L | L |
| T7 | SQL, HTML or spreadsheet formulas injected through a name, a report or a header | DF6, DF8 | CWE-89, CWE-79, CWE-1236 | M | H | L |
| T8 | A moved tag of an action or a base image changes what a release is built with | TB6 | CWE-829 | L | H | L |

### Repudiation

| Id | Threat | Element | CWE | L | I | R |
|----|--------|---------|-----|---|---|---|
| R1 | An admin action (remove, clear, a new password) cannot be traced to an account and an address | central | CWE-778 | L | M | L |

### Information disclosure

| Id | Threat | Element | CWE | L | I | R |
|----|--------|---------|-----|---|---|---|
| I1 | The join code appears on a public page or in public JSON, and anyone can join the game | boards | CWE-200 | M | M | L |
| I2 | A token, join code, password or session ends up in a log, an event, an error or a page | all | CWE-532 | L | H | L |
| I3 | A stolen `central.db` gives away passwords or working sessions | central.db | CWE-916 | L | H | L |
| I4 | Students' full names on a public board | boards | CWE-359 | M | L | L |
| I5 | A token travels to another scheme or host by a redirect from the central server | DF6 | CWE-601 | L | M | L |

### Denial of service

| Id | Threat | Element | CWE | L | I | R |
|----|--------|---------|-----|---|---|---|
| D1 | Login attempts in parallel keep the CPU busy with password hashes, or slip past the limits | DF7 | CWE-307, CWE-400 | M | M | L |
| D2 | Requests without a token or login flood the event feed and the database | DF6, DF7 | CWE-400 | M | M | L |
| D3 | Ten wrong passwords pause the login of an admin's username for 15 minutes | DF7 | CWE-645 | M | L | **L, accepted** |
| D4 | A student breaks their own Juice Shop or makes it hang (RCE DoS, XXE DoS, Memory Bomb, file write, or their own code through SSTi) | DF3 | — | H | L | **L, accepted** |
| D5 | A classmate reaches another VM on a misconfigured network and presses *Forget* or *Restart* | TB1 | CWE-306 | L | H | L (adapter 1 bridged) / **M (adapter 2 bridged)** |

### Elevation of privilege

| Id | Threat | Element | CWE | L | I | R |
|----|--------|---------|-----|---|---|---|
| E1 | Code execution in Juice Shop becomes root in the container, or ctf-node's privileges | TB3 | CWE-250 | H | H | L |
| E2 | An admin route without a login, reachable by anybody | central | CWE-306 | L | H | L |
| E3 | A redirect after login sends the admin to another site | DF7 | CWE-601 | L | L | L |
| E4 | A workflow job with write access runs code it did not need to | TB6 | CWE-250 | L | H | L |

## 3. What are we going to do about it?

### Mitigations

| Threat | Mitigation | Where |
|--------|------------|-------|
| S1 | The server decides: its own catalogue, the time of receipt for first blood and ties, solve times in the future clamped, unknown keys ignored. Flags (prerequisite, burst, before join, after end, two boots, cheat score) show what an honest VM never sends. **The rest is accepted** (section 4). | `internal/central/store/report.go` |
| S2 | A name is in one script; Han, Hiragana, Katakana, Hangul and Bopomofo count as one. The operator can hide a participant. | `internal/names/names.go` |
| S3 | `X-Real-IP` and `X-Forwarded-Proto` count only from a peer in `CTF_TRUSTED_PROXIES`, and only as one valid IP without a zone. | `internal/httpx/proxies.go` |
| S4 | The first account needs a one-time setup code that the server logs at its start; wrong codes count against the login limit. | `internal/central/auth.go` |
| S5 | A VM refuses `http://` to a host on the internet; plain HTTP only to localhost, a name without a dot or a private address, with a warning on the page. A release needs an `https://` `CENTRAL_URL`. | `internal/node/node.go`, `.github/workflows/release.yml` |
| S6 | The central server can only be changed on the page, before joining, with a form token and an origin check (see T2). | `internal/node/page.go` |
| T1 | The proxy refuses the three continue-code apply routes, checked on the path as Juice Shop receives it (decoded and raw). A path with `//` or a `.`/`..` segment is refused, not repaired, so the checked path is the sent path. From inside Juice Shop the proxy is not in the way: there the `burst` flag is what remains (section 4). | `internal/node/proxy.go` |
| T2 | The shop on :80 and the page on :8080: two origins. Form tokens are HMACs under a key in memory, not cookies (which ignore ports). A form from another origin (`Sec-Fetch-Site`, `Origin`) is refused. In the image the page answers nothing to the VM itself (loopback or its own addresses, the only ones Juice Shop's code can send from) but the healthcheck, so code in Juice Shop can neither read a form token nor post one. The gate's session cookie is taken out of every request before it reaches Juice Shop. | `internal/node/page.go`, `internal/httpx/csrf.go` |
| T3 | Admin form tokens are HMACs bound to the session's hash; a form from another site (`Sec-Fetch-Site`) is refused; cookies are `SameSite`, `HttpOnly`, `Secure` behind HTTPS. | `internal/central/admin.go`, `internal/httpx/csrf.go` |
| T4 | A report is about the one participant its token's hash names; nothing in it touches another. | `internal/central/store/report.go` |
| T5 | ctf-node runs as 10001 without capabilities and refuses to start with any; `node.db` is 0600 in a 0700 directory. The keeper takes one command over a socketpair and binds both ports before Juice Shop starts. | `internal/node/keeper/`, `cmd/ctf-node/main.go` |
| T6 | The highest cheat score per challenge is kept, on the VM and on the server. | `internal/node/proxy.go`, `internal/central/store/report.go` |
| T7 | Parameterised SQL only; `html/template` only; `board.js` writes with `textContent` and `setAttribute`; a strict CSP without inline script or style; names restricted to letters, digits, spaces and `. - ' _`; `csvSafe` on every text column of the CSV exports. | `internal/central/store/`, `web/`, `internal/httpx/httpx.go`, `internal/central/admin.go` |
| T8 | Actions pinned by commit SHA, base images by digest; Dependabot proposes the updates as pull requests. | `.github/workflows/release.yml`, `deploy/docker/`, `.github/dependabot.yml` |
| R1 | Every admin action and login is an event with the account and the (trusted) address. | `internal/central/admin.go`, `internal/central/auth.go` |
| I1 | The join code is shown only in admin (the game page, the admin beamer and its `board.json`, all behind the login); the public board and its JSON carry no code. | `internal/central/admin.go`, `internal/central/board.go` |
| I2 | Tokens and codes are never logged, shown or put in an error; the one secret logged is the setup code, on purpose. Validation errors never repeat a token, a code or a name. | `internal/protocol/validate.go`, `internal/node/` |
| I3 | Passwords as PBKDF2-SHA256 (600 000 iterations, salted); sessions and tokens as SHA-256. | `internal/central/password.go`, `internal/central/store/users.go` |
| I4 | A game's board can show initials only, or be hidden. | `internal/central/store/board.go` |
| I5 | ctf-node follows no redirect from the central server. | `internal/node/node.go` |
| D1 | Login limits per address and per username are TAKEN before the hash (atomic), and at most four hashes run at once. | `internal/central/limit.go`, `internal/central/auth.go` |
| D2 | Events caused by requests from anybody: at most 30 per address per minute; events expire after the retention. Body sizes are limited. | `internal/central/server.go` |
| D3 | **Accepted**: the lockout ends 15 minutes after the last try; other admins are not affected. | — |
| D4 | **Accepted**: *Restart Juice Shop* on the page (also for a shop that hangs), and the keeper restarts a Juice Shop that died, in unsafe mode again; the progress comes back from the continue codes. | `internal/node/keeper/supervisor.go` |
| D5 | The VM drops new connections from eth0 (NAT) to both ports; the optional gate (`CTF_GATE=session`) for VMs that others can reach. Adapter 2 bridged stays the operator's to prevent. | `deploy/vm/files/juiceshop-ctf.initd`, `internal/node/page.go` |
| E1 | The keeper holds SETUID and SETGID only, `no-new-privileges` is set, Juice Shop and ctf-node run as different users without capabilities. | `internal/node/keeper/`, `deploy/vm/files/juiceshop-ctf.initd` |
| E2 | Every admin route is wrapped (`requireUser` for GET, `action` for POST). | `internal/central/admin.go`, `internal/central/auth.go` |
| E3 | `safeNext` keeps the redirect after login inside admin. | `internal/central/auth.go` |
| E4 | Read-only by default; only the jobs that make, fill and publish the release have `contents: write`; checkouts that do not push keep no credentials. | `.github/workflows/release.yml` |

## 4. Accepted risks

- **S1, forged reports.** A student owns the VM: with its disk, or with code
  execution in Juice Shop, they can send any solve, and a careful one raises
  no flag. Any secret in the OVA would be the student's too, so no change on
  the VM can prevent this. The board is a game, not an exam: the operator
  does not grade on points alone.
- **Script in a student's own shop** (Juice Shop's XSS) can change the shop.
  That is the exercise. It cannot reach the page (T2).
- **D3**, a paused login, and **D4**, a broken shop.
- **T1 from inside Juice Shop.** With its safety mode off, SSTi evaluates
  the student's code as 65532 in Juice Shop's own process, which can call
  Juice Shop's continue-code apply on loopback, where the proxy is not in
  the way. That is S1 again, reached through a 6★ challenge instead of the
  VM's disk: a copied code with more than five solves raises `burst`, a
  careful one nothing. A classmate can set this off too, with a link that
  takes the victim's browser through the shop's XSS to SSTi: the victim
  then gets solves they did not make, and a flag. Nothing is lost (the
  page cannot be reached from there, so no *Forget*), and the operator
  sees the flag. Turning the safety mode back on would take 18
  challenges from every student to close a path the disk leaves open
  anyway.
- **Plain HTTP on a private network (S5)**, accepted with a warning, for labs
  without certificates.
- **A name entirely in a look-alike script (S2)** passes the one-script rule.
- **Adapter 2 bridged (D5)** puts the VM on the classroom network; that is
  the network Kali needs to reach.

## 5. Did we do a good job?

### How each mitigation is checked

| Threats | Checked by |
|---------|------------|
| T1 | `TestProxyRefusesContinueCodes`, `TestProxyRefusesPathsThatWouldChangeOnTheWay`; `scripts/image.sh` and `scripts/e2e.sh` against the real Juice Shop |
| T2, S6 | `TestThePageIsNotServedOnTheShopsPort`, `TestFormsFromAnotherOriginAreRefused`, `TestThePageRefusesTheVMItself`, `TestTheGatesCookieDoesNotReachJuiceShop`; the "a form from the shop's origin" step of `scripts/e2e.sh`; the isolation step of `scripts/image.sh` (the page answers 65532 with 403) |
| T3 | `TestCSRFIsBoundToTheSession`, `TestCSRF`, `TestCrossOrigin` |
| T4, S1 | the store tests in `internal/central/store/store_test.go` (`TestForgetReachesOnlyThatParticipant`, `TestFlags`, `TestHonestPlayRaisesNoFlag`); the "no false flags" step of `scripts/e2e.sh` |
| T5, E1 | `scripts/image.sh`, with Juice Shop in unsafe mode as on the VM (its `unsafe` step checks that, also after a respawn): uids, capabilities, `node.db` unreadable and the page's port not takeable as 65532, the keeper refusing without SETUID/SETGID |
| T6 | `TestWebhookKeepsTheHighestCheatScore`, `TestTheHighestCheatScoreIsKept` |
| S2 | `TestNormalize` |
| S3 | `TestClientIP`, `TestSecure`, `TestClientAddress` |
| S4 | `TestSetup`, `TestSetupCodeGuessing`; the setup step of `scripts/e2e.sh` |
| S5 | `TestCentralURLOverPlainHTTP` |
| D1 | `TestLoginLimits`, `TestLoginLimitsHoldInParallel`, `TestLoginWhenHashingIsFull` |
| D2 | `TestRejectedEventsAreLimited` |
| D5 | `deploy/vm/test.sh`: the NAT adapter reaches neither port |
| E2 | `TestAdminNeedsLogin` |

### When to review it again

- before every release;
- when a request from outside, a form, a token, a limit or a privilege
  changes, or a new endpoint, port or data store is added;
- when the protocol between VM and central server changes;
- when Juice Shop is upgraded: its routes, its continue-code apply, its
  route matching and what its unsafe mode turns on are assumptions here;
- when a dependency, an action or a base image is added;
- after any security report.

A review walks section 2 again for the elements that changed, updates the
ratings, and adds a check to section 5 for every new mitigation.
