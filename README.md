[![release](https://github.com/macsnoeren/juiceshop-ctf/actions/workflows/release.yml/badge.svg)](https://github.com/macsnoeren/juiceshop-ctf/actions/workflows/release.yml)
# Juice Shop CTF

[Releases](https://github.com/macsnoeren/juiceshop-ctf/releases): the students' VM for VirtualBox and VMware ·
[Docker Hub](https://hub.docker.com/r/macsnoeren/juiceshop-ctf-central): the central server

A capture-the-flag on [OWASP Juice Shop](https://owasp.org/www-project-juice-shop/)
for a class: every student attacks a Juice Shop of their own, in a VM next to
their Kali, and the central leaderboard shows, live and by name, who has
solved what. Nobody types in a flag: the VM sees the solves and reports them.

```
 the student's laptop (VirtualBox)                                    neo.jmnl.nl
 Kali ── host-only ──► CTF VM: juiceshop-ctf ──────── NAT ─────────►  ctf-central
  browser, Burp        /ctf/        ctf-node: join, progress,          games and join codes,
                                    Restart, Forget                    boards per game, beamer,
                       /juiceshop/  Juice Shop v20.3.0                 admin and the event feed
```

The operator makes a **game** in admin and shows its **join code**; a student
types their name and that code on their VM's page, and the VM gets a token for
that participant. The VM image is the same for everybody and holds no secret.

| | |
|---|---|
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | running a lab: games, the board, cleaning up, the VM |
| [deploy/vm/STUDENT.md](deploy/vm/STUDENT.md) | the students' sheet |
| [docs/VERSION-1-1.md](docs/VERSION-1-1.md) | what the first lab showed and why 1.1 is built this way |
| [docs/PROTOCOL.md](docs/PROTOCOL.md) | join, report and leave between VM and central server |
| [docs/DESIGN.md](docs/DESIGN.md) | the design of 1.0, still the reasoning where 1.1 does not say otherwise |
| [docs/TASK-1-1.md](docs/TASK-1-1.md) | how 1.1 was built, step by step |
| [DEVELOPER.md](DEVELOPER.md) | for developers: how the code fits together, where things are, how to make and test a change |
| [CHANGELOG.md](CHANGELOG.md) | |

## Try it

Needs Docker with compose.

```sh
make run      # the central server and three students' VMs, each with its "Kali"; prints the addresses
make stop     # deploy/compose/stop.sh --wipe forgets everything
```

Then open `http://127.0.0.1:8090/juiceshop-ctf/admin/`: the first time it
asks you to make an admin account. Make a game there, join it
from `http://127.0.0.1:8091/ctf/`, solve
`http://127.0.0.1:8091/juiceshop/ftp/acquisitions.md` and watch the board.

## The parts

| | | |
|-|-|-|
| [`cmd/ctf-node`](cmd/ctf-node) | Go, in the `juiceshop-ctf` image `FROM bkimminich/juice-shop:v20.3.0` | the keeper (PID 1, root with SETUID+SETGID only) starts Juice Shop as 65532 and ctf-node as 10001 with no capabilities; ctf-node serves the student's page, proxies the shop, watches it, restores it after a restart, and reports |
| [`cmd/ctf-central`](cmd/ctf-central) | Go + SQLite, `FROM scratch` | games, joins, reports, the boards, admin, the event feed and flags. Configured from its database; the environment says only where that is and where to listen |

Each `main.go` documents its environment at the top. Nothing in either is a
secret.

```
cmd/              ctf-node, ctf-central
internal/node     the VM: page, proxy, watcher, reporter, node.db; keeper/ (the root part)
internal/central  the central server: API, admin, boards; store/ (SQLite)
internal/shared   protocol (v2), names, httpx, config, health, version
internal/juiceshop  the client, the embedded challenge catalogue, fixtures of the pinned version
web/              templates per program, the stylesheet and scripts
deploy/           docker/ (the two Dockerfiles), compose/ (the test setup), vm/ (the students' VM: Packer in Docker)
docs/             the documents above
tests/            e2e.sh (everything, ~5 min), image.sh (the image on its own, ~2 min)
```

## Tests

```sh
make test               # gofmt, vet, unit tests with -race; no Docker
make e2e                # two games, three VMs, offline join, Forget, remove, clear, flags
bash tests/image.sh     # the image: users, isolation inside the container, respawn
```

## On a server

`jmnl-external-servers` deploys the central server as signed Flatcar
bundles, on `neo-jmnl-node` at `https://neo.jmnl.nl/juiceshop-ctf/` (with
`/admin/` behind an IP allow-list as well as its own login).

The students' VM is built here, for VirtualBox and VMware, with nothing but
Docker ([deploy/vm/README.md](deploy/vm/README.md)):

```sh
make vm       # dist/vm/juiceshop-ctf-<version>-virtualbox.ova and -vmware.zip
```

The image runs anywhere Docker does:

```sh
docker run -d -p 80:80 -v juiceshop-ctf:/var/lib/ctf \
  --cap-drop ALL --cap-add SETUID --cap-add SETGID --security-opt no-new-privileges \
  juiceshop-ctf
```

Without `CTF_CENTRAL_URL` (or with *No central server* on its page) it runs
on its own and shows the local score.
