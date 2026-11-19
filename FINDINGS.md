# Findings from the Software Factory adoption

2026-11-17. Found while designing how Software Factory's Go API (an RPC-style API whose every route but one is a POST) adopts Proctor, and by reading notification-service's pilot.

Checked against Proctor `drivers/foundation` at `457452c` (the installed `~/go/bin/proctor` is `0.0.0-1931dd2`) and notification-service `proctor-pilot` at `531abe7`. "Verified by validation" means `~/go/bin/proctor validate -r .` was run on a throwaway repository under `/tmp` holding the specification quoted; "verified by reading" means the code was read and the behavior follows from it, but nothing was run against an application. Nothing here was run with `proctor run` yet.

Already listed in `STATUS.md` "Known gaps" and not repeated: the time-window attribution of the app-log check, `msg`-only log matching, the unprotected `app_logs.file`, the DynamoDB driver's unproven read-only credential, `not run` counting scenarios, the out-of-date `README.md` and `prd.md` about tables, drivers and the app-log check, and notification-service's security-alert flag, rate-limit and WebSocket-origin gaps. P-14 adds new evidence to the documentation item.

To add a finding, append it under its section with the next free ID, in the same fields: summary, severity (bug, edge case or doc gap), evidence (`path:line` and a short quote), failure scenario, suggested fix, and how it was verified (or "unverified").

## Proctor

### P-01: The http driver decides act or observe from the method alone, so an API whose reads are POSTs gets no state checks, no polling and no destructive-target guard

- **Severity:** edge case (a design limit with wide effect).
- **Evidence:** `drivers/http/cmd/proctor-driver-http/validate.go:69-73`: `if observes(method) { v.r.Kind = protocol.KindObserve }` and `v.r.Destructive = destructive(method)`; `rules.go:28-34`: `observes` is GET, HEAD, OPTIONS and `destructive` PUT, PATCH, DELETE. `internal/integrity/stepinfo.go:162-163`: `eventually, consistently and never are only for steps that observe; this step acts` (TIME-11, by design per `prd.md:743` and AT-T-21). HTTP-09 fires only for GET (`validate.go:127`).
- **Failure scenario:** Software Factory reads its state with `POST /api/projects`, `/api/audit`, `/api/gate`, `/api/changes` and `/api/keep {op: read}`. Every one is an act to Proctor, so (1) no read can be the later state check EXE-07 wants, and every act and every read needs a `no_state_check` waiver (about 1,000 in the planned adoption, each a weakening in `proctor diff`); (2) `eventually` on `POST /api/audit` is refused (verified by validation: `INVALID TIME-11 ... eventually-post`), so no asynchronous outcome (a push decided in the background, a clone finishing) can be waited for; (3) EXE-09 never guards `POST /api/remove-repo` or `/api/remove-session`, which destroy data; (4) HTTP-09 never applies to a list read by POST. The same holds for JSON-RPC and GraphQL APIs.
- **Suggested fix:** let `proctor.yaml` declare the exceptions in the http driver's policy, which code owners review, for example `drivers: { http: { official: http, policy: { observe: [{ method: POST, path: /api/audit }], destructive: [{ method: POST, path: /api/remove-repo }] } } }`; the driver reports those requests as `observe` (and `Destructive`), and refuses `eventually` on anything not declared. Keep the method rule as the default.
- **Verified:** by validation (TIME-11) and by reading.
- **Status (2026-11-17):** addressed by an opt-in `reads` list in the http driver's policy (`drivers: { http: { official: http, policy: { reads: [{ method: POST, path: /api/gate }] } } }`, rule HTTP-11), uncommitted on `drivers/foundation`. A listed POST is an observe step: it can be EXE-07's state check, `eventually` may repeat it, and HTTP-09 applies. With no list nothing changes: notification-service validates the same and its full pilot passed 65 of 65 on the new build. `proctor diff` reports each added entry as a `read_declared` weakening. The `destructive` half of the suggested fix wasn't built. A route whose body decides read or write (`POST /api/keep` with `op: read` or `restore`) can't be listed, since entries match method and literal path only.
- **Status (2026-11-18, later):** the destructive half is built (PK-04, REQ-10): `drivers.<http>.policy.destructive: [{ method: POST, path }]` makes a listed POST destructive, with its path and body as its target, so EXE-09 guards Software Factory's `/api/remove-repo` and `/api/remove-session`; `proctor diff` lists an added entry as `destructive_declared` and a removed one as a `destructive_removed` weakening.

### P-02: No driver reads a streaming HTTP response (NDJSON or server-sent events)

- **Severity:** edge case (missing capability).
- **Evidence:** `drivers/http/cmd/proctor-driver-http/call.go:67`: `body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))`, under the call's deadline (`protocol/serve.go:209-211`). The ws driver speaks only the WebSocket handshake (`drivers/ws/driver.go:20-22`, operations `connect`, `send`, `messages`).
- **Failure scenario:** `GET /api/events` on Software Factory answers `200 application/x-ndjson` and keeps the response open, sending a frame per change and a newline every 15 seconds. The http driver waits for the end of the body until `--call-timeout`, then reports the step as ERROR with cause timeout, so the stream's contents (every session with its review and push) can't be checked, and neither can anything visible only there (the agent's reply to a message).
- **Suggested fix:** two operations modelled on the ws driver: `http.open` (act, opens a handle, returns status and headers once they arrive) and `http.frames` (observe, uses the handle, returns the newline-delimited JSON values received so far, with `count`, `closed` and `overflow`), so `eventually` can sample it.
- **Verified:** by reading.
- **Status (2026-11-18):** fixed, uncommitted (PK-12). `http.open` (acts, GET only, returns at the status and headers and opens a handle; NDJSON or SSE from `format` or the Content-Type) and `http.frames` (observes the handle: every NDJSON line or SSE event so far, with `count`, `closed`, `close_reason`, `overflow`, `overflow_reason` and the raw `body_text`) read `/api/events`-shaped streams; the first 10,000 frames, 4 MiB and 1 MiB per frame are kept, and the core fails `none`, `count` and the other list claims over an overflowed list, ws's included (HTTP-15). TIME-14 refuses `eventually` with only claims that hold before anything arrives. Verified on a probe in /tmp/pk12-app (AT-Q-93 to AT-Q-95).

### P-03: EXE-07 counts any GET whose expected status isn't 401 or 403 as the state check, including 405, 404, 400 and an unrelated health probe

- **Severity:** bug (a misleading pass of the integrity rule).
- **Evidence:** `drivers/http/cmd/proctor-driver-http/validate.go:236-241`: `return !valid || (code != 401 && code != 403)`; `internal/integrity/stepinfo.go:197-201` accepts any later step for which that holds, on any driver and path. `prd.md:448` defines EXE-07 as "a check of the state it was meant to change or leave alone".
- **Failure scenario:** both of these pass validation (exit 0, verified): a `POST /api/save-token` expecting 401 followed by `GET /api/projects` expecting 405 `{error: method}`; and `POST /api/remove-repo` expecting 200 followed by `GET /healthz` expecting `{status: ok}`. Neither later step reads anything the POST changed, so a POST that silently changes nothing, or changes the wrong thing, passes. An author who wants to avoid a `no_state_check` waiver (which `proctor diff` reports) has an easy way round it that no reviewer is told about.
- **Suggested fix:** count as a state check only an observe step on the same driver whose expected status describes the resource (2xx, 404, 410), not one that describes the request (400, 405, 406, 413, 415, 421, 429, 5xx), and never the driver's `ready_path`. Better still, require the state check to name the act's target: the same path template, or a path declared as reading it (P-01's policy).
- **Verified:** by validation.
- **Status (2026-11-18):** fixed by PK-03, uncommitted. EXE-07 counts a later observe step only when it reads state (for http an allowlist: 2xx, 3xx, 404, 409, 410, 412, 423; never OPTIONS) and reads the act's target: the same path template (`${…}` as `{}`), a path above or below it by whole segments, or a path its policy entry `shows`, on the same driver or one sharing its policy (`policy_from`). Reads on drivers that report no targets still count (P-28). Both cases exit 3 naming the later step and why.

### P-04: A GET with an accepted credential variant counts as a state check even when it expects 401 or 403

- **Severity:** bug.
- **Evidence:** `internal/integrity/stepinfo.go:201`: `return d.info.StateCheck || acceptedVariant(d.at.step, c)`. The driver already returns `StateCheck: false` for a 401 or 403 (`validate.go:120-123`), and the `||` overrides it for any accepted variant.
- **Failure scenario:** passes validation (verified): `PUT /things/${run.id}` expecting 200, then `GET /things/${run.id}` with `variant: lowercase_scheme` (an `accepted_variants` entry) expecting `403 {error: denied}`. The write is never read back, contrary to EXE-07's "a GET whose expected status isn't 401 or 403" and AT-Q-44.
- **Suggested fix:** `return d.info.StateCheck` for accepted variants too; the variant only lifts the refused-variant exclusion.
- **Verified:** by validation.
- **Status (2026-11-18):** fixed by PK-03. The http driver decides `state_check` from the expected status alone, and an accepted variant no longer overrides it; the case exits 3.

### P-05: EXE-09 treats a path built from an identity value as a target the scenario controls

- **Severity:** bug.
- **Evidence:** `drivers/http/cmd/proctor-driver-http/validate.go:79`: `v.r.FixedTarget = !strings.Contains(path, "${")`. Identity values (`${alice.id}`) come from the binding and are the same in every scenario and every run (`internal/runner/runner.go:418-424`).
- **Failure scenario:** `PUT /things/fixed` as a scenario's first step is refused with EXE-09, but `PUT /things/${page.id}` is accepted (both verified by validation), although it overwrites the same record each time, which other scenarios and earlier runs also wrote. notification-service's preferences scenarios do exactly this (`PUT /v1/notifications/preferences/${alice.id}`); they are safe only because each resets the record in setup, which nothing enforces.
- **Suggested fix:** count a path as not fixed only when it holds `${run.id}` or a value saved earlier in the scenario; treat `${<identity>.<value>}` like a literal. EXE-09's own message already says "use a target with a saved value or ${run.id}".
- **Verified:** by validation.
- **Status (2026-11-18):** fixed by PK-04, uncommitted. A destructive act's target is fixed unless it refers to `${run.id}` or a value saved earlier in the scenario; identity values, secrets, `${fill(N)}`, literals and references outside the target (a header) keep it fixed. The http driver reports the target as written (`exact_targets`: the path, and for a POST its policy declares destructive, the path and body), and the core decides from the names actually saved. notification-service's 39 PUTs of `/v1/notifications/preferences/${alice.id}` in 15 scenarios now get EXE-09 (PK-19 adds a read or waiver to each scenario's first write).

### P-06: The app-log check reads only JSON lines with string levels, so a logfmt or numeric-level log passes without being checked

- **Severity:** bug (a silent pass).
- **Evidence:** `internal/applogs/applogs.go:147`: `if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "{") {` and `:152`: `level, ok := obj[r.LevelField].(string)`; any other line is skipped unless it starts `panic:` or `fatal error:`. `internal/runner/applogs.go:59-73` then passes a window in which no line was a candidate, however many lines it read.
- **Failure scenario:** Software Factory's engine logs with Go's `slog.NewTextHandler` (`time=... level=ERROR msg="..."`, `internal/adapters/loggers/slog/logger.go:32` there). With `app_logs` turned on, every scenario would pass the check while the engine logged errors. A Node.js service using pino (`"level":50`) gets the same result.
- **Suggested fix:** an `app_logs.format` (`json` or `logfmt`) in `proctor.yaml`, numeric levels mapped by a declared table, and an ERROR (not a pass) for a window that has lines but none that parse under the declared format.
- **Verified:** by reading.
- **Status (2026-11-18):** fixed, uncommitted on `drivers/foundation` (PK-01, feature `app_logs.logfmt`). `app_logs.format` (`json`, the default, or `logfmt` as `slog.TextHandler` writes it) and `level_numbers`. Every non-blank line that doesn't read under the format is ERROR `app_logs_unparsed` naming it (`first_unparsed`, `unparsed_count`), except a panic's stack trace; `app_logs.unparsed: allow` skips such lines, and `proctor diff` reports adding it as `app_log_unparsed_allowed`. A numeric level `level_numbers` doesn't map, or any numeric level without a table, is ERROR `app_logs_unmapped_level`. A window with a failing-level line and an unreadable one stays FAIL, its message naming both. EXE-10 validates `format`, `unparsed` and `level_numbers`. Software Factory's engine-log check is on (`levels: [ERROR, FATAL, PANIC]`). Verified: AT-Q-67..69, 117, 118 against a probe and a planted `logger.Error` that failed exactly SES-001 and FLD-T04R (implementer); Claude reran the build checks and Software Factory's `make proctor` on this build: 942 of 942, 60 app-log results, 1581 lines read and parsed, 0 unparsed, 0 unmapped.

### P-07: Two tables in one file with the same case ID collide, and the second's step description is used for the first

- **Severity:** bug (low: INT-02 already fails validation, but the other findings it causes are wrong).
- **Evidence:** a table row becomes a scenario whose ID is the case ID (`internal/integrity/spec.go:84`: `%s id %q is also used in %s`), and step descriptions are keyed by `model.StepRef{Spec, Scenario, Phase, Index}` (`internal/model/model.go:280-283`), so equal case IDs share one key.
- **Failure scenario:** three tables each with a row `["projects", "/api/projects"]`, two of them POST with `no_state_check` and one GET. Validation reports INT-02 once, plus `EXE-07 ... no_state_check is only for steps that act` for both POST tables, because they received the GET table's description (verified; renaming the case IDs uniquely removes both EXE-07 findings). An author fixes the wrong thing.
- **Suggested fix:** key table rows by table ID and case ID, or skip the step checks for scenarios INT-02 rejected.
- **Verified:** by validation.
- **Status (2026-11-18):** fixed in PK-05, uncommitted. A case or scenario ID repeated within one file gives only INT-02, naming the earlier table or scenario that holds it; the checks that use the drivers' step descriptions are skipped for every scenario with that ID and every table with a case of it, so no wrong EXE-07 or EXE-03 appears. A repeat in another file is still step-checked. AT-Q-81: the old build gave EXE-07 for both POST tables, the new build only INT-02.

### P-08: AUTH-03 doesn't refuse two identities with the same user ID, which AT-R-15 requires

- **Severity:** bug (against the acceptance test).
- **Evidence:** `prd.md:1786`, AT-R-15: "Alice and Bob bound to the same token; separately the same user ID | Exit 3". `internal/runner/runner.go:418-424` copies identity values without comparing them; only credentials are compared (`runner.go:434`: `owner[prefix+tok] != ""`).
- **Failure scenario:** a binding generator bug gives `bob` the same `values.id` as `alice`; every "bob can't read alice's data" scenario then tests a user against themselves and passes for the wrong reason.
- **Suggested fix:** refuse (exit 3) two identities whose `values.id` (or a value name `proctor.yaml` declares as the identity's key) are equal; or change AT-R-15 if values are meant to be free-form.
- **Verified:** by reading.
- **Status (2026-11-18):** fixed in PK-05, uncommitted (feature `auth03.distinct_ids`). `proctor run` refuses (AUTH-03, exit 3, nothing sent) two identities with the same `values.id` or the same `values.user_id`, naming both identities and the value name, never the value; compared under the same name only; it runs at `run` time, since `validate` has no binding. AT-R-15 passes against a local probe (0 requests, implementer). notification-service's `up.sh` gives every identity its own uuid4 id.

### P-09: `--report` isn't written when a local run fails before the specifications load

- **Severity:** edge case.
- **Evidence:** `internal/commands/run.go:59-83`: the timeout check (`exitInvalid`), the `--commit`/`--base`/`--pr-head` check, the missing `--binding` and `gitrepo.Open` failures all `return` before `emit`, which is the only writer of `--report` (`run.go:151-160`).
- **Failure scenario:** a wrapper always passes `--report "$state/report.json"` (notification-service's `run.sh:22`). A run started outside a Git repository, or with a bad flag, exits 3 and leaves the previous run's report in place, which then reads as this run's result (see N-02).
- **Suggested fix:** once `--report` is parsed, write a minimal report (`result: invalid`, the message) on every exit path, or remove the file first.
- **Verified:** by reading.
- **Status (2026-11-18):** fixed, uncommitted on `drivers/foundation` (PK-06, REP-14, feature `report.always`). Once flags are read, every exit of `proctor run` with `--report` writes the file: exits before the specifications load write `{"result":"invalid","exit_code":3,"mode":"local","errors":[…]}` in local mode, or the full authoritative report with the message under `errors`. The one exit without a report is `--authoritative` with no `--report`. Verified: AT-R-22 and each early exit by the implementer; Claude reran outside Git over an older file: exit 3, file replaced with `result: invalid`, mode 0600.

### P-10: The report writer follows a symbolic link at the report path

- **Severity:** edge case.
- **Evidence:** `internal/commands/run.go:167-172`: `os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)` then `f.Chmod(0o600)`.
- **Failure scenario:** with `--report /tmp/report.json` on a shared machine, a symbolic link planted at that path makes Proctor truncate and overwrite, and set to mode 0600, any file the user can write. notification-service avoids it only because its state folder is checked to be a private, non-linked folder.
- **Suggested fix:** write to a new temporary file in the same folder with `O_EXCL`, then rename it over the path; refuse an existing path that isn't a regular file.
- **Verified:** by reading.
- **Status (2026-11-18):** fixed, uncommitted (PK-06, plan 1.2). `writePrivate` (`internal/commands/privatefile.go`) refuses a path that isn't absent or a regular file, writes a 0600 `.proctor-*` temp file in the same folder, syncs it and renames it over the path; local and authoritative reports both use it (the authoritative one was 0644). Verified: AT-R-23; Claude reran it: exit 3, the link's target unchanged by hash, no temp file left. The job summary is still appended through a link: P-45.

### P-11: A request body can't be binary or over 16 MiB, and `Content-Length` can't be set

- **Severity:** edge case (missing capability).
- **Evidence:** `drivers/http/cmd/proctor-driver-http/call.go:117,125`: `body` is sent as JSON and `body_text` must be text; `internal/expr/expr.go:145`: `const MaxFill = 16 << 20`; `rules.go:100`: `content-length` can't be set (EXE-01).
- **Failure scenario:** Software Factory's `/api/upload` takes PNG, JPEG and GIF bodies and `/api/import` a gzip archive of at most 256 MiB, refusing a larger `Content-Length` with 413 before reading. None of these, nor the archive checks (`repos.json` naming `../../evil`), can be specified.
- **Suggested fix:** `body_base64` (or `body_file`, a path inside the protected folder, hashed into the report) and a byte-pattern fill that streams more than 16 MiB without holding it; keep `Content-Length` computed by the client.
- **Verified:** by reading.
- **Status (2026-11-18):** fixed, uncommitted (PK-11, HTTP-14, features `http.body_base64`, `http.body_fill`, `http.body_file`). One body per request: `body_base64` decoded at validation (at most 8 MiB), `body_fill: { bytes, pattern | pattern_base64 }` streamed with its true `Content-Length` up to `max_body_bytes` in the http driver's protected policy (default 16 MiB, at most 1 GiB), and `body_file`, a regular file inside `proctor/` with no symbolic link on its path, read and hashed by the core from the validated commit or working tree (`provenance.sha256`), re-checked before its step; the three need a `Content-Type`, and the evidence shows them as size and sha256. `proctor diff` lists `max_body_bytes_higher` and a `body_file` content change. Verified (AT-Q-90 to AT-Q-92, /tmp/pk11-app, implementer): base64, fill and file bodies arrive with the intended length, sha256 and `Content-Length`, never chunked; a 300 MiB fill gets the route's 413; a 1 GiB fill kept the driver near 12 MiB; 34 validation refusals (exit 3), and a JSON body over a lowered limit is ERROR when sent; a body file rewritten mid-run is ERROR with nothing sent; an authoritative run (against a stub GitHub API) sent the committed bytes of a file the working tree had changed and recorded its hash; a secret in the body and header is absent from the report; the diff shows the raised limit and the changed file. Software Factory hasn't adopted it yet (its upload and import checks).

### P-12: No pattern matcher and no "doesn't contain", so formats and absences in text can't be asserted

- **Severity:** edge case (missing capability).
- **Evidence:** `drivers/http/cmd/proctor-driver-http/validate.go:15-26`: the matchers are `equal`, `present`, `absent`, `keys`, `length`, `unordered`, `any`, `none`, `contains`, `count`; `none` takes object claims for list elements (`validate.go:183-196`).
- **Failure scenario:** Software Factory's checks "an `X-SF-Request` header of 16 hex digits, a new one on each request" and "`/api/diagnostics` text in which a grep for `/workspace`, `https://` and `@` finds nothing" can be written only as `present` and `contains`, so a request id of `x`, a repeated id, or a diagnostics file that leaks a path all pass.
- **Suggested fix:** `matches: { <path>: <anchored RE2> }`, `excludes: { <path>: <text> }` for strings, and `differs: { <path>: "${saved}" }`; list each removal or loosening as a weakening in `proctor diff`.
- **Verified:** by reading.
- **Status (2026-11-18):** fixed, uncommitted (PK-08, EXE-12, features `matchers.matches`, `matchers.excludes`, `matchers.differs`). `matches` (an RE2 pattern matched against the whole text, as `\A(?:…)\z`; refused at validate when invalid, over 1 KiB, matching empty text, or holding `${...}`), `excludes` (text or a list of texts; the value must be text containing none of them) and `differs` (present and not equal, as `equal` compares) are in the model and checked by the core for every driver (validate in `internal/integrity/spec.go`, fact paths in `factpaths.go`, judging in `internal/runner/match.go`); the http driver's strict `expect` decoding takes them. A missing path, and a value that isn't text for `matches` and `excludes`, fails all three. `proctor diff` reports a removal as `assertion_removed` and as `assertion_weakened` any changed pattern, an `excludes` text no remaining text rules out (or one dropped from the list), and any changed `differs` value. Verified against a scratch app (/tmp/pk08-app; AT-Q-84 to AT-Q-86): `[0-9a-f]{16}` passes a random id and its header and fails `x`, `5`, a missing path and a 4-digit partial pattern; `differs` passes a new id and fails a repeated one and a missing path; `excludes` passes clean diagnostics and fails `/workspace` at byte 26, a missing path and a number; validation refuses the bad patterns and lists; the loosened specs gave the five expected weakenings in `proctor diff` (implementer).

### P-13: The call timeout is one value for the whole run

- **Severity:** edge case.
- **Evidence:** `internal/commands/run.go:48`: `f.DurationVar(&o.callTimeout, "call-timeout", 10*time.Second, ...)`; `internal/runner/step.go:253-256` applies it to every call; a step has no timeout of its own (`internal/model/model.go`, `Step`).
- **Failure scenario:** Software Factory's `POST /api/dispatch` may take up to 105 seconds to start a sandboxed session, so the run needs `--call-timeout 120s`, and then a refusal check that hangs on any of ~950 other requests also waits two minutes before failing. The timeout also isn't in a protected file, so it isn't reviewed.
- **Suggested fix:** an optional `timeout:` on a step, bounded by a `time_limits.max_call` in `proctor.yaml`.
- **Verified:** by reading.
- **Status (2026-11-18):** fixed, uncommitted (PK-10, TIME-13, feature `step.timeout`). A step (setup steps and tables included) can set `timeout:`, which replaces `--call-timeout` for its calls and is the driver's deadline; `time_limits.max_call` in `proctor.yaml` (default 60 s, like the other limits) bounds it and `--call-timeout`. Validation refuses (exit 3) a step timeout over `max_call`, zero, negative or without a unit, and an invalid `max_call`; `run` refuses a `--call-timeout` over it before any driver starts ("Nothing was sent to the application"). `proctor diff` lists a raised `max_call` as `max_call_longer` and a step timeout added, raised or removed as `step_timeout_longer`; a lowered one is a changed expectation. Verified (AT-T-62, /tmp/pk10-app, implementer): a GET answering after 11 s ERRORs `timeout` after 10 s without a timeout and passes with `timeout: 15s` under `max_call: 20s`; 30s, 0s, -1s and `10` exit 3, 61s exits 3 without `max_call`; `--call-timeout 30s` exits 3; the diff gave the expected kinds. Software Factory's `run.sh` `--call-timeout 120s` is refused by this build until its `proctor.yaml` sets `max_call: 120s` or the dispatch step takes its own timeout.
- **Status (2026-11-18):** adopted in Software Factory (PK-SF-ADOPT). The `/api/dispatch` steps carry `timeout: 110s` under `time_limits.max_call: 110s`, run.sh no longer passes `--call-timeout`, and every other request uses the 10 s default. Verified: Claude checked the full run, 1598/1598 with exit 0.

### P-14: The documentation contradicts the code in places beyond those `STATUS.md` lists

- **Severity:** doc gap.
- **Evidence:**
  - `README.md:29-34,51`: the quick start's `proctor.yaml` has `version: 1` and `readiness:`, and its binding `target:`. Copied as written, `proctor validate` fails: `INVALID INT-15 proctor/proctor.yaml: line 6: unknown key "readiness" in the top level` (verified); the code wants version 2 (`internal/integrity/load.go:142-144`) and `drivers.http.{base_url,user_agent,ready_path}`.
  - `README.md:15` says time-based checks are "specified but not built", and `STATUS.md:69` lists them as parked; `internal/runner/step.go:29-214` builds `eventually`, `consistently` and `never`, validation checks them (`internal/integrity/spec.go:296-358`), and notification-service uses `eventually` 56 times.
  - `prd.md:607` (AUTH-06) names the field `credential: <variant>`; specifications use `variant:` (`internal/model/model.go`, `Step.Variant`).
  - `prd.md:449` (EXE-08) says only FIX-05 and HTTP-09 can be waived; `internal/integrity/spec.go:20` also allows EXE-09.
- **Failure scenario:** a new adopter following the README gets exit 3 on the first command; someone planning from STATUS.md designs around a missing `eventually` that exists.
- **Suggested fix:** replace the quick start with a version 2 example that validates, and correct the four statements.
- **Verified:** by validation (the quick start) and by reading.
- **Status (2026-11-19):** fixed by PK-18, uncommitted. README's quick start is version 2 (`drivers.http` in `proctor.yaml`, `drivers.http.base_url`, `user_agent` and `ready_path` in the binding), carries the specification it runs, and builds with `scripts/build.sh`, since `go build ./cmd/proctor` alone leaves no driver beside the program and `validate` then exits 2 (verified); its fenced blocks, extracted from the README unchanged by a script, validate with exit 0 in an empty `git init` folder (AT-Q-114). README:15's "time-based checks ... and tables are specified but not built" and STATUS.md's parked time checks are gone, and README's "Built so far" covers the whole findings program. AUTH-06 and AT-Q-48 say `variant: <variant>` (`model.Step.Variant`). EXE-08 already listed FIX-05, HTTP-09 and EXE-09, which is exactly `waivable` in `internal/integrity/spec.go:20`. Also fixed, each checked against the code: README's Specifications example was version 1 (`request:`, `status:`, `json:`) and is now a description plus an `eventually` step that validates; README's and prd §16.1's exit codes for `validate` were 0 and 3, but it also exits 2 when a driver can't be started and 4 for INT-01 (both verified). Found and left for the owner: (1) prd TIME-01, TIME-02 and TIME-07 call the windows' values `timeout`, `duration` and `interval`, while the code takes `within`, `for` and `every` and refuses the prd's names (INT-15, verified); (2) prd TIME-03 says `never` is for log records and Temporal executions and that HTTP and PostgreSQL state use `consistently`, while the code's `never` works on any observing step and requires that its expectations never all hold (`internal/runner/step.go`); (3) README links `prd.md` and lists it in the repository layout, but `.gitignore:5` ignores `/prd.md`, so a clone has none.

### P-15: EXE-09's guard accepts any earlier observing step, even one on another path

- **Severity:** bug (a misleading pass of the integrity rule).
- **Evidence:** `internal/integrity/stepinfo.go`, `checkRunScoped` (about lines 283-288): `for _, earlier := range ds[:i] { if earlier.observes() { unguarded = false`.
- **Failure scenario:** `GET /healthz`, then `DELETE /things/fixed`, then a GET expecting 404 validates with exit 0; without the health step the same scenario gets EXE-09. The health read says nothing about `/things/fixed`, so a scenario can delete a fixed target other scenarios or earlier runs wrote, guarded by nothing. A declared POST read (P-01's `reads` list) now also counts as such a guard.
- **Suggested fix:** count an earlier observe as the guard only when it's on the same driver and reads the same path template.
- **Verified:** by validation, with the build before the `reads` list.
- **Status (2026-11-18):** fixed by PK-04. EXE-09 counts an earlier step as the guard only when it is on the same driver or one sharing its policy and either observes with an expectation, no refused credential variant and an outcome that reads state, on the same target as written, a path above or below it, or a path its policy `shows`; or is an earlier destructive act on exactly the same target that EXE-09 accepted. `GET /healthz` before `DELETE /things/fixed` exits 3, and the message names each earlier read that didn't count and why.

### P-16: A 204 response can't meet EXE-03 through its body

- **Severity:** edge case.
- **Evidence:** `drivers/http/cmd/proctor-driver-http/validate.go`, `checkStatus`: "a verification step expecting %d must also assert a body field or header".
- **Failure scenario:** `DELETE` expecting 204, with nothing else asserted, gets EXE-03. A 204 has no body by definition, so the author must assert some header to pass, which pushes them toward asserting an incidental header (`content-length`, `date`) rather than anything meaningful.
- **Suggested fix:** for 204 and 304, accept `absent: { $.body: ... }` or an empty-body assertion, or require a later state check instead of a header.
- **Verified:** by validation.
- **Status (2026-11-18):** fixed, uncommitted (PK-10, EXE-03, feature `exe03.no_body`). A 2xx step with no body to assert, such as a 204, meets EXE-03 with `absent: ["$.body", "$.body_text"]` (an empty http fact has neither key, so the pair fails on any body), and the 204 message names it instead of "a body field or header". A step whose only assertion besides the status is `absent` of `$.body` alone or `$.body_text` alone is now refused (exit 3), since each passes a body of the other kind; the previous build accepted both. A 304, like every 3xx, still needs only its status. Status alone on a 204 is still refused. Verified (AT-Q-89, implementer): validation on the PK-08 and PK-10 builds side by side; a run where the pair passes on a 204 and fails against a 200 with a JSON body and a 200 with a text body. Go's HTTP client drops a body sent with a 204, so no 204 can show one.

### P-17: A `reads` entry no step uses isn't flagged

- **Severity:** edge case.
- **Evidence:** `drivers/http/cmd/proctor-driver-http/reads.go`, `parseReads`, accepts every well-formed entry; `proctor diff` reports only added entries.
- **Failure scenario:** a route is renamed from `/api/gate` to `/api/gates`; the old entry stays in `proctor.yaml` forever, and a later route reusing the old path silently becomes a read without the diff showing it as newly declared.
- **Suggested fix:** in `proctor validate`, refuse (or report) a `reads` entry that no step in the listed specifications matches.
- **Verified:** by reading.
- **Status (2026-11-18):** fixed by PK-07, uncommitted (feature `http.unused_entries`). `validate` and `run` refuse (HTTP-12, exit 3, nothing sent) an http policy entry no step uses, named as `read POST <path>`, `destructive POST <path>`, `shows POST <path> -> <act>` or `shows GET <path> -> <act>`. A `reads` or `destructive` entry is used when a step sends it, table rows included; a `shows` pair is used when, in one scenario, a read it applies to checks an act on the shown path (after the act, not necessarily as the first credited check, or before a destructive act as EXE-09's guard), with an expectation, no refused variant and an outcome that reads state. `policy_from` drivers share uses. Software Factory: every entry used; notification-service: none. Claude reran the build checks and both adopters' validation.

### P-18: Tables can't carry `eventually`

- **Severity:** edge case.
- **Evidence:** the INT-15 allowed keys for tables don't include `eventually`: validation says `unknown key "eventually" in tables`.
- **Failure scenario:** a table of declared POST reads (or of GETs) that check an asynchronous outcome per row can't poll, so each row must be written as a separate scenario.
- **Suggested fix:** allow `eventually`, `consistently` and `never` on a table's step, applied to each row.
- **Verified:** by validation.
- **Status (2026-11-18):** fixed, uncommitted (PK-10, TBL-08, feature `table.time`). A table takes `eventually`, `consistently`, `never` and `timeout`, copied into each row's step; validation is the scenario step's, with TIME-11 refusing each acting row by name and the values' mistakes reported once, at the first row. Verified (AT-T-63, implementer): `eventually` rows passed after 7 samples (a state appearing at 1.5 s); `consistently` and `never` passed the stable rows and failed the row whose value changed at 1 s, at sample 5; two constructs, an `every` over half the window, an 11-minute window and an unknown key each exit 3 once; GET and POST rows with `eventually` give TIME-11 for each POST row.

### P-19: Whether a build supports a feature such as `reads` can't be detected

- **Severity:** edge case.
- **Evidence:** `bin/proctor version` prints `0.0.0-457452c-dirty` for a build with the uncommitted `reads` list, the same commit a build without it names; the only signal is the old driver's refusal text, `drivers.http.policy: the driver takes no value here`.
- **Failure scenario:** Software Factory's `proctor/env/run.sh` must parse that message to tell the person their Proctor is too old; if the wording changes, the wrapper reports a confusing validation failure instead.
- **Suggested fix:** include a hash of a dirty tree in the version, or add `proctor version --features` listing each driver's rules and policy keys.
- **Verified:** by running both builds.
- **Status (2026-11-18):** fixed, uncommitted (PK-06, REP-15). A dirty build is `0.0.0-<sha>-dirty-<12 hex>`, the hex the SHA-256 of `git diff HEAD` plus untracked files' names, sizes and contents (untracked-only changes count as dirty); `proctor version --features` and `--json` list `internal/commands/features.go`'s 15 names. Verified: AT-Q-82 (implementer); Claude rebuilt from the same tree and got the same version, and read `--json`. Software Factory's `run.sh` switches to `--features` in PK-06's Software Factory part.

### P-20: `proctor validate` needs a Git repository even in local mode

- **Severity:** edge case.
- **Evidence:** in a plain folder, `proctor validate` prints `proctor: git rev-parse: fatal: not a git repository` and exits 3.
- **Failure scenario:** a wrapper can't probe what a build supports against a throwaway folder of specifications without `git init` first; neither can a person checking a specification outside a checkout.
- **Suggested fix:** in local mode, read the working tree without Git when there's no repository, and skip the integrity checks that need one, saying so.
- **Verified:** by running.
- **Status (2026-11-19):** fixed by PK-18, uncommitted (MODE-06, feature `validate.no_git`). `proctor validate` without `--commit`, when the `--repo` folder isn't in a Git repository, reads it as it is, prints `Note: not a Git repository: the folder was read as it is`, adds `"git": false` and the `note` to `--json`, and keeps its exit codes (0, 2, 3, 4). `gitrepo.Open` returns `NotRepositoryError` only when Git says "not a git repository" and neither the folder nor any folder above it has a `.git` entry, so a broken repository (a `.git` file naming a missing folder) is still refused, exit 3. The one Git fact `validate` uses, whether each mutation script and each file a mutant edits is tracked or untracked and not ignored (MUT-01), is refused rather than skipped: with `mutation` configured outside Git, each such file is an MUT-01 finding, exit 3, saying it can't be told outside Git. Refusing is the stricter choice: a skipped check would let a folder pass outside Git that fails inside it, and nothing else in local `validate` reads Git (the commit checks, symbolic links, unlisted files and CODEOWNERS, are `integrity`'s and authoritative mode's). `validate --commit`, `run` (a `--report` still gets its `invalid` report), `run --authoritative`, `diff`, `integrity`, `mutate`, `approve`, `logs normalize` and `mutants generate` refuse with exit 3 and `not a Git repository: <folder> (git rev-parse: …); this command reads Git, and only proctor validate without --commit reads a folder outside one`. Verified (AT-Q-115): the quick start's files in a plain folder exit 0 with the note and `"git": false`; an unknown step key exits 3 (INT-15) and a missing state check exits 3 (EXE-07), the driver's checks running there; each refusing command exits 3 with that message. Inside Git, the output of the four adopters' validation is unchanged, the JSON too.

### P-21: A later unrelated read takes EXE-07's credit, and then an honest waiver is refused

- **Severity:** bug (P-03 within one scenario).
- **Evidence:** Software Factory's `proctor/specs/sessions.yaml`, SES-001: `nothing-kept` (`POST /api/keep {op: read}`, an act because `/api/keep` also restores and can't be declared a read) is counted as state-checked by later `POST /api/gate` reads, which don't read the kept heads. Adding `no_state_check` is refused: `EXE-07 ... no_state_check waives a state check, but a later step already checks state; remove the waiver`.
- **Failure scenario:** the author can neither state the real reason nor point the check at the right read, and the report claims a state check that never happened.
- **Suggested fix:** as in P-03: count as the state check only a later observe that reads the act's target (the same path, or a declared read naming it).
- **Verified:** by validation.
- **Status (2026-11-18):** fixed in Proctor by PK-03: an unrelated later read no longer takes the credit, and the honest waiver is accepted unless a related read exists. In Software Factory, SES-001 `nothing-kept` is still credited by a Docker read (P-28), so its waiver waits on P-28.
- **Status (2026-11-19):** closed by PK-21 (P-28): a Docker read no longer counts as the state check of an http act without a `relations` entry, so SES-001 `nothing-kept`'s honest `no_state_check` now validates in Software Factory (exit 0).

### P-22: The `reads` list is per driver, so two http drivers on one application can drift apart

- **Severity:** edge case.
- **Evidence:** `drivers/http/cmd/proctor-driver-http/reads.go` keeps the list per driver instance; Software Factory runs a second `official: http` driver (`http_misdirected`, pointed at `localhost`) for the wrong-Host rows, since the http driver refuses a `Host` header (EXE-01).
- **Failure scenario:** a route added to one driver's `reads` and not the other's makes the second driver's rows acts; nothing in Proctor notices. Software Factory's `proctor/env/routes.sh` compares the two lists by hand.
- **Suggested fix:** let a policy be shared by drivers of the same official name, or warn when two drivers with the same `official` and the same application declare different lists; or let a step set a `Host` header within limits, so the second driver isn't needed.
- **Verified:** by reading.
- **Status (2026-11-18):** addressed by `drivers.<n>.policy_from: <driver>` (INT-18: same official program, no chain, no own `policy`); drivers sharing a policy are one EXE-07 group. Software Factory's `http_misdirected` uses it, and `routes.sh` no longer compares two lists.

### P-23: A misspelt path in `absent`, or a misspelt key in a `none` claim, passes validation and every run

- **Severity:** bug (a misleading pass).
- **Evidence:** `internal/runner/host.go:478-487` checks only `with` against the operation's `args_schema`; `fact_schema` is used only on the answer (`internal/runner/step.go:264-270`), never on the expectation. `internal/runner/match.go:154-173`: a `where` sub-key is parsed as a path and compared element by element, so a key no element has matches nothing.
- **Failure scenario:** a ses step with `expect: { absent: ["$.emailz"], none: { "$.emails": { subjekt: "x" } } }` validates (`RESULT: PASSED (exit 0)`, verified). At run time `$.emailz` is always absent and no element has `subjekt`, so both checks pass whatever was sent. A specification meant to show that a field is gone, or that no email with a subject went out, checks nothing, and a reviewer reading `absent`/`none` believes it does. The same holds for every driver, and for http bodies.
- **Suggested fix:** in `validateStep`, resolve every expectation path, `where` sub-key and `save` path against the operation's `fact_schema` (properties, then items for `[n]`), and refuse one the fact can't hold (exit 3). The docker driver's DKR-04 does this for its own operations until the core does.
- **Verified:** by validation (the ses case) and by reading.
- **Status (2026-11-18):** fixed for every driver by the core's EXE-11 (PK-02, uncommitted). `validate` walks each operation's `fact_schema` along every expectation path, claim key and `save` path, and refuses a name a closed object can't hold or a matcher on the wrong kind of value (exit 3). At run time, a `none` or `count … is: 0` claim whose key is in no element of a non-empty list fails, under `never` too. The docker driver's DKR-04 code is retired. A misspelt path under a free-form part (http `body`, DynamoDB items, PostgreSQL rows) still can't be found. Verified: the ses and docker negatives exit 3 naming EXE-11, a live http probe FAILs where the old build passed (implementer); Claude reran the build checks and both adopters' validation (Software Factory 10 specifications, 942 scenarios; notification-service 11, 69; exit 0).

### P-24: `proctor diff` says nothing about a driver a pull request adds, or the policy it gives it

- **Severity:** edge case.
- **Evidence:** `internal/protdiff/protdiff.go:357-388`: `compareDrivers` compares drivers present in both base and head; for a driver only in head it reports only the http `reads` it gained.
- **Failure scenario:** a head commit that adds `dynamodb: { official: dynamodb, policy: { tables: { everything: prod_users } } }` gives only `modified proctor/proctor.yaml approval required` and no line about the driver (verified); widening the same policy afterwards is a `driver_policy_changed` weakening (verified). So the first grant of access, usually the widest, is the one a reviewer isn't shown. With the docker driver that grant is read access to the person's Docker.
- **Suggested fix:** a `driver_added` line naming the driver, its official program and its canonical policy, listed with the protected changes (not as a weakening, since it adds checks); the docker plan builds it (A12).
- **Verified:** by running `proctor diff` on throwaway commits.
- **Status (2026-11-18):** addressed by `driver_added` (docker driver, Part A, uncommitted): `proctor diff` lists an added driver with its official program and policy among the other changes; widening or narrowing the docker scope is `docker_scope_widened` or `docker_scope_narrowed`.
- **Status (2026-11-19):** confirmed by PK-18: `compareDrivers` lists `driver_added` for every driver in the head's `proctor.yaml` that the base's lacks, whatever its official program; a throwaway diff adding `docker`, `sqs`, `ses`, `ws`, `postgres` and a second `http` with `policy_from` listed all six, each with its official program and policy. One case wasn't covered: when the base has no `proctor.yaml` (the pull request that adopts Proctor), the drivers weren't compared at all, so the first grant of access showed only as an added file. PK-18 generalised it: with no configuration at the base, every head driver is listed as `driver_added`, with the weakenings an added driver gets (`read_declared`, `state_check_declared`) and `destructive_declared`; verified on throwaway commits (8 drivers listed). A `--change-kind fix` there still exits 4, as before, since an added `proctor.yaml` already fails a fix.

### P-25: `driver_policy_changed` names the driver twice

- **Severity:** doc gap (cosmetic).
- **Evidence:** `internal/protdiff/protdiff.go:376-377` puts `driver %s: ` at the start of `Detail` while the weakening already carries `Driver: name`, and the printer shows both.
- **Failure scenario:** the output reads `driver_policy_changed driver dynamodb: driver dynamodb: policy {...} became {...}` (verified). Harmless, but it makes the one line a reviewer must read harder to scan.
- **Suggested fix:** drop the prefix from `Detail`, as `read_declared` does (`protdiff.go:408-409`).
- **Verified:** by running `proctor diff`.
- **Status (2026-11-18):** fixed by PK-03: `driver_policy_changed` and `driver_origin_changed` name the driver once; `policy_from` changes show both resolved policies; each added `shows` path is a `state_check_declared` weakening.
- **Status (2026-11-19):** confirmed by PK-18 on its build: widening a DynamoDB policy prints `driver_policy_changed driver dynamodb: policy {"tables":{"users":"prod_users"}} became {…}`, naming the driver once.

### P-26: JSONPath can't name a key with a dot, a slash or a leading digit, so such keys can't be asserted or saved

- **Severity:** edge case (missing capability).
- **Evidence:** `internal/expr/expr.go:23`: `nameRE = ^[A-Za-z_][A-Za-z0-9_-]*`; `expr.go:31-53` accepts only `.name` and `[index]`. `internal/runner/match.go:161,168-173`: `any`, `none` and `count` sub-keys go through the same parser.
- **Failure scenario:** a fact or response object keyed by Docker labels (`dev.software-factory.stack`), port names (`8080/tcp`), MIME types, file paths or numeric IDs can't be addressed: `$.labels.dev.software-factory.stack` parses as three nested names and selects nothing, so `equal` fails and `absent` passes (see P-23). A driver must reshape such maps into lists of `{key, value}` (the docker plan does, A4), and an HTTP API that returns such a map can only be matched as a whole object.
- **Suggested fix:** accept bracket-quoted names, `$['dev.software-factory.stack']` and `$["8080/tcp"]`, in `ParsePath`, in `where` sub-keys and in `save`, with the same escaping rules everywhere.
- **Verified:** by reading.
- **Status (2026-11-18):** fixed, uncommitted (PK-09, feature `paths.bracket_names`). Paths take quoted names (`['…']`, `["…"]`, escapes `\\`, `\'`, `\"`; no empty name, no control characters, at most 256 bytes) in expectations, claim and `where` keys, `save`, `redaction.json_paths` and driver paths; the grammar is `protocol/jsonpath`, shared by the core and the drivers. Every path is rewritten to one canonical spelling before any check, so HTTP-08, redaction, FIX-05, FIX-08 and `proctor diff` see `$['body']['id']` as `$.body.id`; one matcher naming a path in two spellings is EXE-13; claim keys are syntax-checked at validate. Claude reran the build checks and both adopters' validation (unchanged).

### P-27: The http driver cuts `body_text` at 64 KiB without saying so

- **Severity:** edge case. It would let text past the cut pass once a "doesn't contain" exists; today it causes confusing failures.
- **Evidence:** `drivers/http/cmd/proctor-driver-http/call.go:247` `f["body_text"] = cut(body, bodyTextBytes)`, with `bodyTextBytes = 64 << 10` (`main.go:22`). `cut` (`call.go:262-271`) returns the prefix, and nothing in the fact marks the cut. Bodies up to `maxBodyBytes` (10 MiB) are read.
- **Failure scenario:** a 200 KiB text answer leaks `/workspace` at byte 100,000. `equal` fails confusingly and `contains` past the cut fails, but P-12's suggested `excludes` would pass.
- **Suggested fix:** `body_truncated: true` in the fact, and every text assertion on a truncated `$.body_text` fails, naming the cut (PK-08).
- **Verified:** by reading.
- **Status (2026-11-18):** fixed, uncommitted (PK-08, HTTP-13, feature `http.body_truncated`). The http fact has `body_truncated` beside `body_text` (true when the body was longer than 64 KiB); the ws driver's refused-handshake `body_text`, cut at 1 KiB, now has it too (the other drivers don't cut text facts: ws frames are dropped with `overflow`, sqs and ses bodies are whole). `equal`, `contains`, `matches`, `excludes` and `differs` on a cut `body_text` fail with "`$.body_text` was cut at 65536 bytes (body_truncated: true)" and `cut_at` in the report, and `save` of it fails the step; `present` and `absent` still judge it. Verified (AT-Q-87): a 200,010-byte text with `/workspace` at byte 100,000 fails `excludes`, `contains` and `save`, each naming the cut; a short text has `body_truncated: false` and passes (implementer).

### P-28: EXE-07 accepts any later observing step on another driver as the state check

- **Severity:** edge case (P-03 across drivers).
- **Evidence:** `internal/integrity/stepinfo.go` `checkStateChecks`: `for _, later := range ds[i+1:] { if !later.at.setup && later.checksState(c) { checked = true`. It compares neither driver nor target, while `markCheckedSetup` in the same file requires the same driver.
- **Failure scenario:** a PUT of preferences followed by a peek at an unrelated SQS queue, or a read of an unrelated DynamoDB table, validates without `no_state_check`. After PK-03 this is the remaining way to claim a state check without reading the state.
- **Suggested fix:** store drivers report targets (table alias, queue alias, Docker object), and `proctor.yaml` relates them to acts (`shows` across drivers). A cross-driver check without such a relation is refused; adopt it in notification-service.
- **Verified:** by reading.
- **Status (2026-11-19):** fixed, uncommitted (PK-21, owner's REQ-03; EXE-07, new INT-20, feature `relations`). Every official driver reports targets (dynamodb the table alias, sqs the queue alias, ws `ws.connect`'s path template, ses `sent`, postgres the connection name, docker `<kind>/<name>` or the list operation's name, and `http.open` its path), and a step on a handle (`ws.send`, `ws.messages`, `http.frames`) takes its opener's. A later read on another driver group counts only through a `relations` entry in `proctor.yaml`, `{ act: { driver, target }, shows: [{ driver, target }] }`, matched by driver group and equal target; EXE-07's message names the relation that would count. INT-20 refuses (exit 3) a malformed relation, one naming a target no step reports, and a `shows` entry no act uses; `proctor diff` lists an added entry as `state_check_declared`. Acts on a driver without targets are no longer credited by any later read. Before the adopters changed, the new build gave notification-service 56 EXE-07 (27 http acts read through DynamoDB, 26 `sqs.send` read through SES, DynamoDB or another queue, 2 `dynamodb.put` read through http, 1 `ws.connect` read through DynamoDB) and Software Factory 1 (SES-001 `nothing-kept`). notification-service now declares 8 relations, each from its code; Software Factory's `nothing-kept` has its `no_state_check`. Both validate (exit 0).

### P-29: `ses.sent` with no `with` is refused, though the driver means it to work

- **Severity:** edge case.
- **Evidence:** `drivers/ses/driver.go:45` declares `ArgsSchema` as `type: object`, and the core checks it at `internal/runner/host.go:483`. The driver's own `argsOrEmpty` (`driver.go:193,227`) shows that leaving `with` out is meant to read every email.
- **Failure scenario:** a step `do: ses.sent` with no `with` fails validation with `with: with: expected object, got null`, exit 3. The doubled "with:" comes from `host.go:486`.
- **Suggested fix:** `type: [object, null]` in the ses schema, as the docker driver does, or the core treats an absent `with` as `{}`; and drop the doubled prefix.
- **Verified:** in a throwaway repository (docker driver implementer, 2026-11-18).
- **Status (2026-11-18):** fixed in the core (PK-02): an absent `with` is checked as `{}` when the operation's `args_schema` describes an object and doesn't accept null, and argument problems name their location once (`with: missing required key "method"`). `do: ses.sent` with no `with` validates.

### P-30: A schema problem in a driver's policy or binding can't name the driver's rule

- **Severity:** documentation gap.
- **Evidence:** the core checks a driver's declared `policy_schema` and `connection_schema` before Configure (`internal/runner/host.go:262-270`), with no rule ID, and strips `*_env` keys first (`host.go:103-138`).
- **Failure scenario:** a driver that wants DKR-01, DKR-02 or HTTP-11 named in the message, or a "takes no secrets" message for a `socket_env` key, must declare `true` and check everything in Configure; then Hello's schemas no longer describe the policy for any tool that reads them. The http `reads` list and the docker driver both do this.
- **Suggested fix:** let a schema problem carry a rule ID the driver names, or document that a driver needing named rules declares `true` and validates in Configure.
- **Verified:** by the docker driver's validation negatives and by reading.
- **Status (2026-11-19):** documented, no code change (PK-23a). PK-18 hadn't covered it: neither `prd.md` nor `README.md` mentioned `policy_schema`, `connection_schema` or validating in Configure. prd §19 now says that the core checks both schemas before `configure` with no rule ID (exit 3, `proctor.yaml: drivers.<name>.policy…` or `binding: drivers.<name>…`), that a driver whose messages must name its own rule declares the schema as `true` and checks the value in `configure`, that its `hello` schema then no longer describes the policy or binding, and that the http driver's policy and both docker schemas do this. Letting a schema problem carry a driver's rule ID wasn't built.

### P-31: After a failed act, the later observe steps still run and fail, burying the cause

- **Severity:** edge case (misleading report).
- **Evidence:** `internal/runner/runner.go:889-900`: `afterFailure` skips only acts (`NOT_RUN (earlier_step_failed)`); observe steps after them still run.
- **Failure scenario:** in Software Factory's planted `boxPids = 1024` defect, SES-001 failed first at `box-running` (`pids_limit` expected 2048, got 1024), then 10 more observe steps failed (`file-written`, `box-gone`, `stopped`, `network-gone`, `home-gone`, `gone` and others) only because the acts before them never ran. One cause, 11 FAIL lines.
- **Suggested fix:** mark an observe that follows a skipped act as running "after a skipped act" (or skip it), and print each scenario's first failure as its summary.
- **Verified:** by the planted-defect run (Part B implementer, 2026-11-18, `/tmp/sf-partb-defect1.log`).
- **Status (2026-11-18):** fixed, uncommitted (PK-06, REP-16, feature `report.first_failure`), presentation only. Observe steps still run after a failure; a FAIL after a skipped act carries `after_skipped_act: true` and shows "(after a skipped act)", and each failing scenario records `first_failure` and prints `cause: <step> failed first`. No result or exit code changed (exit 1 before and after, implementer's `/tmp/pk06-p31`). Claude read `runner.go` `noteFailure`: set only on FAIL, never on ERROR or NOT_RUN.

### P-32: The docker driver writes the binding's socket path, with the user's home folder, into the report

- **Severity:** low (privacy).
- **Evidence:** `drivers/docker/driver.go:248` (the proof's detail) and `:293` (Ready's detail) include the socket path; Software Factory's `report.json` held `/Users/<user>/.docker/run/docker.sock` in `drivers[0].proofs[0].detail`.
- **Failure scenario:** a report shared or kept as a CI artifact carries the runner's home path, though the driver's plan (A11) says no host path appears in evidence.
- **Suggested fix:** say "the binding's socket", or give only the base name.
- **Verified:** in `report.json`.
- **Status (2026-11-18):** fixed, uncommitted (PK-DKR-2). The proof, Ready and transport errors say "the binding's socket"; OS errors keep only the syscall text and any leftover copy of the path is replaced; DKR-02 messages no longer echo the binding's value. Live run with the socket under the home folder, including the not-a-socket, missing and unclean cases: no `/Users/` in `report.json` or stdout (implementer).

### P-33: The docker driver's `request_excerpt` leaves out its inspect requests

- **Severity:** documentation gap (evidence).
- **Evidence:** `drivers/docker/call.go:165` (`note`) is called only from the list functions (`:183`, `:220`, `:255`); `inspectContainer` and `inspectNetwork` (`:331`, `:345`) never call it.
- **Failure scenario:** a step's evidence shows only the label-filtered list requests, understating what it asked Docker.
- **Suggested fix:** note each inspect, as `GET /v1.44/containers/<id>/json`.
- **Verified:** by reading the report.
- **Status (2026-11-18):** fixed, uncommitted (PK-DKR-2). Every request goes through `reader.get`, which notes it in `request_excerpt`: lists, the scope lists that resolve names, and each inspect (`GET /v1.44/containers/<id>/json`, `GET /v1.44/networks/<id>`); AT-Q-119.

### P-34: The module-by-module build check leaves driver binaries in the tree

- **Severity:** low.
- **Evidence:** `GOWORK=off go build ./...` in a module whose only package is a `main` (`drivers/dynamodb`, `drivers/http`) writes `drivers/dynamodb/proctor-driver-dynamodb` and `drivers/http/proctor-driver-http` (about 22 MB); `.gitignore` covers only `/bin/`.
- **Failure scenario:** a later `git add -A` commits the binaries. Two agents hit it this session.
- **Suggested fix:** build with `go build -o /dev/null ./...` in such loops (STATUS.md or the contributor docs), or ignore `drivers/*/proctor-driver-*` and `simulators/*/proctor-sim-*`.
- **Verified:** by running it (PK-00 and PK-02 implementers).
- **Status (2026-11-19):** fixed, uncommitted (PK-23a). `.gitignore` ignores `/drivers/*/proctor-driver-*` and `/simulators/*/proctor-sim-*`. A `*` doesn't cross `/`, so only a binary at a module's root matches, not the source folders `drivers/<name>/cmd/proctor-driver-<name>/`. Checked after `cd drivers/http && GOWORK=off go build ./...`: `git check-ignore -v drivers/http/proctor-driver-http` names `.gitignore:7`, `simulators/ses/proctor-sim-ses` and `drivers/docker/proctor-driver-docker` match too, while `drivers/http/cmd/proctor-driver-http/main.go` and its folder aren't ignored (exit 1, `--no-index` included) and that folder's untracked files still show in `git status`; the binary was then deleted.

### P-35: EXE-11's run-time rule fails a ws claim whose key's parent no frame has

- **Severity:** edge case (a false FAIL, never a false pass).
- **Evidence:** `internal/runner/match.go` `keyInNoElement`: a claim key missing from every element makes `none` fail.
- **Failure scenario:** `none: { "$.messages": { "json.type": x } }` fails when every frame is text or binary, since no element has `json`; "no JSON message of type x" can't be asserted alone.
- **Suggested fix:** decide whether a key whose parent is absent in every element counts as "can't fail" (today) or as vacuously true only when the parent is optional in the fact schema.
- **Verified:** by reading (PK-02 implementer); notification-service's one such claim sits beside an `any` on the same key and passes.
- **Status (2026-11-18):** decided and fixed, uncommitted (PK-08). Decision: keep failing. A fact schema does exist (the ws `messages` schema requires only `type`, so `json` is optional), but reading a missing parent as "not this value" would pass exactly the runs where the claim tests nothing: an app that starts sending its errors as plain text (`error: boom`) would pass `none: { json.type: error }`, and a misspelt key under the unconstrained `json` would pass every run with no JSON frame. Under REQ-08 the rule with no false pass wins. The message now names the missing part and how to write the claim: "claim key json.type is in no element of $.messages; the claim can't fail (EXE-11): no element has json. … add a claim that some element has it (any, or count with is above 0, on json.type), or, if no element should, assert what the elements are instead". Verified (AT-Q-132) against a scratch ws server: text-only frames FAIL with that message; beside a JSON frame and an `any` the same claim passes (implementer).

### P-36: The docker driver recognises Docker's socket only by the bind source's name, so Docker Desktop's proxy socket isn't counted

- **Severity:** bug (possible false pass).
- **Evidence:** `drivers/docker/project.go:453` sets `docker_socket` only when the bind source's base name is `docker.sock`. Docker Desktop's Compose can report the helper's bind as `/run/host-services/docker.proxy.sock:/var/run/docker.sock` (PK-03 implementer, by `docker inspect`).
- **Failure scenario:** STK-001 then fails on the helper (`docker_socket: false`, source `host_path`). Worse, with the helper on another path, a second container mounting Docker's socket shows as `host_path`, and "only the helper holds the socket" (`count … docker_socket: true … is: 1`) would pass falsely.
- **Suggested fix:** classify by the destination as well (`/var/run/docker.sock`, `/run/docker.sock`), and recognise Docker Desktop's proxy and `.raw` socket paths as sources.
- **Verified:** by `docker inspect` and two runs (PK-03 implementer); earlier runs on the same machine passed with the default environment, so when Desktop rewrites the path is not yet known.

### P-37: A Docker socket bound from a renamed or nested source still reads `host_path`

- **Severity:** low (possible false pass).
- **Evidence:** `drivers/docker/project.go:487` `bindsDockerSocket` decides by names and known folders; Docker reports only the bind's source path, not what it is.
- **Failure scenario:** `-v /srv/x.sock:/tmp/y.sock` (a renamed socket), or a bind of `$HOME` or another folder that holds the socket, reads `host_path`, so "only the helper holds the socket" passes.
- **Suggested fix:** none in the driver alone (Docker Desktop's host paths live in its VM); document it, and keep the destination rule; a project can add a `none` claim on `host_path` mounts it doesn't expect.
- **Verified:** by reading (PK-DKR-2 implementer).
- **Status (2026-11-19):** documented, no code change (PK-23a). There is no fix in the driver alone: Docker reports only a bind's source path, and on Docker Desktop that path is resolved inside Docker's VM, so the driver can't see what it names. The destination rule stays. prd DKR-07 and README's "Docker checks" state the limit and recommend a `none: { "$.container.mounts": { source: host_path } }` claim on containers that shouldn't have a host bind (it validates, exit 0, on the installed build); STATUS.md lists it under "Known gaps".

### P-38: A volume whose `local` driver bind-mounts the socket's folder isn't counted

- **Severity:** low (possible false pass).
- **Evidence:** `drivers/docker/project.go:445`: a volume mount's `source` stays `null`, and the container projection doesn't read the volume's options (`-o type=none,o=bind,device=/var/run`).
- **Failure scenario:** a container gets Docker's socket through such a volume and reads `docker_socket: false`.
- **Suggested fix:** apply the destination rule to volume mounts too, and classify a bind-type `local` volume by its `device` with the same folder rule, without showing the path.
- **Verified:** by reading (PK-DKR-2 implementer).
- **Status (2026-11-19):** fixed by PK-22, uncommitted (DKR-07, feature `docker.volume_socket`). A volume mount reads `source: docker_socket`, and the container `docker_socket: true`, when its destination passes the bind destination rules, or when its in-scope `local` volume is a bind (`device` with `type: none` or `o` holding `bind`) whose `device` passes the bind source rule, or whose mount `subpath` is named `docker*.sock`, is a `.docker` folder or ends in `.docker/run` (`drivers/docker/project.go` `volumeSocket`). The options decoder reads `o` and `device` only to compute two booleans; neither is stored or shown. Docker Desktop answers with the device under `/host_mnt` (`/var/run` became `/host_mnt/private/var/run`, implementer-verified), so a source is judged without `/host_mnt`, and macOS's `/private` before `/var`; this applies to binds too and only ever adds `docker_socket`. A mount at any other destination of a volume outside the scope, which the driver can't read without widening its scope-filtered lists, or of an in-scope volume with a non-`local` driver, is ERROR `unsupported_result`, naming the container and the destination (and an in-scope volume's name), rather than a guessed `false` or `true`. The requests and the scope filter are unchanged. Verified by the implementer with throwaway created-only containers: a bind volume of `/var/run` reads true; plain, tmpfs and `/tmp` bind volumes read false; a plain volume at `/var/run/docker.sock` reads true; a `/root` bind volume with subpath `.docker/run` reads true; an unscoped volume is ERROR, and the same volume at `/run/docker.sock` reads true; no device path appears in the report (AT-Q-147, AT-Q-148). P-37's destination rule is kept.

### P-39: A waiver on a table's step couldn't cover only some rows

- **Severity:** edge case.
- **Evidence:** before PK-04, `internal/integrity/stepinfo.go` `checkRunScoped` reported "waives X, but the rule doesn't apply to this step" for every row the rule didn't fire on, and `model.Table.Waive` (a `map[string]string`) was copied to every row by `table.go`.
- **Failure scenario:** Software Factory's refusal tables mix `/api/remove-repo` and `/api/remove-session` with 75 other routes; once those routes are destructive, no waiver could be written, so the tables would be split or a read added only to satisfy EXE-09. Accepting a table waiver when any row needs it would make a row added later, covered by the old waiver, invisible to `proctor diff`.
- **Suggested fix:** a waiver given as its reason alone applies in every row; `{ rows: [<case ids>], reason }` covers only the named rows, and validation refuses named rows that don't fire, firing rows not named, and unknown ids; `proctor diff` reports each newly covered row as `rule_waived`.
- **Verified:** by validation, AT-Q-124 to AT-Q-126 (PK-04 implementer).
- **Status (2026-11-18):** fixed by PK-04, uncommitted (feature `waive.rows`). Software Factory's SEC-T01..T07, T09 and FLD-T01 waivers name their remove rows.

### P-40: `proctor diff` doesn't list a waiver inside an added scenario

- **Severity:** edge case (review gap).
- **Evidence:** `internal/protdiff/protdiff.go`: the `added` case reports the scenario as added and nothing about its steps' `waive` or `no_state_check`; an added table row covered by a waiver is reported (PK-04).
- **Failure scenario:** a pull request adds a scenario whose act carries `no_state_check` or `waive`; the reviewer sees "added scenario", which reads as more checking, not the waiver inside it.
- **Suggested fix:** list each waiver of an added scenario as `rule_waived` (and each `no_state_check` as its weakening kind).
- **Verified:** by reading (PK-04 implementer).
- **Status (2026-11-18):** fixed by PK-07, uncommitted. `proctor diff` lists each `no_state_check` (`state_check_waived`) and waiver (`rule_waived`) of an added scenario's steps and of a step added to an existing scenario, and for an added table row the table's `no_state_check` and its setup's waivers. AT-Q-127.

### P-41: An empty waiver reason in a table is reported once per row

- **Severity:** low (noise).
- **Evidence:** `internal/integrity/spec.go` checks "must state a reason" in every expanded case.
- **Failure scenario:** a 77-row table with an empty reason gives 77 identical findings.
- **Suggested fix:** check it once per table, as the `rows` checks do.
- **Verified:** by reading (PK-04 implementer).
- **Status (2026-11-18):** fixed by PK-07, uncommitted. A table's empty reason, unwaivable rule, empty `rows` and repeated row are reported once per table, at its first row; a table with `setup_from` now gets the `rows` checks it used to skip. AT-Q-128.

### P-42: A missing identity value is an ERROR mid-run, not AUTH-03, and earlier requests are sent first

- **Severity:** bug (medium: against AUTH-03 and AT-R-14; not a false pass, but requests reach the application and the exit code says ERROR instead of a configuration mistake).
- **Evidence:** prd AUTH-03 ("a missing or empty credential or attribute … is a configuration error (exit 3)") and AT-R-14 expect exit 3. `internal/runner/runner.go` `loadCredentials` copied the binding's `values` without checking that each `${<identity>.<name>}` a step uses exists or is non-empty; the reference was resolved only when its step ran (`internal/expr/expr.go:193`, `${%s} has no saved value`).
- **Failure scenario:** bob is bound with `user_id` but a step uses `${bob.id}`: alice's earlier step ran (a request sent), then bob's step ERRORed, exit 2. An empty value was sent as-is, so `/users/${bob.id}` became `/users/`.
- **Suggested fix:** in `loadCredentials`, collect every identity-value reference the steps make and report each one missing or empty as AUTH-03 before any request.
- **Verified:** live, against a local probe (PK-05 implementer).
- **Status (2026-11-18):** fixed in PK-05, uncommitted. `proctor run` refuses (AUTH-03, exit 3, nothing sent) every `${<identity>.<name>}` a step uses in `with` or `expect` whose value the binding lacks or holds empty (or only spaces), naming the identity and value name, never a value. AT-R-14 passes against a local probe (0 requests). notification-service's and Software Factory's binding writers write every value their specs use (read by the implementer).

### P-43: `sqs.peek` reads at most 10 messages

- **Severity:** edge case (capability; the driver reports `truncated`, so no false pass).
- **Evidence:** notification-service's dead-letter checks read a queue that one run fills with 7 messages; SQS's `ReceiveMessage` returns at most 10 per call and the driver makes one call.
- **Failure scenario:** a queue holding more than 10 messages hides the one a check looks for; the check fails as `truncated` rather than finding it.
- **Suggested fix:** page through the queue up to a protected limit without deleting (visibility timeout 0), or filter by an attribute the step names.
- **Verified:** by reading and by the pilot's counts (PK-NS-3 implementer).
- **Status (2026-11-19):** fixed by PK-22, uncommitted (SQS-03, feature `sqs.peek_hold`). The evidence above was wrong: since `2d87cb2` the driver already looped up to 150 receives (`maxReceives`), not one call. The loop never got past 10 because, with visibility timeout 0, the emulator returns the same oldest 10 messages to every receive (orchestrator's spike, 2026-11-19; the implementer saw 10 messages in 21 receives, `truncated`). The fix is the owner's opt-in hold (REQ-04): a queue listed in the sqs policy's `peek_hold` is received with a visibility timeout lasting until about 2 s before the call's deadline (at most 30 s), and every received message is set visible again with `ChangeMessageVisibilityBatch` on every exit; a failed release is a step ERROR naming how many messages stay hidden and for how long. Unlisted queues behave as before. `proctor diff` lists enabling it as `driver_policy_changed` (rerun on a build of this tree). Verified by the implementer against a throwaway emulator: 27 and then 26 messages read with `truncated: false`, all visible again and received by hand afterwards, none deleted; a release failed through a proxy is ERROR (exit 2) and the messages come back when the hold ends; after a failed receive the held messages are released at once; a 3 s step reads nothing and says `truncated` (AT-Q-144 to AT-Q-146).

### P-44: Two tables without an `id` in one file are merged by the step checks

- **Severity:** low.
- **Evidence:** a table's `id` is optional; `ApplyStepInfo` keys tables by `Case.Table`, and `First` is keyed by setup group (`internal/model/table.go:228`), so two id-less tables in one file share a key.
- **Failure scenario:** for EXE-09 and FIX-05 one table's rows can guard the other's, and a waiver's `rows` can name a row of the other table without the "table doesn't have" error.
- **Suggested fix:** require a table `id`, or key tables by their index in the file.
- **Verified:** by reading (PK-07 implementer).

### P-45: The GitHub job summary is appended through a symbolic link

- **Severity:** low (same class as P-10).
- **Evidence:** `internal/commands/authoritative.go:279-280` appends the job summary to `GITHUB_STEP_SUMMARY` with `os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)`, which follows a link at that path; PK-06 moved every report write to `writePrivate` but not this one.
- **Failure scenario:** on a runner where a planted link sits at the summary path, Proctor appends its summary to the link's target, any file the user can write.
- **Suggested fix:** refuse a summary path that `os.Lstat` finds is a link (open with `O_NOFOLLOW` on Unix), keeping append semantics for a regular file.
- **Found by:** PK-06's implementer (2026-11-18); Claude confirmed the line. Placed in PK-16, which adds the next report writer.
- **Status (2026-11-18):** fixed, uncommitted (PK-16). The job summary is opened with `openAppend` (`internal/commands/privatefile.go`): a path `os.Lstat` finds is a link, a directory or a special file is refused with a warning on stderr (exit code unchanged), and a regular or new file is opened with `O_APPEND|O_WRONLY|O_CREATE|O_NOFOLLOW` and checked again with `Stat`. Verified by the implementer: an authoritative run with `GITHUB_STEP_SUMMARY` at a link left the target unchanged by hash, refused a FIFO, appended to a regular file and created a new one.

### P-46: A failed status expectation doesn't show the response's error, so the cause hides elsewhere

- **Severity:** edge case (misleading report).
- **Evidence:** mutation round 1, M-08 (`ReadonlyRootfs: false` in Software Factory's box recipe): SES-001's `start` FAIL printed only `status expected 202, got 409` and `$.body.ok/id/sid not present`; the 409 body's `error` and `message` (the engine's sandbox check refusing a writable root file system, `internal/core/session/sandboxbox.go:42`) weren't shown (`/tmp/sf-mut/M-08.log`).
- **Failure scenario:** a reader of the FAIL looks at the dispatch route, not the box recipe that caused it.
- **Suggested fix:** when a status expectation fails, print the response body's top-level `error` and `message` (scrubbed), or the start of the body within `report.excerpt_bytes`.
- **Found by:** mutation round 1 (2026-11-18).
- **Status (2026-11-18):** fixed, uncommitted (PK-08, REP-17, feature `report.status_response`). A failed `equal` on `$.status` carries `response`: the body's top-level `error` and `message`, or else `body ` and the start of the body (text, or the JSON), after the driver's redaction paths and the sensitive-name rule, scrubbed with every report text and cut to `report.excerpt_bytes`; the terminal prints it under the assertion. Verified (AT-Q-133): a 409 `{error, message}` shows `response: error "sandbox_refused", message "the box's root file system is writable"`, a text 409 shows `response: body conflict: the box is busy`, a body echoing the bearer token shows `[redacted]` in the terminal and `report.json` (0 matches of the token), and a 64 KiB body is cut at 4096 bytes; the previous build showed only `expected 202, got 409` (implementer).

### P-47: Progress lines name a table row but not its table

- **Severity:** low (readability).
- **Evidence:** mutation round 1, M-05: progress printed `[n/942] FAIL t01_add_repo` with no table id; counting failures per table meant matching the `FAIL <table-id>` lines at the end (`/tmp/sf-mut/M-05.log`).
- **Suggested fix:** print `<table-id>/<row-id>` in progress lines.
- **Found by:** mutation round 1 (2026-11-18).
- **Status (2026-11-18):** fixed, uncommitted (PK-08, REP-18, feature `progress.table_rows`). Progress lines print `<table id>/<case id>`, and `tables[<n>]/<case id>` for a table without an id (`runner.Progress.Table`, `internal/commands/progress.go`), so `proctor mutate`'s progress has it too. Verified (AT-Q-134): `grid/t01_random`, `tables[1]/t01_hex`, `p48grid/t02_delete`; the previous build printed `t01_random` (implementer).

### P-48: A waived destructive act guards the later acts on its target, which then need no waiver

- **Severity:** false pass (EXE-09 weakened silently).
- **Evidence:** `internal/integrity/stepinfo.go:516` `accepted[i] = !unguarded || waived(d.at, "EXE-09")`; a later act on the same fixed target is then guarded by `guardFor(..., accepted[j], ...)` (lines 499-506), so it fires nothing, needs no waiver, and `proctor diff` reports no `rule_waived` weakening for it. Found while Software Factory's PK-SF-MUT1 wrote SEC-T11 and SEC-T12: naming every row's waiver was refused (365 errors) because the first waived row already covered the rest.
- **Failure scenario:** the waived row `t11_put_remove_session` (refused, so harmless) covers `DELETE /api/remove-session`; a later row that really deletes, for example after DELETE is routed to the handler, passes validation with no waiver of its own and no diff line. In a generic API, a waived `PUT /things/fixed` (refused, 403) silently covers a later `DELETE /things/fixed` that succeeds.
- **Suggested fix:** only an act EXE-09 accepted on its own merits (guarded by a read or an earlier guarded act) guards later acts; a waived act guards nothing, so each later act needs its own named waiver (ASM-18's named-row rule then lists them).
- **Found by:** PK-SF-MUT1's implementer (2026-11-18); Claude confirmed the line. Placed with PK-08 (next Proctor package after the mutation packages).
- **Status (2026-11-18):** fixed, uncommitted (PK-08, EXE-09, feature `exe09.waivers_guard_nothing`). `accepted[i] = !unguarded` in `internal/integrity/stepinfo.go`: only an act EXE-09 accepted without a waiver guards later acts on its target, and an unguarded later act's message names the waived one ("EXE-09 accepted it only through its waiver, and a waived act guards nothing"). Verified (AT-Q-135): a scenario with a waived refused PUT and an unwaived DELETE of `/things/fixed`, and a table whose row `t01_put` is waived by name and row `t02_delete` deletes the same target, validate with exit 0 on the previous build and exit 3 now (EXE-09 for both DELETEs); with their own waivers they validate and `proctor diff` lists `rule_waived` for `P48-001 delete` and `t02_delete`. Software Factory now gets 730 EXE-09 lines for 365 rows of `page-security.yaml` (SEC-T11 182, SEC-T12 182, SEC-T14 1), which need their own named waivers; notification-service is unchanged (implementer).

### P-49: `sqs.peek` raises each message's receive count

- **Severity:** latent (possible false pass or changed behaviour).
- **Evidence:** `drivers/sqs/call.go:256` (`peekQueue`, `:207` in `peek` at `457452c`) loops `for !settled() && receives < maxReceives && len(messages) < limit`, one `ReceiveMessage` per pass (`VisibilityTimeout: 0` unless the queue is in `peek_hold`), up to `maxReceives = 150` (`drivers/sqs/driver.go:41`). Every receive counts as a delivery: in the orchestrator's spike on 2026-11-19 against notification-service's emulator image `proctor-ns-aws-emulator:1.0.385`, a message's `ApproximateReceiveCount` was 9 after 9 such calls. With PK-22's `peek_hold`, each peek of a held queue raises each message's count by exactly 1 instead of once per receive (implementer-measured), which slows the effect but doesn't remove it.
- **Failure scenario:** on a queue with a redrive policy (`maxReceiveCount` N), repeated peeks, for example under `eventually` polling, move messages into the dead-letter queue. Proctor then causes what a dead-letter check looks for, or a message the application should still process leaves the source queue. notification-service peeks only `notifications_dlq` (its source queues get only `sqs.send` and `sqs.depth`), so it isn't affected today.
- **Suggested fix:** refuse or warn on a peek of a queue that has a `RedrivePolicy` (from `GetQueueAttributes`), or cap the receives per peek on such queues. The choice is the owner's.
- **Verified:** by the orchestrator's spike against the emulator (2026-11-19) and by reading; not checked against real SQS.

## notification-service
- **Status (2026-11-18):** fixed, uncommitted (PK-DKR-2). A bind or npipe reads `docker_socket` by its destination (`/var/run/docker.sock`, `/run/docker.sock`); by a source or destination named `docker*.sock`; by a `.sock` under `/run/host-services/` other than `ssh-auth.sock`; or by a source folder that holds the socket (`/`, `/var`, `/var/run`, `/run`, `/run/user/<n>`, `/run/host-services`, `.docker`, `.docker/run`). Live on Docker Desktop: Desktop's proxy socket, the socket at another destination, a file at `/var/run/docker.sock` and `/var/run` bound as a folder read true; `ssh-auth.sock` and a /tmp folder read `host_path` (implementer). Claude read the rule and reran the build checks and Software Factory's validation; Software Factory's full run on this build is pending.
- **Status (2026-11-18):** fixed, uncommitted (PK-09). Tables are keyed by their position in the file (`tables[n]`) for the setup group, EXE-09, FIX-05, the waiver rows checks, `expect_logs`, the log window and the report; an id-less table's `${run.id}` ends in `-tables-<n>`. Before, the second id-less table's setup never ran and its rows used the first table's saved values (a row could pass against the other table's state); now both setups run and EXE-09 and the waiver checks are per table.

### N-01: A member's first requests can create two conversations

- **Severity:** bug (application code, not caught by the specifications).
- **Evidence:** `internal/core/chat/services/chat_service.go:176-216`: `GetOrCreateConversation` reads with `s.convRepo.FetchByUserID(ctx, claims.UserID)` (line 180) and, when nothing is found, creates with `s.convRepo.Create(ctx, conv)` (line 209). `internal/adapters/repositories/dynamodb/conversation_repository.go:104-113` reads through the `userId-index` global secondary index, which DynamoDB serves eventually consistent only, and `:69-72` writes with an unconditional `PutItem` keyed by a new UUID.
- **Failure scenario:** the page opens the chat WebSocket and lists conversations at once (both call `GetOrCreateConversation`, `chat_service.go:118` and `:241`), or a member has two tabs: both reads find nothing, and both create a conversation. Even sequential calls can miss a conversation written a moment earlier, since the index lags. The member then sees whichever `FetchByUserID` returns (newest first), support sees two open conversations, and messages split between them. CHR-001's `one-conversation-per-member` step (`proctor/specs/chat-rest.yaml:35`) runs one request at a time against a local emulator and can't see it.
- **Suggested fix:** make creation conditional on a per-user key: a `PutItem` on an item keyed by `user_id` (or a separate lock item) with `attribute_not_exists`, then read the winner on `ConditionalCheckFailedException`. A spec can check it once requests can run concurrently.
- **Verified:** by reading.
- **Status (2026-11-18):** fixed, uncommitted on `proctor-pilot`. A new conversation's ID is `uuid.NewSHA1(namespace, user_id)` and the port's `Create` became `CreateIfAbsent`, a `PutItem` with `attribute_not_exists(conversation_id)` that returns the stored item (strongly consistent `GetItem`) on `ConditionalCheckFailedException` (`conversation_repository.go`, `chat_service.go`). The `userId-index` read stays first, so conversations with older random IDs are still found. New scenario CHR-008 checks the conflict path one request at a time, using `dynamodb.put` to plant the item a competing first call would have written; CHR-001 also checks the derived key. Implementer's race test: 12 simultaneous first calls per member gave alice 3 stored conversations before, 1 after. Trade-offs, written into `.claude/rules/chat-authz.md`: anyone who knows a user ID can compute that member's conversation ID, which is safe only while non-admin "not found" and "not owned" both answer 403; during a rolling deploy an old pod and a new pod can still create two for a brand-new member.

### N-02: `run.sh` leaves the previous run's report in place when a run fails early

- **Severity:** edge case.
- **Evidence:** `proctor/env/run.sh:22`: `args+=(--report "$state/report.json")`; `proctor/env/up.sh:87` removes the old logs, `pids`, `claims.json`, `proctor.env`, `binding.yaml` and others but not `report.json`, and `down.sh` doesn't remove it either. When `up.sh` fails, `run.sh` exits before Proctor runs; when Proctor exits before loading (P-09), it writes no report.
- **Failure scenario:** `STATUS.md` points people at `/tmp/proctor-ns/report.json`. After a failed start (a port in use, Docker stopped), that file still holds the last successful run's `PASSED`, with nothing in it saying it's old.
- **Suggested fix:** add `"$state/report.json"` to `up.sh`'s `rm -f` list, before anything can fail.
- **Verified:** by reading.
- **Status (2026-11-18):** fixed, uncommitted. `up.sh` removes `$state/report.json` first (only when `$state` is a real directory you own), and `run.sh` removes the report it will request, the default or a `--report` path, before its own first failure point. `down.sh` is unchanged, since it runs after Proctor writes the report.

### N-03: The pilot runs with the token cache off, while the service caches tokens for 60 seconds by default

- **Severity:** edge case (coverage).
- **Evidence:** `proctor/env/up.sh:469`: `auth_ttl=${PROCTOR_NS_AUTH_CACHE_TTL:-0}`; `internal/adapters/handlers/http/middleware/auth.go:54`: `const DefaultAuthCacheTTL = 60 * time.Second`, used whenever `AUTH_CACHE_TTL` is unset (`cmd/main.go:616-629`).
- **Failure scenario:** the AUTH scenarios (unknown, expired and malformed tokens) pass against a service that asks the token service on every request. Deployed with the default, a token the token service starts refusing (revoked, expired) is still accepted for up to 60 seconds from the cache, and a defect in the cache (a key that ignores part of the token, a refusal cached as acceptance) is never exercised.
- **Suggested fix:** run the AUTH suite a second time with the default TTL (`PROCTOR_NS_AUTH_CACHE_TTL` unset), or add a scenario with a short TTL that checks a token refused after the cache expires, and say in `STATUS.md` which configuration the pass covers.
- **Verified:** by reading.
- **Status (2026-11-18):** fixed, uncommitted. The pilot runs with the deployed 60-second default: `up.sh` no longer sets `AUTH_CACHE_TTL` and exits 2 if `PROCTOR_NS_AUTH_CACHE_TTL` is set. New scenarios: AUTH-006 (an altered or extended token is refused after the real one is cached), AUTH-007 (a token the token service stops accepting stays accepted from the cache until a `user.access.changed` event evicts it within 5 s, and the refusal isn't cached as an acceptance), AUTH-008 (a 3-second token is refused within 10 s). The token simulator gained `accept_validations` and `lifetime_seconds`. Pilot: 69 of 69, 878 assertions.

### N-04: The erasure cascade can miss a conversation created a moment earlier

- **Severity:** bug (data erasure).
- **Evidence:** `cmd/main.go:184-198` finds a member's conversations through `ListAllByUserID` and `DeleteByUserID` (`internal/adapters/repositories/dynamodb/conversation_repository.go`), which both read the `userId-index`, served eventually consistent only.
- **Failure scenario:** a `user.deleted` event arrives just after the member's first chat call. The index doesn't hold the new conversation yet, so it and its messages survive the erasure.
- **Suggested fix:** also delete the conversation under the member-derived key (N-01's fix makes it computable), with its messages, read with a strongly consistent `GetItem`.
- **Verified:** by reading (N-01 implementer, 2026-11-18).
- **Status (2026-11-18):** fixed, uncommitted. The `user.deleted` cascade (`cmd/main.go`) also reads the member's key (`chatservices.MemberConversationID`, new port method `FetchByIDConsistent`), erases that conversation's messages, then its row by key, under the existing messages-first guard; if either read fails it erases no chat data and the event is redelivered; a repeat finds nothing. New scenario FAN-004 fails on the old cascade (the member-key conversation and its message survive) and passes on the new, and checks that a redelivery succeeds. Residual, in `event-pipeline.md`: a random-ID conversation a pre-N-01 pod creates during a rolling deploy, and one created after the cascade's read while the deleted user's token still works.

### N-05: AUTH-007 and AUTH-008 fail when Proctor is rerun against a kept environment

- **Severity:** edge case (test environment).
- **Evidence:** `proctor/specs/auth.yaml` AUTH-007 and AUTH-008 depend on the token simulator's per-token state (`accept_validations`, `lifetime_seconds`), which only a fresh `up.sh` resets.
- **Failure scenario:** after `run.sh --keep`, running Proctor again against the same environment fails both scenarios, though the service is correct.
- **Suggested fix:** reset per-token state on the simulator's SIGHUP reload, or have `up.sh` issue fresh tokens for each Proctor run.
- **Verified:** by reasoning.
- **Status (2026-11-18):** fixed for AUTH-007 and AUTH-008, uncommitted. `run.sh --reuse` runs `fresh-tokens.sh`, which issues fresh tokens to the identities whose claims carry state and reloads the token simulator, then runs Proctor against the kept environment; both pass on the rerun. A kept-environment rerun still fails other scenarios: N-08, N-09, N-10.

### N-06: `make comments-check` fails on `proctor-pilot` at HEAD

- **Severity:** bug (CI).
- **Evidence:** `checkcomments: proctor/env/emulator.Dockerfile: no lexer knows how this file type writes a comment`; the file came in with commit `1170ebc`. Reproduced by Claude on the working tree, where the file is unchanged.
- **Failure scenario:** CI's Lint job fails on this branch.
- **Suggested fix:** rename it to `proctor/env/emulator/Dockerfile`, or teach `tools/checkcomments` the name (the tool must stay byte-identical across the three Go services).
- **Verified:** by running it.
- **Status (2026-11-18):** fixed, uncommitted. Moved to `proctor/env/emulator/Dockerfile`, `up.sh` updated; `make comments-check` passes (rerun by Claude); the image was rebuilt from the new path in the pilot.

### N-07: Local govulncheck runs an older Go than CI

- **Severity:** edge case (tooling).
- **Evidence:** `go.mod:3` says `go 1.27.1`, so a local run uses go1.27.1 and fails on GO-2026-6859 (net/http, fixed in 1.27.4); CI's `go-version: '1.27'` resolves to 1.27.4 and passes.
- **Failure scenario:** a developer sees a vulnerability CI doesn't, or the reverse after CI's Go moves.
- **Suggested fix:** raise the `go` line or add a `toolchain go1.27.4` line in `go.mod`.
- **Verified:** by running both (N-01 implementer).
- **Status (2026-11-18):** fixed, uncommitted. `go.mod` and `proctor/sim/token/go.mod` say `go 1.27.4`, a patch floor that CI's and Docker's `GOTOOLCHAIN=local` respect (a `toolchain` line they'd ignore); local govulncheck runs go1.27.4 and finds nothing. gateway-service and platform-service still say 1.27.1 (outside this work).

### N-08: CHR-008's waiver says `dynamodb.put` replaces its item, but the driver only writes new items

- **Severity:** edge case (a waiver reason that's false; rerun failure).
- **Evidence:** CHR-008 (`proctor/specs/chat-rest.yaml`) and PRF-008 put fixed keys (`${newcomer.conversation_id}`, `${carol.id}`); the dynamodb driver refuses an existing key ("an item with key … already exists … put writes only new items"). CHR-008's FIX-05 waiver reads "the put replaces the same item on every run".
- **Failure scenario:** a reviewer accepts the fixed key on a reason that isn't true; any rerun against a kept environment fails both scenarios at the put.
- **Suggested fix:** correct the waiver's reason, and make the keys per run where the scenario allows, or have `run.sh --reuse` clear those items first.
- **Verified:** by a rerun against a kept environment (PK-NS-2 implementer).
- **Status (2026-11-18):** fixed, uncommitted (PK-NS-3). CHR-008's FIX-05 reason now says `dynamodb.put` writes only new items and how a rerun stays clean. `run.sh --reuse` runs `reset-reused.sh`, which through the emulator only (it refuses unless the endpoint is the local emulator and STS answers account 000000000000) deletes the newcomer, typist and bob member-key conversations with their messages, and carol's preferences, then confirms each is gone. CHR-008 and PRF-008 pass on a kept-environment rerun (72 of 72).

### N-09: Dead-letter queues fill across kept runs, past `sqs.peek`'s 10 messages

- **Severity:** edge case (rerun failure; no false pass, since the peek reports `truncated`).
- **Evidence:** EVF-001, EVF-002, EVF-003 and CRT-003 peek DLQs that keep earlier runs' messages; the driver returns at most 10 ("the queue reports 12–14 messages").
- **Failure scenario:** a rerun's own message isn't among the 10 read, so the scenario fails.
- **Suggested fix:** purge the DLQs at the start of each Proctor run (`up.sh` and `--reuse`), or match this run's message by attribute.
- **Verified:** by a rerun against a kept environment.
- **Status (2026-11-18):** fixed, uncommitted (PK-NS-3). `reset-reused.sh` drains both dead-letter queues by receive-and-delete (not PurgeQueue, whose background deletion can remove a run's own messages); `up.sh` needs nothing, since each start gets a new emulator. The rerun drained 7 messages and passed 72 of 72.

### N-10: CHR-006 fails on a rerun because bob's seen state persists

- **Severity:** edge case (rerun failure).
- **Evidence:** CHR-006 asserts `absent $.body.data[0].user_last_seen_at`; bob's conversation keeps the previous run's value.
- **Failure scenario:** any rerun against a kept environment fails CHR-006.
- **Suggested fix:** reset bob's seen state in `--reuse`, or give the scenario a member of its own per run.
- **Verified:** by a rerun against a kept environment.
- **Status (2026-11-18):** fixed, uncommitted (PK-NS-3). `--reuse` removes bob's member-key conversation and messages, so CHR-006 starts from a never-read conversation; it passed on the rerun.

### N-11: A member's typing indicator right after the conversation is created is dropped

- **Severity:** edge case.
- **Evidence:** `handleTyping` (`chat_ws.go:360`) calls `FetchConversationByUser` (`chat_service.go:238`), which reads the eventually consistent `userId-index`.
- **Failure scenario:** the first typing event after a member's first chat call finds no conversation and is silently dropped.
- **Suggested fix:** fall back to `FetchByIDConsistent(MemberConversationID(uid))`.
- **Verified:** by reading (PK-NS-2 implementer).
- **Status (2026-11-18):** fixed, uncommitted (PK-NS-3). `FetchConversationByUser` falls back to `FetchByIDConsistent(MemberConversationID(uid))` and keeps the row only if it is the caller's. New CHW-016 fails on the old code (no `typing_notify`) and passes on the new.

### N-12: After erasure, a stale index entry can let a deleted member write orphaned messages

- **Severity:** bug (data after erasure).
- **Evidence:** `GetOrCreateConversation` (`chat_service.go:187`) reads the index first; after the cascade deletes the conversation, the index can still return it for a while.
- **Failure scenario:** while the deleted user's token still works (60-second cache, token lifetime), `SendMessage` inserts a message into the deleted conversation; it stays until the 90-day TTL, and `UpdateLastMessage`'s `ErrNotFound` is only logged.
- **Suggested fix:** confirm the conversation with a strongly consistent read before writing, and refuse a message for a conversation that is gone.
- **Verified:** by reading.
- **Status (2026-11-18):** fixed, uncommitted (PK-NS-3). `SendMessage` reads its target with `FetchByIDConsistent` and refuses (`ErrNotFound`, socket `SEND_FAILED`) when the conversation is gone, for members and support alike. `user.deleted` also empties the auth cache for the deleted user, locally before the cascade and on every pod over Redis, so the deleted member's cached token stops working. New FAN-005 fails on the old code (200 after the erasure, conversation recreated) and passes on the new; the emulator's indexes are synchronous, so the stale-index case was shown with a throwaway probe (old: 1 orphan message; new: refused, 0). Left open: a missed Redis broadcast on another pod (60 s at most) and the window between the read and the insert; a tombstone (erasure marker) was proposed, not built.

### N-13: `GetOrCreateConversation` returns the row under the member key without checking it is the caller's

- **Severity:** low.
- **Evidence:** `internal/core/chat/services/chat_service.go`, `GetOrCreateConversation` after `CreateIfAbsent`: on a conflict it returns the stored row read back under the member key, without comparing its `user_id` with the caller's.
- **Failure scenario:** only data planted under a SHA-1-derived key can trigger it (the erasure cascade already refuses such a row), so it is defence in depth.
- **Suggested fix:** if `stored.UserID != claims.UserID`, return an error.
- **Verified:** by reading (PK-NS-3 implementer).

### N-14: The pilot's dead-letter checks are near `sqs.peek`'s 10-message limit in a single run

- **Severity:** edge case (no false pass: the peek reports `truncated`).
- **Evidence:** one fresh run already dead-letters 7 messages into `notifications_dlq`; the sqs driver reads at most 10 (see P-43).
- **Failure scenario:** three more dead-lettering scenarios would make EVF and CRT checks miss their own message in one run.
- **Suggested fix:** P-43's larger or paged peek, or match each check's message more narrowly.
- **Verified:** by the rerun's drain count (PK-NS-3 implementer).
- **Status (2026-11-19):** open. PK-22 (P-43) makes it fixable: listing `notifications_dlq`, the only queue the pilot peeks, in `peek_hold` lets its dead-letter checks read past 10 messages. It closes when notification-service adds `peek_hold: [notifications_dlq]` to its `proctor.yaml`, which is the pilot's decision; `proctor diff` will list that as `driver_policy_changed`. The dead-letter queue has no redrive policy (notification-service's `proctor/env/up.sh:258-259` sets one only on the two source queues), so the receive counts a peek raises move nothing (P-49).


## Software Factory specifications (mutation round 1, 2026-11-18)

Planted defects that every one of Software Factory's 942 scenarios let through. Round 1 ran 13 mutants, one at a time, each in a throwaway copy with a full `make proctor` (Proctor build `/tmp/proctor-pk09-bin`): baseline 942 of 942; 8 killed, 5 survived, 0 errors. Logs and exact edits: `/tmp/sf-mut/` (`summary.log`, `M-NN.log`, `mutants.md`). Claude reread the logs and checked the spec facts below with grep.

### S-01: An Origin that only starts with the page's origin is never sent

- **Severity:** security gap in the specs (the code is right today).
- **Mutant:** M-02, `page.go:242` `origin[0] != s.origin` → `!strings.HasPrefix(origin[0], s.origin)`. It survived: under it `http://127.0.0.1:17799.evil.test` and `http://127.0.0.1:177990` pass.
- **Evidence:** the only wrong Origin any spec sends is `https://proctor.invalid` (6 uses), which shares no prefix with the page's origin.
- **Fix:** a `page-security.yaml` table that POSTs `/api/projects` (a declared read) with the page key and `Origin: "${page.origin}.proctor.invalid"` and `"${page.origin}0"`, expecting 403 `{error: origin}`.
- **Status (2026-11-18):** fixed, uncommitted (PK-SF-MUT1): SEC-T14 and SEC-T14R (Origin `${page.origin}.proctor.invalid` and `${page.origin}0`, 403 `{error: origin}`); M-02 now killed, 10 scenarios failed. Verified: Claude reread `/tmp/sf-mut2/*.log` and reran `routes.sh` and the old-build refusal (exit 2); the full suite rerun is recorded in the plan.

### S-02: The real page key under another scheme is never sent

- **Severity:** security gap in the specs.
- **Mutant:** M-04, `page.go:521` the `Bearer` scheme check dropped. It survived.
- **Evidence:** CRD-T01's `basic_scheme` variant is `Basic <random bytes>` (`proctor/env/up.sh:177`), refused by the key comparison before the scheme matters.
- **Fix:** `up.sh` writes the variant as `Basic <page key>` (and add `Token <page key>`), so a row expecting 401 `{error: token}` fails if the scheme isn't checked; the key is already a scrub pattern.
- **Status (2026-11-18):** fixed, uncommitted (PK-SF-MUT1): `up.sh` sends `Basic <page key>` and a new `Token <page key>` variant (`token_scheme` declared in `proctor.yaml`); CRD-T01 rows expect 401; M-04 now killed, 2 failed. Verified: Claude reread `/tmp/sf-mut2/*.log` and reran `routes.sh` and the old-build refusal (exit 2); the full suite rerun is recorded in the plan.

### S-03: The gate and the agent API aren't reachable by any spec

- **Severity:** coverage gap (design decision).
- **Mutant:** M-07, `internal/core/gate/writes.go:10` `.agents` dropped from the protected agent config folders. It survived.
- **Evidence:** `gate.Check` and `ProtectedWrite` are reached only through the agent listener's `/check` (`internal/adapters/http/page/push.go:110`), which only Claude Code's plugin hooks call; the binding reaches only the page port and Docker, and the fake ACP agent writes files without asking the gate. `.claude/rules/proctor.md` scopes the specs to the page's HTTP API but doesn't say the gate and the agent API (`/check`, `/push-request`) go unspecified.
- **Fix:** an owner's decision: publish the agent listener on loopback in `compose.proctor.yaml` with its connection secret in the binding, so specs can POST `/check`; until then, say it in proctor.md's "What the specifications can't check".
- **Status (2026-11-18):** open; not fixed: `.claude/rules/proctor.md` now names the gate and agent API as unspecified; the owner chose to open the agent listener in the test stack and go deeper (REQ-25), design under way. Verified: Claude reread `/tmp/sf-mut2/*.log` and reran `routes.sh` and the old-build refusal (exit 2); the full suite rerun is recorded in the plan.
- **Status (2026-11-18):** mostly fixed, uncommitted (PK-SF-GATE-1). The owner chose the fake agent asking the gate from inside the container (REQ-26) instead of publishing the listener, which `BoxListener.Accept` makes impossible. The changes:
  - a check-only key that answers only `/check`, from the session's own container, in the development image only (`internal/core/session/registry.go`, `cmd/server/devchecks.go`);
  - the fake's `try:` lines (`dev/fakeacp/gate.go`);
  - `proctor/specs/gate.yaml`, GATE-001..005.

  M-07 (HAND-007) is now killed. 8 of 9 gate mutants were killed; OWN-KEY-FROMBOX, the key's cross-container check, survives because no spec can hold the key outside its container. Still open: a positive `guarded` answer, which needs commits (GATE-2, paused with the mutation audit). Verified: Claude reread the key code and reran build, vet and `no-comments`, and validated 1596 scenarios. The implementer's full run on the real tree: 1596/1596, exit 0. The security review of the key is under way.

### S-04: No spec sends PUT, DELETE or PATCH

- **Severity:** security gap in the specs.
- **Mutant:** M-10, `page.go:268` PUT accepted on POST routes, which also skips the POST-only Origin requirement. It survived.
- **Evidence:** specs use GET (14), HEAD (1), OPTIONS (2), POST (77) and nothing else; SEC-T10 sends only GET to POST routes.
- **Fix:** a SEC-T11 table like SEC-T10 with PUT, DELETE and PATCH columns or rows, expecting 405 `{error: method}`, with the `<id>`/`<id>R` split.
- **Status (2026-11-18):** fixed, uncommitted (PK-SF-MUT1): SEC-T11 (PUT, DELETE, PATCH with the page's Origin on all 91 POST routes) and SEC-T12 (the same with no Origin), 405 `{error: method}`; M-10 now killed, 182 failed. Verified: Claude reread `/tmp/sf-mut2/*.log` and reran `routes.sh` and the old-build refusal (exit 2); the full suite rerun is recorded in the plan.

### S-05: No spec sends a repository URL that add-repo must refuse

- **Severity:** gap in the specs (a person's credential silently dropped).
- **Mutant:** M-11, `internal/core/session/repos.go:261` `u.User != nil ||` dropped. It survived: add-repo answers 202 instead of 400 "Leave the user name and password out".
- **Evidence:** `fields.yaml` has only the `{url: 1}` type row; `repositories.yaml` sends only valid https addresses; no spec reaches a `NormalizeURL` refusal.
- **Fix:** an add-repo refusal table expecting 400 `{error: url}` with `https://u:p@gitserver.test:8443/dev/x-${run.id}.git`, `http://…`, `ssh://…`, `…?x`, `…#x` and `/a/../b`, then a `/api/projects` read showing nothing was listed.
- **Status (2026-11-18):** fixed, uncommitted (PK-SF-MUT1): REP-005: six refused add-repo URLs with the exact `NormalizeURL` messages, then a projects read showing nothing added; M-11 now killed. Verified: Claude reread `/tmp/sf-mut2/*.log` and reran `routes.sh` and the old-build refusal (exit 2); the full suite rerun is recorded in the plan.

### S-06: Two kills hang on a single row

- **Severity:** low (thin coverage).
- **Evidence:** M-03 (`Sec-Fetch-Site: same-site` accepted) was caught only by one navigation row (`GET /`, NAV-T02 `same_site_navigation`); no POST route gets `same-site` (SEC-T04 sends only `cross-site`). M-08 was caught by the engine's own sandbox check before SES-001's Docker read of `readonly_rootfs` ran.
- **Fix:** add `same-site` to SEC-T04's rows for API routes.
- **Status (2026-11-18):** fixed, uncommitted (PK-SF-MUT1): SEC-T13 and SEC-T13R (`same-site` on every POST route, 403 `{error: site}`); a new variant M-03-SITE (same-site outside navigation) is killed, 91 failed. Verified: Claude reread `/tmp/sf-mut2/*.log` and reran `routes.sh` and the old-build refusal (exit 2); the full suite rerun is recorded in the plan.

### S-07: A Write to a dangling symbolic link isn't checked as its target

- **Severity:** bug (gate bypass: possible write to a protected file).
- **Evidence:** the plugin's write check resolves only the part of a path that exists (`plugin/hooks/check.ts`, ported to `dev/fakeacp/gate.go` by PK-SF-GATE-1). A Write to a link whose target doesn't exist yet is therefore checked under the link's own name.
- **Failure scenario:** a repository commits `env-link -> .env`, and the agent writes to `env-link`. `/check` answers `protected: ""`, and the write creates `.env`.
- **Fix:** an owner's decision. Either resolve the link itself (`readlink`) when its target doesn't exist, and check the target's path; or refuse a Write through any symbolic link.
- **Status (2026-11-19):** fixed by GATE-FIX (owner's decision REQ-32), pending the second round's checks; see S-11 and S-12. Before: open. GATE-002's row `w_dangling_env` pins today's answer on the fake's Go port; the real Claude Code's Write wasn't tried.
- **Found by:** PK-SF-GATE-1's implementer.

### S-08: A Write to the worktree's `.git` file is allowed

- **Severity:** bug (an agent can break the worktree).
- **Evidence:** `internal/core/gate/writes.go` `ProtectedWrite` refuses only `.codex` and `.agents` segments and a last segment of `.env`. In a worktree, `.git` is a file that points to the clone, and the fake overwrote it during exploration, cutting the worktree's link to the clone.
- **Failure scenario:** an agent acting on planted instructions writes `.git` (for example `gitdir: /elsewhere`), and the engine's later reads of the worktree's `.git` files follow it.
- **Fix:** an owner's decision, together with Q-02 (`.env.*`, `.envrc`). ARCHITECTURE.md says start-up files are covered by the start-up approval, but a worktree's `.git` file isn't a start-up file.
- **Status (2026-11-19):** fixed by GATE-FIX (owner's decision REQ-32), pending the second round's checks; see S-11 and S-12. Before: open. The gate rows now use `.git/hooks` and `.git/config`, which fail to write.
- **Found by:** PK-SF-GATE-1's implementer.

### S-09: Proctor has no clean-up step, so one failure cascades

- **Severity:** low (misleading report).
- **Evidence:** a failed assertion skips the scenario's later acts, the session stop included. The session left running holds the pool's only container, so every later session scenario fails too. GATE-1 worked around this by stopping each session before asserting.
- **Fix:** a Proctor `finally:` or `cleanup:` step group that always runs, with its failures reported apart from the scenario's verdict. This is a Proctor package still to be planned.
- **Status (2026-11-18):** open.
- **Status (2026-11-19):** fixed in Proctor by PK-20 (EXE-14, owner's REQ-02), uncommitted: `cleanup:` acts on scenarios and tables (once per row) always run after the steps, a setup failure included; a cleanup failure turns only a PASS into ERROR `cleanup_failed`, never becomes `first_failure`, and is reported apart; observing steps, time checks, `save` and `no_state_check` are refused in cleanup, and EXE-07 doesn't apply there. Software Factory can now move its "stop before asserting" workaround into `cleanup:`; that adoption isn't done.

### S-10: `/api/file-check` reports a path through a symbolic link as missing

- **Severity:** low (an absence check passes without looking).
- **Evidence:** like dot paths (Gotcha in `.claude/rules/proctor.md`), a path through a linked folder reads as missing, so an absence check there proves nothing. This is noted in `proctor.md`.
- **Fix:** specs read such paths with `/api/file` or a Docker read instead; or `/api/file-check` answers "not checked" for paths it won't follow.
- **Status (2026-11-18):** open; documented.

### S-11: A `..` after a missing folder or after a link hides a link to `.env` or `.git`

- **Severity:** medium (gate bypass; blocked the first GATE-FIX round).
- **Evidence:** the security review of GATE-FIX (2026-11-19) ran the plugin's `placed` and the fake's port. The walk sets `gone` at the first missing part and never clears it, and it never walks the folded path through links. So `missing/../env-link` and `out-link/../env-link` (with `out-link -> /elsewhere`) are checked as `env-link`, allowed, and the fake then wrote `.env`.
- **Fix:** re-check existence after `..`, and also walk the folded path and send its real form. GATE-002 rows for both shapes.
- **Status (2026-11-19):** fixed by GATE-FIX round 2, uncommitted; the verify-fix review reran its reproductions and found them refused; full run 1624/1624, 17782 assertions.
- **Found by:** knackpack:security-reviewer (F1).

### S-12: The `.git` rule stops at the worktree's root

- **Severity:** medium (the session clone's git metadata is writable by file tools).
- **Evidence:** the worktree's metadata is in `<ws>/repos/<repo>/.git/worktrees/<name>/`, outside `top`, and `ProtectedWrite` allowed every path outside `top`, by absolute path or through a link.
- **Fix:** the owner decided (REQ-37): refuse a `.git` part anywhere in the real path.
- **Status (2026-11-19):** fixed by GATE-FIX round 2, uncommitted; the verify-fix review reran its reproductions and found them refused; full run 1624/1624, 17782 assertions.
- **Found by:** knackpack:security-reviewer (F2).

### S-13: An exception while placing a path allows the Write

- **Severity:** low (fail-open; no attacker-chosen input found that throws).
- **Evidence:** `plugin/hooks/register.ts` `checkFor` caught a failure of `placeOf` as "no paths", and `/check` with no paths protects nothing.
- **Fix:** refuse instead, for tools that carry a path.
- **Status (2026-11-19):** fixed by GATE-FIX round 2, uncommitted; the verify-fix review reran its reproductions and found them refused; full run 1624/1624, 17782 assertions.
- **Found by:** knackpack:security-reviewer (F3).

### S-14: A path starting with `~/` is checked under the working folder

- **Severity:** medium (conditional: gate bypass if Claude Code's Write expands `~`, which its 2.1.328 path resolver does).
- **Evidence:** the verify-fix security review of GATE-FIX (2026-11-19). `placed` treats `~/../../workspace/s/<box>/worktrees/<name>/.env` as relative to the working folder, so every form lands outside the worktree and answers allowed, while the tool opens the worktree's `.env`. `.git` is safe (REQ-37 matches it anywhere). Pre-existing, not introduced by GATE-FIX.
- **Fix:** expand `~` and `~/` to the home folder as Claude Code does; refuse when the home folder can't be got. GATE-002 rows.
- **Status (2026-11-19):** in progress (GATE-FIX round 3).
- **Found by:** knackpack:security-reviewer (F4).
