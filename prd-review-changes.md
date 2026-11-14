# Proctor PRD — Review Changes

What `prd-revised.md` changes from `prd.md`, and why. `prd.md` is unchanged. The first part covers revision 1; "Revision 2: QA review" at the end covers the changes since, and where they supersede revision 1 (for example, optional specifications, `fixtures/` and exit-code precedence), revision 2 wins.

Two sources drove the changes: the adversarial review of `prd.md`, and the review handout that set the direction (forge and trusted CI enforce approval; evaluate Venom first; integrity first). Section numbers refer to `prd-revised.md` unless marked "original".

Items marked **Added by the revision** go beyond the handout. They are proposals for you to accept or reject, not agreed requirements.

## 1. Trust model

| Change | Where | Why |
|---|---|---|
| Added a threat model, trust boundary and threat-model limits | §4 | The original protected only spec files. An agent can also change discovery globs, target URL, connections, Compose files, the CI workflow, the Proctor binary and the application's behavior under test. Any of these can produce a false pass. |
| Split inputs into trusted (supplied by CI), protected (in repo, code-owner approval) and subject under test | §4.3, §7.1 | Each group needs a different protection. The original treated the repository as one trust zone. |
| Added local and authoritative modes | §5 | A local run is the agent's own feedback loop and can't be evidence. The original never said where the authoritative run happens. |
| Authoritative mode is a property of the environment, not a flag | §5.2 | **Added by the revision.** A `--authoritative` flag on a laptop would let anyone produce an "authoritative" report. |
| Flagged that a `pull_request` workflow runs the PR's own workflow file | §4.2, §6.3 | **Added by the revision.** The handout says "protected CI workflow" but doesn't name this mechanism. It is how a PR can make a required check pass trivially. |
| Flagged that database views are application code | §4.2, §7.1, §10.2 | **Added by the revision.** The handout recommends views to reduce coupling. They do reduce coupling, but migrations define them, so they add no trust. |

## 2. Approval and enforcement

| Change | Where | Why |
|---|---|---|
| Source-control platform and trusted CI enforce; Proctor detects, validates, executes, reports | §6.1 | Handout decision. "Must not be callable by an ordinary AI execution flow" (original §12) can't be enforced by a CLI on the agent's machine. |
| Any change to a protected input needs approval; the semantic diff only explains | §6.2, §7.5 | Many weakenings aren't assertion edits: changed identity, broadened matcher, longer timeout, skipped scenario, file moved out of a glob. Gating on any change avoids depending on classifying them. |
| Separated "integrity violation" (always fails) from "protected change detected" (approval via platform) | §6.2 | **Added by the revision.** The handout listed both "integrity failure" and "protected change detected" outcomes without defining the difference. Without it, a legitimate approved spec change could never pass. |
| Removed `proctor approve` from v1 | §6.4, §16 | **Added by the revision.** Approval is code-owner review of the PR. A command an agent can run can't carry human authority. A separate approval record is OD-03. |
| Reference GitHub configuration | §6.3 | Handout §3.2. Settings checked against GitHub documentation; see the sources in §6.3. |

## 3. Specification integrity

| Change | Where | Why |
|---|---|---|
| Manifest replaces glob discovery in authoritative mode | §7.2 | A glob can silently stop matching a required file. |
| Stable scenario and table-case IDs separate from names | INT-02, TBL-01 | Lets removal and renaming be detected. |
| Required scenarios must execute; no conditional skips; empty set never passes | INT-03, INT-04 | The original SKIPPED status (original §18) had no rule preventing a required scenario from being skipped. |
| Integrity record with hashes and versions | §7.4 | Handout §4.3. |
| Hash raw committed bytes, no YAML canonicalization in v1 | INT-10 | **Added by the revision.** The handout allowed "canonicalized or otherwise deterministic" hashing. Canonicalization can hide differences such as duplicate keys that parsers resolve differently, and raw bytes match what Git shows reviewers. |

## 4. Implementation coupling and evidence

| Change | Where | Why |
|---|---|---|
| Three evidence levels: public contract, business state, operational | §10 | The tagline says "verify behavior, not implementation", but the original examples asserted on internal tables, log event names and workflow IDs, and §16.3 (original) forbade trusting application claims while §7.4 (original) relied on logs. |
| Level 2 and 3 checks are labelled implementation-coupled | §10.2, DB-06 | Makes the coupling visible. Schema refactors may legitimately need spec changes. |
| A business outcome needs a Level 1 or 2 check; Level 3 only corroborates | EVD-01 | **Added by the revision** as a rule. The handout described the limitation but not a validation rule. |
| `requirement_is_operational: true` for audit-log-type requirements | EVD-02 | **Added by the revision.** It makes the EVD-01 exception explicit and protected. |

## 5. Assertions and time

| Change | Where | Why |
|---|---|---|
| Added `consistently` and `never`, with the sampling limitation | §11.9 | `eventually` alone can't test absence: "Bob's project is unchanged" holds at t=0 even if a background job changes it later. |
| Observation starts after the triggering step | §11.9 | **Added by the revision.** The handout's "before or immediately after" needs concurrent steps; deferred to OD-08. |
| Steps with a request must assert beyond status | EXE-03 | **Added by the revision.** Original §10.2 called status-only checks bad but didn't forbid them. The exception is OD-07. |
| Tables for authorization matrices | §11.3 | Handout §13; from Vaccari's "Tests as data" article. Missing combinations become visible. |

## 6. Identities, fixtures, adapters

| Change | Where | Why |
|---|---|---|
| Identity profiles: types in protected config, credentials in the environment binding | §11.4 | The original's `auth: alice` assumed users and tokens existed. |
| v1 identity types: none, bearer, basic, OAuth2 client credentials | AUTH-01 | **Added by the revision** as the v1 cut. The handout listed OIDC and mTLS as possibilities; they are deferred to OD-05. |
| Setup through the application's public API; identities provisioned outside Proctor | FIX-01, FIX-02 | **Added by the revision** as the default. It needs no new write path. Direct seeding is OD-06. |
| Disposable environments for authoritative runs | FIX-06 | **Added by the revision.** Cleanup in shared environments is hard to make reliable, and disposable environments also remove the need for production secrets (SEC-10). |
| Parameterized SQL in every example; validation rejects `${...}` in queries | DB-01 | The original examples used `'${project_id}'` interpolation, contradicting its own §7.2 and §17. |
| Read-only enforced by a SELECT-only role created by the trusted environment, plus `READ ONLY` transactions | DB-02 | A read-only transaction alone can be changed by the session, and the application's migrations (agent-controlled) shouldn't create Proctor's role. |
| Temporal: namespace, Workflow ID and Run ID; Continue-As-New chains; lookup without public workflow IDs | §11.8 | The original required APIs to return workflow IDs and ignored run IDs and Continue-As-New. |
| Logs: correlation by W3C trace context, then business ID, then time window marked weak | §11.6 | Timestamp matching picks up old or unrelated events. `traceparent` is standard, so it's no Proctor-specific test signal. |

## 7. Reports and CLI

| Change | Where | Why |
|---|---|---|
| Provenance in every authoritative report | §13.1 | A PASS that doesn't name the commit, specs and runner proves nothing. |
| Redaction rules and defaults | §13.2 | Original §17.13 required it but gave no mechanism. |
| Results PASS, FAIL, ERROR, NOT_RUN; SKIPPED removed | §13.3 | NOT_RUN for a required scenario is an integrity violation, not a neutral status. |
| Exit codes 0–4 with precedence | §13.3 | Handout §12.3. Precedence (REP-07) was **added by the revision**. |
| New `proctor integrity`; `validate` also rejects SQL interpolation and assertion-free steps | §16 | Integrity can be checked without running the app. |
| No copyable CI workflow yet | §16 | The original example didn't install or pin Proctor and ran the PR's own workflow. The correct workflow depends on OD-01. |

## 8. Roadmap

| Change | Why |
|---|---|
| Integrity moved from Phase 7 to Phase 0 | It is the distinguishing feature; Phases 1–6 of the original rebuilt a generic runner first. |
| Phase 0 uses Venom as the candidate runner | **Added by the revision.** The handout put integrity in Phase 0 and runner evaluation in Phase 1, but integrity checks such as "a required scenario didn't execute" need a runner to govern. |
| Phase 1 is an explicit build-or-reuse decision | Handout §14.4. |
| Mutation testing moved to a follow-up after Phase 4 | It stays useful, but isn't needed to prove the trust model. |

## 9. Kept from the original

The product vision and central principle, the problem example, language independence, the adapter architecture and future adapter list, non-goals, read-only database stance, the distinction between infrastructure errors and behavioral failures, bounded polling, deterministic sequential execution, testing Proctor itself with multi-language example apps, and the "vertical slice first" guidance (now built on the trust proof of concept).

## 10. Removed

- Glob-based discovery as the authoritative mechanism (kept only for local convenience if needed).
- `proctor approve` in v1.
- The SKIPPED result.
- SQL examples with string interpolation.
- The CI example that ran an unpinned `proctor` from the PR's workflow.
- The claim that Temporal "status: completed" and log events verify effects (original Principle 3 now distinguishes evidence levels).

---

# Revision 2: QA review

Revision 2 applies the QA engineer's review of revision 1 (52 findings: P = platform and integrity, T = time checks, R = results, C = contradictions, V = testability, K = clocks, X = Go-track points). The reviewed revision 1 files are kept outside the repository for reference. The Go developer's review had not arrived when revision 2 was written.

Decisions marked **Revision 2 choice** were the author's triage, approved by the product owner as the basis for a first version but not individually confirmed.

## Corrections to revision 1's own claims

| Claim in revision 1 | Correction | Finding |
|---|---|---|
| Option B works "on every plan" | Private repositories need a paid plan (Pro, Team or Enterprise) for code owners and protected branches | §9 of the review |
| INT-03's fix covered ordinary failures | Table setup failures, configuration errors and an unreachable target still produced exit 4 | R-01 |
| PA-MATRIX satisfies the rules | EXE-03 rejected it and every setup step; OD-07's "tables already work this way" was false | C-01 |
| The trusted job is safe | It checked Proctor's checksum, then built the pull request's code on the same runner | P-01 |
| The success criterion | Claimed "no protected input changed without approval", which Proctor can't see under OD-02 A | C-08 |
| Temporal statuses | Paused is pre-release and off by default; API spellings differ from the docs | K-01, C-11 |

## Changes

| Area | Change | Findings |
|---|---|---|
| Threat model | Added: skipped jobs report success; stale merges; silent CODEOWNERS errors; Git hiding changes; table-owner grants. Limits now include re-runs, option B's check-name gap, sampling, and changes reverted during the request | P-02, P-03, P-04, P-05, P-07, P-08, X-02 |
| Trust boundary | The application is built in a separate job; the verification job never runs pull-request code (SEC-12, §16.2) | P-01, X-04 |
| Authoritative mode | Rewritten: Proctor records how it was invoked plus verifiable workflow identity (OIDC claims) and whether the workflow file changed; the merge rule reads the status, not the report (MODE-01 to MODE-05) | X-01 |
| GitHub configuration | Added: up-to-date branches or merge queue; expected-source pinning kept (it stops token-posted statuses); all of `/.github/` owned; a single CODEOWNERS file; plan requirements; fork pull requests | P-04, P-05, P-08, P-15 |
| Reference workflow | Requirements listed: isolated build, `fetch-depth: 2`, checksums right before use, an aggregator job with `if: always()` as the required check, `merge_group`, no automatic re-runs | P-01, P-03, P-04, R-03, X-03 |
| Change detection | Read protected inputs from the Git object database; compare trees by blob ID with no rename detection; any Git error is an integrity violation; base is the test merge's first parent (INT-07 to INT-09) | P-07, X-03 |
| New integrity checks | CODEOWNERS errors via GitHub's API (INT-11); undeclared adapters, connections and identities (INT-12); regular files only, no symlinks, submodules or LFS pointers, references resolved (INT-13); no diff-hiding or byte-changing Git attributes (INT-14); strict YAML (INT-15). **Revision 2 choice:** these were judged cheap enough for v1 | P-05, P-06, P-11, P-12, C-06 |
| Protected set | Protected root; dependency definitions with their referenced files; `.gitattributes`, `.gitmodules`, `.lfsconfig` | P-06, P-11 |
| Optional specifications | **Revision 2 choice:** removed. Every manifest entry is required, and INT-06 is retired. The undefined `required: false` was a weakening path | P-10 |
| Fixtures | **Revision 2 choice:** `fixtures/` removed from v1, because it was undefined and unhashed | P-14 |
| INT-03, INT-04 | Not-started reasons carry their own exit codes; only an unexplained missing result is exit 4. INT-04 split into pre-execution (integrity) and post-execution (ERROR). "Required scenario ID not found" defined as parsed but not reported by the runner | R-01, C-02 |
| Exit codes | Precedence 4, 3, 1, 2; `summary` block; NOT_RUN reasons; abort and panic handling; always continue in authoritative mode (REP-07, REP-08, REP-12, REP-13) | R-03, R-04 |
| Setup | A failed setup step is ERROR `setup_failed` (FIX-04) | R-02 |
| Readiness | Readiness gate before the first scenario (FIX-07) | K-03, T-05 |
| Re-runs | Earlier failed attempts reported (REP-10). **Revision 2 choice:** not blocking in v1 (OD-15), against the review's proposal, because one flaky failure would block a commit permanently | P-02 |
| Image digest | The running application must match the build job's image digest (REP-11) | P-01 |
| EXE-03 | Applies to verification steps; 2xx needs a body or header assertion; 4xx and 5xx may assert status alone; setup needs only a status. OD-07 resolved | C-01 |
| Positive controls and self-contained scenarios | EXE-05, EXE-06 | P-09 |
| Identities | AUTH-03 covers attributes, empty values, duplicates and mismatches; optional identity-check endpoint (AUTH-04) | P-09 |
| Interpolation | Once only; a single reference keeps its type; strict comparisons (HTTP-02, HTTP-03) | C-04 |
| User-Agent | From the binding; no runtime default (HTTP-04); `traceparent` only where used | P-13 |
| Tables | Split into allowed and denied tables; setup runs once; `setup_from`; cases independent; setup failure handling (TBL-04 to TBL-06) | C-01, C-05 |
| Logs | File-position correlation only for `never`; matches count wherever they are; `expect.fields` semantics; liveness record required for `never`; trace field configurable; JSON Lines only (LOG-01 to LOG-08). **Revision 2 choice:** no presence twins (OD-17), against the review's proposal | T-01, T-02, K-02, C-11 |
| PostgreSQL | Transaction per sample at READ COMMITTED; `search_path` set; privilege self-check (exit 4); declared tables and views only, views labelled (DB-02 to DB-07) | T-04, X-02 |
| Temporal | Chains instead of start times; Describe before the trigger; `max_clock_skew`; no `never` through Visibility; status name mapping; Paused caveat (TMP-01 to TMP-08) | C-03, K-01, C-11 |
| Time checks | Unit-string values with protected limits; sample schedule; anchor via `after:`; errored-sample rules; GET-only polling; backend retries rejected; monotonic clock (TIME-06 to TIME-12) | T-01 to T-09, K-04 |
| PA-002 | Asserts a version field and checks PostgreSQL as well as HTTP | T-05 |
| Provenance | Tested commit, parents, head and base; image digests; binding and dependency hashes; workflow identity; attempts | P-02, P-04, V-01 |
| Redaction | Rules add to defaults; the "named like" rule spelled out; excerpt bound in policy | C-10, V-04 |
| Semantic diff | Setup, order, time-parameter direction, EVD-02 flag, policy and dependency changes | P-09, T-06 |
| EVD-01 | Now a validation rule: no Level 1 or 2 step without `requirement_is_operational` | C-07 |
| Architecture | The core owns scheduling, tables, setup, interpolation and trace context; a runner backend is at most a single-operation executor | X-05, T-09 |
| Acceptance tests | **Revision 2 choice:** moved to `acceptance-tests.md`, with an observable per requirement and a phase per test | V-01 to V-04 |
| Roadmap | Phase 0 uses a throwaway schema subset; each phase ends when its tests pass; OD-11 moved to Phase 0 | C-09, V-02 |
| Success criterion | Reworded to what Proctor can show | C-08 |

## Not adopted

| Proposal | Why | Tracked as |
|---|---|---|
| Fail when an earlier attempt failed (REP-10 as proposed) | One flaky failure would block a commit until a new push | OD-15 |
| Presence twin for every absence check | The liveness record already turns the blind-channel case into ERROR; twins add authoring cost | OD-17 |
| Gate on Proctor's own CODEOWNERS matching | Proctor's matcher may differ from GitHub's; it reports for now | OD-16 |

## Changes from implementing Phase 0, increment 1

| Change | Where | Why |
|---|---|---|
| A moved-and-edited file is a removal plus an addition; its scenarios are reported as moved and modified | INT-08, AT-I-05 | Without rename detection there is no way to pair a changed file with its old path; scenario IDs carry the move instead |
| `protected_paths` named as the `proctor.yaml` key for extra protected files and directories | §7.1, INT-13, §14 | The PRD said "listed in `proctor.yaml`" without a key |
| `proctor diff --head <test-merge> --pr-head <id>`; `--repo` on every command; invalid invocations exit 3 | §16.1 | INT-09 needs a way to pass the test merge and the head; the exit code for usage errors was undefined |

## Phase 1: runner decision

The product owner decided on 2026-11-14 that Proctor has its own runner, takes only ideas from other open-source tools, and copies no code from them.

| Change | Where | Why |
|---|---|---|
| Proctor executes specifications with its own runner; no other runner as library, subprocess or backend | §19, ARC-02, §20 Phase 1 | Product owner decision |
| No code copied, translated or closely paraphrased from other test runners; dependencies only with checked licenses | SEC-13 (new), §22 | Product owner decision, for copyright reasons |
| Venom proof of concept and native-runner comparison removed from Phase 0; Phase 0 runs its scenarios on the native runner | §20 Phase 0 | The decision answers the question they existed for |
| OD-09 resolved: people approve Proctor's own schema | `open-decisions.md` | Follows from the decision |
| "Runner backend" removed from the trust boundary, the pinned inputs and the provenance fields | §4.3, §7.1, §13.1 | There is no separate backend to pin or record |
| TIME-06 and SEC-06 no longer depend on what a backend supports: the schema has no retry, delay, repeat, shell or code-loading construct, and unknown keys are rejected | TIME-06, SEC-06, §12 | The conditional wording assumed a third-party backend |
| HTTP-05 (new): a request path can't leave the binding's base URL | §11.2, AT-Q-37 | Gap: nothing stopped a specification from sending `path: https://other.example/` or `//other.example/`. With a native runner Proctor resolves URLs itself, so the rule has to be stated |
| SEC-07 lists the GitHub API and says Proctor sends no analytics | SEC-07, AT-Q-38 | Gap: SEC-07 contradicted INT-11 and MODE-03, which call GitHub; analytics was one of Step CI's rejection reasons |
| Form bodies are URL-encoded and no longer conditional on a backend | §11.2 | The condition referred to a backend that no longer exists |
| `runner-evaluation.md` turned into the decision record, with the ideas taken, the failure modes avoided and the no-copied-code rule | `runner-evaluation.md` | Phase 1 acceptance |

## Changes from implementing Phase 0, increment 2 (`proctor run`)

| Change | Where | Why |
|---|---|---|
| A saved value inserted into a path is escaped as one segment; a resulting `.` or `..` segment is ERROR | HTTP-05 | Gap: HTTP-05 checked the path as written, but a captured value such as `../admin` could still change it |
| HTTP-06 (new): specifications can't set `Host`, `User-Agent`, `Authorization`, `Proxy-Authorization`, `Cookie`, `Content-Length`, `Transfer-Encoding` or `Connection` | §11.2, AT-Q-39 | Gap: a spec could override the binding's User-Agent (HTTP-04), the target host (HTTP-05) or an identity's credentials (AUTH-02) through a header |
| `proctor run` takes `--binding` and `--request-timeout` | §16.1 | The flags the local run needs |

Phase 0 behavior that doesn't change a requirement, recorded so it isn't mistaken for the v1 design:

- Redirects are never followed, which is stricter than HTTP-01 allows; the response is judged as received.
- The HTTP client ignores proxy settings from the environment (SEC-07).
- Each request has a 10-second limit by default and reads at most 10 MiB of response; OD-18 still sets the v1 defaults.
- JSONPath is a subset: `$`, `.name` and `[index]`. Anything else is invalid (exit 3) until OD-10 picks an RFC 9535 implementation.
- Only `${name}` references to values saved by earlier steps of the same scenario; the `run.`, `steps.`, `identities.` and `row.` namespaces come with schema v1.
- Identities of type `none` and `bearer` (token from the environment variable the binding names); other types exit 3.
- Readiness: when `readiness.path` is set, the run polls it every 250 ms until it answers 2xx or `startup_timeout` (30 s by default) passes; then every scenario is NOT_RUN with `environment_not_ready` (exit 2).

## Testing approach

| Change | Where | Why |
|---|---|---|
| Proctor is tested only through its binary, run as a separate process; no function-level unit tests | §17 | Product owner decision (2026-11-14) |

## Changes from mutation testing (REQ-34, REQ-35)

The product owner asked for the strictest reliability bar (2026-11-14), so the gaps that mutation testing found are closed in the PRD and in the code. Rules marked "exit 3" were proposed as warnings in `mutation-testing/prd-improvements.md`; they are errors here because a warning a pipeline ignores leaves the gap open.

| Change | Where | Why |
|---|---|---|
| HTTP-07: exact matching of objects and arrays, and `keys` for an object's exact key set | §11.2 | Mutant AM-17 (a leaked `password_hash`) passed field-by-field assertions |
| HTTP-08: `present`, `absent`, `absent_headers` | §11.2 | Held-out mutant HM-25 (CORS granted to any origin) couldn't be expressed |
| HTTP-09: `length`, which may take a saved number; a length on an unfiltered list is invalid (exit 3) unless waived | §11.2 | AM-20 (limit ignored), HM-07 and HN-20 (page-size rules) |
| HTTP-10: a body with duplicate keys or several JSON values fails | §11.2 | Go keeps the last duplicate, other parsers the first, so a PASS would depend on Proctor's parser |
| EXE-07: a state check after every state-changing verification step, exit 3; `no_state_check` waiver with a reason | §11.1 | AM-14 (401, but the delete happened) passed whenever the read-back was missing |
| AUTH-06: refused and accepted credential variants, `token_env` or `header_env` | §11.4 | AM-12 and AM-13 were caught only through invented identities; HM-17 (lower-case scheme) couldn't be expressed |
| AUTH-07: `${secret.name}` in request bodies and headers, and a report scrub of every credential | §11.4 | HN-25 (login counter not reset) needs the real password in a request; an application echoing a token would have put it in the report |
| FIX-04: every ERROR has a cause | §11.5 | AM-02 and AM-10 were reported as ERROR with no sign that the application, not the environment, was wrong |
| FIX-05: `${run.id}` includes the scenario ID; a matched value without it is invalid (exit 3) unless waived | §11.5 | A run-wide value let two scenarios' rows match one check (found when the unmodified app failed) |
| FIX-08: every setup operation is also a verification step, exit 3 | §11.5 | AM-02 and AM-10 |
| DB-04a: column-level grants, `CREATE` on a schema, `CREATEDB`, `REPLICATION`, owned schemas, functions and databases, switched roles, missing declared objects | §11.7 | Each lets the verification role change data or read undeclared objects; column grants pass a table-level check |
| DB-08: result types, exact `first_row`, row cap, duplicate columns, saves from one row | §11.7 | AM-18 and AM-22 need database checks; HM-05, HM-06 and HM-13 need values only the database has |
| Weakenings listed separately in the semantic diff | §7.5 | All eight round-1 specification mutants showed only as "modified" |
| `trust_violation` reason; `trust_violation` and `not_started` are integrity violations | §13.3 | DB-04 refusals stop the run with exit 4 |
| Mutation testing runs continuously, with held-out sets and mutants of Proctor itself | §17 | Was a follow-up after Phase 4 |
| Single-sample database checks built early; Phase 4 accepted only when the MVP matrix passes | §20 | Mutation round 1 |

Defaults that don't change a requirement: 5-second query timeout (`--query-timeout`); `${fill(N)}` for at most 16 MiB of `x` characters, in request bodies and headers only.

## Warnings removed (REQ-37, OD-20)

The product owner decided that every warning is a failure (2026-11-14). Proctor no longer has a warning level.

| Change | Where | Why |
|---|---|---|
| EXE-08 (new): every `validate` finding fails with exit 3 or 4, and `run` sends nothing while one exists | §11.1 | OD-20; a warning a pipeline doesn't fail on gets ignored |
| FIX-05 and HTTP-09 are errors; a step can waive either with `waive: { <rule>: <reason> }`, and an added waiver is a weakening in the semantic diff | §11.2, §11.5, §7.5 | Without a waiver, FIX-05 would force authors to drop legitimate assertions, such as a fixed role name sent and read back. A waiver with no reason, for another rule, or with nothing to waive is invalid |
| EXE-06 (positive control for denied cases, not built yet) rejects instead of warning | §11.1 | Same decision |
| A `no_state_check` on a step that a later step already checks is invalid | EXE-07 | A stale waiver would hide a later removal of that check |
| The JSON report has no `warnings` field | §13 | Nothing fills it |

The round-1 specifications in `mutation-testing/round1/proctor/` send fixed names and now fail validation (FIX-05). They are kept as they ran; reproducing round 1 needs the Proctor build of that round.
