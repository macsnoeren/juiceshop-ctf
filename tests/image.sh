#!/usr/bin/env bash
# image.sh: the juiceshop-ctf image on its own, checked from outside
# (TASK-1-1 T1.1-31, T1.1-38, T1.1-39; docs/VERSION-1-1.md §3.1).
#
#   run         `docker run` exactly as §3.1 says; /ctf/ and /juiceshop/ answer
#   size        the image is Juice Shop's plus less than 20 MB
#   users       keeper uid 0, ctf-node uid 10001 with NO capabilities, Juice
#               Shop uid 65532
#   isolation   as 65532 (code in Juice Shop): node.db cannot be read,
#               ctf-node and the keeper cannot be signalled; the webhook is
#               not reachable from outside the container's loopback
#   respawn     killing Juice Shop from inside: the keeper starts it again and
#               ctf-node puts the progress back
#   refuse      without SETUID/SETGID the container does not start; ctf-node
#               -serve as root refuses to run
#
# Needs docker and python3; builds the image first. About two minutes.
#
#   bash tests/image.sh
set -euo pipefail
cd "$(dirname "$0")/.."

IMG=juiceshop-ctf:image-test
C=juiceshop-ctf-image-test
PORT="${IMAGE_TEST_PORT:-18400}"
B="http://127.0.0.1:$PORT"
NET=juiceshop-ctf-image-test
JAR="$(mktemp)"
STEP=setup

cleanup() {
    local rc=$?
    if [ "$rc" -ne 0 ]; then echo "---- logs"; docker logs --tail 30 "$C" 2>&1 || true; fi
    docker rm -f "$C" "$C-refuse" >/dev/null 2>&1 || true
    docker network rm "$NET" >/dev/null 2>&1 || true
    docker volume rm "$C" >/dev/null 2>&1 || true
    rm -f "$JAR"
    exit "$rc"
}
trap cleanup EXIT
step() { STEP="$1"; echo "== $1"; }
ok()   { echo "   ok: $1"; }
fail() { echo "FAIL [$STEP]: $1" >&2; exit 1; }
wait_for() {
    local limit="$1" what="$2"; shift 2
    for _ in $(seq "$limit"); do if "$@" >/dev/null 2>&1; then ok "$what"; return 0; fi; sleep 1; done
    fail "$what (not within ${limit}s)"
}
status() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
# as UID: run a node.js snippet inside the container as that user
as() { local u="$1"; shift; docker exec -u "$u" "$C" /nodejs/bin/node -e "$1"; }
healthy() { [ "$(docker inspect -f '{{.State.Health.Status}}' "$C")" = healthy ]; }
is_shop() { local o; o="$(curl -s "$B/juiceshop/")"; grep -q '<title>OWASP Juice Shop' <<<"$o"; }
page_has() { local o; o="$(curl -s "$B/ctf/")"; grep -qF -- "$1" <<<"$o"; }
unsolved() { ! solved "$1"; }
nothing_solved() {
    curl -s "$B/juiceshop/api/Challenges" | python3 -c '
import json, sys
sys.exit(0 if not any(c["solved"] for c in json.load(sys.stdin)["data"]) else 1)'
}
solved() {
    curl -s "$B/juiceshop/api/Challenges" | python3 -c '
import json, sys
sys.exit(0 if any(c["key"] == sys.argv[1] and c["solved"] for c in json.load(sys.stdin)["data"]) else 1)' "$1"
}

step "build"
docker build -q -f deploy/docker/Dockerfile.node --build-arg VERSION="$(git describe --tags --always --dirty)" -t "$IMG" . >/dev/null
docker rm -f "$C" >/dev/null 2>&1 || true
docker network create "$NET" >/dev/null

step "run (as VERSION-1-1.md §3.1, without a central server)"
docker run -d --name "$C" --network "$NET" -p "127.0.0.1:$PORT:80" -v "$C:/var/lib/ctf" \
    -e CTF_CENTRAL_URL= -e POLL_INTERVAL=5s \
    --cap-drop ALL --cap-add SETUID --cap-add SETGID --security-opt no-new-privileges "$IMG" >/dev/null
wait_for 90 "healthy" healthy
[ "$(status "$B/ctf/")" = 200 ] || fail "/ctf/ is not 200"
wait_for 60 "/juiceshop/ is the Juice Shop" is_shop
docker run --rm "$IMG" -version | grep -q '^ctf-node v' || fail "-version"
ok "$(docker run --rm "$IMG" -version)"

step "size"
# Against the base image, named by the Dockerfile's last FROM so the tag is
# kept in one place. Pulled explicitly: a build leaves the base behind as a
# tagged image with the classic image store, NOT with the containerd image
# store of GitHub's runners, and `docker image inspect` then fails on it.
BASE="$(awk '/^FROM /{b=$2} END{print b}' deploy/docker/Dockerfile.node)"
docker image inspect "$BASE" >/dev/null 2>&1 || docker pull -q "$BASE" >/dev/null || fail "cannot pull $BASE"
python3 - "$IMG" "$BASE" <<'EOF' || fail "the image is too large"
import subprocess, sys
a, b = [int(subprocess.check_output(["docker", "image", "inspect", "-f", "{{.Size}}", i]).decode()) for i in sys.argv[1:3]]
print("   %s + %.2f MB" % (sys.argv[2], (a - b) / 1e6))
sys.exit(0 if a - b < 20e6 else 1)
EOF

step "users (T1.1-31)"
procs="$(as 65532 '
const fs = require("fs");
for (const p of fs.readdirSync("/proc").filter(d => /^\d+$/.test(d))) {
  try {
    const s = fs.readFileSync("/proc/" + p + "/status", "utf8");
    const cmd = fs.readFileSync("/proc/" + p + "/cmdline", "utf8").split("\0").join(" ").trim();
    const uid = s.match(/^Uid:\s+(\d+)/m)[1], cap = s.match(/^CapEff:\s+(\w+)/m)[1], nnp = s.match(/^NoNewPrivs:\s+(\d)/m)[1];
    console.log([p, uid, cap, nnp, cmd].join("|"));
  } catch (e) {}
}')"
echo "$procs" | sed 's/^/   /'
echo "$procs" | grep -q '^1|0|00000000000000c0|1|/ctf-node$' || fail "PID 1 is not the keeper as root with SETUID+SETGID only"
echo "$procs" | grep -q '|10001|0000000000000000|1|/ctf-node -serve$' || fail "ctf-node -serve is not uid 10001 with no capabilities"
echo "$procs" | grep -q '|65532|0000000000000000|1|/nodejs/bin/node build/app.js$' || fail "Juice Shop is not uid 65532 with no capabilities"
ok "keeper 0 (SETUID, SETGID), ctf-node 10001 (none), Juice Shop 65532 (none); no-new-privileges on all"
SERVE_PID="$(echo "$procs" | grep -- '-serve$' | cut -d'|' -f1)"
JS_PID="$(echo "$procs" | grep 'app.js$' | cut -d'|' -f1)"

step "join (locally) and solve"
csrf="$(curl -s -c "$JAR" -b "$JAR" "$B/ctf/" | sed -n 's/.*name="csrf" value="\([0-9a-f]*\)".*/\1/p' | head -1)"
[ "$(status -c "$JAR" -b "$JAR" --data-urlencode "csrf=$csrf" --data-urlencode "name=Image Test" "$B/ctf/join")" = 200 ] || fail "join"
[ "$(status "$B/juiceshop/ftp/acquisitions.md")" = 200 ] || fail "solving"
wait_for 20 "the page shows the solve" page_has '<span class="num">100</span>'

step "isolation (T1.1-39)"
result="$(as 65532 "
const fs = require('fs'), net = require('net');
const r = {};
try { fs.readFileSync('/var/lib/ctf/node.db'); r.read = 'READ'; } catch (e) { r.read = e.code; }
try { fs.readdirSync('/var/lib/ctf'); r.list = 'LISTED'; } catch (e) { r.list = e.code; }
for (const [k, p] of [['node', $SERVE_PID], ['keeper', 1]]) {
  try { process.kill(p, 'SIGTERM'); r[k] = 'SIGNALLED'; } catch (e) { r[k] = e.code; }
}
console.log(JSON.stringify(r));")"
echo "   as 65532: $result"
[ "$result" = '{"read":"EACCES","list":"EACCES","node":"EPERM","keeper":"EPERM"}' ] || fail "Juice Shop's user reaches ctf-node"
ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$C")"
from_outside="$(docker run --rm --network "$NET" curlimages/curl:8.10.1 -s -o /dev/null -w '%{http_code}' --max-time 5 -X POST "http://$ip:8081/webhook" || true)"
[ "$from_outside" = 000 ] || fail "the webhook answers from outside the container ($from_outside)"
[ "$(docker run --rm --network "$NET" curlimages/curl:8.10.1 -s -o /dev/null -w '%{http_code}' --max-time 5 "http://$ip/ctf/")" = 200 ] || fail "port 80 is not reachable from outside"
ok "node.db unreadable, ctf-node and the keeper not signallable, webhook on loopback only"

step "respawn: Juice Shop killed from inside"
as 65532 "process.kill($JS_PID, 'SIGKILL')"
wait_for 20 "Juice Shop is gone" unsolved directoryListingChallenge
wait_for 60 "the keeper started it again and ctf-node restored the solve" solved directoryListingChallenge
[ "$(docker inspect -f '{{.RestartCount}}' "$C")" = 0 ] || fail "the container restarted"

step "Restart Juice Shop button"
csrf="$(curl -s -c "$JAR" -b "$JAR" "$B/ctf/" | sed -n 's/.*name="csrf" value="\([0-9a-f]*\)".*/\1/p' | head -1)"
[ "$(status -c "$JAR" -b "$JAR" --data-urlencode "csrf=$csrf" "$B/ctf/restart")" = 200 ] || fail "restart"
wait_for 60 "the solve is back after the button" solved directoryListingChallenge

step "Forget: node.db empty, Juice Shop empty"
[ "$(status -c "$JAR" -b "$JAR" --data-urlencode "csrf=$csrf" --data-urlencode confirm=forget "$B/ctf/forget")" = 200 ] || fail "forget"
rows="$(as 10001 "
const sqlite3 = require('/juice-shop/node_modules/sqlite3');
const db = new sqlite3.Database('/var/lib/ctf/node.db', sqlite3.OPEN_READONLY);
const t = ['membership','outbox','solves','codes','baseline','standing','status','commands_applied','sessions','gate_failures'];
db.all(t.map(x => 'SELECT ' + JSON.stringify(x) + ' AS t, COUNT(*) AS n FROM ' + x).join(' UNION ALL '), (e, r) => {
  if (e) { console.log('ERR ' + e); return; }
  console.log(r.filter(x => x.n > 0).map(x => x.t + '=' + x.n).join(' ') || 'empty');
});")"
[ "$rows" = empty ] || fail "student rows left in node.db: $rows"
wait_for 60 "Juice Shop is back, with nothing solved" nothing_solved

step "refuse to run privileged"
docker run -d --name "$C-refuse" --cap-drop ALL --security-opt no-new-privileges "$IMG" >/dev/null
for _ in $(seq 20); do [ "$(docker inspect -f '{{.State.Running}}' "$C-refuse")" = false ] && break; sleep 1; done
[ "$(docker inspect -f '{{.State.Running}} {{.State.ExitCode}}' "$C-refuse")" = "false 1" ] || fail "it ran without SETUID/SETGID"
docker logs "$C-refuse" 2>&1 | grep -q 'CANNOT START ctf-node AS UID 10001' || fail "no clear message without SETUID/SETGID"
ok "without SETUID/SETGID: stops with a clear message"
out="$(docker run --rm -e CTF_KEEPER=1 "$IMG" -serve 2>&1 || true)"
echo "$out" | grep -q 'REFUSING TO START' || fail "ctf-node -serve ran as root"
ok "ctf-node -serve refuses to run as root"

echo
echo "ALL PASSED"
