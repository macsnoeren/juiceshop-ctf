# CLAUDE.md

Juice Shop CTF: a capture-the-flag on OWASP Juice Shop for a class. Two Go
programs in one module: `ctf-node` runs inside the Juice Shop image on every
student's VM, `ctf-central` runs once on the operator's server.
[docs/DEVELOPER.md](docs/DEVELOPER.md) explains how they fit together; read it
before a non-trivial change.

## Commands

```sh
make test      # gofmt, vet, unit tests with -race (no Docker) — run before every commit
make e2e       # the whole CTF in Docker (scripts/e2e.sh, ~5 min)
make run       # local test setup; make stop to stop it
bash scripts/image.sh   # the image on its own, after touching the keeper or Dockerfile.node
```

## Layout — keep it this way

```
README.md CHANGELOG.md LICENSE CLAUDE.md Makefile go.mod go.sum   the ONLY files in the root
cmd/<program>/main.go   flags, environment, wiring. No logic: that lives in internal/
internal/<package>/     one flat level of packages, named after what they do
internal/node/          ctf-node; keeper/ is its root part
internal/central/       ctf-central; store/ is its SQLite database
web/                    templates and static files, embedded by an embed.go per directory
deploy/                 how things are BUILT and RUN: docker/, compose/, vm/
scripts/                every shell script; the Makefile and CI call them
docs/                   every document for people: operators, students, developers
```

Rules:

- **No `shared/`, `common/`, `util/` or `misc/` packages.** A package used by
  both programs goes directly under `internal/` with a name that says what it
  is (`internal/protocol`, `internal/httpx`).
- **No `pkg/`.** Nothing here is meant to be imported by other modules.
- **Tests sit next to the code** (`*_test.go`), fixtures in `testdata/`. Shell
  tests are in `scripts/`; there is no `test/` or `tests/` directory.
- **Documents go in `docs/`.** The only Markdown outside it: `README.md`,
  `CHANGELOG.md`, `CLAUDE.md`, `deploy/vm/README.md` and
  `deploy/compose/README.md` (how to build or run that directory),
  `deploy/docker/DOCKERHUB.md` (the Docker Hub page, published by CI) and
  `internal/juiceshop/testdata/README.md` (how the fixtures were captured).
- **No planning documents** (designs per version, task lists, findings) in
  the repository. Reasoning goes in the code's comments, history in git and
  `CHANGELOG.md`.
- **Do not add a directory or a root file** without a reason that fits the
  rules above; ask first if it does not.

When a file or package moves, also update: the `Makefile`,
`.github/workflows/release.yml`, `.dockerignore`, the `-ldflags` path of
`internal/version` in `deploy/docker/Dockerfile.*`, and the tree in
`docs/DEVELOPER.md` §3.

## Comments

- Short and to the point: say WHY, and what breaks if the line changes. Not
  what the next line obviously does.
- **Never cite a document, section, task, issue or decision** (`§4.3`,
  `T1.1-31`, `see DESIGN.md`, `decision D5`, `docs/PROTOCOL.md is the
  reference`). A comment must stand on its own. This holds for ALL code:
  Go, shell, Dockerfiles, YAML, HCL, templates. The documents in `docs/`
  point at the code, never the other way round. Only a path a program
  actually uses is allowed (`CHANGELOG.md` read by the release script, the
  Docker Hub readme, a link written into the release notes).
- Every file starts with a comment that says what it is and what is unusual
  about it. Keep it true when you change the file.
- Emphasis in CAPITALS; a warning starts with `>>>`.
- Test sections are headed `// ---- <behaviour> ----`, named after what they
  hold in place.

## Conventions

- Everything in English: code, comments, docs, commit messages.
- Commit subjects are a plain sentence saying what changed and why; no
  `feat:` prefixes.
- Every user-visible change gets a line under `## Unreleased` in
  `CHANGELOG.md`.
- A change to `internal/protocol` is a change to `docs/PROTOCOL.md`, and must
  keep older VMs working: a VM only changes with a new OVA.
- The module path is `github.com/macsnoeren/juiceshop-ctf`: where the repo
  is, so `go install …/cmd/ctf-central@latest` works. **Before tagging
  v2.0.0** (or any later major version), the module path must end in `/v2`
  (`/v3`, …): change `go.mod`, every import, and the `-ldflags` path in
  `deploy/docker/Dockerfile.*`, or `go install …@v2.0.0` fails.
  `make release-check` (and so the release workflow) refuses a tag that does
  not fit. The same change is needed when the repository is renamed or moves.
- Two direct dependencies (`modernc.org/sqlite`, `golang.org/x/text`). A third
  needs a good reason, written in the commit.

## Security

The students are being trained to attack, and this code is what they attack
next to Juice Shop. [docs/THREAT-MODEL.md](docs/THREAT-MODEL.md) is the
threat model: data flows, trust boundaries, threats and their defences; read it before a change that touches a request
from outside, the proxy, a form, a token, a limit or a privilege.

**Every change Claude writes gets a security check before it is committed
or handed over**, however small:

1. Run the `/security-review` skill on the pending changes, or, where it is
   not available, review the diff against the threat model in
   docs/THREAT-MODEL.md yourself, as an attacker would.
2. Check at least, for what the diff touches:
   - input from outside (HTTP, a report, the webhook, the environment) is
     validated where it enters, and limited in size;
   - every admin route is wrapped (`s.requireUser`, `s.action`), every form
     carries a token and refuses another origin;
   - a path, URL or name is checked in the form in which it is USED, not in
     the form in which it came in;
   - addresses come from `s.clientIP`, never from a header read by hand;
   - a limit in front of slow work is taken (`window.Take`), not counted;
   - no secret (token, join code, password, session) reaches a log line, an
     event, an error message or a page it does not belong on;
   - the page and the shop stay two origins; `-serve` gains no privilege;
   - nothing a report says affects another participant;
   - a new dependency, action or base image is pinned (SHA, digest).
3. Say what the check found in the reply to the user: what was checked,
   what was fixed, and anything left open. Never weaken a defence without
   saying so plainly.
4. A fixed weakness gets a test that fails when the defence is taken away,
   under a `// ---- <behaviour> ----` heading; and docs/THREAT-MODEL.md is
   updated when a threat or a defence changes. The code never refers to
   the threat model (no threat ids, no "see THREAT-MODEL.md").

## License

GPL-3.0-or-later (`LICENSE`). **Every new source file starts with**, in its
own comment syntax (after a `#!` line if there is one, before a Go package
comment, with a blank line after):

```
SPDX-License-Identifier: GPL-3.0-or-later
Copyright (C) 2026 Maurice Snoeren
```

- Go, JS: `//` lines. CSS: `/* … */`. Shell, Dockerfile, YAML, HCL,
  Makefile: `#` lines. Templates (`.html`, `.svg`): `{{/* … */}}`, outside
  every `{{define}}`.
- Not in Markdown, JSON or `testdata/`. Files captured from Juice Shop (the
  catalogue JSON, `internal/juiceshop/testdata/`) are Juice Shop's, MIT:
  never put our header on them.
- Both images carry `LICENSE` at `/usr/share/licenses/juiceshop-ctf/` and
  the label `org.opencontainers.image.licenses`. Keep both when changing a
  Dockerfile.
- A new dependency must be GPL-3.0 compatible (MIT, BSD, Apache-2.0, LGPL,
  GPL-3.0). Not GPL-2.0-only, not proprietary or "non-commercial".
