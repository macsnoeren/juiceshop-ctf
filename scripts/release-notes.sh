#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Maurice Snoeren

# release-notes.sh: checks that a version may be released, and prints its
# release notes — the section of CHANGELOG.md for that version. The release
# workflow (.github/workflows/release.yml) runs it first; run it yourself
# before tagging, so a release does not stop half-way:
#
#   bash scripts/release-notes.sh v1.1.0
#
# A version may be released when:
#
#   - it is vMAJOR.MINOR.PATCH, optionally with a pre-release suffix
#     (v1.2.0-rc.1): the same string becomes the git tag, the version the
#     binaries print (-ldflags, internal/version), the VM's file names,
#     and — without the v — the Docker Hub tags;
#   - CHANGELOG.md has a heading "## MAJOR.MINOR.PATCH — <date>" for it, with
#     the date filled in: "— unreleased" means it is not finished. A
#     pre-release may use the heading of the version it leads up to;
#   - the module path in go.mod fits the major version: from v2 on it must
#     end in /vMAJOR, below v2 it must not, or `go install …@vN` fails. The
#     -ldflags in deploy/docker/Dockerfile.* must use that path: -X on a
#     path that does not exist is silently ignored, and the binaries would
#     call themselves "dev".
#
# Exit 0 and the notes on stdout, or exit 1 and the reason on stderr. On
# GitHub the reason is an ::error:: line, so it is an annotation on the run's
# summary page and not only a line deep in the log.
set -euo pipefail
cd "$(dirname "$0")/.."

die() {
    if [ "${GITHUB_ACTIONS:-}" = true ]; then
        echo "::error title=release-notes::$*" >&2
    else
        echo "release-notes: $*" >&2
    fi
    exit 1
}

tag="${1:-}"
if ! [[ "$tag" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)(-[0-9A-Za-z.-]+)?$ ]]; then
    die "'$tag' is not a version: write vMAJOR.MINOR.PATCH, e.g. v1.1.0 or v1.2.0-rc.1"
fi
major="${BASH_REMATCH[1]}"
version="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.${BASH_REMATCH[3]}"
pre="${BASH_REMATCH[4]}"

module="$(awk '$1 == "module" { print $2; exit }' go.mod)"
[ -n "$module" ] || die "go.mod has no module line"
if [ "$major" -ge 2 ]; then
    [[ "$module" == */v"$major" ]] \
        || die "$tag is major version $major, but go.mod says 'module $module': make it $module/v$major (go.mod, every import, the -ldflags in deploy/docker/Dockerfile.*)"
elif [[ "$module" =~ /v[0-9]+$ ]]; then
    die "$tag is below v2, but go.mod says 'module $module': a /vN suffix is for v2 and later"
fi
for f in deploy/docker/Dockerfile.node deploy/docker/Dockerfile.central; do
    grep -qF -- "-X $module/internal/version.Version=" "$f" \
        || die "$f does not set $module/internal/version.Version in its -ldflags: the binaries would print 'dev'"
done

heading="$(grep -m1 -E "^## ${version//./\\.}( |$)" CHANGELOG.md || true)"
if [ -z "$heading" ]; then
    die "CHANGELOG.md has no '## $version — <date>' section"
fi
if [ -z "$pre" ] && grep -qi 'unreleased' <<<"$heading"; then
    die "CHANGELOG.md still says '$heading': put the release date in it first (a pre-release, v$version-rc.1, may go before)"
fi

# The section: from its heading to the next "## ", without the heading.
awk -v h="$heading" '
    $0 == h { on = 1; next }
    on && /^## / { exit }
    on { print }
' CHANGELOG.md | sed -e '/./,$!d'
