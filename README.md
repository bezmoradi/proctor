<h1 align="center">⚙️ Engineered, Not Vibe Coded</h1>

<p align="center">
  <img src="https://img.shields.io/badge/Engineered-not%20vibe%20coded-blue?style=for-the-badge" alt="Engineered, not vibe coded">
</p>

## #1 STOP VIBE CODING <img src="https://img.shields.io/badge/Merged-8957e5?style=flat-square&logo=git&logoColor=white" alt="Merged">

```diff
- vibe coded: shipped because it compiled
+ engineered: designed, reviewed, documented
```

<sub>✅ Approved by human reviewers · All checks passed</sub>

# Proctor

Proctor checks that an application still does what people agreed it should do, and refuses to report a pass when the agreement itself was quietly changed. It is for codebases where AI coding agents change the code, the tests and the test configuration in the same pull request.

- The expected behavior lives in YAML specifications under a protected folder, and people approve changes to them through code-owner review.
- A trusted CI job runs every listed scenario against the application built from the tested commit and records what it checked. Proctor approves nothing; GitHub's branch protection does, using Proctor's check and report as evidence.
- Static integrity checks and a semantic diff of protected changes list weakenings first, with an optional [change kind](docs/guides/ci-github.md#change-kind).
- Official drivers for HTTP (with streams and binary, large and file bodies), WebSocket, PostgreSQL (read-only), DynamoDB, SQS, SES, OpenTelemetry, Docker (read-only), Redis (read-only), command-line programs, a headless browser and HTTP fakes, with local simulators for SES, OpenTelemetry and the services your application calls; your own drivers can live in your repository.
- Exact JSON assertions, text matchers, time-based checks with per-step timeouts, and tables that can cover every combination of declared dimensions.
- The application-log check, approved transcripts, approved logs and mutation testing.
- `proctor version --features` tells a wrapper what a build supports.

## How it works

- **Protected inputs.** The specifications, `proctor.yaml` and the files they name live under `proctor/`. That folder, `.github/`, `.gitattributes`, `.gitmodules`, `.lfsconfig`, CODEOWNERS and any `protected_paths` are protected inputs: a change to them needs a code owner's approval, and `proctor diff` lists it with weakenings, such as a removed assertion or an added waiver, first.
- **Local mode**, for development: `proctor validate` checks the specifications without contacting the application, and `proctor run --binding FILE` runs them against an application you started. Every local `RESULT:` line ends `(exit <n>, local, non-authoritative)`.
- **Authoritative mode**, for the trusted CI job: `proctor run --authoritative` reads every protected input from the tested commit, verifies the job's GitHub Actions identity, lists every protected change as "approval required", and writes the JSON report and job summary that the required check and the reviewers rely on. See [Authoritative mode](docs/guides/ci-github.md).

## Install

```sh
curl -fsSL https://github.com/bezmoradi/proctor/releases/latest/download/install.sh | sh
```

The script runs on linux and darwin, amd64 and arm64, checks the archive's SHA-256, and installs `proctor`, its drivers and the simulators into `~/.local/bin`. [Installation](docs/guides/install.md) covers verifying the script first, other folders, updating, pinning a release in CI and `requires`.

## Quick start

With Proctor installed, from your application's repository root:

```sh
proctor init
```

It writes a starting `proctor/` folder with four files and prints the next steps. It reads no Git and contacts nothing. It refuses (exit 3) when any of the four files exists, when `proctor/` or a folder in it is a symbolic link or not a folder, and when run below a Git repository's root; it writes nothing when it fails (exit 2). In a folder named `my-api`:

```yaml
# proctor/proctor.yaml
version: 2
project: my-api
manifest: manifest.yaml
drivers:
  http: { official: http }
identities:
  anonymous: { type: none }
```

```yaml
# proctor/manifest.yaml
version: 2
specifications:
  - id: health
    path: specs/health.yaml
```

```yaml
# proctor/specs/health.yaml
version: 2
suite: health
scenarios:
  - id: HLT-001
    name: The application answers its health check
    steps:
      - id: ready
        do: http.request
        identity: anonymous
        with:
          method: GET
          path: /readyz
        expect:
          equal:
            $.status: 200
          absent: [$.body_invalid]
```

A 2xx step must assert more than its status (EXE-03), so the starting scenario also checks that the answer isn't broken JSON. The binding tells Proctor where the application runs:

```yaml
# proctor/environments/local.yaml
drivers:
  http:
    base_url: http://127.0.0.1:8080
    user_agent: proctor-local/0
    ready_path: /readyz
```

`--project`, `--base-url` and `--ready-path` set `project`, `base_url`, and both `ready_path` and the scenario's `path`. Then, from the repository root, with the application running:

```sh
proctor validate
proctor run --binding proctor/environments/local.yaml
```

`validate` needs neither the application nor Git: in a folder that isn't in a Git repository it reads the files as they are and says `not a Git repository: the folder was read as it is` (`"git": false` with `--json`); with `--commit` it needs Git, and so does every other command that reads the repository. The run waits for `/readyz` (set `ready_path` to your application's health endpoint), executes every listed scenario and prints `RESULT: PASSED (exit 0, local, non-authoritative)` when they all pass. Add `--json` for the machine-readable report.

To see Proctor working before you adopt it, follow [`examples/sample/README.md`](examples/sample/README.md). It starts a users API with PostgreSQL, Kafka and OpenTelemetry in Docker Compose, checks it with official drivers and a repository driver, breaks one agreed behavior to show the failing scenario, and runs the mutants. CI runs it on every pull request.

Next, say what the application does. This specification creates a user and reads it back; list it in the manifest as `- { id: users, path: specs/users.yaml }`:

```yaml
# proctor/specs/users.yaml
version: 2
suite: users
scenarios:
  - id: USR-001
    name: A created user can be read back
    steps:
      - id: create
        do: http.request
        identity: anonymous
        with:
          method: POST
          path: /api/v1/users
          body: { name: "Ada ${run.id}" }
        expect:
          equal:
            $.status: 201
            $.body.message: Created user successfully
        save: { user_id: $.body.data }
      - id: read
        do: http.request
        identity: anonymous
        with:
          method: GET
          path: "/api/v1/users/${user_id}"
        expect:
          equal:
            $.status: 200
            $.body.data.id: "${user_id}"
            $.body.data.name: "Ada ${run.id}"
```

For CI, see [Authoritative mode](docs/guides/ci-github.md) and [`examples/github/`](examples/github/README.md).

## Documentation

[docs/README.md](docs/README.md) lists every page by task:

- **Learn the model:** [Concepts](docs/concepts.md).
- **Write specifications:** [Writing specifications](docs/guides/specifications.md), [Approved transcripts and logs](docs/guides/approvals.md), [Mutation testing](docs/guides/mutation-testing.md), [Repository drivers](docs/guides/repository-drivers.md).
- **Look something up:** [rules such as EXE-14](docs/reference/rules.md), [commands](docs/reference/cli.md), [configuration](docs/reference/configuration.md), [specification format](docs/reference/specifications.md), [drivers](docs/reference/drivers.md), [reports and exit codes](docs/reference/reports.md), [`proctor diff`](docs/reference/diff.md).
- **Run it in CI:** [Authoritative mode](docs/guides/ci-github.md) and [`examples/github/`](examples/github/README.md).
- **Judge whether to trust it:** [Security model](docs/security-model.md), including [Privacy](docs/security-model.md#privacy).
- **Work on Proctor:** [Development](docs/development.md) and [Architecture](docs/architecture.md).

## Author

This project is maintained by [Bez Moradi](https://github.com/bezmoradi).

## License

Proctor is licensed under [MIT](LICENSE).
