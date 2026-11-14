# Proctor — Acceptance Tests

The observable tests that show each requirement and outcome in `prd-revised.md` (revision 2). A phase in `prd-revised.md` §20 is done when the tests assigned to it pass. Status: draft, written with revision 2; not yet reviewed.

Every expected outcome is something a test can assert:

- an exit code;
- a JSON report field and value;
- a job-summary line;
- an entry in `proctor diff` output;
- a GitHub merge state.

GitHub merge states use the GraphQL `mergeStateStatus` values (`BLOCKED`, `BEHIND`, `CLEAN`) and `reviewDecision` values (`REVIEW_REQUIRED`, `APPROVED`).

## Conventions

**Test IDs.**

| Prefix | Group |
|---|---|
| AT-I | Integrity |
| AT-W | Workflow and platform |
| AT-T | Time checks |
| AT-R | Results and exit codes |
| AT-O | Outcomes |
| AT-Q | Single-requirement tests referenced from the coverage table |

**Report fields.** REP-08 names only `result`, `mode`, `trust` and `summary`. Other field names in this file are provisional and marked **(p)**. Phase 0 and Phase 2 must fix them (see "PRD inconsistencies", item 16).

**Time notation.**

- t0 is the start of a check's first sample.
- `#n` is the n-th sample, counting from `#1` at t0.
- `@+t` is an offset from t0.
- `job(t, f)` means the example application's background job changes field `f` of Bob's project `t` seconds after the denied delete, and leaves it changed.
- Unless stated, a GET reads state when the request arrives, so a sample observes state at its start time.
- PA-002's `still-readable-by-bob` runs 10 s at 250 ms, so its samples are due at @+0, +0.25 … +10.0: 41 samples.

**Common fixture.**

- **Test repository R** in a test organization, configured per `prd-revised.md` §6.3.
  - Base commit **B** on `main`, holding the manifest, PA-001, PA-002, PA-ALLOWED and PA-DENIED.
  - Pull request head **H**, and GitHub's test merge commit **M** (first parent B, second parent H).
  - Tests marked **[org]** need this test organization and can't run in a local Git repository.
- **Example application** "projects", built from R with fault flags set in its configuration:
  - `FAULT_OWNER_CHECK_OFF`: any user can read or delete any project.
  - `FAULT_BG_JOB=<t>,<field>`: see `job(t, f)`.
  - `FAULT_LOG_FRESH_TRACE`: logs events under a new trace ID.
  - `FAULT_LOG_SUCCESS_ONLY`: logs `project.export.completed` without producing an export.
  - `FAULT_EXPORT_NOOP`: the export workflow completes without writing the export.
  - `FAULT_SLOW_GET=<ms>`: GETs read state on arrival, then delay the response.
  - Probe endpoints: `/probe/echo`, `/probe/fail-once`, `/probe/redirect-external`, `/probe/headers`, `/probe/concurrency`.
- **Stub S:** an HTTP server that answers 200 to everything and counts requests.
- **Reference workflow W:** per `prd-revised.md` §16.2, with build, verification and aggregator jobs.

---

## AT-I: Integrity

All tests run in authoritative mode on M unless stated. "Listed" means it appears in three places: `proctor diff --base B --head H` output, the report's protected-changes list (p), and a job-summary line naming the path, the scenario or case ID, and "approval required".

| ID | Attack or mistake | Expected observable | Requirements | Phase |
|---|---|---|---|---|
| AT-I-01 | H deletes `specs/project-access.yaml` but keeps its manifest entry | `proctor integrity --commit M` exits 4; `proctor run` exits 4, `result: integrity_violation`, and `summary` names INT-01 and the path | INT-01, INT-04 | 0 |
| AT-I-02 | A manifest path differs from the tree entry only in letter case | Exit 4 (INT-01), on every filesystem | INT-01 | 0 |
| AT-I-03 | H removes the manifest entry and keeps the file | Listed (manifest modified; its scenarios `removed`); the file appears under `unlisted` (p); the exit code comes from execution (0 if the rest pass) | INT-05, INT-10 | 0 |
| AT-I-04 | H moves a spec without changing its bytes and updates the entry | Diff entry `moved`, same blob and scenario IDs; exit 0 | INT-08 | 0 |
| AT-I-05 | H moves a spec and changes PA-D-01's 403 to 200 (Git would call it a rename, R099) | Diff entries `removed` (old path) and `added` (new path), and scenario PA-D-01 `moved`, modified; never "renamed" only | INT-08 | 0 |
| AT-I-06 | H changes scenario ID `PA-001` to `PA-101` | Listed as `PA-001` removed and `PA-101` added | INT-02, INT-10 | 0 |
| AT-I-07 | Two specs use scenario ID `PA-001` | `proctor validate` exits 3, naming both files | INT-02 | 0 |
| AT-I-08 | H changes PA-D-01's expected status from 403 to 200 | Listed; with the fault present the check passes, so AT-O1 governs merge. Phase 6 adds the semantic-diff line "PA-D-01 expected status 403 → 200" | INT-10, §7.5 | 0 (listing), 6 (semantic) |
| AT-I-09 | H changes 403 to the accepted-value list `[200, 403]` | Listed; the semantic diff says "broadened matcher" | §7.5 | 6 |
| AT-I-10 | H replaces an exact value with a wildcard | `proctor validate` exits 3: wildcards aren't an assertion type (§11.2) | §11.2 | 2 |
| AT-I-11 | H changes PA-D-01's identity from `alice` to `bob` | Listed; the semantic diff says "identity changed in denied case PA-D-01" | §7.5 | 6 |
| AT-I-12 | H lengthens an `eventually` timeout, shortens a `consistently` duration, and lengthens its interval | Listed; the semantic diff marks each as weakening, with direction | §7.5 | 6 |
| AT-I-13 | H changes PA-ALLOWED's `bob-project` setup body | Listed; the semantic diff shows the changed setup on every case of PA-ALLOWED and PA-DENIED | §7.5, TBL-04 | 6 |
| AT-I-14 | H reorders scenarios in a spec | Listed; the semantic diff says "order changed" | §7.5 | 6 |
| AT-I-15 | H flips `requirement_is_operational` | Listed; the semantic diff names the flag | EVD-02, §7.5 | 6 |
| AT-I-16 | A runner test double omits PA-002 from its results | Exit 4; PA-002 `NOT_RUN`, reason `not_started` | INT-03 | 0 |
| AT-I-17 | Negative control for AT-I-16: PA-002's first step FAILs | Exit 1; PA-002 `FAIL`; later steps `NOT_RUN`, reason `earlier_step_failed`; no integrity violation | INT-03 | 0 |
| AT-I-18 | An empty manifest; separately, a listed spec with no scenarios or tables | Both: `proctor integrity` exits 4 before any request (stub and application request counts 0) | INT-04 | 0 |
| AT-I-19 | H adds `specs/extra.yaml`, not in the manifest | Not executed (its IDs are absent from results); listed under `unlisted` (p) | INT-05 | 0 |
| AT-I-20 | CODEOWNERS names a nonexistent team for `proctor/` | Exit 4; `summary` names INT-11 and GitHub's CODEOWNERS error | INT-11 | 0 [org] |
| AT-I-21 | CODEOWNERS exists in both `/.github/` and `/` | Exit 4 (INT-11) | INT-11 | 0 |
| AT-I-22 | A CODEOWNERS line leaves a protected path with no owner from `protected_owners` | Exit unchanged; the report lists the path as unowned (p). Whether this should fail is OD-16 | INT-11 | 0 |
| AT-I-23 | A protected spec becomes a symlink; a later commit changes only the target | Exit 4 (INT-13), naming the path and its mode `120000` | INT-13 | 0 |
| AT-I-24 | `proctor/specs` becomes a symlinked directory | Exit 4 (INT-13) | INT-13 | 0 |
| AT-I-25 | A submodule at `proctor/vendor` with `.gitmodules` `ignore = all` | Exit 4 (INT-13), mode `160000` | INT-13 | 0 |
| AT-I-26 | A protected spec becomes a Git LFS pointer | Exit 4 (INT-13) | INT-13 | 0 |
| AT-I-27 | `compose.verify.yaml` mounts `./scripts/init.sql`, which is outside the protected root and not listed in `proctor.yaml` | Exit 4 (INT-13), naming the reference | INT-13 | 0 |
| AT-I-28 | H edits a file that `compose.verify.yaml` references under the protected root | Listed; its hash appears in the report (INT-16) | INT-13, INT-16 | 0 |
| AT-I-29 | H adds `*.yaml text eol=crlf` to `.gitattributes` | Exit 4 (INT-14), naming the attribute and paths | INT-14 | 0 |
| AT-I-30 | H adds `proctor/** linguist-generated=true` | Exit 4 (INT-14) | INT-14 | 0 |
| AT-I-31 | A spec contains a duplicate key (`status: 403` then `status: 200`) | `proctor validate` exits 3, naming both lines | INT-15 | 0 |
| AT-I-32 | A spec uses an anchor and alias; separately a merge key `<<`; separately a custom tag | Each: exit 3 (INT-15) | INT-15 | 0 |
| AT-I-33 | A scenario uses a disabled adapter; separately an undeclared connection; separately an undeclared identity | Each: `proctor validate` exits 3 (INT-12) | INT-12 | 0 (adapter), 2 (identity) |
| AT-I-34 | H adds a second `proctor.yaml` at the repository root whose `manifest:` points to an unprotected file | No effect: the report's `proctor.yaml` hash equals the blob at the trusted path | MODE-01 | 0 |
| AT-I-35 | H adds `proctor/environments/ci.yaml` and points `local.yaml` at stub S, with `FAULT_OWNER_CHECK_OFF` set | Stub S count 0; PA-D-01 `FAIL`, exit 1, the same as a control run; the report's binding hash equals the trusted binding's | MODE-01 | 0 |
| AT-I-36 | A spec has `${project_id}` inside a `query` | `proctor validate` exits 3 (DB-01) | DB-01 | 4 |
| AT-I-37 | H adds only a trailing space and a YAML comment to a spec | Listed as modified; the report's hash for that spec equals SHA-256 of the committed blob at H | INT-10 | 0 |
| AT-I-38 | Checkout with `fetch-depth: 1`, so the base commit is absent | Exit 4; never "Protected changes against base: none" | INT-08, INT-09 | 0 |
| AT-I-39 | A workflow step modifies a spec in the working tree after checkout | The executed bytes and the report hash equal the blob at M, not the working tree | INT-07, MODE-02 | 0 |
| AT-I-40 | `FAULT_LOG_SUCCESS_ONLY`; the export scenario has a Level 1 export check and a Level 3 log check | Scenario `FAIL` at the Level 1 step; exit 1 | EVD-01 | 5 |
| AT-I-41 | `FAULT_EXPORT_NOOP`; the export scenario's Temporal check expects `completed` | The Temporal step passes; the scenario `FAIL`s at the Level 1 or 2 step; exit 1 | EVD-01, TMP-05 | 5 |
| AT-I-42 | A scenario has only Level 3 checks and no `requirement_is_operational` | `proctor validate` exits 3 (EVD-01) | EVD-01 | 5 |
| AT-I-43 | A manifest path contains `../` or is absolute | Exit 4. **Expected, not yet stated by the PRD** (see inconsistencies, item 11) | INT-01 | 0 |
| AT-I-44 | The running application's image digest differs from the build job's output | Exit 4 (REP-11) | REP-11, MODE-05 | 0 |

## AT-W: Workflow and platform

| ID | Attack or condition | Expected observable | Requirements | Phase |
|---|---|---|---|---|
| AT-W-01 | H changes the application build so that, during the build, it writes an executable to `/usr/local/bin/proctor` and listens on the target port. `FAULT_OWNER_CHECK_OFF` is set | The verification job log shows the executed binary's digest equal to the pin; stub count 0; the check fails with PA-D-01 `FAIL`, exit 1; merge state `BLOCKED` | SEC-12, §16.2 | 0 [org] |
| AT-W-02 | H commits `./proctor` and `bin/proctor` that write a marker file and print a passing report | No marker file exists after the job; the result equals a control run | SEC-11, §16.2 | 0 [org] |
| AT-W-03 | The download source serves a binary whose checksum differs from the pin | The job fails before Proctor runs (no report artifact); the aggregator fails; merge state `BLOCKED` | SEC-11 | 0 [org] |
| AT-W-04 | `FAULT_BG_JOB` fires in 50% of runs. Attempt 1 fails and attempt 2 passes | Attempt 2's report lists attempt 1 as an earlier behavioral FAIL (p), and the job summary names it. Merge state may become `CLEAN`: a documented limit (§4.4, OD-15) | REP-10 | 0 [org] |
| AT-W-05 | H breaks the application build | The aggregator check fails; merge state `BLOCKED` | §16.2 item 7 | 0 [org] |
| AT-W-06 | Option B: H adds `if: false` to the verification job only | The aggregator fails because a prerequisite was skipped; `BLOCKED` | §16.2 item 7 | 0 [org] |
| AT-W-07 | Option B: H also changes the aggregator so it always succeeds | `reviewDecision: REVIEW_REQUIRED` until a code owner approves the workflow change. Under option A, the edit has no effect | §6.3, OD-01 | 0 [org] |
| AT-W-08 | PR 2 passes on base B; then PR 1 merges and changes behavior so that PR 2 combined with it fails | PR 2's `mergeStateStatus` is `BEHIND`. After updating, the check fails and the state is `BLOCKED`. With a merge queue, the `merge_group` run fails and PR 2 leaves the queue | §6.3, O-2, REP-03 | 0 [org] |
| AT-W-09 | An earlier commit of H adds a workflow that posts the required check name; the final commit removes it | Expected `BLOCKED`. If `CLEAN`, Phase 0 records it as an option B limit in §4.4 | §6.3, P-08 | 0 [org] |
| AT-W-10 | Someone posts a commit status with the required check name using a personal token | `BLOCKED`, because the expected source is pinned | §6.3 | 0 [org] |
| AT-W-11 | Option A: H edits a copy of the required workflow inside R | The run log and the report's `trust.workflow_identity.job_workflow_sha` equal the pinned SHA | OD-01, MODE-03 | 0 [org] |
| AT-W-12 | Run on test merge M | The report records base = M's first parent, head = H, tested = M. If the second parent doesn't equal the event's head SHA, exit 4 | INT-09 | 0 |
| AT-W-13 | The workflow grants `id-token: write`; separately, it doesn't | With it, `trust.workflow_identity` holds `workflow_ref`, `workflow_sha`, `job_workflow_ref`, `job_workflow_sha`, `sha`, `event_name`, `run_id`, `run_attempt`, equal to the run's values. Without it, `trust.workflow_identity: unverified` | MODE-03 | 0 [org] |
| AT-W-14 | Option B: H changes the verification workflow, which still runs Proctor | `trust.workflow_changed: true`; job-summary line "not authoritative until the workflow change is approved" | MODE-04 | 0 [org] |
| AT-W-15 | A laptop run with `CI=true` and `GITHUB_ACTIONS=true` and no `--authoritative` | `mode: local`; "non-authoritative" in the terminal header and on the final result line | MODE-01, O-4 | 0 |
| AT-W-16 | A laptop run with `--authoritative` | `mode: authoritative` and `trust.workflow_identity: unverified`. The merge rule is unaffected: this report sets no status | MODE-01, MODE-03 | 0 |
| AT-W-17 | A previous run's report artifact is attached to a newer head | Merge state is governed by the status on the new head only | REP-01, REP-03 | 0 [org] |
| AT-W-18 | The verification job produces no report, or an unparseable one | The aggregator fails; `BLOCKED` | REP-13, §16.2 item 6 | 0 [org] |
| AT-W-19 | A fork pull request (only if OD-19 includes forks) | The check runs without repository secrets, and its result follows the scenarios. It is never `success` by skipping | §6.3, FIX-01 | 3 [org] |

## AT-T: Time checks

Unless stated, each test runs a scenario containing only the check under test, and expectations are under TIME-01 to TIME-12.

| ID | Edge | Input | Expected observable | Requirements | Phase |
|---|---|---|---|---|---|
| AT-T-01 | Interval larger than window | PA-002 with `interval: 30s` | `proctor validate` exits 3 | TIME-07 | 4 |
| AT-T-02 | Interval equal to window | `interval: 10s`, `duration: 10s` | Exit 3 (more than half the window) | TIME-07 | 4 |
| AT-T-03 | Interval equal to timeout | `eventually` 15 s / 15 s | Exit 3 | TIME-07 | 4 |
| AT-T-04 | Zero window | `duration: 0s`; separately `"0"` | Exit 3 | TIME-07 | 4 |
| AT-T-05 | Zero timeout | `timeout: 0s` | Exit 3 | TIME-07 | 4 |
| AT-T-06 | Negative | `duration: -10s` | Exit 3 | TIME-07 | 4 |
| AT-T-07 | No unit | `timeout: 10` | Exit 3, naming the field | TIME-07 | 4 |
| AT-T-08 | Zero interval | `interval: 0s` | Exit 3; no panic | TIME-07 | 4 |
| AT-T-09 | Below the minimum | `interval: 50ms` with `min_interval: 100ms` | Exit 3 | TIME-07 | 4 |
| AT-T-10 | Too many samples or too long | `duration: 10m`, `interval: 100ms` (6,001 samples); separately `duration: 11m` | Each: exit 3 | TIME-07 | 4 |
| AT-T-11 | Sample slower than the interval | `FAULT_SLOW_GET=600`; job(2.1, status) | Sample starts @+0, 0.6, 1.2, 1.8, 2.4. `FAIL` at #5 @+2.4 s; exit 1. Samples started 5. The skipped-due-time count can't be asserted yet (inconsistencies, item 7) | TIME-08 | 4 |
| AT-T-12 | Sample slower than 4 × interval | `FAULT_SLOW_GET=1500`, no change | Every sample times out at 1 s; `ERROR`; exit 2 | TIME-08, TIME-10 | 4 |
| AT-T-13 | Backend retry field | The spec has `retry: 3` on a step | Exit 3 | TIME-06 | 4 |
| AT-T-14 | PostgreSQL snapshot | `still-stored` with job(2.1, status) | `FAIL` at #10 @+2.25 s; exit 1 (one transaction per sample) | DB-02 | 4 |
| AT-T-15 | No change | PA-002, fast GETs | `PASS`; samples due 41, started 41 | TIME-02, TIME-04, TIME-08 | 4 |
| AT-T-16 | Change at the window's end | job(9.9, status) | `FAIL` at #41 @+10.0 s; exit 1 | TIME-08 | 4 |
| AT-T-17 | Slow last sample | `FAULT_SLOW_GET=400`; job(9.9, status) | The final sample runs and is awaited. `FAIL` at the last sample, which starts @+10.15 s or later; exit 1 | TIME-08 | 4 |
| AT-T-18 | Trigger anchor | PA-002 as written | The report's triggering step (p) is `alice-deletes`, and t0's offset from it is recorded | TIME-09, TIME-04 | 4 |
| AT-T-19 | Late log check | An event logged @+20 s after the trigger; the `eventually` log check starts @+45 s | `PASS` at #1 | LOG-06 | 5 |
| AT-T-20 | `eventually` in setup times out | A setup step with `eventually` never satisfied | Scenario `ERROR`, reason `setup_failed`; exit 2 | FIX-04 | 4 |
| AT-T-21 | Polling a POST | `eventually` on POST `/api/projects/{id}/export` | Exit 3 | TIME-11 | 4 |
| AT-T-22 | `save` with `consistently` | A `consistently` step that also has `save` | Exit 3 | TIME-11 | 4 |
| AT-T-23 | One-time link | `consistently` on a GET that returns 410 after the first call | `FAIL` at #2 @+0.25 s; exit 1 | TIME-10 | 4 |
| AT-T-24 | Rate limit | PA-002 against a limit of 20 requests per 10 s | `FAIL` at #21 @+5.0 s with status 429; exit 1 | TIME-10 | 4 |
| AT-T-25 | Satisfied on the deadline sample | `eventually` 15 s / 500 ms; the row completes @+14.8 s; queries take 300 ms | `PASS` at #31 | TIME-08 | 4 |
| AT-T-26 | Long-poll | A GET that answers @+25 s; `eventually` 15 s / 500 ms | Every sample times out at 2 s; `ERROR`; exit 2 | TIME-08 | 4 |
| AT-T-27 | Buffered log flush | `never` 10 s; the forbidden record is flushed @+10.3 s | `PASS`, with the TIME-05 notice (documented limit) | TIME-05 | 5 |
| AT-T-28 | Errors, then a pass | `eventually`: samples #1 to #3 connection refused, #4 satisfied | `PASS` at #4; errored samples 3 | TIME-10 | 4 |
| AT-T-29 | Every sample errors | The database is down for the whole window | `ERROR`; exit 2 | TIME-10, DB-05 | 4 |
| AT-T-30 | Permanent error | Invalid column (SQLSTATE 42703) | `ERROR` at #1; exit 2 | DB-05, TIME-10 | 4 |
| AT-T-31 | One errored sample | PA-002; #20 connection reset, others `active` | `ERROR` after the window; exit 2 | TIME-10 | 4 |
| AT-T-32 | Error, then a violation | As AT-T-31, and #30 returns `status: archived` | `FAIL` at #30; exit 1 | TIME-10 | 4 |
| AT-T-33 | 5xx response | #20 returns 503 with HTML | `FAIL` at #20; exit 1 | TIME-10 | 4 |
| AT-T-34 | Log file never created | The binding names a file the application never writes | Readiness fails: exit 2; every scenario `NOT_RUN`, reason `environment_not_ready` | FIX-07, LOG-04 | 3 |
| AT-T-35 | The run's only check errors throughout | A single-check run where every sample errors | Exit 2, not 4 | INT-04 | 4 |
| AT-T-36 | Misspelled event in `never` | `event: project.delted`; the access record carries the trace ID | `PASS`: a documented limit, because liveness doesn't catch misspellings (OD-17) | LOG-08 | 5 |
| AT-T-37 | Trace propagation removed | `FAULT_LOG_FRESH_TRACE`; `never` log check | `ERROR`, reason `correlation_not_observed`; exit 2 | LOG-08 | 5 |
| AT-T-38 | `never` through Visibility | `never` with a search-attribute lookup | Exit 3 | TMP-08 | 5 |
| AT-T-39 | Event during the triggering request, `eventually` | The handler logs the event before responding | `PASS` at #1 | LOG-06 | 5 |
| AT-T-40 | Event during the triggering request, `never` | The handler deletes, logs `project.deleted` with the trace ID, then answers 403 | `FAIL` at #1; exit 1 | LOG-06 | 5 |
| AT-T-41 | Change reverted within the request | The handler changes Bob's row and restores it before answering | `PASS` (documented limit, OD-08) | §4.4 | 4 |
| AT-T-42 | Time-window correlation with `eventually` | `correlation: time_window` on an `eventually` | Exit 3 | LOG-02 | 5 |
| AT-T-43 | Coarse timestamp, file position | A `never` with time-window correlation; a record written after the recorded position but stamped one second earlier | `FAIL`; exit 1 (position decides, not timestamp) | LOG-02 | 5 |
| AT-T-44 | Workflow started inside the request | POST `/export` starts `export-${export_id}`, then answers 202 | `PASS` when the chain's latest run is `completed` | TMP-07 | 5 |
| AT-T-45 | Clock skew | The Temporal server's clock is 300 ms behind Proctor's | Same as AT-T-44 (within `max_clock_skew`); the report shows both clocks' times | TMP-07 | 5 |
| AT-T-46 | Continue-As-New | The export continues-as-new twice | One candidate chain; `PASS`; the report lists 3 run IDs and the first run ID | TMP-02, TMP-03 | 5 |
| AT-T-47 | Retry chain | The first run fails; a retry run completes | Target `chain`: `PASS`. Target single run: `FAIL` at the final sample, last status `failed` (TMP-04) | TMP-02, TMP-04 | 5 |
| AT-T-48 | Static Workflow ID reused | `nightly-summary`, with an older completed chain present | The older chain is excluded by the pre-trigger snapshot. If the application starts no new chain: `FAIL` at the final sample; exit 1 | TMP-07 | 5 |
| AT-T-49 | Two candidate chains | Two new chains match | `ERROR`; exit 2 | TMP-07 | 5 |
| AT-T-50 | Wall-clock jump | The system clock steps forward 2 s at @+5 s | 41 samples over 10 s of monotonic time | TIME-12 | 4 |
| AT-T-51 | Slow start | The application port is closed for 5 s after start | No scenario `ERROR`; the results equal a run on an already-started environment | FIX-07 | 3 |
| FIX-08 | AT-Q-45 | 0 |
| AT-T-52 | Never ready | The readiness endpoint never answers 2xx within `startup_timeout` | Exit 2; every scenario `NOT_RUN`, reason `environment_not_ready`; not 4 | FIX-07, INT-03 | 3 |
| AT-T-53 | Late Temporal worker | The worker for task queue `exports` starts 20 s late | Readiness waits for a poller; the results equal a run with a prompt worker | FIX-07 | 3 |
| AT-T-54 | Paused for the whole window | The workflow is paused for 60 s of `eventually` 60 s / 1 s | `FAIL` at the final sample; last status `paused` | TMP-04 | 5 |
| AT-T-55 | Paused counted as running | `consistently: status: running`, 1 s interval; paused @+2.5 s | `FAIL` at #4 @+3.0 s | TMP-04 | 5 |
| AT-T-56 | Failed early | The run fails @+4.5 s with no newer run; `eventually: completed`, 60 s / 1 s | `FAIL` at the final sample (@+60 s), last status `failed`; no early stop (TMP-04) | TMP-04 | 5 |
| AT-T-57 | Unasserted field | job(2.1, name) | `FAIL` at #10 @+2.25 s, on `$.version` | §11.9 PA-002 | 4 |
| AT-T-58 | Cached GET | Bob's GET is served from a 30 s cache; job(2.1, status) | The HTTP step passes; `still-stored` `FAIL`s at #10 @+2.25 s; exit 1 | §10, PA-002 | 4 |
| AT-T-59 | Change after the window | job(12, status) | `PASS`, with the TIME-05 notice | TIME-05 | 4 |
| AT-T-60 | Change between samples | The field changes @+2.3 s and reverts @+2.4 s | `PASS`, with the TIME-05 notice | TIME-05 | 4 |
| AT-T-61 | Transient change a sample can see | The field changes @+2.1 s and reverts @+3.0 s | `FAIL` at #10 @+2.25 s (tests "every sample") | TIME-02 | 4 |

## AT-R: Results and exit codes

| ID | Situation | Expected observable | Requirements | Phase |
|---|---|---|---|---|
| AT-R-01 | PA-002's setup POST returns 500 | PA-002 `ERROR`, reason `setup_failed`; its steps `NOT_RUN`; exit 2 | FIX-04 | 2 |
| AT-R-02 | PA-ALLOWED's setup fails | PA-ALLOWED and PA-DENIED `ERROR`; every case `NOT_RUN`, reason `setup_failed`; exit 2, not 4 | TBL-06, INT-03 | 2 |
| AT-R-03 | PA-D-01 fails (`FAULT_OWNER_CHECK_OFF`) | PA-D-02 and PA-D-03 still run; PA-DENIED `FAIL`; exit 1 | TBL-06 | 2 |
| AT-R-04 | An earlier step fails | Later steps `NOT_RUN`, reason `earlier_step_failed`; scenario `FAIL`; exit 1 | INT-03 | 2 |
| AT-R-05 | An earlier step errors | Later steps `NOT_RUN`, reason `earlier_step_errored`; scenario `ERROR`; exit 2 | INT-03 | 2 |
| AT-R-06 | `FAIL` in one scenario and `ERROR` in another | Exit 1; `result: failed`; `summary` counts 1 failed and 1 error | REP-07, REP-08 | 2 |
| AT-R-07 | A listed spec fails validation | Exit 3; application and stub request counts 0 | REP-07 | 2 |
| AT-R-08 | A listed spec is unparseable YAML | Exit 3 | REP-07 | 2 |
| AT-R-09 | An integrity violation and a missing credential together | Exit 4 | REP-07 | 2 |
| AT-R-10 | SIGTERM during the second scenario | A report exists with `result: error` and `aborted: true`; unstarted scenarios `NOT_RUN`, reason `aborted`; exit 2 | REP-13 | 2 |
| AT-R-11 | Target unreachable from the start | Readiness fails; exit 2; scenarios `NOT_RUN`, reason `environment_not_ready` | FIX-07, R-01 | 3 |
| AT-R-12 | After an `ERROR` in scenario 1 | Scenarios 2 to n have their own results | REP-12 | 2 |
| AT-R-13 | Alice's token variable missing; separately empty | Exit 3; application request count 0 | AUTH-03 | 2 |
| AT-R-14 | `PROCTOR_ALICE_USER_ID` unset | Exit 3 | AUTH-03 | 2 |
| AT-R-15 | Alice and Bob bound to the same token; separately the same user ID | Exit 3 | AUTH-03 | 2 |
| AT-R-16 | An identity defined in the binding but not in `proctor.yaml` | Exit 3 | AUTH-03 | 2 |
| AT-R-17 | `identity_check` returns a different user ID for Bob | Exit 3 | AUTH-04 | 2 |
| AT-R-18 | Protected changes present; every check passes (OD-02 A) | Exit 0; `result: passed`; `summary` shows protected changes N > 0; job-summary lines list them | REP-08, INT-10 | 0 |
| AT-R-19 | A request step that expects 200 asserts status only | Exit 3. The same step expecting 403 with status only: valid | EXE-03 | 2 |
| AT-R-20 | `result` matches the exit code | For every test in this file, `result` maps to the exit code: passed→0, failed→1, error→2, invalid→3, integrity_violation→4 | REP-08 | 2 |
| AT-R-21 | Proctor panics internally (test-only fault hook in a test build) | `result: error`, reason `internal`; exit 2; a report exists | REP-13 | 2 |

## AT-O: Outcomes

| ID | Scenario | Expected observable | Outcome |
|---|---|---|---|
| AT-O1 | H sets `FAULT_OWNER_CHECK_OFF` and changes PA-D-01 to expect 200, so the check passes. (1) A reviewer who isn't a code owner approves. (2) A code owner approves. (3) The agent then pushes another commit | (1) `reviewDecision: REVIEW_REQUIRED`, `BLOCKED`. (2) `CLEAN`, which is the intended effect of approval. (3) The approval is dismissed; `REVIEW_REQUIRED`, `BLOCKED`. Also: approval by the pusher of the latest push doesn't count | O-1 |
| AT-O2 | (a) H changes only application code, setting `FAULT_OWNER_CHECK_OFF`. (b) The base-advance case of AT-W-08 | (a) The check on H fails; the report has PA-D-01 `FAIL`; exit 1; `BLOCKED`. (b) As AT-W-08 | O-2 |
| AT-O3 | A passing authoritative run on M | The report records: tested M with parents B and H, head H, base B, image digest equal to the build output, manifest hash equal to SHA-256 of the manifest blob at M, per-spec hashes, Proctor, runner and adapter versions equal to the pins, the workflow identity, and the run attempt | O-3 |
| AT-O4 | A local run | `mode: local`; "non-authoritative" in the terminal header and on the final line. "Fast" in O-4 has no target, so no timing is asserted | O-4 |

## AT-Q: Single-requirement tests

| ID | Test | Expected observable | Requirements | Phase |
|---|---|---|---|---|
| AT-Q-01 | The application returns `{"id":"${run.id}"}` literally; the next step sends `${project_id}` to `/probe/echo`, which reports what it received | Echo received the literal `${run.id}`; exit 0 | HTTP-02 | 2 |
| AT-Q-02 | `/probe/redirect-external` answers 302 to another host; the step expects 302 and that `Location` | Exit 0. Exit 1 would mean the redirect was followed | HTTP-01, SEC-07 | 2 |
| AT-Q-03 | `/probe/concurrency` records requests in flight across a run | `max_in_flight` = 1 | EXE-04 | 2 |
| AT-Q-04 | Unique sentinel values in the token variables, the DSN password, a `Set-Cookie` header, response fields `password` and `api_key`, `$.email` and `X-Api-Key`. Each check also fails another assertion, so evidence is written | No sentinel appears anywhere in the JSON report or the terminal output | REP-04, REP-05, REP-06, AUTH-02, SEC-01, SEC-09 | 6 |
| AT-Q-05 | A captured value of `nope' OR '1'='1` passed as `$1` in a lookup by ID; `expect: rows: 0` | Exit 0 (bound, not interpolated) | DB-01, SEC-04 | 4 |
| AT-Q-06 | Proctor's role has `INSERT` on `app.projects`. Separately: superuser; `CREATEROLE`; member of another role; owns an object; `USAGE` on a sequence | Each: exit 4 before the first scenario, whatever the transaction mode | DB-04, SEC-03 | 4 |
| AT-Q-07 | A declared view calls a volatile function that inserts a row | The step is `ERROR` with SQLSTATE 25006; exit 2; the row count is unchanged; the report labels the check "application-defined view" | DB-02, DB-07 | 4 |
| AT-Q-08 | The migrations create a schema named after Proctor's role, containing a `projects` table with different data | The query reads `app.projects`; an assertion on a known value passes | DB-03 | 4 |
| AT-Q-09 | A step expecting 200 calls `/probe/fail-once`, which fails once and then succeeds | `FAIL`; exit 1; one request sent | TIME-06 | 4 |
| AT-Q-10 | `FAULT_LOG_FRESH_TRACE`; an `eventually` log check on `trace_id` and `event` | `FAIL` at the final sample; exit 1 (matching by event alone would pass) | LOG-02 | 5 |
| AT-Q-11 | A query selects three columns and asserts one | The report contains only the asserted column | DB-06 | 4 |
| AT-Q-12 | Two records match the key; only one has every expected field | `PASS`. When the fields are split across the two records: `FAIL` | LOG-07 | 5 |
| AT-Q-13 | After a run with log checks | The log file's size, inode and SHA-256 are unchanged | LOG-03 | 5 |
| AT-Q-14 | The log file is rotated during a `consistently` window | The sample during rotation is a sample error; the result follows TIME-10 | LOG-04 | 5 |
| AT-Q-15 | A binding log source of `type: docker` | Exit 3 | LOG-01 | 5 |
| AT-Q-16 | A table row contains a condition | Exit 3 | TBL-03 | 2 |
| AT-Q-17 | PA-ALLOWED and PA-DENIED run; the application counts POST `/api/projects` | 2 POSTs, not one per case; every case resolves `${alice_project}` | TBL-04, TBL-05 | 2 |
| AT-Q-18 | A run with PA-ALLOWED and PA-DENIED | The JSON report has one result per case ID; the terminal shows each table with per-case results | TBL-01, TBL-02 | 2 |
| AT-Q-19 | Scenario B references a capture from scenario A | Exit 3 | EXE-01, EXE-02 | 2 |
| AT-Q-20 | Denied cases for `alice` with no allowed case for `alice` | `proctor validate` reports EXE-06 naming the identity and exits 3; nothing is reported for `anonymous` | EXE-06 | 2 |
| AT-Q-21 | The application returns `"owner_id": "u-7"`, and the binding holds `u-7` | Equal; `PASS`. With `"owner_id": 7` against `"7"`: `FAIL` (type-strict; see inconsistencies, item 8) | HTTP-03 | 2 |
| AT-Q-22 | The binding sets `user_agent: example-client/1.0`; `/probe/headers` records headers | The User-Agent received equals `example-client/1.0` | HTTP-04 | 2 |
| AT-Q-23 | Each identity type against `/probe/headers` and a test token endpoint | `none`: no `Authorization`. `bearer`: `Bearer <token>`. `basic`: the Basic credentials. `oauth2_client_credentials`: a token from the test endpoint | AUTH-01 | 2 |
| AT-Q-24 | Two consecutive runs | Different `${run.id}` values in the reports; both runs pass against the same environment | FIX-05 | 3 |
| AT-Q-25 | A spec contains a SQL setup step type | Exit 3 (no seeding in v1) | FIX-02, FIX-03 | 2 |
| AT-Q-26 | A spec asserts a Temporal activity | Exit 3 | TMP-06 | 5 |
| AT-Q-27 | A spec uses status `cancelled` | Exit 3; the valid name is `canceled` | TMP-04 | 5 |
| AT-Q-28 | Each lookup mode (derived ID, search attributes with `eventually`, captured ID) on a completed export | Each: `PASS`; the report shows the first run ID and the run IDs | TMP-01, TMP-03 | 5 |
| AT-Q-29 | A spec contains an `exec`, `script`, `command` or `plugin` key; a `lib/` directory with executable files sits under the protected root | The spec: exit 3 before any request. The `lib/` files: never read or run | SEC-06 | 0 |
| AT-Q-30 | A request to a host that isn't the target, a connection or a token endpoint | The step is `ERROR`; no request reaches that host | SEC-07 | 2 |
| AT-Q-31 | A request to an endpoint that accepts TCP and never answers | The step is `ERROR` within the configured or default timeout. The PRD fixes no default value | SEC-05 | 2 |
| AT-Q-32 | A response body of 10 KB | The report's excerpt is at most `report.excerpt_bytes` | REP-05 | 6 |
| AT-Q-33 | Snapshot of the JSON report schema | A golden-schema test fails when a field from REP-08 is renamed or removed | REP-09 | 2 |
| AT-Q-34 | The report's hashes after AT-O3 | Hashes for the manifest, each spec, `proctor.yaml`, the binding, `compose.verify.yaml` and each file it references equal SHA-256 of their blobs | INT-16 | 0 |
| AT-Q-35 | Where the plan allows, after a run | `gh attestation verify` on the report artifact succeeds, naming the tested commit | REP-02 | 6 |
| AT-Q-36 | Each time construct, passing and failing (`eventually` completes @+3 s; `consistently` holds; `never` sees nothing) | `PASS` for each; with the condition broken, `FAIL` at the first sample that shows it | TIME-01, TIME-02, TIME-03 | 4 |
| AT-Q-37 | Request paths `https://other.example/x`, `//other.example/x`, `/a/../b`, `/a%2Fb`, `a/b` and `/x?debug=1`, each in its own spec; then `/projects/p1` | Each of the first six: exit 3, and no request reaches any host. The last: the request reaches the target at `/projects/p1` | HTTP-05 | 0 |
| AT-Q-39 | A spec sets each of `Host`, `User-Agent`, `Authorization`, `Proxy-Authorization`, `Cookie`, `Content-Length`, `Transfer-Encoding`, `Connection` | Each: exit 3, and no request reaches any host | HTTP-06 | 0 |
| AT-Q-38 | A full local run with outbound connections recorded at the network boundary | Connections go only to the target, the configured connections and the token endpoints; an authoritative run adds only the GitHub API host | SEC-07 | 2 |
| AT-Q-40 | `json` expects `{x: 1}` for a body object `{"x": 1, "y": 2}`; `keys` lists `[x]` for the same object | Each: exit 1 | HTTP-07 | 0 |
| AT-Q-41 | `absent: [$.b]` where `b` is `null`; `present: [$.b]`; `absent_headers` naming a header the response sends | Exit 1, exit 0, exit 1 | HTTP-08 | 0 |
| AT-Q-42 | `length: { $.list: "${n}" }` with `n` saved as 2 from a database check, against a two-element list and a three-element list | Exit 0, then exit 1 | HTTP-09, DB-08 | 0 |
| AT-Q-43 | Response bodies `{"a": 1, "a": 2}`, `{"a": 2} {"a": 2}` and `{"a": 2} x`, with `$.a: 2` expected | Each: exit 1 | HTTP-10 | 0 |
| AT-Q-44 | A POST verification step with no later GET or database check; the same with a later GET expecting 401; the same with `no_state_check: <reason>` | Exit 3, exit 3, exit 0; the semantic diff lists the waiver | EXE-07, §7.5 | 0 |
| AT-Q-45 | A DELETE used only in setup | Exit 3, naming the operation | FIX-08 | 0 |
| AT-Q-46 | A setup step answered with 500; with the target down; with a response slower than the request timeout | Each: scenario ERROR, reason `setup_failed`, cause `unexpected_response`, `unreachable`, `timeout` | FIX-04 | 0 |
| AT-Q-47 | Two scenarios create `item ${run.id}` in two runs | Four different names, each `<12 hex digits>-<scenario ID>` | FIX-05 | 0 |
| AT-Q-48 | A `credential: expired` request expecting 200; a variant whose token equals the identity's; an `accepted_variants` request expecting 200 with a `header_env` value | Exit 3; exit 3 with AUTH-03 and no request sent; exit 0 with the header value sent as given | AUTH-06, AUTH-03 | 0 |
| AT-Q-49 | `${secret.pw}` in a request body, the application echoing it and the bearer token in its response; `${secret.pw}` in a path or an expectation | The secret is sent, the report shows `[redacted]` and neither value; exit 3 | AUTH-07, AUTH-02 | 0 |
| AT-Q-50 | Database checks: a syntax error, `UPDATE ... RETURNING`, `nextval`, a `'; DROP TABLE ...` argument, `pg_sleep` beyond the query timeout, two columns with one name, a `bytea` column, 10,001 rows | ERROR (exit 2) for each but the argument, which is data (exit as the expectation says); the table unchanged after each | DB-01, DB-02, DB-05, DB-08 | 4 |
| AT-Q-51 | A query returning a column `first_row` doesn't name; `"1"` expected for an integer column | Exit 1 each | DB-08 | 4 |
| AT-Q-52 | The verification role holds, in turn: column `INSERT`, `UPDATE`, `DELETE`, `TRUNCATE`, `TRIGGER`, `SELECT` on an undeclared table, sequence `USAGE`, `CREATE` on a schema, membership of `pg_read_all_data`, `CREATEROLE`, `BYPASSRLS`, ownership of a table; then superuser; then a declared object is missing | Each: exit 4 before any scenario, every scenario NOT_RUN with `trust_violation` | DB-04, DB-04a, DB-07 | 4 |
| AT-Q-53 | No schemas declared and a query naming an unqualified table | ERROR, cause `query_error` | DB-03 | 4 |
| AT-Q-54 | Injected faults: connection closed, reset mid-body, short body, broken chunking, non-HTTP reply, invalid gzip, a stalled body, 11 MiB, HTML, a redirect, 503 | None gives PASS; transport faults are exit 2, the rest exit 1 | §15, SEC-05, HTTP-01 | 0 |
| AT-Q-55 | Each specification weakening in `mutation-testing/harness.py` (SM-01 to SM-12) | Validation rejects it (exit 3) or the semantic diff lists it as a weakening | §7.5, EXE-03, EXE-07 | 4 |
| AT-Q-56 | A value sent and matched later without `${run.id}`; a `length` on a GET with no query parameters; each with `waive: { <rule>: <reason> }`; a waiver with an empty reason, for EXE-07, or on a step the rule doesn't apply to; a `no_state_check` on a step a later GET checks | Exit 3 naming FIX-05 or HTTP-09, and `run` sends no request; exit 0 with the waivers, which the semantic diff lists as `rule_waived` weakenings; exit 3 for each bad waiver; no output line says WARNING | EXE-08, FIX-05, HTTP-09, EXE-07 | 0 |

## AT-REQ: Coverage

A test is listed against a requirement only if it would fail when the requirement is missing.

| Requirement | Tests | Phase |
|---|---|---|
| MODE-01 | AT-I-34, AT-I-35, AT-W-15, AT-W-16 | 0 |
| MODE-02 | AT-I-39 | 0 |
| MODE-03 | AT-W-11, AT-W-13 | 0 |
| MODE-04 | AT-W-14 | 0 |
| MODE-05 | AT-I-44. The JSON-output part isn't testable as written (inconsistencies, item 3) | 0 |
| INT-01 | AT-I-01, AT-I-02, AT-I-43 | 0 |
| INT-02 | AT-I-06, AT-I-07 | 0 |
| INT-03 | AT-I-16, AT-I-17, AT-R-02, AT-R-04, AT-R-05, AT-T-52 | 0 |
| INT-04 | AT-I-01, AT-I-18, AT-T-35 | 0 |
| INT-05 | AT-I-03, AT-I-19 | 0 |
| INT-06 | Removed; no test | — |
| INT-07 | AT-I-39 | 0 |
| INT-08 | AT-I-04, AT-I-05, AT-I-38 | 0 |
| INT-09 | AT-I-38, AT-W-12 | 0 |
| INT-10 | AT-I-03, AT-I-08, AT-I-37, AT-R-18 | 0 |
| INT-11 | AT-I-20, AT-I-21, AT-I-22 | 0 |
| INT-12 | AT-I-33 | 0 |
| INT-13 | AT-I-23 to AT-I-28 | 0 |
| INT-14 | AT-I-29, AT-I-30 | 0 |
| INT-15 | AT-I-31, AT-I-32 | 0 |
| INT-16 | AT-I-28, AT-Q-34 | 0 |
| EVD-01 | AT-I-40, AT-I-41, AT-I-42 | 5 |
| EVD-02 | AT-I-15 | 6 |
| EXE-01 | AT-Q-19 | 2 |
| EXE-02 | AT-Q-19 | 2 |
| EXE-03 | AT-R-19 | 2 |
| EXE-04 | AT-Q-03 | 2 |
| EXE-05 | **Not testable mechanically:** whether a scenario depends on state from another is a reviewer's judgment. Partial signal: run each scenario alone and compare with the full run | — |
| EXE-06 | AT-Q-20 | 2 |
| EXE-07 | AT-Q-44, AT-Q-56 | 0 |
| EXE-08 | AT-Q-56 | 0 |
| HTTP-01 | AT-Q-02 | 2 |
| HTTP-02 | AT-Q-01 | 2 |
| HTTP-03 | AT-Q-21 | 2 |
| HTTP-04 | AT-Q-22. Behavior without a binding value is unspecified (inconsistencies, item 17) | 2 |
| HTTP-05 | AT-Q-37 | 0 |
| HTTP-06 | AT-Q-39 | 0 |
| HTTP-07 | AT-Q-40 | 0 |
| HTTP-08 | AT-Q-41 | 0 |
| HTTP-09 | AT-Q-42, AT-Q-56 | 0 |
| HTTP-10 | AT-Q-43 | 0 |
| TBL-01 | AT-Q-18, AT-I-07 | 2 |
| TBL-02 | AT-Q-18 | 2 |
| TBL-03 | AT-Q-16 | 2 |
| TBL-04 | AT-Q-17, AT-I-13 | 2 |
| TBL-05 | AT-Q-17 | 2 |
| TBL-06 | AT-R-02, AT-R-03 | 2 |
| AUTH-01 | AT-Q-23 | 2 |
| AUTH-02 | AT-Q-04 | 6 |
| AUTH-03 | AT-R-13 to AT-R-16 | 2 |
| AUTH-04 | AT-R-17 | 2 |
| AUTH-05 | AT-R-17 (variant: `user_id_type: integer` with a non-numeric value gives exit 3; with a numeric value, `$.owner_id` integer comparison passes) | 2 |
| AUTH-06 | AT-Q-48 | 0 |
| AUTH-07 | AT-Q-49 | 0 |
| FIX-01 | AT-W-19 (forks); otherwise a reference-setup review item | 3 |
| FIX-02 | AT-Q-25 | 2 |
| FIX-03 | AT-Q-25, AT-Q-06 | 4 |
| FIX-04 | AT-R-01, AT-T-20, AT-Q-46 | 2 |
| FIX-05 | AT-Q-24, AT-Q-47, AT-Q-56 | 3 |
| FIX-06 | **Configuration review item:** a black-box test can't tell a disposable environment from a reused one. Partial signal: AT-Q-24's second run starts with an empty `app.projects` | 3 |
| FIX-07 | AT-T-34, AT-T-51, AT-T-52, AT-T-53, AT-R-11 | 3 |
| LOG-01 | AT-Q-15 | 5 |
| LOG-02 | AT-Q-10, AT-T-42, AT-T-43 | 5 |
| LOG-03 | AT-Q-13 | 5 |
| LOG-04 | AT-Q-14, AT-T-34 | 5 |
| LOG-05 | AT-I-40 (the Level 3 label in the report) | 5 |
| LOG-06 | AT-T-19, AT-T-39, AT-T-40 | 5 |
| LOG-07 | AT-Q-12 | 5 |
| LOG-08 | AT-T-37 | 5 |
| DB-01 | AT-I-36, AT-Q-05 | 4 |
| DB-02 | AT-T-14, AT-Q-07 | 4 |
| DB-03 | AT-Q-08 | 4 |
| DB-04 | AT-Q-06, AT-Q-52 | 4 |
| DB-04a | AT-Q-52 | 4 |
| DB-05 | AT-T-29, AT-T-30 | 4 |
| DB-06 | AT-Q-11 | 4 |
| DB-07 | AT-Q-07 (view label). "May read only declared objects" isn't testable as written (inconsistencies, item 9) | 4 |
| DB-08 | AT-Q-42, AT-Q-50, AT-Q-51 | 4 |
| TMP-01 | AT-Q-28 | 5 |
| TMP-02 | AT-T-46, AT-T-47 | 5 |
| TMP-03 | AT-T-46, AT-Q-28 | 5 |
| TMP-04 | AT-T-54, AT-T-55, AT-T-56, AT-Q-27 | 5 |
| TMP-05 | AT-I-41 | 5 |
| TMP-06 | AT-Q-26 | 5 |
| TMP-07 | AT-T-44, AT-T-45, AT-T-48, AT-T-49 | 5 |
| TMP-08 | AT-T-38 | 5 |
| TIME-01 | AT-Q-36, AT-T-28 | 4 |
| TIME-02 | AT-Q-36, AT-T-61 | 4 |
| TIME-03 | AT-Q-36, AT-T-40 | 5 |
| TIME-04 | AT-T-15, AT-T-18 (field names provisional) | 4 |
| TIME-05 | AT-T-27, AT-T-59, AT-T-60 (the notice) | 4 |
| TIME-06 | AT-Q-09, AT-T-13 | 4 |
| TIME-07 | AT-T-01 to AT-T-10 | 4 |
| TIME-08 | AT-T-11, AT-T-12, AT-T-16, AT-T-17, AT-T-25, AT-T-26 | 4 |
| TIME-09 | AT-T-18 | 4 |
| TIME-10 | AT-T-23, AT-T-28 to AT-T-33 | 4 |
| TIME-11 | AT-T-21, AT-T-22 | 4 |
| TIME-12 | AT-T-50 | 4 |
| REP-01 | AT-W-17, AT-W-18 | 0 |
| REP-02 | AT-Q-35 | 6 |
| REP-03 | AT-W-08, AT-W-17 | 0 |
| REP-04 | AT-Q-04 | 6 |
| REP-05 | AT-Q-04, AT-Q-32 | 6 |
| REP-06 | AT-Q-04 | 6 |
| REP-07 | AT-R-06, AT-R-07, AT-R-09 | 2 |
| REP-08 | AT-R-06, AT-R-18, AT-R-20 | 2 |
| REP-09 | AT-Q-33 | 2 |
| REP-10 | AT-W-04 | 0 |
| REP-11 | AT-I-44. How the running image's digest is obtained is unspecified (inconsistencies, item 4) | 0 |
| REP-12 | AT-R-12 | 2 |
| REP-13 | AT-R-10, AT-R-21, AT-W-18 | 2 |
| SEC-01 | AT-Q-04 | 6 |
| SEC-02 | AT-R-13 | 2 |
| SEC-03 | AT-Q-06, AT-Q-07 | 4 |
| SEC-04 | AT-Q-05 | 4 |
| SEC-05 | AT-Q-31 (no default value fixed) | 2 |
| SEC-06 | AT-Q-29 | 0 |
| SEC-07 | AT-Q-02, AT-Q-30, AT-Q-38 | 2 |
| SEC-08 | AT-T-29, AT-R-11 | 4 |
| SEC-09 | AT-Q-04 | 6 |
| SEC-10 | **Configuration review item:** no black-box observable shows the absence of production credentials | 0 |
| SEC-11 | AT-W-02, AT-W-03 | 0 |
| SEC-12 | AT-W-01 | 0 |
| SEC-13 | **Review item:** each release's dependency list has a checked license, and its notices ship with the release; no black-box test shows code wasn't copied | 0 |
| ARC-01 | **Not black-box testable:** code-structure requirement; check in code review | — |
| ARC-02 | **Not black-box testable:** code-structure requirement; check in code review | — |

Requirements with no black-box test: EXE-05, FIX-06, SEC-10, SEC-13, ARC-01, ARC-02 (and INT-06, removed). Partly untestable as written: MODE-05, HTTP-04, DB-07, REP-11, SEC-05.

## PRD inconsistencies found while writing these tests

The author resolved all 18 in `prd-revised.md` revision 2 after this file was drafted. Tests that marked a value or rule as unspecified because of one of these now follow the PRD text named here.

| # | Gap | Resolved by |
|---|---|---|
| 1 | `--binding` could point into the checkout | MODE-01: a binding inside the checkout is exit 3 |
| 2 | `trust.workflow_changed` under OD-01 A | MODE-04: `not_applicable` under A; `job_workflow_sha` recorded |
| 3 | JSON output vs `report.formats` | MODE-05: authoritative mode always writes JSON |
| 4 | Source of the running image digest | REP-11: the workflow passes both digests in the binding |
| 5 | API permissions for INT-11 and REP-10 | §16.2 item 5: `contents: read`, `actions: read` |
| 6 | TMP-04 early FAIL vs TIME-01/TIME-10 | TMP-04: no early FAIL; samples until the timeout |
| 7 | Late samples and skipped due times | TIME-08: passed due times are skipped, one sample starts at once |
| 8 | String identity attributes vs type-strict comparison | AUTH-05: binding may declare `user_id_type`; converted at load |
| 9 | DB-07 enforcement | DB-04 and DB-07: enforced through grants, not SQL parsing |
| 10 | Unknown keys | INT-15: undefined keys fail validation (exit 3) |
| 11 | `../` and absolute manifest paths | INT-01: forbidden; integrity violation |
| 12 | Readiness for non-Temporal workers | FIX-07: `readiness.checks` paths |
| 13 | `never` correlation key source | LOG-08: must come from the triggering step (exit 3) |
| 14 | `setup_from` vs file order | TBL-04: must reference an earlier table (exit 3) |
| 15 | NOT_RUN reason after failed setup | FIX-04: `setup_failed` |
| 16 | REP-08 field names | REP-09: provenance names fixed in Phase 0, result names in Phase 2; provisional names here until then |
| 17 | Missing `user_agent` | HTTP-04: exit 3 |
| 18 | `after:` naming a missing or later step | TIME-09: exit 3 |
