# Architecture

## Repository layout

| Path | Contents |
|---|---|
| `cmd/proctor`, `internal/` | The CLI |
| `drivers/<name>/` | The official drivers, one Go module each (`browser`, `cli`, `docker`, `dynamodb`, `http`, `otel`, `postgres`, `redis`, `ses`, `sqs`, `stub`, `ws`) |
| `protocol/` | The protocol between `proctor` and its drivers, Go module `github.com/bezmoradi/proctor/protocol`; `protocol/cmd/proctor-driver-example/` is a driver to copy for a [repository driver](guides/repository-drivers.md) |
| `simulators/ses/` | `proctor-sim-ses`, a local SES stand-in for the ses driver |
| `simulators/http/` | `proctor-sim-http`, a general HTTP fake for the stub driver |
| `simulators/otel/` | `proctor-sim-otel`, a local OTLP/HTTP receiver for the otel driver |
| `scripts/build.sh` | Builds `proctor`, its drivers and the simulators |
| `scripts/release.sh`, `.github/workflows/release.yml` | Builds the release archives; drafts a release from a `v*` tag on main |
| `scripts/install.sh`, `scripts/release-install.sh` | The install script's template, which refuses to run; `release.sh` renders it into the release with the version and the archives' checksums |
| `.github/workflows/ci.yml`, `pull-requests.yml`, `workflow-guard.yml` | CI and pull request checks |
| `.github/workflows/proctor.yml` | The [self-check](development.md#self-check) in authoritative mode |
| `.github/rulesets/` | The branch and tag rulesets the owner applies on GitHub |
| `.go-arch-lint.yml`, `scripts/check-arch.sh` | Which packages may import which; `internal/model`, `internal/expr`, `internal/transcript` and `internal/strictyaml` stay free of file, process and network access |
| `examples/github/` | The reference GitHub workflow and CODEOWNERS file |
| `examples/github/verify/` | An example disposable environment for the workflow, copied to `proctor/verify/` |
| `examples/sample/` | A sample users API and its `proctor/` folder, with the `kafka` repository driver, run end to end in CI |
| `proctor/` | Proctor's check of itself with the cli driver, run in CI (see [Self-check](development.md#self-check)) |
