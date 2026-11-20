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
  `T1.1-31`, `see DESIGN.md`, `decision D5`). A comment must stand on its own.
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
