#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Maurice Snoeren

#
# stop.sh — stop the test setup.
#
#   ./stop.sh          stop and remove the containers; the games, the VMs'
#                      node.db and the central database are KEPT (volumes)
#   ./stop.sh --wipe   the same, and delete the volumes too: every game,
#                      every join, every solve, gone
#
# The images stay; ./start.sh rebuilds what changed.

set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"

case "${1:-}" in
  "")        docker compose down --remove-orphans ;;
  --wipe)    docker compose down --volumes --remove-orphans ;;
  -h|--help) sed -n '3,11p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
  *)         echo "unknown option: $1 (try --help)" >&2; exit 2 ;;
esac
