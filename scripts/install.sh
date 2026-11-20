#!/bin/sh
set -eu

version='@VERSION@'
sha256_linux_amd64='@SHA256_LINUX_AMD64@'
sha256_linux_arm64='@SHA256_LINUX_ARM64@'
sha256_darwin_amd64='@SHA256_DARWIN_AMD64@'
sha256_darwin_arm64='@SHA256_DARWIN_ARM64@'

repo=bezmoradi/proctor
latest="https://github.com/$repo/releases/latest/download/install.sh"
base="https://github.com/$repo/releases/download/v$version"
LC_ALL=C
export LC_ALL

tmp=
stage=

stop() {
	printf 'Install stopped: %s\n' "$1" >&2
	exit 1
}

cleanup() {
	[ -z "$stage" ] || rm -rf "$stage"
	[ -z "$tmp" ] || rm -rf "$tmp"
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sum=$(sha256sum "$1") || return 1
	else
		sum=$(shasum -a 256 "$1") || return 1
	fi
	printf '%s\n' "${sum%% *}"
}

check_sums() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum -c SHA256SUMS >/dev/null 2>&1
	else
		shasum -a 256 -c SHA256SUMS >/dev/null 2>&1
	fi
}

main() {
	exec </dev/null
	case $version$sha256_linux_amd64$sha256_linux_arm64$sha256_darwin_amd64$sha256_darwin_arm64 in
	*@*) stop "this is the template in the repository, not a release's script. Install with: curl -fsSL $latest | sh" ;;
	esac

	system=$(uname -s)
	machine=$(uname -m)
	case $system in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) os= ;;
	esac
	case $machine in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) arch= ;;
	esac
	if [ -z "$os" ] || [ -z "$arch" ]; then
		stop "this is $system on $machine, and Proctor's releases are built only for linux/amd64, linux/arm64, darwin/amd64 and darwin/arm64."
	fi
	case $os-$arch in
	linux-amd64) want=$sha256_linux_amd64 ;;
	linux-arm64) want=$sha256_linux_arm64 ;;
	darwin-amd64) want=$sha256_darwin_amd64 ;;
	darwin-arm64) want=$sha256_darwin_arm64 ;;
	esac

	command -v curl >/dev/null 2>&1 || stop "curl isn't installed. Install curl and run this again."
	command -v tar >/dev/null 2>&1 || stop "tar isn't installed. Install tar and run this again."
	command -v git >/dev/null 2>&1 || stop "git isn't installed, and Proctor reads your repository with it. Install git and run this again."
	if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
		stop "neither sha256sum nor shasum is installed, so the download can't be checked. Install coreutils (or perl's shasum) and run this again."
	fi

	if [ -n "${PROCTOR_INSTALL_DIR:-}" ]; then
		dir=$PROCTOR_INSTALL_DIR
	elif [ -n "${HOME:-}" ]; then
		dir=$HOME/.local/bin
	else
		stop "HOME isn't set. Set PROCTOR_INSTALL_DIR to the folder to install into."
	fi
	case $dir in
	/*) ;;
	*) dir="$(pwd)/$dir" ;;
	esac

	trap cleanup EXIT
	trap 'exit 1' HUP INT TERM
	tmp=$(mktemp -d "${TMPDIR:-/tmp}/proctor-install.XXXXXX" 2>/dev/null) || stop "couldn't make a temporary folder."

	name="proctor-$version-$os-$arch"
	archive="$tmp/$name.tar.gz"
	echo "Downloading $base/$name.tar.gz"
	curl -fsSL --proto '=https' --tlsv1.2 --retry 3 -o "$archive" "$base/$name.tar.gz" || stop "couldn't download $base/$name.tar.gz; nothing in $dir was changed."
	if ! got=$(sha256_of "$archive") || [ "$got" != "$want" ]; then
		stop "$name.tar.gz doesn't match the SHA-256 this script carries, so nothing in $dir was changed."
	fi
	mkdir "$tmp/x" || stop "couldn't make a folder in $tmp."
	tar -xzf "$archive" -C "$tmp/x" --no-same-owner || stop "couldn't unpack $name.tar.gz; nothing in $dir was changed."
	pkg="$tmp/x/$name"
	[ -f "$pkg/SHA256SUMS" ] || stop "$name.tar.gz holds no $name/SHA256SUMS; nothing in $dir was changed."
	if grep -Evx '[0-9a-f]{64}  [A-Za-z0-9._-]+' "$pkg/SHA256SUMS" >/dev/null; then
		stop "$name/SHA256SUMS has a line that isn't a SHA-256 and a file name; nothing in $dir was changed."
	fi
	(cd "$pkg" && check_sums) || stop "the files in $name.tar.gz don't match its SHA256SUMS; nothing in $dir was changed."

	programs=
	while read -r _ file; do
		case $file in
		proctor | proctor-driver-* | proctor-sim-*) ;;
		*) continue ;;
		esac
		if [ -L "$pkg/$file" ] || [ ! -f "$pkg/$file" ] || [ ! -x "$pkg/$file" ]; then
			stop "$file in $name.tar.gz isn't a program; nothing in $dir was changed."
		fi
		programs="$programs $file"
	done <"$pkg/SHA256SUMS"
	case " $programs " in
	*" proctor "*) ;;
	*) stop "$name.tar.gz holds no proctor; nothing in $dir was changed." ;;
	esac
	case $programs in
	*proctor-driver-*) ;;
	*) stop "$name.tar.gz holds no drivers; nothing in $dir was changed." ;;
	esac
	got=$("$pkg/proctor" version 2>/dev/null) || stop "the downloaded proctor doesn't run from $tmp; if TMPDIR doesn't allow running programs, set it to a folder that does. Nothing in $dir was changed."
	[ "$got" = "$version" ] || stop "the downloaded proctor reports version $got, not $version; nothing in $dir was changed."

	mkdir -p "$dir" 2>/dev/null || stop "couldn't make the folder $dir."
	for file in $programs; do
		if [ -d "$dir/$file" ]; then
			stop "$dir/$file is a folder or a link to one; move it away and run this again. Nothing in $dir was changed."
		fi
	done
	stage=$(mktemp -d "$dir/.proctor-install.XXXXXX" 2>/dev/null) || stop "couldn't write in $dir; nothing in it was changed."
	for file in $programs; do
		if ! cp "$pkg/$file" "$stage/$file" || ! chmod 0755 "$stage/$file"; then
			stop "couldn't copy $file into $dir; nothing in it was changed."
		fi
	done
	for file in $programs; do
		mv -f "$stage/$file" "$dir/$file" || stop "couldn't move $file into place in $dir, so some programs there may be $version and others older; run this again."
	done
	rm -rf "$stage"
	stage=

	echo "Installed Proctor $version for $os/$arch in $dir:"
	for file in $programs; do
		echo "  $file"
	done
	installed=$("$dir/proctor" version) || stop "$dir/proctor doesn't run."
	echo "$dir/proctor version: $installed"
	case ":${PATH:-}:" in
	*":$dir:"*) ;;
	*) echo "$dir isn't on PATH; add it in your shell's profile: export PATH=\"$dir:\$PATH\"" ;;
	esac
	echo "To check where this release came from, with a signed-in gh: curl -fsSLO $base/$name.tar.gz && gh attestation verify $name.tar.gz -R $repo --signer-workflow $repo/.github/workflows/release.yml --source-ref refs/tags/v$version"
}

main "$@"
