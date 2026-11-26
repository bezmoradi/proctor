# Authoritative mode

```sh
proctor run --authoritative --config proctor/proctor.yaml \
  --binding "$RUNNER_TEMP/binding.yaml" --pr-head "$PR_HEAD_SHA" --report "$RUNNER_TEMP/report.json"
```

In this mode Proctor:

- reads every protected input from the tested commit, not the working tree;
- takes the base from GitHub's test merge commit, after checking that its second parent is the pull request head, or with `--base` refuses a base that hasn't exactly one merge base with the tested commit (exit 4, INT-09);
- lists every protected change as "approval required";
- asks GitHub's API for CODEOWNERS errors at the base;
- requires the job's GitHub Actions OIDC token and verifies it against GitHub's published keys, so a job without one, such as a pull request from a fork, can't give an authoritative result (MODE-03);
- reports whether the workflow that ran differs from the base branch's;
- refuses to run unless the binding's `image` (or each entry of `images`) gives equal `built_digest` and `running_digest` (REP-11). Proctor compares the two values; the trusted job that writes the binding must read them itself, the built one from its build and the running one from the started container, as the reference workflow does;
- takes each repository driver's `program`, `sha256` and `operations` from the base commit's `proctor.yaml`, and reads its program from `--driver-programs DIR` (default: the checkout); see [Repository drivers](repository-drivers.md);
- refuses a repository driver that isn't pinned for the job's platform or that the commit tracks;
- with `--earlier-reports DIR`, says which earlier attempts of the same workflow run failed a scenario for the same commit and inputs (see [Re-runs](#re-runs)).

It always writes the JSON report and a job summary. A binding inside the checkout is refused. Nothing runs while any of these checks fails.

These checks hold only while the pull request's code can't act as root on the job's host. An application that mounts the host's Docker socket can, and then it can rewrite the report or read the job's tokens; run such an environment off the job's host (see [Adapting it](../../examples/github/README.md#adapting-it)).

GitHub must be configured so that nothing merges without the check and a code owner's approval:

- CODEOWNERS on the protected folder, all of `.github/`, and the CODEOWNERS file itself;
- required code-owner review with at least one required approval (with 0, GitHub doesn't enforce code-owner review), stale approvals dismissed, approval of the most recent push required, and no bypass for administrators;
- the workflow's aggregator job as the required status check, pinned to the GitHub Actions app, so a status posted with a person's token can't satisfy it;
- branches up to date before merging;
- actions pinned by commit SHA, and each job's token limited to the `permissions` it needs.

Code owners and branch protection in a private repository need a paid GitHub plan. [`examples/github/README.md`](../../examples/github/README.md) gives these settings as a branch protection request and the results of testing them.

The reference workflow ([`examples/github/proctor.yml`](../../examples/github/proctor.yml)) builds the image in one job and verifies it in another, with a pinned Proctor release archive whose checksums it checks. A third job, `verdict`, is the required check: it fails if any prerequisite failed or was skipped.

Copy `examples/github/` as its README says, and pin `PROCTOR_VERSION` and `PROCTOR_SHA256` to a release you verified (see [Other ways to install](install.md#other-ways-to-install)). The workflow handles `merge_group` too. It starts the disposable environment from `proctor/verify/` on the base commit, never the pull request's, so merge an environment change before the change that needs it. Its binding marks the environment `disposable: true`.

## Re-runs

Anyone with write access can re-run a failed job, and a passing re-run replaces the failure in the merge check. Proctor doesn't block that, but it makes it visible. The reference workflow uploads each attempt's report as the artifact `proctor-report-<attempt>`, and on a re-run downloads the earlier ones into a folder outside the checkout and passes it with `--earlier-reports`. Proctor then lists, in the report's `earlier_attempts`, on the terminal and in the job summary:

- each earlier attempt that recorded a scenario FAIL for the same run, tested commit and input hashes, with the failing scenario IDs (an ERROR or an integrity violation doesn't count);
- each earlier attempt without such a report, as `unknown`, never as clean;
- each file in the folder it ignored, with the reason: another run, commit or input hashes, a later attempt, a symbolic link or anything but a regular file, more than 64 MiB, or not an authoritative report.

The result and exit code don't change. Proctor needs no token or API call for this. A folder inside the checkout, or the flag in local mode, exits 3.

## Change kind

A pull request can claim what kind of change it is, and Proctor fails it when its protected changes don't fit the claim:

| Kind | Fails when |
|---|---|
| `refactor`, `docs` | any protected file changed |
| `fix` | anything but additions: a weakening, a changed expected value or request, a moved assertion, a specification file that doesn't parse, or a changed protected file other than specification files, new manifest entries and new approved files (so a `proctor.yaml` change fails a fix) |
| `feature`, or no kind | never; every protected change still needs code-owner approval |

```sh
proctor diff --base main --head HEAD --change-kind fix
```

The kind is only a claim, so it can add a failure and never removes one: `feature` keeps today's rules. `proctor diff` exits 4 on a mismatch and lists each one with its place; `proctor run --authoritative` reports each as a `CHG-02` integrity violation and runs nothing. An unknown kind exits 3. In the reference workflow ([`examples/github/proctor.yml`](../../examples/github/proctor.yml)) the label `proctor:refactor`, `proctor:docs`, `proctor:fix` or `proctor:feature`, in any letter case, sets the kind; two `proctor:` labels, or one naming no kind, fail the first job before anything is built. The report's `change_kind` and the job summary record the kind and where it came from (`--change-kind-source label`). Anyone who can label the pull request can pick the kind, and Proctor judges only the protected files, not the application code.

## The OIDC token

- The OIDC token must have audience `proctor` and be signed with RS256 by a key of at least 2048 bits, with 60 s of clock skew allowed; a missing token, one that doesn't verify, and one naming another commit, repository, run or attempt exit 4 (MODE-03).
