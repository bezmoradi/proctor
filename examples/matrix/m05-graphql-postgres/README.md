# Project m05-graphql-postgres

Here is the guide on how to use the **m05-graphql-postgres** project.

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
$ docker build -t m05-graphql-postgres .
$ docker run --rm --env-file .env -p 8080:8080 m05-graphql-postgres
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

[Zap](https://github.com/uber-go/zap) library is used for logging purposes and it can be used as follows:

```go
logger := zap.New()
```

The `New` function gets an optional `fileName` param:

```go
logger := zap.New("logs.txt")
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

## GraphQL API Guide

The GraphQL API is a **single `POST /graphql` endpoint** that takes a JSON body — there is no Playground or GraphiQL, and `GET` or form requests are refused. Send each operation as JSON `{"query": "<operation>"}` with `Content-Type: application/json` (the port is `PORT` in `.env`, default `8080`). A document may select at most 10 root fields and 100 fields in total, and introspection is off unless `GRAPHQL_INTROSPECTION=true` in `.env`:

```text
curl --location 'http://localhost:8080/graphql' \
--header 'Content-Type: application/json' \
--data '{"query": "{ users(limit: 20) { items { id name } next_cursor } }"}'
```

### Operations

- **List users** — cursor-paginated. `users` returns a `UserPage { items, next_cursor }`; pass `limit` (default `20`, max `100`) and a `cursor` from a previous page:
  `{ users(limit: 20, cursor: "") { items { id name } next_cursor } }`
- **Fetch one:** `{ user(id: "<USER_ID>") { id name } }`
- **Create:** `mutation { create(name: "John Doe") }` — returns the new user's id
- **Update:** `mutation { update(id: "<USER_ID>", name: "Jane Doe") { id name } }` — returns the updated user
- **Delete:** `mutation { delete(id: "<USER_ID>") }` — returns `true`

A missing id fails both with the error `user not found`.


