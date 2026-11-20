#!/bin/sh
cd "$(dirname -- "$0")/.." || exit 1
version=v1.15.0
tmp=$(mktemp -d "${TMPDIR:-/tmp}/check-arch.XXXXXX") || exit 1
trap 'rm -rf "$tmp"' EXIT
failed=0

go run "github.com/fe3dback/go-arch-lint@$version" check --output-color=false >"$tmp/arch" 2>&1
rc=$?
cat "$tmp/arch"
if [ "$rc" != 0 ]; then
	echo "check-arch: go-arch-lint found imports .go-arch-lint.yml doesn't allow"
	failed=1
fi
grep -Eq '^[[:space:]]*On \| Base: component imports' "$tmp/arch" || {
	echo "check-arch: go-arch-lint's component-imports linter is off, so the check refuses to pass"
	failed=1
}
grep -Eq '^[[:space:]]*On \| Advanced: vendor imports' "$tmp/arch" || {
	echo "check-arch: go-arch-lint's vendor-imports linter is off (allow.depOnAnyVendor), so the check refuses to pass"
	failed=1
}

go run "github.com/fe3dback/go-arch-lint@$version" mapping --output-color=false 2>&1 |
	sed -n 's|^[[:space:]]*[A-Za-z0-9_-][A-Za-z0-9_-]*[[:space:]][[:space:]]*/\([^[:space:]]*\)$|proctor/\1|p' | LC_ALL=C sort -u >"$tmp/mapped"
GOWORK=off go list ./... | LC_ALL=C sort -u >"$tmp/packages" || failed=1
LC_ALL=C comm -23 "$tmp/packages" "$tmp/mapped" >"$tmp/unmapped"
if [ -s "$tmp/unmapped" ]; then
	sed 's|$|: a package of the proctor module that no component in .go-arch-lint.yml holds|' "$tmp/unmapped"
	failed=1
fi

for dir in protocol drivers/*/ simulators/*/; do
	dir=${dir%/}
	[ -d "$dir" ] || continue
	if [ ! -f "$dir/go.mod" ]; then
		echo "$dir: has no go.mod, so its packages belong to the proctor module, outside every rule"
		failed=1
		continue
	fi
	mod=$(cd "$dir" && GOWORK=off go list -m) || { failed=1; continue; }
	for target in linux/amd64 darwin/arm64 windows/amd64; do
		(cd "$dir" && GOOS=${target%/*} GOARCH=${target#*/} go list -deps -f '{{.ImportPath}}' ./...) || echo "check-arch: go list failed in $dir for $target" >&2
	done >"$tmp/deps" 2>"$tmp/err"
	if [ -s "$tmp/err" ]; then
		cat "$tmp/err"
		failed=1
	fi
	LC_ALL=C sort -u "$tmp/deps" | grep -E '^proctor(/|$)' | grep -vE "^$mod(/|$)" >"$tmp/bad"
	if [ -s "$tmp/bad" ]; then
		sed "s|^|$dir ($mod) depends on |; s|$|, a package of another proctor module; only github.com/bezmoradi/proctor/protocol crosses modules|" "$tmp/bad"
		failed=1
	fi
done

if [ "$failed" = 1 ]; then
	exit 1
fi
echo "Architecture: the proctor module's packages import only what .go-arch-lint.yml allows, and protocol, each driver and each simulator import no package of another proctor module."
