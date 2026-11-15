# Status: Proctor MVP and the notification-service pilot

As of 2026-11-15. Development is paused until the week of 2026-11-23.

Proctor runs end to end against notification-service: 65 scenarios, 836 step assertions, plus an application-log check on every scenario, all passing on the code as committed. Nothing is pushed. Both branches exist only on this machine.

## Where the code is

| Repository | Path | Branch | Commit | Pushed |
|---|---|---|---|---|
| Proctor | `/Users/behzadmoradi/Documents/Projects/proctor` | `drivers/foundation` | `1931dd2` for the code; this file is the commit after it | no |
| notification-service | `/Users/behzadmoradi/Documents/Projects/arcauthic/notification-service` | `proctor-pilot` | `531abe7` (10 commits ahead of `main`) | no |

The installed build in `~/go/bin` is `proctor` `0.0.0-1931dd2`, with six official drivers (http, ws, dynamodb, sqs, ses, postgres) and `proctor-sim-ses`. Reinstall after any Proctor change with `scripts/build.sh --install`: notification-service's `run.sh` passes `--report`, which builds before `a11d7d4` reject in local mode.

Test-only material (examples, end-to-end tests, mutation-testing harness, design notes) lives outside the repository in `/Users/behzadmoradi/Documents/Projects/proctor-reference/`. The example projects are `examples/users-api` (68 scenarios) and `examples/users-api-postgres` (93 scenarios).

## How to resume

Run the whole pilot (about 8 minutes; starts a local AWS emulator in Docker, Redis with Sentinel, token and email simulators and the app, then tears them down):

```sh
cd /Users/behzadmoradi/Documents/Projects/arcauthic/notification-service
proctor/env/run.sh
```

Expected end of stdout: `Scenarios: 65   passed 65   failed 0   errors 0   not run 0   assertions 836` and `proctor-ns: proctor exit 0`. Progress lines go to stderr as each scenario finishes. The JSON report is saved to `/tmp/proctor-ns/report.json` (mode 0600), and the app's log stays in `/tmp/proctor-ns/app.log` after teardown. Add `--keep` to leave the environment running; stop it with `proctor/env/down.sh`.

Check the specifications without starting anything: `proctor validate -r .` in notification-service (expect 11 specifications, 65 scenarios, exit 0).

Docker Desktop must be running. The scripts use only the Compose project `proctor-ns` and never real AWS.

## What Proctor does now

- A core plus driver programs that talk JSON-RPC over stdio. Drivers report facts; the core matches, times, scrubs secrets and decides.
- Integrity checks on the protected `proctor/` files, `proctor diff` that lists weakenings (removed or loosened checks, added waivers, moved assertions), local and authoritative (CI) modes.
- Matchers: equal, present, absent, keys, length, unordered, any, none, contains, count; `any`, `none` and `count` take one claim or a list. Tables of cases.
- Added on 2026-11-15:
  - Progress on stderr: one line per finished scenario, and a "running" line every 15 s for a long one. `--no-progress` turns it off.
  - `--report <path>` in local mode writes the same scrubbed JSON as `--json`.
  - After a check fails, later steps that only observe still run and report; steps that act are skipped.
  - App-log check: with `app_logs` in `proctor.yaml` and the log file named in the binding, any ERROR, FATAL or PANIC line written during a scenario fails it unless the scenario lists it under `expect_logs` (level, exact message, optional exact count). A listed entry that never appears also fails, so allowances can't go stale. `proctor diff` reports an added or widened allowance, or a removed level, as a weakening. Rule code EXE-10.

## What was verified, and how

- notification-service `531abe7` with installed `0.0.0-1931dd2`: full `run.sh` passed 65 of 65, 836 assertions, exit 0; every scenario and table carried a passing app-log check (EVF-001 read 36 log lines and matched all its declared errors); `report.json` written; no `proctor-ns` containers left.
- Proctor: `go build ./...`, `go vet ./...`, `gofmt -l .` empty, `GOWORK=off go vet ./...` in every driver and simulator module, no comments in the code; both example projects validate (exit 0).
- Deliberate bugs, each caught:
  - Deleting `prefs.SecurityAlerts = true` in `internal/core/preference/services/preferences_service.go` fails only PRF-003, exit 1. With `a11d7d4` its two later observe steps also run and show the wrong value was stored.
  - An extra `ErrorContext` log in the preferences read path fails the 9 scenarios that read preferences on the app-log check alone (reported by the subagent that built it).
- Reported by subagents and not rerun by hand: the validation negatives for `app_logs` and `expect_logs`, the `proctor diff` weakening kinds for log allowances, and log-file truncation handling.

## Known gaps

- Not exercised end to end: an act step skipped after a failure, the `missing_saved_value` reason, an error after a failure (the scenario stays FAIL by design).
- The app-log check matches by time window, which works only because scenarios run one at a time. Matching is on the message only (`msg`), so a generic message such as "Failed to process message" allows any cause.
- Where the binding's `app_logs.file` points isn't protected; the binding is environment configuration.
- The DynamoDB driver can't prove its credential is read-only; the report says `proof read_only: unproven`.
- The summary's `not run` counts scenarios, not steps.
- `README.md` and the untracked `prd.md` are out of date: they still say tables are unbuilt and log evidence is parked, and don't describe drivers or the app-log check. Documentation was deferred by agreement.
- notification-service:
  - No scenario checks that a security email still goes out to a user whose stored `security_alerts` is false. The email pipeline ignores the flag today.
  - The rate-limit fix (`middleware/ratelimit.go`) has no spec; it was checked by hand.
  - The WebSocket library accepts an `Origin` whose host equals the request's `Host`, whatever the scheme.
  - Left as designed: the rate limiter keys on `X-Forwarded-For` behind CloudFront, and `/readyz` doesn't check Redis or SES (it's the Kubernetes readiness probe).

## Parked

Temporal evidence, time-based checks (`eventually`, `consistently`), sign-in profiles beyond bearer (AUTH-01 basic and OAuth2 client credentials, the AUTH-04 identity check), the Compose-file half of INT-13, automated tests, release attestation, and Node.js and Python sample apps. The exec observer and guard mode (PROP-11) are proposed, not agreed.

## Candidate next steps

1. The chat discussion that was deferred.
2. Run independent scenarios in parallel to cut the 8 minutes. Needs a design first: scenarios share one app and database, and the app-log check attributes lines by time.
3. Count skipped steps and assertions separately in the summary.
4. Bring `README.md` up to date.
5. Close the notification-service spec gaps above.

## Working rules

- Never add code comments in Proctor.
- Testing is manual: no `*_test.go` files.
- Commit locally; never push without asking.
- Never print tokens, passwords or connection strings; never touch real AWS.
- Leave the Docker containers `sf-p1-*`, `sf-p9-dind`, `newsletter-agent-local-db-1` and `users-api-postgres-psql-1` alone.
- Never edit the arcauthic README files.

The agreement record for this work, with every requirement and assumption by ID, is `/Users/behzadmoradi/.local/state/knackpack/intent/proctor-3287142865/98bf4bb5-5efd-4cde-919e-14b99afabcbe.md`.
