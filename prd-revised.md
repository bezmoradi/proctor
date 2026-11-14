# Proctor — Product Requirements (Revised)

**Status:** Draft, revision 2, for review. Not approved. `prd.md` stays the reference until this revision is approved.
**Revision of:** `prd.md` (initial PRD). Revision 1 (2026-11-14) followed an adversarial review; revision 2 (2026-11-14) applies the QA engineer's review of revision 1. `prd-review-changes.md` lists both.
**Companion documents:** `prd-review-changes.md` (what changed and why), `acceptance-tests.md` (the observable test for each requirement), `runner-evaluation.md` (the decision to build Proctor's own runner, and what was learned from Venom, Hurl and Step CI), `open-decisions.md` (decisions still to make).

**Tagline:** Verify behavior, not implementation.
**Project type:** Open-source, language-independent behavioral verification and integrity system.
**Initial implementation language:** Go.
**Initial specification format:** YAML.
**Initial targets:** HTTP/REST APIs, with evidence from PostgreSQL, Temporal and structured application logs.

---

# 1. Summary

Proctor produces evidence that a specific application version was checked against a specific behavioral contract, and lists every change to that contract for human review.

AI coding agents can change application code, tests and test configuration in the same change, so a passing test suite no longer shows that the software still meets its original requirements. Proctor separates the contract from the implementation:

- Humans own the expected behavior, written as YAML specifications listed in a protected manifest.
- AI agents may change the implementation and may propose changes to the specifications.
- An authoritative run in trusted CI executes every required scenario against the application built from the candidate commit, and records exactly what it verified.
- The source-control platform requires human approval of any change to a protected verification input, and blocks merges without a passing authoritative run.

Proctor does not enforce approval by itself and cannot see approvals in v1. A local CLI cannot stop an agent that has shell access on the same machine. Proctor detects and lists changes, refuses inconsistent or incomplete verification, executes the contract and produces evidence; the platform and the trusted CI configuration make that evidence binding.

The central promise:

**AI may change the implementation and propose new expected behavior. It must not silently redefine the trusted requirements or manufacture an authoritative passing result.**

Proctor does not guarantee bug-free software and does not detect every AI error. It makes incorrect behavior harder to merge unnoticed, within the limits stated in §4.4.

---

# 2. The problem

A developer asks an AI coding agent:

"Implement project creation. A user must not be able to access another user's projects."

The agent introduces an authorization defect. It then writes or edits tests that check only that a project can be created and retrieved. All tests pass, and the security requirement is violated.

The agent can change both the implementation and the tests that judge it, so the tests share the implementation's mistakes. Protecting the test files alone is not enough. The same agent can usually also change:

- which specification files are discovered;
- the target URL, so requests reach a stub;
- database, Temporal and log-source connections;
- the Docker and Compose files that define what starts, and the files they reference;
- the build, which runs on the same machine as the verifier unless isolated;
- the CI workflow that decides whether verification runs, and whether its job is skipped;
- Git attributes that change what a checkout writes or what a reviewer sees;
- the verification executable;
- the application, so it behaves differently under test conditions.

Each of these can turn a failing contract into a passing result. Proctor's design covers the whole verification path, not only the YAML.

---

# 3. Users and outcomes

**Primary users:** development teams that let AI coding agents change HTTP services, and the human reviewers who approve those changes.

**Outcomes this release must achieve:**

- **O-1.** A change that weakens a required behavior cannot merge without approval from a designated code owner.
- **O-2.** A change that breaks a required behavior fails the authoritative check, and the pull request cannot merge until the check passes on the result that will merge.
- **O-3.** A reviewer can see what a passing result covered: which commits, which specifications, which runner, which evidence, and which protected inputs changed.
- **O-4.** Agents and developers get local feedback from the same specifications, clearly marked as non-authoritative.

**How success is shown:** the tests in `acceptance-tests.md` pass against the reference GitHub configuration, including the outcome tests AT-O1 and AT-O2, and the example applications in three languages pass the same specifications. Adoption metrics are not defined for this release.

---

# 4. Threat model and trust boundaries

## 4.1 Threat addressed

The primary threat is accidental or agent-induced weakening of requirements or verification. Assume an AI coding agent can:

- read the repository;
- change application code, ordinary tests, unprotected configuration, build scripts, Dockerfiles and Compose files;
- run shell commands and install or invoke tools;
- change any file on its working branch, push it, and open pull requests;
- re-run CI jobs if its token or account has write access;
- run local verification repeatedly until it passes.

The agent may misunderstand a requirement or optimize for passing checks instead of preserving the intended behavior.

## 4.2 What Proctor must not assume

- Local file permissions stop an agent from changing specifications.
- A Git commit shows that a human approved a specification.
- A file is trusted because it sits outside the application source directory, or because it is read-only.
- A CI workflow is trusted because it exists in the repository. A workflow triggered by a pull request runs the pull request's own version of the workflow file (§6.3).
- A green required check means the verification ran. GitHub reports a job skipped by a condition, or by a failed prerequisite, as successful.
- The tree that was verified is the tree that merges. A check computed before the target branch moved stays green.
- A CODEOWNERS file protects what it appears to protect. GitHub skips invalid lines and unknown owners silently.
- `git diff` shows every change to a protected file. Shallow clones, rename detection, symlinks, submodules and `.gitattributes` can hide changes.
- A passing local run is authoritative.
- The application cannot recognize that it is under test.
- The `proctor` executable on the verification machine is the real one, if pull-request code ran on that machine first.
- The specification set is complete because a glob matched some files.
- A database view, schema or role is trusted because it is read-only. The application's migrations create the tables, own them and decide their grants.

## 4.3 Trust boundary

The authoritative run is controlled from outside the change being evaluated. Inputs fall into three groups:

| Group | Inputs | Who controls them in an authoritative run |
|---|---|---|
| Trusted, supplied by the run | Proctor executable and version, the verification workflow, the path to `proctor.yaml`, environment bindings (target URL, connection strings, credentials, User-Agent), test identity credentials | Trusted CI configuration that the pull request cannot change |
| Protected, stored in the repository | Everything under the protected root (`proctor/` by default): manifest, specifications, `proctor.yaml`; the verification dependency definitions and every file they reference; all of `/.github/`; CODEOWNERS; `.gitattributes`, `.gitmodules`, `.lfsconfig` | The repository, but any change requires code-owner approval enforced by the platform |
| Subject under test | Application source, its build, its migrations, tables, views and grants | The change being evaluated |

The application is built in a separate job, and the verification job never runs pull-request code on its own host (§16.2).

```text
 Trusted CI configuration           Protected repository inputs
 (not changeable by the PR)         (changes need code-owner approval)
 - verification workflow            - proctor/ (manifest, specs, policy)
 - pinned Proctor + runner          - verification dependency definitions
 - config path, bindings, secrets   - /.github/, CODEOWNERS, .gitattributes
            |                                   |
            |        +-----------------+        |
            |        |  Build job      |        |
            |        |  builds the PR  |        |
            |        |  application,   |        |
            |        |  outputs image  |        |
            |        |  digest         |        |
            |        +--------+--------+        |
            |                 | image digest    |
            v                 v                 v
           +---------------------------------------+
           |  Verification job (no PR code on host)|
           |  starts disposable environment with   |
           |  the image by digest; runs pinned     |
           |  Proctor against it                   |
           +-------------------+-------------------+
                               |
                               v
                Provenance-bound report (artifact)
                               |
                               v
       Aggregator job: required status, never skipped
                               |
                               v
         Merge rule: up-to-date or merge queue, code-owner review
```

## 4.4 Limits of the threat model

Proctor reduces these risks; it does not remove them.

- **An application written to detect the verifier** (a test user, an environment variable, a header, a hostname, a request sequence) can behave differently under test. Mitigations: no Proctor-specific headers; the User-Agent comes from the binding; `traceparent` is sent only on requests whose trace ID a check uses (LOG-02); the artifact built from the candidate commit is the one tested; identities and inputs should vary.
- **A malicious actor who controls both the application and the authoritative environment** is out of scope. Independent security testing is still needed.
- **Platform re-runs.** Anyone with write access can re-run a failed job, and a later passing attempt replaces the failed one in the merge check. Proctor reports earlier failed attempts for the same commit (REP-10) but does not block on them in v1 (OD-15).
- **Option B workflow trust** (§6.3) depends on reviewers and on no workflow run from any commit of the pull request producing the required check name. GitHub does not enforce that.
- **Sampling.** `consistently` and `never` cannot see changes between samples or after the window (TIME-05).
- **Changes made and reverted during the triggering request** are not observable (OD-08).

---

# 5. Execution modes

Proctor has two modes. Every report states its mode.

## 5.1 Local mode

For fast feedback to developers and agents, troubleshooting and specification authoring.

- Uses working-tree specifications and a local binding file, including uncommitted changes.
- May use local Docker Compose and local dependencies.
- The terminal header, the final result line and the JSON `mode` field say `local`, and the terminal output says "non-authoritative".

A local result is feedback, never evidence of conformance.

## 5.2 Authoritative mode

For establishing whether a specific commit satisfies the contract.

Proctor cannot tell by itself whether it is running in a trusted job. Authoritative status comes from the platform configuration that runs Proctor. Proctor's `mode` records how it was invoked, plus the workflow identity it can verify:

- **MODE-01.** Authoritative mode is selected with `proctor run --authoritative --config <path> --binding <path>`. The trusted workflow supplies both paths; where they live is OD-11. `--config` names `proctor.yaml` inside the protected root, read from the Git object database (MODE-02). `--binding` must resolve to a path outside the repository checkout; a binding inside it is a configuration error (exit 3). In this mode Proctor never searches for configuration.
- **MODE-02.** In authoritative mode Proctor reads protected inputs from the Git object database at the tested commit, not from the working tree (INT-07).
- **MODE-03.** Proctor records the workflow identity from the GitHub Actions OIDC token when the job grants `id-token: write`: `workflow_ref`, `workflow_sha`, `job_workflow_ref`, `job_workflow_sha`, `sha`, `event_name`, `run_id`, `run_attempt`. Without the token, the report's `trust.workflow_identity` is `unverified`.
- **MODE-04.** Under OD-01 option B, the workflow lives in the repository: if the workflow file that ran (`workflow_ref`, `workflow_sha`) differs from the same path at the base commit, the report's `trust.workflow_changed` is `true` and the job summary says the result is not authoritative until that change is approved. Under option A, the workflow lives in another repository and is pinned by the ruleset; the report records `job_workflow_sha` and sets `trust.workflow_changed` to `not_applicable`.
- **MODE-05.** Authoritative mode enforces the manifest (§7.2), always writes the JSON report whatever `report.formats` says, and checks the application image digest (REP-11).

The merge rule reads the job's status, not the report. The report is audit evidence that shows what the status covered.

---

# 6. Approval and enforcement

## 6.1 Responsibilities

For v1, the source-control platform and trusted CI enforce approval. Proctor is not a substitute for branch protection.

| Responsibility | Owner |
|---|---|
| Define and approve expected behavior | Human code owners |
| Change the implementation; propose specification changes | AI agent or developer |
| Detect changes to protected inputs against the base commit | Proctor |
| Validate the manifest, the protected inputs and the specification set | Proctor |
| Detect specifications or scenarios that didn't start | Proctor |
| Check that CODEOWNERS has no errors | Proctor, through GitHub's API (INT-11) |
| Explain changes with a semantic diff | Proctor |
| Execute the suite and collect evidence | Proctor |
| Build the application in isolation; never skip the required job | Trusted workflow (§16.2) |
| Require code-owner review of protected inputs | Source-control platform |
| Require the check to pass on the result that will merge | Source-control platform |
| Keep the verification workflow out of the pull request's control | Platform and CI configuration (OD-01) |

## 6.2 The integrity rule

**Any change to a protected verification input requires approval.** Proctor never decides that a change is harmless enough to skip approval. The semantic diff (§7.5) explains changes to reviewers; it is not the gate.

Two distinct outcomes:

- **Integrity violation** (exit code 4): the verification set or its inputs are inconsistent, incomplete or unsafe to trust. Examples: a manifest entry points to a missing file; a protected input is a symlink; CODEOWNERS has errors; Git can't produce the base commit; a scenario the parser found in a listed specification didn't start. An authoritative run with an integrity violation never passes.
- **Protected change detected:** protected inputs differ from the base commit. Proctor lists every change in the report and the job summary. Approval comes from the platform's code-owner review, not from Proctor. Whether Proctor should also fail until it can see that approval is open (OD-02).

## 6.3 Reference GitHub configuration

Proctor documents and tests one reference configuration on GitHub. Other platforms are out of scope for v1. The settings below were checked against GitHub's documentation on 2026-11-14 (sources at the end of this section).

**Plan requirements.** Code owners and protected branches in private repositories need a paid plan (GitHub Pro, Team or Enterprise). Several stronger settings need GitHub Enterprise Cloud, as marked.

Required settings:

- **Protected target branch** with pull requests required.
- **CODEOWNERS** covering the protected root, the verification dependency definitions, all of `/.github/`, `.gitattributes`, `.gitmodules`, `.lfsconfig`, and the CODEOWNERS file itself. GitHub: "you also need to define an owner for the CODEOWNERS file itself". Pull requests use the base branch's CODEOWNERS for review requests. Keep exactly one CODEOWNERS file; GitHub uses the first it finds in `.github/`, the root, then `docs/`.
- **Require review from Code Owners.** A CODEOWNERS file alone requests reviews; it does not require them.
- **Dismiss stale pull request approvals when new commits are pushed**, and **Require approval of the most recent reviewable push** (approval must come from someone other than the pusher).
- **Do not allow bypassing the above settings.** Without it, branch protection doesn't apply to administrators. In rulesets, keep the bypass list to named roles or teams.
- **Required status check** on the aggregator job (§16.2), with its expected source pinned to the GitHub Actions app. Pinning doesn't stop a workflow from producing the check, but it stops statuses posted with a person's or another app's token: "If the status is set by any other person or integration, merging won't be allowed."
- **The check must pass on the result that will merge:** enable "Require branches to be up to date before merging", or use a merge queue with the verification workflow triggered on `merge_group`. Merge queues are available in public repositories owned by organizations and in private repositories on GitHub Enterprise Cloud.
- **Pinned actions** by commit SHA, and the minimum `permissions` for each job's token.

**Workflow trust.** A workflow triggered by `pull_request` runs "the version of the workflow that is present in the associated commit SHA", which for a pull request is its test merge commit. A pull request can therefore edit the job that produces the required check. Two mitigations, in order of strength (OD-01):

1. **Ruleset-required workflow** ("Require workflows to pass before merging"), configured at organization or enterprise level from a separately controlled repository and pinned to a SHA. Announced for GitHub Enterprise Cloud; GitHub's documentation is inconsistent about other plans. The ruleset pins only the workflow file; anything that workflow runs from the target checkout belongs to the pull request (§16.2 forbids running any). That the pinned file is what runs is supported by the ruleset API's fields but not stated outright, so Phase 0 confirms it in a test organization.
2. **CODEOWNERS on all of `/.github/` with required code-owner review.** The guarantee then rests on a reviewer noticing a workflow change, and on no run from any commit of the pull request producing the required check name. A run started by an earlier commit can still post the check after a later commit removes its workflow. Proctor reports `trust.workflow_changed` (MODE-04), but if the changed workflow never runs Proctor, there is no report.

**Not used:** `pull_request_target` workflows that build or run pull-request code. They run the default branch's workflow but "may have repository write access and access to referenced secrets", and GitHub advises: "Avoid using this event if you need to build or run code from the pull request."

**Fork pull requests.** A `pull_request` run from a fork gets a read-only token and no secrets. Authoritative runs for forks need test identities created inside the disposable environment (FIX-01). A fork pull request must never satisfy the check by being skipped (§16.2). Whether v1 supports forks is OD-19.

**Attestations.** Artifact attestations (REP-02) bind an artifact digest to the workflow, repository, commit SHA and triggering event. In private or internal repositories they require GitHub Enterprise Cloud.

Sources:
- https://docs.github.com/en/actions/concepts/workflows-and-actions/workflows
- https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows
- https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches
- https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/collaborating-on-repositories-with-code-quality-features/troubleshooting-required-status-checks
- https://docs.github.com/en/enterprise-cloud@latest/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets
- https://github.blog/enterprise-software/ci-cd/enforcing-code-reliability-by-requiring-workflows-with-github-repository-rules/
- https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners
- https://docs.github.com/en/actions/security-for-github-actions/security-guides/security-hardening-for-github-actions
- https://docs.github.com/en/actions/concepts/security/artifact-attestations

## 6.4 Human approval in v1

In v1, approving a specification change means approving the pull request that contains it, through code-owner review. There is no `proctor approve` command, because a command an agent can run cannot carry human authority. A separate approval record is a future option (OD-03).

---

# 7. Specification integrity

## 7.1 Protected inputs

| Input | Protection |
|---|---|
| Everything under the protected root (`proctor/` by default): manifest, specifications, `proctor.yaml` policy | Code-owner approval |
| Verification dependency definitions (for example `compose.verify.yaml`) and every file they reference: build contexts, mounted volumes, `env_file`, `configs`, `secrets`, included files | Code-owner approval; referenced files must sit under the protected root or be listed in `proctor.yaml` `protected_paths` (INT-13). Who defines them is OD-12 |
| All of `/.github/`, including workflows and CODEOWNERS | Code-owner approval; the verification workflow itself per OD-01 |
| `.gitattributes`, `.gitmodules`, `.lfsconfig` | Code-owner approval |
| Proctor executable and its adapters | Pinned version and checksum, supplied by trusted CI |
| `proctor.yaml` path, environment bindings and credentials | Supplied by trusted CI |
| Application source, build, migrations, tables, views, grants | Subject under test; the image is tied to the candidate commit by digest |

v1 has no `fixtures/` directory: scenario data comes from setup steps (FIX-02), and test identities from the environment (FIX-01). Database views and schemas are application code; Proctor labels assertions that depend on them (§10.2, DB-07).

## 7.2 Manifest

The manifest lists the authoritative specification set. Proctor does not discover specifications by glob in authoritative mode. In v1 every listed specification is required; there are no optional specifications.

```yaml
# proctor/manifest.yaml
version: 1

specifications:
  - id: authentication
    path: specs/authentication.yaml

  - id: project-access
    path: specs/project-access.yaml

  - id: project-export
    path: specs/project-export.yaml
```

- **INT-01.** Every specification listed in the manifest must exist at the tested commit. Paths are relative to the protected root, contain no `..` segment and no leading `/`, and must match a tree entry byte for byte. A missing file or a path that leaves the protected root is an integrity violation.
- **INT-02.** Every scenario and table case has a stable `id`, unique across the manifest and separate from its human-readable `name`. Duplicate IDs fail validation (exit 3).
- **INT-03.** In authoritative mode, every scenario and table case that Proctor's parser finds in a listed specification must start. One that is skipped, filtered out, or missing from the runner's results is an integrity violation. Specifications have no conditional-skip mechanism in v1. A scenario or case that doesn't start for one of these reasons is NOT_RUN with that reason, and the run takes that reason's exit code, not exit 4:
  - an earlier step of the same scenario failed or errored (the scenario takes that step's result);
  - its table's setup failed (§11.3);
  - the run stopped on a configuration, validation or readiness error (exit 3 or 2);
  - the run was aborted (REP-13).
- **INT-04.** Checked before execution: an empty manifest, a listed specification with no scenarios or tables, or a set that defines no assertions is an integrity violation. Checked after execution: a run in which errors prevented every assertion from being evaluated is ERROR (exit 2), not an integrity violation.
- **INT-05.** Specification files under the protected root that the manifest doesn't list are not executed in authoritative mode; the report lists them under `unlisted`.
- **INT-06.** Removed: v1 has no `required` flag.

## 7.3 Change detection

- **INT-07.** Proctor reads every protected input from the Git object database (`git cat-file blob <commit>:<path>`) at the tested commit, executes exactly those bytes, and hashes them. It never reads protected inputs from the working tree in authoritative mode.
- **INT-08.** `proctor diff --base <commit> --head <commit>` takes full object IDs and checks that both commits exist locally before comparing. It compares the two trees for every protected path, with file modes. It doesn't use Git's rename detection: a removed path and an added path with equal blob IDs and modes are reported as a move; any other path change is a removal plus an addition, and scenario IDs that changed file are reported as moved (and modified, if their content changed). Any Git error, warning or missing object is an integrity violation, never "no changes".
- **INT-09.** Base selection: when the tested tree is GitHub's test merge commit, the base is its first parent, after checking that its second parent is the pull request's head SHA. This needs `fetch-depth: 2` or more. When the pull request head is tested directly, the base is the merge base supplied by the platform; more than one merge base is an integrity violation. The report records base, head and tested commit IDs.
- **INT-10.** In authoritative mode, the report and job summary list every protected change as "approval required". Hashes cover the committed bytes; YAML is not canonicalized before hashing, so any byte change, including formatting, shows as a change. (Reason: canonicalization can hide differences such as duplicate keys that parsers resolve differently.)

## 7.4 Protected-input checks

- **INT-11.** In authoritative mode, Proctor calls GitHub's CODEOWNERS errors endpoint for the base commit. Any reported error is an integrity violation. A CODEOWNERS file in more than one of `/.github/`, `/`, `/docs/` is an integrity violation. Proctor also reports, without failing, any protected path that its own last-match evaluation of CODEOWNERS leaves without an owner listed in `proctor.yaml` `protected_owners`; whether that should fail is OD-16.
- **INT-12.** A scenario that uses a disabled adapter, or a connection or identity that `proctor.yaml` doesn't declare, fails validation (exit 3).
- **INT-13.** Every protected input is a regular file (Git mode 100644 or 100755). A symlink, a submodule (gitlink), a Git LFS pointer, or a path under a symlinked directory is an integrity violation. Files referenced by a verification dependency definition are resolved and hashed; a reference outside the protected root that `proctor.yaml` `protected_paths` doesn't list is an integrity violation. `protected_paths` entries are exact paths, or directories when they end in `/`.
- **INT-14.** None of these Git attributes may apply to a protected path, as reported by `git check-attr -a --source=<commit>`: `diff`, `-diff`, `binary`, `text`, `eol`, `filter`, `working-tree-encoding`, `ident`, `linguist-generated`, `export-ignore`, `export-subst`. A match is an integrity violation. (Reason: they change the bytes a checkout writes, or hide the diff from reviewers.)
- **INT-15.** Protected YAML is parsed strictly. Duplicate keys at any level, anchors, aliases, merge keys (`<<`), custom tags and keys the schema doesn't define fail validation (exit 3). (Reason: parsers disagree on duplicates, and one anchor edit can change many scenarios while the diff shows one line.)
- **INT-16.** Each authoritative run records a content hash of the manifest, each executed specification, `proctor.yaml`, the binding file, the verification dependency definitions and their referenced files, plus the versions and commits in §13.1.

A hash shows what was verified. It does not show that anyone approved it; approval comes from §6.

## 7.5 Semantic diff

The semantic diff helps a reviewer understand a protected change. It reports, per scenario and table case ID:

- added, removed and moved scenarios and cases;
- changed expected values, including status codes, JSON fields and row expectations;
- assertions removed, or replaced with broader matchers (lists of accepted values);
- changed identities, especially in negative cases;
- changed setup steps, shown on each scenario or case whose setup they are;
- changed scenario order within a specification;
- changed time parameters, with the direction that weakens each: a longer `timeout` for `eventually`; a shorter `duration` or a longer `interval` for `consistently` and `never`;
- changed `requirement_is_operational` flags (EVD-02), connection names and adapter settings;
- changes to `proctor.yaml` policy and to verification dependency definitions.

It lists **weakenings** separately from other changes, before them: a removed scenario, step or assertion, a verification step moved into setup, and an added `no_state_check` or `waive` entry, each with the scenario, the step and the number of assertions lost. Changed statuses, values, identities, credentials, queries and arguments are listed with their old and new values.

It is best-effort and explanatory. A weakening it fails to classify still appears as a modified file (INT-08), with its full content diff.

---

# 8. Positioning and terminology

Proctor is a behavioral verification and integrity system. It is not primarily a YAML test runner, an E2E framework, a database assertion tool or a hallucination detector. Generic YAML and plain-text runners for HTTP and SQL already exist (see `runner-evaluation.md`); Proctor's distinct value is the integrity layer and the provenance of its results.

Terms used in this document:

- **Specification:** a YAML file of scenarios and tables describing expected behavior.
- **Scenario:** a sequence of setup and verification steps with a stable ID.
- **Table case:** one row of a table, run as an independent case with its own ID.
- **Setup step:** a request that establishes starting conditions; not evidence.
- **Verification step:** a request or a check whose expectations are evidence.
- **Triggering step:** the step whose effects a time-based check observes (TIME-09).
- **Manifest:** the protected list of specifications that make up the authoritative set.
- **Protected root:** the directory whose contents are protected inputs (`proctor/` by default).
- **Protected input:** any repository file whose change requires code-owner approval (§7.1).
- **Tested commit:** the commit whose tree Proctor verified; for a pull request, usually GitHub's test merge.
- **Authoritative run:** a run invoked by the trusted workflow in authoritative mode (§5.2).
- **Local run:** any other run; feedback only.
- **Evidence level:** how directly a check shows the required behavior (§10).
- **Implementation-coupled assertion:** an assertion that depends on internal schema, event names or workflow structure, and may need maintenance when the implementation changes.

Testing terms keep their usual meanings: black-box testing checks behavior through observable interfaces; service-level testing checks one service with its dependencies; end-to-end testing checks a workflow across several system boundaries.

---

# 9. Goals and non-goals

## 9.1 Goals for v1

1. Express expected behavior in version-controlled specifications with stable IDs.
2. Execute them independently of the application's programming language, through public HTTP APIs.
3. Verify business state in PostgreSQL, and collect operational evidence from Temporal and structured logs.
4. Verify asynchronous behavior with bounded `eventually`, `consistently` and `never` checks.
5. Make every listed scenario impossible to skip silently in authoritative mode.
6. Detect and list every change to protected inputs, and refuse inputs Git can't show faithfully.
7. Bind each authoritative result to the exact commits, specifications and runner it used.
8. Report each pass, failure and error with the evidence behind it.
9. Support local feedback and authoritative CI with the same specifications.
10. Keep adapters separate from the core so new evidence sources can be added.

## 9.2 Non-goals

Proctor should not:

- replace unit-test frameworks (Go `testing`, Jest, pytest and others) or existing E2E frameworks;
- guarantee bug-free software or detect every AI error;
- infer an application's internal architecture;
- support every database, broker and platform in v1;
- build, deploy or host the applications it tests;
- become a general-purpose scripting language;
- enforce approval on its own, without the source-control platform;
- support source-control platforms other than GitHub in v1;
- require applications to be written in Go or to embed a Proctor SDK.

Unit and integration tests stay valuable. Proctor adds an independent layer.

---

# 10. Evidence levels

Not all evidence is equally strong. Proctor assigns each check an evidence level from its adapter, and reports it with the result.

| Level | Adapter | What it shows | Limitation |
|---|---|---|---|
| 1. Public contract | HTTP | Observable API behavior | Does not by itself show persistence; may be served from a cache |
| 2. Business state | PostgreSQL | Persisted state | Coupled to schema; views and grants are application code |
| 3. Operational | Temporal, logs | The application orchestrated or reported an action | Does not show that the intended side effect happened |

## 10.1 Level 1: public contract

The primary form of verification. Expressed in business terms, tested through public interfaces, designed to survive internal refactoring. Long-lived specifications should rely on Level 1 wherever the requirement can be observed through the public interface. Example: "an export request eventually exposes a downloadable export through `GET /api/exports/{id}`" rather than "the exports table has status completed".

## 10.2 Level 2: business state

Used when a requirement concerns persisted state that the public API does not expose, or when an API response alone could hide a missing write or come from a cache. Raw SQL against internal tables couples the specification to the schema, so a schema refactor may require a protected change. Prefer documented business-state queries against declared tables (DB-07). Views reduce coupling but add no trust: the application defines them and they run with their owner's rights.

## 10.3 Level 3: operational evidence

A completed workflow or a logged event shows that the application did or reported something, not that the business outcome is correct. A workflow can complete after a no-op; an application can log `project.export.completed` without producing an export.

- **EVD-01.** Validation rejects a scenario with no Level 1 or Level 2 verification step unless it sets `requirement_is_operational: true` (exit 3). Whether that Level 1 or 2 check covers the business result is the reviewer's judgment; Proctor can't check it.
- **EVD-02.** `requirement_is_operational: true` marks a scenario whose requirement is the operational event itself, such as an audit-log requirement. It is a protected field and appears in the semantic diff.

---

# 11. Functional requirements

All examples in this section are illustrative; the schema is fixed in Phase 2 (OD-10).

## 11.1 Scenarios and steps

- **EXE-01.** A scenario has an `id`, a `name`, optional `setup` steps, and one or more verification `steps`.
- **EXE-02.** Steps run in the order written. Values captured in one step are available to later steps of the same scenario, and a table's setup captures are available to its cases (TBL-04). Nothing else is shared between scenarios.
- **EXE-03.** Every verification step states at least one expectation. A verification step that expects a 2xx status must also assert at least one body field or header. A step that expects a 4xx or 5xx status may assert the status alone. Setup steps need only an expected status. Validation enforces this (exit 3).
- **EXE-04.** Scenarios run sequentially, in manifest order and then file order. Parallel execution is out of scope until scenario isolation is designed (§15).
- **EXE-05.** Each scenario creates the application state its assertions depend on, in its own setup. It does not rely on state left by earlier scenarios.
- **EXE-07.** In each scenario, every state-changing verification step (POST, PUT, PATCH or DELETE), whether it is expected to succeed or to be refused, is followed by a check of the state it was meant to change or leave alone: a database check (§11.7), or a GET whose expected status isn't 401 or 403. Validation rejects a scenario without one (exit 3). A step whose effect can't be read back states why in `no_state_check: <reason>`; the reason is shown to reviewers, and adding one is a weakening in the semantic diff (§7.5). A `no_state_check` on a step that a later step already checks is invalid.
- **EXE-08.** Proctor has no warning level: every finding of `validate` fails the command with exit 3 or 4, and `run` sends nothing when one exists. A rule that would otherwise force a specification to drop a legitimate assertion can be waived for one step with a written reason, `waive: { <rule>: <reason> }`; only FIX-05 and HTTP-09 can be waived this way (EXE-07 uses `no_state_check`). A waiver with an empty reason, for another rule, or for a rule that doesn't apply to the step is invalid, so waivers can't pile up unnoticed, and adding one is a weakening in the semantic diff (§7.5). Example: an API that accepts only fixed role names, where the role sent can't carry `${run.id}`.
- **EXE-06.** Every negative authorization case should have a positive control for the same identity and action: a case showing the identity can perform the action on its own resource. Validation rejects (exit 3) a specification that has denied cases for an identity with no allowed case for that identity, except identities of type `none`. (Not built yet.)

## 11.2 HTTP

Requests: GET, POST, PUT, PATCH and DELETE; base URL from the binding; path and query parameters; headers; JSON bodies; URL-encoded form bodies; per-request timeouts; identities (§11.4).

Assertions: status code; response headers, and headers that must be absent; JSON values selected by JSONPath, matched exactly (HTTP-07); presence and absence of fields (HTTP-08); the exact key set of an object; array length (HTTP-09); explicit lists of accepted values; string, number, boolean and null comparisons.

Captures: values from response bodies and headers, saved under names for later steps.

```yaml
version: 1
suite: project-access

scenarios:
  - id: PA-001
    name: Owner can create and read a project

    steps:
      - id: create
        request:
          method: POST
          path: /api/projects
          identity: alice
          body:
            name: "Alice's project ${run.id}"
        expect:
          status: 201
          json:
            $.name: "Alice's project ${run.id}"
            $.owner_id: "${identities.alice.user_id}"
        save:
          project_id: $.id

      - id: read
        request:
          method: GET
          path: /api/projects/${project_id}
          identity: alice
        expect:
          status: 200
          json:
            $.id: "${project_id}"
            $.owner_id: "${identities.alice.user_id}"
```

- **HTTP-01.** Proctor does not follow redirects to hosts other than the target host unless the configuration allows them.
- **HTTP-02.** Interpolation happens once. A value inserted by `${...}`, whether captured from a response or taken from a table row, is data and is never interpolated again.
- **HTTP-03.** A YAML scalar that is exactly one `${...}` reference keeps the referenced value's type; a reference embedded in other text produces a string. Comparisons are type-strict: `7` does not equal `"7"`.
- **HTTP-04.** The User-Agent header comes from the binding's `target.user_agent`. Proctor has no runtime default; a binding without it is a configuration error (exit 3).
- **HTTP-05.** A request's `path` is resolved against the binding's base URL and can't leave it. It must start with a single `/`, and it can't contain a scheme, a host, a `//` prefix, a `.` or `..` segment, a percent-encoded `/`, `?` or `#`. Query parameters go in `query`. Any other path is invalid (exit 3). A saved value inserted into a path is escaped as a single segment, so it can't add segments or a query; if the result is a `.` or `..` segment, the step is ERROR.
- **HTTP-06.** Specifications can't set the `Host`, `User-Agent`, `Authorization`, `Proxy-Authorization`, `Cookie`, `Content-Length`, `Transfer-Encoding` or `Connection` headers (exit 3). The binding and identities supply the first five; the HTTP client sets the rest.
- **HTTP-07.** A `json` assertion's expected value is matched by type and value. An object matches only an object with exactly the same keys, and an array only an array with the same elements in the same order; extra keys or elements fail. A specification that can't know every value uses `keys: { <path>: [<key>, ...] }`, which fails when the object at the path has any key not listed or lacks one that is.
- **HTTP-08.** `absent: [<path>, ...]` fails if any listed path selects a value, including `null`; `present: [<path>, ...]` fails if any selects nothing. A field set to `null` is present. `absent_headers: [<name>, ...]` fails if the response has any listed header. A path can't be both asserted and absent (exit 3).
- **HTTP-09.** `length: { <path>: <n> }` fails unless the path selects an array of exactly `n` elements; `n` is a non-negative integer or one `${name}` reference to a saved whole number. List assertions are meaningful only over records the scenario controls: validation rejects (exit 3) `length` asserted on a GET with no query parameters, unless the step waives HTTP-09 (EXE-08).
- **HTTP-10.** A response body that isn't exactly one JSON value, or has an object with a duplicate key, fails every JSON assertion of the step. (Parsers disagree on which duplicate wins, so such a body has no single meaning.)

## 11.3 Tables

Tables express many cases of the same request with different inputs and expected outputs, such as an authorization matrix. Use tables for straightforward combinations; use scenarios for multi-step workflows.

```yaml
tables:
  - id: PA-ALLOWED
    name: Owners can read their own projects

    setup:
      - id: alice-project
        request:
          method: POST
          path: /api/projects
          identity: alice
          body: { name: "Alice's project ${run.id}" }
        expect:
          status: 201
        save:
          alice_project: $.id

      - id: bob-project
        request:
          method: POST
          path: /api/projects
          identity: bob
          body: { name: "Bob's project ${run.id}" }
        expect:
          status: 201
        save:
          bob_project: $.id

    request:
      method: GET
      path: /api/projects/${row.project}
      identity: ${row.identity}

    columns: [case, identity, project]
    rows:
      - [PA-A-01, alice, "${alice_project}"]
      - [PA-A-02, bob,   "${bob_project}"]

    expect:
      status: 200
      json:
        $.id: "${row.project}"

  - id: PA-DENIED
    name: Users cannot read other users' projects
    setup_from: PA-ALLOWED

    request:
      method: GET
      path: /api/projects/${row.project}
      identity: ${row.identity}

    columns: [case, identity, project, status]
    rows:
      - [PA-D-01, alice,     "${bob_project}",   403]
      - [PA-D-02, bob,       "${alice_project}", 403]
      - [PA-D-03, anonymous, "${alice_project}", 401]

    expect:
      status: ${row.status}
```

- **TBL-01.** The first column is the case ID. Case IDs follow the rules for scenario IDs (INT-02, INT-03).
- **TBL-02.** Reports show the table as a grid with each case's result, so missing combinations are visible.
- **TBL-03.** Rows cannot contain conditions or skip markers.
- **TBL-04.** A table's setup runs once, before its first case. Its captures are visible to every case. `setup_from` reuses the setup and captures of a table that appears earlier in the same specification; a reference to a later or missing table fails validation (exit 3).
- **TBL-05.** Row cells are interpolated once, after setup, using its captures (HTTP-02).
- **TBL-06.** Cases run in row order and independently: a failing case doesn't stop later cases. The table's result is its worst case. If the setup fails, every case is NOT_RUN with reason `setup_failed`, and the table is ERROR (FIX-04).

## 11.4 Identities and authentication

Specifications refer to identities by name. `proctor.yaml` defines each identity's type; the binding supplies its credentials and attributes.

```yaml
# proctor.yaml (protected)
identities:
  alice: { type: bearer }
  bob: { type: bearer }
  anonymous: { type: none }
```

```yaml
# binding (supplied by the trusted job in authoritative mode)
identities:
  alice:
    token_env: PROCTOR_ALICE_TOKEN
    user_id_env: PROCTOR_ALICE_USER_ID
  bob:
    token_env: PROCTOR_BOB_TOKEN
    user_id_env: PROCTOR_BOB_USER_ID
```

- **AUTH-01.** v1 supports `none`, `bearer` (static token from an environment variable), `basic` and `oauth2_client_credentials`. OIDC test identity providers and mutual TLS are future options (OD-05).
- **AUTH-02.** Credentials never appear in specifications, `proctor.yaml` or reports. Reports show the identity name only.
- **AUTH-03.** Before any request, Proctor checks every identity used by a listed scenario. A missing or empty credential or attribute, an identity the binding defines but `proctor.yaml` doesn't (or the reverse), or two identities sharing a token or a `user_id` is a configuration error (exit 3).
- **AUTH-04.** If `proctor.yaml` names an identity-check endpoint (`identity_check: { path: /api/me, user_id: $.id }`), Proctor calls it for each identity before the first scenario and checks that the returned user ID matches the binding. A mismatch is a configuration error (exit 3).
- **AUTH-06.** An identity can have credential variants supplied by the disposable environment. `variants` (for example `expired`, `invalid_signature`, `wrong_audience`, `wrong_issuer`, `unsigned`, `malformed`, `no_expiry`) are refused credentials: a request naming one with `credential: <variant>` must expect 401 or 403 (exit 3 otherwise), and can't be a setup step. `accepted_variants` are other valid forms of the same credential, such as the scheme spelled in lower case, and may expect any status. The binding gives each variant either `token_env` (sent as `Bearer <token>`) or `header_env` (the whole `Authorization` value). Proctor never creates or alters a credential itself. Under AUTH-03, every identity and variant must send a different value.
- **AUTH-07.** `proctor.yaml` may declare `secrets` (such as a test user's password) that requests send as `${secret.<name>}`, in request bodies and headers only; the binding names the environment variable holding each. Secrets can't appear in paths, queries, expectations or database checks (exit 3). Before a report is written, Proctor replaces every token, secret and connection string that appears anywhere in it, for example in a response the application echoed, with `[redacted]`.
- **AUTH-05.** Identity attributes from the binding are strings. The binding may declare an attribute's type (`user_id_type: integer`), and Proctor converts the value once at load time; a value that doesn't convert is a configuration error (exit 3). Comparisons then follow HTTP-03.

## 11.5 Identity provisioning, setup, readiness and isolation

Setup establishes starting conditions; it is not evidence that the application performs the operation under test.

- **FIX-01.** Test identities are provisioned by the disposable environment, for example a test identity provider started with it, or an initialization job that creates accounts. Proctor only consumes their credentials. Which mechanisms the reference setup documents is OD-05.
- **FIX-02.** Scenario data is created in `setup` steps through the application's public API. This needs no write path besides the application itself.
- **FIX-03.** The PostgreSQL verification adapter stays read-only. Direct data seeding, if ever needed, is a separate setup adapter with its own credentials, controlled by the trusted job (OD-06).
- **FIX-04.** Setup steps are reported separately and are not counted as assertions. A setup step whose expectation fails, or that can't be executed, is ERROR with reason `setup_failed`; the scenario's remaining steps are NOT_RUN with reason `setup_failed`, and the scenario is ERROR (exit 2). Every ERROR step and scenario carries a `cause`: `unexpected_response` (an expectation failed; the report names it and the value received), `unreachable`, `timeout`, `unreadable_response`, `response_too_large`, `invalid_request`, `query_error` or `unsupported_result`. A setup ERROR with cause `unexpected_response` points at the application, not the environment.
- **FIX-05.** Each run has a unique ID, and `${run.id}` expands to it followed by `-` and the scenario's ID (for example `3f9a1c2b7d4e-PGU-001`). Specifications use it in names and keys so that data from different runs, or from different scenarios in one run, does not collide. Validation rejects (exit 3) a request that sends a value a later step of the scenario matches on when the value doesn't contain `${run.id}`, unless the step waives FIX-05 (EXE-08).
- **FIX-06.** Authoritative runs use a disposable environment created for the run. Cleanup inside a shared environment is out of scope for v1.
- **FIX-08.** Every operation a specification set uses in setup (method, path template and expected status) is also exercised, in at least one scenario, as a verification step, so a defect in it is reported as FAIL somewhere rather than only as a setup ERROR. Validation rejects a set that doesn't (exit 3).
- **FIX-07.** Before the first scenario, Proctor waits up to `startup_timeout` (protected policy) for readiness: the application's readiness endpoint answers 2xx; each PostgreSQL connection answers `SELECT 1` as the verification role; each Temporal task queue listed in `proctor.yaml` has a poller; each additional readiness path listed in `readiness.checks` (for example a background worker's health endpoint) answers 2xx; each log source exists. If readiness fails, the run is ERROR (exit 2), and every scenario is NOT_RUN with reason `environment_not_ready`.

## 11.6 Logs

The log adapter checks structured events the application wrote.

- **LOG-01.** v1 reads JSON Lines files. Docker container logs and plain-text matching are not supported (OD-13).
- **LOG-02.** Matching uses a correlation key, in this order of preference:
  1. the trace ID from W3C Trace Context: Proctor sends a `traceparent` header on requests whose trace ID a later check uses, and records the trace ID. The application must propagate it and write it into its log records, in the field named by `proctor.yaml` (`logs.trace_id_field`); the trace ID is optional in the OpenTelemetry log data model, so Proctor can't assume it is present;
  2. a business identifier captured from a response, such as an export ID;
  3. file position: records written after a position Proctor records just before the triggering request. Allowed only for `never`, where an unrelated record pushes towards FAIL, and reported as weak correlation. Timestamps are reported but never used to select records.
- **LOG-03.** Proctor reads logs without consuming, truncating or deleting them.
- **LOG-04.** A missing, unreadable or rotated log source is a sample error (TIME-10), never a missing event.
- **LOG-05.** Log checks are Level 3 evidence (§10.3).
- **LOG-06.** Records matched by trace ID or business identifier count wherever they are in the source, including records written during the triggering request.
- **LOG-07.** `expect.fields` passes when at least one matching record has every listed field with the listed value.
- **LOG-08.** A `never` check on logs is valid only with a liveness record: by the end of the window, the source must contain at least one record carrying the check's correlation key, such as the triggering request's own access record. Without one, the check is ERROR with reason `correlation_not_observed`. The correlation key of a `never` check must come from its triggering step (TIME-09); a key from any other step fails validation (exit 3).

```yaml
- id: export-completed-event
  verify:
    logs:
      source: application
      match:
        trace_id: "${steps.request-export.trace_id}"
        event: project.export.completed
      eventually:
        timeout: 10s
        interval: 500ms
      expect:
        fields:
          export_id: "${export_id}"
```

## 11.7 PostgreSQL

- **DB-01.** Queries use driver parameter binding. Captured values are passed as arguments, never interpolated into SQL text. Validation rejects `${...}` inside a `query` (exit 3).
- **DB-02.** Each query, and each sample of a time-based check, runs in its own transaction opened `READ ONLY` at `READ COMMITTED`, with `SET LOCAL statement_timeout` and a client-side deadline.
- **DB-03.** At connect, Proctor sets `search_path` to the schemas declared in `proctor.yaml`, or requires schema-qualified names when none are declared.
- **DB-04.** Before the first scenario, Proctor checks its own role and refuses to run (exit 4) if the role is a superuser, has `CREATEROLE` or `BYPASSRLS`, is a member of another role, owns any object, holds `INSERT`, `UPDATE`, `DELETE`, `TRUNCATE`, `REFERENCES` or `TRIGGER` on any table, holds `USAGE` or `UPDATE` on any sequence, or holds `SELECT` on any table or view that `proctor.yaml` doesn't declare (DB-07). (Reason: the application's migrations own the tables and decide their grants, so the role's privileges can't be assumed. PostgreSQL describes read-only transactions as "a high-level notion of read-only that does not prevent all writes to disk".)
- **DB-04a.** DB-04 also refuses a role with column-level `INSERT`, `UPDATE` or `REFERENCES` on any table, `CREATE` on any schema, `CREATEDB` or `REPLICATION`, one that owns a schema, function or database, a session whose `session_user` differs from `current_user`, and a declared object that doesn't exist (all exit 4). The report lists the first 50 problems and the count of the rest.
- **DB-05.** Connection failures and invalid SQL are execution errors (exit 2), not assertion failures. Invalid SQL (SQLSTATE class 42) ends the check at once.
- **DB-06.** Reports include only the columns a specification asserts on, after redaction (§13.2).
- **DB-08.** A check's results are read in PostgreSQL's text format and compared by column type: integers and numerics as exact numbers, booleans as booleans, `json` and `jsonb` decoded, `NULL` as null, everything else as its text form; `bytea` columns are an error. `rows: <n>` matches the exact row count. `first_row` must name every column the query returns, with type-strict values (HTTP-07 applied to rows). A query that returns two columns with one name, or more than 10,000 rows, is ERROR. A check that expects `rows: 1` may `save` named columns of that row for later steps; arguments are still passed only as parameters (DB-01).
- **DB-07.** Level 2 queries may read only tables and views declared in `proctor.yaml`. Proctor enforces this through the role's grants (DB-04), not by parsing SQL, and checks each declared object's kind (`pg_class.relkind`). Checks that read a view are labelled "application-defined view" in reports. All database checks are Level 2 evidence and labelled implementation-coupled.

```yaml
- id: export-persisted
  verify:
    postgres:
      connection: app_db
      query: |
        SELECT status
        FROM app.exports
        WHERE id = $1
      args:
        - "${export_id}"
      eventually:
        timeout: 15s
        interval: 500ms
      expect:
        rows: 1
        first_row:
          status: completed
```

## 11.8 Temporal

Temporal: "A Workflow Execution is uniquely identified by its Namespace, Workflow Id, and Run Id." Runs are chained by Continue-As-New, retries, cron and reset; a chain shares its first run's ID (`first_execution_run_id`). Visibility, used for search-attribute queries, is eventually consistent. Sources: https://docs.temporal.io/workflow-execution, https://docs.temporal.io/workflow-execution/continue-as-new, https://docs.temporal.io/visibility.

- **TMP-01.** Proctor locates workflows without requiring the application to expose Temporal identifiers through its public API. Supported lookups, in order of preference:
  1. a Workflow ID derived from a business identifier by a documented convention, such as `export-${export_id}`;
  2. a visibility query on workflow type and custom search attributes, with `eventually`;
  3. a Workflow ID captured from a response, when the API already exposes it for its own reasons.
- **TMP-02.** A check targets either a chain (the default), following it to its latest run, or a single run. The specification says which.
- **TMP-03.** Proctor reports the run IDs it observed, the chain's first run ID and the final status.
- **TMP-04.** Specifications use these status names, mapped to Temporal's API values: `running`, `paused`, `completed`, `failed`, `canceled`, `terminated`, `timed_out`, `continued_as_new`. Names match exactly; `running` doesn't match a paused workflow. Paused is pre-release on self-hosted Temporal and disabled by default. An `eventually` status check follows TIME-01 and TIME-10 like any other: it keeps sampling until the timeout, because a retry or Continue-As-New can still add a newer run, and the report shows the last status seen.
- **TMP-05.** A completed workflow is Level 3 evidence. Business completion must be checked by a Level 1 or 2 step (EVD-01).
- **TMP-06.** Activity-level and history assertions are out of scope for v1.
- **TMP-07.** Candidate selection uses chains, not start times where possible:
  - if the Workflow ID can be resolved before the triggering request, Proctor records the current chain (DescribeWorkflowExecution) just before sending it, and excludes that chain;
  - if the Workflow ID contains a value first returned by the triggering step, every chain with that ID is a candidate whose first run started no earlier than the triggering request's start minus `max_clock_skew` (protected policy, default 1 s);
  - two or more candidate chains are ERROR. The report shows the times from both clocks.
- **TMP-08.** `never` through Visibility is not allowed (exit 3), because Visibility can return stale results. `never` on a known Workflow ID uses DescribeWorkflowExecution.

```yaml
- id: export-workflow
  verify:
    temporal:
      connection: workflows
      find:
        workflow_id: "export-${export_id}"
      target: chain
      eventually:
        timeout: 60s
        interval: 1s
      expect:
        status: completed
```

## 11.9 Time-based checks

All waits are bounded.

- **TIME-01. `eventually`:** the condition must become true within `timeout`. Passes at the first sample that satisfies it.
- **TIME-02. `consistently`:** the condition must hold at every sample for `duration`. Fails at the first sample that violates it.
- **TIME-03. `never`:** a matching log record or Temporal execution must not be observed at any sample during `duration`. HTTP and PostgreSQL state use `consistently`.
- **TIME-04.** Reports record the window, the interval, t0 in UTC and its offset from the triggering step, samples due, started, evaluated, errored and skipped (with reasons), the number and offset of the first passing or failing sample, time to pass, and the number of requests sent.
- **TIME-05.** Sampling cannot show that no transient change occurred between samples, and a finite window cannot show that a change will never occur. Reports for `consistently` and `never` say so. When transient changes matter, the requirement needs a stronger evidence source, such as an audit table or change-data-capture stream (future).
- **TIME-06.** Behavioral failures are never retried except through these constructs. The schema has no retry, delay or repeat options, so a specification that uses one is rejected as an unknown key (exit 3); Proctor's core schedules samples.
- **TIME-07. Values.** `timeout`, `duration` and `interval` are strings with a unit (`ms`, `s`, `m`). Zero, negative and unitless values fail validation (exit 3). `interval` must be at least `min_interval` and at most half the window; a check schedules at most `max_samples`; a window is at most `max_window`. These limits are protected policy in `proctor.yaml`, with proposed defaults of 100 ms, 1,000 samples and 10 minutes (OD-18).
- **TIME-08. Schedule.** Samples are due at t0 + k × interval while k × interval ≤ window, and at t0 + window. At most one sample of a check is in flight. When a sample ends after one or more later due times have passed, those due times are recorded as skipped, one sample starts immediately, and the schedule continues from the next due time after it. The last due sample always runs and is awaited. A sample's timeout is the smaller of its adapter timeout and 4 × interval. A check therefore ends within window + 8 × interval.
- **TIME-09. Anchor.** The triggering step is the step named in `after:`, which must be an earlier step of the same scenario (exit 3 otherwise); otherwise the step whose trace ID or captured value the check uses; otherwise the nearest earlier step that sends POST, PUT, PATCH or DELETE. The window starts with the first sample, after earlier steps complete. The triggering step decides which records and executions belong to the check (LOG-06, TMP-07), not when sampling starts.
- **TIME-10. Errored samples.** A complete HTTP response, whatever its status, is an observation, not an error.
  - `eventually` passes at the first satisfying sample, even after errors. If none satisfies it, it is FAIL when the final sample was evaluated, and ERROR when the final sample errored.
  - `consistently` and `never` fail at the first violation. After a sample error they keep sampling to the end of the window; if no sample violated and any errored, they are ERROR.
  - A permanent error, such as invalid SQL or a missing namespace, ends any check as ERROR at once.
- **TIME-11. Polled requests.** Time constructs on request steps are allowed only for GET (exit 3 otherwise). `save` and `${steps.<id>.trace_id}` refer to the passing sample of `eventually`, and are invalid with `consistently` and `never`.
- **TIME-12. Clock.** Windows, due times and timeouts use a monotonic clock.

Example: Alice's denied delete must not change Bob's project, including through background work. It asserts a version field so that a change to any field is caught, and checks persisted state as well as the API.

```yaml
- id: PA-002
  name: A denied delete does not change another user's project

  setup:
    - id: bob-project
      request:
        method: POST
        path: /api/projects
        identity: bob
        body: { name: "Bob's project ${run.id}" }
      expect:
        status: 201
      save:
        bob_project: $.id
        bob_version: $.version

  steps:
    - id: alice-deletes
      request:
        method: DELETE
        path: /api/projects/${bob_project}
        identity: alice
      expect:
        status: 403

    - id: still-readable-by-bob
      after: alice-deletes
      request:
        method: GET
        path: /api/projects/${bob_project}
        identity: bob
      consistently:
        duration: 10s
        interval: 250ms
      expect:
        status: 200
        json:
          $.version: "${bob_version}"
          $.status: active

    - id: still-stored
      after: alice-deletes
      verify:
        postgres:
          connection: app_db
          query: |
            SELECT version, deleted_at
            FROM app.projects
            WHERE id = $1
          args:
            - "${bob_project}"
          consistently:
            duration: 10s
            interval: 250ms
          expect:
            rows: 1
            first_row:
              version: "${bob_version}"
              deleted_at: null
```

The scenario relies on the readiness gate (FIX-07) including the background worker, so that a job which would act on the denial runs inside the window. Starting observation before the triggering request is OD-08.

---

# 12. Specification design principles

- **Declarative.** Specifications describe preconditions, actions, expected outcomes, evidence sources and time bounds. They are not a programming language.
- **Readable without the implementation** for Level 1 checks. Level 2 and 3 checks may need implementation knowledge and are labelled.
- **Explicit assertions.** A step passes only on stated expectations, never merely because a request completed (EXE-03).
- **Negative cases with positive controls.** Security requirements need negative cases, such as a user cannot read another user's project, and each needs a matching positive case (EXE-06). Tables make these matrices compact.
- **Self-contained scenarios.** Each scenario creates the state it depends on (EXE-05).
- **No embedded code.** No shell commands, JavaScript, Python or custom executable expressions. Proctor's schema has no construct for them, and unknown keys are rejected (SEC-06).
- **Strict YAML.** No duplicate keys, anchors, aliases, merge keys or custom tags (INT-15).
- **Mocks are labelled.** A dependency replaced by a simulator is reported as simulated, and its checks are never reported as verification of the real dependency.

---

# 13. Reports and provenance

## 13.1 Provenance

Every authoritative report records:

- repository identity;
- the tested commit, its parents, the pull request head SHA the status is attached to, and the base commit used for change detection (INT-09);
- the application image digest, and the digest of the image the running application was started from (REP-11);
- the hash of the manifest, each executed specification, `proctor.yaml`, the binding, and the verification dependency definitions with their referenced files (INT-16);
- whether any protected file's bytes differ between the tested commit and the pull request head;
- Proctor version, adapter versions;
- environment name;
- workflow identity (MODE-03), `trust.workflow_changed` (MODE-04), CI run ID and run attempt;
- run ID, start and end timestamps;
- the protected changes against the base commit (INT-10);
- the result of every scenario and table case.

- **REP-01.** The trusted job generates and stores the report as a CI artifact. A report produced elsewhere is not evidence.
- **REP-02.** Where the plan allows, the trusted job attaches a signed attestation linking the report to the tested commit and image digest (OD-04).
- **REP-03.** The required status sits on the pull request head SHA. Because the check must pass on the result that will merge (§6.3), a status computed against an older base doesn't let the pull request merge.
- **REP-10.** If earlier attempts of the same workflow run recorded a behavioral FAIL for the same tested commit and input hashes, the report and job summary say so and name those attempts. In v1 this does not change the result (OD-15).
- **REP-11.** The trusted workflow passes two digests in the binding: the image digest the build job produced, and the digest of the image the running application container was started from, read by the workflow from the container runtime. Proctor records both and treats a mismatch, or a missing value, as an integrity violation. Proctor itself needs no access to the container runtime.

## 13.2 Sensitive data

- **REP-04.** Reports never contain credentials, tokens, passwords, API keys or connection strings.
- **REP-05.** Request and response bodies and log records are not stored whole. Reports keep the fields that assertions reference plus a bounded excerpt of at most `report.excerpt_bytes` (protected policy), after redaction. Database results follow DB-06.
- **REP-06.** Redaction rules in `proctor.yaml` add to the defaults; the defaults can't be removed. Defaults redact the `Authorization`, `Cookie` and `Set-Cookie` headers, and any JSON field, column or log field whose name contains, case-insensitively, `password`, `secret`, `token` or `api_key`.

## 13.3 Results and exit codes

Each scenario, table case and step has one result:

- **PASS:** every assertion held.
- **FAIL:** a behavioral assertion did not hold.
- **ERROR:** Proctor could not execute or evaluate the check, including failed setup (FIX-04).
- **NOT_RUN:** the check did not start. It always carries a reason: `earlier_step_failed`, `earlier_step_errored`, `setup_failed`, `environment_not_ready`, `trust_violation` (DB-04), `configuration_error`, `aborted`, or `not_started`. Only `not_started` (INT-03) and `trust_violation` are integrity violations. ERROR results also carry a cause (FIX-04).

Process exit codes:

| Code | Meaning |
|---|---|
| 0 | Every check passed |
| 1 | At least one behavioral assertion failed |
| 2 | Execution or infrastructure error, and no behavioral failure |
| 3 | Invalid configuration or specification |
| 4 | Integrity or trust violation |

- **REP-07.** When several apply, the order is 4, then 3, then 1, then 2. A FAIL is evidence against the commit; an ERROR is missing evidence. Exit 2 therefore means "nothing failed, but some checks weren't evaluated".
- **REP-08.** The JSON report has a top-level `result` (`passed`, `failed`, `error`, `invalid`, `integrity_violation`), always matching the exit code, a `mode` (`local` or `authoritative`), a `trust` block (§5.2), and a `summary`: counts by result for scenarios and for table cases, integrity violations with reasons, the number of protected changes, and the IDs that failed or errored.
- **REP-09.** These meanings and field names are stable once v1 is released and documented in the CLI reference. The provenance fields are named in Phase 0 and the result fields in Phase 2; `acceptance-tests.md` uses provisional names until then.
- **REP-12.** In authoritative mode Proctor always continues to the next scenario after a FAIL or ERROR.
- **REP-13.** On SIGTERM or SIGINT, Proctor starts no new scenario, writes a report with `result: error` and `aborted: true`, marks unstarted scenarios NOT_RUN with reason `aborted`, and exits 2. Proctor recovers internal panics and reports them as ERROR with reason `internal`. The trusted workflow treats a missing or unparseable report as a failure.

Example terminal output:

```text
PROCTOR VERIFICATION  (authoritative)

Tested:    9b41e07 (merge of head 3f2a9c1 into base 77c0d12)
Image:     sha256:4b1e…   Manifest: sha256:9d07…   Proctor: 1.0.0
Workflow:  org/verify/.github/workflows/proctor.yml@a1c3…  (attempt 1)

Protected changes against base: none

PASS   PA-001      Owner can create and read a project
FAIL   PA-002      A denied delete does not change another user's project
         step alice-deletes  expected status 403, got 204
         evidence: HTTP (level 1)
PASS   PA-ALLOWED  2/2 cases
PASS   PA-DENIED   3/3 cases

Scenarios: 2   Table cases: 5   Passed: 6   Failed: 1   Errors: 0   Not run: 0
RESULT: FAILED (exit 1)
```

---

# 14. Configuration and repository layout

`proctor.yaml` holds protected policy only. Bindings (target URL, connection strings, credential variable names, User-Agent) live in a separate file. In authoritative mode the trusted job supplies both paths (MODE-01).

```yaml
# proctor/proctor.yaml (protected)
version: 1
project: example-application
manifest: manifest.yaml
protected_owners: ["@example-org/verification-owners"]
protected_paths: [compose.verify.yaml]

adapters:
  http: enabled
  postgres: enabled
  temporal: enabled
  logs: enabled

connections:
  postgres:
    app_db:
      schemas: [app]
      objects: [app.projects, app.exports]
  temporal:
    workflows:
      task_queues: [exports]
  logs:
    application:
      trace_id_field: trace_id

identities:
  alice: { type: bearer }
  bob: { type: bearer }
  anonymous: { type: none }

readiness:
  path: /healthz/ready
  startup_timeout: 120s

time_limits:
  min_interval: 100ms
  max_samples: 1000
  max_window: 10m

temporal:
  max_clock_skew: 1s

redaction:
  json_paths: [$.email]
  headers: [X-Api-Key]

report:
  formats: [terminal, json]
  excerpt_bytes: 2048
```

```yaml
# proctor/environments/local.yaml (local mode only)
target:
  base_url: http://localhost:8080
  user_agent: example-client/1.0
connections:
  postgres:
    app_db: { dsn_env: PROCTOR_APP_DB_DSN }
  temporal:
    workflows: { address: "localhost:7233", namespace: default }
  logs:
    application: { type: file, path: ./logs/application.jsonl }
identities:
  alice: { token_env: PROCTOR_ALICE_TOKEN, user_id_env: PROCTOR_ALICE_USER_ID }
  bob: { token_env: PROCTOR_BOB_TOKEN, user_id_env: PROCTOR_BOB_USER_ID }
```

Suggested layout in an application repository (illustrative; Proctor does not impose a source layout):

```text
my-application/
├── proctor/                  protected root
│   ├── proctor.yaml
│   ├── manifest.yaml
│   ├── specs/
│   ├── verify/               files the dependency definitions reference
│   └── environments/
│       └── local.yaml        local mode only
├── compose.verify.yaml       protected (dependencies for verification)
├── .gitattributes            protected
├── .github/                  protected, entirely
│   ├── CODEOWNERS
│   └── workflows/
└── …application code…        subject under test
```

---

# 15. Determinism and isolation

- Explicit timeouts on every request, query and wait, set in the specification or by a documented default.
- Bounded, scheduled sampling only (§11.9), on a monotonic clock.
- Deterministic order: manifest order, then scenario order in each file (EXE-04).
- Clear error classification (§13.3).
- Scenario-scoped variables (EXE-02) and self-contained scenarios (EXE-05).
- Unique run IDs for generated data (FIX-05).
- A readiness gate before the first scenario (FIX-07).
- Parallel execution off; enabling it requires a design for isolation of identities and data (future).

---

# 16. CLI and reference workflow

## 16.1 CLI

The CLI is a single Go executable. It is the entry point to the engine, not the product's main value.

| Command | Purpose | Exit codes |
|---|---|---|
| `proctor validate` | Parse configuration, manifest and specifications strictly; check IDs, references, assertions (EXE-03, EVD-01), SQL (DB-01), time values (TIME-07), identities declared (INT-12) | 0 or 3; never contacts the application |
| `proctor integrity --commit <id>` | Static integrity checks at a commit: INT-01, INT-02, INT-04 (static part), INT-05, INT-11, INT-13, INT-14 | 0, 3 or 4 |
| `proctor diff --base <id> --head <id>`, or `proctor diff --head <test-merge> --pr-head <id>` (base from INT-09) | Protected changes (INT-08) and the semantic diff (§7.5) | 0, or 4 on any Git error |
| `proctor run --binding <file>` | Validate, check integrity, check readiness, execute, report; INT-03 and INT-04 (run part). `--request-timeout` bounds each request | 0–4; local mode unless `--authoritative` |
| `proctor report` | Show or export the last report | 0 |

There is no `proctor approve` in v1 (§6.4). An invalid invocation (unknown command or flag) exits 3. Every command takes `--repo <dir>`, defaulting to the current directory. In local mode, `--config` defaults to `proctor/proctor.yaml` and `--binding` is required.

Local use from any build system:

```json
{ "scripts": { "test:behavior": "proctor run --binding proctor/environments/local.yaml" } }
```

```makefile
.PHONY: test-behavior
test-behavior:
	proctor validate
	proctor run --binding proctor/environments/local.yaml
```

## 16.2 Reference workflow requirements

Phase 0 builds and documents the reference workflow. This document doesn't include a copyable workflow, because its trust mechanism depends on OD-01. The workflow must:

1. **Build in isolation.** A build job, separate from the verification job, builds the application image from the tested commit and outputs its digest. The verification job never runs pull-request code on its own host: no build scripts, Makefile targets, package installs or other tasks from the checkout. It runs only its own steps, the pinned Proctor and runner, the image by digest, and the protected dependency definitions.
2. **Check out with history.** Fetch the tested commit with `fetch-depth: 2` or more, so INT-09 can find the base.
3. **Verify binaries right before use.** Download the pinned Proctor and runner, verify their checksums immediately before executing them, and record the digest of the executed binary in the job log.
4. **Start a disposable environment** from the protected dependency definitions, with the application image by digest, and provision test identities inside it (FIX-01).
5. **Run Proctor** with `--authoritative --config <path> --binding <path>`. Grant the verification job `id-token: write` for workflow identity (MODE-03), `contents: read` for the CODEOWNERS errors endpoint (INT-11), and `actions: read` to list earlier attempts (REP-10), and nothing else. Pass both image digests in the binding (REP-11).
6. **Upload the report** as an artifact, and treat a missing or unparseable report as failure (REP-13).
7. **Produce the required check from an aggregator job** that runs with `if: always()`, depends on the build and verification jobs, and fails when any of them failed, was cancelled or was skipped. GitHub reports skipped jobs as successful, so the required check must never be a job that can be skipped.
8. **Trigger on `merge_group`** as well as `pull_request` when a merge queue is used.
9. **Never re-run automatically.**

---

# 17. Testing

`acceptance-tests.md` lists the tests that show each requirement and outcome: the adversarial cases against the integrity layer, the time-check edge cases, the result classification cases, and one observable test per requirement. Each test names the phase that must make it pass.

Proctor is tested only from the outside: its tests build the `proctor` binary and run it as a separate process against real Git repositories, real HTTP applications and real PostgreSQL and Temporal servers. There are no function-level unit tests; parsing, validation, interpolation, scheduling and result classification are tested through the command's exit codes and reports. The PostgreSQL tests need a disposable server named by `PROCTOR_E2E_POSTGRES_DSN` and fail without it, unless `PROCTOR_E2E_SKIP_POSTGRES=1` says to skip them on purpose; `PROCTOR_E2E_RACE=1` builds the binary under test with the race detector. The repository also includes example applications in Go, Node.js and Python that pass the same specifications.

Three kinds of mutation testing run continuously, from `mutation-testing/`: application mutants (one deliberate defect in an example application, which the specifications must FAIL), specification and database mutants (one weakened check or verification role, which validation must reject, the semantic diff must show, or the run must refuse), and mutants of Proctor itself (one changed operator, constant, result or statement, which the black-box tests must catch). Each round also includes a held-out set of application mutants written by someone who hasn't seen the specifications, so the score measures the specifications rather than their fit to known defects. A held-out score measured after the specifications were changed to catch those mutants is no longer held-out, and is reported as such.

---

# 18. Security requirements

- **SEC-01.** No credentials in specifications, `proctor.yaml` or reports (AUTH-02, REP-04).
- **SEC-02.** Credentials come from environment variables named by the binding.
- **SEC-03.** PostgreSQL access is read-only by role, checked at startup (DB-04), and by transaction (DB-02).
- **SEC-04.** Parameterized queries only (DB-01).
- **SEC-05.** Timeouts on every connection, request and query.
- **SEC-06.** No shell execution or embedded scripts from specifications, and no code loaded from the repository. The schema has no construct for either, and unknown keys are rejected before execution (exit 3).
- **SEC-07.** Proctor sends requests only to the configured target, the configured connections, the identity token endpoints and, in authoritative mode, the GitHub API (INT-11, MODE-03). It sends no analytics or usage data, and follows no redirect elsewhere unless allowed by configuration (HTTP-01).
- **SEC-08.** Infrastructure errors never count as passes.
- **SEC-09.** Reports are redacted (§13.2).
- **SEC-10.** Authoritative runs need no production credentials; they use disposable environments and test-only identities.
- **SEC-11.** Release binaries are published with checksums, and the release process is documented so the trusted job can pin and verify them. Signed releases are planned (OD-04).
- **SEC-12.** No pull-request code runs on the verification job's host (§16.2).
- **SEC-13.** Proctor contains no code copied, translated or closely paraphrased from other test runners. Libraries are used only as declared dependencies whose licenses have been checked, and their notices ship with Proctor's releases (`runner-evaluation.md`, "No copied code").

---

# 19. Architecture

Proctor keeps a small core and separate adapters, and executes specifications with its own runner. No other test runner is used as a library, subprocess or backend; Phase 1 recorded this decision (`runner-evaluation.md`).

The core owns: manifest and integrity checks, change detection, scenario orchestration, setup and verification steps, table expansion, variables and interpolation, assertion evaluation, sample scheduling for time-based checks, trace context, evidence collection, provenance and reporting.

Adapters own: connections, a single technology-specific operation (one HTTP request, one query in its own transaction, one Temporal describe or list, one log read), retrieval of evidence, and conversion into common result structures.

- **ARC-01.** Technology-specific code stays out of the assertion engine. An adapter returns structured evidence; the core evaluates it.
- **ARC-02.** Each adapter performs one operation behind the adapter interface. The integrity layer depends only on Proctor's own scenario model and result types.

Future adapters (not v1): Redis, Kafka, RabbitMQ, Elasticsearch, Loki, gRPC, WebSocket, S3-compatible storage, filesystem.

Language independence: Proctor talks to applications only through HTTP and the dependencies' standard protocols, so the same specifications work for applications in any language.

---

# 20. Roadmap

Integrity is the product's distinguishing feature, so it is proven first. Each phase ends when the tests `acceptance-tests.md` assigns to it pass.

## Phase 0: Trust proof of concept

Build the integrity layer and the reference workflow against a minimal HTTP example application, with a throwaway subset of the specification schema (scenarios with HTTP steps and IDs only; schema v1 comes in Phase 2). Run the scenarios with a first version of Proctor's own runner, HTTP only.

Deliverables: manifest and INT-01 to INT-05, INT-07 to INT-11, INT-13 to INT-16; `proctor diff`; `proctor run` on the native runner for the Phase 0 subset; authoritative mode (§5.2); the reference workflow (§16.2) using the mechanism chosen in OD-01, confirmed in a test organization; provenance in the JSON report (§13.1).

Acceptance: the Phase 0 tests in `acceptance-tests.md`, including AT-I and AT-W.

## Phase 1: Runner decision

Done on 2026-11-14. The product owner decided that Proctor has its own runner and takes only ideas from other tools, with no copied code (SEC-13). That also settled the specification format: people approve Proctor's own schema (OD-09). `runner-evaluation.md` is the decision record; it lists the ideas taken and the failure modes avoided. The Venom proof of concept planned for Phase 0 was dropped.

Acceptance: the decision record in `runner-evaluation.md`.

## Phase 2: HTTP verification

Specification schema v1 (scenarios, tables, captures, assertions, interpolation rules); identities AUTH-01 to AUTH-04; setup steps; exit codes and the JSON result model; terminal report.

Acceptance: the Phase 2 tests in `acceptance-tests.md`, including the §11.2 and §11.3 examples passing (exit 0) against Go, Node.js and Python example applications.

## Phase 3: Identities, readiness and isolation

Identity provisioning inside the disposable environment; readiness gate (FIX-07); run IDs; fork pull requests if OD-19 includes them.

## Phase 4: PostgreSQL and time-based checks

DB-01 to DB-08, `eventually`, `consistently`, `never`, TIME-01 to TIME-12. Single-sample database checks (DB-01 to DB-08 without `eventually`) were built early, on 2026-11-14, because mutation testing showed that no HTTP-only specification detects an API that is consistent with itself while storing the wrong data.

Acceptance: the Phase 4 tests in `acceptance-tests.md`, and every case in `mutation-testing/mvp-test-matrix.md` giving its required result on the commit that claims the phase.

## Phase 5: Temporal and logs

TMP-01 to TMP-08, LOG-01 to LOG-08.

## Phase 6: Review experience and release

Semantic diff, redaction, attestation where available, the reference GitHub configuration guide, specification and configuration reference, security documentation, installation with checksums.

---

# 21. Product principles

1. **Independent verification.** Proctor is independent of the implementation and of the agent changing it.
2. **Human-owned expected behavior.** AI may propose changes to the contract; humans approve them through the platform.
3. **Authoritative only in trusted CI.** Local runs are feedback, and Proctor says which it was invoked as, not more.
4. **Protect every input.** Specifications, manifest, configuration, workflow, Git attributes, runner and bindings, not only assertions.
5. **Refuse what can't be shown faithfully.** Symlinks, submodules, diff-hiding attributes and Git errors are integrity violations, not "no changes".
6. **Verify effects, and state evidence strength.**
7. **Language independence.**
8. **Explicit assertions with positive controls.**
9. **Evidence-based, provenance-bound results.**
10. **Safe by default.** Read-only access checked at startup, no embedded code, redacted reports.
11. **Simple specifications.**
12. **Replaceable runner and adapters.**
13. **No false guarantees.** Proctor never claims bug-free software, hallucination prevention, that a local CLI can stop an agent, or that it saw an approval.

---

# 22. Instructions to the implementing agent

Use this document with `acceptance-tests.md` and `open-decisions.md`. Before writing substantial code:

1. Read this document, `acceptance-tests.md` and `runner-evaluation.md`.
2. Resolve or escalate the open decisions that block the phase you are starting.
3. Propose the schema, interfaces and error model for that phase.
4. Produce an incremental plan.

Then implement one phase at a time, starting with Phase 0. Keep every example safe to copy: parameterized SQL, no credentials, no embedded code. Do not claim a capability for any other tool without citing its documentation or source, and do not copy code from one (SEC-13). Do not add `proctor approve` or any approval path that an agent could invoke. Keep the implemented schema and its documentation in step.

The first success criterion is not the number of features. It is whether the authoritative run can show, for a real pull request, that every listed scenario ran against the application image built from the tested commit, which protected inputs changed for the code owners to review, and that the required check can't pass without that run.
