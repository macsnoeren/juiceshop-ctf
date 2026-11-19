#!/usr/bin/env bash
#
# start.sh — start the test setup (docker-compose.yml next to this file).
#
#   ./start.sh            instance 1 + central server, on :80
#   ./start.sh --second   plus instance 2, on :8080
#
# Builds the three images from the working tree first, so what runs is what is
# on disk now — the same rule as the node build. Waits until every container
# with a healthcheck is healthy, then prints where to go.
#
# Ports taken? CTF_HTTP_PORT=8000 CTF_HTTP_PORT_2=8001 ./start.sh
# Stopping: ./stop.sh (keeps the data) or ./stop.sh --wipe (forgets it).

set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"

# The images call themselves what git calls this commit (Makefile).
export VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"

profile=()
case "${1:-}" in
  "")       ;;
  --second) profile=(--profile second) ;;
  -h|--help) sed -n '3,14p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
  *)        echo "unknown option: $1 (try --help)" >&2; exit 2 ;;
esac

docker compose "${profile[@]}" up -d --build --wait --wait-timeout 180

# The first address of this machine, for a browser on another one. Falls back
# to localhost where 'hostname -I' does not exist (macOS).
ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
ip="${ip:-localhost}"
p1="${CTF_HTTP_PORT:-80}"
p2="${CTF_HTTP_PORT_2:-8080}"
url() { [[ "$2" == 80 ]] && printf 'http://%s' "$1" || printf 'http://%s:%s' "$1" "$2"; }

cat <<EOF

Juice Shop CTF is running.

  sign up      $(url "${ip}" "${p1}")/ctf/          access code: test1
  the shop     $(url "${ip}" "${p1}")/juiceshop/    after signing up
  leaderboard  $(url "${ip}" "${p1}")/leaderboard/  also /leaderboard/beamer
  admin        $(url "${ip}" "${p1}")/leaderboard/admin/   admin / admin
EOF
if [[ ${#profile[@]} -gt 0 ]]; then
  echo "  instance 2   $(url "${ip}" "${p2}")/ctf/          access code: test2"
fi
cat <<EOF

Logs: docker compose logs -f ctf-agent     Stop: ./stop.sh
EOF
