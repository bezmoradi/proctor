# syntax=docker/dockerfile:1
#
# Multi-stage build for m10-worker-rabbitmq-postgres: stage 1 compiles a Linux binary, stage 2 ships
# only that binary on a minimal, non-root distroless base (no shell, no package
# manager, tiny attack surface).
#
# sqlite selects a cgo driver (mattn/go-sqlite3), so for it cgo stays enabled and
# the runtime base carries libc (distroless/base). Every other database is pure Go,
# so cgo is off and the static binary runs on the smaller distroless/static base.

# ---- build stage ----
FROM golang:1.27.3-bookworm AS build

WORKDIR /src

# Download modules first so this layer stays cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /out/app ./cmd

# ---- runtime stage ----
FROM gcr.io/distroless/static-debian12:nonroot

# /home/nonroot is writable by the unprivileged distroless user (uid 65532); make
# it the working dir so anything the app writes at runtime (e.g. an embedded DB
# file) has a home. Mount a volume here to persist it.
WORKDIR /home/nonroot
COPY --from=build /out/app /home/nonroot/app
ENTRYPOINT ["/home/nonroot/app"]


