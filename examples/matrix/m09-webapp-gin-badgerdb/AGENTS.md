# AGENTS.md

Guidance for AI coding agents (and humans) working in **m09-webapp-gin-badgerdb** — a `web-app` project scaffolded by [Gopamin](https://github.com/bezmoradi/gopamin) using the Clean Architecture (Ports & Adapters) pattern. This file follows the [AGENTS.md](https://agents.md) convention.

## What this project is

A server-rendered **web application** on the **gin** HTTP framework: it renders HTML pages for a `User` resource and serves embedded CSS. The binary is self-contained — HTML/CSS are embedded via `//go:embed` — see [Web UI](#web-ui).

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
    handlers/gin/  # HTTP handlers that render HTML
    repositories/
      badgerdb/  # durable badgerdb repository
    loggers/log/  # logging adapter (implements the logger port)
  web/  # embedded HTML templates + CSS assets (//go:embed)
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
  - `.../<aggregate>/domain/` — business entities and their invariants. **No** framework, database, or transport imports, and **no serialization tags** (`json:"..."`): wire formatting is a transport concern. The adapters own the wire shape — the web view uses `html/template` field access.
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
- HTTP framework: `gin` — handlers in `internal/adapters/handlers/gin`
- Database: `badgerdb` — repository in `internal/adapters/repositories/badgerdb`
- Logger: `log` — adapter in `internal/adapters/loggers/log`

## Web UI

Server-rendered HTML (this is **not** a JSON API). Handlers are in `internal/adapters/handlers/gin`; routes live under `/users`. **The pages are read-only**: there are no create, edit or delete forms yet. Writes are form-encoded requests to the same routes, sent with curl or another client:

| Method | Path | Does |
|---|---|---|
| `GET` | `/users` | list page (an empty list says how to add a user) |
| `GET` | `/users/:id` | detail page, with a link back to the list |
| `POST` | `/users` | create — form field `name` |
| `PUT` | `/users/:id` | update — form field `name` |
| `DELETE` | `/users/:id` | delete |

A missing user, a bad page link and a server failure render an HTML error page (`internal/web/templates/error.html`, via `web.RenderError`) with the matching status. The write routes answer in plain text for their clients. Adding forms means adding them to the page templates; the cross-origin protection already accepts same-origin form posts.

The list page is **forward-only paginated**: when more rows remain it renders a `Next →` link carrying an opaque `?cursor=…` (default page size `20`). This is a deliberately minimal browse UX — there is no "Previous" (backward cursors are out of scope). A hand-edited, malformed `?cursor` surfaces as a `400`; the generated `Next` links always carry a valid cursor.

Static assets are served from `/assets/*`. HTML templates and CSS live in `internal/web` and are **embedded into the binary** via `//go:embed`, so the compiled binary is self-contained (no runtime file dependencies). The server listens on `:8080` by default (see `PORT`).

## Health & readiness

Two orchestration endpoints are mounted at the transport layer (in `cmd/server`), so they work regardless of the HTTP framework:

| Method | Path | Purpose | Success |
|---|---|---|---|
| `GET` | `/healthz` | liveness — the process is running | `200 {"status":"ok"}` |
| `GET` | `/readyz` | readiness — ready to accept traffic | `200 {"status":"ready"}` |

Wire `/healthz` to a liveness probe and `/readyz` to a readiness probe (e.g. Kubernetes). The handlers live in `internal/adapters/handlers/health`. `/readyz` pings the `badgerdb` repository (2 s budget) and answers `503 {"status":"unavailable","failed":["database"]}` while it is down; the cause goes to the log. To probe another dependency, pass one more `health.Check` to `server.Serve` in `cmd/main.go`.

At startup the repository pings `badgerdb`, so a wrong host, port or password stops the app with an error instead of failing every request.

## Middleware

Requests to the application handler pass through a framework-agnostic middleware chain composed in `cmd/server` (package `internal/adapters/handlers/middleware`):

- **recover**: a panic becomes a logged `500` with its stack, not a crash. `http.ErrAbortHandler` passes through untouched, and a panic after the response started aborts the connection rather than appending a `500`.
- **request logging**: method, path, status, duration.
- **CORS**: only the origins in `CORS_ALLOWED_ORIGINS` (comma-separated `scheme://host[:port]`) get CORS headers; empty means same-origin only. Never use `*`.
- **cross-origin protection**: `http.CrossOriginProtection` answers `403` to a state-changing request a browser sent from another origin (the CORS allowlist is trusted). Non-browser clients such as curl are unaffected.
- **body cap**: request bodies over 1 MiB (`middleware.MaxBodyBytes`) get `413`.
- **per-request timeout**: bounds the request `context` so a slow datastore call can't hang forever.

**Security headers** (`nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, a strict `Content-Security-Policy`) wrap the whole server, health endpoints included. The health endpoints are mounted outside the chain, so probes stay cheap and unlogged.

## Running

```bash
make run           # run the app (serves on :8080 — override with PORT)
```


## Container image

The `Dockerfile` builds the binary into a non-root distroless image. The image holds no `.env`, so pass the configuration when you run it:

```bash
docker build -t m09-webapp-gin-badgerdb .
docker run --rm --env-file .env -p 8080:8080 m09-webapp-gin-badgerdb
```

Badger writes to `/home/nonroot/badger`; mount a volume at `/home/nonroot` (`-v m09-webapp-gin-badgerdb-data:/home/nonroot`) to keep it.

## Environment variables

Configuration is loaded from `.env` at the project root (via `godotenv`). Real shell environment variables take precedence over `.env`, so you can override any key inline (e.g. `PORT=9000 make run`).

- `PORT` — HTTP listen port (default `8080`).
- `GIN_MODE` — unset runs Gin in release mode; `debug` adds Gin's route table and warnings on stdout.
- BadgerDB is embedded and writes its data under `./badger` — it has no connection settings in `.env`.
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

To add a page: add the service method behind a port, a handler in `internal/adapters/handlers/gin`, and the template/asset under `internal/web`.

## Conventions
- Keep everything under `internal/core/**` free of framework, database, and HTTP imports (`make arch` enforces this).
- Adapters translate between the outside world and domain types; they hold no business rules.
- Co-locate tests (`*_test.go`); the service layer is unit-tested against a fake repository defined in its test file.
- Run `gofmt`, `go vet ./...`, `go fix ./...` (Go 1.27 modernizers; preview with `make fix-check`), `make lint` (golangci-lint), and `make arch` before committing.


