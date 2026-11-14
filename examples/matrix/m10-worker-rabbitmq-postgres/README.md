# Project m10-worker-rabbitmq-postgres

Here is the guide on how to use the **m10-worker-rabbitmq-postgres** project.

## Run The App

By default, a `Makefile` is added to this project which can be used for running most-used commands. To run this app using the `make` tool, run the following command in the terminal:

```text
$ make run
```

You can also use the `go run` command in order to run your app as follows:

```text
$ go run ./cmd/main.go
```

As the `main.go` file, which is considered the entry point of the app, resides inside the `cmd` folder, while using the `go run` command, you need to pass the full path as above.

To run it as a container instead, build the image and pass `.env` at run time; the "Container image" section of `AGENTS.md` covers reaching the compose services from the container:

```text
$ docker build -t m10-worker-rabbitmq-postgres .
$ docker run --rm --env-file .env m10-worker-rabbitmq-postgres
```

## Run The Tests

As with running the app, you have the option to use the `make` tool or employing the Golang's native commands. To run the tests using the `make` tool, run the following command in the terminal:

```text
$ make test
```

Running the following command will yield identical functionality:

```text
$ go test -v ./...
```

The `-v` flag is used here in order to output test details while running the tests.
Another testing command is also added to the `Makefile` called `test-cover` which will create a text file called `test-coverage.txt` at the root path of the project then open up a new browser tab showing the test coverage results.

## Enforce Architecture Boundaries

This project follows Clean Architecture: dependencies point inward only (`cmd → adapters → services → ports ← domain`). That rule is enforced mechanically by [go-arch-lint](https://github.com/fe3dback/go-arch-lint) using the generated `.go-arch-lint.yml`. To check it:

```text
$ make arch
```

A boundary violation — a database driver leaking into the domain, a service importing an HTTP handler, a stray package outside every layer — **fails the check**. Fix it by moving the code to the correct layer, not by loosening `.go-arch-lint.yml`. The `arch-check` target is the CI-friendly variant (it additionally refuses to pass if the linter has been disabled). The linter runs via `go run …@<pinned-version>`, so it never becomes a dependency of this project.


## Logger Guide

The built-in `slog` package is used for logging purposes and it can be used as follows:

```go
logger := slog.New()
```

The `New` function gets an optional `fileName` param:

```go
logger := slog.New("logs.txt")
```

The name of the log file is arbitrary but if the `logs.txt` name is selected, it will be ignored by Git by default. If a file name is passed, instead of `os.Stdout` which is the default option, all logs will be written to the selected file which will be created at the root path of the project.

## Postgres Setup

Since this project incorporates database integration, you must adhere to the following instructions before running the app.  
A `docker-compose.yml` file is included in this project which will be used for creating a Postgres container. Some commands are also added to the `Makefile` file for running a container (The following guide is applicable if GNU/Make tool is installed on your machine).

### Run A Postgres Container

First, make sure Docker is up and running on your machine. Then in the root path of the project run the following command:

```text
$ make docker-up
```

In order to stop the container, you can run the following command:

```text
$ make docker-down
```

Although it stops the container, still the volume related to the previously-created container exits and if you run `make docker-up` again, you have access to persisted data if any.  
In order to stop the container and at the same time remove the volume related to that container, you can run the following command:

```text
$ make docker-down-rm-vol
```

### Apply Database Migrations

The database schema is managed by versioned migration files under `migrations/` (via [golang-migrate](https://github.com/golang-migrate/migrate)) rather than being created when the app boots. After the container is up and healthy, create the schema **before** running the app:

```text
$ make migrate-up
```

Roll back the most recent migration with `make migrate-down`, and scaffold a new pair with `make migrate-create name=<change>` (e.g. `make migrate-create name=add_email`).

## RabbitMQ Guide

`make docker-up` starts RabbitMQ from `docker-compose.yml`, and `make docker-down` stops it. Its management UI is at `http://localhost:15672`; sign in with `RABBITMQ_USER` and `RABBITMQ_PASSWORD` from `.env`.

`make run` publishes one sample `user.created` event to the durable queue `m10-worker-rabbitmq-postgres-users` (`RABBITMQ_QUEUE`), then consumes the queue until Ctrl+C. Each `user.created` event becomes a user in postgres, once per event id, so each run adds exactly one user. A message is acknowledged only after it is processed, so one in progress when the worker stops goes back to the queue. The "Broker workflow" section of `AGENTS.md` describes the event format and the delivery guarantees.

### Send an event

The worker reads event envelopes; anything else is logged and dropped. The `id` must be a new UUID for each event. Publish from the management UI (Queues → `m10-worker-rabbitmq-postgres-users` → Publish message), or from the container:

```text
$ docker compose exec rabbitmq rabbitmqadmin --username <RABBITMQ_USER> --password <RABBITMQ_PASSWORD> \
    publish message --routing-key m10-worker-rabbitmq-postgres-users \
    --payload '{"id":"7c1f8f9e-8a8e-4a43-9a43-0f9f4b8c2f10","type":"user.created","version":1,"data":{"name":"Ada"}}'
```

The worker logs `event persisted` with the event's type and id.

### Publisher-only and subscriber-only workers

Delete the `broker.Consume(...)` call in `cmd/main.go` for a pure publisher, or the sample `Publish(...)` for a pure subscriber.


