#!/usr/bin/env bash
# e2e.sh: the CTF 1.1 end to end, in Docker (TASK-1-1 T1.1-53..55).
#
# Brings up deploy/compose/docker-compose.yml under its own project name and
# ports (so it never touches a setup you have running): the central server
# and three VMs, each with its Kali. Every request a student makes goes from
# that student's Kali container to their own VM, as in the lab. Then, in order:
#
#   games      two games made in admin; VM 1 and 2 join A, VM 3 joins B
#   solves     solves on each VM reach their own game's board, and only that
#   board      every student stays on the board throughout — the lab's F1
#   cookies    without any cookie the page and the shop are the same
#   restart    "Restart Juice Shop": the solves are back in the shop
#   codes      VM 1's continue code is refused on VM 2, whose score stays
#   forget     VM 2 forgets: the shop empty, "left" in admin
#   offline    central server down: VM 2 joins B and solves; when it is back,
#              the join and the solve arrive
#   remove     admin removes VM 3's student: the VM forgets by itself
#   clear      admin clears game B: VM 2 forgets by itself; A is untouched
#   flags      the honest play above raised NO flag (T1.1-55) ...
#   copy       ... and a copy of VM 1, made after it joined, is flagged
#
# Exits non-zero at the first failed check, names it, and shows the logs.
# Needs docker (with compose) and python3. About five minutes.
#
#   bash tests/e2e.sh
#   E2E_KEEP=1 bash tests/e2e.sh     # leave it running afterwards
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
cd "$HERE/../deploy/compose"

PROJECT=juiceshop-ctf-e2e
export CTF_CENTRAL_PORT="${E2E_PORT:-18190}"
export CTF_VM_PORT_1=18191 CTF_VM_PORT_2=18192 CTF_VM_PORT_3=18193
export VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)-e2e"
C="http://127.0.0.1:$CTF_CENTRAL_PORT/juiceshop-ctf"
WORK="$(mktemp -d)"
STEP=setup

dc() { docker compose -p "$PROJECT" "$@"; }

cleanup() {
    local rc=$?
    if [ "$rc" -ne 0 ]; then
        echo; echo "---- last log lines ----"
        dc logs --tail 12 ctf-central vm1 vm2 vm3 2>/dev/null | grep -v ' info: \| warn: ' || true
        docker logs --tail 12 "$PROJECT-copy" 2>/dev/null || true
    fi
    if [ -z "${E2E_KEEP:-}" ]; then
        docker rm -f "$PROJECT-copy" >/dev/null 2>&1 || true
        dc down -v --remove-orphans >/dev/null 2>&1 || true
        docker volume rm "${PROJECT}_vmcopy" >/dev/null 2>&1 || true
    fi
    rm -rf "$WORK"
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

# ---- the student side: everything from that student's Kali ---------------

# kali N ARGS... : curl in kaliN, with that student's cookie jar
kali() { local n="$1"; shift; dc exec -T "kali$n" curl -s -b /tmp/jar -c /tmp/jar "$@"; }
kali_nojar() { local n="$1"; shift; dc exec -T "kali$n" curl -s "$@"; }
page() { kali "$1" "http://vm$1/ctf/"; }
csrf_of() { page "$1" | sed -n 's/.*name="csrf" value="\([0-9a-f]*\)".*/\1/p' | head -1; }
# post N PATH FIELD=VALUE... : a form on VM N's page; prints the HTTP status
post() {
    local n="$1" path="$2"; shift 2
    local args=(--data-urlencode "csrf=$(csrf_of "$n")")
    for f in "$@"; do args+=(--data-urlencode "$f"); done
    kali "$n" -o /dev/null -w '%{http_code}' "${args[@]}" "http://vm$n$path"
}
shop() { local n="$1"; shift; kali "$n" "http://vm$n/juiceshop/$*"; }
solve() { [ "$(kali "$1" -o /dev/null -w '%{http_code}' "http://vm$1/juiceshop/$2")" = 200 ] || fail "solving $2 on VM $1"; }
in_shop() {
    shop "$1" api/Challenges | python3 -c '
import json, sys
d = json.load(sys.stdin)["data"]
sys.exit(0 if set(sys.argv[1:]) <= {c["key"] for c in d if c["solved"]} else 1)' "${@:2}"
}
shop_empty() {
    shop "$1" api/Challenges | python3 -c '
import json, sys
sys.exit(0 if not any(c["solved"] for c in json.load(sys.stdin)["data"]) else 1)'
}
# Output is captured before grep: `curl | grep -q` under pipefail fails when
# grep stops reading early and curl gets SIGPIPE on a large page.
has() { local out; out="$(cat)"; grep -qF -- "$1" <<<"$out"; }
shows() { page "$1" | has "$2"; }

# ---- the operator side: admin, from this machine ---------------------------

AJ="$WORK/admin.jar"; : >"$AJ"
admin_csrf() { curl -s -b "$AJ" -c "$AJ" "$C/admin/" | sed -n 's/.*name="csrf" value="\([0-9a-f]*\)".*/\1/p' | head -1; }
# admin PATH FIELD=VALUE... : prints the page answered
admin() {
    local path="$1"; shift
    local args=(--data-urlencode "csrf=$(admin_csrf)")
    for f in "$@"; do args+=(--data-urlencode "$f"); done
    curl -s -b "$AJ" -c "$AJ" "${args[@]}" "$C/admin$path"
}
# game NAME : creates and starts a game; sets GAME_ID and GAME_CODE
game() {
    local out; out="$(admin /games "name=$1")"
    GAME_CODE="$(printf '%s' "$out" | sed -n 's/.*<p class="joincode">\([a-z0-9-]*\)<\/p>.*/\1/p')"
    GAME_ID="$(printf '%s' "$out" | sed -n 's/.*action="\/juiceshop-ctf\/admin\/games\/\([0-9]*\)\/state".*/\1/p' | head -1)"
    [ -n "$GAME_CODE" ] && [ -n "$GAME_ID" ] || fail "creating game $1"
    admin "/games/$GAME_ID/state" to=running >/dev/null
}
# board SLUG : "name=score,name=score" of the game's board, by name
board() {
    curl -s "$C/g/$1/board.json" | python3 -c '
import json, sys
print(",".join("%s=%d" % (r["name"], r["score"]) for r in sorted(json.load(sys.stdin)["rows"], key=lambda r: r["name"])))'
}
board_is() { [ "$(board "$1")" = "$2" ]; }
pid_of() {
    curl -s "$C/g/$1/board.json" | python3 -c '
import json, sys
print([r["participant"] for r in json.load(sys.stdin)["rows"] if r["name"] == sys.argv[1]][0])' "$2"
}
flags() { curl -s "$C/admin/events.csv?kind=flag" | tail -n +2; }
status_in_admin() { curl -s "$C/admin/games/$1" | has "<span class=\"badge st-$2\">$2</span>"; }
copied() { flags | has two_boots; }

# ---------------------------------------------------------------------------

step "start"
dc down -v --remove-orphans >/dev/null 2>&1 || true
dc build >"$WORK/build.log" 2>&1 || { tail -30 "$WORK/build.log"; fail "build"; }
dc up -d --wait --wait-timeout 240 >"$WORK/up.log" 2>&1 || { cat "$WORK/up.log"; fail "compose up"; }
for n in 1 2 3; do
    wait_for 60 "VM $n answers its Kali" shows "$n" "Join the CTF"
done
[ "$(kali_nojar 1 -o /dev/null -w '%{http_code}' --max-time 3 http://vm2/ctf/ || true)" = 000 ] || fail "Kali 1 reaches VM 2"
ok "each Kali reaches only its own VM"

step "games and joins (T1.1-53)"
game "Game A"; A_ID=$GAME_ID; A_CODE=$GAME_CODE
game "Game B"; B_ID=$GAME_ID; B_CODE=$GAME_CODE
ok "two games: A ($A_CODE), B ($B_CODE)"
[ "$(post 1 /ctf/join "name=Anna" "code=$A_CODE")" = 200 ] || fail "VM 1 joins A"
[ "$(post 2 /ctf/join "name=Bram" "code=$(echo "$A_CODE" | tr a-z A-Z)")" = 200 ] || fail "VM 2 joins A (code in capitals)"
[ "$(post 3 /ctf/join "name=Chloé" "code=$B_CODE")" = 200 ] || fail "VM 3 joins B"
shows 1 "Anna" && shows 1 "Game A" || fail "VM 1's page"
board_is game-a "Anna=0,Bram=0" || fail "board A: $(board game-a)"
board_is game-b "Chloé=0" || fail "board B: $(board game-b)"
ok "all three on their own game's board"

step "solves"
solve 1 ftp/acquisitions.md                 # 1 star
solve 1 metrics                             # 1 star
solve 2 ftp/acquisitions.md
solve 3 'ftp/eastere.gg%2500.md'            # null byte + easter egg: 4 + 4 stars
wait_for 40 "board A: Anna 200, Bram 100" board_is game-a "Anna=200,Bram=100"
wait_for 40 "board B: Chloé 1400, alone" board_is game-b "Chloé=1400"
shows 1 '<span class="num">200</span>' || fail "VM 1's page does not show its score"
curl -s "$C/g/game-a/" | has 'first blood' || fail "no first blood on board A"
curl -s "$C/g/game-b/chart.svg" | has '<polyline' || fail "no chart on board B"
ok "pages: score on the VM, first blood and chart on the board"

step "no cookies (T1.1-54)"
kali_nojar 1 http://vm1/ctf/ | has "Anna" || fail "without cookies the page forgets the student"
[ "$(kali_nojar 1 -o /dev/null -w '%{http_code}' 'http://vm1/juiceshop/rest/products/search?q=apple')" = 200 ] || fail "without cookies the shop is closed"
ok "no cookie needed for the page or the shop"

step "restart Juice Shop"
[ "$(post 1 /ctf/restart)" = 200 ] || fail "restart"
wait_for 60 "VM 1's solves are back in its shop" in_shop 1 directoryListingChallenge exposedMetricsChallenge
board_is game-a "Anna=200,Bram=100" || fail "the board changed after the restart: $(board game-a)"

step "a continue code from another VM"
cc="$(shop 1 rest/continue-code | python3 -c 'import json,sys; print(json.load(sys.stdin).get("continueCode",""))')"
[ -n "$cc" ] || fail "no continue code on VM 1"
for p in rest/continue-code/apply REST/Continue-Code/apply rest/continue-code-findIt/apply rest/continue-code-fixIt/apply; do
    s="$(kali 2 -o /dev/null -w '%{http_code}' -X PUT "http://vm2/juiceshop/$p/$cc")"
    [ "$s" = 403 ] || fail "PUT $p on VM 2 answered $s"
done
sleep 6
board_is game-a "Anna=200,Bram=100" || fail "VM 2 got points from VM 1's code: $(board game-a)"
ok "refused in four spellings; VM 2 still has 100"

step "forget"
[ "$(post 2 /ctf/forget confirm=forget)" = 200 ] || fail "forget"
shows 2 "Join the CTF" || fail "VM 2 still knows its student"
wait_for 60 "VM 2's shop is empty" shop_empty 2
wait_for 10 "Bram is off board A, Anna stays" board_is game-a "Anna=200"
status_in_admin "$A_ID" left || fail "admin does not show Bram as left"
ok "admin shows Bram as left"

step "offline join"
dc stop ctf-central >/dev/null 2>&1
[ "$(post 2 /ctf/join "name=Bram" "code=$B_CODE")" = 200 ] || fail "joining while the central server is down"
shows 2 "Waiting for the central server" || fail "VM 2 does not say the join waits"
solve 2 metrics
wait_for 20 "VM 2 counts the solve locally" shows 2 '<span class="num">1<small>/'
dc start ctf-central >/dev/null 2>&1
wait_for 60 "the queued join and its solve arrive on board B" board_is game-b "Bram=100,Chloé=1400"
board_is game-a "Anna=200" || fail "board A changed: $(board game-a)"

step "remove a participant"
admin "/participants/$(pid_of game-b Chloé)/remove" "confirm=Game B" >/dev/null
wait_for 10 "Chloé is off board B" board_is game-b "Bram=100"
wait_for 40 "VM 3 forgot by itself" shows 3 "Join the CTF"
wait_for 60 "VM 3's shop is empty" shop_empty 3
status_in_admin "$B_ID" removed || fail "admin does not show Chloé as removed"

step "clear a game"
admin "/games/$B_ID/clear" "confirm=Game B" >/dev/null
wait_for 10 "board B is empty" board_is game-b ""
wait_for 40 "VM 2 forgot by itself" shows 2 "Join the CTF"
board_is game-a "Anna=200" || fail "clearing B touched A: $(board game-a)"
ok "game A untouched"

step "no false flags (T1.1-55)"
f="$(flags)"
[ -z "$f" ] || fail "honest play raised flags: $f"
ok "the feed holds no flag"

step "a copied VM is flagged"
dc stop vm1 >/dev/null 2>&1
docker volume create "${PROJECT}_vmcopy" >/dev/null
docker run --rm -v "${PROJECT}_vm1:/from" -v "${PROJECT}_vmcopy:/to" alpine:3.24 cp -a /from/. /to/
dc start vm1 >/dev/null 2>&1
docker run -d --name "$PROJECT-copy" --network "${PROJECT}_uplink" -v "${PROJECT}_vmcopy:/var/lib/ctf" \
    -e CTF_CENTRAL_URL=http://ctf-central:8080/juiceshop-ctf -e POLL_INTERVAL=5s -e REPORT_INTERVAL=10s \
    --cap-drop ALL --cap-add SETUID --cap-add SETGID --security-opt no-new-privileges juiceshop-ctf:latest >/dev/null
wait_for 120 "the copy is flagged: one token, two Juice Shop boots" copied
[ "$(flags | wc -l)" = 1 ] || fail "more than the one expected flag: $(flags)"
board_is game-a "Anna=200" || fail "the copy changed the score: $(board game-a)"

echo
echo "ALL PASSED"
