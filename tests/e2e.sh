#!/usr/bin/env bash
# e2e.sh: the whole CTF in Docker, end to end (DESIGN.md §12; TASKS.md T28,
# T30-T32).
#
# Brings up docker/docker-compose.yml with BOTH instances under its own
# project name and ports (so it never touches a setup you have running), then
# checks, in order:
#
#   door       /juiceshop/ redirects to /ctf/ before signing up
#   sign-up    both instances register; the gate cookie opens the shop
#   solve      one curl solves a challenge; it reaches the leaderboard
#   separate   a solve on instance 1 is not on instance 2's line (T28)
#   restore    `restart juiceshop`: the solve is back IN the shop, and the
#              leaderboard still has it (§6.2)
#   isolation  from inside juiceshop: the agent's API, the central server and
#              the internet are unreachable; the webhook answers GET with 405 (T31)
#   failure    central down: a solve arrives once it is back; agent down: the
#              door closes within 10 s and opens again with the same cookie (T32)
#   reset      /admin/ resets instance 2: its old cookie stops working and the
#              sign-up form is back (§8.4)
#
# It exits non-zero at the first failed check, names it, and shows the logs.
# Needs docker (with compose), curl and python3. Takes about two minutes.
#
#   bash tests/e2e.sh
#   E2E_KEEP=1 bash tests/e2e.sh     # leave the containers running afterwards

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
cd "$HERE/../docker"

PROJECT=juiceshop-ctf-e2e
export CTF_HTTP_PORT="${E2E_PORT:-18081}"
export CTF_HTTP_PORT_2="${E2E_PORT_2:-18082}"
B1="http://127.0.0.1:$CTF_HTTP_PORT"
B2="http://127.0.0.1:$CTF_HTTP_PORT_2"
WORK="$(mktemp -d)"
STEP="setup"

dc() { docker compose -p "$PROJECT" --profile second "$@"; }

cleanup() {
    local rc=$?
    if [ "$rc" -ne 0 ]; then
        echo
        echo "---- last log lines ----"
        dc logs --tail 15 ctf-agent ctf-status ctf-central nginx 2>/dev/null || true
    fi
    if [ -z "${E2E_KEEP:-}" ]; then
        dc down -v --remove-orphans >/dev/null 2>&1 || true
    fi
    rm -rf "$WORK"
    exit "$rc"
}
trap cleanup EXIT

step() { STEP="$1"; echo "== $1"; }
ok()   { echo "   ok: $1"; }
fail() { echo "FAIL [$STEP]: $1" >&2; exit 1; }

# status URL [curl args...] → HTTP status code
status() { local u="$1"; shift; curl -s -o /dev/null -w '%{http_code}' "$@" "$u"; }

# wait_for SECONDS DESCRIPTION COMMAND... : retry COMMAND once a second
wait_for() {
    local limit="$1" what="$2"; shift 2
    for _ in $(seq "$limit"); do
        if "$@" >/dev/null 2>&1; then ok "$what"; return 0; fi
        sleep 1
    done
    fail "$what (not within ${limit}s)"
}

# board NAME → "score solved" of NAME on the public leaderboard, or "absent"
board() {
    curl -s "$B1/leaderboard/api/v1/leaderboard" | python3 -c '
import json, sys
rows = [r for r in json.load(sys.stdin)["rows"] if r["name"] == sys.argv[1]]
print("%d %d" % (rows[0]["score"], rows[0]["solved"]) if rows else "absent")' "$1"
}
board_is() { [ "$(board "$1")" = "$2" ]; }

# register BASE JAR NAME CODE : sign up through the form, keep the cookies
register() {
    local base="$1" jar="$2" name="$3" code="$4" csrf
    csrf="$(curl -s -c "$jar" -b "$jar" "$base/ctf/" | sed -n 's/.*name="csrf" value="\([0-9a-f]*\)".*/\1/p' | head -1)"
    [ -n "$csrf" ] || fail "no CSRF token on $base/ctf/"
    local code_out
    code_out="$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' -c "$jar" -b "$jar" \
        --data-urlencode "csrf=$csrf" --data-urlencode "name=$name" --data-urlencode "code=$code" "$base/ctf/register")"
    case "$code_out" in
        "303 "*/juiceshop/) ok "registered '$name' on $base" ;;
        *) fail "registering on $base answered $code_out" ;;
    esac
}

shop_open()   { [ "$(status "$1/juiceshop/" -b "$2")" = 200 ]; }
shop_closed() { [ "$(status "$1/juiceshop/" -b "$2")" = 302 ]; }

# solved_in_shop BASE JAR KEY : Juice Shop itself says KEY is solved
solved_in_shop() {
    curl -s -b "$2" "$1/juiceshop/api/Challenges" | python3 -c '
import json, sys
d = json.load(sys.stdin)["data"]
sys.exit(0 if any(c["key"] == sys.argv[1] and c["solved"] for c in d) else 1)' "$3"
}

# ---------------------------------------------------------------------------

step "start (build, both instances)"
dc down -v --remove-orphans >/dev/null 2>&1 || true
dc up -d --build --wait --wait-timeout 240 >"$WORK/up.log" 2>&1 || { cat "$WORK/up.log"; fail "compose up"; }
wait_for 60 "instance 1 answers" test "$(status "$B1/ctf/")" = 200
wait_for 60 "instance 2 answers" test "$(status "$B2/ctf/")" = 200

step "door"
J1="$WORK/jar1"; J2="$WORK/jar2"; : >"$J1"; : >"$J2"
loc="$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' "$B1/juiceshop/")"
case "$loc" in "302 "*/ctf/) ok "/juiceshop/ without a cookie → /ctf/" ;; *) fail "/juiceshop/ without a cookie answered $loc" ;; esac
[ "$(status "$B1/assets/i18n/en.json")" = 302 ] || fail "/assets/ is not behind the door"
ok "/assets/ is behind the door too"

step "sign-up"
register "$B1" "$J1" "E2E One" test1
register "$B2" "$J2" "E2E Two" test2
shop_open "$B1" "$J1" || fail "instance 1 shop closed after sign-up"
shop_open "$B2" "$J2" || fail "instance 2 shop closed after sign-up"
curl -s -b "$J1" "$B1/juiceshop/" | grep -q '<title>OWASP Juice Shop' || fail "not the Juice Shop page"
[ "$(status "$B1/assets/i18n/en.json" -b "$J1")" = 200 ] || fail "translations not served"
ok "both shops open with their cookie"
# a cookie of instance 1 does not open instance 2 (another secret, another registration)
shop_closed "$B2" "$J1" || fail "instance 1's cookie opens instance 2"
ok "instance 1's cookie does not open instance 2"

step "solve"
[ "$(status "$B1/juiceshop/ftp/acquisitions.md" -b "$J1")" = 200 ] || fail "could not fetch the confidential document"
wait_for 20 "the solve is on the leaderboard (100 points)" board_is "E2E One" "100 1"

step "separate (T28)"
board_is "E2E Two" "0 0" || fail "instance 2 shows $(board "E2E Two"), want 0 0"
ok "instance 2 is on the board with 0"

step "no continue codes from students (ctf-instance.conf)"
# The attack itself: take student 1's continue code and apply it on instance 2.
# Left open, student 2 would get all of student 1's points — and the same PUT is
# what Juice Shop's frontend sends from its cookie after a reset.
cc="$(curl -s -b "$J1" "$B1/juiceshop/rest/continue-code" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("continueCode",""))')"
[ -n "$cc" ] || fail "no continue code for instance 1"
for p in rest/continue-code/apply REST/Continue-Code/apply rest/continue-code-findIt/apply rest/continue-code-fixIt/apply; do
    s="$(status "$B2/juiceshop/$p/$cc" -X PUT -b "$J2")"
    [ "$s" = 403 ] || fail "PUT /juiceshop/$p/… answered $s, want 403"
done
ok "applying a continue code through nginx is refused (4 spellings)"
sleep 3
board_is "E2E Two" "0 0" || fail "instance 2 got points from instance 1's code: $(board "E2E Two")"
ok "instance 2 still has 0 points"

step "restore after a Juice Shop restart"
dc restart juiceshop >/dev/null 2>&1
wait_for 60 "Juice Shop is back" shop_open "$B1" "$J1"
wait_for 30 "the solve is restored in Juice Shop" solved_in_shop "$B1" "$J1" directoryListingChallenge
board_is "E2E One" "100 1" || fail "leaderboard changed after the restart: $(board "E2E One")"
ok "the leaderboard is unchanged"

step "isolation (T31)"
probe='
const probe = async (u, o) => {
  try { const r = await fetch(u, { ...o, signal: AbortSignal.timeout(4000) }); return "HTTP " + r.status; }
  catch (e) { return "BLOCKED"; }
};
(async () => console.log(JSON.stringify({
  agentApi:        await probe("http://ctf-agent:8082/healthz"),
  agentApiOnHook:  await probe("http://ctf-agent-hook:8082/healthz"),
  agentApiByAlias: await probe("http://ctf-agent-api:8082/healthz"),
  central:         await probe("http://ctf-central:8080/leaderboard/healthz"),
  status:          await probe("http://ctf-status:8080/ctf/healthz"),
  internet:        await probe("https://1.1.1.1/"),
  webhookGet:      await probe("http://ctf-agent-hook:8081/webhook"),
})))();'
result="$(dc exec -T juiceshop /nodejs/bin/node -e "$probe")"
echo "   from juiceshop: $result"
python3 - "$result" <<'EOF' || fail "juiceshop can reach something it must not"
import json, sys
r = json.loads(sys.argv[1])
want = {"agentApi": "BLOCKED", "agentApiOnHook": "BLOCKED", "agentApiByAlias": "BLOCKED",
        "central": "BLOCKED", "status": "BLOCKED", "internet": "BLOCKED", "webhookGet": "HTTP 405"}
bad = {k: r.get(k) for k in want if r.get(k) != want[k]}
if bad:
    print("   unexpected:", bad)
    sys.exit(1)
EOF
ok "agent API, central, status page and internet unreachable; webhook refuses GET"

step "failure: central server down (T32)"
dc stop ctf-central >/dev/null 2>&1
[ "$(status "$B1/juiceshop/metrics" -b "$J1")" = 200 ] || fail "could not fetch /metrics"
sleep 3
shop_open "$B1" "$J1" || fail "the shop closed while only the central server was down"
ok "the shop stays open while the central server is down"
dc start ctf-central >/dev/null 2>&1
wait_for 45 "the solve made during the outage arrives (200 points)" board_is "E2E One" "200 2"

step "failure: agent down (T32)"
dc stop ctf-agent >/dev/null 2>&1
wait_for 10 "the door closes without the agent" shop_closed "$B1" "$J1"
dc start ctf-agent >/dev/null 2>&1
wait_for 30 "the door opens again with the same cookie" shop_open "$B1" "$J1"

step "reset from /admin/ (§8.4)"
AJ="$WORK/adminjar"; : >"$AJ"
page="$(curl -s -u admin:admin -c "$AJ" -b "$AJ" "$B1/leaderboard/admin/")"
csrf="$(printf '%s' "$page" | sed -n 's/.*name="csrf" value="\([0-9a-f]*\)".*/\1/p' | head -1)"
pid="$(curl -s "$B1/leaderboard/api/v1/leaderboard" | python3 -c '
import json, sys
print([r["participant"] for r in json.load(sys.stdin)["rows"] if r["name"] == "E2E Two"][0])')"
[ -n "$csrf" ] && [ -n "$pid" ] || fail "no CSRF token or participant id on the admin page"
[ "$(status "$B1/leaderboard/admin/participants/$pid/reset" -u admin:admin -b "$AJ" --data-urlencode "csrf=$csrf")" = 303 ] \
    || fail "reset was not accepted"
wait_for 10 "E2E Two is off the board" board_is "E2E Two" "absent"
wait_for 45 "instance 2's old cookie no longer opens the shop" shop_closed "$B2" "$J2"
curl -s "$B2/ctf/" | grep -q 'action="/ctf/register"' || fail "instance 2 does not show the sign-up form again"
ok "instance 2 shows the sign-up form again"
board_is "E2E One" "200 2" || fail "the reset touched another participant"
ok "instance 1 is untouched"

echo
echo "ALL PASSED"
