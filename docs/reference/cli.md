# Command reference

## Commands

| Command | What it does | Exit codes |
|---|---|---|
| `proctor init [--project NAME] [--base-url URL] [--ready-path PATH]` | Writes a starting `proctor/` folder in the current folder, never over an existing file, and prints the next steps (see [Quick start](../../README.md#quick-start)). Reads no Git and contacts nothing | 0, 2 a write failed and nothing was written, 3 refused |
| `proctor validate [--commit REV]` | Parses the configuration, manifest and specifications strictly, with each driver's own checks, and rejects specifications likely to give a misleading pass. Never contacts the application. Without `--commit` it reads the working tree, or a folder outside Git as it is (see [Quick start](../../README.md#quick-start)) | 0, 2 a driver couldn't be started, 3 invalid, 4 an integrity violation such as a listed file that's missing or an unlisted one (INT-05) |
| `proctor integrity --commit REV` | Integrity checks on a commit, read from Git's object database: symlinks, submodules, LFS pointers, diff-hiding attributes, unlisted specifications (INT-05), more than one CODEOWNERS | 0, 3, 4 |
| `proctor diff --base REV --head REV [--change-kind KIND]` | Every changed protected file and scenario (`reordered` ones too), with weakenings (removed assertions, waivers, steps moved into setup) printed first; with a kind, also whether the changes match it (see [Change kind](../guides/ci-github.md#change-kind)) | 0, 3 an unknown kind or a folder outside Git, 4 a Git error, a base and head without exactly one merge base (INT-09), or a kind mismatch |
| `proctor run --binding FILE` | Local mode: runs the working tree's specifications against a running application | 0 to 4 |
| `proctor run --binding FILE --only ID[,ID…]` or `--rerun-failed REPORT` | Local mode only: runs just the named scenarios, tables, table cases or specification ids, or those that didn't pass in a saved JSON report, both together if given. The run is partial: the header and the `RESULT` line say how many of how many ran, the report has `selection`, and its `result` is `partial` when all of them passed (exit 0). Validation still judges the whole set, so a policy entry, relation or setup operation that only unselected scenarios use is accepted. An unknown id, or a report with nothing to run again, exits 3; `--authoritative` refuses both (exit 3) | 0 to 4 |
| `proctor run --authoritative ...` | For the trusted CI job: see below | 0 to 4 |
| `proctor approve [--received DIR] [--all \| IDS] [--yes]` | Local mode only: shows each received transcript's or log's diff from the approved one and, when you answer `y`, writes it under `proctor/approved/`. Refused in CI, without a terminal unless `--yes`, and when the specifications changed since the run. See [Approved transcripts](../guides/approvals.md#approved-transcripts) and [Approved logs](../guides/approvals.md#approved-logs) | 0, 2, 3 |
| `proctor logs normalize [--field K=V]… FILE` | Prints a log file's lines as an approved log shows them, to stdout only: only the fields `app_logs.approved` lists, scrubbed. Reads only `proctor.yaml` and the file, writes nothing, contacts nothing. See [Approved logs](../guides/approvals.md#approved-logs) | 0, 2, 3 |
| `proctor mutate [--only IDS] [--scenarios IDS] [--report FILE]` | Local mode only: builds and starts each mutant declared in the protected mutants file from a copy of the working tree, runs the specifications against it, and reports each as killed, survived, `killed_elsewhere`, error or `not_run`. Refused when `CI` or `GITHUB_ACTIONS` is set. See [Mutation testing](../guides/mutation-testing.md) | 0 all killed, 1 a survivor, 2 errors, a failed baseline or a stopped run, 3 invalid |
| `proctor mutate html --from REPORT --out DIR [--coverage PROFILE] [--force]` | Draws the JSON report of `proctor mutate --report` as HTML pages in a new or empty folder outside the repository: the counts and score, the survivors, and each mutated file's source with its mutants. See [The HTML report](../guides/mutation-testing.md#the-html-report) | 0, 2 the source changed since the run, 3 invalid |
| `proctor mutants generate --out FILE [--coverage PROFILE] [--build-check]` | Reads the project's Go code and writes candidate mutants (ids `GEN-0001` and up) to a separate file for a person to review and merge into the mutants file; never writes the mutants file itself. See [Generating mutants](../guides/mutation-testing.md#generating-mutants) | 0, 2, 3 |
| `proctor report show ID… --report FILE [--json]` | Prints each named scenario, table or table case from a saved JSON report with its steps, expected and actual values, as the run's terminal output does, so one failure in a large report can be read without `jq` | 0, 3 a report or ID that isn't there |
| `proctor report failed --report FILE [--json]` | Lists every scenario and table case in a saved report that didn't pass, with its result and reason | 0, 3 |
| `proctor version [--features \| --json]` | The build's version (a build with uncommitted changes ends in `-dirty-` and a hash of them) and the features it has, so a wrapper can check for one by name | 0 |

## Commands and flags

- `validate`, `integrity`, `diff`, `run`, `approve`, `logs normalize`, `mutate` and `mutants generate` take `-r, --repo DIR` (default `.`) and `-c, --config FILE` (default `proctor/proctor.yaml`); all of them but `logs normalize` take `--json`. `proctor -v` prints the version; `proctor mutants` alone prints its help and exits 3.

| Flag | Default | Meaning |
|---|---|---|
| `init --project NAME` | the folder's name | letters, digits, `.`, `_` and `-`, starting with a letter or digit; the default drops other characters |
| `init --base-url URL` | `http://127.0.0.1:8080` | the binding's `base_url`: `http://` or `https://`, with no credentials, query or fragment |
| `init --ready-path PATH` | `/readyz` | the binding's `ready_path` and the starting scenario's `path` |
| `integrity --commit REV` | `HEAD` | the commit to check |
| `diff --pr-head REV` | | `--head` is then GitHub's test merge, whose first parent is the base |
| `diff`, `run`: `--change-kind-source` | `flag` | `flag`, or `label` when the workflow read a `proctor:` label |
| `run`, `mutate`: `--ready-timeout` | `30s` | how long to wait for every driver to report its target ready |
| `run`, `mutate`: `--no-progress` | | no progress line on stderr as each scenario finishes |
| `run`: `--only`, `--rerun-failed` | | local mode only: run a selection, reported as partial; see [Commands](#commands) |
| `run --report FILE` | | in local mode, also writes the `--json` document, with mode 0600 |
| `run --commit REV` | `HEAD` | with `--authoritative`: the tested commit |
| `run --base REV` | | with `--authoritative`: the base, when the head is tested directly, with exactly one merge base with the tested commit (exit 4, INT-09); give `--pr-head` or `--base`, not both and not neither (exit 3) |
| `run --driver-programs DIR` | the checkout | with `--authoritative`: the folder holding the repository drivers' programs at their `program` paths |
| `mutate --call-timeout` | `10s` | the limit for each driver call |
| `mutants generate`: `--goos`, `--goarch` | `linux`, `arm64` | lower-case letters and digits only |
| `mutate html --from FILE` | | a report of at most 512 MiB |

- `approve` without `--received` takes the folder under `$TMPDIR/proctor-received/<project>/` whose `manifest.json` changed last. End of input at its prompt prints `No answer; stopping.` and exits 0.
- `scripts/build.sh [--install] [OUTDIR]` builds into `bin/` by default; `--install` also copies every program into `$(go env GOPATH)/bin`. `proctor` holds its drivers' checksums and refuses one that doesn't match (exit 4, INT-17), so copy the programs together.

- Readiness asks each driver in turn every 250 ms, each attempt bounded by 3 s (5 s for docker and postgres), until `--ready-timeout`; a lasting problem ends the wait at once. A target that isn't ready makes every scenario NOT_RUN `environment_not_ready` (exit 2), and an `app_logs.file` that can't be read, or an `app_logs.otel` receiver that doesn't answer, when the run starts exits 2.
