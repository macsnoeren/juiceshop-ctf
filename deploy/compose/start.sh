#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Maurice Snoeren

#
# start.sh — start the test setup (docker-compose.yml next to this file):
# the central server and three students' VMs, each with its Kali.
#
#   ./start.sh
#
# Builds the two images from the working tree first, so what runs is what is
# on disk now, versioned from git. Waits until every container with a
# healthcheck is healthy, then prints where to go.
#
# Ports taken? CTF_CENTRAL_PORT=9090 CTF_VM_PORT_1=9091 ... ./start.sh
# Stopping: ./stop.sh (keeps the data) or ./stop.sh --wipe (forgets it).

set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
case "${1:-}" in
  "") ;;
  -h|--help) sed -n '3,13p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
  *) echo "unknown option: $1 (try --help)" >&2; exit 2 ;;
esac

# The images call themselves what git calls this commit (Makefile).
export VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
docker compose build
docker compose up -d --wait --wait-timeout 240

c="${CTF_CENTRAL_PORT:-8090}"
cat <<EOF2

Juice Shop CTF $VERSION is running.

  central server   http://127.0.0.1:$c/juiceshop-ctf/            the games and their boards
  admin            http://127.0.0.1:$c/juiceshop-ctf/admin/      the first visit makes an account; then make a game
  VM 1             http://127.0.0.1:${CTF_VM_PORT_1:-8091}/ctf/
  VM 2             http://127.0.0.1:${CTF_VM_PORT_2:-8092}/ctf/
  VM 3             http://127.0.0.1:${CTF_VM_PORT_3:-8093}/ctf/

  As a student, from "Kali":  docker compose exec kali1 curl -s http://vm1/ctf/
  The VMs know the central server as http://ctf-central:8080/juiceshop-ctf.

Logs: docker compose logs -f vm1     Stop: ./stop.sh
EOF2
