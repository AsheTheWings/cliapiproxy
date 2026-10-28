#!/bin/sh
# Runs one test class, or the fast gate, with an explicit input allowlist.
#
# usage: scripts/test.sh fast|unit|component|conformance|integration|qualification [go test flags...] [packages...]
#
# Packages default to ./... from the repository root. Qualification also
# passes CLIPROXY_QUALIFICATION_LAYOUT and CLIPROXY_QUALIFICATION_REVISION
# through and needs Podman. See docs/testing.md.
set -eu

usage() {
	echo "usage: scripts/test.sh fast|unit|component|conformance|integration|qualification [go test flags...] [packages...]" >&2
	exit 2
}

[ "$#" -ge 1 ] || usage
class=$1
shift
case "$class" in
fast) tags=unit,component,conformance ;;
unit | component | conformance | integration | qualification) tags=$class ;;
*) usage ;;
esac

# The image under qualification is that class's only further input.
qualification_layout= qualification_revision=
if [ "$class" = qualification ]; then
	qualification_layout=$(cd "${CLIPROXY_QUALIFICATION_LAYOUT:?the OCI layout of the image to qualify}" && pwd)
	qualification_revision=${CLIPROXY_QUALIFICATION_REVISION:?the revision the image must carry}
fi

packages=./...
for arg in "$@"; do
	case "$arg" in
	./* | github.com/*) packages= ;;
	esac
done

cd "$(dirname "$0")/.."

goroot=$(go env GOROOT)
gocache=$(go env GOCACHE)
gomodcache=$(go env GOMODCACHE)
goproxy=$(go env GOPROXY)
run_home=$(mktemp -d)
trap 'rm -rf "$run_home"' EXIT INT TERM

# Only these inputs reach the tests: toolchain location and caches, and a
# run-owned home and temporary directory.
status=0
env -i \
	PATH="$goroot/bin:/usr/bin:/bin" \
	HOME="$run_home" \
	TMPDIR="$run_home" \
	GOCACHE="$gocache" \
	GOMODCACHE="$gomodcache" \
	GOPROXY="$goproxy" \
	GOFLAGS=-mod=readonly \
	GOTOOLCHAIN=local \
	CGO_ENABLED=1 \
	CLIPROXY_QUALIFICATION_LAYOUT="$qualification_layout" \
	CLIPROXY_QUALIFICATION_REVISION="$qualification_revision" \
	go test -count=1 -tags "$tags" "$@" $packages || status=$?

if [ "$status" -ne 0 ]; then
	echo "scripts/test.sh: $class tests failed; rerun one with: scripts/test.sh $class -run '^TestName\$' ./path/to/package" >&2
fi
exit "$status"
