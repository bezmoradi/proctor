<h1 align="center">⚙️ Engineered, Not Vibe Coded</h1>

<p align="center">
  <img src="https://img.shields.io/badge/Engineered-not%20vibe%20coded-blue?style=for-the-badge" alt="Engineered, not vibe coded">
</p>

## #1 STOP VIBE CODING <img src="https://img.shields.io/badge/Merged-8957e5?style=flat-square&logo=git&logoColor=white" alt="Merged">

```diff
- vibe coded: shipped because it compiled
+ engineered: designed, reviewed, documented
```

<sub>✅ Approved by human reviewers · All checks passed</sub>

# Proctor

Proctor checks that an application still does what people agreed it should do, and refuses to report a pass when the agreement itself was quietly changed. It is for codebases where AI coding agents change the code, the tests and the test configuration in the same pull request. It is a first draft, not yet released.

- The expected behavior lives in YAML specifications under a protected folder, and people approve changes to them through code-owner review.
- A trusted CI job runs every listed scenario against the application built from the tested commit and records what it checked. Proctor approves nothing; GitHub's branch protection does, using Proctor's check and report as evidence.
- Static integrity checks and a semantic diff of protected changes list weakenings first, with an optional [change kind](#change-kind).
- Official drivers for HTTP (with streams and binary, large and file bodies), WebSocket, PostgreSQL (read-only), DynamoDB, SQS, SES, OpenTelemetry, Docker (read-only) and HTTP fakes, with local simulators for SES, OpenTelemetry and the services your application calls; your own drivers can live in your repository.
- Exact JSON assertions, text matchers, time-based checks with per-step timeouts, and tables that can cover every combination of declared dimensions.
- The application-log check, approved transcripts, approved logs and mutation testing.
- `proctor version --features` tells a wrapper what a build supports.

## Table of Contents

- [Install](#install)
- [Quick start](#quick-start)
- [Commands](#commands)
- [Authoritative mode](#authoritative-mode)
- [Specifications](#specifications)
- [Approved transcripts](#approved-transcripts)
- [Approved logs](#approved-logs)
- [Mutation testing](#mutation-testing)
- [Repository drivers](#repository-drivers)
- [Quick reference](#quick-reference)
- [Repository layout](#repository-layout)
- [Development](#development)
- [Privacy](#privacy)
- [Author](#author)
- [License](#license)

## Install

No release is published yet, so build from source: in Proctor's checkout, `scripts/build.sh <dir>` builds `proctor`, its drivers and the simulators into `<dir>` (`bin/` by default).

Once releases exist, each one has an archive per platform (linux and darwin, amd64 and arm64) and a `SHA256SUMS`. Download one, check its checksum and its signed provenance, then check the files inside:

```sh
VERSION=<version> OS=linux ARCH=amd64; name="proctor-$VERSION-$OS-$ARCH"
curl -fsSLO "https://github.com/bezmoradi/proctor/releases/download/v$VERSION/$name.tar.gz"
curl -fsSLO "https://github.com/bezmoradi/proctor/releases/download/v$VERSION/SHA256SUMS"
grep " $name.tar.gz\$" SHA256SUMS | shasum -a 256 -c -
gh attestation verify "$name.tar.gz" -R bezmoradi/proctor \
  --signer-workflow bezmoradi/proctor/.github/workflows/release.yml --source-ref "refs/tags/v$VERSION"
tar -xzf "$name.tar.gz" && (cd "$name" && shasum -a 256 -c SHA256SUMS)
```

Keep `proctor` and its `proctor-driver-*` programs in one folder: `proctor` refuses a driver whose checksum it doesn't hold (exit 4, INT-17); a driver kept in your repository is pinned in `proctor.yaml` instead (see [Repository drivers](#repository-drivers)). `.github/workflows/release.yml` drafts a release when the owner pushes a `v*` tag on a commit on main; the owner then publishes it.

## Quick start

Needs Go 1.27.4 or later, Bash and Git. In Proctor's checkout, build Proctor and its drivers into `bin/` (a driver is found beside the `proctor` program, so `go build ./cmd/proctor` alone isn't enough):

```sh
scripts/build.sh
```

In your application's Git repository, create a `proctor/` folder with four files:

```yaml
# proctor/proctor.yaml
version: 2
project: my-api
manifest: manifest.yaml
drivers:
  http: { official: http }
identities:
  anonymous: { type: none }
```

```yaml
# proctor/manifest.yaml
version: 2
specifications:
  - id: users
    path: specs/users.yaml
```

```yaml
# proctor/specs/users.yaml
version: 2
suite: users
scenarios:
  - id: USR-001
    name: A created user can be read back
    steps:
      - id: create
        do: http.request
        identity: anonymous
        with:
          method: POST
          path: /api/v1/users
          body: { name: "Ada ${run.id}" }
        expect:
          equal:
            $.status: 201
            $.body.message: Created user successfully
        save: { user_id: $.body.data }
      - id: read
        do: http.request
        identity: anonymous
        with:
          method: GET
          path: "/api/v1/users/${user_id}"
        expect:
          equal:
            $.status: 200
            $.body.data.id: "${user_id}"
            $.body.data.name: "Ada ${run.id}"
```

The binding tells Proctor where the application runs:

```yaml
# proctor/environments/local.yaml
drivers:
  http:
    base_url: http://127.0.0.1:8080
    user_agent: proctor-local/0
    ready_path: /readyz
```

Then, from the repository root, with the application running:

```sh
/path/to/bin/proctor validate
/path/to/bin/proctor run --binding proctor/environments/local.yaml
```

`validate` needs neither the application nor Git: in a folder that isn't in a Git repository it reads the files as they are and says `not a Git repository: the folder was read as it is` (`"git": false` with `--json`); with `--commit` it needs Git, and so does every other command that reads the repository. The run waits for `/readyz` (set `ready_path` to your application's health endpoint), executes every listed scenario and prints `RESULT: PASSED (exit 0, local, non-authoritative)` when they all pass. Add `--json` for the machine-readable report.

## Commands

| Command | What it does | Exit codes |
|---|---|---|
| `proctor validate [--commit REV]` | Parses the configuration, manifest and specifications strictly, with each driver's own checks, and rejects specifications likely to give a misleading pass. Never contacts the application. Without `--commit` it reads the working tree, or a folder outside Git as it is (see [Quick start](#quick-start)) | 0, 2 a driver couldn't be started, 3 invalid, 4 an integrity violation such as a listed file that's missing |
| `proctor integrity --commit REV` | Integrity checks on a commit, read from Git's object database: symlinks, submodules, LFS pointers, diff-hiding attributes, unlisted specifications, more than one CODEOWNERS | 0, 3, 4 |
| `proctor diff --base REV --head REV [--change-kind KIND]` | Every changed protected file and scenario (`reordered` ones too), with weakenings (removed assertions, waivers, steps moved into setup) printed first; with a kind, also whether the changes match it (see [Change kind](#change-kind)) | 0, 3 an unknown kind or a folder outside Git, 4 a Git error, a base and head without exactly one merge base (INT-09), or a kind mismatch |
| `proctor run --binding FILE` | Local mode: runs the working tree's specifications against a running application | 0 to 4 |
| `proctor run --authoritative ...` | For the trusted CI job: see below | 0 to 4 |
| `proctor approve [--received DIR] [--all \| IDS] [--yes]` | Local mode only: shows each received transcript's or log's diff from the approved one and, when you answer `y`, writes it under `proctor/approved/`. Refused in CI, without a terminal unless `--yes`, and when the specifications changed since the run. See [Approved transcripts](#approved-transcripts) and [Approved logs](#approved-logs) | 0, 2, 3 |
| `proctor logs normalize [--field K=V]… FILE` | Prints a log file's lines as an approved log shows them, to stdout only: only the fields `app_logs.approved` lists, scrubbed. Reads only `proctor.yaml` and the file, writes nothing, contacts nothing. See [Approved logs](#approved-logs) | 0, 2, 3 |
| `proctor mutate [--only IDS] [--scenarios IDS] [--report FILE]` | Local mode only: builds and starts each mutant declared in the protected mutants file from a copy of the working tree, runs the specifications against it, and reports each as killed, survived, `killed_elsewhere`, error or `not_run`. Refused when `CI` or `GITHUB_ACTIONS` is set. See [Mutation testing](#mutation-testing) | 0 all killed, 1 a survivor, 2 errors, a failed baseline or a stopped run, 3 invalid |
| `proctor mutate html --from REPORT --out DIR [--coverage PROFILE] [--force]` | Draws the JSON report of `proctor mutate --report` as HTML pages in a new or empty folder outside the repository: the counts and score, the survivors, and each mutated file's source with its mutants. See [The HTML report](#the-html-report) | 0, 2 the source changed since the run, 3 invalid |
| `proctor mutants generate --out FILE [--coverage PROFILE] [--build-check]` | Reads the project's Go code and writes candidate mutants (ids `GEN-0001` and up) to a separate file for a person to review and merge into the mutants file; never writes the mutants file itself. See [Generating mutants](#generating-mutants) | 0, 2, 3 |
| `proctor version [--features \| --json]` | The build's version (a build with uncommitted changes ends in `-dirty-` and a hash of them) and the features it has, so a wrapper can check for one by name | 0 |

Exit codes: 0 passed, 1 a scenario failed, 2 an infrastructure error, 3 invalid input, 4 integrity violation. When several apply, 4 wins over 3, 3 over 1, and 1 over 2. Every local `RESULT:` line, from `validate`, `integrity`, `run` and `mutate`, ends `(exit <n>, local, non-authoritative)`, a run that stops before it sends anything included.

On SIGINT or SIGTERM, `run` starts no new scenario, marks the rest NOT_RUN `aborted`, writes its report with `aborted: true` and exits 2 (1 if a scenario already failed); `mutate` likewise starts no new mutant. A panic inside a scenario makes it ERROR `internal` and the run goes on; a panic anywhere else exits 2 with `reason: internal`, and the report is still written.

There is no warning level. Every validation finding fails the command. A step can waive a rule only with a written reason (`no_state_check: <reason>`, or `waive: { FIX-05: <reason> }`), and `proctor diff` lists each added waiver as a weakening.

## Authoritative mode

```sh
proctor run --authoritative --config proctor/proctor.yaml \
  --binding "$RUNNER_TEMP/binding.yaml" --pr-head "$PR_HEAD_SHA" --report "$RUNNER_TEMP/report.json"
```

In this mode Proctor:

- reads every protected input from the tested commit, not the working tree;
- takes the base from GitHub's test merge commit, after checking that its second parent is the pull request head, or with `--base` refuses a base that hasn't exactly one merge base with the tested commit (exit 4, INT-09);
- lists every protected change as "approval required";
- asks GitHub's API for CODEOWNERS errors at the base;
- verifies the job's GitHub Actions OIDC token against GitHub's published keys;
- reports whether the workflow that ran differs from the base branch's;
- refuses to run unless the image the application was started from is the image built from the tested commit;
- takes each repository driver's `program`, `sha256` and `operations` from the base commit's `proctor.yaml`, and reads its program from `--driver-programs DIR` (default: the checkout); see [Repository drivers](#repository-drivers);
- refuses a repository driver that isn't pinned for the job's platform or that the commit tracks;
- with `--earlier-reports DIR`, says which earlier attempts of the same workflow run failed a scenario for the same commit and inputs (see [Re-runs](#re-runs)).

It always writes the JSON report and a job summary. A binding inside the checkout is refused. Nothing runs while any of these checks fails.

GitHub must be configured so that nothing merges without the check and a code owner's approval:

- CODEOWNERS on the protected folder, all of `.github/`, and the CODEOWNERS file itself;
- required code-owner review with at least one required approval (with 0, GitHub doesn't enforce code-owner review), stale approvals dismissed, approval of the most recent push required, and no bypass for administrators;
- the workflow's aggregator job as the required status check, pinned to the GitHub Actions app, so a status posted with a person's token can't satisfy it;
- branches up to date before merging;
- actions pinned by commit SHA, and each job's token limited to the `permissions` it needs.

Code owners and branch protection in a private repository need a paid GitHub plan. [`examples/github/README.md`](examples/github/README.md) gives these settings as a branch protection request and the results of testing them.

The reference workflow ([`examples/github/proctor.yml`](examples/github/proctor.yml)) builds the image in one job and verifies it in another, with a pinned Proctor release archive whose checksums it checks. A third job, `verdict`, is the required check: it fails if any prerequisite failed or was skipped.

Copy `examples/github/` as its README says, and pin `PROCTOR_VERSION` and `PROCTOR_SHA256` to a release you verified (see [Install](#install)). The workflow handles `merge_group` too. It starts the disposable environment from `proctor/verify/` on the base commit, never the pull request's, so merge an environment change before the change that needs it. Its binding marks the environment `disposable: true`.

### Re-runs

Anyone with write access can re-run a failed job, and a passing re-run replaces the failure in the merge check. Proctor doesn't block that, but it makes it visible. The reference workflow uploads each attempt's report as the artifact `proctor-report-<attempt>`, and on a re-run downloads the earlier ones into a folder outside the checkout and passes it with `--earlier-reports`. Proctor then lists, in the report's `earlier_attempts`, on the terminal and in the job summary:

- each earlier attempt that recorded a scenario FAIL for the same run, tested commit and input hashes, with the failing scenario IDs (an ERROR or an integrity violation doesn't count);
- each earlier attempt without such a report, as `unknown`, never as clean;
- each file in the folder it ignored, with the reason: another run, commit or input hashes, a later attempt, a symbolic link or anything but a regular file, more than 64 MiB, or not an authoritative report.

The result and exit code don't change. Proctor needs no token or API call for this. A folder inside the checkout, or the flag in local mode, exits 3.

### Change kind

A pull request can claim what kind of change it is, and Proctor fails it when its protected changes don't fit the claim:

| Kind | Fails when |
|---|---|
| `refactor`, `docs` | any protected file changed |
| `fix` | anything but additions: a weakening, a changed expected value or request, a moved assertion, a specification file that doesn't parse, or a changed protected file other than specification files, new manifest entries and new approved files (so a `proctor.yaml` change fails a fix) |
| `feature`, or no kind | never; every protected change still needs code-owner approval |

```sh
proctor diff --base main --head HEAD --change-kind fix
```

The kind is only a claim, so it can add a failure and never removes one: `feature` keeps today's rules. `proctor diff` exits 4 on a mismatch and lists each one with its place; `proctor run --authoritative` reports each as a `CHG-02` integrity violation and runs nothing. An unknown kind exits 3. In the reference workflow ([`examples/github/proctor.yml`](examples/github/proctor.yml)) the label `proctor:refactor`, `proctor:docs`, `proctor:fix` or `proctor:feature` sets the kind; two `proctor:` labels, or one naming no kind, fail the first job before anything is built. The report's `change_kind` and the job summary record the kind and where it came from (`--change-kind-source label`). Anyone who can label the pull request can pick the kind, and Proctor judges only the protected files, not the application code.

## Specifications

A specification is a suite of scenarios, and a scenario a list of steps, such as the quick start's `proctor/specs/users.yaml`. A step names a driver's operation in `do`, the identity it acts as, the operation's arguments in `with`, what the answer must hold in `expect`, and the values it `save`s for later steps. A step that observes can be sampled until its expectations hold (`eventually`), or must keep them at every sample (`consistently`) or at none (`never`):

```yaml
- id: activated
  do: http.request
  identity: anonymous
  with: { method: GET, path: "/api/v1/users/${user_id}" }
  expect:
    equal: { $.status: 200, $.body.data.state: active }
  eventually: { within: 10s, every: 500ms }
```

`consistently` and `never` take `for` instead of `within`. A step that acts can't be sampled (TIME-11), and the window and interval are bounded by `time_limits` in `proctor.yaml`. Samples are due every `every` from the first and at the window's end, and the last one always runs; a sample that overruns due times is followed at once by one for the latest, and the missed ones are counted in the step's `samples_skipped` (TIME-08). A passing `consistently` or `never` step has a `notice` in the JSON report, and the run prints one `Note:` line counting them, because samples can't see a change between them or after the window (TIME-05).

A scenario or table with `requires: disposable_environment` runs only when the binding says `disposable: true`; otherwise it is NOT_RUN `disposable_environment_required` (exit 2).

Validation requires:

- a later state check after every request that changes state (EXE-07);
- `${run.id}` in every value a scenario writes and later matches (FIX-05);
- every setup operation to also appear as a verification step (FIX-08);
- a destructive request (PUT, PATCH, DELETE, or a POST declared destructive) on a target that refers to neither `${run.id}` nor a value saved earlier in the scenario to follow an earlier read of the same target, or an earlier destructive request on it that EXE-09 accepted without a waiver, or to waive EXE-09 with a reason. A waived request guards nothing, so each later request on the same target needs its own waiver, which `proctor diff` lists as `rule_waived`. A path built from an identity value, such as `/things/${alice.id}`, is a fixed target, and a read counts only when it reads that target as written, a path above or below it, or a path its policy `shows`, expecting an outcome that reads state (EXE-09);
- every path in an expectation, a claim's keys and every `save` path to be one the operation's fact can hold, as its driver's `fact_schema` describes it, and each matcher to name the right kind of value: a list for `length`, `any`, `none` and `count`, an object for `keys`, text for `contains` and `matches`, and text or a list of texts for `excludes` (EXE-11). Inside a free-form part, such as an http `body`, any path is accepted, so a misspelt `absent` there still can't be caught.

At run time, a `none` claim or a `count` with `is: 0` fails when the list has elements but none of them has one of the claim's keys, since such a claim could never fail (EXE-11). That holds when no element has the key's parent, too: `none: { "$.messages": { json.type: error } }` fails when every frame is plain text, because "no JSON frame of type error" would then also pass when the server starts sending its errors as plain text. The message names the missing part; add a claim that some element has it (`any`, or `count` above 0), or assert what the elements are instead.

Besides `equal`, `present`, `absent`, `keys`, `length`, `unordered`, `any`, `none`, `contains` and `count`, three matchers check text and change:

```yaml
expect:
  matches: { "$.headers.x-request-id[0]": "[0-9a-f]{16}" }
  excludes: { "$.body_text": [/workspace, "https://", "@"] }
  differs: { "$.headers.x-request-id[0]": "${first_id}" }
```

- `matches` takes an RE2 pattern that must match the whole text, as if written `^(?:…)$`. Validation refuses (exit 3, EXE-12) an invalid pattern, one longer than 1 KiB, one that matches empty text (`.*`, `[0-9]*`), and one holding `${...}`, which it doesn't expand. A value that isn't text fails.
- `excludes` takes one text or a list; the text at the path must contain none of them. On a list of texts, such as `$.attribute_keys`, no element may contain any of them, and an empty list fails, since nothing could hold them. A missing path or a value that isn't text or a list of texts fails; an empty text, an empty list of texts to exclude and a repeated text are refused (EXE-12).
- `differs` takes any value, usually a saved one; the value at the path must be present and not equal to it, compared as `equal` compares (`7` differs from `"7"`).

`proctor diff` lists removing any of them as `assertion_removed` and loosening one as `assertion_weakened`: any change to a `matches` pattern (Proctor can't tell whether the new one accepts more), an `excludes` text that no remaining text rules out (`/workspace` to `/workspace/x`, or a text dropped from the list), and any change to a `differs` value.

The http driver keeps the first 64 KiB of a body that isn't JSON as `$.body_text` and says whether it cut it in `$.body_truncated` (the ws driver does the same for a refused handshake's body, cut at 1 KiB). `equal`, `contains`, `matches`, `excludes` and `differs` on a cut `$.body_text` fail, naming the cut and its size (`cut_at` in the report), and so does `save` from it; under `never` they fail the step instead of counting as not holding. `present` and `absent` still work (HTTP-13).

A body that claims to be JSON but isn't valid JSON, or has a duplicate key, is kept as `$.body_text` with the reason in `$.body_invalid`, and every assertion on a `$.body` path then fails, `absent` and `never` included (HTTP-10).

A 2xx step must assert something besides its status (EXE-03). For a response without a body, such as a `DELETE` answering 204, assert that the body is absent with both paths, `absent: ["$.body", "$.body_text"]`: a JSON body is `$.body` and any other is `$.body_text`, so the pair fails on any body. Either path alone as the only other assertion is refused, since `absent: [$.body]` passes a text body. A 304 needs only its status, like every 3xx.

When an `equal` on `$.status` fails, the report shows the response's top-level `error` and `message`, or the start of the body, scrubbed and within `report.excerpt_bytes` (`response` in the JSON report). Progress lines name a table's row as `<table>/<case>`, with `tables[<n>]` for a table without an `id`.

A path is `$` followed by `.name`, `[index]` and quoted names. A quoted name, `['…']` or `["…"]`, names a key a `.name` can't, such as one with a dot, a slash or a leading digit; inside it `\\`, `\'` and `\"` stand for a backslash and the quotes. Claim keys are relative to one element, so `['dev.x']` there means `$['dev.x']`:

```yaml
expect:
  equal:
    "$.body.labels['dev.software-factory.stack']": sf-proctor
    '$["body"]["ports"]["8080/tcp"]': open
    "$.headers['x.trace']": [t1]
  none: { "$.body.items": { "['dev.x']": gone } }
save: { stack: "$.body.labels['dev.software-factory.stack']" }
```

Without the brackets, `$.body.labels.dev.software-factory.stack` reads three nested names. Proctor reads every path in one spelling (`$['body']['id']` is `$.body.id`), in the drivers, the report and `proctor diff` too, and refuses a matcher that names one path twice in two spellings (EXE-13).

Tables are told apart by their position in the file, so two tables without an `id` keep their own setup, rows and `${run.id}`; messages call such a table `tables[<n>]`.

A step waits `--call-timeout` (10 s by default) for each call. A slow one can set its own `timeout`, and `time_limits.max_call` in `proctor.yaml` bounds it:

```yaml
# proctor/proctor.yaml
time_limits: { max_call: 120s }
```

```yaml
- id: start
  do: http.request
  identity: alice
  timeout: 120s
  with: { method: POST, path: /api/dispatch, body: { repo: "r-${run.id}" } }
  expect:
    equal: { $.status: 202, $.body.state: started }
```

`max_call` defaults to 60 s. Validation refuses (exit 3, TIME-13) a step timeout longer than it, zero, negative or without a unit, and `run` refuses a `--call-timeout` longer than it before anything is sent, so the longest wait is always in a protected file. `proctor diff` lists a raised `max_call` as `max_call_longer`, and a step timeout added, raised or removed as `step_timeout_longer`; a lowered one is a changed expectation.

A table can carry `eventually`, `consistently` or `never`, and `timeout`, as a scenario's step does; each row is sampled on its own (TBL-08):

```yaml
tables:
  - id: settled
    name: each export settles
    do: http.request
    identity: alice
    with: { method: GET, path: "/exports/${row.id}" }
    expect:
      equal: { $.status: 200, $.body.state: done }
    eventually: { within: 10s, every: 500ms }
    columns: [case, id]
    rows:
      - [csv, "${csv_export}"]
      - [pdf, "${pdf_export}"]
```

A row that acts (a POST that isn't a declared read) can't be polled and is refused by name (TIME-11); a mistake in the values is reported once for the table.

A table can claim to cover every combination of some dimensions, such as every route × every refused method (TBL-07). The dimensions and the sets of combinations live in a protected file that `proctor.yaml` names, and a table names its set with `covers`:

```yaml
# proctor/proctor.yaml
dimensions: dimensions.yaml
```

```yaml
# proctor/dimensions.yaml
version: 1
dimensions:
  post_routes: [/api/add-repo, /api/remove-repo, /api/session]
  other_methods: [PUT, DELETE, PATCH]
sets:
  no_key:
    columns: { route: post_routes }
    except:
      - cells: { route: /api/session }
        reason: "/api/session signs in without a key; sign-in.yaml checks it"
  wrong_method:
    columns: { route: post_routes, method: other_methods }
```

```yaml
tables:
  - id: SEC-T11
    name: a POST route refuses PUT, DELETE and PATCH
    do: http.request
    identity: page
    with: { method: "${row.method}", path: "${row.route}", body: {} }
    expect:
      equal: { $.status: 405 }
    covers: wrong_method
    columns: [case, route, method]
    rows:
      - [t11_put_add_repo, /api/add-repo, PUT]
      - [t11_delete_add_repo, /api/add-repo, DELETE]
```

Across the tables that cover a set (in any listed file, tables without an `id` included), every combination of the set's dimensions, minus its `except` entries, must have exactly one row. Validation fails (exit 3, TBL-07) on:

- a missing combination, named as `no row for (route="/api/remove-repo", method="PATCH")` after a line with the count; at most 50 are listed per set, then the number left;
- a combination with two rows (both are named), and a covered cell that isn't a value of its dimension;
- `covers` naming an unknown set, a column the table lacks, its case id column, or a column the step isn't built from (only `identity`, `variant` and `with` count);
- a set no table covers, a dimension no set uses, a repeated or non-scalar value, and a set of more than 100,000 combinations;
- an exception that names a value outside its dimension, leaves out a column, repeats another, also has a row, or has no reason.

Values are compared as written, before `${...}` is replaced, and with their YAML type, so `"7"` isn't `7`. `proctor diff` lists a value no longer in a dimension, or a column that now takes a dimension without it, as `dimension_value_removed`; a removed set, set column, `dimensions` key or table `covers` as `coverage_dropped`; and a new exception as `coverage_exception_added`. Adding a value, a set or a column only adds checks.

An http request sends at most one body: `body` (JSON), `body_text` (text as written), or one of three forms for bytes that aren't JSON or text (HTTP-14):

```yaml
- { method: POST, path: /api/upload, headers: { Content-Type: image/png }, body_base64: iVBORw0KGgoA//4A }
- { method: POST, path: /api/import, headers: { Content-Type: application/gzip }, body_fill: { bytes: 314572800, pattern: x } }
- { method: POST, path: /api/import, headers: { Content-Type: application/gzip }, body_file: fixtures/evil-repos.tar.gz }
```

- `body_base64` is standard base64 with `=` padding; spaces and line breaks are ignored, so a long value can be a YAML block. Validation refuses invalid base64, an empty body and more than 8 MiB.
- `body_fill` repeats a pattern of 1 to 64 bytes, `pattern` as text or `pattern_base64` for any bytes, to exactly `bytes`. The driver streams it, so a 1 GiB fill never sits in memory.
- `body_file` is a file inside `proctor/`, named relative to it, of 1 byte to 8 MiB. Its path can't be absolute, hold `..` or `${...}`, or end in `.yaml` or `.yml`, and neither the file nor a folder above it can be a symbolic link, even one that stays inside `proctor/` (INT-13). Proctor reads it from the commit or working tree it validates, records its sha256 in the report's `provenance.sha256`, reads it again before the step and refuses the step, sending nothing, if its bytes changed since validation.

These three need a `Content-Type` in `headers`. Every body is bounded by `max_body_bytes` in the http driver's policy (16 MiB when absent, at most 1 GiB); validation refuses one of these three over it, and a `body` or `body_text` over it is ERROR when sent, since its size is known only once values are inserted:

```yaml
drivers:
  http:
    official: http
    policy: { max_body_bytes: 314572800 }
```

The client sets `Content-Length` to the body's true length, so a route that refuses a large `Content-Length` before reading can be checked; specifications still can't set it. The report shows these bodies as their size and sha256 (with the file's path or the fill's pattern), never their bytes. `proctor diff` lists a raised `max_body_bytes` as `max_body_bytes_higher`, a step whose `body_file` now holds other bytes as a `body_file` change with both hashes, and a long `body_base64` as its size and sha256.

The http driver counts GET, HEAD and OPTIONS as reads and every other method as a write. An API whose reads are POSTs can declare those POST requests in the http driver's policy in `proctor.yaml` (HTTP-11):

```yaml
drivers:
  http:
    official: http
    policy:
      reads:
        - { method: POST, path: /api/gate }
        - { method: POST, path: /api/audit }
```

A step whose method and path, as written in the specification, equal an entry is a read: it can be the state check after a write, `eventually`, `consistently` and `never` can poll it, HTTP-09 applies to it, and it can't carry `no_state_check`. The request body isn't matched. Entries take only method POST (PUT, PATCH and DELETE change state, so they can't be declared reads) and a path without a query string; a bad entry fails `validate` and `run` with exit 3. `proctor diff` lists each added entry as a `read_declared` weakening. Without `reads`, every request is judged by its method.

A write's state check must read what the write changed (EXE-07): a later read on the same driver of the same path, or of a path above or below it by whole segments (`GET /things/${id}` after `POST /things`), expecting a status that describes the resource (2xx, 3xx, 404, 409, 410, 412 or 423, not 400, 401, 403, 405, 422 or 5xx). A read that shows a write on another path says so with `shows`, on a `reads` entry or, for a GET, in the policy's own `shows` list, writing `{}` where a step's path has `${...}`:

```yaml
policy:
  reads:
    - { method: POST, path: /api/projects, shows: [/api/add-repo, /api/remove-repo] }
  shows:
    - { path: "/v1/users/{}/notifications", acts: [/v1/notifications/mark-read] }
```

`proctor diff` lists each added path as a `state_check_declared` weakening.

A POST that deletes or overwrites what it names can be declared in the policy's `destructive` list, so EXE-09 guards it like a DELETE:

```yaml
policy:
  destructive:
    - { method: POST, path: /api/remove-repo }
```

Such a request still acts and needs its state check. Its target is its path and its body, so it is fixed unless one of them refers to `${run.id}` or a saved value. Entries take only method POST and a path without a query string, and can't also be in `reads`. `proctor diff` lists an added entry as `destructive_declared` and a removed one as a `destructive_removed` weakening.

Every entry must be used by a step of the listed specifications, or `validate` and `run` fail with exit 3 (HTTP-12), so an entry left behind after a route is renamed can't quietly apply to a route added later on its path. A `reads` or `destructive` entry is used when some step, a table's row included, sends that request. A `shows` path is used when, in one scenario, a read that shows it checks an act on that path: after the act as its state check (not necessarily the first one), or before a destructive act on a fixed target as its guard, with an expectation and an outcome that reads state. Drivers sharing a policy through `policy_from` share its uses.

A `waive` on a table's step written as its reason alone must apply in every row. A waiver for some rows names them, `waive: { EXE-09: { rows: [t01_remove_repo], reason: <text> } }`: validation refuses a named row the rule doesn't apply to, a row it applies to that the list leaves out, and an unknown case id, and `proctor diff` reports each row the list newly covers as `rule_waived`.

Every driver reports what a step reads or changes, and on one driver a read counts after an act only when their targets relate: http and ws compare paths as above (`ws.send` and `ws.messages` take their connection's path, `http.frames` its stream's), dynamodb compares table aliases, sqs queue aliases, postgres connection names, ses always reads `sent`, stub reads and fails `routes/<id>` (`routes` for every route), otel reads `spans` or `logs`, and docker reads `container/<name>`, `network/<name>`, `volume/<name>` or `containers`, `networks`, `volumes`. A read on another driver counts only when `proctor.yaml` declares that the act shows there (INT-20):

```yaml
# proctor/proctor.yaml
relations:
  - act: { driver: http, target: "/v1/notifications/preferences/{}" }
    shows:
      - { driver: dynamodb, target: preferences }
  - act: { driver: sqs, target: notifications }
    shows:
      - { driver: ses, target: sent }
```

Declare a relation only when the application's code shows that the act changes what the read reads. EXE-07's message names the relation that would make a refused read on another driver count. A relation must name targets that steps report, and every `shows` entry must be used by an act and a later read in some scenario, or `validate` and `run` fail with exit 3; quote a target holding `{}`. `proctor diff` lists each added `shows` entry as a `state_check_declared` weakening.

A second driver of the same official program, or the same `program`, that reaches the same application takes the first one's policy with `policy_from: http` instead of its own `policy` (INT-18), so the two lists can't drift apart, and a read on either can be the state check of a write on the other.

With `app_logs` in `proctor.yaml` and `app_logs: { file: <absolute path> }` in the binding, any line at a failing level (ERROR, FATAL or PANIC by default) that the application writes to that file during a scenario fails it, unless the scenario lists it under `expect_logs` (EXE-10). Lines are JSON objects by default; `format: logfmt` reads `key=value` lines as Go's `slog.TextHandler` writes them, and `level_numbers: { 30: INFO, 50: ERROR, 60: FATAL }` maps numeric levels such as pino's; map every number the application writes, since an unmapped one is ERROR (`app_logs_unmapped_level`). A log line that doesn't read under the declared format is ERROR (`app_logs_unparsed`), never a pass, except a panic's stack trace; `unparsed: allow` skips such lines, and `proctor diff` reports adding it as a weakening.

An application that exports its logs over OTLP is checked the same way with `app_logs: { otel: <endpoint> }` in the binding, the address of the `proctor-sim-otel` receiver it exports to (see [Telemetry](#telemetry)), instead of `file`; give exactly one of the two (exit 3). The window is the receiver's log records from the scenario's start to its end, whoever sent them, so an undeclared ERROR from any service fails the scenario. A record's level is its `severity`, its message the body as text, and it reads as one JSON line, `{"time","level","msg","service","trace_id","span_id",<attributes>}`, with an attribute whose name clashes with one of those under `attributes`; `expect_logs`, approved logs and redaction work as with a file. A record without a severity is ERROR `app_logs_unparsed` unless `unparsed: allow`, and `format`, `level_field`, `message_field` and `level_numbers` can't be set with `otel` (exit 3). A receiver that refuses log records during a scenario (it's full, or a record is too large), or restarts, makes the scenario ERROR `app_logs_unreadable`.

Before reading the window, a scenario waits `app_logs.flush_wait` for the application's last lines: `300ms` by default with a file, `1500ms` with `otel` (exporters send logs in batches), at most `10s`.

A database check runs one parameterized, read-only query. Before using the verification role, Proctor checks that the role can only read the objects `proctor.yaml` declares and owns no object at all, types, domains, large objects, extensions and the other catalog objects included (DB-04).

### Cleanup

A scenario or a table can list `cleanup:` acts, which run after its steps whatever happened, a failed setup included; a table's cleanup runs once after each row (EXE-14):

```yaml
- id: SES-010
  name: A started session can be read
  steps:
    - id: start
      do: http.request
      identity: anonymous
      with: { method: POST, path: /api/start-session, body: { name: "s ${run.id}" } }
      expect: { equal: { $.status: 201, $.body.state: running } }
      save: { session: $.body.id }
    - id: check
      do: http.request
      identity: anonymous
      with: { method: GET, path: "/api/session/${session}" }
      expect: { equal: { $.status: 200, $.body.state: running } }
  cleanup:
    - id: stop
      do: http.request
      identity: anonymous
      with: { method: POST, path: /api/remove-session, body: { id: "${session}" } }
      expect: { equal: { $.status: 200 } }
```

A cleanup step that fails turns a passing scenario into ERROR `cleanup_failed`; a failing scenario stays FAIL, with its `first_failure`, and the cleanup is reported apart (`cleanup` and `cleanup_failure` in the report, a `cleanup <result>` line in the terminal and on the progress line). A cleanup step that needs a value whose step didn't run, or a handle that isn't open, is NOT_RUN with nothing sent. A destructive act on a fixed target that EXE-09 accepts because an earlier step guards it is sent only when every earlier step passed (otherwise NOT_RUN `cleanup_unguarded`), since a guard that failed may have found someone else's data; one that waives EXE-09 is always sent. Cleanup steps only act: a step that observes, `eventually`, `consistently`, `never`, `save`, `no_state_check`, `as` and credential variants are refused there (exit 3), and they need no state check (EXE-07). They run inside the scenario's app-log window and appear in its transcript. After an interrupt the drivers are stopped, so the cleanup is NOT_RUN `aborted`. `proctor diff` lists a removed cleanup step (`cleanup_step_removed`), a verification step moved into cleanup (`step_moved_to_cleanup`) and a loosened cleanup expectation as weakenings.

### Streams

`http.open` and `http.frames` read a response that stays open, newline-delimited JSON (NDJSON) or server-sent events (SSE), the way `ws.connect` and `ws.messages` read a WebSocket (HTTP-15):

```yaml
steps:
  - id: open
    do: http.open
    identity: page
    as: events
    with: { path: /api/events }
    expect:
      equal: { $.status: 200, $.open: true, $.format: ndjson }
  - id: hello
    do: http.frames
    on: events
    eventually: { within: 2s, every: 100ms }
    expect:
      any: { "$.frames": { json.type: hello } }
  - id: no-errors
    do: http.frames
    on: events
    consistently: { for: 1s, every: 200ms }
    expect:
      none: { "$.frames": { json.type: error } }
      equal: { $.overflow: false }
```

- `http.open` acts and always sends GET, with `path`, `query` and `headers` as a request takes them, the binding's User-Agent and the identity's credential. It returns at the status and headers. `format: ndjson | sse` is optional: without it `text/event-stream` reads as SSE, `application/x-ndjson` and its siblings as NDJSON, and any other 2xx is ERROR naming the Content-Type. A non-2xx answer opens no stream; its fact has `open: false` and the body as a request's.
- `http.frames` answers at once with every frame since the stream opened: `count`, `frames`, `closed` and `close_reason`, `overflow` and `overflow_reason`, `body_text` (the first 1 MiB as received) with `body_truncated`, and for SSE `retry`. An NDJSON frame is a line, with `text` and `json` (or `json_invalid`); blank keep-alive lines aren't frames. An SSE frame is an event, with `event`, `data` (its `data:` lines joined with a newline), `id` and `json`; comments aren't frames. Wait for frames with `eventually`, `consistently` or `never`.
- The first 10,000 frames, 4 MiB of them and 1 MiB per frame are kept. Past a limit, or when the stream ends inside a frame, `overflow` is true and every claim on `$.frames` or `$.count` but `any` and `present` fails, naming the reason, since a frame left out could hold what `none` rules out. The same holds for `ws.messages`, which keeps the first 10,000 frames and 16 MiB of them.
- `http.frames`, `ws.messages`, `ses.sent`, `stub.received`, `otel.spans` and `otel.logs` report what has arrived so far. `eventually` on one of them with only `none`, `absent`, `excludes`, a zero `count` or `length`, `equal` to false, 0 or an empty value, or `differs` from anything else is refused (exit 3, TIME-14): it would pass at the first sample, before anything arrived. Use `consistently` or `never`, or add a claim that waits, such as `equal: { $.closed: true }` or `length: { $.traces: 1 }`.
- The stream is closed at the end of the scenario, passed, failed or errored. A value at a sensitive path in a frame (`json.api_token`) is scrubbed from the frame's `text`, `body_text` and the excerpt too.

### Docker checks

The `docker` driver reads containers, networks and volumes from Docker's Engine API, with six operations: `container`, `network` and `volume` take a `name`, and `containers`, `networks` and `volumes` take optional `labels` and `has_labels`. Its policy in `proctor.yaml` names the labels that make an object visible:

```yaml
drivers:
  docker:
    official: docker
    policy:
      scope:
        - { label: com.docker.compose.project, value: sf-proctor }
        - { label: dev.software-factory.stack, value: sf-proctor }
```

An object is visible when it carries at least one listed label with exactly that value; everything else reads as not found, and a visible object's reference to one outside the scope (a network member, a network, a mounted volume) is shown without its name. The binding names Docker's Unix socket, with no default and no fallback to `DOCKER_HOST`:

```yaml
drivers:
  docker:
    socket: /var/run/docker.sock
```

A fact holds a fixed projection of each object's settings (user, read-only root, capabilities, security options, limits, restart policy, networks with their addresses, mounts, labels as `{ key, value }` lists), and never its environment, command, entrypoint, healthcheck command, log options or host paths. A bind shows only as `source: docker_socket` or `host_path`; it reads `docker_socket` (and the container `docker_socket: true`) when its destination is `/var/run/docker.sock` or `/run/docker.sock`, its source is a Docker socket (Docker Desktop's `/run/host-services/docker.proxy.sock` included) or a folder that holds one, and the rule errs towards `docker_socket`. Each step's `request_excerpt` lists every request it sent, inspects included. The report states `proof read_only: unproven`: Docker has no read-only socket, so the driver limits itself to GET requests on a fixed list of paths, and a run needs the same trust as `docker ps`. `proctor diff` lists a scope entry added or removed as a `docker_scope_widened` or `docker_scope_narrowed` weakening, and an added driver as `driver_added`.

A volume mount reads `source: docker_socket` (and the container `docker_socket: true`) when it's mounted at one of those socket destinations, or when its volume is a `local` bind (`-o type=none -o o=bind -o device=…`) whose device, or the mount's `subpath`, is a socket or a folder that holds one. The driver reads the device only to decide this and never shows it. A container that mounts a volume outside the scope, or a volume whose driver isn't `local`, makes the step ERROR (`unsupported_result`): the driver can't read a volume outside the scope and doesn't know other volume drivers, so it can't tell whether the mount reaches the socket, and guessing `false` could pass a check that should fail. Compose doesn't give the anonymous volumes it creates (an image's `VOLUME`, or a bare `- /path` under `volumes:`) the project label, so give the containers your specifications read named volumes instead.

The socket rule reads only paths, so Docker's socket under another name and destination (`-v /srv/x.sock:/tmp/y.sock`), or bound through a folder DKR-07 doesn't list, such as the home folder, reads `host_path`. The driver can't look at what a source path names: Docker reports only the path, and on Docker Desktop that path is resolved inside Docker's VM, not on the machine running Proctor. A check like "only the helper holds Docker's socket" can then pass while another container holds it too, so also assert that a container has no host bind you don't expect:

```yaml
        expect:
          equal: { "$.container.docker_socket": false }
          none: { "$.container.mounts": { source: host_path } }
```

### SQS peeks

`sqs.peek` reads a queue's messages without deleting any. It receives with visibility timeout 0, and the pilot's emulator then returns the same oldest 10 messages to every receive, so a longer queue reads as `truncated: true`. To read past them, list the queue's alias in `peek_hold`:

```yaml
drivers:
  sqs:
    official: sqs
    policy:
      queues:
        dlq: orders-dlq
      peek_hold: [dlq]
```

A held peek hides each message it receives from every other consumer, your application's included, until about 2 s before the step's deadline and for at most 30 s, then sets every one visible again. If that fails, the step is ERROR and says how many messages stay hidden and for how long. `peek_hold` is off by default, takes only aliases from `queues`, and refuses FIFO queues, where one held message stops the rest of its group (exit 3, rule SQS-03). `proctor diff` lists turning `peek_hold` on, or adding a queue to it, as a `driver_policy_changed` weakening.

`sqs.peek` refuses a queue that has a redrive policy (rule SQS-04). Every receive, held or not, raises each message's `ApproximateReceiveCount`, which counts towards the policy's `maxReceiveCount`, so repeated peeks, for example under `eventually`, would move the queue's messages to its dead-letter queue. Before its first receive, a peek reads the queue's `RedrivePolicy`; if there is one, the step is ERROR (`invalid_request`, not retried), naming the dead-letter queue and `maxReceiveCount`, and nothing is received. Check such a queue with `sqs.depth`, and peek its dead-letter queue instead; `sqs.send` works on it as usual. `validate` can't see a redrive policy, since it doesn't contact SQS, so a `peek_hold` entry for such a queue is accepted and its peeks are refused at run time. Feature `sqs.peek_redrive_refused`.

### HTTP fakes

`proctor-sim-http` stands in for an HTTP service your application calls, such as a vendor API, and the `stub` driver reads the requests it got and makes it fail on demand. Run one sim per faked service, each with its own `--addr`, route file and driver, such as `apic: { official: stub }` in `proctor.yaml` and `apic: { endpoint: "https://apic.sim.test:18194", ca_bundle: /tmp/sim/apic-ca.pem }` in the binding.

Keep the route file under `proctor/`, such as `proctor/sims/apic.yaml`, so it's protected and `proctor diff` shows its changes. It is strict YAML, like Proctor's own files: unknown or duplicate keys, anchors, aliases, merge keys, explicit tags and a second document are refused, and the sim exits 2 naming the line.

```yaml
# proctor/sims/apic.yaml
version: 1
redact: { headers: [apic-challenge], json_paths: [aaaUser.attributes.pwd], query: [token] }
routes:
  - id: aaa_login
    match:
      method: POST
      path: /api/aaaLogin.json
      body: { aaaUser: { attributes: { name: proctor } } }
    respond:
      headers: { Set-Cookie: "APIC-cookie=sim-token; Path=/" }
      json: { imdata: [{ aaaLogin: { attributes: { token: sim-token } } }] }
  - id: leaves
    match:
      method: GET
      path: /api/class/fabricNode.json
      query: { query-target-filter: { matches: 'eq\(fabricNode\.role,"leaf"\)' } }
      headers: { cookie: { matches: '.*APIC-cookie=sim-token.*' } }
    respond: { file: apic/leaves.json, headers: { Content-Type: application/json } }
  - id: mr_get
    match: { method: GET, path: "/api/v4/projects/{project}/merge_requests/{iid}" }
    respond: { json: { iid: "{{path.iid}}", state: opened }, delay: 200ms }
```

- A route's `id` matches `^[a-z][a-z0-9_]{0,62}$`. `method` and `path` are required; `query`, `headers` (names in any case) and `body` narrow the match. A value is exact text or `{ matches: <RE2> }`, which must match the whole value, and extra names are allowed. `body` is a JSON subset: every key it lists must match, and lists and other values must be equal; a body that isn't JSON never matches.
- Paths are compared segment by segment, with escapes decoded within a segment, so `group%2Fproject` stays one segment; `{name}` is one whole segment. A path holds no query, and a path under `/__proctor/` is refused.
- The first matching route in the file wins, and two routes with the same `match` are refused. A request no route matches gets 404 with `X-Proctor-Sim: unmatched`, and is logged and counted in `unmatched`.
- `respond` takes `status` (200 to 599, default 200), `headers`, at most one of `json`, `text` and `file`, and `delay` (1ms to 10m). A `file` is relative to the route file's folder, without `..` or a symbolic link, at most 8 MiB, read once at start, and needs a `Content-Type`. An answer that always fails, such as a 500, is an ordinary route.
- `{{path.<name>}}`, `{{query.<name>}}` and `{{seq}}` (the request's number in the log) work in `json` values, `text` and header values; a value that is exactly `"{{seq}}"` becomes a number. Any other template is refused.

The sim logs every request before it answers. It writes `[redacted]` in place of `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`, `X-Auth-Token`, `X-Subject-Token`, `Private-Token`, `X-Api-Key` and what `redact` lists before it keeps anything, so a hidden value is never in the log or the report. To check that the application sent the right credential, match it in a route (`headers: { authorization: "Bearer sim-gitlab" }`) and count that route's requests. Other bodies, such as forms, are kept as they are.

Every scenario shares the sim's log, so a scenario marks where it starts and reads after the mark. `stub.fail` opens a handle whose fault lasts until the end of the scenario, passed, failed or errored:

```yaml
steps:
  - id: login-down
    do: apic.fail
    as: apic_down
    with: { route: aaa_login, status: 503, json: { imdata: [] }, times: 2, for: 5m }
    expect: { equal: { $.active: true } }
    save: { mark: $.last_seq }
  - id: sync
    do: http.request
    identity: anonymous
    with: { method: POST, path: /api/sync }
    expect: { equal: { $.status: 502, $.body.error: upstream_unavailable } }
  - id: saw-fault
    do: apic.received
    with: { route: aaa_login, after: "${mark}" }
    expect:
      equal: { $.count: 2, $.unmatched: 0 }
      any: { $.requests: { fault: true, status: 503 } }
```

Without a fault, take the mark with a read: `do: apic.received`, `save: { mark: $.last_seq }`.

- `stub.received` takes `route`, `method`, `path` (the escaped path, without the query) and `after` (a number, or a saved `"${mark}"`), all optional. Its fact has `count`, `requests` (oldest first, each with `seq`, `at`, `route`, `method`, `host`, `path`, `query`, `headers`, `body` or `body_text`, `body_truncated`, `body_invalid`, `body_bytes`, `status` and `fault`), `unmatched` (requests no route matched after `after`, whatever the other filters), `last_seq`, `overflow` and `overflow_reason`. `status` is 0 when no answer was sent: a `reset`, or a client that gave up during a delay.
- `overflow` is true when requests in scope were dropped from the log, when more than 100 are in scope, or when an in-scope body was cut at 64 KiB. Then every claim on `$.requests` or `$.count` but `any` and `present` fails, naming the reason; narrow the read with `after`, `route` or `path`.
- `stub.fail` takes `route` (an id, or `"*"` for every request, unmatched ones included), then exactly one of `status` (with optional `headers` and `json` or `text`) and `reset: true` (the connection is reset without an answer), with an optional `delay` before either; `delay` alone gives a slow but normal answer, which with the application's timeout is a timeout. `times` (1 to 10,000) limits how many requests it changes, and `for` (1s to 1h, default 10m) how long it lasts, so a fault a stopped run left behind ends by itself. Its fact is `id`, `route`, `active`, `expires_at` and `last_seq`. The driver deletes the fault when the scenario ends, and any fault still open when it stops.
- A route has at most one active fault, and `"*"` overlaps every route: another fault on it is ERROR STUB-03, naming the active fault and its expiry. A `route` that isn't in the sim's route file is ERROR STUB-02 at run time, never a count of 0.
- Validation refuses a wrong combination of `fail`'s arguments (STUB-01), `stub.received` in setup (FIX-03), and `${fill(...)}` and `${secret...}` in either. `stub.fail` acts on `routes/<id>` (`routes` for `"*"`) and needs a later `stub.received` on the same driver as its state check (EXE-07); TIME-14 applies to `stub.received` (see [Streams](#streams)).
- A read on the sim counts as the state check of a request to the application only through a relation, declared when the application's code shows that the request calls that route: `{ act: { driver: http, target: /api/v1/apic/createPortConfig }, shows: [{ driver: gitlab, target: routes/create_commit }] }`.

For HTTPS, `--tls-name apic.sim.test,127.0.0.1 --ca-out /tmp/sim/apic-ca.pem` makes a CA in memory, valid for 30 days, signs a certificate with those names, and writes only the CA certificate (mode 0644; a symbolic link there is refused). No key touches the disk. Make the application trust that file (for example with `SSL_CERT_FILE` or `NODE_EXTRA_CA_CERTS`), give it to the driver as `ca_bundle`, and reach the host name through a compose network alias or a hosts entry. `--cert FILE --key FILE` serves your own pair instead. TLS 1.2 is the minimum, the sim speaks HTTP/1.1 only, and client certificates aren't supported. The route file's sha256 is in `GET /__proctor/routes` and in the sim's first log line.

`POST /__proctor/reset` clears the log and the faults, for scripts between runs; `seq` keeps counting. Not supported: response sequences and state (for "the first call fails and the retry succeeds", use a fault with `times: 1`), record or proxy mode, OpenAPI-generated answers, XML, form and regex body matching, HTTP/2, streaming answers, WebSockets and reloading the route file.

### Telemetry

`proctor-sim-otel` is a local OpenTelemetry receiver, and the `otel` driver reads what it received: `otel.spans` for traces and `otel.logs` for log records. Declare `otel: { official: otel }` under `drivers`, and point both the application's OTLP exporter and the driver's binding `endpoint` at the receiver, such as `http://127.0.0.1:4318`.

`trace: true` on an `http.request` sends a fresh sampled `traceparent` (`00-<random trace id>-<random span id>-01`) and puts its `trace_id` in the fact. Save it and read that trace:

```yaml
- id: call
  do: http.request
  identity: alice
  with: { method: GET, path: /api/v1/stacks, query: { name: "s-${run.id}" }, trace: true }
  expect: { equal: { $.status: 200, $.body.count: 1 } }
  save: { trace: $.trace_id }
- id: one-trace
  do: otel.spans
  with: { trace_id: "${trace}" }
  expect:
    length: { $.traces: 1 }
    unordered: { "$.traces[0].services": [stacks-api, stacks-worker] }
    equal: { "$.traces[0].root_count": 1 }
  eventually: { within: 10s, every: 250ms }
- id: error-status
  do: otel.spans
  with: { trace_id: "${trace}", kind: server }
  expect:
    any: { $.spans: { status.code: error, "attributes['http.response.status_code']": 500 } }
  eventually: { within: 10s, every: 250ms }
```

- The sampled flag makes a parent-based sampler (the SDK default, `parentbased_always_on`, or `ParentBased(TraceIDRatioBased(…))`) keep the trace whatever its ratio. Validation refuses `trace: true` together with a `traceparent` in `headers`, and `$.trace_id` in `save` or `expect` without `trace: true` (exit 3). Without `trace: true` the request is sent as before.
- Every `otel.*` step must set `trace_id`, or filter on a value that holds `${run.id}` or a saved value (exit 3, OTEL-01): the receiver keeps every scenario's and every run's telemetry, so an unscoped read would match other people's records. `${run.id}` can't be a trace id (it isn't 32 hex characters), so without `trace: true` save a `trace_id` the application returns, or filter on an attribute that holds `${run.id}`.
- Both reads are verification steps, refused in setup (FIX-03), and count as an act's state check only through a `relations` entry (targets `spans` and `logs`). TIME-14 applies to both (see [Streams](#streams)).
- Exporters send in batches, so read with `eventually`. In the test environment set `OTEL_BSP_SCHEDULE_DELAY=200` and `OTEL_BLRP_SCHEDULE_DELAY=200` (the SDK defaults wait up to 5 s for spans and 1 s for logs), `OTEL_TRACES_SAMPLER=parentbased_always_on`, and `OTEL_EXPORTER_OTLP_ENDPOINT=http://<receiver>:4318` with `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf` or `http/json`. A `none` read straight after the act passes before anything arrived, so assert what must be there first.
- The receiver speaks OTLP/HTTP only. An exporter that uses gRPC sends through a test-only collector whose `otlphttp` exporter has `endpoint: http://proctor-sim-otel:4318` in its `traces` and `logs` pipelines; its batch processor adds its own delay (200 ms by default), so allow for it in `within`.
- The receiver never resets: start it in the environment's `up` script, and restart it to empty it. Once it refuses anything (a request it can't read, one too large or of another media type, or records past its limits), every later `otel.*` fact has `overflow: true` and an `overflow_reason`, and so does a read matching more than 1,000 records. Then every claim on `$.spans`, `$.logs` or `$.count` but `any` and `present` fails, since a refused record could be the one `none` rules out.
- `excludes` on a list forbids attribute names: `excludes: { $.attribute_keys: [authorization, cookie, password] }`.

After every sample of `otel.spans` or `otel.logs`, Proctor looks in the text of `$.spans` or `$.logs`, keys included, for every credential and secret it knows: each bound identity's token or variant, each declared secret the binding sets, and each binding `drivers.<name>.<key>_env` value, used by a step or not. A hit FAILs the step at once with reason `secret_in_telemetry`, without sampling again under `eventually`, `consistently` or `never`; the report names the path, such as `$.spans[0].attributes['url.query']`, and whose value it holds, and shows the value only as `[redacted]`.

## Approved transcripts

A transcript is a scenario's whole story: each step's interpolated request and the fact that came back, setup included. You read and approve it once; later runs compare their transcript with the approved one byte for byte. It adds a check and replaces none: the scenario's explicit assertions stay the reviewed contract.

```yaml
scenarios:
  - id: REP-001
    name: a created report reads back
    transcript: true
    steps: [...]
tables:
  - id: KIND-T01
    transcript: true          # one transcript per row
```

```yaml
# proctor/proctor.yaml
transcripts:
  max_bytes: 1048576          # the default; at most 8 MiB
  scrub:
    - { name: time, pattern: '[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+Z' }
    - { name: request_id, pattern: 'req-[0-9a-f]{12}' }
    - { name: date, path: '$.headers.date', numbered: false }
    - { name: date_parts, path: '$.header_lists.date', numbered: false }
```

Before comparing, Proctor scrubs each step's `with` and `fact`: credentials, secrets and redacted fields become `[redacted]`; the scenario's run id, saved values and identity values become `${run.id}`, `${name}` and `${alice.id}`; then each `scrub` rule replaces what it matches with `<name-N>`, numbered by first appearance so equal values stay visibly equal (`numbered: false` makes every match `<name>`, for values like an HTTP `Date` that repeat only sometimes). A `path` is read in each step's fact; the http driver holds each header twice, in `headers` and `header_lists`, so name both or use a `pattern`. Run twice and compare the received files before approving: anything that still differs needs a rule.

The flow:

1. `proctor run --binding …` fails a scenario with no approved transcript as `transcript_not_approved`, and writes what it saw to a new private folder outside the repository, printed as `Received:` (default `$TMPDIR/proctor-received/<project>/<run id>/`, or `--received DIR`, which must be new or empty).
2. Read each received file. `proctor approve REP-001` (or `--all`) shows the diff and asks `[y/N]` per file; `y` writes `proctor/approved/<spec id>/<scenario id>.transcript.yaml`.
3. Commit the approved files. They're protected inputs, so the pull request needs code-owner review: that review is the approval. `proctor approve` can't tell whether a person answered.
4. Later runs FAIL `transcript_changed` with a short diff when the story changes, and ERROR `transcript_too_large` over `max_bytes`. A scenario's own FAIL or ERROR stands.

`approve` refuses when `CI` or `GITHUB_ACTIONS` is set, when stdin isn't a terminal and `--yes` isn't given, when any specification, the configuration or the `transcripts` settings changed since the run, when a received or approved file changed since the run, and for a scenario that failed on its own. Validation refuses a file under `proctor/approved/` that no opted-in scenario owns (INT-19), a link anywhere there, an approved file over `max_bytes` or without its format line, and two ids that differ only in letter case. `proctor diff` lists `transcript_dropped`, `scrub_widened` and each changed approved file with the lines changed. Authoritative mode reads the approved files from the tested commit and writes nothing under the checkout.

## Approved logs

An approved log is the part of the application's log a scenario produced, cut to the fields you choose and scrubbed, that a person read and approved. Later runs compare their log with it byte for byte. It needs the app-log check (`app_logs` in `proctor.yaml`, and `app_logs.file` or `app_logs.otel` in the binding), and it allows nothing: an ERROR line still fails the scenario, whatever the approved log holds. With `app_logs.otel`, records from several processes interleave, so use `order: sorted`.

```yaml
scenarios:
  - id: REP-001
    approved_log: true
tables:
  - id: KIND-T01              # a table needs an id; one log for the whole table
    approved_log: true
```

```yaml
# proctor/proctor.yaml
app_logs:
  format: logfmt
  approved:
    fields: [method, path, status, user, request_id, took]   # required; every other field is left out
    order: written                         # or sorted, for lines written concurrently
    max_bytes: 1048576                     # the default; at most 8 MiB
    scrub:
      - { name: req, field: request_id }   # the whole value
      - { name: dur, field: took, pattern: '[0-9.]+ms' }
      - { name: time, pattern: '[0-9]{2}:[0-9]{2}:[0-9]{2}' }
```

Each line is `<LEVEL> <msg> k=v…` with the listed fields in their order; `panic:` and `fatal error:` lines are `RUNTIME <line>` without their stack trace; lines that don't parse aren't shown and are counted in the last line, `unparsed lines: N`. Scrubbing is a transcript's (secrets and credentials, then `${run.id}`, saved values and identity values), plus `[redacted]` for a field or nested key with a sensitive name (`password`, `token`, `secret`, `api_key`, `authorization`, `cookie`), then the `scrub` rules. A received log looks like:

```text
format: "proctor-log/1"
INFO "report created" method=POST path=/reports/${report_id} status=201 user=${alice.id} request_id=<req-1> took=<dur-1>
RUNTIME panic: runtime error: index out of range [3] with length 2
unparsed lines: 1
```

The flow is the transcripts' flow: the first run FAILs `log_not_approved` and writes `<spec id>/<id>.log` to the `Received:` folder; read it, `proctor approve REP-001` (an id names both a scenario's transcript and its log), commit `proctor/approved/<spec id>/<id>.log` for code-owner review; later runs FAIL `log_changed` with a diff. `approve` refuses a log whose scenario didn't pass on its own, the app-log check included. `proctor diff` lists `approved_log_dropped`, `scrub_widened` (a field no longer listed, a rule added or changed) and `log_order_relaxed` (`order` became `sorted`).

To compare a production incident with a scenario: take the incident's lines from production with your own tools; write a scenario that sends the same request as the same kind of identity, with `approved_log: true`; run it; then

```sh
proctor logs normalize excerpt.log --field request_id=8f2c > excerpt.normalized.log
diff excerpt.normalized.log "$RECEIVED/users/REP-001.log"
```

and change the scenario until the only difference is the defect (or nothing, once it's fixed), then approve. `normalize` prints to stdout only and never writes a file. Proctor doesn't know production's secrets, so only the field list, the sensitive names and your scrub rules keep them out of its output: read it before you share it. Proctor never fetches production logs, and an approved log always comes from a test run.

## Mutation testing

`proctor mutate` checks that your specifications catch defects you declare. Each mutant is an exact text edit of the application; your own scripts build and start the changed copy, and Proctor runs the scenarios against it. A mutant is killed when at least one scenario FAILs.

```yaml
# proctor/proctor.yaml
mutation:
  mutants: mutants.yaml            # under proctor/, so it is protected like the specifications
  up: env/mutant-up.sh             # builds and starts the copy; receives the copy's folder as $1
  down: env/mutant-down.sh         # stops it; always runs once up has started
  binding: /tmp/my-app/binding.yaml  # absolute, outside the repository; up writes it
```

```yaml
# proctor/mutants.yaml
- id: M-01
  title: Create answers 200 instead of 201
  edits:
    - file: internal/handlers/users.go
      find: "w.WriteHeader(http.StatusCreated)"
      replace: "w.WriteHeader(http.StatusOK)"
- id: M-02
  title: Only the second greeting changes
  killed_by: [GRT-002]
  edits:
    - file: labels.go
      line: 8
      find: "\treturn \"hello\"\n"
      replace: "\treturn \"bonjour\"\n"
```

Writing mutants:

- `find` must be unique in the file, or appear exactly `count` times (default 1), and every occurrence is then replaced. Proctor never picks one of several matches: any other number of matches is an ERROR, "the edit didn't apply: found N times, expected M", never a kill. Make `find` long enough to be unique.
- To change one of several identical blocks, give `line`, the 1-based line where that block's `find` starts (it may span lines). If `find` doesn't start on that line, the mutant is an ERROR, "find not at line N", never a silent change somewhere else. `line` can't be combined with a `count` other than 1, and `proctor validate` checks that each `line` is in the file and that `find` starts there.
- Several edits of one file apply in the order written, and each is located in the original file, before any edit; edits that overlap are an ERROR.
- An edit's `file` is repository-relative, a regular file tracked by Git or untracked and not ignored, and never a protected input: a mutant can't edit the specifications or Proctor's configuration.
- `killed_by` names the scenarios meant to catch the mutant. If only other scenarios fail, the verdict is `killed_elsewhere`, which counts as survived.
- Proctor copies the files `git ls-files -co --exclude-standard` lists into a fresh 0700 folder under `$TMPDIR` for the baseline and each mutant, without `.git` and without ignored files; a listed symbolic link refuses the run. `up` runs from your repository with the copy's folder as its argument, and `PROCTOR_MUTANT` (`baseline` or the mutant's id) and `PROCTOR_MUTANT_DIR` in its environment.
- The baseline (the unchanged copy) must pass, or no mutant runs (exit 2). The first `up` or `down` that fails or times out (`--up-timeout`, default 45m; `--down-timeout`, default 10m) stops the run, and the rest are `not_run`; a script past its timeout is killed with its whole process group. `--keep-going` goes on after an `up` that exits non-zero, never after a failed `down`, which may have left containers or images behind.
- Write some mutants without reading the specifications, or have someone else write them, so the score measures the specifications rather than their fit to known defects. Each full image build takes time, so use `--only` while writing them.

`proctor diff` lists a removed mutant (`mutant_removed`), a changed edit or `killed_by` (`mutant_changed`) and a changed or removed `mutation` key (`mutation_changed`, `mutation_removed`) as weakenings; an added mutant isn't one.

### The HTML report

`proctor mutate html` draws a report `proctor mutate --report` wrote as pages to open in a browser, so a long run can be looked at again without running it again:

```sh
proctor mutate --report /tmp/mutate.json
proctor mutate html --from /tmp/mutate.json --out /tmp/mutate-html --coverage /tmp/cover.txt
open /tmp/mutate-html/index.html
```

- `index.html` has the counts per verdict, the mutation score, the run time, Proctor's version, the survivors (`survived` and `killed_elsewhere`) with links to them, and a table of the mutated files, those with survivors first. The score is the mutants a scenario caught (`killed` and `killed_elsewhere`) out of those and the `survived` ones; `error` and `not_run` mutants aren't in it.
- Each mutated file gets a page with its whole source and line numbers. A mutated line carries each mutant's verdict as a word, not only a colour, and under it the mutant's id, title, operator (for a generated mutant), verdict and message, the scenarios that failed ("no scenario failed" otherwise) and its `killed_by`, and a diff of the lines its edit changes. A mutant whose edit didn't apply in the run is listed at the top of its file's page. With `--coverage` (a text profile, mapped to files as for `mutants generate`), lines in blocks that never ran are greyed and marked "never ran".
- The pages are read from the repository the report names, as it is now. A mutant whose edit applied in the run but whose `find` is no longer in its file, or no longer at its `line`, refuses the pages (exit 2), naming the mutant, rather than drawing the wrong lines; a file that changed elsewhere gets a warning and a note on its page.
- Each page is plain HTML that stands alone, with the stylesheet inline in one `<style>` element: no script, no other file, nothing loaded from the network, so it renders the same opened from disk. Its Content-Security-Policy is `default-src 'none'; style-src 'sha256-…'; img-src 'none'`, the hash computed from the same embedded stylesheet bytes the page holds, so only that `<style>` element applies and no `style=` attribute is used. All text, source code, titles, scenario ids and file names included, is escaped by `html/template`, and file pages are named `file-0001.html` and so on, never after their paths.
- `--out` must be outside the repository (exit 3: the pages hold its source code), not a symbolic link, and new or empty unless `--force`, which replaces the pages' files in it and leaves its other files. Each file is written with mode 0600 and the folder, when Proctor creates it, with 0700.
- The report must come from a Proctor with this command (feature `mutate.html`): older reports have no `provenance.repository` and no `edits` per mutant, and are refused (exit 3).

### Generating mutants

`proctor mutants generate` writes a first draft of mutants for a Go project, so nobody types hundreds of edits by hand. It reads Go only, with the standard library's parser, and applies eight operators:

| Operator | Change |
|---|---|
| `negate` | an `if` or `for` condition `c` becomes `!(c)` |
| `boundary` | `<` and `<=`, `>` and `>=` swap |
| `equality` | `==` and `!=` swap |
| `logic` | `&&` and `\|\|` swap, with parentheses added where the swap would change how the expression groups |
| `swallow` | in a function whose result is `error`, a returned error becomes `nil` |
| `remove-call` | a call statement or a `defer` is removed (`_ = 0` after a label) |
| `bool` | `true` and `false` swap |
| `constant` | an integer literal n becomes n+1, never in an array length or a shift, never to a type's largest value, and never to a value a neighbouring `case`, map key or constant already has |

```sh
proctor mutants generate --out /tmp/gen.yaml --index /tmp/gen.md \
  --coverage /tmp/cover.txt --build-check --limit 300 --seed 1
```

- Every mutant has one edit with `line` and `find`, the exact source text of the changed expression or statement, so `proctor mutate` changes exactly that spot and a stale line is an ERROR. When that text also starts elsewhere on the same line, `find` runs on to the end of the line, or starts at the line's first token, until one match starts there; a mutant whose `find` can't be made unique, is over 512 bytes, or leaves a file that doesn't parse is left out.
- `--coverage` takes a text profile (`go tool covdata textfmt -i DIR -o FILE`, or `-coverprofile`). With it, a mutant is written only where a block that ran (count above 0) holds the changed code; package-level values count as run when any block of their package ran. Profile paths are import paths, mapped to files through each `go.mod`'s module path. Without it every line is used, with a warning. To get a profile from your scenarios, build the application with `go build -cover`, run it with `GOCOVERDIR` set, run the scenarios, and stop it with a normal exit (counters are written at `os.Exit` or when `main` returns, not on a kill).
- `--build-check` keeps a mutant only if `go build -trimpath -o /dev/null ./...` and `go vet` of its package, run in the module that holds the file, still pass on a copy of the listed files with the mutant applied, with `GOOS`/`GOARCH` from `--goos`/`--goarch` (default `linux`/`arm64`) and `CGO_ENABLED=0`. `--jobs` (default 4) copies run at once, each reused and restored after each mutant; mutants that fail are replaced by the next ones until `--limit` is reached. The unchanged copy must build, or the command exits 2. Without the flag nothing is built, with a warning; even then, a removal that would leave a local variable or an import unused, and the removal of a `panic` in a function with results (which may then lack its final return), isn't generated.
- Choosing is spread first: the first pass takes one mutant per function and operator (closures count as their own function, `main.func1`), then later passes fill up to `--limit` (default 300), round-robin over the functions. Order within a pass comes from `--seed` (default 1): the same files and flags give a byte-identical file.
- Scope: the files `git ls-files -co --exclude-standard` lists, built for `--goos`/`--goarch`, minus `*_test.go`, generated files (`// Code generated … DO NOT EDIT.`), `vendor/`, `testdata/`, folders starting with `.` or `_`, and the protected inputs. `--include` and `--exclude` take path globs (`**` matches any folders; `dev/` means `dev/**`).
- Output: `--out` (required) gets a YAML list that passes `proctor validate` when added to the mutants file, with titles such as `boundary in parseLimit (limits.go:16)`. `--index` adds a `.csv`, `.md` or `.markdown` table of id, file:line, operator, function and coverage (`yes`, or `unknown` without a profile), and `--out-json` the same mutants as JSON. Each is written with mode 0600; the configured mutants file is always refused, and a path inside the protected inputs needs `--force`. A mutant whose file, line, find and replace already appear in the configured mutants file isn't generated again, and no two generated mutants share a file, find and replace.

## Repository drivers

A driver doesn't have to ship with Proctor. Keep its source in a protected folder, build it in the job, and pin the program in `proctor.yaml`:

```yaml
drivers:
  k8s:
    program: proctor/drivers/k8s/bin/proctor-driver-k8s
    sha256:
      linux/amd64: <64 hex digits>
      darwin/arm64: <64 hex digits>
    operations: { pods: observe, events: observe }
    policy: { namespaces: [payments] }
```

- `program` takes the place of `official`; a driver has exactly one of them (exit 3, INT-12). It is a clean path from the repository root to a file named `proctor-driver-<name>`.
- `sha256` maps `<GOOS>/<GOARCH>` to the program's checksum as `shasum -a 256` prints it. `operations` lists every operation of the driver as `act` or `observe`; `by_args` isn't accepted.
- Keep the source in `proctor/drivers/<name>/` (`go.mod`, `go.sum`, `cmd/proctor-driver-<name>/`) with `bin/` in its `.gitignore`, and build it from that folder with the command the reference workflow runs:

  ```sh
  GOWORK=off GOTOOLCHAIN=go1.27.4 CGO_ENABLED=0 GOFLAGS=-mod=readonly GOOS=linux GOARCH=amd64 \
    go build -trimpath -buildvcs=false -o bin/proctor-driver-<name> ./cmd/proctor-driver-<name>
  ```

  Leave `GOAMD64` and `GOARM64` unset, set no `-ldflags -X`, and keep the driver's version a constant in its source. The build is then the same byte for byte on every machine, so a Mac computes the `linux/amd64` pin the job gets. A `replace` to a local folder records that folder's path in the program, so pin the protocol module by version instead.

Proctor refuses:

- a program whose folder isn't under the protected root or a `protected_paths` entry ending in `/`, a symbolic link on the program's path, a program that isn't a regular file, and one over 256 MiB (exit 4, INT-21); a missing program exits 2;
- a program whose checksum differs from its pin for this platform, in every mode (exit 4, INT-17); without a pin for this platform a local run goes ahead with origin `unpinned`, and an authoritative run refuses it (exit 4, INT-17);
- a program that Git tracks, in a commit for `integrity`, `validate --commit` and authoritative runs, and in the index for `validate` (with `--commit` too), `run`, `approve` and `mutate` (exit 4, INT-21). Paths are compared ignoring ASCII case, so any tracked file in the program's folder or a submodule at that folder or above counts;
- a driver whose `hello` doesn't list exactly the operations in `operations`, with the same kinds (exit 3, INT-21).

No repository driver starts while an INT-12 or INT-21 finding stands.

An authoritative run uses the base commit's driver, never the tested commit's: it takes the driver's `program`, `sha256` and `operations` from the base's `proctor.yaml`, so its program must match the base's pin. When the tested commit changes the driver's entry or a file in its source folder (the folder above the program's folder, `proctor/drivers/<name>/` in the layout above), the report lists it in `trust.repository_drivers_from_base`. A repository driver the base doesn't have isn't started: it is listed in `trust.repository_drivers_not_in_base`, and every scenario that uses it is NOT_RUN `driver_not_in_base` (exit 2), so the run can't pass. A driver change takes effect once merged, so merge it before the specifications that need it. The reference workflow builds the drivers from the base tree, outside the checkout, and passes that folder as `--driver-programs`.

Proctor runs a private copy of the program from a new folder under `TMPDIR`, which must allow running programs, and removes it afterwards. The targets, `shows`, `destructive` and state checks a driver reports aren't in `proctor.yaml`, so review its source for them. `proctor diff` lists a changed pin as `driver_program_changed` and an `act` that became `observe` as `operation_now_observes`.

The protocol is the Go module `github.com/bezmoradi/proctor/protocol`, which needs Go 1.27.4 or later. While the repository is private, set `GOPRIVATE=github.com/bezmoradi/*` with Git credentials, or run `go mod vendor` in the driver's folder: the reference workflow then builds with `-mod=vendor`, and the vendored code is protected and reviewed with the rest. Proctor speaks one protocol version, so rebuild the driver when it changes. To start, copy [`protocol/cmd/proctor-driver-example/`](protocol/cmd/proctor-driver-example/main.go), a driver with one `observe` operation. In an operation, `accumulates` marks an `observe` operation whose fact only grows, with or without a handle, so TIME-14 applies to it, and `scan` lists fact paths whose text Proctor searches for known credentials and secrets after every sample (`secret_in_telemetry`, see [Telemetry](#telemetry)); a `scan` path that doesn't parse stops the run (exit 2).

## Quick reference

### Commands and flags

- `validate`, `integrity`, `diff`, `run`, `approve`, `logs normalize`, `mutate` and `mutants generate` take `-r, --repo DIR` (default `.`) and `-c, --config FILE` (default `proctor/proctor.yaml`); all of them but `logs normalize` take `--json`. `proctor -v` prints the version; `proctor mutants` alone prints its help and exits 3.

| Flag | Default | Meaning |
|---|---|---|
| `integrity --commit REV` | `HEAD` | the commit to check |
| `diff --pr-head REV` | | `--head` is then GitHub's test merge, whose first parent is the base |
| `diff`, `run`: `--change-kind-source` | `flag` | `flag`, or `label` when the workflow read a `proctor:` label |
| `run`, `mutate`: `--ready-timeout` | `30s` | how long to wait for every driver to report its target ready |
| `run`, `mutate`: `--no-progress` | | no progress line on stderr as each scenario finishes |
| `run --report FILE` | | in local mode, also writes the `--json` document, with mode 0600 |
| `run --commit REV` | `HEAD` | with `--authoritative`: the tested commit |
| `run --base REV` | | with `--authoritative`: the base, when the head is tested directly, with exactly one merge base with the tested commit (exit 4, INT-09); give `--pr-head` or `--base`, not both and not neither (exit 3) |
| `run --driver-programs DIR` | the checkout | with `--authoritative`: the folder holding the repository drivers' programs at their `program` paths |
| `mutate --call-timeout` | `10s` | the limit for each driver call |
| `mutants generate`: `--goos`, `--goarch` | `linux`, `arm64` | lower-case letters and digits only |
| `mutate html --from FILE` | | a report of at most 512 MiB |

- `approve` without `--received` takes the folder under `$TMPDIR/proctor-received/<project>/` whose `manifest.json` changed last. End of input at its prompt prints `No answer; stopping.` and exits 0.
- `scripts/build.sh [--install] [OUTDIR]` builds into `bin/` by default; `--install` also copies every program into `$(go env GOPATH)/bin`. `proctor` holds its drivers' checksums and refuses one that doesn't match (exit 4, INT-17), so copy the programs together.

### Environment variables

| Variable | Effect |
|---|---|
| `PROCTOR_NO_UPDATE_CHECK` | any value turns off the update check (see [Privacy](#privacy)) |
| `CI`, `GITHUB_ACTIONS` | any value turns off the update check; `approve` and `mutate` refuse to run |
| `GITHUB_REPOSITORY`, `GITHUB_TOKEN` | authoritative mode asks GitHub for CODEOWNERS errors with them; without either, or with no CODEOWNERS file at the base, it exits 4 (INT-11), so it can't pass outside GitHub Actions |
| `GITHUB_API_URL` | GitHub's API, default `https://api.github.com` |
| `ACTIONS_ID_TOKEN_REQUEST_URL`, `ACTIONS_ID_TOKEN_REQUEST_TOKEN` | the job's OIDC token (`permissions: id-token: write`); without them `trust.workflow_identity` is `unverified` and `trust.workflow_changed` is `unknown` |
| `GITHUB_RUN_ID`, `GITHUB_RUN_ATTEMPT` | recorded in the report; they must match the OIDC token |
| `GITHUB_STEP_SUMMARY` | the file the job summary is appended to |

### Configuration keys

| Key | Meaning |
|---|---|
| `protected_paths` | more paths to protect besides `proctor/`, `.github/`, `.gitattributes`, `.gitmodules`, `.lfsconfig` and CODEOWNERS; an entry ending in `/` is a folder |
| `protected_owners` | authoritative mode lists each protected file whose CODEOWNERS rule at the base names none of these owners as `UNOWNED` and in `trust.unowned_protected_paths`, reported only (OD-16); without the key every protected file is listed |
| `drivers.<name>.program` | a [repository driver](#repository-drivers)'s program, a clean path from the repository root to `proctor-driver-<name>` under a protected folder; instead of `official` |
| `drivers.<name>.sha256` | `<GOOS>/<GOARCH>: <64 hex digits>` for each platform the program is pinned for, at least one |
| `drivers.<name>.operations` | every operation of a repository driver, `act` or `observe`; the driver's `hello` must match it |
| `identities.<name>.type` | `none` or `bearer`; validation refuses any other type (exit 3); the names `run`, `secret`, `row` and `fill` are reserved |
| `identities.<name>.variants`, `accepted_variants` | other credentials a step sends with `variant: <name>`, never in setup or cleanup; a `variants` one must expect 401 or 403 (AUTH-06), an `accepted_variants` one needn't |
| `secrets` | names a step may use as `${secret.<name>}`, in an http body or headers only |
| `redaction.json_paths` | paths whose values the report and transcripts show as `[redacted]` |
| `redaction.headers` | header names, in any case, whose values assertions, saves, excerpts, transcripts and approved logs show as `[redacted]`; their values are scrubbed from the whole report. A key with dots counts when one of its parts is such a name or `authorization`, `proxy-authorization`, `cookie` or `set-cookie`, so with `x-api-key` listed, `attributes['http.request.header.x-api-key']` is redacted like the header. App-log lines in the report (`unexpected`, `first_unparsed`, `first_unmapped`) are redacted like the rest of it |
| `identity_check` | `{ path, user_id, driver }`: before the first scenario, `GET <path>` with each `bearer` identity a listed scenario uses; the value at `user_id` in the body must equal the identity's `values.user_id` in the binding, or nothing runs and the run exits 3 (exit 2 when a request errors, such as a timeout); `driver` is needed only with more than one http driver (AUTH-04) |
| `report.excerpt_bytes` | the size of each excerpt in the report, default 4096 |
| `time_limits` | `min_interval` (default `100ms`, the shortest `every`), `max_window` (`10m`), `max_samples` (1000 per check), `max_call` (`60s`); `every` defaults to `500ms` and is at most half the window |
| `app_logs` | also `levels` (default `ERROR`, `FATAL`, `PANIC`), `level_field` (`level`), `message_field` (`msg`) and `flush_wait` (`300ms` with a file, `1500ms` with `otel`, at most `10s`) |
| binding `environment` | the target's name, the report's `target` |
| binding `disposable` | `true` when the environment is thrown away after the run, which `requires: disposable_environment` needs |
| binding `identities.<name>` | `token_env`, the variable holding a bearer token; `values`, used as `${<name>.<key>}`, where `id` and `user_id` must differ between identities (AUTH-03); `variants.<v>.token_env` or `.header_env` (the whole `Authorization` value) |
| binding `secrets.<name>.env` | the variable holding a declared secret; an identity, variant or secret that `proctor.yaml` doesn't declare is refused (exit 3) |
| binding `app_logs` | `file`, the absolute path of the application's log, or `otel`, the `proctor-sim-otel` endpoint it exports logs to; exactly one |
| binding `image` | authoritative mode: `built_digest` and `running_digest`, each `sha256:` and 64 lower-case hex digits, must be equal (REP-11) |
| binding `drivers.<name>.<key>_env` | names a variable whose value the driver gets as a secret; Proctor scrubs it from its output |

- An identity's or variant's variable must be set, non-empty and without a line break, and no two identities or variants may send the same credential (AUTH-03).
- References: `${run.id}`, `${<saved>}`, `${<identity>.<key>}`, `${secret.<name>}` and `${fill(N)}` (N `x` characters, at most 16 MiB, in an http body or headers only).
- Tables: the first column is the case id, every other column must be used by `identity`, `variant`, `with` or `expect`, `${row.<column>}` can't be in `do`, a key or the case id, a cell can't refer to another, and `${row.x}` inside longer text needs a single-value cell. `setup_from: <table id>` reuses an earlier table's setup, instead of `setup`.
- The manifest refuses a repeated specification id or path (exit 3, INT-02), and a path that is absolute, holds `..`, a backslash or NUL, or isn't in clean form (exit 4, INT-01).
- A protected file can't have the Git attributes `diff`, `binary`, `text`, `eol`, `crlf`, `filter`, `working-tree-encoding`, `ident`, `linguist-generated`, `export-ignore` or `export-subst` (exit 4, INT-14). `proctor/approved/` holds at most 20,000 entries.

### Matchers

| Matcher | Passes when the value at the path |
|---|---|
| `equal` | equals the expected value, with its JSON type |
| `present`, `absent` | exists, or doesn't |
| `keys` | is an object with exactly these keys |
| `length` | is a list with exactly this many elements |
| `unordered` | is a list of exactly these values, in any order |
| `contains` | is text that contains this text |
| `any`, `none` | is a list where some element, or no element, has every claimed value |
| `count` | is a list where exactly `is` elements have every `where` value: `count: { $.items: { where: { state: done }, is: 2 } }` |

### Drivers

- `http.request`: `method` (`GET`, `HEAD`, `OPTIONS`, `POST`, `PUT`, `PATCH`, `DELETE`), `path`, `query`, `headers`, a body and `trace` (true sends a fresh sampled `traceparent`); fact `status`, `headers`, `header_lists` (each value list split at commas), `body` or `body_text`, `body_truncated`, and `body_invalid`, why a body that looks or claims to be JSON wasn't read as JSON, and with `trace: true` `trace_id`. Binding `base_url`, `user_agent` (required) and `ready_path`.
- http limits: a response body over 10 MiB is ERROR `response_too_large`; redirects aren't followed, proxies are ignored and compression is off; a `HEAD` step asserts only status and headers; specifications can't set `Host`, `User-Agent`, `Authorization`, `Proxy-Authorization`, `Cookie`, `Content-Length`, `Transfer-Encoding` or `Connection`; a path with `?`, `#`, `\`, a space, a control character, `%2F`, `%5C` or a `.` or `..` segment is refused; a header name with capital letters in an expectation is refused, since facts hold them in lower case (HTTP-08); `set-cookie` is redacted.
- `ws.connect` (with `as`): `path`, `query`, `headers`, `subprotocols`, `auth` (where the credential goes: `subprotocol`, the default, `query:<name>` or `header:<name>`) and `no_origin`; fact `connected`, `status`, `subprotocol`, `headers`, `body_text`, `body_truncated`. `ws.send` (with `on`) takes exactly one of `json` and `text`; fact `sent`. `ws.messages` (with `on`): `count`, `messages` (`type` `text` or `binary`, `text`, `json`), `closed`, `close_code`, `close_reason` and `overflow`, set after 10,000 frames or 16 MiB. Binding `base_url` (`ws`, `wss`, `http` or `https`), `origin` (sent as `Origin` unless `no_origin`) and `ready_path`; no policy.
- `postgres.query`: `connection`, `query`, `args`; fact `row_count`, `rows`. Policy `connections: { <name>: { schemas, objects } }`, with `objects` required and names lower-case and unquoted; binding `<name>_dsn_env`. Sessions run with `application_name=proctor` and `default_transaction_read_only=on`; connect and statement timeouts are 5 s; `NaN` and `Infinity` read as text; the role check also refuses `MAINTAIN` on PostgreSQL 17 and later.
- `dynamodb.get` (`table`, `key`, `consistent`, default true; fact `found`, `item`), `dynamodb.query` (`table`, `index`, `key` with the partition key and at most the sort key, matched by equality, `limit` 1 to 1000; fact `count`, `items`; over 1,000 matches without `limit` is ERROR) and `dynamodb.put` (`table`, `item`; fact `written`). `table` is an alias from the policy's `tables: { <alias>: <table name> }`.
- dynamodb and sqs binding: `endpoint`, `region`, `ca_bundle` (a PEM file of at most 1 MiB, for https only), `access_key_env` and `secret_key_env` (static credentials only).
- `sqs.send` (`queue`, `body`, `attributes`, `delay_seconds` 0 to 900, and for a FIFO queue `group_id`, required, and `dedup_id`; fact `message_id`), `sqs.peek` (`queue`, `max` 1 to 1000, default 1000; fact `count`, `truncated`, `messages` with `message_id`, `body_text`, `body`, `attributes`) and `sqs.depth` (`queue`; fact `visible`, `in_flight`, `delayed`). Policy `queues: { <alias>: <queue name> }` and `peek_hold`. A peek stops after 150 receives, or when 3 receives in a row add nothing and it holds as many messages as the queue reports (20 in a row when it holds fewer); the step's summary says why it's `truncated`.
- `ses.sent` (`to`, matched against to, cc and bcc, and `from`, both ignoring case; fact `count`, `emails` with `message_id`, `from`, `to`, `cc`, `bcc`, `reply_to`, `subject`, `text`, `html`, `sent_at`) reads `GET <endpoint>/__proctor/sent`; a 404 isn't retried and an answer over 15 MiB is ERROR. Binding `endpoint`, `ca_bundle`; no policy. `proctor-sim-ses [--addr 127.0.0.1:18193] [--identity SENDER]` answers SESv2 `POST /v2/email/outbound-emails` and `/__proctor/sent`, and keeps the newest 10,000 emails, each with at most 50 recipients and a 40 MiB raw message.
- `stub.received` and `stub.fail` (with `as`; see [HTTP fakes](#http-fakes)) read `GET <endpoint>/__proctor/received` and post to `/__proctor/faults`; a 404 isn't retried. Binding `endpoint`, `ca_bundle`; no policy.
- `proctor-sim-http --routes FILE [--addr 127.0.0.1:18194] [--cert FILE --key FILE | --tls-name NAMES --ca-out FILE]` serves the route file and `/__proctor/routes`, `received`, `faults` and `reset`, logs one line per request without headers or bodies, exits 2 on any problem with the route file or flags, and stops within 5 s on SIGINT or SIGTERM.
- `otel.spans` (`trace_id`, `service`, `name`, `kind` `unspecified|internal|server|client|producer|consumer`, `status` `unset|ok|error`, `attributes` matched equal with their JSON type, `attribute_contains` text in a text attribute; fact `count`, `spans`, `traces` with `trace_id`, `span_count`, `services`, `root_count` and `error_count` over every received span of the trace, `services`, `attribute_keys`, `overflow`, `overflow_reason`) and `otel.logs` (`trace_id`, `service`, `severity_at_least` `TRACE` to `FATAL`, `body_contains`, `attributes`, `attribute_contains`; fact `count`, `logs`, `services`, `attribute_keys`, `overflow`, `overflow_reason`) read `POST <endpoint>/__proctor/spans` and `/__proctor/logs`. A span has `seq`, `received_at`, `service` (`unknown_service` without `service.name`), `scope`, `resource`, `attributes`, `trace_id`, `span_id`, `parent_span_id` (lower-case hex, `""` for none), `name`, `kind`, `status.code` and `status.message`, `start_time`, `end_time`, `duration_ms`, `events`, `links` and `dropped`; a log record `time`, `observed_time`, `severity_number`, `severity` (from the number, else the text in upper case), `severity_text`, `body` (text, or the JSON value), `trace_id`, `span_id` and `event_name`. Attributes are flattened (`parent.child` for nested maps, integers past ±2^53 as decimal text, bytes as base64); write a dotted key in brackets, `"$.spans[0].attributes['http.route']"`. `attribute_keys` lists the keys over the matched records, resource, event and link attributes included. Ready when `GET /__proctor/stats` answers as `proctor-sim-otel`; a 404, or an answer from something else such as a collector, isn't retried, and an answer over 15 MiB is ERROR. Binding `endpoint`; no policy.
- `proctor-sim-otel [--http 127.0.0.1:4318]` answers OTLP/HTTP `POST /v1/traces`, `/v1/logs` and `/v1/metrics` (metrics are counted and discarded) in `application/x-protobuf` or `application/json`, gzip or not. Any other media type, OTLP/gRPC included, or another encoding is 415, data it can't read 400 with a `google.rpc.Status` (in OTLP/JSON, ids must be hex, not base64), and a request over 64 MiB after decompression 413. It keeps the oldest 100,000 spans, 100,000 log records and 256 MiB of records, and refuses newer ones with a partial success (`rejectedSpans` or `rejectedLogRecords`), which clients don't retry. `GET /__proctor/stats` shows the counts, the refusals since it started and each service's last export.
- `docker`: ready when `/_ping` answers `OK` and Docker accepts API 1.44; `labels` takes at most 20 keys, `has_labels` 1 to 20, and both together 39; the policy's `scope` takes at most 16 entries; more than 200 objects in scope is ERROR.

### Limits and timeouts

- Readiness asks each driver in turn every 250 ms, each attempt bounded by 3 s (5 s for docker and postgres), until `--ready-timeout`; a lasting problem ends the wait at once. A target that isn't ready makes every scenario NOT_RUN `environment_not_ready` (exit 2), and an `app_logs.file` that can't be read, or an `app_logs.otel` receiver that doesn't answer, when the run starts exits 2.
- The OIDC token must have audience `proctor` and be signed with RS256 by a key of at least 2048 bits, with 60 s of clock skew allowed; a token that doesn't verify, or names another commit, repository, run or attempt, exits 4 (MODE-03).
- `proctor-sim-http` keeps the newest 10,000 requests and 128 MiB of them (bodies, headers and queries), the first 64 KiB of each body (it reads up to 16 MiB and counts the rest in `body_bytes`), and answers at most 100 requests (12 MiB) per read. A route's and a fault's `delay` are at most 10m, a fault's `for` at most 1h and its `times` at most 10,000; a `file` answer is at most 8 MiB.

### Reports

- `run --json` and local `--report`: `result`, `exit_code`, `mode`, `trust.reason`, `provenance` (`proctor_version`, `config`, `sha256`), `run_id`, `target`, `readiness`, `violations`, `drivers[]` (`name`, `official`, `program` and `platform` for a repository driver, `version`, `origin`: `official`, `repository` or `unpinned`, `sha256`, `proofs`, `stderr`, `stderr_dropped_bytes`), `identity_check`, `summary`, `scenarios`, `tables`, `received`, and `aborted` after an interrupt. Every local report, one that stops early included, has `trust` and `summary`.
- `summary` adds to the counts `plain_scenarios` and `table_cases` (each `total`, `passed`, `failed`, `errors`, `not_run`), `steps`, `failed_ids` and `errored_ids` (a table case as `<table>/<case>`), `integrity_violations`, and in authoritative mode `protected_changes`.
- A run stopped by an invalid configuration or specification (exit 3) reports every scenario and table case as NOT_RUN `configuration_error`. A scenario or case left without a result is NOT_RUN `not_started` and an integrity violation (exit 4, INT-03).
- A table without an `id` is shown as a grid named `tables[<n>]`, which is also its `tables[].id` and its cases' `table`.
- A step has `phase`, `result`, `reason`, `cause`, `action.summary`, `evidence` (`request_excerpt`, `response_excerpt`, `driver_duration_ms`), `duration_ms`, `samples`, `samples_skipped` (due times a slow sample overran, omitted when 0), `notice` (TIME-05), `saved` and `assertions[]` (with `cut_at` and `overflow`); a scenario has `handles[]`, a table `setup_from` and `rows[].cells`. A step whose telemetry holds a known credential or secret FAILs with reason `secret_in_telemetry` and one `secret_scan` assertion per place (at most 20), whose `target` is the path and `actual` `[redacted]`.
- An ERROR the interrupt (Ctrl-C or SIGTERM) caused has cause `interrupted`; its reason is `aborted`, or `setup_failed` for a setup step.
- The authoritative report adds `trust` (`workflow_identity`, `workflow_changed`: `true`, `false`, `unknown` or `not_applicable`, `codeowners_errors`, `unowned_protected_paths`, `approval_required`, `repository_drivers_from_base`, `repository_drivers_not_in_base`), `provenance` (`repository`, `tested_commit`, `parents`, `pr_head`, `base`, `image`, `environment`, `ci_run_id`, `ci_run_attempt`, `started_at`, `ended_at`), `protected_changes`, `change_kind`, `earlier_attempts` and `errors`. Its `provenance.sha256` also holds a `protected_paths:<path>` key for each file only `protected_paths` protects (INT-16).
- `validate --json` and `integrity --json`: `result`, `exit_code`, `mode` and `report`; `validate` adds `drivers`, `errors`, and outside Git `git: false` with `note`. `diff --json`: `result`, `exit_code`, `protected_changes`, `weakenings`, `change_kind` and `diff` (`files`, `scenarios`, `weakenings`, `driver_changes`, `expectation_changes`, `assertion_moves`).
- `mutate --report`: `selected`, `trust`, `provenance`, `baseline`, `mutants[]` (`verdict`, `killed_by`, `failed_scenarios`, `run_result`) and `counts`.

### proctor diff kinds

| Kind | Weakening |
|---|---|
| `scenario_removed`, `table_row_removed`, `step_removed`, `setup_step_removed` | removed, with the number of `assertions_lost` (a setup step that became cleanup counts as removed) |
| `step_moved_to_setup` | a verification step became setup |
| `driver_changed`, `driver_origin_changed` | a step's `do` names another driver; a driver's `official` or `program` changed, or it switched between them |
| `driver_program_changed` | a repository driver's `sha256` pin was added or changed, so its steps run another program |
| `operation_now_observes` | a repository driver's operation went from `act` to `observe` |
| `variant_accepted`, `variant_removed` | a variant added to `accepted_variants`; a step no longer uses its variant |
| `state_check_waived`, `state_check_waiver_changed` | `no_state_check` added or changed |
| `time_check_changed`, `wait_longer`, `window_shorter`, `sampling_sparser` | a time check was added, removed or changed kind, waits longer, watches for less time or samples less often |
| `max_window_longer`, `max_samples_higher`, `min_interval_shorter` | a `time_limits` value loosened |
| `redaction_removed` | a `redaction.json_paths` or `redaction.headers` entry removed |
| `identity_check_removed`, `identity_check_changed` | the identity check was removed, or its driver, path or `user_id` changed; `identity_check_added` is listed with the other changes |
| `app_logs_removed`, `app_log_level_removed`, `app_log_field_changed`, `app_log_unparsed_allowed`, `app_log_flush_shorter` | the app-log check loosened; `app_log_flush_shorter` when `flush_wait` became shorter for a file or an `otel` source, an unset value counting as that source's default |
| `log_error_allowed`, `log_count_removed`, `log_count_raised` | `expect_logs` allows more |
| `not_analysed` | one side doesn't parse, so read the file diff |

An assertion that moved to another step sending the same request is listed under `Moved assertions (not lost)` as `assertion_moved` (`assertion_moves` with `--json`), not as a weakening. A scenario whose place among the others changed is listed as `reordered`, not as a weakening. A removed pin (`driver_pin_removed`) and an added, removed or `observe`-to-`act` operation (`operations_changed`) are listed with the driver changes, not as weakenings.

## Repository layout

| Path | Contents |
|---|---|
| `cmd/proctor`, `internal/` | The CLI |
| `drivers/<name>/` | The official drivers, one Go module each (`docker`, `dynamodb`, `http`, `otel`, `postgres`, `ses`, `sqs`, `stub`, `ws`) |
| `protocol/` | The protocol between `proctor` and its drivers, Go module `github.com/bezmoradi/proctor/protocol`; `protocol/cmd/proctor-driver-example/` is a driver to copy for a [repository driver](#repository-drivers) |
| `simulators/ses/` | `proctor-sim-ses`, a local SES stand-in for the ses driver |
| `simulators/http/` | `proctor-sim-http`, a general HTTP fake for the stub driver |
| `simulators/otel/` | `proctor-sim-otel`, a local OTLP/HTTP receiver for the otel driver |
| `scripts/build.sh` | Builds `proctor`, its drivers and the simulators |
| `scripts/release.sh`, `.github/workflows/release.yml` | Builds the release archives; drafts a release from a `v*` tag on main |
| `.github/workflows/ci.yml`, `pull-requests.yml`, `workflow-guard.yml` | CI and pull request checks |
| `.github/rulesets/` | The branch and tag rulesets the owner applies on GitHub |
| `.go-arch-lint.yml`, `scripts/check-arch.sh` | Which packages may import which; `internal/model`, `internal/expr`, `internal/transcript` and `internal/strictyaml` stay free of file, process and network access |
| `examples/github/` | The reference GitHub workflow and CODEOWNERS file |
| `examples/github/verify/` | An example disposable environment for the workflow, copied to `proctor/verify/` |

## Development

```sh
go build ./... && go vet ./... && test -z "$(gofmt -l .)"
sh scripts/check-arch.sh
scripts/build.sh /tmp/proctor-bin && /tmp/proctor-bin/proctor version --features
```

CI (`.github/workflows/ci.yml`) runs these and the rest of the checks in [CLAUDE.md](CLAUDE.md#verify) on every pull request. A pull request needs a linked issue (`Closes #<n>`) to merge. This repository doesn't include Proctor's test suite.

## Privacy

Proctor runs on your own machine or CI runner. Its author runs no server for it and receives none of your data, and it sends no telemetry.

- **What it reads:** your repository's working tree or Git objects, the binding, the environment variables the binding names, the application's log file, or the records at its `app_logs.otel` receiver, when `app_logs` is set, and the answers of the targets your specifications check.
- **Where it writes:** reports to stdout and the `--report` file (mode 0600); received transcripts and logs to a new 0700 folder under `$TMPDIR/proctor-received/<project>/`, or `--received`; approved files under `proctor/approved/` only when you answer `y` to `proctor approve`; mutant copies under `$TMPDIR`; and the files you name for `mutate html` and `mutants generate`. A run scrubs the credentials, secrets and redacted values it knows from what it writes.
- **What leaves the machine:** `validate`, `integrity`, `diff` and a local `run` ask `https://api.github.com/repos/bezmoradi/proctor/releases/latest` for the latest release, sending only `proctor/<version>` as the User-Agent; set `PROCTOR_NO_UPDATE_CHECK=1` to turn it off (it is off when `CI` or `GITHUB_ACTIONS` is set, and in authoritative mode). In authoritative mode it verifies the job's OIDC token against GitHub's published keys and asks GitHub's API for CODEOWNERS errors with `GITHUB_TOKEN`. Drivers, and the app-log check with `app_logs.otel`, reach only the targets in the binding. The simulators keep what they receive in memory and send nothing on. `mutate` runs your `up` and `down` scripts, and `mutants generate --build-check` runs your `go` toolchain, which may download modules as your Go settings allow.
- **Stopping it:** delete the reports, the `proctor-received` folders and any `mutate` leftovers its report lists; Proctor keeps no other state.

## Author

This project is maintained by [Bez Moradi](https://github.com/bezmoradi).

## License

Proctor is licensed under [MIT](LICENSE).
