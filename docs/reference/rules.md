# Rules

Every message from `validate`, `run`, `integrity`, `diff` and the drivers names the rule it applies, such as `EXE-14` or `INT-22`. This page lists each rule with what it refuses or reports and how it ends the command. A rule doesn't have an exit code of its own when it fails a step: then the step is FAIL or ERROR, and the run's exit code follows from the results (see [Exit codes](reports.md#exit-codes)).

In the Exit column, 3 means invalid input and 4 an integrity violation; `validate` reports both before anything runs, and `run` refuses to start. "Step" means the rule fails or errors one step at run time. "Reported" means Proctor records it and the result doesn't change.

## Integrity (INT)

| Rule | What it refuses or reports | Exit |
|---|---|---|
| INT-01 | A manifest or specification path that is absolute, holds `..`, a backslash or NUL, or isn't in clean form | 4 |
| INT-02 | A repeated specification id or path in the manifest, and a repeated or invalid scenario or table id | 3 |
| INT-03 | A scenario or table case left without a result, reported NOT_RUN `not_started` | 4 |
| INT-04 | A manifest that lists no specifications, or a specification with no scenarios | 4 |
| INT-05 | A `.yaml` or `.yml` file under `proctor/` that the manifest doesn't list, outside the files and folders that are exempt (see [Configuration](configuration.md)) | 4 |
| INT-07 | A protected file that can't be read | 4 |
| INT-08 | A Git error while `proctor diff` reads the commits it compares | 4 |
| INT-09 | A base and tested commit without exactly one merge base, or a test merge whose parents aren't the base and the pull request head | 4 |
| INT-11 | More than one CODEOWNERS file; in authoritative mode, a CODEOWNERS error GitHub reports at the base, or no `GITHUB_REPOSITORY` and `GITHUB_TOKEN` to ask with | 4 |
| INT-12 | A driver with both or neither of `official` and `program`, a step or invariant whose `do` names a driver or whose `identity` names an identity `proctor.yaml` doesn't configure, and a `program` that isn't a clean path inside the repository | 3 |
| INT-13 | A symbolic link, or anything but a folder, where protected inputs live (4); a `body_file` that is absolute, holds `..` or `${...}`, ends in `.yaml` or `.yml`, or passes through a symbolic link (3) | 4 or 3 |
| INT-14 | A protected file with a Git attribute that can hide its diff, such as `diff`, `binary`, `filter` or `export-ignore` | 4 |
| INT-15 | A specification that doesn't parse as strict YAML into the specification format, a specification or manifest whose `version` isn't 2, and a table case whose `with` or `expect` doesn't parse | 3 |
| INT-16 | Reported: each file only `protected_paths` protects has a `protected_paths:<path>` key in the authoritative report's `provenance.sha256` | Reported |
| INT-17 | A driver program whose checksum differs from its pin or from the official checksums built into `proctor`, and in authoritative mode a repository driver without a pin for the job's platform | 4 |
| INT-18 | A `policy_from` that names the driver itself or a driver `proctor.yaml` doesn't have | 3 |
| INT-19 | A file under `proctor/approved/` that no opted-in scenario owns, or an approved folder that can't be listed | 3 |
| INT-20 | A `relations` entry that names a target no step reports, or a `shows` entry no act and later read use | 3 |
| INT-21 | A repository driver's program outside a protected folder, behind a symbolic link, not a regular file, over 256 MiB, or tracked by Git (4); a driver whose `hello` doesn't list exactly `operations`, or a repository driver that answers `no_state_change` (3) | 4 or 3 |
| INT-22 | A `requires.proctor` version this build is below, a `requires.features` entry it doesn't list, or a used feature `requires.features` leaves out | 3 |

See [Installation](../guides/install.md#requirements) for `requires` and [Repository drivers](../guides/repository-drivers.md) for INT-12, INT-17 and INT-21.

## Mode, change kind and reporting (MODE, CHG, REP, OD)

| Rule | What it refuses or reports | Exit |
|---|---|---|
| MODE-01 | A `proctor.yaml` at the repository root rather than inside a protected folder, a configuration path that isn't clean, and in authoritative mode a binding inside the checkout | 3 |
| MODE-03 | Authoritative mode without the job's OIDC token, with one that doesn't verify, or with one naming another commit, repository, run or attempt | 4 |
| MODE-04 | Authoritative mode can't compare the workflow that ran with the base branch's | 4 |
| MODE-05 | `--authoritative` without `--report` | 3 |
| CHG-01 | An unknown `--change-kind` or `--change-kind-source` | 3 |
| CHG-02 | Protected changes that don't fit the claimed change kind | 4 |
| CHG-03 | The reference workflow's `kind` job: two `proctor:` labels, or one naming no kind, fail it before anything is built | workflow |
| REP-06 | A bad `redaction.json_paths` or `redaction.headers` entry (3); the identity check's answer for another user, with both IDs hidden | 3 |
| REP-10, OD-15 | Reported: earlier attempts of the same workflow run that failed a scenario for the same commit and inputs, in `earlier_attempts` | Reported |
| REP-11 | An authoritative binding whose `image`, or an `images` entry, lacks equal `built_digest` and `running_digest` | 4 |
| REP-13 | The reference workflow's `verdict` job: fails unless the report is a passing authoritative report for this commit, run and attempt | workflow |
| OD-16 | Reported: a protected file whose CODEOWNERS rule names none of `protected_owners`, as `UNOWNED` | Reported |

See [Authoritative mode](../guides/ci-github.md) and [`examples/github/`](../../examples/github/README.md).

## Identities (AUTH)

| Rule | What it refuses or reports | Exit |
|---|---|---|
| AUTH-01 | An identity type other than `none` or `bearer` | 3 |
| AUTH-02 | A specification that sets `Authorization`, `Proxy-Authorization` or `Cookie` itself, since credentials come from identities | 3 |
| AUTH-03 | Two identities or variants that send the same credential or share an `id` or `user_id` value, an identity value a step uses that the binding lacks, and an identity variable that is unset, empty or holds a line break | 3 |
| AUTH-04 | The identity check: a bearer identity whose `GET <path>` answers another user ID, or that has no `values.user_id` (3; 2 when the request errors) | 3 |
| AUTH-06 | A step that sends a `variants` credential without expecting 401 or 403 | 3 |
| AUTH-07 | A reference such as `${secret.<name>}` outside `with`, or a secret `proctor.yaml` doesn't declare | 3 |

See [Configuration](configuration.md) for identities, variants and `identity_check`.

## Steps and execution (EXE, FIX, HND)

| Rule | What it refuses or reports | Exit |
|---|---|---|
| EXE-01 | A `do` that isn't `<driver>.<operation>` of a declared driver, a variant without an identity, and a header the HTTP client sets itself (`Content-Length`, `Transfer-Encoding`, `Connection`) | 3 |
| EXE-02 | A malformed `expect`, a `save` name that isn't a letter or `_` followed by letters, digits or `_`, and a `save` `capture` that isn't a valid pattern with exactly one group | 3 |
| EXE-03 | A step without an expectation, and a 2xx http step that asserts only its status | 3 |
| EXE-07 | A step that changes state without a later state check that reads what it changed; a `no_state_check` without a reason or outside verification; a `waive` naming a rule that can't be waived | 3 |
| EXE-09 | A destructive request on a fixed target with no earlier guarding read and no waiver | 3 |
| EXE-10 | The app-log check: a line at a failing level that the scenario's `expect_logs` doesn't list fails it (step); a binding and `proctor.yaml` that disagree about `app_logs` (3) | Step or 3 |
| EXE-11 | A path the operation's fact can't hold, or a matcher on the wrong kind of value (3); at run time, a `none` claim or a zero `count` whose keys no element has (step) | 3 or step |
| EXE-12 | A `matches` pattern that is invalid, over 1 KiB, matches empty text or holds `${...}`, and an `excludes` with an empty text, an empty list or a repeated text | 3 |
| EXE-13 | A matcher that names one path twice in two spellings | 3 |
| EXE-14 | An empty `cleanup`; in cleanup, a step that observes without `eventually`, uses `consistently` or `never`, saves, opens a handle with `as` or has `no_state_check`; and an `unless_passed` that is empty, outside cleanup, names no setup or verification step, or sits on a guarded destructive act on a fixed target | 3 |
| FIX-03 | A read-only check, such as `docker`, `redis`, `otel` or `stub.received`, in setup, which goes through the application | 3 |
| FIX-05 | A value a scenario writes and later matches that doesn't hold `${run.id}`, unless waived with a reason | 3 |
| FIX-08 | A setup operation that no verification step exercises with that outcome | 3 |
| HND-01 to HND-09 | Handles: an invalid name in `as` or `on`, a repeated `as`, an `on` naming no earlier handle or another driver's, a step with both `as` and `on`, a handle on an operation that doesn't open or use one (or a missing one where it does, unless the operation makes it optional, as `http.request` does for a cookie jar), a handle in a table's setup or cleanup, and `as` with `eventually`, `consistently` or `never` | 3 |

See [Specification format](specifications.md#validation) and [Writing specifications](../guides/specifications.md).

## Tables, timing, transcripts and logs (TBL, TIME, TRN, ALG, MUT, INV)

| Rule | What it refuses or reports | Exit |
|---|---|---|
| TBL-01 | A table without `columns`, or a column name that isn't lower-case letters, digits and `_` | 3 |
| TBL-03 | A row that isn't a list with one cell per column | 3 |
| TBL-04 | A table with both `setup` and `setup_from`, or a `setup_from` that names no earlier table | 3 |
| TBL-05 | `${row...}` in `do`, a key, cleanup or outside a table, or naming no column | 3 |
| TBL-07 | Coverage: a missing or repeated combination, a bad `covers`, an unused set or dimension, and a bad exception | 3 |
| TBL-08 | A table's `eventually`, `consistently` or `never` samples each row on its own | — |
| TIME-05 | Reported: a passing `consistently` or `never` step has a `notice`, since samples can't see a change between them | Reported |
| TIME-07 | A sampling window or interval that is missing, invalid or past `time_limits`, and a bad `time_limits` value | 3 |
| TIME-08 | Reported: due samples a slow sample overran, counted in `samples_skipped` | Reported |
| TIME-11 | `eventually`, `consistently` or `never` on a step that acts, and `save` with `consistently` or `never` | 3 |
| TIME-13 | A step `timeout` over `time_limits.max_call`, zero, negative or without a unit, and a `--call-timeout` over `max_call` | 3 |
| TIME-14 | `eventually` with only absence claims on a read that reports what has arrived so far, which would pass before anything arrived | 3 |
| TRN-01 | A `transcript` value that isn't `true` or `false` | 3 |
| TRN-03 | A bad `transcripts` setting or scrub rule | 3 |
| TRN-04 | An approved transcript's path outside `proctor/approved/`, or two that differ only in letter case | 3 |
| TRN-05 | A `transcripts.max_bytes` outside 1 byte to 8 MiB | 3 |
| ALG-01 | An `approved_log` value that isn't `true` or `false`, and `approved_log: true` without `app_logs.approved` in `proctor.yaml` | 3 |
| ALG-02 | `app_logs.approved.fields` missing or too long | 3 |
| ALG-03 | An approved log's path outside `proctor/approved/`, or one shared by two scenarios or tables | 3 |
| MUT-01 | A `mutation` key that is missing or bad, such as a `binding` that isn't an absolute path outside the repository or a script that isn't there, a mutants file that can't be read or isn't a list of valid mutants, and a mutant whose edits don't apply to the files as they are (a `find` found another number of times than `count`, not at its `line`, or edits that overlap) | 3 |
| INV-01 | An invariant that acts, or that uses `save`, `as`, `on`, `variant`, `consistently`, `never`, `no_state_check`, `waive`, `unless_passed`, `${run.id}`, a saved value or `${row.…}` | 3 |

See [Writing specifications](../guides/specifications.md), [Approved transcripts and logs](../guides/approvals.md) and [Mutation testing](../guides/mutation-testing.md).

## Drivers

Each driver checks its own steps, policy and binding during `validate` and before `run` sends anything (exit 3), and reports problems it finds while running as an ERROR step (exit 2). See [Drivers](drivers.md).

| Rule | What it refuses or reports | Exit |
|---|---|---|
| HTTP-02 | `${secret...}` or `${fill(N)}` outside an http body or headers or a `browser.fill` value | 3 |
| HTTP-04 | A binding without `user_agent`, or a specification that sets `User-Agent` | 3 |
| HTTP-05 | A path that doesn't start with `/` or holds `?`, `#`, `\`, a space or a control character once values are inserted, a specification that sets `Host`, a `host` that isn't a lower-case host name or matches none of the binding's `virtual_hosts`, and a request that would leave the target | 3 or step |
| HTTP-08 | A header name with capital letters in an expectation, or a header both expected and absent | 3 |
| HTTP-09 | A list `length` on a request without query parameters, which may count records other runs created, unless waived with a reason | 3 |
| HTTP-10 | An assertion on `$.body` when the body claims to be JSON but isn't valid JSON | Step |
| HTTP-11 | A bad `reads` entry | 3 |
| HTTP-12 | A `reads`, `shows` or `destructive` entry no step uses | 3 |
| HTTP-13 | A claim on a text a driver cut, beside `<x>_truncated: true` | Step |
| HTTP-14 | More than one body, a bad `body_base64`, `body_fill` or `body_file`, and a body over `max_body_bytes` (an ERROR step when its size is known only once values are inserted) | 3 or step |
| HTTP-15 | How `http.open` and `http.frames` read NDJSON and SSE streams | — |
| WS-01 to WS-03 | A bad `ws` path, a `ws.send` without exactly one of `json` and `text`, and a bad `auth` or one without an identity | 3 |
| BROWSER-01 to BROWSER-06 | A bad `path` or query name, a bad `auth` or one without an identity, bad selectors, a bad `wait_for`, a bad `fill` value, and a bad or repeated `origins` entry | 3 |
| CLI-01 to CLI-05 | A bad or unused command, an `env` variable without a binding value, an undeclared command or too many arguments, an `observe` program or file a pull request can change (at `run`), and a step that doesn't expect `$.exit_code` | 3 |
| DB-01, DB-03, DB-07 | A missing or bad `query` or `args`, a bad schema or object name, and a connection that declares no objects | 3 |
| DB-04 | The check that the verification role can only read the declared objects and owns none; when it fails, nothing runs | 4 |
| DDB-01 to DDB-03 | A table that isn't a policy alias, a bad `key` or `item`, and an `index` on `get` or naming no index | 3 |
| DKR-01 to DKR-03 | A bad `scope` policy, a bad socket binding, and bad arguments | 3 |
| DKR-05 to DKR-07 | A request off the driver's fixed list of paths, an answer outside the filter, and a mount the driver can't judge | Step |
| OTEL-01 | An `otel.*` read without `trace_id` or a filter that holds `${run.id}` or a saved value | 3 |
| REDIS-01 to REDIS-04 | A bad `key` or `match`, a `redis.keys` match without `${run.id}` or a saved value, a key outside or a pattern unused by the policy, and a bad binding | 3 |
| REDIS-05 | The check that the Redis user can't write; `unproven` lets the run go ahead, and an error stops it | 4 |
| SQS-01 to SQS-03 | A queue that isn't a policy alias, a bad `max`, and a bad `peek_hold` | 3 |
| SQS-04 | A peek on a queue with a redrive policy | Step |
| STUB-01 | A wrong combination of `stub.fail`'s arguments | 3 |
| STUB-02, STUB-03 | A route the sim's route file doesn't have, and a fault that overlaps an active one | Step |
