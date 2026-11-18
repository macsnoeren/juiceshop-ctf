# docker/

The images, and a test setup that runs the whole CTF on one machine with the
networks and the nginx layout of a real node (DESIGN.md §4, §12).

| File | |
|---|---|
| `Dockerfile.agent`, `Dockerfile.status`, `Dockerfile.central` | the three images. Build context is the **repository root**: `docker build -f docker/Dockerfile.agent -t juiceshop-ctf-agent:latest .` |
| `docker-compose.yml` | the test setup: Juice Shop, agent, status page, central server, nginx |
| `start.sh`, `stop.sh` | start it (build, wait until healthy, print the URLs) and stop it |
| `nginx/ctf-instance.conf` | **the path layout and the door of one instance; this is what a node's nginx carries over** |
| `nginx/ctf-leaderboard.conf` | `/leaderboard/` to the central server; test setup only |
| `nginx/default.conf` | the two server blocks (`:80` instance 1, `:8080` instance 2) |
| `nginx/ctf-admin.htpasswd` | test login for `/leaderboard/admin/`: `admin` / `admin` |
| `env/*.env` | sample configuration. **Not secrets**, and never to be reused on a server |

## Running it

```sh
./start.sh             # instance 1 + central server; prints the URLs
./start.sh --second    # plus instance 2 on :8080
./stop.sh              # stop, keep the sign-ups and the leaderboard
./stop.sh --wipe       # stop, and forget everything
```

The scripts are `docker compose up -d --build --wait` and `docker compose
--profile second down`; the plain compose commands work just as well.

| URL | |
|---|---|
| `http://<ip>/ctf/` | sign up (access code `test1`), later: own progress |
| `http://<ip>/juiceshop/` | the shop, only with the gate cookie; without it you land on `/ctf/` |
| `http://<ip>/leaderboard/` | the board; `/leaderboard/beamer` for a projector |
| `http://<ip>/leaderboard/admin/` | `admin` / `admin`: instances, tokens, reset, hide, scoring, CSV |
| `http://<ip>:8080/ctf/` | instance 2 (access code `test2`), with `--profile second` |

Ports 80 and 8080 taken? `CTF_HTTP_PORT=8000 CTF_HTTP_PORT_2=8001 docker compose up`.

## By IP address from another machine

The test env files set `CTF_COOKIE_SECURE=false`. A browser refuses to store a
`Secure` cookie from `http://<ip>/` (only `localhost` is exempt), so without
it the door would never open from another machine on plain HTTP.

> **That also means the access code and the gate cookie cross the network in
> the clear.** Fine in a closed lab, not on the internet. On a server: HTTPS,
> and `CTF_COOKIE_SECURE=true` (the default when the variable is absent).

## What is set differently from production

| | here | production default | why |
|---|---|---|---|
| `POLL_INTERVAL` | 5s | 30s | the restore after a Juice Shop restart is seen within seconds |
| `REPORT_INTERVAL` | 15s | 60s | a reset from `/admin/` rides on the next report, so it arrives sooner |
| `REPORT_BACKOFF_MAX` | 10s | 5m | the outage test does not wait minutes |
| `CTF_COOKIE_SECURE` | false | true | plain HTTP by IP, above |
| `CTF_SEED` | two instances | unset | no trip through `/admin/` before the first test |

## Trying things by hand

```sh
# solve a challenge with curl: copy the cookie line from the progress page on /ctf/
curl -b 'jmnl_ctf_gate=…' http://localhost/juiceshop/ftp/acquisitions.md

# restart Juice Shop: its progress is gone, the agent puts it back within seconds
docker compose restart juiceshop
docker compose logs -f ctf-agent

# what Juice Shop can reach (should be: nginx and the agent's webhook, nothing else)
docker compose exec juiceshop /nodejs/bin/node -e 'fetch("https://1.1.1.1").then(()=>console.log("OUT"),()=>console.log("blocked"))'
```

`bash tests/e2e.sh` from the repository root does all of this and more,
under its own project name and ports, and cleans up afterwards.
