# Proctor

Proctor checks that an application still does what people agreed it should do, and refuses to report a pass when the agreement itself was quietly changed. It exists for codebases where AI coding agents change the code, the tests and the test configuration in the same pull request.

The expected behavior lives in YAML specifications under a protected folder. People approve changes to those files through code-owner review. A trusted CI job runs every listed scenario against the application built from the tested commit, and records what it checked. Proctor itself doesn't approve anything; GitHub's branch protection does, using Proctor's check and report as evidence.

**Status: first draft, pre-release.** Built so far:

- static integrity checks;
- the semantic diff of protected changes;
- HTTP scenarios with exact JSON assertions;
- read-only PostgreSQL checks;
- local mode and authoritative mode.

Temporal and log evidence, time-based checks (`eventually`, `consistently`, `never`) and tables are specified but not built. [`prd.md`](prd.md) is the product requirements document. It includes the decisions, the acceptance tests and the roadmap, with what each phase still lacks. Proctor has no license yet, so it isn't open source until one is chosen.

## Quick start

Needs Go 1.27.1 or later and Git.

```sh
go build -o bin/proctor ./cmd/proctor

# Start the sample API (in memory, no database) in another terminal:
(cd examples/users-api && PORT=18080 make run)

bin/proctor validate --config examples/users-api/proctor/proctor.yaml
bin/proctor run --config examples/users-api/proctor/proctor.yaml \
  --binding examples/users-api/proctor/environments/local.yaml
```

The run waits for `/readyz`, executes scenarios USR-001 to USR-005 and prints `RESULT: PASSED (exit 0, local, non-authoritative)`. Add `--json` for the machine-readable report.

## Commands

| Command | What it does | Exit codes |
|---|---|---|
| `proctor validate` | Parses the configuration, manifest and specifications strictly, and rejects specifications likely to give a misleading pass. Never contacts the application | 0, 3 |
| `proctor integrity --commit REV` | Integrity checks on a commit, read from Git's object database: symlinks, submodules, LFS pointers, diff-hiding attributes, unlisted specifications, more than one CODEOWNERS | 0, 3, 4 |
| `proctor diff --base REV --head REV` | Every changed protected file and scenario, with weakenings (removed assertions, waivers, steps moved into setup) listed first | 0, 4 |
| `proctor run --binding FILE` | Local mode: runs the working tree's specifications against a running application | 0 to 4 |
| `proctor run --authoritative ...` | For the trusted CI job: see below | 0 to 4 |

Exit codes: 0 passed, 1 a scenario failed, 2 an infrastructure error, 3 invalid input, 4 integrity violation. When several apply, 4 wins over 3, 3 over 1, and 1 over 2.

There is no warning level. Every validation finding fails the command. A step can waive a rule only with a written reason (`no_state_check: <reason>`, or `waive: { FIX-05: <reason> }`), and `proctor diff` lists each added waiver as a weakening.

## Authoritative mode

```sh
proctor run --authoritative --config proctor/proctor.yaml \
  --binding "$RUNNER_TEMP/binding.yaml" --pr-head "$PR_HEAD_SHA" --report "$RUNNER_TEMP/report.json"
```

In this mode Proctor:

- reads every protected input from the tested commit, not the working tree;
- takes the base from GitHub's test merge commit, after checking that its second parent is the pull request head;
- lists every protected change as "approval required";
- asks GitHub's API for CODEOWNERS errors at the base;
- verifies the job's GitHub Actions OIDC token against GitHub's published keys;
- reports whether the workflow that ran differs from the base branch's;
- refuses to run unless the image the application was started from is the image built from the tested commit.

It always writes the JSON report and a job summary. A binding inside the checkout is refused. Nothing runs while any of these checks fails.

GitHub must be configured as `prd.md` §6.3 describes:

- CODEOWNERS on the protected folder, all of `.github/`, and the CODEOWNERS file itself;
- required code-owner review, with stale approvals dismissed and no bypass for administrators;
- the workflow's aggregator job as the required status check, pinned to the GitHub Actions app;
- branches up to date before merging.

The reference workflow (§16.2) builds the image in one job and verifies it in another, with a verified, pinned Proctor binary. A third job, `verdict`, is the required check: it fails if any prerequisite failed or was skipped.

## Specifications

```yaml
version: 1
suite: users
scenarios:
  - id: USR-001
    name: A created user can be read back
    steps:
      - id: create
        request: { method: POST, path: /api/v1/users, identity: anonymous, body: { name: "Ada ${run.id}" } }
        expect:
          status: 201
          json:
            $.message: Created user successfully
        save: { user_id: $.data }
      - id: read
        request: { method: GET, path: "/api/v1/users/${user_id}", identity: anonymous }
        expect:
          status: 200
          json:
            $.data: { id: "${user_id}", name: "Ada ${run.id}" }
```

Validation requires:

- a later state check after every request that changes state (EXE-07);
- `${run.id}` in every value a scenario writes and later matches (FIX-05);
- every setup operation to also appear as a verification step (FIX-08).

A database check runs one parameterized, read-only query. Before using the verification role, Proctor checks that the role can only read the objects `proctor.yaml` declares.

## Repository layout

| Path | Contents |
|---|---|
| `cmd/proctor`, `internal/` | The CLI |
| `examples/` | `users-api`, a sample application generated with [gopamin](https://github.com/bezmoradi/gopamin), with its Proctor specifications (see [`examples/README.md`](examples/README.md)), and `github/`, the reference GitHub workflow and CODEOWNERS file |
| `prd.md` | Requirements, decisions, acceptance tests and roadmap |

## Development

```sh
go vet ./...
gofmt -l cmd internal
go build ./...
```

This repository doesn't include Proctor's test suite.
