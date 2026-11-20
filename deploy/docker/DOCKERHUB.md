# Juice Shop CTF — central server

The central server of [Juice Shop CTF](https://github.com/macsnoeren/juiceshop-ctf):
a capture-the-flag on OWASP Juice Shop for a class. Every student attacks a
Juice Shop of their own, in a VM next to their Kali; the VM reports what they
solve to this server, which shows live leaderboards per game, a beamer view,
and an admin for the teacher.

The students' VM (VirtualBox and VMware) is on the project's
[releases page](https://github.com/macsnoeren/juiceshop-ctf/releases).

## Run it

```sh
docker run -d --name ctf-central -p 8080:8080 \
  -v ctf-central-db:/data \
  macsnoeren/juiceshop-ctf-central:latest
```

Then open `http://<host>:8080/admin/`. **The first visit makes the first
admin account** (the setup page), so open it yourself right after starting
the server, or put it behind an allow-list. After that: make a game, show
its join code, and put `http://<host>:8080/g/<game>/beamer` on the beamer.

Point the students' VMs at it on their CTF page (*Central server*), or build
the VM with another default.

## Configuration

Everything a teacher sets — games, join codes, scoring, the title, the time
zone, admin accounts — is made in the admin and kept in the database. The
environment only says where things are:

| Variable | Default | |
|----------|---------|-|
| `CTF_DB_PATH` | `/data/central.db` | the SQLite database; keep `/data` on a volume |
| `CTF_ADDR` | `:8080` | where it listens |
| `CTF_BASE_PATH` | *(empty)* | a path prefix behind a reverse proxy, e.g. `/juiceshop-ctf` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

Behind HTTPS (a reverse proxy), pass `X-Forwarded-Proto` and `X-Real-IP`:
the login cookie is then marked Secure, and the join limits count the
students' real addresses.

## The image

- `FROM scratch`: one static Go binary, CA certificates and the licence text
  (`/usr/share/licenses/juiceshop-ctf/LICENSE`), nothing else; runs as uid
  10001. `linux/amd64` and `linux/arm64`.
- `HEALTHCHECK` built in (`/ctf-central -healthcheck`).
- Tags: `latest`, `MAJOR.MINOR` and `MAJOR.MINOR.PATCH`, from the project's
  release tags. `/ctf-central -version` prints the version it was built from.

## License

GNU General Public License v3.0 or later. Source:
[github.com/macsnoeren/juiceshop-ctf](https://github.com/macsnoeren/juiceshop-ctf).
