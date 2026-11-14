# AGENTS.md

Guidance for AI coding agents (and humans) working in **m03-gorilla-sqlite** — an `api` project scaffolded by [Gopamin](https://github.com/bezmoradi/gopamin) using the Clean Architecture (Ports & Adapters) pattern. This file follows the [AGENTS.md](https://agents.md) convention.

## What this project is

A JSON REST API built on the **gorilla** HTTP framework. It exposes a `User` resource under `/api/v1/users` behind a consistent JSON envelope — see [HTTP API](#http-api).

## Project layout

```text
cmd/
  main.go  # entry point: constructs adapters, injects them, starts the server
  server/  # HTTP server bootstrap
internal/
  core/
    shared/    # cross-cutting contracts: the logger port (used across aggregates)
    user/      # the User aggregate — grouped by feature, layered within
      domain/    # entities + invariants (no external imports)
      ports/     # interfaces the aggregate owns (implemented by adapters)
      services/  # use cases (depend only on ports)
  adapters/  # implement the ports; the only place third-party libs live
    handlers/gorilla/  # REST handlers + routing
    api/  # shared JSON response/error envelope
    repositories/
      sqlite/  # durable sqlite repository
    loggers/log/  # logging adapter (implements the logger port)
configs/  # loads .env into the process environment
tools/  # small project-root helpers
.env  # configuration — see Environment variables
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
- HTTP framework: `gorilla` — handlers in `internal/adapters/handlers/gorilla`
- Database: `sqlite` — repository in `internal/adapters/repositories/sqlite`
- Logger: `log` — adapter in `internal/adapters/loggers/log`
- API docs: OpenAPI + Swagger UI at `/swagger` (`internal/adapters/api`); see [API documentation](#api-documentation-openapi--swagger).

## HTTP API

Base path `/api/v1/users`. Every response is the JSON envelope `{"message"?, "data"?, "error"?}`.


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
curl -s -XPOST localhost:8080/api/v1/users -H 'Content-Type: application/json' -d '{"name":"alice"}'
#   -> 201 {"message":"Created user successfully","data":"<id>"}
curl -s 'localhost:8080/api/v1/users?limit=2'
#   -> 200 {"message":"Fetched users successfully","data":[...2 users...],"next_cursor":"<opaque>"}
curl -s 'localhost:8080/api/v1/users?limit=2&cursor=<opaque>'
#   -> 200 the next page; when "next_cursor" is absent you have reached the end
curl -s localhost:8080/api/v1/users/does-not-exist
#   -> 404 {"error":"Not found"}
```

## Health & readiness

Two orchestration endpoints are mounted at the transport layer (in `cmd/server`), so they work regardless of the HTTP framework:

| Method | Path | Purpose | Success |
|---|---|---|---|
| `GET` | `/healthz` | liveness — the process is running | `200 {"status":"ok"}` |
| `GET` | `/readyz` | readiness — ready to accept traffic | `200 {"status":"ready"}` |

Wire `/healthz` to a liveness probe and `/readyz` to a readiness probe (e.g. Kubernetes). The handlers live in `internal/adapters/handlers/health`. `/readyz` pings the `sqlite` repository (2 s budget) and answers `503 {"status":"unavailable","failed":["database"]}` while it is down; the cause goes to the log. To probe another dependency, pass one more `health.Check` to `server.Serve` in `cmd/main.go`.

At startup the repository pings `sqlite`, so a wrong host, port or password stops the app with an error instead of failing every request.

## API documentation (OpenAPI / Swagger)

The API is documented by an OpenAPI 3.0.3 spec served by the app, with interactive Swagger UI:

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/openapi.yaml` | the raw OpenAPI document (`application/yaml`) |
| `GET` | `/swagger/` | interactive Swagger UI ("try it out") |

Both are mounted at the transport layer in `cmd/server` (so they work for every framework) and are **self-contained**: the spec is embedded via `//go:embed` and the Swagger UI assets are compiled into the binary by `swaggest/swgui` — no CDN, nothing to deploy alongside. The serving handlers are `api.SpecHandler` / `api.SwaggerUIHandler` in `internal/adapters/api`.

**The spec is templated, not scraped** — it lives in `internal/adapters/api/openapi.yaml` and was written to match the generated handlers. It is **not** auto-derived from them, so **when you add or change a route, update `openapi.yaml` too** (same discipline as this file and the README). If you'd rather have the spec regenerated from code, swap to annotation-driven generation with [swaggo/swag](https://github.com/swaggo/swag) (`// @Summary`/`// @Router` comments + a `swag init` build step) — more machinery, but it re-derives the doc from annotated handlers.

## Middleware

Requests to the application handler pass through a framework-agnostic middleware chain composed in `cmd/server` (package `internal/adapters/handlers/middleware`):

- **recover**: a panic becomes a logged `500` with its stack, not a crash. `http.ErrAbortHandler` passes through untouched, and a panic after the response started aborts the connection rather than appending a `500`.
- **request logging**: method, path, status, duration.
- **CORS**: only the origins in `CORS_ALLOWED_ORIGINS` (comma-separated `scheme://host[:port]`) get CORS headers; empty means same-origin only. Never use `*`.
- **cross-origin protection**: `http.CrossOriginProtection` answers `403` to a state-changing request a browser sent from another origin (the CORS allowlist is trusted). Non-browser clients such as curl are unaffected.
- **body cap**: request bodies over 1 MiB (`middleware.MaxBodyBytes`) get `413`.
- **JSON only**: a `POST`/`PUT`/`PATCH` without `Content-Type: application/json` gets `415`, so a cross-site form or `text/plain` fetch can't reach a handler.
- **per-request timeout**: bounds the request `context` so a slow datastore call can't hang forever.

**Security headers** (`nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, a strict `Content-Security-Policy`, skipped under `/swagger/` because Swagger UI runs inline scripts) wrap the whole server, health endpoints included. The health endpoints are mounted outside the chain, so probes stay cheap and unlogged.

## Running

```bash
make run           # run the app (serves on :8080 — override with PORT)
```

## Database migrations

The SQLite schema is owned by versioned migration files in `migrations/` ([golang-migrate](https://github.com/golang-migrate/migrate)). The SQLite adapter applies them at startup from a copy embedded in the binary (`migrations/migrations.go`), so a container from the image has its schema; `make migrate-up` writes the same version table, so running both is harmless. Only SQLite does this: a database server may have several instances starting at once, which would race on DDL.

```bash
make migrate-up                        # apply all pending migrations
make migrate-down                      # roll back the most recent migration
make migrate-create name=add_email     # scaffold a new up/down pair under migrations/
```

The migrate CLI is pinned and run via `go run` (never added to `go.mod`), mirroring `make lint`/`make arch`. When you change the schema, add a migration pair rather than editing the repository constructor — the adapter assumes the table already exists.


## Container image

The `Dockerfile` builds the binary into a non-root distroless image (with libc, because the SQLite driver uses cgo). The image holds no `.env`, so pass the configuration when you run it:

```bash
docker build -t m03-gorilla-sqlite .
docker run --rm --env-file .env -p 8080:8080 m03-gorilla-sqlite
```

The SQLite file lives in the working directory, `/home/nonroot`; mount a volume there (`-v m03-gorilla-sqlite-data:/home/nonroot`) to keep it. The app applies the migrations itself at startup.

## Environment variables

Configuration is loaded from `.env` at the project root (via `godotenv`). Real shell environment variables take precedence over `.env`, so you can override any key inline (e.g. `PORT=9000 make run`).

- `PORT` — HTTP listen port (default `8080`).
- `SQLITE_DB_URL` — the SQLite database file path (embedded; default `./test.db`, no server).
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

To add an endpoint: add the port method and service logic, then a handler in `internal/adapters/handlers/gorilla` and register its route. Return results through the `api.Response` envelope (errors under `error`, data under `data`).

## Conventions
- Keep everything under `internal/core/**` free of framework, database, and HTTP imports (`make arch` enforces this).
- Adapters translate between the outside world and domain types; they hold no business rules.
- Co-locate tests (`*_test.go`); the service layer is unit-tested against a fake repository defined in its test file.
- Run `gofmt`, `go vet ./...`, `go fix ./...` (Go 1.27 modernizers; preview with `make fix-check`), `make lint` (golangci-lint), and `make arch` before committing.


