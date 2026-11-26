# Configuration reference

## Configuration keys

| Key | Meaning |
|---|---|
| `requires` | `proctor: ">=X.Y.Z"` and `features: [...]`, checked before anything else (INT-22, see [Requirements](#requirements)) |
| `invariants` | read-only steps that must hold before the first scenario and after each one; see [Environment invariants](#environment-invariants) (INV-01) |
| `protected_paths` | more paths to protect besides `proctor/`, `.github/`, `.gitattributes`, `.gitmodules`, `.lfsconfig` and CODEOWNERS; an entry ending in `/` is a folder |
| `protected_owners` | authoritative mode lists each protected file whose CODEOWNERS rule at the base names none of these owners as `UNOWNED` and in `trust.unowned_protected_paths`, reported only (OD-16); without the key every protected file is listed |
| `drivers.<name>.program` | a [repository driver](#repository-drivers)'s program, a clean path from the repository root to `proctor-driver-<name>` under a protected folder; instead of `official` |
| `drivers.<name>.sha256` | `<GOOS>/<GOARCH>: <64 hex digits>` for each platform the program is pinned for, at least one |
| `drivers.<name>.operations` | every operation of a repository driver, `act` or `observe`; the driver's `hello` must match it |
| `identities.<name>.type` | `none` or `bearer`; validation refuses any other type (exit 3); the names `run`, `secret`, `row` and `fill` are reserved |
| `identities.<name>.variants`, `accepted_variants` | other credentials a step sends with `variant: <name>`, never in setup or cleanup; a `variants` one must expect 401 or 403 (AUTH-06), an `accepted_variants` one needn't |
| `secrets` | names a step may use as `${secret.<name>}`, in an http body or headers, or a `browser.fill` value, only |
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
| binding `image`, `images` | authoritative mode: `built_digest` and `running_digest`, each `sha256:` and 64 lower-case hex digits, must be equal (REP-11). `images` maps each service of an environment built from several images (`images: { app: {…}, git: {…} }`, names in lower case) to its pair; give `image`, `images` or both. The report's `provenance` records `image` and `images` |
| binding `drivers.<name>.<key>_env` | names a variable whose value the driver gets as a secret; Proctor scrubs it from its output |

- An identity's or variant's variable must be set, non-empty and without a line break, and no two identities or variants may send the same credential (AUTH-03).

- The manifest refuses a repeated specification id or path (exit 3, INT-02), and a path that is absolute, holds `..`, a backslash or NUL, or isn't in clean form (exit 4, INT-01).
- Every `.yaml` or `.yml` file under `proctor/` is taken for a specification and must be listed in the manifest (exit 4, INT-05), except `proctor.yaml`, the manifest, the files `mutation.mutants` and `dimensions` name, `approved/`, and everything under `environments/`: keep other YAML there, such as a Compose overlay. `validate` checks the files Git tracks or would add in the working tree (with `--commit`, that commit's; outside Git, every file), and `integrity` and authoritative mode check the commit, so all three refuse the same files.
- A protected file can't have the Git attributes `diff`, `binary`, `text`, `eol`, `crlf`, `filter`, `working-tree-encoding`, `ident`, `linguist-generated`, `export-ignore` or `export-subst` (exit 4, INT-14). `proctor/approved/` holds at most 20,000 entries.

## Environment variables

| Variable | Effect |
|---|---|
| `PROCTOR_NO_UPDATE_CHECK` | any value turns off the update check (see [Privacy](#privacy)) |
| `CI`, `GITHUB_ACTIONS` | any value turns off the update check; `approve` and `mutate` refuse to run |
| `GITHUB_REPOSITORY`, `GITHUB_TOKEN` | authoritative mode asks GitHub for CODEOWNERS errors with them; without either, or with no CODEOWNERS file at the base, it exits 4 (INT-11), so it can't pass outside GitHub Actions |
| `GITHUB_API_URL` | GitHub's API, default `https://api.github.com` |
| `ACTIONS_ID_TOKEN_REQUEST_URL`, `ACTIONS_ID_TOKEN_REQUEST_TOKEN` | the job's OIDC token (`permissions: id-token: write`); without them authoritative mode exits 4 (MODE-03), with `trust.workflow_identity` `unverified` and `trust.workflow_changed` `unknown` |
| `GITHUB_RUN_ID`, `GITHUB_RUN_ATTEMPT` | recorded in the report; they must match the OIDC token |
| `GITHUB_STEP_SUMMARY` | the file the job summary is appended to |
