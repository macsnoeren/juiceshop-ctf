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
# Ports taken? CTF_CENTRAL_PORT=9090 CTF_VM_PORT_1=9091 CTF_VM_PAGE_PORT_1=9191 ... ./start.sh
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
code="$(docker compose logs ctf-central 2>/dev/null | sed -n 's/.*setup_code=\([a-z0-9-]*\).*/\1/p' | tail -1)"
cat <<EOF2

Juice Shop CTF $VERSION is running.

  central server   http://127.0.0.1:$c/juiceshop-ctf/            the games and their boards
  admin            http://127.0.0.1:$c/juiceshop-ctf/admin/      ${code:+setup code $code: }make the first account; then a game
  VM 1             http://127.0.0.1:${CTF_VM_PAGE_PORT_1:-8191}/ctf/   its shop: http://127.0.0.1:${CTF_VM_PORT_1:-8091}/juiceshop/
  VM 2             http://127.0.0.1:${CTF_VM_PAGE_PORT_2:-8192}/ctf/   its shop: http://127.0.0.1:${CTF_VM_PORT_2:-8092}/juiceshop/
  VM 3             http://127.0.0.1:${CTF_VM_PAGE_PORT_3:-8193}/ctf/   its shop: http://127.0.0.1:${CTF_VM_PORT_3:-8093}/juiceshop/

  As a student, from "Kali":  docker compose exec kali1 curl -s http://vm1:8080/ctf/
  The VMs know the central server as http://ctf-central:8080/juiceshop-ctf.

Logs: docker compose logs -f vm1     Stop: ./stop.sh
EOF2
