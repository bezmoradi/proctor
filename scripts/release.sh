#!/usr/bin/env bash
set -euo pipefail

usage() {
	echo "usage: scripts/release.sh VERSION [OUTDIR]" >&2
	echo "  VERSION like 0.1.0 or 0.1.0-draft.1; OUTDIR defaults to dist" >&2
	exit 2
}

[ $# -ge 1 ] && [ $# -le 2 ] || usage
version=$1
out=${2:-dist}
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo "release: invalid version $version" >&2; usage; }

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

if [ -n "$(git status --porcelain)" ]; then
	echo "release: the working tree has uncommitted changes; commit or stash them first" >&2
	exit 1
fi
commit=$(git rev-parse HEAD)

if [ -e "$out" ]; then
	echo "release: $out already exists; remove it or choose another directory" >&2
	exit 1
fi
mkdir -p "$out"

targets="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"
for t in $targets; do
	os=${t%/*}
	arch=${t#*/}
	bin="$out/proctor-$version-$os-$arch"
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -buildvcs=false \
		-ldflags "-s -w -buildid= -X proctor/internal/cli.Version=$version" \
		-o "$bin" ./cmd/proctor
	echo "built $bin"
done

notices="$out/proctor-$version-NOTICES.txt"
{
	echo "Proctor $version, built from commit $commit with $(go version | cut -d' ' -f3)"
	echo
	echo "==== Proctor ===="
	cat LICENSE
	echo
	echo "==== Go standard library ($(go version | cut -d' ' -f3)) ===="
	cat "$(go env GOROOT)/LICENSE"
	go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' ./cmd/proctor | sort -u |
		while read -r path ver dir; do
			found=0
			for f in "$dir"/LICENSE* "$dir"/NOTICE* "$dir"/COPYING*; do
				[ -f "$f" ] || continue
				echo
				echo "==== $path $ver: $(basename "$f") ===="
				cat "$f"
				found=1
			done
			if [ $found -eq 0 ]; then
				echo "release: no license file found for $path $ver" >&2
				exit 1
			fi
		done
} > "$notices"
echo "wrote $notices"

if command -v sha256sum >/dev/null 2>&1; then
	sum() { sha256sum "$@"; }
else
	sum() { shasum -a 256 "$@"; }
fi
(cd "$out" && sum proctor-"$version"-* > SHA256SUMS)
echo "wrote $out/SHA256SUMS"
cat "$out/SHA256SUMS"
