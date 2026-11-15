# Example application

`users-api` is a sample application for trying Proctor. It isn't part of Proctor. It is its own Go module, so `go test ./...` at the repository root doesn't build it.

It was generated with [gopamin](https://github.com/bezmoradi/gopamin) v1.3.0 (MIT), then changed in two ways:

- the `.git` directory gopamin creates was removed, because a nested repository would be recorded as a submodule;
- the generated `LICENSE` was removed. It was a template with a `<YOUR_NAME>` placeholder; the example falls under Proctor's license.

| Example | Generated with | Storage | Authentication |
|---|---|---|---|
| `users-api` | `gopamin new -n users-api -t api -f http -l slog` | In memory | None |

It needs Go 1.27.3 or later. With `GOTOOLCHAIN=auto`, an older Go downloads it on first use.

## users-api

```sh
cd examples/users-api
PORT=18080 make run
```

Routes are under `/api/v1/users`: `GET` (list), `GET /{id}`, `POST`, `PUT /{id}` and `DELETE /{id}`. `/healthz` and `/readyz` sit outside the prefix. Responses seen on 2026-11-14:

| Request | Status | Body |
|---|---|---|
| `POST /api/v1/users` with `{"name":"Ada","email":"ada@example.com"}` | 201 | `{"message":"Created user successfully","data":"<uuid>"}` |
| `POST /api/v1/users` with `{}` | 422 | `{"message":"name is required","error":"Validation failed"}` |
| `GET /api/v1/users` | 200 | `{"message":"Fetched users successfully","data":[{"id":"<uuid>","name":"Ada"}]}` |
| `GET /api/v1/users/does-not-exist` | 404 | `{"error":"Not found"}` |

## Running Proctor against users-api

`examples/users-api/proctor/` holds a specification set for this example: `proctor.yaml`, `manifest.yaml`, and `specs/users.yaml` with scenarios USR-001 to USR-005. Claude wrote them from the example's code and README, and they were accepted for the testing phase on 2026-11-14. `environments/local.yaml` is the local binding: the target URL and User-Agent. It sits under the protected root, as in the PRD's layout (§14), but only local runs read it; an authoritative run takes its binding from the trusted job (OD-11).

From the repository root, with the example running on port 18080:

```sh
go run ./cmd/proctor run \
  --config examples/users-api/proctor/proctor.yaml \
  --binding examples/users-api/proctor/environments/local.yaml
```

Proctor waits up to 15 seconds for `/readyz`, runs the five scenarios and exits 0 when all pass. Add `--json` for the machine-readable report. A run reads the working tree, so its result is local and not evidence for a commit.

## What this example can't exercise

- **Database checks (DB-*):** it keeps users in memory. The black-box tests in `e2e/postgres_test.go` exercise the PostgreSQL checks.
- **Logs (LOG-01):** it writes `log/slog` text lines to standard error, not JSON Lines files.
- **Identities and authorization tables (§11.3, AUTH-*):** it has no login, so every request is anonymous.
- **The probe endpoints the acceptance tests use (`prd.md` Appendix C)** (`/probe/echo`, `/probe/headers`, `/probe/redirect-external`): not present.
- **Temporal (TMP-*):** gopamin doesn't generate Temporal workers.
- **Language independence (§17):** it is written in Go; Node.js and Python examples are still needed.
