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
for m in protocol drivers/* simulators/ses; do (cd "$m" && GOWORK=off go vet ./... && GOWORK=off go build -o /dev/null ./...) || echo "FAILED $m"; done
grep -rnE '^\s*//' --include='*.go' . | grep -vE '//(go:|nolint)'
find . \( -name '*_test.go' -o -name testdata \) -not -path './.git/*'
scripts/build.sh <out-dir> && <out-dir>/proctor version
```

Build into a folder under `/tmp`, not `bin/` or `~/go/bin`. `scripts/build.sh` builds the drivers' checksums into `proctor`, which refuses drivers that don't match (INT-17), so every program it builds is copied or installed together.

## Safety

- Commit only when the owner asks. Never push, add a remote or publish a release.
- Never print tokens, passwords or connection strings.
- Never touch real AWS. The dynamodb, sqs and ses drivers run against local emulators.
- Leave these Docker containers alone: `sf-dind`, `sf-p1-*`, `sf-p9-dind`, `newsletter-agent-local-db-1`, `users-api-postgres-psql-1`.
- Never edit the arcauthic README files.
- Put throwaway repositories under `/tmp/proctor-<task>-<random>/` and delete them afterwards.
