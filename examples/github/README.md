# Reference GitHub configuration

`proctor.yml` and `CODEOWNERS` are the reference workflow (prd.md §16.2) and code-owner file (§6.3) for OD-01 option B. They were confirmed on 2026-11-14 in a temporary public repository, `bezmoradi/proctor-trust-test-20261114`, with Proctor 0.0.0-phase0+ee924a9 and a small in-memory users API generated with [gopamin](https://github.com/bezmoradi/gopamin). The results below come from that test.

To use them:

1. Copy `proctor.yml` to `.github/workflows/` and `CODEOWNERS` to `.github/`.
2. Replace the owners and the two `PROCTOR_` values.
3. Protect the target branch with the settings below.

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
- **Option A, a ruleset-required workflow** (AT-W-11). It needs GitHub Enterprise Cloud.
- **Merge queues**, `merge_group` runs (§6.3), earlier attempts (REP-10 isn't built), fork pull requests (AT-W-19), and a missing or unparseable report from a workflow that still runs Proctor (AT-W-18).
- **AT-W-01 and AT-W-02 in full.** The build didn't try to replace the pinned binary, and the planted scripts were shown not to run only by the executed binary's digest.
