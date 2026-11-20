#!/bin/sh
cd "$(dirname -- "$0")/.." || exit 1
limit=524288
tmp=$(mktemp -d "${TMPDIR:-/tmp}/check-files.XXXXXX") || exit 1
trap 'rm -rf "$tmp"' EXIT

git -c core.quotePath=false ls-files -z --cached --others --exclude-standard >"$tmp/z" || exit 1
tr '\0' '\n' <"$tmp/z" >"$tmp/list"
[ -s "$tmp/list" ] || {
	echo "scripts/check-files.sh: git lists no files"
	exit 1
}

while IFS= read -r f; do
	[ -f "$f" ] || continue
	size=$(wc -c <"$f" | tr -d ' ')
	[ "$size" -gt "$limit" ] && printf '%s: %s bytes, over 512 KiB\n' "$f" "$size"
	grep -Iq . "$f" 2>/dev/null || continue
	grep -Il "$(printf '\r')" "$f" >/dev/null 2>&1 && printf '%s: CRLF line endings\n' "$f"
	grep -InE '^(<<<<<<< |>>>>>>> |=======$)' "$f" 2>/dev/null | sed "s|^|$f:|; s|$|: a merge conflict marker|"
done <"$tmp/list" >"$tmp/problems"

tr '[:upper:]' '[:lower:]' <"$tmp/list" | sort | uniq -d | sed 's|$|: two paths differ only by case|' >>"$tmp/problems"

if [ -s "$tmp/problems" ]; then
	cat "$tmp/problems"
	exit 1
fi
echo "Files: none over 512 KiB, no CRLF, no conflict markers and no paths differing only by case."
