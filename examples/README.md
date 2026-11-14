# Example applications

These are the applications Proctor is developed and tested against. They aren't part of Proctor. Each is its own Go module, so `go test ./...` at the repository root doesn't build them.

Both were generated with [gopamin](https://github.com/bezmoradi/gopamin) v1.3.0 (MIT), then changed in two ways:

- the `.git` directory gopamin creates was removed, because a nested repository would be recorded as a submodule;
- the generated `LICENSE` was removed. It was a template with a `<YOUR_NAME>` placeholder; the examples fall under Proctor's license.

| Example | Generated with | Storage | Authentication |
|---|---|---|---|
| `users-api` | `gopamin new -n users-api -t api -f http -l slog` | In memory | None |
| `users-api-postgres` | `gopamin new -n users-api-postgres -t api -f chi -d postgres -l slog -a jwt` | PostgreSQL 17 in Docker | JWT bearer token from `POST /login` |

Both need Go 1.27.3 or later. With `GOTOOLCHAIN=auto`, an older Go downloads it on first use.

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

## users-api-postgres

```sh
cd examples/users-api-postgres
make docker-up     # PostgreSQL on 127.0.0.1:5432
make migrate-up    # creates the users table
PORT=18081 make run
make docker-down-rm-vol   # stop and delete the database afterwards
```

The same user routes as `users-api`, plus `POST /login`, which takes `{"username":"admin","password":"<password>"}` and returns `{"token":"<jwt>"}`. Every `/api/v1` route then needs `Authorization: Bearer <jwt>`; without it, or with a tampered token, the answer is 401 `{"error":"unauthorized"}`. User IDs are PostgreSQL serials (`"1"`), not UUIDs.

gopamin printed the demo password once, when it generated the project, and `.env` keeps only its hash. To set a password you know, run `make hash-password` and put the printed hash in `.env` as `AUTH_PASSWORD_HASH`. `.env` also holds the database password and `JWT_SECRET`; it is ignored by Git and must stay out of commits.

## Running Proctor against users-api

`examples/users-api/proctor/` holds a specification set for this example: `proctor.yaml`, `manifest.yaml`, and `specs/users.yaml` with scenarios USR-001 to USR-005. Claude wrote them from the example's code and README, and they were accepted for the testing phase on 2026-11-14. `environments/local.yaml` is the local binding: the target URL and User-Agent. It sits under the protected root, as in the PRD's layout (§14), but only local runs read it; an authoritative run takes its binding from the trusted job (OD-11).

From the repository root, with the example running on port 18080:

```sh
go run ./cmd/proctor run \
  --config examples/users-api/proctor/proctor.yaml \
  --binding examples/users-api/proctor/environments/local.yaml
```

Proctor waits up to 15 seconds for `/readyz`, runs the five scenarios and exits 0 when all pass. Add `--json` for the machine-readable report. A run reads the working tree, so its result is local and not evidence for a commit.

## Running Proctor against users-api-postgres

`examples/users-api-postgres/proctor/` holds scenarios PGU-001 to PGU-049. Each write, and each refused write, is followed by a read-only database check. Response bodies are asserted whole, or by their exact key set. Created names carry `${run.id}`. The scenarios cover:
- CRUD round trips, and other users left unaffected;
- name rules;
- paging and cursors;
- body-size and content-type limits;
- security and CORS headers;
- refused credentials, as variants of `admin`: expired, no expiry, bad signature, wrong audience, wrong issuer, unsigned, malformed;
- the login throttle.

A run needs:

- the application, started with the same `JWT_SECRET` the tokens are signed with;
- a verification role that can only read `public.users`, since Proctor refuses any role with more rights (DB-04, exit 4). As the database owner:

  ```sql
  CREATE ROLE proctor_reader LOGIN PASSWORD '<choose one>';
  GRANT CONNECT ON DATABASE test TO proctor_reader;
  GRANT USAGE ON SCHEMA public TO proctor_reader;
  GRANT SELECT ON public.users TO proctor_reader;
  ```

- the environment variables `environments/local.yaml` names:
  - `PROCTOR_ADMIN_TOKEN`, from `/login`;
  - one variable per credential variant;
  - `PROCTOR_ADMIN_PASSWORD`, the admin password, sent only by the login scenarios as `${secret.admin_password}`;
  - `PROCTOR_APP_DB_DSN`, the reader's connection string.

`mutation-testing/harness.py` does all of this: it creates the role with a random password, logs in, and mints the variants. See `mutation-testing/mutation-test-results.md`.

Three scenarios need state that only a disposable environment gives:
- PGU-025 and PGU-026 assume at most 100 users;
- PGU-036 and PGU-049 assume no other login attempt for the same user within the last minute.

## What these examples can't exercise yet

- **Logs (LOG-01):** both write `log/slog` text lines to standard error, not JSON Lines files.
- **Authorization tables (§11.3, AUTH-*):** there is one identity, `admin`, and users have no owner, so there is no "allowed for its own resource, denied for another's" case.
- **The probe endpoints the acceptance tests use (`prd.md` Appendix C)** (`/probe/echo`, `/probe/headers`, `/probe/redirect-external`): not present.
- **Temporal (TMP-*):** gopamin doesn't generate Temporal workers.
- **Language independence (§17):** both are Go; Node.js and Python examples are still needed.
