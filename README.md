# Proctor

Proctor checks that an application still does what people agreed it should do, and refuses to report a pass when the agreement itself was quietly changed. It exists for codebases where AI coding agents change the code, the tests and the test configuration in the same pull request.

The expected behavior lives in YAML specifications under a protected folder. People approve changes to those files through code-owner review. A trusted CI job runs every listed scenario against the application built from the tested commit, and records what it checked. Proctor itself doesn't approve anything; GitHub's branch protection does, using Proctor's check and report as evidence.

**Status: first draft, pre-release.** Built so far:

- static integrity checks, and the semantic diff of protected changes, with weakenings listed first and an optional [change kind](#change-kind);
- local mode and authoritative mode; `proctor validate` also reads a plain folder outside Git;
- official drivers for HTTP (with declared POST reads and deletes, streamed NDJSON and server-sent events, and binary, large and file bodies), WebSocket, PostgreSQL (read-only), DynamoDB, SQS, SES and Docker (read-only: containers, networks, volumes);
- exact JSON assertions and the text matchers `matches`, `excludes` and `differs`, with every expectation path checked against the operation's fact schema;
- time-based checks (`eventually`, `consistently`, `never`) and per-step timeouts;
- tables, including tables that must cover every combination of declared dimensions;
- the application-log check (JSON or logfmt), approved transcripts and approved logs;
- mutation testing: declared mutants, generated candidates and an HTML report;
- `proctor version --features`, so a wrapper can check what a build supports.

Temporal evidence and log-event evidence (`prd.md` §11.8 and §11.6) are specified but parked, not built; the application-log check above is separate from them. [`prd.md`](prd.md) is the product requirements document. It includes the decisions, the acceptance tests and the roadmap, with what each phase still lacks. Proctor has no license yet, so it isn't open source until one is chosen.

## Quick start

Needs Go 1.27.1 or later, Bash and Git. In Proctor's checkout, build Proctor and its drivers into `bin/` (a driver is found beside the `proctor` program, so `go build ./cmd/proctor` alone isn't enough):

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
| `proctor diff --base REV --head REV [--change-kind KIND]` | Every changed protected file and scenario, with weakenings (removed assertions, waivers, steps moved into setup) listed first; with a kind, also whether the changes match it (see [Change kind](#change-kind)) | 0, 3 an unknown kind or a folder outside Git, 4 a Git error or a kind mismatch |
| `proctor run --binding FILE` | Local mode: runs the working tree's specifications against a running application | 0 to 4 |
| `proctor run --authoritative ...` | For the trusted CI job: see below | 0 to 4 |
| `proctor approve [--received DIR] [--all \| IDS] [--yes]` | Local mode only: shows each received transcript's or log's diff from the approved one and, when you answer `y`, writes it under `proctor/approved/`. Refused in CI, without a terminal unless `--yes`, and when the specifications changed since the run. See [Approved transcripts](#approved-transcripts) and [Approved logs](#approved-logs) | 0, 2, 3 |
| `proctor logs normalize [--field K=V]… FILE` | Prints a log file's lines as an approved log shows them, to stdout only: only the fields `app_logs.approved` lists, scrubbed. Reads only `proctor.yaml` and the file, writes nothing, contacts nothing. See [Approved logs](#approved-logs) | 0, 2, 3 |
| `proctor mutate [--only IDS] [--scenarios IDS] [--report FILE]` | Local mode only: builds and starts each mutant declared in the protected mutants file from a copy of the working tree, runs the specifications against it, and reports each as killed, survived, `killed_elsewhere`, error or `not_run`. Refused when `CI` or `GITHUB_ACTIONS` is set. See [Mutation testing](#mutation-testing) | 0 all killed, 1 a survivor, 2 errors, a failed baseline or a stopped run, 3 invalid |
| `proctor mutate html --from REPORT --out DIR [--coverage PROFILE] [--force]` | Draws the JSON report of `proctor mutate --report` as HTML pages in a new or empty folder outside the repository: the counts and score, the survivors, and each mutated file's source with its mutants. See [The HTML report](#the-html-report) | 0, 2 the source changed since the run, 3 invalid |
| `proctor mutants generate --out FILE [--coverage PROFILE] [--build-check]` | Reads the project's Go code and writes candidate mutants (ids `GEN-0001` and up) to a separate file for a person to review and merge into the mutants file; never writes the mutants file itself. See [Generating mutants](#generating-mutants) | 0, 2, 3 |
| `proctor version [--features \| --json]` | The build's version (a build with uncommitted changes ends in `-dirty-` and a hash of them) and the features it has, so a wrapper can check for one by name | 0 |

Exit codes: 0 passed, 1 a scenario failed, 2 an infrastructure error, 3 invalid input, 4 integrity violation. When several apply, 4 wins over 3, 3 over 1, and 1 over 2.

There is no warning level. Every validation finding fails the command. A step can waive a rule only with a written reason (`no_state_check: <reason>`, or `waive: { FIX-05: <reason> }`), and `proctor diff` lists each added waiver as a weakening.

## Authoritative mode

```sh
proctor run --authoritative --config proctor/proctor.yaml \
  --binding "$RUNNER_TEMP/binding.yaml" --pr-head "$PR_HEAD_SHA" --report "$RUNNER_TEMP/report.json"
```

In this mode Proctor:

- reads every protected input from the tested commit, not the working tree;
- takes the base from GitHub's test merge commit, after checking that its second parent is the pull request head;
- lists every protected change as "approval required";
- asks GitHub's API for CODEOWNERS errors at the base;
- verifies the job's GitHub Actions OIDC token against GitHub's published keys;
- reports whether the workflow that ran differs from the base branch's;
- refuses to run unless the image the application was started from is the image built from the tested commit.

It always writes the JSON report and a job summary. A binding inside the checkout is refused. Nothing runs while any of these checks fails.

GitHub must be configured as `prd.md` §6.3 describes:

- CODEOWNERS on the protected folder, all of `.github/`, and the CODEOWNERS file itself;
- required code-owner review, with stale approvals dismissed and no bypass for administrators;
- the workflow's aggregator job as the required status check, pinned to the GitHub Actions app;
- branches up to date before merging.

The reference workflow (§16.2) builds the image in one job and verifies it in another, with a verified, pinned Proctor binary. A third job, `verdict`, is the required check: it fails if any prerequisite failed or was skipped.

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

`consistently` and `never` take `for` instead of `within`. A step that acts can't be sampled (TIME-11), and the window and interval are bounded by `time_limits` in `proctor.yaml`.

Validation requires:

- a later state check after every request that changes state (EXE-07);
- `${run.id}` in every value a scenario writes and later matches (FIX-05);
- every setup operation to also appear as a verification step (FIX-08);
- a destructive request (PUT, PATCH, DELETE, or a POST declared destructive) on a target that refers to neither `${run.id}` nor a value saved earlier in the scenario to follow an earlier read of the same target, or an earlier destructive request on it that EXE-09 accepted without a waiver, or to waive EXE-09 with a reason. A waived request guards nothing, so each later request on the same target needs its own waiver, which `proctor diff` lists as `rule_waived`. A path built from an identity value, such as `/things/${alice.id}`, is a fixed target, and a read counts only when it reads that target as written, a path above or below it, or a path its policy `shows`, expecting an outcome that reads state (EXE-09);
- every path in an expectation, a claim's keys and every `save` path to be one the operation's fact can hold, as its driver's `fact_schema` describes it, and each matcher to name the right kind of value: a list for `length`, `any`, `none` and `count`, an object for `keys`, text for `contains`, `matches` and `excludes` (EXE-11). Inside a free-form part, such as an http `body`, any path is accepted, so a misspelt `absent` there still can't be caught.

At run time, a `none` claim or a `count` with `is: 0` fails when the list has elements but none of them has one of the claim's keys, since such a claim could never fail (EXE-11). That holds when no element has the key's parent, too: `none: { "$.messages": { json.type: error } }` fails when every frame is plain text, because "no JSON frame of type error" would then also pass when the server starts sending its errors as plain text. The message names the missing part; add a claim that some element has it (`any`, or `count` above 0), or assert what the elements are instead.

Besides `equal`, `present`, `absent`, `keys`, `length`, `unordered`, `any`, `none`, `contains` and `count`, three matchers check text and change:

```yaml
expect:
  matches: { "$.headers.x-request-id[0]": "[0-9a-f]{16}" }
  excludes: { "$.body_text": [/workspace, "https://", "@"] }
  differs: { "$.headers.x-request-id[0]": "${first_id}" }
```

- `matches` takes an RE2 pattern that must match the whole text, as if written `^(?:…)$`. Validation refuses (exit 3, EXE-12) an invalid pattern, one longer than 1 KiB, one that matches empty text (`.*`, `[0-9]*`), and one holding `${...}`, which it doesn't expand. A value that isn't text fails.
- `excludes` takes one text or a list; the text at the path must contain none of them. A missing path or a value that isn't text fails; an empty text, an empty list and a repeated text are refused (EXE-12).
- `differs` takes any value, usually a saved one; the value at the path must be present and not equal to it, compared as `equal` compares (`7` differs from `"7"`).

`proctor diff` lists removing any of them as `assertion_removed` and loosening one as `assertion_weakened`: any change to a `matches` pattern (Proctor can't tell whether the new one accepts more), an `excludes` text that no remaining text rules out (`/workspace` to `/workspace/x`, or a text dropped from the list), and any change to a `differs` value.

The http driver keeps the first 64 KiB of a body that isn't JSON as `$.body_text` and says whether it cut it in `$.body_truncated` (the ws driver does the same for a refused handshake's body, cut at 1 KiB). `equal`, `contains`, `matches`, `excludes` and `differs` on a cut `$.body_text` fail, naming the cut and its size (`cut_at` in the report), and so does `save` from it; under `never` they fail the step instead of counting as not holding. `present` and `absent` still work (HTTP-13).

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

Every driver reports what a step reads or changes, and on one driver a read counts after an act only when their targets relate: http and ws compare paths as above (`ws.send` and `ws.messages` take their connection's path, `http.frames` its stream's), dynamodb compares table aliases, sqs queue aliases, postgres connection names, ses always reads `sent`, and docker reads `container/<name>`, `network/<name>`, `volume/<name>` or `containers`, `networks`, `volumes`. A read on another driver counts only when `proctor.yaml` declares that the act shows there (INT-20):

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

A second driver of the same official program that reaches the same application takes the first one's policy with `policy_from: http` instead of its own `policy` (INT-18), so the two lists can't drift apart, and a read on either can be the state check of a write on the other.

With `app_logs` in `proctor.yaml` and `app_logs: { file: <absolute path> }` in the binding, any line at a failing level (ERROR, FATAL or PANIC by default) that the application writes to that file during a scenario fails it, unless the scenario lists it under `expect_logs` (EXE-10). Lines are JSON objects by default; `format: logfmt` reads `key=value` lines as Go's `slog.TextHandler` writes them, and `level_numbers: { 30: INFO, 50: ERROR, 60: FATAL }` maps numeric levels such as pino's; map every number the application writes, since an unmapped one is ERROR (`app_logs_unmapped_level`). A log line that doesn't read under the declared format is ERROR (`app_logs_unparsed`), never a pass, except a panic's stack trace; `unparsed: allow` skips such lines, and `proctor diff` reports adding it as a weakening.

A database check runs one parameterized, read-only query. Before using the verification role, Proctor checks that the role can only read the objects `proctor.yaml` declares.

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
- The first 10,000 frames, 4 MiB of them and 1 MiB per frame are kept. Past a limit, or when the stream ends inside a frame, `overflow` is true and every claim on `$.frames` or `$.count` but `any` and `present` fails, naming the reason, since a frame left out could hold what `none` rules out. The same holds for `ws.messages`.
- `eventually` on `http.frames` or `ws.messages` with only `none`, `absent`, `excludes`, a zero `count` or `length`, `equal` to false, 0 or an empty value, or `differs` from anything else is refused (TIME-14): it would pass at the first sample, before anything arrived. Use `consistently` or `never`, or add a claim that waits, such as `equal: { $.closed: true }`.
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

A fact holds a fixed projection of each object's settings (user, read-only root, capabilities, security options, limits, restart policy, networks with their addresses, mounts, labels as `{ key, value }` lists), and never its environment, command, entrypoint, healthcheck command, log options or host paths. A bind shows only as `source: docker_socket` or `host_path`; it reads `docker_socket` (and the container `docker_socket: true`) when its destination is `/var/run/docker.sock` or `/run/docker.sock`, its source is a Docker socket (Docker Desktop's `/run/host-services/docker.proxy.sock` included) or a folder that holds one, and the rule errs towards `docker_socket` (prd.md DKR-07). Each step's `request_excerpt` lists every request it sent, inspects included. The report states `proof read_only: unproven`: Docker has no read-only socket, so the driver limits itself to GET requests on a fixed list of paths, and a run needs the same trust as `docker ps`. `proctor diff` lists a scope entry added or removed as a `docker_scope_widened` or `docker_scope_narrowed` weakening, and an added driver as `driver_added`.

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

A held peek hides each message it receives from every other consumer, your application's included, until about 2 s before the step's deadline and for at most 30 s, then sets every one visible again. If that fails, the step is ERROR and says how many messages stay hidden and for how long. `peek_hold` is off by default, takes only aliases from `queues`, and refuses FIFO queues, where one held message stops the rest of its group (exit 3, prd.md SQS-03). Every receive, held or not, raises a message's `ApproximateReceiveCount`, which counts towards a redrive policy's `maxReceiveCount`: a held peek raises it by 1, an unheld one once per receive, so repeated peeks of a queue with a redrive policy can move its messages to the dead-letter queue. `proctor diff` lists turning `peek_hold` on, or adding a queue to it, as a `driver_policy_changed` weakening.

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

An approved log is the part of the application's log a scenario produced, cut to the fields you choose and scrubbed, that a person read and approved. Later runs compare their log with it byte for byte. It needs the app-log check (`app_logs` in `proctor.yaml`, and `app_logs.file` in the binding), and it allows nothing: an ERROR line still fails the scenario, whatever the approved log holds.

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

`proctor diff` lists a removed mutant (`mutant_removed`), a changed edit or `killed_by` (`mutant_changed`) and a changed or removed `mutation` key (`mutation_changed`, `mutation_removed`) as weakenings; an added mutant isn't one. prd.md MUT-01 to MUT-05.

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
- The report must come from a Proctor with this command (feature `mutate.html`): older reports have no `provenance.repository` and no `edits` per mutant, and are refused (exit 3). prd.md MUT-07.

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
- `--build-check` keeps a mutant only if `go build -trimpath -o /dev/null ./...` and `go vet` of its package, run in the module that holds the file, still pass on a copy of the listed files with the mutant applied, with `GOOS`/`GOARCH` from `--goos`/`--goarch` (default `linux`/`arm64`) and `CGO_ENABLED=0`. `--jobs` (default 4) copies run at once, each reused and restored after each mutant; mutants that fail are replaced by the next ones until `--limit` is reached. The unchanged copy must build. Without the flag nothing is built, with a warning; even then, a removal that would leave a local variable or an import unused, and the removal of a `panic` in a function with results (which may then lack its final return), isn't generated.
- Choosing is spread first: the first pass takes one mutant per function and operator (closures count as their own function, `main.func1`), then later passes fill up to `--limit` (default 300), round-robin over the functions. Order within a pass comes from `--seed` (default 1): the same files and flags give a byte-identical file.
- Scope: the files `git ls-files -co --exclude-standard` lists, built for `--goos`/`--goarch`, minus `*_test.go`, generated files (`// Code generated … DO NOT EDIT.`), `vendor/`, `testdata/`, folders starting with `.` or `_`, and the protected inputs. `--include` and `--exclude` take path globs (`**` matches any folders; `dev/` means `dev/**`).
- Output: `--out` (required) gets a YAML list that passes `proctor validate` when added to the mutants file, with titles such as `boundary in parseLimit (limits.go:16)`. `--index` adds a `.csv` or `.md` table of id, file:line, operator, function and coverage (`yes`, or `unknown` without a profile), and `--out-json` the same mutants as JSON. Each is written with mode 0600; the configured mutants file is always refused, and a path inside the protected inputs needs `--force`. A mutant whose file, line, find and replace already appear in the configured mutants file isn't generated again, and no two generated mutants share a file, find and replace.

## Repository layout

| Path | Contents |
|---|---|
| `cmd/proctor`, `internal/` | The CLI |
| `drivers/<name>/` | The official drivers, one Go module each (`docker`, `dynamodb`, `http`, `postgres`, `ses`, `sqs`, `ws`) |
| `examples/github/` | The reference GitHub workflow and CODEOWNERS file |
| `prd.md` | Requirements, decisions, acceptance tests and roadmap |

## Development

```sh
go vet ./...
gofmt -l cmd internal
go build ./...
```

This repository doesn't include Proctor's test suite.
