# Working on Proctor

## Requirements

`docs/` is the documentation for users and the source of truth for how Proctor behaves. Each topic has one page, listed in `docs/README.md`: how-to pages under `docs/guides/`, lookup pages under `docs/reference/`. Say each thing once, on the page that owns it, and link to it from elsewhere. `README.md` is the landing page: what Proctor is, how it works, installing and the quick start.

Every change updates, in the same piece of work:

- the `docs/` page that owns the topic, with what users need, plus `docs/reference/rules.md` for a new or changed rule ID, `docs/reference/reports.md` for a new result reason or report field, and `docs/reference/diff.md` for a new `proctor diff` kind;
- `README.md` only when what it covers changes;
- `internal/commands/features.go`, for a new feature, so `proctor version --features` lists it.

CI's Markdown and link checks cover every tracked `.md` file, so a moved heading must take its links with it.

## Code rules

- No code comments, except tool directives (`//go:build`, `//nolint`).
- No `*_test.go` files and no `testdata`. There is no test code: each change is checked by hand runs of the built binary, reported with the change.

## Verify

Run from the repository root; the two searches must print nothing:

```sh
go build ./... && go vet ./... && test -z "$(gofmt -l .)"
for m in protocol drivers/* simulators/* examples/sample examples/sample/proctor/drivers/kafka; do (cd "$m" && GOWORK=off go vet ./... && GOWORK=off go build -o /dev/null ./...) || echo "FAILED $m"; done
sh scripts/check-arch.sh
for m in . protocol drivers/* simulators/* examples/sample examples/sample/proctor/drivers/kafka; do (cd "$m" && GOWORK=off go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run --config "$OLDPWD/.golangci.yml" ./...) || echo "FAILED $m"; done
grep -rnE '^\s*//' --include='*.go' . | grep -vE '//(go:|nolint)'
find . \( -name '*_test.go' -o -name testdata \) -not -path './.git/*'
scripts/build.sh <out-dir> && <out-dir>/proctor version && <out-dir>/proctor version --features
(cd examples/sample/proctor/drivers/kafka && GOWORK=off GOTOOLCHAIN=go1.27.4 CGO_ENABLED=0 GOFLAGS=-mod=readonly go build -trimpath -buildvcs=false -o bin/proctor-driver-kafka ./cmd/proctor-driver-kafka) && PATH="<out-dir>:$PATH" PROCTOR_NO_UPDATE_CHECK=1 sh -c 'proctor validate && proctor run --binding proctor/environments/local.yaml'
```

`sh scripts/check-arch.sh` and the golangci-lint line fetch pinned tools with `go run`, so they need the Go module proxy and fail under `GOPROXY=off`. `.go-arch-lint.yml` lists which internal packages and third-party modules each package of the `proctor` module may import, and `scripts/check-arch.sh` also refuses any import from `protocol`, a driver or a simulator into another `proctor/...` module, and any `os`, `os/...`, `syscall`, `net` or `net/...` import, or any other Proctor package, in `internal/model`, `internal/expr`, `internal/transcript` and `internal/strictyaml`. A new package needs a component in `.go-arch-lint.yml`, and a new import an allowed edge. When the check fails, change the code; don't widen `mayDependOn` or `canUse` in `.go-arch-lint.yml` unless the new edge is the point of the change.

`.github/workflows/ci.yml` runs these checks on every pull request, every push to main and weekly, plus cross-builds for the four release platforms, govulncheck, file, spelling, shell, Markdown, link and secret checks, actionlint and zizmor on the workflows, and the sample job: it builds Proctor and the sample's `kafka` repository driver, starts `examples/sample/compose.yaml`, and runs `proctor validate` and `proctor run` against it in local mode; its step "Proctor checks itself" then runs `proctor validate` and `proctor run --binding proctor/environments/local.yaml` on the root `proctor/` folder, whose cli driver specifications run the built `proctor` first on `PATH` ([Self-check](docs/development.md#self-check)). `.github/workflows/proctor.yml` runs the same self-check in authoritative mode on pull requests and merge groups: a verifier built from the base commit runs the pull request's `proctor` only inside a container with no network and the checkout read-only, and its `verdict` job is the check to require; it also fails on a change to the protected files until the owner adds `review-override` after the last push. To merge, a pull request needs a linked issue (`pull-requests.yml`). One that changes `.github/`, `scripts/` or a lint or scanner config at the root also needs the owner's `review-override` label, added after its last push (`workflow-guard.yml`).

`examples/sample` and its driver, `examples/sample/proctor/drivers/kafka`, are Go modules of their own outside `go.work`, so the loops above and CI name them. A change to the driver's source or `go.sum` changes its program: rebuild it with the fixed command in [Repository drivers](docs/guides/repository-drivers.md) for `linux/amd64` and `darwin/arm64` and update both `sha256` pins in `examples/sample/proctor/proctor.yaml`, or the sample job fails with INT-17.

Build into a folder under `/tmp`, not `bin/` or `~/go/bin`. `scripts/build.sh` builds the drivers' checksums into `proctor`, which refuses drivers that don't match (INT-17), so every program it builds is copied or installed together.

A built `proctor` asks `api.github.com` for the latest published release on `validate`, `integrity`, `diff` and a local `run` (`internal/commands/update.go`); `PROCTOR_NO_UPDATE_CHECK=1`, `CI` or `GITHUB_ACTIONS` turns that off. Only the owner releases, as [Releasing](docs/development.md#releasing) describes.

## Safety

- Commit only when the owner asks. Never push, add a remote or publish a release.
- Never print tokens, passwords or connection strings.
- Never touch real AWS. The dynamodb, sqs and ses drivers run against local emulators.
- The redis driver runs only against a local `redis-server` or Sentinel you started, never a shared Redis.
- The browser driver needs an installed Chrome or Chromium, and no CI job runs it. It starts headless Chrome with a throwaway profile in `TMPDIR`; never give `chrome_path` or `remote_url` a browser that uses your own profile.
- Check the cli driver only with throwaway repositories under `/tmp`: its commands run as you, with your files and network.
- Leave Docker containers and other processes this work didn't start alone.
- Never change another repository's files unless the owner asks for that specific change.
- Proctor's files, issues, pull requests and commit messages never name or describe the owner's other projects.
- Put throwaway repositories under `/tmp/proctor-<task>-<random>/` and delete them afterwards.
