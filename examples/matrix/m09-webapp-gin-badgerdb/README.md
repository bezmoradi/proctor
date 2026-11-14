# Project m09-webapp-gin-badgerdb

Here is the guide on how to use the **m09-webapp-gin-badgerdb** project.

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
$ docker build -t m09-webapp-gin-badgerdb .
$ docker run --rm --env-file .env -p 8080:8080 m09-webapp-gin-badgerdb
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

## BadgerDB Setup

Upon initial execution of the application, a folder named `badger` will be generated at the project's root (This folder is ignored by Git by default).

## Web App Guide

This is a **server-rendered HTML app** (not a JSON API). Visit `http://localhost:8080/users` in a browser to see the user list page (the port is `PORT` in `.env`, default `8080`). The list is **paginated**: when there are more users than fit on a page, a **"Next page →"** link appears carrying an opaque `?cursor=…` for the following page.

The pages are **read-only** for now: there are no forms. Add, change and delete users with the requests below.

### Add A New User

```text
curl --location 'http://localhost:8080/users' \
--header 'Content-Type: application/x-www-form-urlencoded' \
--data-urlencode 'name=John Doe'
```

Reload `http://localhost:8080/users` to see the new user on the page.

### Update An Existing User

```text
curl --location --request PUT 'http://localhost:8080/users/<USER_ID>' \
--header 'Content-Type: application/x-www-form-urlencoded' \
--data-urlencode 'name=Jane Doe'
```

### Delete An Existing User

```text
curl --location --request DELETE 'http://localhost:8080/users/<USER_ID>'
```


