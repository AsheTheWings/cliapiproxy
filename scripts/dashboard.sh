#!/bin/sh
# Verifies the management dashboard and builds its page for the server to
# embed: installs the locked dependencies, runs the Bun tests, lint, the type
# check, and the production build, then copies the page to
# internal/dashboard/dist/management.html.
#
# usage: scripts/dashboard.sh [version]
#
# The version defaults to 0.0.0 and shows on the dashboard's system page.
# Needs the Bun version that dashboard/package.json names. See docs/building.md.
set -eu

[ "$#" -le 1 ] || {
	echo "usage: scripts/dashboard.sh [version]" >&2
	exit 2
}
version=${1:-0.0.0}

cd "$(dirname "$0")/.."

bun=$(command -v bun) || {
	echo "scripts/dashboard.sh: bun is not on PATH" >&2
	exit 1
}
bun_cache=${BUN_INSTALL_CACHE_DIR:-$HOME/.bun/install/cache}
run_home=$(mktemp -d)
trap 'rm -rf "$run_home"' EXIT INT TERM

# Only these inputs reach the install, tests, and build: the toolchain, the
# package cache, a run-owned home and temporary directory, and the version.
env -i \
	PATH="$(dirname "$bun"):/usr/bin:/bin" \
	HOME="$run_home" \
	TMPDIR="$run_home" \
	BUN_INSTALL_CACHE_DIR="$bun_cache" \
	VERSION="$version" \
	sh -c 'cd dashboard && bun install --frozen-lockfile && bun run verify' || {
	echo "scripts/dashboard.sh: dashboard verification failed; rerun one test file with: (cd dashboard && bun test tests/<name>.test.ts)" >&2
	exit 1
}

cp dashboard/dist/index.html internal/dashboard/dist/management.html
