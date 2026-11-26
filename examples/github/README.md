# Reference GitHub configuration

`proctor.yml` and `CODEOWNERS` are the reference workflow and code-owner file for running Proctor from a workflow in the repository, with its `verdict` job as the required check. `verify/` is an example of the disposable environment's definitions. The trust test below, from 2026-11-14, ran an earlier form of the workflow in a temporary public repository with Proctor 0.0.0-phase0+ee924a9 and a small in-memory users API.

To use them:

1. Copy `proctor.yml` to `.github/workflows/`, `CODEOWNERS` to `.github/`, and `verify/` to `proctor/verify/`, so `/proctor/` in CODEOWNERS covers it.
2. Replace the owners. Set `PROCTOR_VERSION` to a release, and `PROCTOR_SHA256` to the line for `proctor-<version>-linux-amd64.tar.gz` in that release's `SHA256SUMS`, after you have verified the archive's signature (see [Other ways to install](../../docs/guides/install.md#other-ways-to-install)).
3. Set `PROCTOR_IDENTITIES` to the `bearer` identities `proctor/proctor.yaml` declares, and adapt `verify/compose.verify.yaml` to the application: its dependencies, port 8080 and its issuer setting. Pin every image there by digest.
4. Protect the target branch with the settings below.

## What the workflow does

- **kind** maps the pull request's one `proctor:` label to a change kind (CHG-03). A `merge_group` run has no labels, so it passes no kind; the kind is checked on the pull request's own runs.
- **build** builds the application image from the tested commit and outputs its digest. It's the only job that runs the pull request's code on its host, and it has only `contents: read`.
- **verify** (`contents: read`, `id-token: write`):
  - finds the base: for `pull_request`, the test merge's first parent, after checking that its second parent is the pull request head; for `merge_group`, the event's `base_sha`, which must be an ancestor of the tested commit (the checkout fetches full history for that).
  - takes `proctor/drivers/` **from the base commit** into `$RUNNER_TEMP/base-drivers/`, outside the checkout, and builds each repository driver there, `proctor/drivers/<name>/cmd/proctor-driver-<name>` into `proctor/drivers/<name>/bin/`, with the fixed command from [Repository drivers](../../docs/guides/repository-drivers.md), written in the workflow rather than taken from a script. Proctor reads the programs from that folder (`--driver-programs`) and their `program`, `sha256` and `operations` from the base's `proctor.yaml`, so the pull request's driver code is never built or run here. A driver change, a new driver included, takes effect once it's merged, so merge it before the specifications that need it.
  - loads the image and checks its ID against the build's digest.
  - takes `proctor/verify/` **from the base commit**, never from the pull request, and refuses anything there that isn't a regular file. An unapproved change to the environment, such as a privileged container or a host mount, never runs on this host with this job's token. A change to `proctor/verify/` takes effect once it's merged.
  - starts the environment with `docker compose up --wait` under `env -i`, so no variable of the job, such as the OIDC request token, reaches a container. Then it checks that the running container's image is the built one.
  - downloads the release archive, checks its SHA-256 against the pin right before use, extracts it under `$RUNNER_TEMP` (outside the checkout), checks the archive's own `SHA256SUMS`, and logs the SHA-256 of the `proctor` it runs. Its drivers sit beside it, as INT-17 requires.
  - asks the environment's identity provider for each identity's token, masks it, and passes it to Proctor in an environment variable that the binding names. The binding (mode 0600, under `$RUNNER_TEMP`) holds variable names, never tokens, and marks the environment `disposable: true`.
  - runs Proctor with `--pr-head` for a pull request, or `--base` for a merge group, and uploads the report, also when Proctor failed. Finally it tears the environment down and deletes the binding.
- **verdict** is the required check. It runs with `if: always()` and no permissions, downloads this attempt's report and fails when any job didn't succeed, or when the report is missing or isn't one JSON object that is an authoritative, passing report (`result: passed`, `exit_code: 0`) for this commit, run and attempt with a verified workflow identity (`trust.workflow_identity` an object, not `unverified`) (REP-13, MODE-03). A pull request from a fork gets no OIDC token, so Proctor refuses it (exit 4, MODE-03) and the verdict fails.

A merge queue needs `merge_group` in the workflow and the `verdict` check required in the branch protection or ruleset.

Because the environment comes from the base, `proctor/verify/` must be on the target branch before `verdict` is required: a pull request whose base has no `proctor/verify/compose.verify.yaml` fails. An application change that needs a new environment fails until the environment change merges, so merge the environment change first in its own pull request, and keep it working with the application version already on the base.

## Adapting it

### An application that controls the runner's Docker

The workflow's trust rests on the pull request's code never running as root on the verify job's host. An application that mounts the host's Docker socket, for example to start a container per session, breaks that: whoever controls the Docker daemon is root on the runner. The pull request's application code, built into the image, can then read the job's tokens, the OIDC request token included, from the runner's processes, replace the verified `proctor` under `$RUNNER_TEMP`, or rewrite the report, so a hostile pull request can make `verdict` pass. Taking the environment from the base doesn't help, because the base's environment itself hands the socket to code from the pull request.

Proctor can't make that host trustworthy, and its report can't tell. Keep the socket away from the job's host: start the environment inside a virtual machine on the runner, or on another machine the job reaches over the network, and run Proctor, its report and `verdict` outside it, with no token of the job inside. The docker driver then reads that machine's daemon. Until then, treat the result as non-authoritative.

### An environment started by scripts

When the environment comes up through scripts, which sign in, write the binding and need the tested files, follow the pattern the workflow uses for repository drivers: take the scripts **from the base commit** into `$RUNNER_TEMP`, outside the checkout, with `git archive "$BASE" -- <scripts folder>`, and run them under `env -i` with only the variables they need, plus one naming the checkout, such as `CHECKOUT="$GITHUB_WORKSPACE"`. The pull request can then change what the scripts read, its application, but not what they do. A change to the scripts takes effect once merged, like a change to `proctor/verify/`.

### Labels that don't name a kind

The workflow runs on `labeled` and `unlabeled` so a `proctor:` label can set the change kind, so any label, even one unrelated to Proctor, starts the whole run again. Don't skip the jobs for such a label: GitHub counts a skipped required check as passing, so a skipped `verdict` would turn a failed commit green. To avoid the cost, move the kind into a workflow of its own that only runs `proctor diff --base "$BASE" --head "$TESTED" --change-kind "$KIND"` (no stack, a few seconds), require its check too, and drop the label triggers and `--change-kind` from this one. The authoritative report then records no `change_kind`, and the kind is checked by `proctor diff` alone.

## Branch protection

```json
{
  "required_status_checks": { "strict": true, "checks": [{ "context": "verdict", "app_id": 15368 }] },
  "enforce_admins": true,
  "required_pull_request_reviews": {
    "dismiss_stale_reviews": true,
    "require_code_owner_reviews": true,
    "required_approving_review_count": 1,
    "require_last_push_approval": true
  },
  "restrictions": null,
  "allow_force_pushes": false,
  "allow_deletions": false
}
```

Apply it with `gh api -X PUT repos/<owner>/<repo>/branches/main/protection --input <file>`. App ID 15368 is GitHub Actions, so a status posted with a person's token can't satisfy the check.

**`required_approving_review_count` must be at least 1.** With 0, GitHub doesn't enforce "Require review from Code Owners". In the test, the pull requests that skipped verification (T05) and weakened a specification (T08) both became `CLEAN`, so they could have been merged. With 1, every pull request needs an approval, including the ones that change no protected file.

Branch protection and rulesets on a private repository need a paid plan. On GitHub Free, the API refused both with "Upgrade to GitHub Pro or make this repository public".

## Results

The settings above were active, except where a row says so.

| Test | Pull request | Checks | Merge state | Evidence |
|---|---|---|---|---|
| Control: documentation only | T01 (#1) | build, verify, verdict pass | `BLOCKED`, `REVIEW_REQUIRED` | With reviews relaxed (approvals 0, no last-push approval), `CLEAN`, and it merged. Report: `passed`; identity verified, with `workflow_ref`, `workflow_sha`, `job_workflow_ref`, `job_workflow_sha`, `sha`, `event_name`, `run_id` and `run_attempt` equal to the run's (AT-W-13); tested = the test merge, base = its first parent, head = its second (AT-W-12); built and running image digests equal (REP-11) |
| Behavior defect: an unknown user returns 200 | T02 (#2) | verify, verdict fail | `BLOCKED` | Report: `failed`, exit 1, USR-003 and USR-005 FAIL |
| A `verdict` status posted with a personal token on T02's head | #2 | verdict status success, check run failure | `BLOCKED` with reviews relaxed | AT-W-10 |
| Broken build | T03 (#3) | build fails, verify skipped, verdict fails | `BLOCKED` | AT-W-05 |
| Workflow adds `if: false` to the verification job | T04 (#4) | verify skipped, verdict fails | `BLOCKED` | AT-W-06 |
| The above, and the verdict always succeeds | T05 (#5) | verdict passes | `BLOCKED`, `REVIEW_REQUIRED` | AT-W-07: only code-owner review stops it. `CLEAN` with approvals set to 0 (see the warning above) |
| Harmless workflow edit; Proctor still runs | T06 (#6) | all pass | `BLOCKED`, `REVIEW_REQUIRED` | Report: `workflow_changed: true`, `approval_required: true`, change listed (AT-W-14). The job summary's "not authoritative until the workflow change is approved" line is covered by Proctor's black-box tests, which aren't in this repository; this run's summary wasn't read, because GitHub's API doesn't return it |
| The workflow downloads a binary that doesn't match the pin | T07 (#7) | verify, verdict fail | `BLOCKED` | Log: `sha256sum: WARNING: 1 computed checksum did NOT match`; no report artifact (AT-W-03) |
| A specification drops an assertion | T08 (#8) | all pass | `BLOCKED`, `REVIEW_REQUIRED` | Report: `approval_required: true`, weakening `assertion_removed USR-003 json $.error` |
| `bin/proctor` and `./proctor-cli` planted in the checkout | T09 (#9) | all pass | `BLOCKED`, `REVIEW_REQUIRED` | Log: `executing proctor with sha256 b76eaeda…`, equal to the pin; the planted scripts never ran (AT-W-02, by digest only) |
| Another pull request merges first | #6, #8, #9 after #1 merged | – | `BEHIND` | AT-W-08 |
| An earlier commit adds a workflow that posts a passing `verdict`; the last commit removes it and keeps a defect | T10 (#10) | commit 1: fake `verdict` pass and real `verdict` fail on one SHA; commit 2: real `verdict` fails | `BLOCKED` at both commits, with reviews relaxed | AT-W-09. Both check runs come from GitHub Actions, so app pinning can't tell them apart; a failing run with the same name still blocks |

## Not shown by this test

- **An approved pull request becoming mergeable.** The test had one account, and GitHub doesn't let an author approve their own pull request. A second account in the code-owner team is needed to show `REVIEW_REQUIRED` turning into `CLEAN` after approval, and stale approvals being dismissed by a new push.
- **A ruleset-required workflow** instead of one in the repository (AT-W-11). It needs GitHub Enterprise Cloud.
- **Merge queues**, `merge_group` runs, fork pull requests (AT-W-19), and a missing or unparseable report from a workflow that still runs Proctor (AT-W-18). The workflow now triggers on `merge_group` and its verdict checks the report, but neither has run on GitHub.
- **The release archive, the disposable environment and its identities.** The test downloaded a bare `proctor` and started the image with `docker run`. The workflow now downloads the release archive with its drivers, starts `proctor/verify/compose.verify.yaml` from the base commit and gets tokens from the environment's identity provider. Its steps were run on a Mac with a stub identity provider, never on GitHub, and Compose's `up --wait` with the example definition never ran at all.
- **Earlier attempts** (REP-10, AT-W-04). The workflow changed after this test: each attempt uploads its report as `proctor-report-<attempt>`, a re-run downloads the earlier ones with `pattern: proctor-report-*` and passes them to `--earlier-reports`, and the verify job no longer has `actions: read`. Proctor's side is tested locally with the run identity set by hand. Not yet shown on GitHub: a re-run whose download returns the earlier attempt's artifact, and a verify job that reads the build job's `image` artifact without `actions: read`.
- **The binding's form.** The test used a binding with `target:`; current Proctor refuses that key, and the workflow now writes `drivers: { http: … }`, `identities` and `disposable: true` instead. That form hasn't run on GitHub yet.
- **AT-W-01 and AT-W-02 in full.** The build didn't try to replace the pinned binary, and the planted scripts were shown not to run only by the executed binary's digest.
