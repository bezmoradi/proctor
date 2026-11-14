# AGENTS.md

Guidance for AI coding agents (and humans) working in **users-api-postgres** — an `api` project scaffolded by [Gopamin](https://github.com/bezmoradi/gopamin) using the Clean Architecture (Ports & Adapters) pattern. This file follows the [AGENTS.md](https://agents.md) convention.

## What this project is

A JSON REST API built on the **chi** HTTP framework. It exposes a `User` resource under `/api/v1/users` behind a consistent JSON envelope — see [HTTP API](#http-api).

## Project layout

```text
cmd/
  main.go  # entry point: constructs adapters, injects them, starts the server
  server/  # HTTP server bootstrap
  hashpassword/  # prints AUTH_PASSWORD_HASH for a new demo password (make hash-password)
internal/
  core/
    shared/    # cross-cutting contracts: the logger port (used across aggregates)
    user/      # the User aggregate — grouped by feature, layered within
      domain/    # entities + invariants (no external imports)
      ports/     # interfaces the aggregate owns (implemented by adapters)
      services/  # use cases (depend only on ports)
  adapters/  # implement the ports; the only place third-party libs live
    handlers/chi/  # REST handlers + routing
    api/  # shared JSON response/error envelope
    repositories/
      postgres/  # durable postgres repository
    loggers/slog/  # logging adapter (implements the logger port)
configs/  # loads .env into the process environment
tools/  # small project-root helpers
.env  # configuration — see Environment variables
docker-compose.yml  # postgres service(s) for local development
.go-arch-lint.yml  # architecture-boundary rules (make arch)
.golangci.yml  # golangci-lint config — static analysis (make lint)
```

## The dependency rule

Source-code dependencies point **inward only**:

```
cmd  →  adapters  →  services  →  ports  ←  domain
```

Inner layers must never import outer ones. Concretely:

- The core is grouped **by aggregate** (e.g. `internal/core/user/`), each with its own layered sub-packages:
  - `.../<aggregate>/domain/` — business entities and their invariants. **No** framework, database, or transport imports, and **no serialization tags** (`json:"..."`): wire formatting is a transport concern. The adapters own the wire shape — the REST layer maps entities to DTOs in `internal/adapters/api` (e.g. `api.User`, mapped via `api.NewUser`).
  - `.../<aggregate>/ports/` — interfaces that aggregate owns and the outside world implements.
  - `.../<aggregate>/services/` — use cases / business logic. Depend on **ports**, never on a concrete adapter.

  **Input rules live in `domain`, not in a service.** `domain.ValidateName` is the single gate every write passes through, so create and update can't drift apart. Add a new rule there and both inherit it; add it in one service method and you have shipped a hole. A rule that exists to keep bad input away from the datastore (length bounds, control characters) belongs here — anything that reaches the driver instead comes back as a `500` when it should have been a `422`.
- `internal/core/shared/` — cross-cutting contracts (logger, router) used across aggregates.
- `internal/adapters/` — the only place third-party libraries live: HTTP/GraphQL handlers, repositories, message brokers, and loggers that implement the ports.
- `cmd/` — the composition root: it constructs concrete adapters, injects them into services, and starts the process.

Never import an `internal/adapters/...` package from `internal/core/...`.

### This rule is enforced

The dependency rule above is checked mechanically by `make arch` ([go-arch-lint](https://github.com/fe3dback/go-arch-lint), config in `.go-arch-lint.yml`). A violation **fails the build**. Fix it by moving the offending code to the correct layer — **never** by widening a `mayDependOn` list or adding an `anyVendorDeps` grant to the core. Loosening `.go-arch-lint.yml` to turn a red build green defeats the purpose of the architecture.

If go-arch-lint ever reports a dependency you can't find in your imports, it's almost certainly `deepScan` tracing a dependency-injection call in `cmd` — leave `deepScan: false` (the config enforces the import graph, not data-flow).
This project ships **no CI**, so nothing runs the check automatically yet. Wire `make arch-check` into your CI pipeline (or a pre-push hook) to make boundary violations actually block a merge — `arch-check` exits non-zero on a violation and also fails if the linter has been silently disabled.

## This project's stack
- HTTP framework: `chi` — handlers in `internal/adapters/handlers/chi`
- Database: `postgres` — repository in `internal/adapters/repositories/postgres`
- Logger: `slog` — adapter in `internal/adapters/loggers/slog`
- Authentication: JWT bearer (`internal/adapters/handlers/auth`); see [Authentication](#authentication-jwt).

## HTTP API

Base path `/api/v1/users`. Every response is the JSON envelope `{"message"?, "data"?, "error"?}`.

**Every `/users` route requires a bearer token** — send `Authorization: Bearer <token>` (get one from `POST /login`). Without a valid token the response is `401`. See [Authentication](#authentication-jwt).


| Method | Path | Purpose | Success |
|---|---|---|---|
| `GET` | `/api/v1/users?limit=&cursor=` | list users (paginated) | `200` |
| `GET` | `/api/v1/users/:id` | fetch one | `200` |
| `POST` | `/api/v1/users` | create — body `{"name":"..."}` | `201` |
| `PUT` | `/api/v1/users/:id` | update — body `{"name":"..."}`; returns the user | `200` |
| `DELETE` | `/api/v1/users/:id` | delete | `200` |

Status codes: `200`/`201` success · `400` malformed JSON body **or invalid pagination cursor** · `404` not found · `413` body over 1 MiB · `415` write without `Content-Type: application/json` · `422` validation (e.g. empty name) · `500` datastore failure. **Errors** populate the `error` field (e.g. `{"error":"Not found"}`); **successes** populate `message`/`data`. Keep this convention when adding endpoints — do not return an error under `message`.

**Pagination.** The list endpoint is cursor-paginated so a single request can never trigger an unbounded scan. `?limit=N` bounds the page (default `20`, capped at `100`, both in `domain`); when more rows remain the envelope carries a `next_cursor`, which the client passes back verbatim as `?cursor=…` to fetch the next page. The cursor is **opaque** (URL-safe base64, `base64.RawURLEncoding`) — its contents are a private detail of the repository (a keyset id, a driver paging token, …), so never parse or construct one client-side. A malformed cursor is `400 {"error":"Invalid pagination cursor"}`. There is no offset paging: two of the supported stores (Apache Cassandra, DynamoDB) have no offset at all, so the cursor model is the only one that spans every datastore.

Two things to know when writing a `while next_cursor != ""` client:
- **Cassandra and DynamoDB may return a non-empty `next_cursor` whose next fetch is empty** — their drivers can hand back a trailing continuation token past the last row. The keyset stores (relational, Mongo, Badger) don't; they stop cleanly. A correct client keeps paging until `next_cursor` is absent (one harmless extra round-trip on those two stores).
- **Cursors are not signed, and don't need to be** — for the SQL/Dynamo stores a cursor is just the last row's id (already in the response, so it grants no new capability). The property that actually matters is that the list query is **authorization-scoped** (e.g. filtered to the caller's tenant — see the auth section if present): a cursor must never let a client page across data it couldn't already list. Scope the query, not the cursor.

```bash
# create, then page (server on :8080 by default — see PORT)
# every /api/v1/* route is behind the bearer token, so mint one first (the password
# was printed at generation; `make hash-password` sets a new one); without it every call below is 401
TOKEN=$(curl -s -XPOST localhost:8080/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<PASSWORD>"}' | jq -r .token)
curl -s -XPOST localhost:8080/api/v1/users -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"name":"alice"}'
#   -> 201 {"message":"Created user successfully","data":"<id>"}
curl -s -H "Authorization: Bearer $TOKEN" 'localhost:8080/api/v1/users?limit=2'
#   -> 200 {"message":"Fetched users successfully","data":[...2 users...],"next_cursor":"<opaque>"}
curl -s -H "Authorization: Bearer $TOKEN" 'localhost:8080/api/v1/users?limit=2&cursor=<opaque>'
#   -> 200 the next page; when "next_cursor" is absent you have reached the end
curl -s -H "Authorization: Bearer $TOKEN" localhost:8080/api/v1/users/does-not-exist
#   -> 404 {"error":"Not found"}
```

## Health & readiness

Two orchestration endpoints are mounted at the transport layer (in `cmd/server`), so they work regardless of the HTTP framework:

| Method | Path | Purpose | Success |
|---|---|---|---|
| `GET` | `/healthz` | liveness — the process is running | `200 {"status":"ok"}` |
| `GET` | `/readyz` | readiness — ready to accept traffic | `200 {"status":"ready"}` |

Wire `/healthz` to a liveness probe and `/readyz` to a readiness probe (e.g. Kubernetes). The handlers live in `internal/adapters/handlers/health`. `/readyz` pings the `postgres` repository (2 s budget) and answers `503 {"status":"unavailable","failed":["database"]}` while it is down; the cause goes to the log. To probe another dependency, pass one more `health.Check` to `server.Serve` in `cmd/main.go`.

At startup the repository pings `postgres` with a 5 s budget, so a wrong host, port or password stops the app with an error instead of failing every request.

## Middleware

Requests to the application handler pass through a framework-agnostic middleware chain composed in `cmd/server` (package `internal/adapters/handlers/middleware`):

- **recover**: a panic becomes a logged `500` with its stack, not a crash. `http.ErrAbortHandler` passes through untouched, and a panic after the response started aborts the connection rather than appending a `500`.
- **request logging**: method, path, status, duration.
- **CORS**: only the origins in `CORS_ALLOWED_ORIGINS` (comma-separated `scheme://host[:port]`) get CORS headers; empty means same-origin only. Never use `*`.
- **cross-origin protection**: `http.CrossOriginProtection` answers `403` to a state-changing request a browser sent from another origin (the CORS allowlist is trusted). Non-browser clients such as curl are unaffected.
- **body cap**: request bodies over 1 MiB (`middleware.MaxBodyBytes`) get `413`.
- **JSON only**: a `POST`/`PUT`/`PATCH` without `Content-Type: application/json` gets `415`, so a cross-site form or `text/plain` fetch can't reach a handler.
- **per-request timeout**: bounds the request `context` so a slow datastore call can't hang forever.

**Security headers** (`nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, a strict `Content-Security-Policy`) wrap the whole server, health endpoints included. The health endpoints are mounted outside the chain, so probes stay cheap and unlogged.

## Authentication (JWT)

Every application route requires a bearer token; a demo `POST /login` issues one. The logic lives in `internal/adapters/handlers/auth` (the only package that imports the JWT library — the core stays free of it; the authenticated subject travels in `context.Context`, readable via `auth.SubjectFrom(ctx)`).

- **Flow:** `POST /login` with `{"username","password"}` returns `{"token":"…"}`; send it as `Authorization: Bearer <token>` (scheme case-insensitive) on every other route. Missing/invalid/expired → `401` with a `WWW-Authenticate: Bearer` challenge.
- **Config** is `JWT_*`/`AUTH_*` in `.env`, read and validated by `auth.LoadConfig` in `cmd/server`. The server **refuses to start** when `JWT_SECRET` is missing, shorter than 32 bytes or the old placeholder, when `JWT_ISSUER`, `JWT_AUDIENCE` or `AUTH_USERNAME` is missing, when `AUTH_PASSWORD_HASH` is missing, malformed or the hash of the old demo password, or when the retired plaintext `AUTH_PASSWORD` is still set. gopamin wrote a random secret and password hash at generation and printed the password once; `make hash-password` (`cmd/hashpassword`) prints the hash for a new one.
- **Tokens:** **HS256**, algorithm **pinned** (`WithValidMethods`) so `alg:none` and RS256→HS256 confusion are rejected; `iss` and `aud` are issued and checked (`JWT_ISSUER`, `JWT_AUDIENCE`), so another service that shares the secret but not both names can't mint accepted tokens.
- **Password:** `AUTH_PASSWORD_HASH` is PBKDF2-HMAC-SHA256 with 600,000 iterations (standard library `crypto/pbkdf2`). The password is checked even for an unknown username, so timing doesn't reveal which usernames exist.
- **Login throttling:** `/login` allows 5 attempts per username and 20 per client address in each one-minute window, then answers `429` with `Retry-After`; a success clears the username's counter. Every attempt is logged as `auth.login.success`, `auth.login.failure` or `auth.login.throttled`, with the client address and username. Limits of this in-memory limiter: it is **per instance** (N replicas allow N times the attempts); behind a reverse proxy every client shares the proxy's address, so derive the client from the proxy's forwarding header only once you trust that proxy; anyone can **lock out a known username** for a minute by spending its 5 attempts; it tracks at most 10,000 keys and, once full of live windows, refuses new keys until one expires. Put a shared limiter (gateway or Redis) in front for production.
- **All-or-nothing:** the auth middleware wraps the whole framework handler, so **every** app route is protected; the only public endpoints are `/healthz`, `/readyz` and `/login`. For mixed public/protected app routes, add per-framework middleware (echo groups, chi sub-routers, …).
- **Productionize before shipping:** load `JWT_SECRET` from a secret store and rotate it; replace the single env-configured user in `LoginHandler` with a **real user store**. Serve over TLS only (a bearer token is a password). Not yet included: refresh tokens, revocation, MFA, and role/scope authorization.

## Running

This project needs its backing service(s) running first.

```bash
make docker-up     # start postgres in Docker (docker-compose.yml)
make migrate-up    # apply schema migrations (required before first run — see below)
make run           # run the app (serves on :8080 — override with PORT)
make docker-down   # stop the backing service (add docker-down-rm-vol to wipe volumes)
```

## Database migrations

The `postgres` schema is owned by versioned migration files in `migrations/` (applied with [golang-migrate](https://github.com/golang-migrate/migrate)), **not** created by the app at startup — so multiple instances never race on DDL, and schema changes are deliberate and reviewable.

```bash
make migrate-up                        # apply all pending migrations (run before first boot)
make migrate-down                      # roll back the most recent migration
make migrate-create name=add_email     # scaffold a new up/down pair under migrations/
```

The migrate CLI is pinned and run via `go run` (never added to `go.mod`), mirroring `make lint`/`make arch`. When you change the schema, add a migration pair rather than editing the repository constructor — the adapter assumes the table already exists.


## Container image

The `Dockerfile` builds the binary into a non-root distroless image. The image holds no `.env`, so pass the configuration when you run it:

```bash
docker build -t users-api-postgres .
docker run --rm --env-file .env -p 8080:8080 users-api-postgres
```

Inside a container, `localhost` is the container itself, so the hosts in `.env`, written for `make run`, don't reach the compose services. Add `--network <directory>_default` (the network `docker compose` created; `docker network ls` lists it) and override the hosts with `-e`. On that network each service listens on its own default port, so if you changed a `*_PORT` in `.env`, override it back too (for example `-e POSTGRES_PORT=5432`).

| Variable | Value on the compose network |
|---|---|
| `POSTGRES_HOST` | `psql` |


Apply the migrations (`make migrate-up`) before the first start; the app doesn't create its schema.

## Environment variables

Configuration is loaded from `.env` at the project root (via `godotenv`). Real shell environment variables take precedence over `.env`, so you can override any key inline (e.g. `PORT=9000 make run`).

- `PORT` — HTTP listen port (default `8080`).
- `POSTGRES_*` connection settings — host, port, database and credentials, plus `POSTGRES_SSLMODE` (required): `disable` only for the local compose database, `verify-full` for a real server.
- `JWT_SECRET`, `JWT_TTL`, `JWT_ISSUER`, `JWT_AUDIENCE`, `AUTH_USERNAME`, `AUTH_PASSWORD_HASH` — JWT signing + demo login (see the Authentication section).
See `.env` for the full, authoritative list of keys this project reads.

## Testing

```bash
make test          # go test ./...
make test-cover    # run tests and open an HTML coverage report
```
The service layer is unit-tested against a fake repository defined in the test itself (`fakeRepository` in `internal/core/user/services`), so tests need no running database and import no adapter. Tests are co-located (`*_test.go`). `make arch` skips test files (`excludeFiles` in `.go-arch-lint.yml`), so a test may wire real adapters; boundaries are enforced on production code.

## Adding a feature

1. **New aggregate** (e.g. `order`): create `internal/core/order/{domain,ports,services}` as a **sibling** of `user/` — a self-contained bounded context. Do **not** grow `user/`. For a **new operation on an existing aggregate**, extend that aggregate's `ports` (the interface) and `services` (the logic).
2. Implement the adapter under the matching `internal/adapters/...` package.
3. Wire the concrete adapter into the composition root in `cmd/main.go`.
4. If you added a new aggregate, mirror the `user-*` block in `.go-arch-lint.yml` (`order-domain` / `order-ports` / `order-services`) so `make arch` keeps enforcing the layering — deny-by-default fails the build on an unattached package until you do.

To add an endpoint: add the port method and service logic, then a handler in `internal/adapters/handlers/chi` and register its route. Return results through the `api.Response` envelope (errors under `error`, data under `data`).

## Conventions
- Keep everything under `internal/core/**` free of framework, database, and HTTP imports (`make arch` enforces this).
- Adapters translate between the outside world and domain types; they hold no business rules.
- Co-locate tests (`*_test.go`); the service layer is unit-tested against a fake repository defined in its test file.
- Run `gofmt`, `go vet ./...`, `go fix ./...` (Go 1.27 modernizers; preview with `make fix-check`), `make lint` (golangci-lint), and `make arch` before committing.


