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

discover() {
	local kind=$1 prefix=$2 found="" dir name
	for dir in "$root/$kind"/*/; do
		[ -d "$dir" ] || continue
		name=$(basename "$dir")
		[ -d "$dir/cmd/$prefix-$name" ] || continue
		if ! [[ $name =~ ^[a-z][a-z0-9_-]*$ ]]; then
			echo "build: $kind/$name: the name must match ^[a-z][a-z0-9_-]*\$" >&2
			exit 1
		fi
		if [ ! -f "$dir/go.mod" ]; then
			echo "build: $kind/$name has no go.mod; each one is its own module" >&2
			exit 1
		fi
		found="$found $name"
	done
	echo "$found"
}

drivers=$(discover drivers proctor-driver)
sims=$(discover simulators proctor-sim)
if [ -z "$drivers" ]; then
	echo "build: no drivers found under drivers/<name>/cmd/proctor-driver-<name>" >&2
	exit 1
fi

if git describe --tags >/dev/null 2>&1; then
	version=$(git describe --tags --always --dirty)
	version=${version#v}
elif desc=$(git describe --tags --always --dirty 2>/dev/null); then
	version=0.0.0-$desc
else
	version=""
fi

dirty_digest() {
	{
		git diff HEAD --binary --no-color --no-ext-diff --no-textconv
		git ls-files -z --others --exclude-standard | while IFS= read -r -d '' f; do
			if [ -L "$f" ]; then
				printf 'untracked link %s\0%s\0' "$f" "$(readlink "$f")"
			elif [ -f "$f" ]; then
				printf 'untracked file %s\0%s\0' "$f" "$(wc -c <"$f" | tr -d ' ')"
				cat "$f"
			else
				printf 'untracked other %s\0' "$f"
			fi
		done
	} | sum | cut -c1-12
}

if [ -n "$version" ] && [[ $version != *-dirty ]] && [ -n "$(git ls-files --others --exclude-standard | head -n 1)" ]; then
	version=$version-dirty
fi

case $version in
*-dirty)
	digest=$(dirty_digest)
	if ! [[ $digest =~ ^[0-9a-f]{12}$ ]]; then
		echo "build: couldn't hash the uncommitted changes for the version" >&2
		exit 1
	fi
	version=$version-$digest
	;;
esac

xflags() {
	[ -n "$version" ] || return 0
	local mod
	mod=$(GOWORK=off go list -m)
	echo "-X main.Version=$version -X $mod.Version=$version"
}

build_module() {
	local dir=$1 prog=$2
	(
		cd "$root/$dir"
		GOWORK=off go build -trimpath -buildvcs=false -ldflags "$(xflags)" -o "$out/$prog" "./cmd/$prog"
	)
	echo "built $out/$prog"
}

for d in $drivers; do
	build_module "drivers/$d" "proctor-driver-$d"
done
for s in $sims; do
	build_module "simulators/$s" "proctor-sim-$s"
done

sumfile="$root/internal/runner/official.sum"
coreflags=""
[ -z "$version" ] || coreflags="-X proctor/internal/commands.Version=$version"
trap ': > "$sumfile"' EXIT
(
	cd "$out"
	for d in $drivers; do
		sum "proctor-driver-$d"
	done
) > "$sumfile"
GOWORK=off go build -trimpath -buildvcs=false -ldflags "$coreflags" -o "$out/proctor" ./cmd/proctor
: > "$sumfile"
trap - EXIT
echo "built $out/proctor ${version:-(no version from git)} with these pinned drivers:"
(cd "$out" && for d in $drivers; do sum "proctor-driver-$d"; done)

if [ "$install" -eq 1 ]; then
	gopath=$(go env GOPATH)
	dest="${gopath%%:*}/bin"
	mkdir -p "$dest"
	for f in proctor $(for d in $drivers; do echo "proctor-driver-$d"; done) $(for s in $sims; do echo "proctor-sim-$s"; done); do
		cp "$out/$f" "$dest/$f"
		echo "installed $dest/$f"
	done
fi
