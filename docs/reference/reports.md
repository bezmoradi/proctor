# Reports and exit codes

## Exit codes

Exit codes: 0 passed, 1 a scenario failed, 2 an infrastructure error, 3 invalid input, 4 integrity violation. When several apply, 4 wins over 3, 3 over 1, and 1 over 2. Every local `RESULT:` line, from `validate`, `integrity`, `run` and `mutate`, ends `(exit <n>, local, non-authoritative)`, a run that stops before it sends anything included; a partial run's ends `(exit <n>, local, non-authoritative; <k> of <m> scenarios ran)` instead.

On SIGINT or SIGTERM, `run` starts no new scenario, marks the rest NOT_RUN `aborted`, writes its report with `aborted: true` and exits 2 (1 if a scenario already failed); `mutate` likewise starts no new mutant. A panic inside a scenario makes it ERROR `internal` and the run goes on; a panic anywhere else exits 2 with `reason: internal`, and the report is still written.

## Results and reasons

Each scenario, table case and step is PASS, FAIL, ERROR or NOT_RUN. FAIL means the application didn't do what the specification says; ERROR means the check itself couldn't be completed, such as a timeout or a driver problem; NOT_RUN means nothing was sent. Anything but PASS carries a `reason`, and an ERROR often a `cause` too, such as `timeout`, `invalid_request` or `unsupported_result`. `proctor report failed --report FILE` lists them from a saved report.

| Reason | Means |
|---|---|
| `aborted` | An interrupt (Ctrl-C or SIGTERM) stopped the run: what hadn't started is NOT_RUN, and a step it cut short is ERROR with cause `interrupted` |
| `app_logs` | The [app-log check](../guides/specifications.md#application-logs): FAIL for a line at a failing level that `expect_logs` doesn't list, ERROR when the log can't be read (cause `app_logs_unparsed`, `app_logs_unmapped_level` or `app_logs_unreadable`) |
| `cleanup_failed` | A cleanup step didn't pass, so a passing scenario became ERROR; a failing scenario stays FAIL |
| `cleanup_not_needed` | A cleanup step with `unless_passed` didn't run because the step it names passed; it doesn't count against the scenario |
| `cleanup_unguarded` | A guarded destructive cleanup act on a fixed target didn't run, because an earlier step didn't pass |
| `configuration_error` | The configuration or a specification is invalid (exit 3), so nothing ran |
| `disposable_environment_required` | The scenario has `requires: disposable_environment` and the binding doesn't say `disposable: true` (exit 2) |
| `driver_not_in_base` | Authoritative mode: the scenario uses a repository driver the base commit doesn't have (exit 2) |
| `earlier_step_failed`, `earlier_step_errored` | An earlier step failed or errored, so this step wasn't sent |
| `environment_not_ready` | A target didn't report ready within `--ready-timeout`, so no scenario ran (exit 2) |
| `handle_not_open` | The handle the step names with `on` isn't open, because the step that opens it failed or didn't run |
| `identity_check_failed` | The [identity check](configuration.md) failed, so no scenario ran (exit 3, or 2 when a request errored) |
| `internal` | A panic inside Proctor; a scenario it hits is ERROR and the run goes on |
| `invariant_error` | An environment invariant couldn't be checked, so whether the environment is clean is unknown: before the first scenario no scenario runs (exit 2); after a scenario a PASS becomes ERROR |
| `left_state_behind` | An [environment invariant](../guides/specifications.md#environment-invariants) didn't hold after the scenario, which had started clean; a PASS becomes ERROR |
| `log_not_approved`, `log_changed` | FAIL: the scenario has no approved log yet, or its log differs from the approved one (see [Approved logs](../guides/approvals.md#approved-logs)) |
| `log_too_large`, `log_not_written`, `log_unusable` | ERROR: the log is over `max_bytes`, the received file couldn't be written, or the approved file couldn't be compared |
| `missing_saved_value` | The step uses a value an earlier step didn't save, because that step failed or didn't run |
| `not_started` | The scenario never started; also an integrity violation (exit 4, INT-03) |
| `secret_in_telemetry` | FAIL: telemetry holds a credential or secret Proctor knows (see [Telemetry](drivers.md#telemetry)) |
| `setup_failed` | A setup step didn't pass, so the scenario's steps didn't run |
| `transcript_not_approved`, `transcript_changed` | FAIL: the scenario has no approved transcript yet, or its transcript differs from the approved one (see [Approved transcripts](../guides/approvals.md#approved-transcripts)) |
| `transcript_too_large`, `transcript_not_written`, `transcript_unusable` | ERROR: the transcript is over `max_bytes`, the received file couldn't be written, or the approved file couldn't be compared |
| `trust_violation` | A driver's proof failed, such as the read-only check of a database role (DB-04) or a Redis user (REDIS-05), so no scenario ran (exit 4) |

## Reports

- `run --json` and local `--report`: `result`, `exit_code`, `mode`, `trust.reason`, `provenance` (`proctor_version`, `config`, `sha256`), `run_id`, `target`, `readiness`, `violations`, `drivers[]` (`name`, `official`, `program` and `platform` for a repository driver, `version`, `origin`: `official`, `repository` or `unpinned`, `sha256`, `proofs`, `stderr`, `stderr_dropped_bytes`), `identity_check`, `summary`, `scenarios`, `tables`, `received`, and `aborted` after an interrupt. Every local report, one that stops early included, has `trust` and `summary`.
- `summary` adds to the counts `plain_scenarios` and `table_cases` (each `total`, `passed`, `failed`, `errors`, `not_run`), `steps`, `failed_ids` and `errored_ids` (a table case as `<table>/<case>`), `integrity_violations`, and in authoritative mode `protected_changes`.
- A run stopped by an invalid configuration or specification (exit 3) reports every scenario and table case as NOT_RUN `configuration_error`. A scenario or case left without a result is NOT_RUN `not_started` and an integrity violation (exit 4, INT-03).
- A run with `--only` or `--rerun-failed` adds `selection` (`only`, `rerun_failed`, `selected`, `total`); with [invariants](../guides/specifications.md#environment-invariants), a scenario can have `left_state_behind` (the invariant steps that failed after it), `invariant_errors` (the invariant steps after it when one of them errored) and `dirty_since`, and the report, local or authoritative, `dirty_at_start` (the invariant steps that didn't pass before the first scenario).
- A table without an `id` is shown as a grid named `tables[<n>]`, which is also its `tables[].id` and its cases' `table`.
- A step has `phase`, `result`, `reason`, `cause`, `action.summary`, `evidence` (`request_excerpt`, `response_excerpt`, `driver_duration_ms`), `duration_ms`, `samples`, `samples_skipped` (due times a slow sample overran, omitted when 0), `notice` (TIME-05), `saved` and `assertions[]` (with `cut_at` and `overflow`); a scenario has `handles[]`, a table `setup_from` and `rows[].cells`. A step whose telemetry holds a known credential or secret FAILs with reason `secret_in_telemetry` and one `secret_scan` assertion per place (at most 20), whose `target` is the path and `actual` `[redacted]`.
- An ERROR the interrupt (Ctrl-C or SIGTERM) caused has cause `interrupted`; its reason is `aborted`, or `setup_failed` for a setup step.
- The authoritative report adds `trust` (`workflow_identity`, `workflow_changed`: `true`, `false`, `unknown` or `not_applicable`, `codeowners_errors`, `unowned_protected_paths`, `approval_required`, `repository_drivers_from_base`, `repository_drivers_not_in_base`), `provenance` (`repository`, `tested_commit`, `parents`, `pr_head`, `base`, `image`, `images`, `environment`, `ci_run_id`, `ci_run_attempt`, `started_at`, `ended_at`), `protected_changes`, `change_kind`, `earlier_attempts` and `errors`. Its `provenance.sha256` also holds a `protected_paths:<path>` key for each file only `protected_paths` protects (INT-16).
- `validate --json` and `integrity --json`: `result`, `exit_code`, `mode` and `report`; `validate` adds `drivers`, `errors`, and outside Git `git: false` with `note`. `diff --json`: `result`, `exit_code`, `protected_changes`, `weakenings`, `change_kind` and `diff` (`files`, `scenarios`, `weakenings`, `driver_changes`, `expectation_changes`, `assertion_moves`, and `added_steps` when a step was added).
- `mutate --report`: `selected`, `trust`, `provenance`, `baseline`, `mutants[]` (`verdict`, `killed_by`, `failed_scenarios`, `run_result`) and `counts`.
