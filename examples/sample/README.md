# Sample: Proctor end to end

A users API generated with [gopamin](https://github.com/bezmoradi/gopamin) (`gopamin new -n sample -t api -f http -b kafka -d postgres -l slog -o otel -a jwt`), with its tests and comments removed, and a `proctor/` folder that checks it. The API stores users in PostgreSQL, publishes an event to Kafka on every write, exports traces and logs over OTLP/HTTP, and requires a bearer token from its demo `POST /login`.

| Path | Contents |
|---|---|
| `compose.yaml` | The app, PostgreSQL, a single-node Kafka and `proctor-sim-otel`, on `127.0.0.1` ports 18480, 15480, 19480 and 14318 |
| `db/proctor-reader.sql` | The read-only role the `postgres` driver checks with |
| `proctor/proctor.yaml` | The drivers (`http`, `postgres`, `otel` and the repository driver `kafka`), identities, relations, the app-log check, transcript scrubbing and mutation |
| `proctor/specs/` | Create, read, update, delete and list, the 400, 404, 422 and 401 answers, each write checked in the database and on Kafka, and one request's spans |
| `proctor/approved/` | The approved transcript of USR-005 |
| `proctor/mutants.yaml`, `proctor/env/` | Three mutants and the scripts that build and start each one |
| `proctor/drivers/kafka/` | The source of the `kafka` driver |

The login (`admin`, `sample-demo-password`), the JWT signing value and the trust authentication in `compose.yaml` are local demo values; never reuse them.

## Walkthrough

You need Git, Go 1.27.4 or later, Docker with Compose v2 and curl. Run every command from the root of Proctor's checkout.

1. Build Proctor and the sample's driver:

   ```sh
   scripts/build.sh /tmp/proctor-bin
   export PATH="/tmp/proctor-bin:$PATH" PROCTOR_NO_UPDATE_CHECK=1
   (cd examples/sample/proctor/drivers/kafka && GOWORK=off GOTOOLCHAIN=go1.27.4 CGO_ENABLED=0 GOFLAGS=-mod=readonly \
     go build -trimpath -buildvcs=false -o bin/proctor-driver-kafka ./cmd/proctor-driver-kafka)
   ```

2. Start the stack, then take a token from the demo login and name the read-only database role:

   ```sh
   docker compose -f examples/sample/compose.yaml up -d --build --wait
   export SAMPLE_TOKEN=$(curl -fsS --retry 10 --retry-all-errors --retry-delay 1 -X POST http://127.0.0.1:18480/login \
     -H 'Content-Type: application/json' -d '{"username":"admin","password":"sample-demo-password"}' |
     sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
   export SAMPLE_DB_DSN='postgres://proctor_reader@127.0.0.1:15480/sample?sslmode=disable'
   ```

   The token is valid for an hour; take a new one the same way.

3. Validate the specifications and run them:

   ```sh
   proctor validate -c examples/sample/proctor/proctor.yaml
   proctor run -c examples/sample/proctor/proctor.yaml --binding examples/sample/proctor/environments/local.yaml
   ```

   `validate` ends with `RESULT: PASSED (exit 0, local, non-authoritative)`. `run` lists the drivers, `kafka` as `repository` with its checksum, proves that the database role can only read `users` (DB-04), passes all six scenarios and ends with `RESULT: PASSED (exit 0, local, non-authoritative)`.

4. Break agreed behavior. In `examples/sample/internal/adapters/repositories/postgres/repository.go`, change `"UPDATE %v SET name = $1 WHERE id = $2"` to `"UPDATE %v SET nickname = $1 WHERE id = $2"`, rebuild the app and run again:

   ```sh
   docker compose -f examples/sample/compose.yaml up -d --build --wait app
   proctor run -c examples/sample/proctor/proctor.yaml --binding examples/sample/proctor/environments/local.yaml
   ```

   USR-002, "A user's name can be changed", fails and the run exits 1 with `RESULT: FAILED (exit 1, local, non-authoritative)`. The rename answers 500 instead of 200, the read and the database still hold the old name, no `user.updated` event reaches Kafka, and the app-log check reports the unexpected ERROR line `update user failed` with PostgreSQL's error. Undo the change and rebuild:

   ```sh
   git checkout examples/sample/internal/adapters/repositories/postgres/repository.go
   docker compose -f examples/sample/compose.yaml up -d --build --wait app
   ```

5. Run the mutants:

   ```sh
   proctor mutate -c examples/sample/proctor/proctor.yaml
   ```

   For the unchanged copy and each mutant in `proctor/mutants.yaml`, `proctor/env/mutant-up.sh` restarts the stack from a copy of the checkout and `mutant-down.sh` stops it. The baseline passes, the three mutants are `killed`, and the command ends with `RESULT: PASSED (exit 0, local, non-authoritative)` after a few minutes. It leaves the stack stopped.

6. Tear down:

   ```sh
   docker compose -f examples/sample/compose.yaml down -v --rmi local
   ```

## Your own driver

`kafka` isn't one of Proctor's drivers. It is a [repository driver](../../README.md#repository-drivers): its source is in `proctor/drivers/kafka/`, a Go module started from `protocol/cmd/proctor-driver-example/` that requires the protocol module by version, and `proctor.yaml` names its program, its one operation (`events`, which reads a topic's records and filters them by key, JSON field or text) and its `sha256` for `linux/amd64` and `darwin/arm64`.

Build it with step 1's command. On another platform the program has no pin, so a local run goes ahead and reports it as `unpinned`. After changing its source or its dependencies, rebuild it for both pinned platforms with the README's fixed command, `GOOS=linux GOARCH=amd64` and `GOOS=darwin GOARCH=arm64`, and put each program's `shasum -a 256` in `drivers.kafka.sha256`. Until then Proctor refuses the program (exit 4, INT-17), as it refuses one that Git tracks or that sits outside the protected folder.
