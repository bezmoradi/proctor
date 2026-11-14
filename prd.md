# Proctor — Product Requirements

**Status:** Revision 3 (2026-11-14), the working basis for implementation. Every open decision has been decided (Appendix A); any of them can be revisited.
**Contents:** sections 1 to 22 are the requirements. Appendix A records the decisions, Appendix B the runner decision (Phase 1), Appendix C the acceptance tests, and Appendix D the revision history.

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

**How success is shown:** the tests in Appendix C pass against the reference GitHub configuration, including the outcome tests AT-O1 and AT-O2, and the example applications in three languages pass the same specifications. Adoption metrics are not defined for this release.

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

Proctor is a behavioral verification and integrity system. It is not primarily a YAML test runner, an E2E framework, a database assertion tool or a hallucination detector. Generic YAML and plain-text runners for HTTP and SQL already exist (see Appendix B); Proctor's distinct value is the integrity layer and the provenance of its results.

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
- **REP-09.** These meanings and field names are stable once v1 is released and documented in the CLI reference. The provenance fields are named in Phase 0 and the result fields in Phase 2; Appendix C uses provisional names until then.
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

Appendix C lists the tests that show each requirement and outcome: the adversarial cases against the integrity layer, the time-check edge cases, the result classification cases, and one observable test per requirement. Each test names the phase that must make it pass.

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
- **SEC-13.** Proctor contains no code copied, translated or closely paraphrased from other test runners. Libraries are used only as declared dependencies whose licenses have been checked, and their notices ship with Proctor's releases (Appendix B, "No copied code").

---

# 19. Architecture

Proctor keeps a small core and separate adapters, and executes specifications with its own runner. No other test runner is used as a library, subprocess or backend; Phase 1 recorded this decision (Appendix B).

The core owns: manifest and integrity checks, change detection, scenario orchestration, setup and verification steps, table expansion, variables and interpolation, assertion evaluation, sample scheduling for time-based checks, trace context, evidence collection, provenance and reporting.

Adapters own: connections, a single technology-specific operation (one HTTP request, one query in its own transaction, one Temporal describe or list, one log read), retrieval of evidence, and conversion into common result structures.

- **ARC-01.** Technology-specific code stays out of the assertion engine. An adapter returns structured evidence; the core evaluates it.
- **ARC-02.** Each adapter performs one operation behind the adapter interface. The integrity layer depends only on Proctor's own scenario model and result types.

Future adapters (not v1): Redis, Kafka, RabbitMQ, Elasticsearch, Loki, gRPC, WebSocket, S3-compatible storage, filesystem.

Language independence: Proctor talks to applications only through HTTP and the dependencies' standard protocols, so the same specifications work for applications in any language.

---

# 20. Roadmap

Integrity is the product's distinguishing feature, so it is proven first. Each phase ends when the tests Appendix C assigns to it pass.

## Phase 0: Trust proof of concept

Build the integrity layer and the reference workflow against a minimal HTTP example application, with a throwaway subset of the specification schema (scenarios with HTTP steps and IDs only; schema v1 comes in Phase 2). Run the scenarios with a first version of Proctor's own runner, HTTP only.

Deliverables: manifest and INT-01 to INT-05, INT-07 to INT-11, INT-13 to INT-16; `proctor diff`; `proctor run` on the native runner for the Phase 0 subset; authoritative mode (§5.2); the reference workflow (§16.2) using the mechanism chosen in OD-01, confirmed in a test organization; provenance in the JSON report (§13.1).

Acceptance: the Phase 0 tests in Appendix C, including AT-I and AT-W.

## Phase 1: Runner decision

Done on 2026-11-14. The product owner decided that Proctor has its own runner and takes only ideas from other tools, with no copied code (SEC-13). That also settled the specification format: people approve Proctor's own schema (OD-09). Appendix B is the decision record; it lists the ideas taken and the failure modes avoided. The Venom proof of concept planned for Phase 0 was dropped.

Acceptance: the decision record in Appendix B.

## Phase 2: HTTP verification

Specification schema v1 (scenarios, tables, captures, assertions, interpolation rules); identities AUTH-01 to AUTH-04; setup steps; exit codes and the JSON result model; terminal report.

Acceptance: the Phase 2 tests in Appendix C, including the §11.2 and §11.3 examples passing (exit 0) against Go, Node.js and Python example applications.

## Phase 3: Identities, readiness and isolation

Identity provisioning inside the disposable environment; readiness gate (FIX-07); run IDs; fork pull requests if OD-19 includes them.

## Phase 4: PostgreSQL and time-based checks

DB-01 to DB-08, `eventually`, `consistently`, `never`, TIME-01 to TIME-12. Single-sample database checks (DB-01 to DB-08 without `eventually`) were built early, on 2026-11-14, because mutation testing showed that no HTTP-only specification detects an API that is consistent with itself while storing the wrong data.

Acceptance: the Phase 4 tests in Appendix C, and every case in `mutation-testing/mvp-test-matrix.md` giving its required result on the commit that claims the phase.

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

Use this document, including its appendices. Before writing substantial code:

1. Read this document, including Appendices B and C.
2. Resolve or escalate the open decisions that block the phase you are starting.
3. Propose the schema, interfaces and error model for that phase.
4. Produce an incremental plan.

Then implement one phase at a time, starting with Phase 0. Keep every example safe to copy: parameterized SQL, no credentials, no embedded code. Do not claim a capability for any other tool without citing its documentation or source, and do not copy code from one (SEC-13). Do not add `proctor approve` or any approval path that an agent could invoke. Keep the implemented schema and its documentation in step.

The first success criterion is not the number of features. It is whether the authoritative run can show, for a real pull request, that every listed scenario ran against the application image built from the tested commit, which protected inputs changed for the code owners to review, and that the required check can't pass without that run.

---

# Appendix A. Decisions

The decisions the requirements depended on, in the order they blocked work. Each has a recommendation. On 2026-11-14 the product owner decided OD-20 and left the rest to Claude ("For open questions, I leave it up to you to make the right decision"); each decision is recorded under its recommendation and can be revisited. No owners are assigned yet. Decisions resolved in revision 2 are listed at the end with where they were resolved.

### Blocking Phase 0

#### OD-01. How does the authoritative workflow stay out of the pull request's control?

A `pull_request` workflow runs the pull request's own workflow file, so a pull request can edit the job that produces the required check (§6.3).

| Option | Strength | Cost |
|---|---|---|
| A. Ruleset-required workflow from a separate repository, pinned to a SHA | The pull request can't change the workflow file, if Phase 0 confirms the pinned file is what runs. The workflow must still run no code from the checkout (§16.2) | GitHub Enterprise Cloud (announced plan; documentation inconsistent); organization-level setup |
| B. CODEOWNERS on all of `/.github/` plus required code-owner review | Paid plans for private repositories (Pro, Team or Enterprise); free for public ones | Depends on a reviewer noticing workflow changes, and on no run from any commit of the pull request posting the check name; GitHub doesn't enforce the latter |
| C. External CI service or GitHub App that posts the status | The pull request can't change it | Proctor must build and operate a service; outside v1 non-goals |

**Recommendation:** document A as the strong setup and B as the baseline, build Phase 0 so both work, and confirm A in a test organization. Reports under B carry `trust.workflow_changed` (MODE-04). **Blocks:** Phase 0 reference workflow.

**Decided (2026-11-14, Claude, delegated by the product owner):** the recommendation. Phase 0 doesn't depend on GitHub Enterprise Cloud: B is the baseline, A is documented and confirmed once an organization on Enterprise Cloud is available.

#### OD-02. Should Proctor fail an authoritative run when protected inputs changed?

Proctor can't see code-owner approval without calling the GitHub API.

- A. Report changes only; the platform's required review enforces approval. Simple, but Proctor's result alone says nothing about approval (§1 and §22 are worded accordingly).
- B. Query the GitHub API for code-owner approval of the latest push, and fail (exit 4) without it. Stronger evidence in the report, but couples Proctor to GitHub, needs a token with read access, and duplicates platform logic that can drift.

**Recommendation:** A for v1. **Blocks:** INT-10 behavior.

**Decided (2026-11-14, Claude, delegated by the product owner):** the recommendation.

#### OD-11. Where do trusted bindings and the configuration path live?

In authoritative mode, the trusted workflow passes `--config` and `--binding` (MODE-01).

- A. In the separately controlled workflow repository (fits OD-01 A).
- B. In CI variables and secrets of the application repository, with the binding file generated by the workflow.

**Recommendation:** follow OD-01: A with a ruleset-required workflow, B otherwise. **Blocks:** Phase 0 authoritative mode. (Moved from Phase 2 in revision 2.)

**Decided (2026-11-14, Claude, delegated by the product owner):** the recommendation.

#### OD-16. Should Proctor's own CODEOWNERS evaluation fail the run?

INT-11 fails on errors reported by GitHub's CODEOWNERS errors endpoint. Proctor also evaluates CODEOWNERS itself to find protected paths with no owner from `protected_owners`. Its matcher may differ from GitHub's.

- A. Report only (revision 2).
- B. Fail with exit 4.

**Recommendation:** A until Proctor's matcher is tested against GitHub's behavior on the patterns the reference setup uses; then B. **Blocks:** INT-11 final behavior.

**Decided (2026-11-14, Claude, delegated by the product owner):** the recommendation.

### Blocking Phase 2

#### OD-10. Expression and path syntax

Variables use one `${...}` syntax (`${name}`, `${steps.<id>.trace_id}`, `${identities.alice.user_id}`, `${row.col}`, `${run.id}`), interpolated once with type rules (HTTP-02, HTTP-03). Still open: the JSONPath dialect and how accepted-value lists are written.

**Recommendation:** an RFC 9535 JSONPath implementation, and no regular-expression matchers in v1, since broad matchers weaken assertions. Phase 0 uses a throwaway subset (§20). **Blocks:** Phase 2 schema.

**Decided (2026-11-14, Claude, delegated by the product owner):** the recommendation.

### Blocking Phase 3

#### OD-05. Which identity mechanisms does the reference setup document?

v1 supports `none`, `bearer`, `basic` and `oauth2_client_credentials` (AUTH-01). Revision 2 provisions identities inside the disposable environment (FIX-01), which also makes fork pull requests possible.

**Recommendation:** document one test identity provider started with the environment, and one initialization job that creates accounts. Defer OIDC and mTLS until a user needs them. **Blocks:** Phase 3 guide.

**Decided (2026-11-14, Claude, delegated by the product owner):** the recommendation.

#### OD-12. Who defines the verification dependencies?

**Recommendation:** a protected file in the repository (`compose.verify.yaml`), whose referenced files sit under the protected root (INT-13). **Blocks:** Phase 3 disposable environments.

**Decided (2026-11-14, Claude, delegated by the product owner):** the recommendation.

#### OD-19. Do v1 authoritative runs support fork pull requests?

Fork runs get a read-only token and no secrets (§6.3).

**Recommendation:** yes, if OD-05's identities are created inside the environment, which needs no secrets. Otherwise document that fork pull requests can't merge until a maintainer moves the branch into the repository. Either way, a fork pull request must never pass by being skipped. **Blocks:** Phase 3.

**Decided (2026-11-14, Claude, delegated by the product owner):** the recommendation.

### Blocking Phase 4 and later

#### OD-18. Default time limits

TIME-07 makes `min_interval`, `max_samples` and `max_window` protected policy. Proposed defaults are 100 ms, 1,000 samples and 10 minutes; they come from the QA review and are not based on measurements.

**Recommendation:** keep the proposed defaults for Phase 4 and revisit after running the example applications. **Blocks:** Phase 4.

**Decided (2026-11-14, Claude, delegated by the product owner):** the recommendation.

#### OD-15. Should an earlier failed attempt block the commit?

Anyone with write access can re-run a failed job, and a passing later attempt replaces the failed one in the merge check (§4.4). REP-10 reports earlier failed attempts but doesn't change the result.

- A. Report only (revision 2).
- B. Fail if any earlier attempt for the same tested commit and input hashes recorded a behavioral FAIL. Stops a flaky defect from merging by re-run, but a single flaky failure then blocks that commit until a new commit is pushed.

**Recommendation:** A for v1; revisit if re-runs mask real defects in practice. **Blocks:** REP-10 behavior.

**Decided (2026-11-14, Claude, delegated by the product owner):** the recommendation.

#### OD-06. Is direct data seeding needed?

**Recommendation:** not in v1. Revisit with a separate setup adapter, its own credentials and trusted-job control, when a real scenario needs it. **Blocks:** nothing now.

**Decided (2026-11-14, Claude, delegated by the product owner):** the recommendation.

#### OD-08. Can observation start before the triggering request?

Changes made and reverted during the triggering request aren't observable (§4.4).

**Recommendation:** defer. **Blocks:** nothing now.

**Decided (2026-11-14, Claude, delegated by the product owner):** the recommendation.

#### OD-03. Is a separate approval record needed?

**Recommendation:** defer until a user needs approval evidence outside GitHub. **Blocks:** nothing now.

**Decided (2026-11-14, Claude, delegated by the product owner):** the recommendation.

#### OD-04. Signing releases and reports

**Recommendation:** checksums in v1; signed releases with Sigstore before 1.0; report attestations where the plan allows (private repositories need GitHub Enterprise Cloud). **Blocks:** Phase 6.

**Decided (2026-11-14, Claude, delegated by the product owner):** the recommendation.

#### OD-17. Should every absence check need a presence twin?

The QA review proposed that each `never` check have a matching presence check in a listed scenario. Revision 2 adopted only the liveness record (LOG-08) and the Visibility restriction (TMP-08).

**Recommendation:** not in v1. The liveness record already turns the "channel can't see the event" case into ERROR; a twin rule adds authoring cost. Revisit if misspelled-event false passes appear. **Blocks:** nothing now.

**Decided (2026-11-14, Claude, delegated by the product owner):** the recommendation.

### Raised by mutation testing (REQ-34, REQ-35)

These came up while closing the mutation-testing gaps. Each is built as recommended, so nothing is blocked, but each changes what a product owner signs up for.

#### OD-20. Should warnings fail an authoritative run?

FIX-05 (a matched value without `${run.id}`) and HTTP-09 (a length on an unfiltered list) are warnings: they show in every report and don't change the exit code, because some legitimate values (enums, fixed codes) can't carry a run ID.

- A. Warnings never fail (built).
- B. An authoritative run fails (exit 3) on any warning unless the scenario acknowledges it, for example `run_scoped: not applicable, enum value`.

**Recommendation:** B before v1. A warning a pipeline doesn't fail on gets ignored. **Blocks:** nothing now.

**Decided (2026-11-14, product owner):** stricter than B: every warning is a failure, in every mode. Built as EXE-08: FIX-05 and HTTP-09 are errors (exit 3), and a step can waive either only with a written reason (`waive: { FIX-05: <reason> }`), which the semantic diff lists as a weakening. The waiver is Claude's choice under REQ-38: without it FIX-05 would force authors to drop legitimate assertions.

#### OD-21. Secrets in requests (AUTH-07)

`${secret.name}` lets an approved specification send a binding-supplied value, such as a test user's password, in a request body or header. Login rules (throttling, counter reset) can't be tested without it. Any approved specification can send it to any path of the target, and an application that logs request bodies could then record it.

**Recommendation:** keep AUTH-07, and allow only secrets created for the disposable environment, never production credentials. CODEOWNERS approval of the specification already covers the rest. **Blocks:** nothing now; the product owner should confirm.

**Decided (2026-11-14, Claude, delegated by the product owner):** the recommendation; the delegation stands in for the product owner's confirmation.

#### OD-22. Token-lifetime checks

A login that issues tokens valid for 24 hours instead of 15 minutes (held-out mutant HM-22) isn't detectable: Proctor never decodes credentials, and waiting out the lifetime is too slow.

- A. Accept the gap (current).
- B. Add an assertion that decodes a JWT *from a response* (not from the binding) and checks its claims, such as `exp - iat`.

**Recommendation:** B in Phase 6, limited to responses: token lifetime is a security contract. **Blocks:** nothing now.

**Decided (2026-11-14, Claude, delegated by the product owner):** the recommendation.

#### OD-23. Scenarios that need a disposable environment

PGU-025 and PGU-026 assume at most 100 users. PGU-036 and PGU-049 assume no other login attempt for the same user within a minute. They hold under FIX-06, which requires a fresh environment per authoritative run. In a shared environment they could give false FAILs, never false PASSes.

**Recommendation:** keep them, and let a scenario declare `requires: disposable_environment`, so a run against an environment the binding doesn't mark as disposable reports them NOT_RUN with that reason, not FAIL. **Blocks:** nothing now.

**Decided (2026-11-14, Claude, delegated by the product owner):** the recommendation.

### Resolved in revision 2

| Decision | Resolution | Where |
|---|---|---|
| OD-07. Status-only steps | Verification steps expecting 2xx need a body or header assertion; 4xx and 5xx may assert status alone; setup steps need only a status | EXE-03 |
| OD-13. Docker logs in v1 | JSON Lines files only | LOG-01 |
| OD-14. Base for change detection | First parent of the test merge, after checking the second parent is the head; otherwise the platform's merge base | INT-09 |

### Resolved in Phase 1

| Decision | Resolution | Where |
|---|---|---|
| OD-09. Approved format | Proctor's own schema. The product owner decided on 2026-11-14 that Proctor has its own runner, so no other runner's format exists to approve | Appendix B, §19 |

### Ideas not adopted yet

From the "Tests as data" article and the reviews. Not v1 requirements.

- **Requirement traceability:** link scenarios to requirement IDs so a reviewer sees which requirements lose coverage when a scenario is removed.
- **Invariants:** checks that run after every scenario, such as "no project has two owners".
- **Mutation testing of specifications:** planned after Phase 4 (§17).

---

# Appendix B. Runner decision record

**Question:** Should Proctor's execution backend be Venom, Hurl, Step CI, or a minimal runner built for Proctor?
**Checked:** 2026-11-14, by reading each project's source and documentation. Nothing was executed; "Observed" below means seen in the source code, not seen running.
**Status:** Decided on 2026-11-14 by the product owner: Proctor executes specifications with its own runner. This is the Phase 1 decision record (§20). The evaluation below is kept as the record of what was examined and what Proctor learned from it.

### Decision

**Proctor has its own runner.** No other test runner is used as a library, a subprocess or a backend, and no code is copied from one. Other tools are a source of ideas only; "Ideas Proctor takes" below lists them, and "No copied code" sets the rule.

Why this holds up against the evaluation:

- **No existing runner can serve through its CLI.** Each one lets a test file skip checks, redirect its target, or exit in ways the integrity layer can't trust (Findings).
- **Wrapping wouldn't save much.** Revision 2 already puts scheduling, tables, setup, interpolation and trace context in Proctor's core (§19), and Proctor needs its own PostgreSQL, Temporal and log executors whatever the choice. A wrapped Venom or Hurl would have contributed one HTTP request and some assertion helpers.
- **It removes a dependency the integrity layer would have to police.** Venom's `lib/` executors, Hurl's in-file options and every backend's retry options would each have needed a rejection rule.

What it costs: Proctor writes its HTTP execution, JSONPath evaluation (through a library, OD-10), assertions, captures and interpolation, and that code starts less exercised than Venom's or Hurl's. The test suite and the example applications in three languages (§17) are where it gets exercised.

Phase 0 no longer runs the Venom proof of concept or the native-runner comparison planned in an earlier version of Appendix B; the decision makes their outcome irrelevant. The decision rule set for them was not applied.

### Context

The integrity layer in the PRD needs these properties from whatever executes the specifications. A candidate that can't provide one, even through wrapping, is out.

| ID | Mandatory property | Source in the PRD |
|---|---|---|
| M1 | Every required scenario has a stable, unique ID and a per-ID result | INT-02, INT-03 |
| M2 | Nothing in a specification can skip a required scenario, or the wrapper can detect it | INT-03, INT-04 |
| M3 | Failures, errors and invalid input are distinguishable | REP-07, REP-08 |
| M4 | Specifications can't redirect the target or connections set by the trusted binding | §5.2, §17.1 |
| M5 | No shell or code execution from specifications | SEC-06 |
| M6 | Parameterized, read-only PostgreSQL queries | DB-01, DB-02 |
| M7 | Bounded polling; `consistently` and `never` can be added | §11.9 |
| M8 | Temporal and JSON-log checks can be added without forking | §11.6, §11.8 |
| M9 | The pinned version can be verified | SEC-11 |
| M10 | Per-step evidence for reports | §13 |

**Baseline (doing nothing):** a team can already combine Hurl or Venom with CODEOWNERS on its test directory and a required status check. That covers part of outcomes O-1 and O-2 in the PRD. It lacks required-ID enforcement, provenance, evidence levels, `consistently`, Temporal and log checks, and any protection against a test file that skips or redirects itself. Those gaps are Proctor's reason to exist, and also its scope.

### Findings

Labels: **D** documented, **O** observed in source, **I** inferred, **U** unresolved.

#### Venom

Source: `github.com/ovh/venom` at commit `0eb5198`. Latest stable release v1.3.0 (2026-01-06), pre-release v1.4.0-beta.4 (2026-10-01). Commits most months through 2026-10-30. Apache-2.0, Go.

| Property | Finding | Label |
|---|---|---|
| M1 | Test cases have an `id` field (`types.go:156`) that reaches JSON and XML output. It is undocumented and not checked for presence or uniqueness. Names are slugified in reports (`process_testsuite.go:221`). | O |
| M2 | `skip:` assertion blocks skip cases or steps (README "If one condition from the skip block is not true, it's skipped"). The run passes when one case passes and the rest are skipped (`process.go:210-216`). A suite with no test cases passes. An HTTP step with no assertions defaults to `statuscode ShouldEqual 200` (`executors/http/http.go:91-92`); a step with no type and no assertions passes vacuously. | D, O |
| M3 | Every non-pass outcome exits 2: invalid YAML, missing variables, assertion failures, all-skipped runs, flag errors (`cmd/venom/run/cmd.go:390-410`). XML writes failures and errors as the same `<error>` element. | O |
| M4 | Suite `vars:` can't override CLI variables (`process_files.go:181`), but a step's `vars:` with `default:` can shadow one for later steps (`process_testcase.go:213-214`), and a specification can hard-code any URL. `.venomrc` and `VENOM_VAR_*` are read from the working directory, `$HOME` and the environment. | O |
| M5 | The `exec` executor runs `/bin/sh` commands and is the default for steps with `script` or `command` (`venom.go:133-135`). YAML user executors load from `<suite dir>/lib`, and Go plugins from `lib/<name>.so` (`venom.go:340-350`). The CLI has no allowlist. | D, O |
| M5 (wrapped) | As a Go library, a wrapper can register only chosen executors (`RegisterExecutorBuiltin`, as `cmd/venom/run/cmd.go:328-330` does). User executors from `lib/` and the plugin fallback still load (`venom.go:170-186`), so the wrapper would also have to control the suite directory. | I |
| M6 | The SQL executor runs query strings with `db.Queryx(s)` and no bind arguments (`executors/sql/sql.go:79,97`). Values reach SQL only through template interpolation. No read-only option. `dbfixtures` writes to the database with testfixtures' safety check disabled (`dbfixtures.go:131,151`). **Fails M6; Proctor would need its own executor.** | O |
| M7 | `retry`, `delay` (whole seconds) and `timeout`, bounded by the retry count. The timeout passes the parent context to the executor, so a timed-out executor isn't cancelled (`process_teststep.go:147-149`). No `consistently`. | O |
| M8 | Possible through custom built-in executors in a Go wrapper. | I |
| M9 | Release assets are bare binaries with no checksums or signatures. `venom update` replaces the binary in place. Pinning would mean building from a pinned source commit. | O, D |
| M10 | JSON output has per-step status, assertion results, interpolated inputs, executor results (HTTP request, status, headers, body), retries and timings (`types.go:187-208`). One file per suite, named by basename, so suites with the same basename in different directories overwrite each other (`venom_output.go:113-114`). XML and TAP misreport skips; TAP misreports failures. | O |
| Other | No list, dry-run or lint command. `Parse()` is separate from `Process()` in the Go API, so a wrapper could enumerate cases without running them, but `skip:` can't be evaluated without running. Suites and cases run sequentially in command-line order. `range:` over a map iterates in random order. The Go API has no stated stability guarantee. | O, I, U |

#### Hurl

Source: `github.com/Orange-OpenSource/hurl` at commit `cb4fa2d`. Latest release 8.1.0 (2026-10-28); master is 9.0.0. Daily commits. Apache-2.0, Rust. Releases ship `.sha256` checksums; no signatures or attestations found.

| Property | Finding | Label |
|---|---|---|
| M1 | Entries are identified only by file, index and line in the JSON report (`src/json/result.rs:67-83`). Proctor would have to map IDs to positions. | O |
| M2 | `[Options] skip: true`, which can be driven by a variable (`docs/grammar.md:120`), and `repeat: 0`. A skipped entry produces no result at all (`runner/hurl_file.rs:233-246`), so it is absent from every report. `--from-entry`, `--to-entry` and `--no-assert` also bypass checks. An all-skipped file exits 0. | D, O |
| M3 | 0 success, 1 command-line error, 2 parse error, 3 runtime error, 4 assertion failure (`main.rs:39-44`, `docs/manual/hurl.md`). JUnit separates `failure` from `error`. **Meets M3.** | D |
| M3 (precedence) | When both runtime errors and assertion failures occur, 3 wins (`exit_code()` in `packages/hurl/src/main.rs`). The manual lists the codes but not the precedence. | O |
| M4 | In-file `[Options]` override CLI variables ("overrides all others", `docs/manual/hurl.md:159-164`). Files can also set `resolve`, `connect-to` or `proxy`, or use absolute URLs. 8.1.0 reads `~/.config/hurl/config` implicitly. A wrapper must reject these options and isolate `HOME` and `XDG_CONFIG_HOME`. | D, O |
| M5 | No code execution; template functions are `newUuid` and `newDate` only. File reads are confined to `--file-root`. **Meets M5.** | D, O |
| M6, M8 | HTTP only. No SQL, Temporal, logs or plugins. | D |
| M7 | `retry: N` retries on any failure, including assertions, so it works as polling. `-1` means unlimited and must be forbidden. No `consistently`. | D |
| M9 | Checksummed releases. **Meets M9.** | O |
| M10 | JSON report per entry: request, response, timings, captures, each assertion with line and message; secrets redacted (`json/result.rs:76-148`). **Meets M10.** | O |
| Other | `hurlfmt --out json` parses a file into a JSON syntax tree without running it, which a wrapper can use to inspect options before execution. | D |

#### Step CI

Source: `github.com/stepci/stepci` at commit `e9af898` and `stepci/runner` at `01b236f`. CLI 2.8.2 (2024-06-10); last CLI commit 2024-08-03; last runner commit 2025-04-05, a dependency bump. MPL-2.0, TypeScript, npm.

| Property | Finding | Label |
|---|---|---|
| M2 | A step `if:` that evaluates false is marked skipped and passed (`runner index.ts:375-377`); an all-skipped workflow exits 0. | O |
| M3 | Exit 5 for any non-pass, assertion or error alike (`src/index.ts:31`). | O |
| M4 | Test-level `env` overrides CLI values (`runner index.ts:375,382`). | O |
| M5 | The `plugin` step runs `require(params.id)` (`runner steps/plugin.ts:20`), so any test file can load any resolvable Node module. **Fails M5.** | O |
| M10 | The CLI has no JSON, JUnit or other report output; details are only available through the library API. | O |
| Other | Analytics are sent to PostHog on each command unless `STEPCI_DISABLE_ANALYTICS` is set (`src/lib/analytics.ts`). No dry-run or list mode. | O |

**Rejected:** fails M5, the project is dormant, and analytics by default are unsuitable for a verification tool.

#### Minimal Proctor-native runner

Not built; assessed from the requirements.

- Meets every mandatory property by design, because the schema, executors, results and exit codes are Proctor's own.
- Cost: an HTTP executor on Go's `net/http`, JSONPath evaluation (a third-party library), the assertion engine, captures and interpolation, polling, and the YAML schema with validation. Most of these are things the integrity layer has to control or constrain anyway, whichever runner is chosen.
- Risk: Proctor rebuilds what already exists, and its HTTP and assertion code is less exercised than Venom's or Hurl's.

### Comparison

| | M1 IDs | M2 no silent skip | M3 exit codes | M4 no redirect | M5 no code exec | M6 SQL | M7 timing | M8 extensible | M9 pinning | M10 evidence |
|---|---|---|---|---|---|---|---|---|---|---|
| Venom, CLI | Partial | No | No | No | No | No | Partial | No | No | Yes |
| Venom, Go library wrapper | Wrapper enforces | Wrapper enforces | Wrapper classifies | Wrapper validates | Partial: `lib/` still loads | Own executor | Own constructs | Yes | Build from source | Yes |
| Hurl, wrapped | Wrapper maps | Wrapper rejects | Yes | Wrapper rejects | Yes | Not covered | Partial | Not covered | Yes | Yes |
| Step CI | Partial | No | No | No | No | Not covered | Partial | Plugin, unsafe | npm only | Library only |
| Native runner | Yes | Yes | Yes | Yes | Yes | Yes | Yes | Yes | Yes | Yes |

"Wrapper enforces" means Proctor writes the logic; the runner doesn't provide it.

### Effect of revision 2 of the PRD

Revision 2 moves sample scheduling, table expansion, setup handling, interpolation, trace context and per-sample transactions into Proctor's core (§19, ARC-02), and forbids using a backend's retry, delay or repeat options (TIME-06), because Venom's whole-second delays and uncancelled timeouts, and Hurl's retries, break the time bounds. A runner backend can therefore contribute at most single-operation execution and assertion helpers. This strengthened the inferred native-runner outcome before the product owner decided it.

### Format consequence

If Proctor uses Venom's or Hurl's format directly, the specifications people approve are Venom YAML or Hurl files plus restrictions. That conflicts with the schema in §11 (scenario IDs, tables, identities, evidence levels, `consistently`). The alternative, translating Proctor's schema into Venom suites or Hurl files, adds a compiler and leaves Venom or Hurl as little more than an HTTP executor. This was OD-09 in Appendix A, resolved by the decision: people approve Proctor's own schema.

### Ideas Proctor takes

These are ideas and documented behaviors, described here in Proctor's own words. Most are already requirements in the PRD; the requirement is what Proctor implements, not the other tool's code.

| Idea | Seen in | Where Proctor has it |
|---|---|---|
| Separate exit codes for a failed check, an error and invalid input | Hurl's exit codes; JUnit's `failure` versus `error` | §13.3 exit codes, REP-07 |
| Parse and validate everything before running anything | Hurl's `hurlfmt`, Venom's separate parse step | `proctor validate`, EXE-01 |
| Per-step evidence: the request as sent, the response, each assertion and its message | Venom's and Hurl's JSON reports | §13 |
| Redact secrets in reports | Hurl's JSON report | REP-04 to REP-06 |
| Confine file reads to one root | Hurl's `--file-root` | INT-01 |
| Publish checksums with each release | Hurl's releases | SEC-11 |

And what Proctor avoids, because a tool above showed how it goes wrong:

| Avoided | Seen in | Proctor's rule |
|---|---|---|
| Test files that skip themselves, conditionally or not | Venom `skip:`, Hurl `skip`/`repeat: 0`, Step CI `if:` | INT-03, INT-04 |
| A step with no assertions passing, or a hidden default assertion | Venom's HTTP default and vacuous steps | EXE-03 |
| Test files overriding the target or connections set by the run | Venom step `vars`, Hurl `[Options]`, Step CI `env` | HTTP-05, SEC-07 |
| Shell commands or code loaded from the test directory | Venom `exec` and `lib/`, Step CI `plugin` | SEC-06 |
| Configuration read implicitly from the home directory or environment | `.venomrc`, `VENOM_VAR_*`, Hurl's user config | MODE-01 (configuration and binding come only from paths the run names) |
| SQL built by string interpolation | Venom's SQL executor | DB-01 |
| Timeouts that don't cancel the work, delays in whole seconds, unbounded retries | Venom, Hurl `retry: -1` | TIME-06 and §11.9 |
| Reports that overwrite each other when two files share a name | Venom's output files | One JSON report per run, results keyed by scenario ID (REP-08) |
| Analytics sent by default | Step CI | SEC-07 (Proctor contacts only the hosts it lists) |

### No copied code

Venom and Hurl are Apache-2.0 and Step CI is MPL-2.0. Proctor copies none of their code, under any license.

- No source from another test runner is copied, translated into Go or closely paraphrased into Proctor. That covers tests, fixtures and documentation text too.
- Runner code is written from Proctor's requirements and from primary references (Go's standard library documentation, RFC 9110 for HTTP, RFC 9535 for JSONPath), not with another runner's source open. The clones read for this evaluation have been deleted; the Findings cite public commits instead.
- A library used as a declared dependency in `go.mod`, such as `go.yaml.in/yaml/v3` (MIT and Apache-2.0), is not copied code. Its license is checked before it is added, and its notices travel with Proctor's releases.
- File-format ideas, such as a YAML suite with named steps, are ideas rather than code. Proctor's schema is its own design (§11).

This is an engineering rule, not legal advice. If the project needs a firm answer on a licensing question, such as what a release must ship to meet Apache-2.0's NOTICE terms, ask someone qualified.

### Risks and uncertainty

- Proctor's HTTP execution and assertion code starts with no field use. Mitigation: the Phase 2 example applications in Go, Node.js and Python, and the adversarial tests in Appendix C.
- The Findings come from reading source at one commit, not from running it. They no longer drive a decision, so they weren't re-checked.
- What would reopen the decision: a requirement Proctor can't meet with reasonable effort on its own, such as a protocol whose client is large and specialized. Even then, the choice would be a library dependency, not another test runner.

### Sources

- Venom: https://github.com/ovh/venom (commit `0eb5198`), releases page, README and `executors/*/README.md`.
- Hurl: https://hurl.dev/docs/manual.html and https://github.com/Orange-OpenSource/hurl (commit `cb4fa2d`).
- Step CI: https://docs.stepci.com and https://github.com/stepci/stepci (commit `e9af898`), https://github.com/stepci/runner (commit `01b236f`).

---

# Appendix C. Acceptance tests

The observable tests that show each requirement and outcome in this document. A phase in §20 is done when the tests assigned to it pass. Written with revision 2 and extended since; not yet reviewed.

Every expected outcome is something a test can assert:

- an exit code;
- a JSON report field and value;
- a job-summary line;
- an entry in `proctor diff` output;
- a GitHub merge state.

GitHub merge states use the GraphQL `mergeStateStatus` values (`BLOCKED`, `BEHIND`, `CLEAN`) and `reviewDecision` values (`REVIEW_REQUIRED`, `APPROVED`).

### Conventions

**Test IDs.**

| Prefix | Group |
|---|---|
| AT-I | Integrity |
| AT-W | Workflow and platform |
| AT-T | Time checks |
| AT-R | Results and exit codes |
| AT-O | Outcomes |
| AT-Q | Single-requirement tests referenced from the coverage table |

**Report fields.** REP-08 names only `result`, `mode`, `trust` and `summary`. Other field names in this appendix are provisional and marked **(p)**. Phase 0 and Phase 2 must fix them (see "PRD inconsistencies", item 16).

**Time notation.**

- t0 is the start of a check's first sample.
- `#n` is the n-th sample, counting from `#1` at t0.
- `@+t` is an offset from t0.
- `job(t, f)` means the example application's background job changes field `f` of Bob's project `t` seconds after the denied delete, and leaves it changed.
- Unless stated, a GET reads state when the request arrives, so a sample observes state at its start time.
- PA-002's `still-readable-by-bob` runs 10 s at 250 ms, so its samples are due at @+0, +0.25 … +10.0: 41 samples.

**Common fixture.**

- **Test repository R** in a test organization, configured per §6.3.
  - Base commit **B** on `main`, holding the manifest, PA-001, PA-002, PA-ALLOWED and PA-DENIED.
  - Pull request head **H**, and GitHub's test merge commit **M** (first parent B, second parent H).
  - Tests marked **[org]** need this test organization and can't run in a local Git repository.
- **Example application** "projects", built from R with fault flags set in its configuration:
  - `FAULT_OWNER_CHECK_OFF`: any user can read or delete any project.
  - `FAULT_BG_JOB=<t>,<field>`: see `job(t, f)`.
  - `FAULT_LOG_FRESH_TRACE`: logs events under a new trace ID.
  - `FAULT_LOG_SUCCESS_ONLY`: logs `project.export.completed` without producing an export.
  - `FAULT_EXPORT_NOOP`: the export workflow completes without writing the export.
  - `FAULT_SLOW_GET=<ms>`: GETs read state on arrival, then delay the response.
  - Probe endpoints: `/probe/echo`, `/probe/fail-once`, `/probe/redirect-external`, `/probe/headers`, `/probe/concurrency`.
- **Stub S:** an HTTP server that answers 200 to everything and counts requests.
- **Reference workflow W:** per §16.2, with build, verification and aggregator jobs.

---

### AT-I: Integrity

All tests run in authoritative mode on M unless stated. "Listed" means it appears in three places: `proctor diff --base B --head H` output, the report's protected-changes list (p), and a job-summary line naming the path, the scenario or case ID, and "approval required".

| ID | Attack or mistake | Expected observable | Requirements | Phase |
|---|---|---|---|---|
| AT-I-01 | H deletes `specs/project-access.yaml` but keeps its manifest entry | `proctor integrity --commit M` exits 4; `proctor run` exits 4, `result: integrity_violation`, and `summary` names INT-01 and the path | INT-01, INT-04 | 0 |
| AT-I-02 | A manifest path differs from the tree entry only in letter case | Exit 4 (INT-01), on every filesystem | INT-01 | 0 |
| AT-I-03 | H removes the manifest entry and keeps the file | Listed (manifest modified; its scenarios `removed`); the file appears under `unlisted` (p); the exit code comes from execution (0 if the rest pass) | INT-05, INT-10 | 0 |
| AT-I-04 | H moves a spec without changing its bytes and updates the entry | Diff entry `moved`, same blob and scenario IDs; exit 0 | INT-08 | 0 |
| AT-I-05 | H moves a spec and changes PA-D-01's 403 to 200 (Git would call it a rename, R099) | Diff entries `removed` (old path) and `added` (new path), and scenario PA-D-01 `moved`, modified; never "renamed" only | INT-08 | 0 |
| AT-I-06 | H changes scenario ID `PA-001` to `PA-101` | Listed as `PA-001` removed and `PA-101` added | INT-02, INT-10 | 0 |
| AT-I-07 | Two specs use scenario ID `PA-001` | `proctor validate` exits 3, naming both files | INT-02 | 0 |
| AT-I-08 | H changes PA-D-01's expected status from 403 to 200 | Listed; with the fault present the check passes, so AT-O1 governs merge. Phase 6 adds the semantic-diff line "PA-D-01 expected status 403 → 200" | INT-10, §7.5 | 0 (listing), 6 (semantic) |
| AT-I-09 | H changes 403 to the accepted-value list `[200, 403]` | Listed; the semantic diff says "broadened matcher" | §7.5 | 6 |
| AT-I-10 | H replaces an exact value with a wildcard | `proctor validate` exits 3: wildcards aren't an assertion type (§11.2) | §11.2 | 2 |
| AT-I-11 | H changes PA-D-01's identity from `alice` to `bob` | Listed; the semantic diff says "identity changed in denied case PA-D-01" | §7.5 | 6 |
| AT-I-12 | H lengthens an `eventually` timeout, shortens a `consistently` duration, and lengthens its interval | Listed; the semantic diff marks each as weakening, with direction | §7.5 | 6 |
| AT-I-13 | H changes PA-ALLOWED's `bob-project` setup body | Listed; the semantic diff shows the changed setup on every case of PA-ALLOWED and PA-DENIED | §7.5, TBL-04 | 6 |
| AT-I-14 | H reorders scenarios in a spec | Listed; the semantic diff says "order changed" | §7.5 | 6 |
| AT-I-15 | H flips `requirement_is_operational` | Listed; the semantic diff names the flag | EVD-02, §7.5 | 6 |
| AT-I-16 | A runner test double omits PA-002 from its results | Exit 4; PA-002 `NOT_RUN`, reason `not_started` | INT-03 | 0 |
| AT-I-17 | Negative control for AT-I-16: PA-002's first step FAILs | Exit 1; PA-002 `FAIL`; later steps `NOT_RUN`, reason `earlier_step_failed`; no integrity violation | INT-03 | 0 |
| AT-I-18 | An empty manifest; separately, a listed spec with no scenarios or tables | Both: `proctor integrity` exits 4 before any request (stub and application request counts 0) | INT-04 | 0 |
| AT-I-19 | H adds `specs/extra.yaml`, not in the manifest | Not executed (its IDs are absent from results); listed under `unlisted` (p) | INT-05 | 0 |
| AT-I-20 | CODEOWNERS names a nonexistent team for `proctor/` | Exit 4; `summary` names INT-11 and GitHub's CODEOWNERS error | INT-11 | 0 [org] |
| AT-I-21 | CODEOWNERS exists in both `/.github/` and `/` | Exit 4 (INT-11) | INT-11 | 0 |
| AT-I-22 | A CODEOWNERS line leaves a protected path with no owner from `protected_owners` | Exit unchanged; the report lists the path as unowned (p). Whether this should fail is OD-16 | INT-11 | 0 |
| AT-I-23 | A protected spec becomes a symlink; a later commit changes only the target | Exit 4 (INT-13), naming the path and its mode `120000` | INT-13 | 0 |
| AT-I-24 | `proctor/specs` becomes a symlinked directory | Exit 4 (INT-13) | INT-13 | 0 |
| AT-I-25 | A submodule at `proctor/vendor` with `.gitmodules` `ignore = all` | Exit 4 (INT-13), mode `160000` | INT-13 | 0 |
| AT-I-26 | A protected spec becomes a Git LFS pointer | Exit 4 (INT-13) | INT-13 | 0 |
| AT-I-27 | `compose.verify.yaml` mounts `./scripts/init.sql`, which is outside the protected root and not listed in `proctor.yaml` | Exit 4 (INT-13), naming the reference | INT-13 | 0 |
| AT-I-28 | H edits a file that `compose.verify.yaml` references under the protected root | Listed; its hash appears in the report (INT-16) | INT-13, INT-16 | 0 |
| AT-I-29 | H adds `*.yaml text eol=crlf` to `.gitattributes` | Exit 4 (INT-14), naming the attribute and paths | INT-14 | 0 |
| AT-I-30 | H adds `proctor/** linguist-generated=true` | Exit 4 (INT-14) | INT-14 | 0 |
| AT-I-31 | A spec contains a duplicate key (`status: 403` then `status: 200`) | `proctor validate` exits 3, naming both lines | INT-15 | 0 |
| AT-I-32 | A spec uses an anchor and alias; separately a merge key `<<`; separately a custom tag | Each: exit 3 (INT-15) | INT-15 | 0 |
| AT-I-33 | A scenario uses a disabled adapter; separately an undeclared connection; separately an undeclared identity | Each: `proctor validate` exits 3 (INT-12) | INT-12 | 0 (adapter), 2 (identity) |
| AT-I-34 | H adds a second `proctor.yaml` at the repository root whose `manifest:` points to an unprotected file | No effect: the report's `proctor.yaml` hash equals the blob at the trusted path | MODE-01 | 0 |
| AT-I-35 | H adds `proctor/environments/ci.yaml` and points `local.yaml` at stub S, with `FAULT_OWNER_CHECK_OFF` set | Stub S count 0; PA-D-01 `FAIL`, exit 1, the same as a control run; the report's binding hash equals the trusted binding's | MODE-01 | 0 |
| AT-I-36 | A spec has `${project_id}` inside a `query` | `proctor validate` exits 3 (DB-01) | DB-01 | 4 |
| AT-I-37 | H adds only a trailing space and a YAML comment to a spec | Listed as modified; the report's hash for that spec equals SHA-256 of the committed blob at H | INT-10 | 0 |
| AT-I-38 | Checkout with `fetch-depth: 1`, so the base commit is absent | Exit 4; never "Protected changes against base: none" | INT-08, INT-09 | 0 |
| AT-I-39 | A workflow step modifies a spec in the working tree after checkout | The executed bytes and the report hash equal the blob at M, not the working tree | INT-07, MODE-02 | 0 |
| AT-I-40 | `FAULT_LOG_SUCCESS_ONLY`; the export scenario has a Level 1 export check and a Level 3 log check | Scenario `FAIL` at the Level 1 step; exit 1 | EVD-01 | 5 |
| AT-I-41 | `FAULT_EXPORT_NOOP`; the export scenario's Temporal check expects `completed` | The Temporal step passes; the scenario `FAIL`s at the Level 1 or 2 step; exit 1 | EVD-01, TMP-05 | 5 |
| AT-I-42 | A scenario has only Level 3 checks and no `requirement_is_operational` | `proctor validate` exits 3 (EVD-01) | EVD-01 | 5 |
| AT-I-43 | A manifest path contains `../` or is absolute | Exit 4. **Expected, not yet stated by the PRD** (see inconsistencies, item 11) | INT-01 | 0 |
| AT-I-44 | The running application's image digest differs from the build job's output | Exit 4 (REP-11) | REP-11, MODE-05 | 0 |

### AT-W: Workflow and platform

| ID | Attack or condition | Expected observable | Requirements | Phase |
|---|---|---|---|---|
| AT-W-01 | H changes the application build so that, during the build, it writes an executable to `/usr/local/bin/proctor` and listens on the target port. `FAULT_OWNER_CHECK_OFF` is set | The verification job log shows the executed binary's digest equal to the pin; stub count 0; the check fails with PA-D-01 `FAIL`, exit 1; merge state `BLOCKED` | SEC-12, §16.2 | 0 [org] |
| AT-W-02 | H commits `./proctor` and `bin/proctor` that write a marker file and print a passing report | No marker file exists after the job; the result equals a control run | SEC-11, §16.2 | 0 [org] |
| AT-W-03 | The download source serves a binary whose checksum differs from the pin | The job fails before Proctor runs (no report artifact); the aggregator fails; merge state `BLOCKED` | SEC-11 | 0 [org] |
| AT-W-04 | `FAULT_BG_JOB` fires in 50% of runs. Attempt 1 fails and attempt 2 passes | Attempt 2's report lists attempt 1 as an earlier behavioral FAIL (p), and the job summary names it. Merge state may become `CLEAN`: a documented limit (§4.4, OD-15) | REP-10 | 0 [org] |
| AT-W-05 | H breaks the application build | The aggregator check fails; merge state `BLOCKED` | §16.2 item 7 | 0 [org] |
| AT-W-06 | Option B: H adds `if: false` to the verification job only | The aggregator fails because a prerequisite was skipped; `BLOCKED` | §16.2 item 7 | 0 [org] |
| AT-W-07 | Option B: H also changes the aggregator so it always succeeds | `reviewDecision: REVIEW_REQUIRED` until a code owner approves the workflow change. Under option A, the edit has no effect | §6.3, OD-01 | 0 [org] |
| AT-W-08 | PR 2 passes on base B; then PR 1 merges and changes behavior so that PR 2 combined with it fails | PR 2's `mergeStateStatus` is `BEHIND`. After updating, the check fails and the state is `BLOCKED`. With a merge queue, the `merge_group` run fails and PR 2 leaves the queue | §6.3, O-2, REP-03 | 0 [org] |
| AT-W-09 | An earlier commit of H adds a workflow that posts the required check name; the final commit removes it | Expected `BLOCKED`. If `CLEAN`, Phase 0 records it as an option B limit in §4.4 | §6.3, P-08 | 0 [org] |
| AT-W-10 | Someone posts a commit status with the required check name using a personal token | `BLOCKED`, because the expected source is pinned | §6.3 | 0 [org] |
| AT-W-11 | Option A: H edits a copy of the required workflow inside R | The run log and the report's `trust.workflow_identity.job_workflow_sha` equal the pinned SHA | OD-01, MODE-03 | 0 [org] |
| AT-W-12 | Run on test merge M | The report records base = M's first parent, head = H, tested = M. If the second parent doesn't equal the event's head SHA, exit 4 | INT-09 | 0 |
| AT-W-13 | The workflow grants `id-token: write`; separately, it doesn't | With it, `trust.workflow_identity` holds `workflow_ref`, `workflow_sha`, `job_workflow_ref`, `job_workflow_sha`, `sha`, `event_name`, `run_id`, `run_attempt`, equal to the run's values. Without it, `trust.workflow_identity: unverified` | MODE-03 | 0 [org] |
| AT-W-14 | Option B: H changes the verification workflow, which still runs Proctor | `trust.workflow_changed: true`; job-summary line "not authoritative until the workflow change is approved" | MODE-04 | 0 [org] |
| AT-W-15 | A laptop run with `CI=true` and `GITHUB_ACTIONS=true` and no `--authoritative` | `mode: local`; "non-authoritative" in the terminal header and on the final result line | MODE-01, O-4 | 0 |
| AT-W-16 | A laptop run with `--authoritative` | `mode: authoritative` and `trust.workflow_identity: unverified`. The merge rule is unaffected: this report sets no status | MODE-01, MODE-03 | 0 |
| AT-W-17 | A previous run's report artifact is attached to a newer head | Merge state is governed by the status on the new head only | REP-01, REP-03 | 0 [org] |
| AT-W-18 | The verification job produces no report, or an unparseable one | The aggregator fails; `BLOCKED` | REP-13, §16.2 item 6 | 0 [org] |
| AT-W-19 | A fork pull request (only if OD-19 includes forks) | The check runs without repository secrets, and its result follows the scenarios. It is never `success` by skipping | §6.3, FIX-01 | 3 [org] |

### AT-T: Time checks

Unless stated, each test runs a scenario containing only the check under test, and expectations are under TIME-01 to TIME-12.

| ID | Edge | Input | Expected observable | Requirements | Phase |
|---|---|---|---|---|---|
| AT-T-01 | Interval larger than window | PA-002 with `interval: 30s` | `proctor validate` exits 3 | TIME-07 | 4 |
| AT-T-02 | Interval equal to window | `interval: 10s`, `duration: 10s` | Exit 3 (more than half the window) | TIME-07 | 4 |
| AT-T-03 | Interval equal to timeout | `eventually` 15 s / 15 s | Exit 3 | TIME-07 | 4 |
| AT-T-04 | Zero window | `duration: 0s`; separately `"0"` | Exit 3 | TIME-07 | 4 |
| AT-T-05 | Zero timeout | `timeout: 0s` | Exit 3 | TIME-07 | 4 |
| AT-T-06 | Negative | `duration: -10s` | Exit 3 | TIME-07 | 4 |
| AT-T-07 | No unit | `timeout: 10` | Exit 3, naming the field | TIME-07 | 4 |
| AT-T-08 | Zero interval | `interval: 0s` | Exit 3; no panic | TIME-07 | 4 |
| AT-T-09 | Below the minimum | `interval: 50ms` with `min_interval: 100ms` | Exit 3 | TIME-07 | 4 |
| AT-T-10 | Too many samples or too long | `duration: 10m`, `interval: 100ms` (6,001 samples); separately `duration: 11m` | Each: exit 3 | TIME-07 | 4 |
| AT-T-11 | Sample slower than the interval | `FAULT_SLOW_GET=600`; job(2.1, status) | Sample starts @+0, 0.6, 1.2, 1.8, 2.4. `FAIL` at #5 @+2.4 s; exit 1. Samples started 5. The skipped-due-time count can't be asserted yet (inconsistencies, item 7) | TIME-08 | 4 |
| AT-T-12 | Sample slower than 4 × interval | `FAULT_SLOW_GET=1500`, no change | Every sample times out at 1 s; `ERROR`; exit 2 | TIME-08, TIME-10 | 4 |
| AT-T-13 | Backend retry field | The spec has `retry: 3` on a step | Exit 3 | TIME-06 | 4 |
| AT-T-14 | PostgreSQL snapshot | `still-stored` with job(2.1, status) | `FAIL` at #10 @+2.25 s; exit 1 (one transaction per sample) | DB-02 | 4 |
| AT-T-15 | No change | PA-002, fast GETs | `PASS`; samples due 41, started 41 | TIME-02, TIME-04, TIME-08 | 4 |
| AT-T-16 | Change at the window's end | job(9.9, status) | `FAIL` at #41 @+10.0 s; exit 1 | TIME-08 | 4 |
| AT-T-17 | Slow last sample | `FAULT_SLOW_GET=400`; job(9.9, status) | The final sample runs and is awaited. `FAIL` at the last sample, which starts @+10.15 s or later; exit 1 | TIME-08 | 4 |
| AT-T-18 | Trigger anchor | PA-002 as written | The report's triggering step (p) is `alice-deletes`, and t0's offset from it is recorded | TIME-09, TIME-04 | 4 |
| AT-T-19 | Late log check | An event logged @+20 s after the trigger; the `eventually` log check starts @+45 s | `PASS` at #1 | LOG-06 | 5 |
| AT-T-20 | `eventually` in setup times out | A setup step with `eventually` never satisfied | Scenario `ERROR`, reason `setup_failed`; exit 2 | FIX-04 | 4 |
| AT-T-21 | Polling a POST | `eventually` on POST `/api/projects/{id}/export` | Exit 3 | TIME-11 | 4 |
| AT-T-22 | `save` with `consistently` | A `consistently` step that also has `save` | Exit 3 | TIME-11 | 4 |
| AT-T-23 | One-time link | `consistently` on a GET that returns 410 after the first call | `FAIL` at #2 @+0.25 s; exit 1 | TIME-10 | 4 |
| AT-T-24 | Rate limit | PA-002 against a limit of 20 requests per 10 s | `FAIL` at #21 @+5.0 s with status 429; exit 1 | TIME-10 | 4 |
| AT-T-25 | Satisfied on the deadline sample | `eventually` 15 s / 500 ms; the row completes @+14.8 s; queries take 300 ms | `PASS` at #31 | TIME-08 | 4 |
| AT-T-26 | Long-poll | A GET that answers @+25 s; `eventually` 15 s / 500 ms | Every sample times out at 2 s; `ERROR`; exit 2 | TIME-08 | 4 |
| AT-T-27 | Buffered log flush | `never` 10 s; the forbidden record is flushed @+10.3 s | `PASS`, with the TIME-05 notice (documented limit) | TIME-05 | 5 |
| AT-T-28 | Errors, then a pass | `eventually`: samples #1 to #3 connection refused, #4 satisfied | `PASS` at #4; errored samples 3 | TIME-10 | 4 |
| AT-T-29 | Every sample errors | The database is down for the whole window | `ERROR`; exit 2 | TIME-10, DB-05 | 4 |
| AT-T-30 | Permanent error | Invalid column (SQLSTATE 42703) | `ERROR` at #1; exit 2 | DB-05, TIME-10 | 4 |
| AT-T-31 | One errored sample | PA-002; #20 connection reset, others `active` | `ERROR` after the window; exit 2 | TIME-10 | 4 |
| AT-T-32 | Error, then a violation | As AT-T-31, and #30 returns `status: archived` | `FAIL` at #30; exit 1 | TIME-10 | 4 |
| AT-T-33 | 5xx response | #20 returns 503 with HTML | `FAIL` at #20; exit 1 | TIME-10 | 4 |
| AT-T-34 | Log file never created | The binding names a file the application never writes | Readiness fails: exit 2; every scenario `NOT_RUN`, reason `environment_not_ready` | FIX-07, LOG-04 | 3 |
| AT-T-35 | The run's only check errors throughout | A single-check run where every sample errors | Exit 2, not 4 | INT-04 | 4 |
| AT-T-36 | Misspelled event in `never` | `event: project.delted`; the access record carries the trace ID | `PASS`: a documented limit, because liveness doesn't catch misspellings (OD-17) | LOG-08 | 5 |
| AT-T-37 | Trace propagation removed | `FAULT_LOG_FRESH_TRACE`; `never` log check | `ERROR`, reason `correlation_not_observed`; exit 2 | LOG-08 | 5 |
| AT-T-38 | `never` through Visibility | `never` with a search-attribute lookup | Exit 3 | TMP-08 | 5 |
| AT-T-39 | Event during the triggering request, `eventually` | The handler logs the event before responding | `PASS` at #1 | LOG-06 | 5 |
| AT-T-40 | Event during the triggering request, `never` | The handler deletes, logs `project.deleted` with the trace ID, then answers 403 | `FAIL` at #1; exit 1 | LOG-06 | 5 |
| AT-T-41 | Change reverted within the request | The handler changes Bob's row and restores it before answering | `PASS` (documented limit, OD-08) | §4.4 | 4 |
| AT-T-42 | Time-window correlation with `eventually` | `correlation: time_window` on an `eventually` | Exit 3 | LOG-02 | 5 |
| AT-T-43 | Coarse timestamp, file position | A `never` with time-window correlation; a record written after the recorded position but stamped one second earlier | `FAIL`; exit 1 (position decides, not timestamp) | LOG-02 | 5 |
| AT-T-44 | Workflow started inside the request | POST `/export` starts `export-${export_id}`, then answers 202 | `PASS` when the chain's latest run is `completed` | TMP-07 | 5 |
| AT-T-45 | Clock skew | The Temporal server's clock is 300 ms behind Proctor's | Same as AT-T-44 (within `max_clock_skew`); the report shows both clocks' times | TMP-07 | 5 |
| AT-T-46 | Continue-As-New | The export continues-as-new twice | One candidate chain; `PASS`; the report lists 3 run IDs and the first run ID | TMP-02, TMP-03 | 5 |
| AT-T-47 | Retry chain | The first run fails; a retry run completes | Target `chain`: `PASS`. Target single run: `FAIL` at the final sample, last status `failed` (TMP-04) | TMP-02, TMP-04 | 5 |
| AT-T-48 | Static Workflow ID reused | `nightly-summary`, with an older completed chain present | The older chain is excluded by the pre-trigger snapshot. If the application starts no new chain: `FAIL` at the final sample; exit 1 | TMP-07 | 5 |
| AT-T-49 | Two candidate chains | Two new chains match | `ERROR`; exit 2 | TMP-07 | 5 |
| AT-T-50 | Wall-clock jump | The system clock steps forward 2 s at @+5 s | 41 samples over 10 s of monotonic time | TIME-12 | 4 |
| AT-T-51 | Slow start | The application port is closed for 5 s after start | No scenario `ERROR`; the results equal a run on an already-started environment | FIX-07 | 3 |
| FIX-08 | AT-Q-45 | 0 |
| AT-T-52 | Never ready | The readiness endpoint never answers 2xx within `startup_timeout` | Exit 2; every scenario `NOT_RUN`, reason `environment_not_ready`; not 4 | FIX-07, INT-03 | 3 |
| AT-T-53 | Late Temporal worker | The worker for task queue `exports` starts 20 s late | Readiness waits for a poller; the results equal a run with a prompt worker | FIX-07 | 3 |
| AT-T-54 | Paused for the whole window | The workflow is paused for 60 s of `eventually` 60 s / 1 s | `FAIL` at the final sample; last status `paused` | TMP-04 | 5 |
| AT-T-55 | Paused counted as running | `consistently: status: running`, 1 s interval; paused @+2.5 s | `FAIL` at #4 @+3.0 s | TMP-04 | 5 |
| AT-T-56 | Failed early | The run fails @+4.5 s with no newer run; `eventually: completed`, 60 s / 1 s | `FAIL` at the final sample (@+60 s), last status `failed`; no early stop (TMP-04) | TMP-04 | 5 |
| AT-T-57 | Unasserted field | job(2.1, name) | `FAIL` at #10 @+2.25 s, on `$.version` | §11.9 PA-002 | 4 |
| AT-T-58 | Cached GET | Bob's GET is served from a 30 s cache; job(2.1, status) | The HTTP step passes; `still-stored` `FAIL`s at #10 @+2.25 s; exit 1 | §10, PA-002 | 4 |
| AT-T-59 | Change after the window | job(12, status) | `PASS`, with the TIME-05 notice | TIME-05 | 4 |
| AT-T-60 | Change between samples | The field changes @+2.3 s and reverts @+2.4 s | `PASS`, with the TIME-05 notice | TIME-05 | 4 |
| AT-T-61 | Transient change a sample can see | The field changes @+2.1 s and reverts @+3.0 s | `FAIL` at #10 @+2.25 s (tests "every sample") | TIME-02 | 4 |

### AT-R: Results and exit codes

| ID | Situation | Expected observable | Requirements | Phase |
|---|---|---|---|---|
| AT-R-01 | PA-002's setup POST returns 500 | PA-002 `ERROR`, reason `setup_failed`; its steps `NOT_RUN`; exit 2 | FIX-04 | 2 |
| AT-R-02 | PA-ALLOWED's setup fails | PA-ALLOWED and PA-DENIED `ERROR`; every case `NOT_RUN`, reason `setup_failed`; exit 2, not 4 | TBL-06, INT-03 | 2 |
| AT-R-03 | PA-D-01 fails (`FAULT_OWNER_CHECK_OFF`) | PA-D-02 and PA-D-03 still run; PA-DENIED `FAIL`; exit 1 | TBL-06 | 2 |
| AT-R-04 | An earlier step fails | Later steps `NOT_RUN`, reason `earlier_step_failed`; scenario `FAIL`; exit 1 | INT-03 | 2 |
| AT-R-05 | An earlier step errors | Later steps `NOT_RUN`, reason `earlier_step_errored`; scenario `ERROR`; exit 2 | INT-03 | 2 |
| AT-R-06 | `FAIL` in one scenario and `ERROR` in another | Exit 1; `result: failed`; `summary` counts 1 failed and 1 error | REP-07, REP-08 | 2 |
| AT-R-07 | A listed spec fails validation | Exit 3; application and stub request counts 0 | REP-07 | 2 |
| AT-R-08 | A listed spec is unparseable YAML | Exit 3 | REP-07 | 2 |
| AT-R-09 | An integrity violation and a missing credential together | Exit 4 | REP-07 | 2 |
| AT-R-10 | SIGTERM during the second scenario | A report exists with `result: error` and `aborted: true`; unstarted scenarios `NOT_RUN`, reason `aborted`; exit 2 | REP-13 | 2 |
| AT-R-11 | Target unreachable from the start | Readiness fails; exit 2; scenarios `NOT_RUN`, reason `environment_not_ready` | FIX-07, R-01 | 3 |
| AT-R-12 | After an `ERROR` in scenario 1 | Scenarios 2 to n have their own results | REP-12 | 2 |
| AT-R-13 | Alice's token variable missing; separately empty | Exit 3; application request count 0 | AUTH-03 | 2 |
| AT-R-14 | `PROCTOR_ALICE_USER_ID` unset | Exit 3 | AUTH-03 | 2 |
| AT-R-15 | Alice and Bob bound to the same token; separately the same user ID | Exit 3 | AUTH-03 | 2 |
| AT-R-16 | An identity defined in the binding but not in `proctor.yaml` | Exit 3 | AUTH-03 | 2 |
| AT-R-17 | `identity_check` returns a different user ID for Bob | Exit 3 | AUTH-04 | 2 |
| AT-R-18 | Protected changes present; every check passes (OD-02 A) | Exit 0; `result: passed`; `summary` shows protected changes N > 0; job-summary lines list them | REP-08, INT-10 | 0 |
| AT-R-19 | A request step that expects 200 asserts status only | Exit 3. The same step expecting 403 with status only: valid | EXE-03 | 2 |
| AT-R-20 | `result` matches the exit code | For every test in this appendix, `result` maps to the exit code: passed→0, failed→1, error→2, invalid→3, integrity_violation→4 | REP-08 | 2 |
| AT-R-21 | Proctor panics internally (test-only fault hook in a test build) | `result: error`, reason `internal`; exit 2; a report exists | REP-13 | 2 |

### AT-O: Outcomes

| ID | Scenario | Expected observable | Outcome |
|---|---|---|---|
| AT-O1 | H sets `FAULT_OWNER_CHECK_OFF` and changes PA-D-01 to expect 200, so the check passes. (1) A reviewer who isn't a code owner approves. (2) A code owner approves. (3) The agent then pushes another commit | (1) `reviewDecision: REVIEW_REQUIRED`, `BLOCKED`. (2) `CLEAN`, which is the intended effect of approval. (3) The approval is dismissed; `REVIEW_REQUIRED`, `BLOCKED`. Also: approval by the pusher of the latest push doesn't count | O-1 |
| AT-O2 | (a) H changes only application code, setting `FAULT_OWNER_CHECK_OFF`. (b) The base-advance case of AT-W-08 | (a) The check on H fails; the report has PA-D-01 `FAIL`; exit 1; `BLOCKED`. (b) As AT-W-08 | O-2 |
| AT-O3 | A passing authoritative run on M | The report records: tested M with parents B and H, head H, base B, image digest equal to the build output, manifest hash equal to SHA-256 of the manifest blob at M, per-spec hashes, Proctor, runner and adapter versions equal to the pins, the workflow identity, and the run attempt | O-3 |
| AT-O4 | A local run | `mode: local`; "non-authoritative" in the terminal header and on the final line. "Fast" in O-4 has no target, so no timing is asserted | O-4 |

### AT-Q: Single-requirement tests

| ID | Test | Expected observable | Requirements | Phase |
|---|---|---|---|---|
| AT-Q-01 | The application returns `{"id":"${run.id}"}` literally; the next step sends `${project_id}` to `/probe/echo`, which reports what it received | Echo received the literal `${run.id}`; exit 0 | HTTP-02 | 2 |
| AT-Q-02 | `/probe/redirect-external` answers 302 to another host; the step expects 302 and that `Location` | Exit 0. Exit 1 would mean the redirect was followed | HTTP-01, SEC-07 | 2 |
| AT-Q-03 | `/probe/concurrency` records requests in flight across a run | `max_in_flight` = 1 | EXE-04 | 2 |
| AT-Q-04 | Unique sentinel values in the token variables, the DSN password, a `Set-Cookie` header, response fields `password` and `api_key`, `$.email` and `X-Api-Key`. Each check also fails another assertion, so evidence is written | No sentinel appears anywhere in the JSON report or the terminal output | REP-04, REP-05, REP-06, AUTH-02, SEC-01, SEC-09 | 6 |
| AT-Q-05 | A captured value of `nope' OR '1'='1` passed as `$1` in a lookup by ID; `expect: rows: 0` | Exit 0 (bound, not interpolated) | DB-01, SEC-04 | 4 |
| AT-Q-06 | Proctor's role has `INSERT` on `app.projects`. Separately: superuser; `CREATEROLE`; member of another role; owns an object; `USAGE` on a sequence | Each: exit 4 before the first scenario, whatever the transaction mode | DB-04, SEC-03 | 4 |
| AT-Q-07 | A declared view calls a volatile function that inserts a row | The step is `ERROR` with SQLSTATE 25006; exit 2; the row count is unchanged; the report labels the check "application-defined view" | DB-02, DB-07 | 4 |
| AT-Q-08 | The migrations create a schema named after Proctor's role, containing a `projects` table with different data | The query reads `app.projects`; an assertion on a known value passes | DB-03 | 4 |
| AT-Q-09 | A step expecting 200 calls `/probe/fail-once`, which fails once and then succeeds | `FAIL`; exit 1; one request sent | TIME-06 | 4 |
| AT-Q-10 | `FAULT_LOG_FRESH_TRACE`; an `eventually` log check on `trace_id` and `event` | `FAIL` at the final sample; exit 1 (matching by event alone would pass) | LOG-02 | 5 |
| AT-Q-11 | A query selects three columns and asserts one | The report contains only the asserted column | DB-06 | 4 |
| AT-Q-12 | Two records match the key; only one has every expected field | `PASS`. When the fields are split across the two records: `FAIL` | LOG-07 | 5 |
| AT-Q-13 | After a run with log checks | The log file's size, inode and SHA-256 are unchanged | LOG-03 | 5 |
| AT-Q-14 | The log file is rotated during a `consistently` window | The sample during rotation is a sample error; the result follows TIME-10 | LOG-04 | 5 |
| AT-Q-15 | A binding log source of `type: docker` | Exit 3 | LOG-01 | 5 |
| AT-Q-16 | A table row contains a condition | Exit 3 | TBL-03 | 2 |
| AT-Q-17 | PA-ALLOWED and PA-DENIED run; the application counts POST `/api/projects` | 2 POSTs, not one per case; every case resolves `${alice_project}` | TBL-04, TBL-05 | 2 |
| AT-Q-18 | A run with PA-ALLOWED and PA-DENIED | The JSON report has one result per case ID; the terminal shows each table with per-case results | TBL-01, TBL-02 | 2 |
| AT-Q-19 | Scenario B references a capture from scenario A | Exit 3 | EXE-01, EXE-02 | 2 |
| AT-Q-20 | Denied cases for `alice` with no allowed case for `alice` | `proctor validate` reports EXE-06 naming the identity and exits 3; nothing is reported for `anonymous` | EXE-06 | 2 |
| AT-Q-21 | The application returns `"owner_id": "u-7"`, and the binding holds `u-7` | Equal; `PASS`. With `"owner_id": 7` against `"7"`: `FAIL` (type-strict; see inconsistencies, item 8) | HTTP-03 | 2 |
| AT-Q-22 | The binding sets `user_agent: example-client/1.0`; `/probe/headers` records headers | The User-Agent received equals `example-client/1.0` | HTTP-04 | 2 |
| AT-Q-23 | Each identity type against `/probe/headers` and a test token endpoint | `none`: no `Authorization`. `bearer`: `Bearer <token>`. `basic`: the Basic credentials. `oauth2_client_credentials`: a token from the test endpoint | AUTH-01 | 2 |
| AT-Q-24 | Two consecutive runs | Different `${run.id}` values in the reports; both runs pass against the same environment | FIX-05 | 3 |
| AT-Q-25 | A spec contains a SQL setup step type | Exit 3 (no seeding in v1) | FIX-02, FIX-03 | 2 |
| AT-Q-26 | A spec asserts a Temporal activity | Exit 3 | TMP-06 | 5 |
| AT-Q-27 | A spec uses status `cancelled` | Exit 3; the valid name is `canceled` | TMP-04 | 5 |
| AT-Q-28 | Each lookup mode (derived ID, search attributes with `eventually`, captured ID) on a completed export | Each: `PASS`; the report shows the first run ID and the run IDs | TMP-01, TMP-03 | 5 |
| AT-Q-29 | A spec contains an `exec`, `script`, `command` or `plugin` key; a `lib/` directory with executable files sits under the protected root | The spec: exit 3 before any request. The `lib/` files: never read or run | SEC-06 | 0 |
| AT-Q-30 | A request to a host that isn't the target, a connection or a token endpoint | The step is `ERROR`; no request reaches that host | SEC-07 | 2 |
| AT-Q-31 | A request to an endpoint that accepts TCP and never answers | The step is `ERROR` within the configured or default timeout. The PRD fixes no default value | SEC-05 | 2 |
| AT-Q-32 | A response body of 10 KB | The report's excerpt is at most `report.excerpt_bytes` | REP-05 | 6 |
| AT-Q-33 | Snapshot of the JSON report schema | A golden-schema test fails when a field from REP-08 is renamed or removed | REP-09 | 2 |
| AT-Q-34 | The report's hashes after AT-O3 | Hashes for the manifest, each spec, `proctor.yaml`, the binding, `compose.verify.yaml` and each file it references equal SHA-256 of their blobs | INT-16 | 0 |
| AT-Q-35 | Where the plan allows, after a run | `gh attestation verify` on the report artifact succeeds, naming the tested commit | REP-02 | 6 |
| AT-Q-36 | Each time construct, passing and failing (`eventually` completes @+3 s; `consistently` holds; `never` sees nothing) | `PASS` for each; with the condition broken, `FAIL` at the first sample that shows it | TIME-01, TIME-02, TIME-03 | 4 |
| AT-Q-37 | Request paths `https://other.example/x`, `//other.example/x`, `/a/../b`, `/a%2Fb`, `a/b` and `/x?debug=1`, each in its own spec; then `/projects/p1` | Each of the first six: exit 3, and no request reaches any host. The last: the request reaches the target at `/projects/p1` | HTTP-05 | 0 |
| AT-Q-39 | A spec sets each of `Host`, `User-Agent`, `Authorization`, `Proxy-Authorization`, `Cookie`, `Content-Length`, `Transfer-Encoding`, `Connection` | Each: exit 3, and no request reaches any host | HTTP-06 | 0 |
| AT-Q-38 | A full local run with outbound connections recorded at the network boundary | Connections go only to the target, the configured connections and the token endpoints; an authoritative run adds only the GitHub API host | SEC-07 | 2 |
| AT-Q-40 | `json` expects `{x: 1}` for a body object `{"x": 1, "y": 2}`; `keys` lists `[x]` for the same object | Each: exit 1 | HTTP-07 | 0 |
| AT-Q-41 | `absent: [$.b]` where `b` is `null`; `present: [$.b]`; `absent_headers` naming a header the response sends | Exit 1, exit 0, exit 1 | HTTP-08 | 0 |
| AT-Q-42 | `length: { $.list: "${n}" }` with `n` saved as 2 from a database check, against a two-element list and a three-element list | Exit 0, then exit 1 | HTTP-09, DB-08 | 0 |
| AT-Q-43 | Response bodies `{"a": 1, "a": 2}`, `{"a": 2} {"a": 2}` and `{"a": 2} x`, with `$.a: 2` expected | Each: exit 1 | HTTP-10 | 0 |
| AT-Q-44 | A POST verification step with no later GET or database check; the same with a later GET expecting 401; the same with `no_state_check: <reason>` | Exit 3, exit 3, exit 0; the semantic diff lists the waiver | EXE-07, §7.5 | 0 |
| AT-Q-45 | A DELETE used only in setup | Exit 3, naming the operation | FIX-08 | 0 |
| AT-Q-46 | A setup step answered with 500; with the target down; with a response slower than the request timeout | Each: scenario ERROR, reason `setup_failed`, cause `unexpected_response`, `unreachable`, `timeout` | FIX-04 | 0 |
| AT-Q-47 | Two scenarios create `item ${run.id}` in two runs | Four different names, each `<12 hex digits>-<scenario ID>` | FIX-05 | 0 |
| AT-Q-48 | A `credential: expired` request expecting 200; a variant whose token equals the identity's; an `accepted_variants` request expecting 200 with a `header_env` value | Exit 3; exit 3 with AUTH-03 and no request sent; exit 0 with the header value sent as given | AUTH-06, AUTH-03 | 0 |
| AT-Q-49 | `${secret.pw}` in a request body, the application echoing it and the bearer token in its response; `${secret.pw}` in a path or an expectation | The secret is sent, the report shows `[redacted]` and neither value; exit 3 | AUTH-07, AUTH-02 | 0 |
| AT-Q-50 | Database checks: a syntax error, `UPDATE ... RETURNING`, `nextval`, a `'; DROP TABLE ...` argument, `pg_sleep` beyond the query timeout, two columns with one name, a `bytea` column, 10,001 rows | ERROR (exit 2) for each but the argument, which is data (exit as the expectation says); the table unchanged after each | DB-01, DB-02, DB-05, DB-08 | 4 |
| AT-Q-51 | A query returning a column `first_row` doesn't name; `"1"` expected for an integer column | Exit 1 each | DB-08 | 4 |
| AT-Q-52 | The verification role holds, in turn: column `INSERT`, `UPDATE`, `DELETE`, `TRUNCATE`, `TRIGGER`, `SELECT` on an undeclared table, sequence `USAGE`, `CREATE` on a schema, membership of `pg_read_all_data`, `CREATEROLE`, `BYPASSRLS`, ownership of a table; then superuser; then a declared object is missing | Each: exit 4 before any scenario, every scenario NOT_RUN with `trust_violation` | DB-04, DB-04a, DB-07 | 4 |
| AT-Q-53 | No schemas declared and a query naming an unqualified table | ERROR, cause `query_error` | DB-03 | 4 |
| AT-Q-54 | Injected faults: connection closed, reset mid-body, short body, broken chunking, non-HTTP reply, invalid gzip, a stalled body, 11 MiB, HTML, a redirect, 503 | None gives PASS; transport faults are exit 2, the rest exit 1 | §15, SEC-05, HTTP-01 | 0 |
| AT-Q-55 | Each specification weakening in `mutation-testing/harness.py` (SM-01 to SM-12) | Validation rejects it (exit 3) or the semantic diff lists it as a weakening | §7.5, EXE-03, EXE-07 | 4 |
| AT-Q-56 | A value sent and matched later without `${run.id}`; a `length` on a GET with no query parameters; each with `waive: { <rule>: <reason> }`; a waiver with an empty reason, for EXE-07, or on a step the rule doesn't apply to; a `no_state_check` on a step a later GET checks | Exit 3 naming FIX-05 or HTTP-09, and `run` sends no request; exit 0 with the waivers, which the semantic diff lists as `rule_waived` weakenings; exit 3 for each bad waiver; no output line says WARNING | EXE-08, FIX-05, HTTP-09, EXE-07 | 0 |

### AT-REQ: Coverage

A test is listed against a requirement only if it would fail when the requirement is missing.

| Requirement | Tests | Phase |
|---|---|---|
| MODE-01 | AT-I-34, AT-I-35, AT-W-15, AT-W-16 | 0 |
| MODE-02 | AT-I-39 | 0 |
| MODE-03 | AT-W-11, AT-W-13 | 0 |
| MODE-04 | AT-W-14 | 0 |
| MODE-05 | AT-I-44. The JSON-output part isn't testable as written (inconsistencies, item 3) | 0 |
| INT-01 | AT-I-01, AT-I-02, AT-I-43 | 0 |
| INT-02 | AT-I-06, AT-I-07 | 0 |
| INT-03 | AT-I-16, AT-I-17, AT-R-02, AT-R-04, AT-R-05, AT-T-52 | 0 |
| INT-04 | AT-I-01, AT-I-18, AT-T-35 | 0 |
| INT-05 | AT-I-03, AT-I-19 | 0 |
| INT-06 | Removed; no test | — |
| INT-07 | AT-I-39 | 0 |
| INT-08 | AT-I-04, AT-I-05, AT-I-38 | 0 |
| INT-09 | AT-I-38, AT-W-12 | 0 |
| INT-10 | AT-I-03, AT-I-08, AT-I-37, AT-R-18 | 0 |
| INT-11 | AT-I-20, AT-I-21, AT-I-22 | 0 |
| INT-12 | AT-I-33 | 0 |
| INT-13 | AT-I-23 to AT-I-28 | 0 |
| INT-14 | AT-I-29, AT-I-30 | 0 |
| INT-15 | AT-I-31, AT-I-32 | 0 |
| INT-16 | AT-I-28, AT-Q-34 | 0 |
| EVD-01 | AT-I-40, AT-I-41, AT-I-42 | 5 |
| EVD-02 | AT-I-15 | 6 |
| EXE-01 | AT-Q-19 | 2 |
| EXE-02 | AT-Q-19 | 2 |
| EXE-03 | AT-R-19 | 2 |
| EXE-04 | AT-Q-03 | 2 |
| EXE-05 | **Not testable mechanically:** whether a scenario depends on state from another is a reviewer's judgment. Partial signal: run each scenario alone and compare with the full run | — |
| EXE-06 | AT-Q-20 | 2 |
| EXE-07 | AT-Q-44, AT-Q-56 | 0 |
| EXE-08 | AT-Q-56 | 0 |
| HTTP-01 | AT-Q-02 | 2 |
| HTTP-02 | AT-Q-01 | 2 |
| HTTP-03 | AT-Q-21 | 2 |
| HTTP-04 | AT-Q-22. Behavior without a binding value is unspecified (inconsistencies, item 17) | 2 |
| HTTP-05 | AT-Q-37 | 0 |
| HTTP-06 | AT-Q-39 | 0 |
| HTTP-07 | AT-Q-40 | 0 |
| HTTP-08 | AT-Q-41 | 0 |
| HTTP-09 | AT-Q-42, AT-Q-56 | 0 |
| HTTP-10 | AT-Q-43 | 0 |
| TBL-01 | AT-Q-18, AT-I-07 | 2 |
| TBL-02 | AT-Q-18 | 2 |
| TBL-03 | AT-Q-16 | 2 |
| TBL-04 | AT-Q-17, AT-I-13 | 2 |
| TBL-05 | AT-Q-17 | 2 |
| TBL-06 | AT-R-02, AT-R-03 | 2 |
| AUTH-01 | AT-Q-23 | 2 |
| AUTH-02 | AT-Q-04 | 6 |
| AUTH-03 | AT-R-13 to AT-R-16 | 2 |
| AUTH-04 | AT-R-17 | 2 |
| AUTH-05 | AT-R-17 (variant: `user_id_type: integer` with a non-numeric value gives exit 3; with a numeric value, `$.owner_id` integer comparison passes) | 2 |
| AUTH-06 | AT-Q-48 | 0 |
| AUTH-07 | AT-Q-49 | 0 |
| FIX-01 | AT-W-19 (forks); otherwise a reference-setup review item | 3 |
| FIX-02 | AT-Q-25 | 2 |
| FIX-03 | AT-Q-25, AT-Q-06 | 4 |
| FIX-04 | AT-R-01, AT-T-20, AT-Q-46 | 2 |
| FIX-05 | AT-Q-24, AT-Q-47, AT-Q-56 | 3 |
| FIX-06 | **Configuration review item:** a black-box test can't tell a disposable environment from a reused one. Partial signal: AT-Q-24's second run starts with an empty `app.projects` | 3 |
| FIX-07 | AT-T-34, AT-T-51, AT-T-52, AT-T-53, AT-R-11 | 3 |
| LOG-01 | AT-Q-15 | 5 |
| LOG-02 | AT-Q-10, AT-T-42, AT-T-43 | 5 |
| LOG-03 | AT-Q-13 | 5 |
| LOG-04 | AT-Q-14, AT-T-34 | 5 |
| LOG-05 | AT-I-40 (the Level 3 label in the report) | 5 |
| LOG-06 | AT-T-19, AT-T-39, AT-T-40 | 5 |
| LOG-07 | AT-Q-12 | 5 |
| LOG-08 | AT-T-37 | 5 |
| DB-01 | AT-I-36, AT-Q-05 | 4 |
| DB-02 | AT-T-14, AT-Q-07 | 4 |
| DB-03 | AT-Q-08 | 4 |
| DB-04 | AT-Q-06, AT-Q-52 | 4 |
| DB-04a | AT-Q-52 | 4 |
| DB-05 | AT-T-29, AT-T-30 | 4 |
| DB-06 | AT-Q-11 | 4 |
| DB-07 | AT-Q-07 (view label). "May read only declared objects" isn't testable as written (inconsistencies, item 9) | 4 |
| DB-08 | AT-Q-42, AT-Q-50, AT-Q-51 | 4 |
| TMP-01 | AT-Q-28 | 5 |
| TMP-02 | AT-T-46, AT-T-47 | 5 |
| TMP-03 | AT-T-46, AT-Q-28 | 5 |
| TMP-04 | AT-T-54, AT-T-55, AT-T-56, AT-Q-27 | 5 |
| TMP-05 | AT-I-41 | 5 |
| TMP-06 | AT-Q-26 | 5 |
| TMP-07 | AT-T-44, AT-T-45, AT-T-48, AT-T-49 | 5 |
| TMP-08 | AT-T-38 | 5 |
| TIME-01 | AT-Q-36, AT-T-28 | 4 |
| TIME-02 | AT-Q-36, AT-T-61 | 4 |
| TIME-03 | AT-Q-36, AT-T-40 | 5 |
| TIME-04 | AT-T-15, AT-T-18 (field names provisional) | 4 |
| TIME-05 | AT-T-27, AT-T-59, AT-T-60 (the notice) | 4 |
| TIME-06 | AT-Q-09, AT-T-13 | 4 |
| TIME-07 | AT-T-01 to AT-T-10 | 4 |
| TIME-08 | AT-T-11, AT-T-12, AT-T-16, AT-T-17, AT-T-25, AT-T-26 | 4 |
| TIME-09 | AT-T-18 | 4 |
| TIME-10 | AT-T-23, AT-T-28 to AT-T-33 | 4 |
| TIME-11 | AT-T-21, AT-T-22 | 4 |
| TIME-12 | AT-T-50 | 4 |
| REP-01 | AT-W-17, AT-W-18 | 0 |
| REP-02 | AT-Q-35 | 6 |
| REP-03 | AT-W-08, AT-W-17 | 0 |
| REP-04 | AT-Q-04 | 6 |
| REP-05 | AT-Q-04, AT-Q-32 | 6 |
| REP-06 | AT-Q-04 | 6 |
| REP-07 | AT-R-06, AT-R-07, AT-R-09 | 2 |
| REP-08 | AT-R-06, AT-R-18, AT-R-20 | 2 |
| REP-09 | AT-Q-33 | 2 |
| REP-10 | AT-W-04 | 0 |
| REP-11 | AT-I-44. How the running image's digest is obtained is unspecified (inconsistencies, item 4) | 0 |
| REP-12 | AT-R-12 | 2 |
| REP-13 | AT-R-10, AT-R-21, AT-W-18 | 2 |
| SEC-01 | AT-Q-04 | 6 |
| SEC-02 | AT-R-13 | 2 |
| SEC-03 | AT-Q-06, AT-Q-07 | 4 |
| SEC-04 | AT-Q-05 | 4 |
| SEC-05 | AT-Q-31 (no default value fixed) | 2 |
| SEC-06 | AT-Q-29 | 0 |
| SEC-07 | AT-Q-02, AT-Q-30, AT-Q-38 | 2 |
| SEC-08 | AT-T-29, AT-R-11 | 4 |
| SEC-09 | AT-Q-04 | 6 |
| SEC-10 | **Configuration review item:** no black-box observable shows the absence of production credentials | 0 |
| SEC-11 | AT-W-02, AT-W-03 | 0 |
| SEC-12 | AT-W-01 | 0 |
| SEC-13 | **Review item:** each release's dependency list has a checked license, and its notices ship with the release; no black-box test shows code wasn't copied | 0 |
| ARC-01 | **Not black-box testable:** code-structure requirement; check in code review | — |
| ARC-02 | **Not black-box testable:** code-structure requirement; check in code review | — |

Requirements with no black-box test: EXE-05, FIX-06, SEC-10, SEC-13, ARC-01, ARC-02 (and INT-06, removed). Partly untestable as written: MODE-05, HTTP-04, DB-07, REP-11, SEC-05.

### PRD inconsistencies found while writing these tests

The author resolved all 18 in revision 2 after these tests were drafted. Tests that marked a value or rule as unspecified because of one of these now follow the PRD text named here.

| # | Gap | Resolved by |
|---|---|---|
| 1 | `--binding` could point into the checkout | MODE-01: a binding inside the checkout is exit 3 |
| 2 | `trust.workflow_changed` under OD-01 A | MODE-04: `not_applicable` under A; `job_workflow_sha` recorded |
| 3 | JSON output vs `report.formats` | MODE-05: authoritative mode always writes JSON |
| 4 | Source of the running image digest | REP-11: the workflow passes both digests in the binding |
| 5 | API permissions for INT-11 and REP-10 | §16.2 item 5: `contents: read`, `actions: read` |
| 6 | TMP-04 early FAIL vs TIME-01/TIME-10 | TMP-04: no early FAIL; samples until the timeout |
| 7 | Late samples and skipped due times | TIME-08: passed due times are skipped, one sample starts at once |
| 8 | String identity attributes vs type-strict comparison | AUTH-05: binding may declare `user_id_type`; converted at load |
| 9 | DB-07 enforcement | DB-04 and DB-07: enforced through grants, not SQL parsing |
| 10 | Unknown keys | INT-15: undefined keys fail validation (exit 3) |
| 11 | `../` and absolute manifest paths | INT-01: forbidden; integrity violation |
| 12 | Readiness for non-Temporal workers | FIX-07: `readiness.checks` paths |
| 13 | `never` correlation key source | LOG-08: must come from the triggering step (exit 3) |
| 14 | `setup_from` vs file order | TBL-04: must reference an earlier table (exit 3) |
| 15 | NOT_RUN reason after failed setup | FIX-04: `setup_failed` |
| 16 | REP-08 field names | REP-09: provenance names fixed in Phase 0, result names in Phase 2; provisional names here until then |
| 17 | Missing `user_agent` | HTTP-04: exit 3 |
| 18 | `after:` naming a missing or later step | TIME-09: exit 3 |

---

# Appendix D. Revision history

The original PRD and the full change log, with the reason for each change, are in commit `b96dafe`: `git show b96dafe:prd.md` and `git show b96dafe:prd-review-changes.md`.

| Revision | Date | What changed |
|---|---|---|
| 0 | before 2026-11-14 | The initial PRD |
| 1 | 2026-11-14 | Applied an adversarial review and the product owner's handout: the source-control platform and trusted CI enforce approval, and Proctor detects changes and produces evidence; a threat model; a protected manifest; integrity checks first; evidence levels; time checks with consistently/never semantics |
| 2 | 2026-11-14 | Applied the QA engineer's review (52 findings), and corrected two of revision 1's claims: option B of OD-01 needs a paid plan for private repositories, and Temporal's Paused status is pre-release and off by default |
| Phase 1 | 2026-11-14 | Proctor has its own runner, with no copied code (SEC-13, Appendix B) |
| Phase 0 and 2 builds | 2026-11-14 | Rules found while building: HTTP-05, HTTP-06, SEC-07, and others marked in the text |
| Mutation testing | 2026-11-14 | Closed the gaps mutation testing found: exact matching (HTTP-07, HTTP-08), list lengths (HTTP-09), ambiguous JSON (HTTP-10), state checks after writes (EXE-07), setup exercised as steps (FIX-08), setup-failure causes (FIX-04), `${run.id}` per scenario (FIX-05), credential variants (AUTH-06), secrets (AUTH-07), read-only PostgreSQL checks built early (DB-01 to DB-08), weakenings in the semantic diff (§7.5) |
| Warnings removed | 2026-11-14 | Every warning is a failure (OD-20, EXE-08) |
| 3 | 2026-11-14 | The PRD, decisions, runner decision record, acceptance tests and change log merged into this one file |

**Removed from the original, and not to be added back without a new decision:** glob-based discovery as the authoritative mechanism; `proctor approve`; the SKIPPED result; SQL examples with string interpolation; a CI example running an unpinned `proctor` from the pull request's workflow; the claim that Temporal "status: completed" and log events verify effects.

**Proposed and not adopted:**

| Proposal | Why | Decision |
|---|---|---|
| Fail when an earlier attempt failed | One flaky failure would block a commit until a new push | OD-15 |
| A presence twin for every absence check | The liveness record already turns the blind-channel case into ERROR; twins add authoring cost | OD-17 |
| Gate on Proctor's own CODEOWNERS matching | Proctor's matcher may differ from GitHub's; it reports for now | OD-16 |
