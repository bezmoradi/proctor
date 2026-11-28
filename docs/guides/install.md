# Installation

```sh
curl -fsSL https://github.com/bezmoradi/proctor/releases/latest/download/install.sh | sh
```

The script:

- runs on linux and darwin, amd64 and arm64, and stops with one line on anything else, or when `curl`, `tar`, `git`, or `sha256sum` or `shasum` is missing; it needs no Go;
- downloads the release's archive for your platform, checks it against the SHA-256 written into the script and against the archive's own `SHA256SUMS`, and checks that its `proctor version` prints the release's version;
- installs `proctor`, its drivers and the simulators together into one folder, `~/.local/bin` by default, without sudo, since `proctor` refuses a driver whose checksum it doesn't hold (INT-17);
- changes nothing in the folder when a download or check fails.

`latest` is the newest published release that isn't a prerelease; `releases/download/v<version>/install.sh` installs that version.

## Install somewhere else

Set `PROCTOR_INSTALL_DIR` to the folder:

```sh
curl -fsSL https://github.com/bezmoradi/proctor/releases/latest/download/install.sh | PROCTOR_INSTALL_DIR="$HOME/tools/proctor" sh
```

## Update

Run the same command again: it replaces `proctor`, its drivers and the simulators, and leaves other files in the folder alone. `validate`, `integrity`, `diff` and a local `run` say when a newer release exists, and print the command that updates the programs in the folder `proctor` runs from.

Set `PROCTOR_NO_UPDATE_CHECK=1` to turn the check off; it is also off when `CI` or `GITHUB_ACTIONS` is set, and in authoritative mode. It asks `api.github.com` for the latest release and sends only `proctor/<version>` as the User-Agent (see [Privacy](../security-model.md#privacy)).

## Check the script first

With a signed-in `gh`:

```sh
curl -fsSLO https://github.com/bezmoradi/proctor/releases/latest/download/install.sh
gh attestation verify install.sh -R bezmoradi/proctor --signer-workflow bezmoradi/proctor/.github/workflows/release.yml
sh install.sh
```

## Other ways to install

Download an archive yourself: each release has one per platform and a `SHA256SUMS`. Check its checksum and its signed provenance, then the files inside:

```sh
VERSION=<version> OS=linux ARCH=amd64; name="proctor-$VERSION-$OS-$ARCH"
curl -fsSLO "https://github.com/bezmoradi/proctor/releases/download/v$VERSION/$name.tar.gz"
curl -fsSLO "https://github.com/bezmoradi/proctor/releases/download/v$VERSION/SHA256SUMS"
grep " $name.tar.gz\$" SHA256SUMS | shasum -a 256 -c -
gh attestation verify "$name.tar.gz" -R bezmoradi/proctor --deny-self-hosted-runners \
  --signer-workflow bezmoradi/proctor/.github/workflows/release.yml --source-ref "refs/tags/v$VERSION"
tar -xzf "$name.tar.gz" && (cd "$name" && shasum -a 256 -c SHA256SUMS)
```

The release workflow attests every file it publishes, `SHA256SUMS`, `install.sh` and the NOTICES file included, and runs only on GitHub-hosted runners, so `--deny-self-hosted-runners` holds for each. Archive names carry the version, so a script that wants the newest release asks for its tag first, or uses `releases/latest/download/install.sh`. In CI, pin a version and its archive's SHA-256 instead, as the [reference workflow](../../examples/github/proctor.yml) does: a new release then can't change a required check's verdict on an unchanged commit, and the upgrade is a reviewed change. Check the features your specifications need with `proctor version --features`, or declare them in `proctor.yaml` (see [Requirements](#requirements)).

Keep `proctor` and its `proctor-driver-*` programs in one folder: `proctor` refuses a driver whose checksum it doesn't hold (exit 4, INT-17); a driver kept in your repository is pinned in `proctor.yaml` instead (see [Repository drivers](repository-drivers.md)).

To build from source, see [Development](../development.md).

## Requirements

`proctor.yaml` can name the Proctor its specifications need, so an older one is refused before it reads them:

```yaml
requires:
  proctor: ">=0.5.0"
  features: [cleanup, cleanup.unless_passed, matchers.matches]
```

Every command that reads the configuration checks it first and exits 3 (INT-22) naming what's missing: a version below `proctor` (`>=` and a version; a development build counts as the version it was built from), or a feature this build's `proctor version --features` doesn't list. With `features`, validation also refuses a specification set that uses a feature the list leaves out, naming where it's used, so the list can't fall behind. It knows the features that have a key of their own: `cleanup`, `cleanup.unless_passed`, `cleanup.eventually`, `save.capture`, `save.secret`, `http.virtual_hosts` (an http step's `host`), `http.cookies` (`as` or `on` on `http.request`), `transcripts`, `approved_logs`, `app_logs`, `mutate`, `relations`, `invariants`, `policy_from`, `auth04.identity_check`, `requires.disposable_environment`, `step.timeout`, `waive.rows`, `tables.covers`, `table.time`, `matchers.matches`, `matchers.excludes`, `matchers.differs`, `drivers.repository`, and the `browser`, `cli`, `docker`, `otel`, `redis` and `stub` drivers. A Proctor older than the `requires` feature refuses the key as unknown (exit 3).
