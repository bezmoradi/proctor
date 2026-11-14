# AGENTS.md

Guidance for AI coding agents (and humans) working in **m10-worker-rabbitmq-postgres** — a `worker` project scaffolded by [Gopamin](https://github.com/bezmoradi/gopamin) using the Clean Architecture (Ports & Adapters) pattern. This file follows the [AGENTS.md](https://agents.md) convention.

## What this project is

A **broker-driven worker** with no HTTP server. It connects to **rabbitmq**, publishes a sample event on startup, and consumes events in a loop until interrupted — see [Broker workflow](#broker-workflow).

## Project layout

```text
cmd/
  main.go  # entry point: wires adapters and runs the worker
internal/
  core/
    shared/    # cross-cutting contracts: the logger port (used across aggregates)
    user/      # the User aggregate — grouped by feature, layered within
      domain/    # entities + invariants (no external imports)
      ports/     # interfaces the aggregate owns (implemented by adapters)
      services/  # use cases (depend only on ports)
  adapters/  # implement the ports; the only place third-party libs live
    repositories/
      postgres/  # durable postgres repository
    brokers/rabbitmq/  # rabbitmq producer + consumer (moves bytes)
    events/  # the event envelope: encodes and decodes the bytes
    loggers/slog/  # logging adapter (implements the logger port)
configs/  # loads .env into the process environment
tools/  # small project-root helpers
.env  # configuration — see Environment variables
docker-compose.yml  # postgres + rabbitmq service(s) for local development
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
  - `.../<aggregate>/domain/` — business entities and their invariants. **No** framework, database, or transport imports, and **no serialization tags** (`json:"..."`): wire formatting is a transport concern. The adapters own the wire shape — the events adapter maps them to the event envelope in `internal/adapters/events`.
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
- Type: worker (broker-driven, no HTTP server)
- Message broker: `rabbitmq` — adapter in `internal/adapters/brokers/rabbitmq`
- Database: `postgres` — repository in `internal/adapters/repositories/postgres`
- Logger: `slog` — adapter in `internal/adapters/loggers/slog`

## Broker workflow

This worker has **no HTTP server**. `cmd/main.go`:

1. connects to **rabbitmq** (adapter: `internal/adapters/brokers/rabbitmq`) and `postgres`;
2. publishes one sample `user.created` event, for a user named `Hello World`, to the `m10-worker-rabbitmq-postgres-users` queue (`RABBITMQ_QUEUE`);
3. consumes until `SIGINT`/`SIGTERM`: each `user.created` event becomes a user, at most once per event id (`CreateOnce`), so a redelivered event doesn't add a second user; other event types are logged and skipped.

**Event format.** Every message is a versioned JSON envelope owned by `internal/adapters/events`; `id` is unique per event, so a consumer can tell a redelivery from a new event. A message that isn't such an envelope is logged and dropped.

```json
{"id":"<uuid>","type":"user.created","version":1,"occurred_at":"2026-01-01T00:00:00Z","data":{"id":"<user id>","name":"Ada"}}
```

**Delivery.** **At-least-once**: the consumer acknowledges a message only after the handler returns nil, so a message whose processing didn't finish (a crash, or a shutdown during a retry) is delivered again. Keep handlers idempotent, as `CreateOnce` is. A failing handler is retried with backoff (0.5 s, doubling to 30 s); each attempt has 30 s and isn't cancelled by the shutdown signal, so a write is never cut off halfway. There is no dead-letter queue: a handler returns nil for a message it can never process, as the worker does for malformed and invalid events.

**Publishing** sends persistent messages to a durable queue and waits for the broker's confirm (2 s budget). A queue that already exists as non-durable can't be redeclared durable (`PRECONDITION_FAILED`, 406): delete it once, or set another `RABBITMQ_QUEUE`.

## Running

This project needs its backing service(s) running first.

```bash
make docker-up     # start postgres + rabbitmq in Docker (docker-compose.yml)
make migrate-up    # apply schema migrations (required before first run — see below)
make run           # run the app
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
docker build -t m10-worker-rabbitmq-postgres .
docker run --rm --env-file .env m10-worker-rabbitmq-postgres
```

Inside a container, `localhost` is the container itself, so the hosts in `.env`, written for `make run`, don't reach the compose services. Add `--network <directory>_default` (the network `docker compose` created; `docker network ls` lists it) and override the hosts with `-e`. On that network each service listens on its own default port, so if you changed a `*_PORT` in `.env`, override it back too (for example `-e POSTGRES_PORT=5432`).

| Variable | Value on the compose network |
|---|---|
| `POSTGRES_HOST` | `psql` |
| `RABBITMQ_HOST` | `rabbitmq` |


Apply the migrations (`make migrate-up`) before the first start; the app doesn't create its schema.

## Environment variables

Configuration is loaded from `.env` at the project root (via `godotenv`). Real shell environment variables take precedence over `.env`, so you can override any key inline (e.g. `LOG_LEVEL=debug make run`).

- `POSTGRES_*` connection settings — host, port, database and credentials, plus `POSTGRES_SSLMODE` (required): `disable` only for the local compose database, `verify-full` for a real server.
- `RABBITMQ_HOST`, `RABBITMQ_PORT`, `RABBITMQ_USER`, `RABBITMQ_PASSWORD` and `RABBITMQ_QUEUE` — the broker connection and the queue (default `m10-worker-rabbitmq-postgres-users`, so projects sharing a broker don't share it).
See `.env` for the full, authoritative list of keys this project reads.

## Testing

```bash
make test          # go test ./...
make test-cover    # run tests and open an HTML coverage report
```
The service layer is unit-tested against a fake repository defined in the test itself (`fakeRepository` in `internal/core/user/services`), so tests need no running database or broker and import no adapter. Tests are co-located (`*_test.go`). `make arch` skips test files (`excludeFiles` in `.go-arch-lint.yml`), so a test may wire real adapters; boundaries are enforced on production code.

## Adding a feature

1. **New aggregate** (e.g. `order`): create `internal/core/order/{domain,ports,services}` as a **sibling** of `user/` — a self-contained bounded context. Do **not** grow `user/`. For a **new operation on an existing aggregate**, extend that aggregate's `ports` (the interface) and `services` (the logic).
2. Implement the adapter under the matching `internal/adapters/...` package.
3. Wire the concrete adapter into the composition root in `cmd/main.go`.
4. If you added a new aggregate, mirror the `user-*` block in `.go-arch-lint.yml` (`order-domain` / `order-ports` / `order-services`) so `make arch` keeps enforcing the layering — deny-by-default fails the build on an unattached package until you do.

To handle a new message type: add the domain type and a service method, then dispatch to it from the consume loop in `cmd/main.go`.

## Conventions
- Keep everything under `internal/core/**` free of framework, database, and HTTP imports (`make arch` enforces this).
- Adapters translate between the outside world and domain types; they hold no business rules.
- Co-locate tests (`*_test.go`); the service layer is unit-tested against a fake repository defined in its test file.
- Run `gofmt`, `go vet ./...`, `go fix ./...` (Go 1.27 modernizers; preview with `make fix-check`), `make lint` (golangci-lint), and `make arch` before committing.


