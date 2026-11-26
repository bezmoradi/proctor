# Security model

Proctor's job in CI is to refuse a pass when the agreed behavior was changed without approval, or when the run itself can't be trusted. It is one of three parts: Proctor's checks, GitHub's branch protection and code-owner review, and the job's host. This page says which part holds what. [Authoritative mode](guides/ci-github.md) and [`examples/github/`](../examples/github/README.md) give the setup and the results of testing it.

## What Proctor enforces

In every mode:

- Every validation finding fails the command; there is no warning level. A specification likely to give a misleading pass is refused, such as an act without a state check (EXE-07), a write without `${run.id}` (FIX-05) or a destructive request on someone else's data (EXE-09), unless a written waiver says why, and `proctor diff` lists each waiver as a weakening. See [Rules](reference/rules.md).
- `proctor` refuses an official driver whose checksum it doesn't hold, and a repository driver whose checksum differs from its pin (INT-17).
- Reports, transcripts and approved logs show the credentials, secrets and redacted values Proctor knows as `[redacted]`, and a step whose telemetry holds one fails (`secret_in_telemetry`).

In authoritative mode, `proctor run --authoritative` also:

- reads every protected input from the tested commit's Git objects, never the working tree, and refuses symbolic links, submodules, LFS pointers and diff-hiding Git attributes there, unlisted specifications (INT-05) and a second CODEOWNERS file;
- lists every protected change against the base as "approval required", weakenings first, and checks the claimed change kind (CHG-02);
- verifies the job's GitHub Actions OIDC token against GitHub's published keys and refuses one naming another commit, repository, run or attempt, so a job without a token, such as a pull request from a fork, can't give an authoritative result (MODE-03); it reports whether the workflow that ran differs from the base branch's (`trust.workflow_changed`);
- asks GitHub's API for CODEOWNERS errors at the base (INT-11);
- refuses a binding inside the checkout (MODE-01), and refuses to run unless the binding's built and running image digests are equal (REP-11);
- takes each repository driver's pin and operations from the base commit, and refuses one without a pin for the job's platform or one the commit tracks (INT-17, INT-21);
- runs nothing while any of these checks fails, and always writes the JSON report and a job summary.

Drivers prove what they can before the first scenario: the postgres driver that its role can only read the declared objects (DB-04), and the redis driver that its user can't write (REDIS-05, which reports `unproven` and goes ahead when it can't tell). The docker driver reports `read_only: unproven`, since Docker has no read-only socket, and the cli driver `sandbox: unproven`.

## What GitHub and the repository must enforce

Proctor approves nothing. The merge decision is GitHub's, so these settings carry as much weight as Proctor's checks (the full list, and why each is needed, is in [Authoritative mode](guides/ci-github.md)):

- CODEOWNERS on the protected folder, all of `.github/`, and the CODEOWNERS file itself, so a change to the specifications or to the workflow that runs Proctor needs a code owner's approval;
- required code-owner review with at least one approval, stale approvals dismissed, approval of the latest push required, and no bypass for administrators;
- the workflow's `verdict` job as the required check, pinned to the GitHub Actions app, and branches up to date before merging;
- actions pinned by commit SHA and each job's token limited to what it needs.

## What it assumes

- **The pull request's code can't act as root on the job's host.** Proctor compares digests and reads its inputs from Git, but code that controls the host can rewrite the report or read the job's tokens. An application that mounts the host's Docker socket breaks this; see [Adapting it](../examples/github/README.md#adapting-it).
- **The trusted job writes the binding and reads the digests itself**, the built one from its build and the running one from the started container. Proctor only compares the two values.
- **The environment and repository drivers come from the base commit**, as the reference workflow takes them, so a pull request can't change what runs on the job's host before it is reviewed and merged.
- **Repository drivers and cli commands are reviewed like application code.** They run as Proctor's user, with that user's files and network; a pin proves which program runs, not what it may do.

## What it doesn't guarantee

- **That the specifications are right or complete.** They are the agreement people approved. [Mutation testing](guides/mutation-testing.md) measures how many declared defects they catch.
- **That a person read what they approved.** `proctor approve` can't tell whether a person answered; the code-owner review of the committed files is the approval.
- **That a passing re-run hides nothing.** A re-run that passes replaces the failure in the merge check. Proctor lists earlier attempts that failed a scenario for the same commit and inputs (see [Re-runs](guides/ci-github.md#re-runs)), and the result doesn't change.
- **That a change kind is honest.** Anyone who can label the pull request picks it, and Proctor judges only the protected files against it, not the application code.
- **That nothing happened between samples.** `consistently` and `never` check at sample times only (TIME-05).
- **Anything from a local run.** Local results always say `local, non-authoritative`.

## Privacy

Proctor runs on your own machine or CI runner. Its author runs no server for it and receives none of your data, and it sends no telemetry.

- **What it reads:** your repository's working tree or Git objects, the binding, the environment variables the binding names, the application's log file, or the records at its `app_logs.otel` receiver, when `app_logs` is set, and the answers of the targets your specifications check.
- **Where it writes:** reports to stdout and the `--report` file (mode 0600); received transcripts and logs to a new 0700 folder under `$TMPDIR/proctor-received/<project>/`, or `--received`; approved files under `proctor/approved/` only when you answer `y` to `proctor approve`; mutant copies under `$TMPDIR`; and the files you name for `mutate html` and `mutants generate`. A run scrubs the credentials, secrets and redacted values it knows from what it writes.
- **What leaves the machine:** `validate`, `integrity`, `diff` and a local `run` ask `https://api.github.com/repos/bezmoradi/proctor/releases/latest` for the latest release, sending only `proctor/<version>` as the User-Agent; set `PROCTOR_NO_UPDATE_CHECK=1` to turn it off (it is off when `CI` or `GITHUB_ACTIONS` is set, and in authoritative mode). In authoritative mode it verifies the job's OIDC token against GitHub's published keys and asks GitHub's API for CODEOWNERS errors with `GITHUB_TOKEN`. Drivers, and the app-log check with `app_logs.otel`, reach only the targets in the binding. The simulators keep what they receive in memory and send nothing on. `mutate` runs your `up` and `down` scripts, and `mutants generate --build-check` runs your `go` toolchain, which may download modules as your Go settings allow.
- **Stopping it:** delete the reports, the `proctor-received` folders and any `mutate` leftovers its report lists; Proctor keeps no other state.
