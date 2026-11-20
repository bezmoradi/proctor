# Working on Proctor

## Requirements

`README.md` is the documentation for users and the source of truth for how Proctor behaves. Keep it short: a quick reference, not a spec.

Every change updates, in the same piece of work:

- `README.md`, with what users need;
- `internal/commands/features.go`, for a new feature, so `proctor version --features` lists it.

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
```

`sh scripts/check-arch.sh` and the golangci-lint line fetch pinned tools with `go run`, so they need the Go module proxy and fail under `GOPROXY=off`. `.go-arch-lint.yml` lists which internal packages and third-party modules each package of the `proctor` module may import, and `scripts/check-arch.sh` also refuses any import from `protocol`, a driver or a simulator into another `proctor/...` module, and any `os`, `os/...`, `syscall`, `net` or `net/...` import, or any other Proctor package, in `internal/model`, `internal/expr`, `internal/transcript` and `internal/strictyaml`. A new package needs a component in `.go-arch-lint.yml`, and a new import an allowed edge. When the check fails, change the code; don't widen `mayDependOn` or `canUse` in `.go-arch-lint.yml` unless the new edge is the point of the change.

`.github/workflows/ci.yml` runs these checks on every pull request, every push to main and weekly, plus cross-builds for the four release platforms, govulncheck, file, spelling, shell, Markdown, link and secret checks, actionlint and zizmor on the workflows, and the sample job: it builds Proctor and the sample's `kafka` repository driver, starts `examples/sample/compose.yaml`, and runs `proctor validate` and `proctor run` against it in local mode. To merge, a pull request needs a linked issue (`pull-requests.yml`). One that changes `.github/`, `scripts/` or a lint or scanner config at the root also needs the owner's `review-override` label, added after its last push (`workflow-guard.yml`).

`examples/sample` and its driver, `examples/sample/proctor/drivers/kafka`, are Go modules of their own outside `go.work`, so the loops above and CI name them. A change to the driver's source or `go.sum` changes its program: rebuild it with the README's fixed command for `linux/amd64` and `darwin/arm64` and update both `sha256` pins in `examples/sample/proctor/proctor.yaml`, or the sample job fails with INT-17.

Build into a folder under `/tmp`, not `bin/` or `~/go/bin`. `scripts/build.sh` builds the drivers' checksums into `proctor`, which refuses drivers that don't match (INT-17), so every program it builds is copied or installed together.

A built `proctor` asks `api.github.com` for the latest published release on `validate`, `integrity`, `diff` and a local `run` (`internal/commands/update.go`); `PROCTOR_NO_UPDATE_CHECK=1`, `CI` or `GITHUB_ACTIONS` turns that off. Only the owner releases, as README's [Releasing](README.md#releasing) section describes.

## Safety

- Commit only when the owner asks. Never push, add a remote or publish a release.
- Never print tokens, passwords or connection strings.
- Never touch real AWS. The dynamodb, sqs and ses drivers run against local emulators.
- Leave these Docker containers alone: `sf-dind`, `sf-p1-*`, `sf-p9-dind`, `newsletter-agent-local-db-1`, `users-api-postgres-psql-1`.
- Never edit the arcauthic README files.
- Put throwaway repositories under `/tmp/proctor-<task>-<random>/` and delete them afterwards.
