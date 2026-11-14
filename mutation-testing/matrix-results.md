# gopamin combination matrix: does Proctor fit different stacks?

Ten projects were generated with [gopamin](https://github.com/bezmoradi/gopamin) from its combination matrix. Each sits in `examples/matrix/<name>`. Every runnable one was checked with Proctor against its unmodified application and six injected defects. Run on 2026-11-14 with `python3 -I mutation-testing/matrix.py run <names>`. Raw results are in `matrix-results.json`.

Together the ten cover:

- all ten frameworks or project types: Echo, Gin, Gorilla, httprouter, GraphQL, chi, net/http, a server-rendered web app, a queue worker;
- eight stores: MySQL, MongoDB, SQLite, MariaDB, PostgreSQL, Cassandra, DynamoDB, Badger;
- three brokers: RabbitMQ, Redis, Kafka;
- four loggers: zap, logrus, log, slog;
- JWT authentication.

## Results

"FAIL" is the required result for a defect: Proctor caught it. "PASS" under a defect means Proctor missed it.

| Combination | Store, broker, logger, auth | Unmodified | XM-01 create stores nothing | XM-02 update stores nothing | XM-03 delete deletes nothing | XM-04 empty name accepted | XM-05 response leaks `password_hash` | XM-06 every create stored twice |
|---|---|---|---|---|---|---|---|---|
| m01 Echo | MySQL, zap | PASS | FAIL | FAIL | FAIL | FAIL | FAIL | **PASS** |
| m02 Gin | MongoDB, logrus | PASS | FAIL | FAIL | FAIL | FAIL | FAIL | **PASS** |
| m03 Gorilla | SQLite, log | PASS | FAIL | FAIL | FAIL | FAIL | FAIL | **PASS** |
| m04 httprouter | MariaDB, slog | PASS | FAIL | FAIL | FAIL | FAIL | FAIL | **PASS** |
| m05 GraphQL | PostgreSQL, zap | PASS | FAIL | FAIL | FAIL | FAIL | not applicable | FAIL |
| m06 chi | Cassandra, RabbitMQ, slog, JWT | PASS | FAIL | FAIL | FAIL | FAIL | FAIL | **PASS** |
| m07 Echo | PostgreSQL, Redis, logrus, JWT | PASS | FAIL | FAIL | FAIL | FAIL | FAIL | FAIL |
| m08 net/http | DynamoDB, Kafka, zap | PASS | FAIL | FAIL | FAIL | FAIL | FAIL | **PASS** |
| m09 web app (Gin) | Badger, log | not specified, see finding 3 | | | | | | |
| m10 worker | RabbitMQ, PostgreSQL, slog | not runnable, see finding 4 | | | | | | |

On every runnable combination, the specifications caught each defect that shows through the API. They passed the unmodified application every time. XM-05 doesn't apply to m05, because a GraphQL client selects its fields and the REST response type doesn't exist there.

## Findings

1. **A defect visible only in the database is caught only on PostgreSQL.** XM-06 stores every user twice while the API stays consistent with itself. Proctor caught it on m05 and m07, through a PostgreSQL check that counts rows. It missed it on MySQL, MongoDB, SQLite, MariaDB, Cassandra and DynamoDB, which have no adapter. `prd.md` §9 lists "support every database, broker and platform in v1" as a non-goal. §19 names no adapter for these stores, so the gap is expected, but it's real. Adapters for MySQL and MariaDB would reuse most of the PostgreSQL design: a read-only role, parameterized queries, and a self-check of the role's privileges. The document stores need their own design.
2. **GraphQL works, at a cost.** Every GraphQL operation is a `POST /graphql`. EXE-07 decides what changes state by HTTP method, so it treats every read as a write. A scenario that ends in a read needs a `no_state_check` waiver, and `proctor diff` lists each waiver as a weakening. m05 needs two. Classifying GraphQL operations by type (query or mutation) would remove them, but it's not built. m05 also exposed a false positive in FIX-05: a saved ID sent as a GraphQL variable counted as a value that needed `${run.id}`. That is fixed (commit d1fb641).
3. **Proctor can't check a server-rendered web app.** m09 takes form bodies and answers with HTML pages and plain-text errors. Proctor sends only JSON bodies and asserts only JSON, and a 2xx step must assert something about its body. So no valid specification can say "the user's page shows the new name", and no Badger adapter exists to read the store either. Form bodies (`application/x-www-form-urlencoded`) and text or HTML assertions would be needed. Neither is in the PRD.
4. **Proctor can't check a worker with no HTTP interface.** m10 consumes RabbitMQ messages and writes to PostgreSQL. Proctor could read the result through its PostgreSQL adapter, but it has no way to publish the message that triggers the work. That would need a broker adapter. `prd.md` §19 lists RabbitMQ, Kafka and Redis as future adapters, not v1.
5. **Authentication carried over unchanged.** Credential variants (an expired token) and anonymous requests worked the same on m06 (chi, Cassandra) and m07 (Echo, PostgreSQL). Each has ten scenarios, including refused requests followed by a check that nothing changed.

## Problems found in the test setup, not in Proctor

- **m02's unmodified app failed at first.** Gin sends `Content-Type: application/json; charset=utf-8`, and the generated specification expected `application/json`. The specification generator (`matrix_specs.py`) now sets the content type per combination. Proctor's exact header match was right to fail.
- **The first m05 run was void.** An application left running from a probe still held the port, so every mutant was run against the unmodified build, and all passed. `matrix.py` now refuses to start an application on a port in use, and fails when an application exits at startup.
- **m06's RabbitMQ failed its healthcheck twice while the machine was heavily loaded.** gopamin's healthcheck allows about 70 seconds. The run passed once the load dropped.
