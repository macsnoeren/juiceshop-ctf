#!/usr/bin/env bash
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
#     binaries print (-ldflags, internal/shared/version), the VM's file names,
#     and — without the v — the Docker Hub tags;
#   - CHANGELOG.md has a heading "## MAJOR.MINOR.PATCH — <date>" for it, with
#     the date filled in: "— unreleased" means it is not finished. A
#     pre-release may use the heading of the version it leads up to.
#
# Exit 0 and the notes on stdout, or exit 1 and the reason on stderr.
set -euo pipefail
cd "$(dirname "$0")/.."

tag="${1:-}"
if ! [[ "$tag" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)(-[0-9A-Za-z.-]+)?$ ]]; then
    echo "release-notes: '$tag' is not a version: write vMAJOR.MINOR.PATCH, e.g. v1.1.0 or v1.2.0-rc.1" >&2
    exit 1
fi
version="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.${BASH_REMATCH[3]}"
pre="${BASH_REMATCH[4]}"

heading="$(grep -m1 -E "^## ${version//./\\.}( |$)" CHANGELOG.md || true)"
if [ -z "$heading" ]; then
    echo "release-notes: CHANGELOG.md has no '## $version — <date>' section" >&2
    exit 1
fi
if [ -z "$pre" ] && grep -qi 'unreleased' <<<"$heading"; then
    echo "release-notes: CHANGELOG.md still says '$heading': put the release date in it first" >&2
    exit 1
fi

# The section: from its heading to the next "## ", without the heading.
awk -v h="$heading" '
    $0 == h { on = 1; next }
    on && /^## / { exit }
    on { print }
' CHANGELOG.md | sed -e '/./,$!d'
