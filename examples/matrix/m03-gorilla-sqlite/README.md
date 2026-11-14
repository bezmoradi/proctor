# Project m03-gorilla-sqlite

Here is the guide on how to use the **m03-gorilla-sqlite** project.

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
$ docker build -t m03-gorilla-sqlite .
$ docker run --rm --env-file .env -p 8080:8080 m03-gorilla-sqlite
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

The built-in `log` package is used for logging purposes and it can be used as follows:

```go
logger := log.New()
```

The `New` function gets an optional `fileName` param:

```go
logger := log.New("logs.txt")
```

The name of the log file is arbitrary but if the `logs.txt` name is selected, it will be ignored by Git by default. If a file name is passed, instead of `os.Stdout` which is the default option, all logs will be written to the selected file which will be created at the root path of the project.

## SQLite Setup

The SQLite database file (`test.db` by default, ignored by Git) is created the first time the app connects. Its **schema** is managed by versioned migration files under `migrations/` (via [golang-migrate](https://github.com/golang-migrate/migrate)), and the app applies any pending ones at startup, so `make run` works on a fresh checkout.

`make migrate-up` applies them without starting the app; both record what they applied in the same table. Roll back the most recent migration with `make migrate-down`, and scaffold a new pair with `make migrate-create name=<change>` (e.g. `make migrate-create name=add_email`).


## API Guide

The API is served under `http://localhost:8080/api/v1/users` (the port is `PORT` in `.env`, default `8080`).

### List Users (cursor-paginated)

```text
curl --location 'http://localhost:8080/api/v1/users?limit=20'
```

The list is **cursor-paginated**: `?limit=N` bounds the page (default `20`, max `100`). When more rows remain, the response carries a `next_cursor` — pass it back as `?cursor=<next_cursor>` to fetch the next page (an opaque token; do not build it yourself).

### Add A New User

```text
curl --location 'http://localhost:8080/api/v1/users' \
--header 'Content-Type: application/json' \
--data '{"name": "John Doe"}'
```

### Fetch An Existing User

```text
curl --location 'http://localhost:8080/api/v1/users/<USER_ID>'
```

### Update An Existing User

```text
curl --location --request PUT 'http://localhost:8080/api/v1/users/<USER_ID>' \
--header 'Content-Type: application/json' \
--data '{"name": "Jane Doe"}'
```

### Delete An Existing User

```text
curl --location --request DELETE 'http://localhost:8080/api/v1/users/<USER_ID>'
```

### API Documentation (Swagger)

The app serves its own API docs — no external tooling needed:

- **Swagger UI** (interactive, "try it out"): `http://localhost:8080/swagger/`
- **Raw OpenAPI spec**: `http://localhost:8080/openapi.yaml`

The spec lives at `internal/adapters/api/openapi.yaml` — update it when you add or change a route.


