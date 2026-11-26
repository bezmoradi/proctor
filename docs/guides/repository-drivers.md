# Repository drivers

A driver doesn't have to ship with Proctor. Keep its source in a protected folder, build it in the job, and pin the program in `proctor.yaml`:

```yaml
drivers:
  k8s:
    program: proctor/drivers/k8s/bin/proctor-driver-k8s
    sha256:
      linux/amd64: <64 hex digits>
      darwin/arm64: <64 hex digits>
    operations: { pods: observe, events: observe }
    policy: { namespaces: [payments] }
```

- `program` takes the place of `official`; a driver has exactly one of them (exit 3, INT-12). It is a clean path from the repository root to a file named `proctor-driver-<name>`.
- `sha256` maps `<GOOS>/<GOARCH>` to the program's checksum as `shasum -a 256` prints it. `operations` lists every operation of the driver as `act` or `observe`; `by_args` isn't accepted.
- Keep the source in `proctor/drivers/<name>/` (`go.mod`, `go.sum`, `cmd/proctor-driver-<name>/`) with `bin/` in its `.gitignore`, and build it from that folder with the command the reference workflow runs:

  ```sh
  GOWORK=off GOTOOLCHAIN=go1.27.4 CGO_ENABLED=0 GOFLAGS=-mod=readonly GOOS=linux GOARCH=amd64 \
    go build -trimpath -buildvcs=false -o bin/proctor-driver-<name> ./cmd/proctor-driver-<name>
  ```

  Leave `GOAMD64` and `GOARM64` unset, set no `-ldflags -X`, and keep the driver's version a constant in its source. The build is then the same byte for byte on every machine, so a Mac computes the `linux/amd64` pin the job gets. A `replace` to a local folder records that folder's path in the program, so pin the protocol module by version instead.

Proctor refuses:

- a program whose folder isn't under the protected root or a `protected_paths` entry ending in `/`, a symbolic link on the program's path, a program that isn't a regular file, and one over 256 MiB (exit 4, INT-21); a missing program exits 2;
- a program whose checksum differs from its pin for this platform, in every mode (exit 4, INT-17); without a pin for this platform a local run goes ahead with origin `unpinned`, and an authoritative run refuses it (exit 4, INT-17);
- a program that Git tracks, in a commit for `integrity`, `validate --commit` and authoritative runs, and in the index for `validate` (with `--commit` too), `run`, `approve` and `mutate` (exit 4, INT-21). Paths are compared ignoring ASCII case, so any tracked file in the program's folder or a submodule at that folder or above counts;
- a driver whose `hello` doesn't list exactly the operations in `operations`, with the same kinds (exit 3, INT-21);
- a driver that answers `no_state_change` for a step, which only official drivers may (exit 3, INT-21), so every act on a repository driver needs a state check.

No repository driver starts while an INT-12 or INT-21 finding stands.

A repository driver runs as Proctor's user, with that user's files and network. The pin proves which program runs, not what it may do, so review its source as you would the application's. Proctor gives it only `PATH`, `HOME` and `TMPDIR`, and on a timeout, a failure or close stops it: on Unix with every process still in its process group, so a process that leaves the group keeps running; on other systems only the driver itself. The cli driver's commands run as Proctor's user too (see [CLI checks](#cli-checks)).

An authoritative run uses the base commit's driver, never the tested commit's: it takes the driver's `program`, `sha256` and `operations` from the base's `proctor.yaml`, so its program must match the base's pin. When the tested commit changes the driver's entry or a file in its source folder (the folder above the program's folder, `proctor/drivers/<name>/` in the layout above), the report lists it in `trust.repository_drivers_from_base`. A repository driver the base doesn't have isn't started: it is listed in `trust.repository_drivers_not_in_base`, and every scenario that uses it is NOT_RUN `driver_not_in_base` (exit 2), so the run can't pass. A driver change takes effect once merged, so merge it before the specifications that need it. The reference workflow builds the drivers from the base tree, outside the checkout, and passes that folder as `--driver-programs`.

Proctor runs a private copy of the program from a new folder under `TMPDIR`, which must allow running programs, and removes it afterwards. The targets, `shows`, `destructive` and state checks a driver reports aren't in `proctor.yaml`, so review its source for them. `proctor diff` lists a changed pin as `driver_program_changed` and an `act` that became `observe` as `operation_now_observes`.

The protocol is the Go module `github.com/bezmoradi/proctor/protocol`, which needs Go 1.27.4 or later. To build without downloading modules, run `go mod vendor` in the driver's folder: the reference workflow then builds with `-mod=vendor`, and the vendored code is protected and reviewed with the rest. Proctor speaks one protocol version, so rebuild the driver when it changes. To start, copy [`protocol/cmd/proctor-driver-example/`](protocol/cmd/proctor-driver-example/main.go), a driver with one `observe` operation. In an operation, `accumulates` marks an `observe` operation whose fact only grows, with or without a handle, so TIME-14 applies to it, and `scan` lists fact paths whose text Proctor searches for known credentials and secrets after every sample (`secret_in_telemetry`, see [Telemetry](#telemetry)); a `scan` path that doesn't parse stops the run (exit 2).
