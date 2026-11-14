# Project m02-gin-mongodb

Here is the guide on how to use the **m02-gin-mongodb** project.

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
$ docker build -t m02-gin-mongodb .
$ docker run --rm --env-file .env -p 8080:8080 m02-gin-mongodb
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

[Logrus](https://github.com/sirupsen/logrus) library is used for logging purposes and it can be used as follows:

```go
logger := logrus.New()
```

The `New` function gets an optional `fileName` param:

```go
logger := logrus.New("logs.txt")
```

The name of the log file is arbitrary but if the `logs.txt` name is selected, it will be ignored by Git by default. If a file name is passed, instead of `os.Stdout` which is the default option, all logs will be written to the selected file which will be created at the root path of the project.

## MongoDB Setup

Since this project incorporates database integration, you must adhere to the following instructions before running the app.  
A `docker-compose.yml` file is included in this project which will be used for creating a MongoDB container. Some commands are also added to the `Makefile` file for running a container (The following guide is applicable if GNU/Make tool is installed on your machine).

### Run A MongoDB Container

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


