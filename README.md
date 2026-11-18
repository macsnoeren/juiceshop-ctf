# Juice Shop CTF

A capture-the-flag on [OWASP Juice Shop](https://owasp.org/www-project-juice-shop/)
in which every student attacks a Juice Shop of their own, and a central
leaderboard shows, live and by name, who has solved what. Nobody types in a
flag: an agent next to each Juice Shop sees the solves and reports them.

```
 student ──► nginx ──► /ctf/        ctf-status    sign up with name + access code → gate cookie
                 └──► /juiceshop/  Juice Shop    only with that cookie
                                      │ webhook + polling
                                   ctf-agent ───► ctf-central   /leaderboard/, /beamer, /admin/
```

The reasoning behind every choice is in [DESIGN.md](DESIGN.md); the way it was
built, step by step, in [TASKS.md](TASKS.md).

## Try it

Needs Docker with compose.

```sh
docker/start.sh        # docker/stop.sh to stop, docker/stop.sh --wipe to start over
```

| | |
|---|---|
| `http://<ip>/ctf/` | sign up, access code **`test1`** |
| `http://<ip>/juiceshop/` | the shop, after signing up |
| `http://<ip>/leaderboard/` | the board, also `/leaderboard/beamer` |
| `http://<ip>/leaderboard/admin/` | admin, **`admin` / `admin`** |

`<ip>` is `localhost`, or this machine's address from another one. A second
student is `docker/start.sh --second`, on port 8080 with
access code `test2`. More in [docker/README.md](docker/README.md).

## The parts

| | Language | Role |
|---|---|---|
| Juice Shop `v20.3.0` | upstream image | the target, pinned, on a network of its own with no way out |
| [`cmd/ctf-agent`](cmd/ctf-agent) | Go | polls Juice Shop, restores it after a restart, reports to the central server |
| [`cmd/ctf-status`](cmd/ctf-status) | Go | the door: sign-up, gate cookie for nginx `auth_request`, own progress |
| [`cmd/ctf-central`](cmd/ctf-central) | Go + SQLite | receives the reports, scores, leaderboard, beamer, admin |

Each binary documents its environment variables at the top of its `main.go`.
The images are `FROM scratch`, 10-20 MB, and run as a non-root user.

```
cmd/            the three programs
internal/       protocol (the report format), agent, status, central, store (SQLite),
                juiceshop (client + fixtures captured from the pinned version), gate (cookie)
web/            templates, stylesheet and leaderboard script, embedded in the binaries
docker/         Dockerfiles, the test setup, nginx configuration
tests/e2e.sh    the whole thing end to end in Docker
```

## Tests

```sh
bash scripts/test.sh          # gofmt, vet, unit tests with -race; no Docker needed
bash scripts/test.sh -fuzz    # plus 30 s of fuzzing on the gate cookie
bash tests/e2e.sh             # Docker: door, sign-up, solve, restore, isolation, outages, reset (~1 min)
```

## On a server

`jmnl-external-servers` deploys this to Flatcar nodes as signed firmware
bundles. For the test phase: one instance (Juice Shop + agent + status) on its
own node, `cybersecurity-1`, over plain HTTP on its IP address; the central
server on `neo-jmnl-node` at `https://neo.jmnl.nl/juiceshop-ctf/` (DESIGN.md
§13). The lines that carry over to a node's
nginx are in [docker/nginx/ctf-instance.conf](docker/nginx/ctf-instance.conf).

On a server:

- give every instance its own `CTF_ACCESS_CODE`, `CTF_GATE_SECRET` and
  `CTF_INTERNAL_TOKEN`;
- put `/admin/` of the central server behind nginx (IP allow-list + password):
  it has no login of its own;
- on the internet, serve it over **HTTPS** and leave `CTF_COOKIE_SECURE` at
  its default `true`. Plain HTTP (`false`) puts the access code and the gate
  cookie on the wire in the clear: acceptable for a test, not for a course;
- `CTF_SEED` only where both halves of the token are made in one place, as in
  `jmnl-external-servers` (`nodes/cybersecurity-1/make-keys.sh` writes the
  agent's `agent.env` and neo's `central.env`). Otherwise create the instance
  and its token in `/admin/`, where only a hash is kept.
