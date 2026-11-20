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
out=$(cd "$out" && pwd)

if command -v sha256sum >/dev/null 2>&1; then
	sum() { sha256sum "$@"; }
else
	sum() { shasum -a 256 "$@"; }
fi

discover() {
	local kind=$1 prefix=$2 found="" dir name
	for dir in "$root/$kind"/*/; do
		[ -d "$dir" ] || continue
		name=$(basename "$dir")
		[ -d "$dir/cmd/$prefix-$name" ] || continue
		if ! [[ $name =~ ^[a-z][a-z0-9_-]*$ ]]; then
			echo "release: $kind/$name: the name must match ^[a-z][a-z0-9_-]*\$" >&2
			exit 1
		fi
		if [ ! -f "$dir/go.mod" ]; then
			echo "release: $kind/$name has no go.mod; each one is its own module" >&2
			exit 1
		fi
		found="$found $name"
	done
	echo "$found"
}

drivers=$(discover drivers proctor-driver)
sims=$(discover simulators proctor-sim)
if [ -z "$drivers" ]; then
	echo "release: no drivers found under drivers/<name>/cmd/proctor-driver-<name>" >&2
	exit 1
fi
programs=".:./cmd/proctor"
for d in $drivers; do programs="$programs drivers/$d:./cmd/proctor-driver-$d"; done
for s in $sims; do programs="$programs simulators/$s:./cmd/proctor-sim-$s"; done
targets="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"

goroot=$(go env GOROOT)
golicense=""
for f in "$goroot/LICENSE" "$goroot/../LICENSE"; do
	if [ -f "$f" ]; then
		golicense=$f
		break
	fi
done
if [ -z "$golicense" ]; then
	echo "release: the Go toolchain's LICENSE wasn't found near $goroot" >&2
	exit 1
fi

notices="$out/proctor-$version-NOTICES.txt"
{
	echo "Proctor $version, built from commit $commit with $(go version | cut -d' ' -f3)"
	echo "Programs: proctor$(for d in $drivers; do printf ' proctor-driver-%s' "$d"; done)$(for s in $sims; do printf ' proctor-sim-%s' "$s"; done)"
	echo
	echo "==== Proctor ===="
	cat LICENSE
	echo
	echo "==== Go standard library ($(go version | cut -d' ' -f3)) ===="
	cat "$golicense"
	for p in $programs; do
		for t in $targets; do
			(cd "$root/${p%%:*}" && GOOS=${t%/*} GOARCH=${t#*/} GOWORK=off go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' "${p#*:}")
		done
	done | sort -u |
		while read -r path ver dir; do
			case $path in
			proctor | proctor/* | github.com/bezmoradi/proctor/protocol) continue ;;
			esac
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

stage="$out/.stage"
sumfile="$root/internal/runner/official.sum"
trap ': > "$sumfile"; rm -rf "$stage"' EXIT
ldflags="-s -w -buildid= -X main.Version=$version"

for t in $targets; do
	os=${t%/*}
	arch=${t#*/}
	name="proctor-$version-$os-$arch"
	pkg="$stage/$name"
	mkdir -p "$pkg"
	for d in $drivers; do
		(
			cd "$root/drivers/$d"
			mod=$(GOWORK=off go list -m)
			CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOWORK=off go build -trimpath -buildvcs=false \
				-ldflags "$ldflags -X $mod.Version=$version" -o "$pkg/proctor-driver-$d" "./cmd/proctor-driver-$d"
		)
	done
	for s in $sims; do
		(
			cd "$root/simulators/$s"
			mod=$(GOWORK=off go list -m)
			CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOWORK=off go build -trimpath -buildvcs=false \
				-ldflags "$ldflags -X $mod.Version=$version" -o "$pkg/proctor-sim-$s" "./cmd/proctor-sim-$s"
		)
	done
	(cd "$pkg" && for d in $drivers; do sum "proctor-driver-$d"; done) > "$sumfile"
	echo "pinned for $os/$arch:"
	cat "$sumfile"
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOWORK=off go build -trimpath -buildvcs=false \
		-ldflags "-s -w -buildid= -X proctor/internal/commands.Version=$version" \
		-o "$pkg/proctor" ./cmd/proctor
	: > "$sumfile"
	cp LICENSE "$pkg/LICENSE"
	cp "$notices" "$pkg/NOTICES.txt"
	(cd "$pkg" && sum * > "$stage/SHA256SUMS" && mv "$stage/SHA256SUMS" SHA256SUMS)
	COPYFILE_DISABLE=1 tar -C "$stage" -czf "$out/$name.tar.gz" "$name"
	echo "built $out/$name.tar.gz"
done
rm -rf "$stage"
: > "$sumfile"
trap - EXIT

(cd "$out" && sum proctor-"$version"-*.tar.gz > SHA256SUMS)
sh "$root/scripts/release-install.sh" "$version" "$out"
(cd "$out" && sum proctor-"$version"-* install.sh > SHA256SUMS.part && mv SHA256SUMS.part SHA256SUMS)
echo "wrote $out/SHA256SUMS"
cat "$out/SHA256SUMS"
