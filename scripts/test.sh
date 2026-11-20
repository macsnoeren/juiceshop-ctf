#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Maurice Snoeren

# test.sh: everything that runs without Docker. Run before every commit.
#
#   bash scripts/test.sh          vet, unit tests with the race detector, gofmt
#
# The end-to-end test, which needs Docker, is scripts/e2e.sh.
set -euo pipefail
cd "$(dirname "$0")/.."

unformatted="$(gofmt -l cmd internal web)"
if [ -n "$unformatted" ]; then
    echo "gofmt needed on:"; echo "$unformatted"; exit 1
fi
go vet ./...
go test -race -count=1 ./...
echo "all tests passed"
