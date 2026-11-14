run:
	@go run ./cmd/main.go

test:
	@go test -v ./...

test-cover:
	@go test ./... -coverprofile test-coverage.txt && go tool cover -html=test-coverage.txt

fix:
	@go fix ./...

fix-check:
	@go fix -diff ./...

GOLANGCI_LINT_VERSION := v2.14.0

# lint: run golangci-lint (config in .golangci.yml). Pinned via `go run …@version`
# so the linter never enters this project's go.mod, matching `make arch`. The first
# run compiles the linter (slow, one-time); later runs use the build cache. Bump the
# pin with each Go minor: a release older than your toolchain fails with "export
# data version … is greater than maximum supported version".
lint:
	@go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

GO_ARCH_LINT_VERSION := v1.15.0

# arch: verify the Clean Architecture dependency rule (.go-arch-lint.yml / AGENTS.md).
# Pinned via `go run …@version` so the linter never enters this project's go.mod.
arch:
	@go run github.com/fe3dback/go-arch-lint@$(GO_ARCH_LINT_VERSION) check --output-color=false

# arch-check: CI/gate variant. Fails on a boundary violation AND refuses to pass if
# the linter — or its vendor-import lock that guards the pure core — has been disabled
# while still exiting 0 (the banner flips to "Off"). Loosening the config to make a
# red build green defeats the scaffold; this catches that.
arch-check:
	@out="$$(go run github.com/fe3dback/go-arch-lint@$(GO_ARCH_LINT_VERSION) check --output-color=false 2>&1)"; \
	rc=$$?; \
	printf '%s\n' "$$out"; \
	if [ $$rc -ne 0 ]; then exit $$rc; fi; \
	printf '%s\n' "$$out" | grep -Eq '^[[:space:]]*On \| Base: component imports' || { echo 'arch-check: component-imports linter is OFF — refusing to pass'; exit 1; }; \
	printf '%s\n' "$$out" | grep -Eq '^[[:space:]]*On \| Advanced: vendor imports' || { echo 'arch-check: vendor-import lock is OFF (core purity unguarded) — refusing to pass'; exit 1; }



# docker-up: start the services and wait until each one's healthcheck passes, so
# `make docker-up && make run` doesn't race a service that is still starting.
docker-up:
	@docker compose up -d --wait

docker-down:
	@docker compose down

docker-down-rm-vol:
	@docker compose down -v


MIGRATE_VERSION := v4.20.1
MIGRATIONS_DIR := migrations

# Read .env as make variables, so the DSN below reuses the app's connection
# settings. They are not exported: no recipe gets the secrets in its environment,
# and a variable set in your shell still wins over .env for `make run`.
ifneq (,$(wildcard .env))
include .env
endif

# The credentials are pasted into a URL unescaped: if the password contains a URL
# reserved character (@ : / ? # %), set DATABASE_URL in .env yourself, with the
# password percent-encoded.
MIGRATE_TAGS := postgres
DATABASE_URL ?= postgres://$(POSTGRES_USERNAME):$(POSTGRES_PASSWORD)@$(POSTGRES_HOST):$(POSTGRES_PORT)/$(POSTGRES_DATABASE)?sslmode=$(POSTGRES_SSLMODE)

# migrate: the golang-migrate CLI, pinned via `go run …@version` with the driver
# build tag, so it never enters this project's go.mod (matching `make lint`/`arch`).
# The first run compiles the tool (slow, one-time); later runs use the build cache.
MIGRATE := go run -tags '$(MIGRATE_TAGS)' github.com/golang-migrate/migrate/v4/cmd/migrate@$(MIGRATE_VERSION)

# migrate-up: apply every pending migration. Run this once before first boot — the
# app doesn't create its schema at startup.
migrate-up:
	@$(MIGRATE) -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up

# migrate-down: roll back the most recent migration.
migrate-down:
	@$(MIGRATE) -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" down 1

# migrate-create: scaffold a new up/down pair, e.g. `make migrate-create name=add_email`.
migrate-create:
	@$(MIGRATE) create -ext sql -dir $(MIGRATIONS_DIR) -seq $(name)


