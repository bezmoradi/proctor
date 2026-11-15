#!/usr/bin/env bash
set -euo pipefail

usage() {
	echo "usage: scripts/build.sh [--install] [OUTDIR]" >&2
	echo "  OUTDIR defaults to bin in the repository; --install also copies the programs into \$(go env GOPATH)/bin" >&2
	exit 2
}

install=0
out=""
for arg in "$@"; do
	case $arg in
	--install) install=1 ;;
	-*) usage ;;
	*)
		[ -z "$out" ] || usage
		out=$arg
		;;
	esac
done
root=$(cd "$(dirname "$0")/.." && pwd)
out=${out:-$root/bin}
mkdir -p "$out"
out=$(cd "$out" && pwd)
cd "$root"

if command -v sha256sum >/dev/null 2>&1; then
	sum() { sha256sum "$@"; }
else
	sum() { shasum -a 256 "$@"; }
fi

drivers="http postgres"
sumfile="$root/internal/runner/official.sum"

for d in $drivers; do
	(cd "drivers/$d" && GOWORK=off go build -trimpath -buildvcs=false -o "$out/proctor-driver-$d" "./cmd/proctor-driver-$d")
	echo "built $out/proctor-driver-$d"
done

trap ': > "$sumfile"' EXIT
(
	cd "$out"
	for d in $drivers; do
		sum "proctor-driver-$d"
	done
) > "$sumfile"
GOWORK=off go build -trimpath -buildvcs=false -o "$out/proctor" ./cmd/proctor
: > "$sumfile"
trap - EXIT
echo "built $out/proctor with these pinned drivers:"
(cd "$out" && for d in $drivers; do sum "proctor-driver-$d"; done)

if [ "$install" -eq 1 ]; then
	gopath=$(go env GOPATH)
	dest="${gopath%%:*}/bin"
	mkdir -p "$dest"
	for f in proctor $(for d in $drivers; do echo "proctor-driver-$d"; done); do
		cp "$out/$f" "$dest/$f"
		echo "installed $dest/$f"
	done
fi
