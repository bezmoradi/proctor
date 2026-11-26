# Proctor documentation

Each topic has one page. Guides say how to do a task; reference pages list every key, flag, field and rule for lookup.

## Start

- [README](../README.md): what Proctor is, how it works, and the quick start.
- [Installation](guides/install.md): install, verify, update, pin a release in CI, and `requires`.
- [`examples/sample/`](../examples/sample/README.md): a users API checked end to end, to see Proctor working before you adopt it.

## Write and keep specifications

- [Writing specifications](guides/specifications.md): scenarios, sampling over time, step timeouts, tables and coverage, cleanup, environment invariants and the application-log check.
- [Approved transcripts and logs](guides/approvals.md): approve a scenario's whole story or its log once, and compare later runs with it.
- [Mutation testing](guides/mutation-testing.md): check that the specifications catch defects you declare, generate candidate mutants, and read the HTML report.
- [Repository drivers](guides/repository-drivers.md): keep your own driver in the repository, pinned by checksum.

## Run it in CI

- [Authoritative mode](guides/ci-github.md): what the trusted job checks, how GitHub must be configured, re-runs and change kinds.
- [`examples/github/`](../examples/github/README.md): the reference workflow, CODEOWNERS file and branch protection settings, and how to adapt them.

## Look something up

- [Commands](reference/cli.md): every command, flag and default.
- [Configuration](reference/configuration.md): `proctor.yaml`, the binding, and environment variables.
- [Specification format](reference/specifications.md): validation rules, matchers, paths, references and state checks across drivers.
- [Drivers](reference/drivers.md): each official driver's operations, facts, policy, binding and limits, and the simulators.
- [Reports and exit codes](reference/reports.md): what each JSON report holds.
- [`proctor diff`](reference/diff.md): every kind of change it lists, and which ones are weakenings.

## Trust and internals

- [Security model](security-model.md): what Proctor reads, writes and sends.
- [Architecture](architecture.md): the repository's layout.
- [Development](development.md): build from source, the self-check, and releasing.
