#!/usr/bin/env bash
#
# stop.sh — stop the test setup started by start.sh.
#
#   ./stop.sh          stop and remove the containers; KEEP the data
#                      (sign-ups, the agents' state, the leaderboard)
#   ./stop.sh --wipe   the same, and delete the volumes: the next start is a
#                      fresh CTF with an empty leaderboard
#
# Both stop instance 2 as well, whether or not it was started: '--profile
# second' is always passed, because without it compose does not see those
# containers and leaves them running.

set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"

volumes=()
case "${1:-}" in
  "")        ;;
  --wipe)    volumes=(--volumes) ;;
  -h|--help) sed -n '3,13p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
  *)         echo "unknown option: $1 (try --help)" >&2; exit 2 ;;
esac

docker compose --profile second down --remove-orphans "${volumes[@]}"

if [[ ${#volumes[@]} -gt 0 ]]; then
  echo "Stopped, and the data is gone: the next ./start.sh begins with an empty leaderboard."
else
  echo "Stopped. The data is kept; ./stop.sh --wipe deletes it."
fi
