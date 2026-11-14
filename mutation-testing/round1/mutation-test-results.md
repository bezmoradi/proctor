# Proctor mutation testing: results

**Target:** `examples/users-api-postgres` (gopamin v1.3.0: chi, PostgreSQL 17, JWT), driven by `proctor run` in local mode.
**Proctor revision:** branch `phase-0/integrity-foundation`, uncommitted working tree, 2026-11-14. Phase 0 runner: HTTP only; there is no PostgreSQL adapter yet.
**Raw data:** `mutation-testing/results.json`. **Harness:** `mutation-testing/harness.py`.

Everything in the two tables below was executed. Anything only reasoned about is in "Not executed", with the reason.

## How the runs work

For each case, the harness:

1. copies the example and applies the mutation as exact text edits; an edit that doesn't match exactly stops the harness;
2. truncates the `users` table and restarts its ID sequence;
3. starts the app, logs in as `admin`, and mints an expired token and a token with a broken signature;
4. runs `proctor run --json` against one of two specification sets;
5. records Proctor's result and the rows left in `users`, so a PASS can be compared with the stored data.

The two specification sets:

- **Hardened:** `examples/users-api-postgres/proctor/specs/users.yaml`, scenarios PGU-001 to PGU-012.
- **Baseline:** what a first draft would plausibly contain. It is the hardened set without PGU-006, PGU-007 and PGU-009 to PGU-012, and without the Content-Type assertion. It keeps the CRUD round trips (create, read, rename, delete), the 404 and validation checks, and the no-token check.

To reproduce, start the example's database and run its migrations (`examples/README.md`), then run `python3 -I mutation-testing/harness.py` from the repository root.

**Controls:** the unmodified app passes both sets (exit 0). A rerun of the control and of AM-01 to AM-17 gave identical results, so no case was flaky across two runs.

## Application mutations

| ID | Layer | Mutation | Expected | Actual, hardened set | Detected? | Actual, baseline set | Detected? | Rows after run | Severity if undetected |
|---|---|---|---|---|---|---|---|---|---|
| AM-01 | REST+DB | Create returns 201 with an ID but inserts nothing | FAIL | FAIL (exit 1) in PGU-001, PGU-004, PGU-005, PGU-006, PGU-007, PGU-011 | Yes | FAIL (exit 1) in PGU-001, PGU-004, PGU-005 | Yes | 0 | – |
| AM-02 | REST+DB | Create inserts the row, then answers 500 | FAIL | ERROR (exit 2) in PGU-001, PGU-004, PGU-005, PGU-006, PGU-007, PGU-011 | Yes, as ERROR (F-04) | ERROR (exit 2) in PGU-001, PGU-004, PGU-005 | Yes, as ERROR | 6 | – |
| AM-03 | DB | Create stores the name in upper case | FAIL | FAIL (exit 1) in PGU-001, PGU-006, PGU-007, PGU-011 | Yes | FAIL (exit 1) in PGU-001 | Yes | 6 | – |
| AM-04 | REST+DB | Update answers 200 but changes nothing | FAIL | FAIL (exit 1) in PGU-004 | Yes | FAIL (exit 1) in PGU-004 | Yes | 6 | – |
| AM-05 | DB | Update renames every user (WHERE clause broken) | FAIL | FAIL (exit 1) in PGU-006 | Yes | PASS (exit 0) | **No** | 6 | Critical |
| AM-06 | REST+DB | Delete answers 200 but deletes nothing | FAIL | FAIL (exit 1) in PGU-005 | Yes | FAIL (exit 1) in PGU-005 | Yes | 8 | – |
| AM-07 | DB | Delete removes every user (WHERE clause broken) | FAIL | FAIL (exit 1) in PGU-007 | Yes | PASS (exit 0) | **No** | 1 | Critical |
| AM-08 | REST | A nonexistent user is returned with 200 and an empty name | FAIL | FAIL (exit 1) in PGU-003, PGU-005 | Yes | FAIL (exit 1) in PGU-003, PGU-005 | Yes | 6 | – |
| AM-09 | REST | An empty name is accepted | FAIL | FAIL (exit 1) in PGU-002 | Yes | FAIL (exit 1) in PGU-002 | Yes | 7 | – |
| AM-10 | REST | Create answers 200 instead of 201 | FAIL | ERROR (exit 2) in PGU-001, PGU-004, PGU-005, PGU-006, PGU-007, PGU-011 | Yes, as ERROR (F-04) | ERROR (exit 2) in PGU-001, PGU-004, PGU-005 | Yes, as ERROR | 6 | – |
| AM-11 | Auth | A request without a token is let through | FAIL | FAIL (exit 1) in PGU-008, PGU-011 | Yes | FAIL (exit 1) in PGU-008 | Yes | 5 | – |
| AM-12 | Auth | Expired tokens are accepted | FAIL | FAIL (exit 1) in PGU-009 | Yes | PASS (exit 0) | **No** | 6 | Critical |
| AM-13 | Auth | Tokens with an invalid signature are accepted | FAIL | FAIL (exit 1) in PGU-010 | Yes | PASS (exit 0) | **No** | 6 | Critical |
| AM-14 | Auth+DB | A request without a token gets 401, but the action is still performed | FAIL | FAIL (exit 1) in PGU-011 | Yes | PASS (exit 0) | **No** | 5 | Critical |
| AM-15 | REST | Create returns the ID as a JSON number instead of a string | FAIL | FAIL (exit 1) in PGU-001 | Yes | FAIL (exit 1) in PGU-001 | Yes | 6 | – |
| AM-16 | REST | Reading a user answers with Content-Type text/plain | FAIL | FAIL (exit 1) in PGU-001 | Yes | PASS (exit 0) | **No** | 6 | Low |
| AM-17 | REST | User responses leak a password_hash field | FAIL | PASS (exit 0) | **No** | PASS (exit 0) | **No** | 6 | High |
| AM-18 | REST+DB | Data is kept in memory; nothing reaches PostgreSQL (the API agrees with itself) | FAIL | PASS (exit 0) | **No** | PASS (exit 0) | **No** | 0 | Critical |
| AM-19 | REST+DB | Create inserts nothing and returns ID 1, which an old row already uses | FAIL | FAIL (exit 1) in PGU-006, PGU-007, PGU-011 | Yes | PASS (exit 0) | **No** | 0 | High |
| AM-20 | REST | The list ignores limit and returns up to 1000 users | FAIL | PASS (exit 0) | **No** | PASS (exit 0) | **No** | 6 | Medium |
| AM-21 | REST | PATCH is routed and answers 200 instead of 405 | FAIL | FAIL (exit 1) in PGU-012 | Yes | PASS (exit 0) | **No** | 6 | Low |
| AM-22 | DB | Every create inserts the row twice | FAIL | PASS (exit 0) | **No** | PASS (exit 0) | **No** | 14 | High |
| AM-23 | REST | Update persists the new name but answers with a stale one | FAIL | FAIL (exit 1) in PGU-004, PGU-006 | Yes | FAIL (exit 1) in PGU-004 | Yes | 6 | – |
| AM-24 | Environment | PostgreSQL stops after the application started | ERROR or NOT_RUN, never PASS | ERROR (exit 2) in PGU-001, PGU-002, PGU-003, PGU-004, PGU-005, PGU-006, PGU-007, PGU-008, PGU-009, PGU-010, PGU-011, PGU-012 | Yes | ERROR (exit 2) in PGU-001, PGU-002, PGU-003, PGU-004, PGU-005, PGU-008 | Yes | 0 | – |

"Rows after run" is the `users` table after the hardened run. The truncate and restart before each run make it comparable across mutants. For example, AM-18 leaves 0 rows where the original leaves 6, and AM-22 leaves 14.

## Specification mutations

Each specification mutation weakens the hardened set once. It is run against the unmodified app, then against the app mutation it would let through. HP-01 is not a weakening: it probes whether a stronger assertion closes F-03.

| ID | Specification mutation | `proctor validate` | `proctor diff` | Original app | Paired app mutant | Paired result | Weakened spec lets the defect through? |
|---|---|---|---|---|---|---|---|
| HP-01 | Probe: PGU-001 asserts the whole $.data object instead of two fields | accepted (0) | 1 protected change: modified PGU-001 | PASS (exit 0) | AM-17 | FAIL (exit 1) in PGU-001 | No |
| SM-01 | PGU-001 asserts only the ID on read-back, not the name | accepted (0) | 1 protected change: modified PGU-001 | PASS (exit 0) | AM-03 | FAIL (exit 1) in PGU-006, PGU-007, PGU-011 | No |
| SM-02 | PGU-004 drops the read after the rename | accepted (0) | 1 protected change: modified PGU-004 | PASS (exit 0) | AM-04 | PASS (exit 0) | **Yes** |
| SM-03 | PGU-011 drops the check that the user still exists | accepted (0) | 1 protected change: modified PGU-011 | PASS (exit 0) | AM-14 | PASS (exit 0) | **Yes** |
| SM-04 | PGU-006 (other users unchanged by a rename) removed | accepted (0) | 1 protected change: removed PGU-006 | PASS (exit 0) | AM-05 | PASS (exit 0) | **Yes** |
| SM-05 | Content-Type assertion removed from PGU-001 | accepted (0) | 1 protected change: modified PGU-001 | PASS (exit 0) | AM-16 | PASS (exit 0) | **Yes** |
| SM-06 | PGU-005's deletion moved to a verification step that checks only the response message | accepted (0) | 1 protected change: modified PGU-005 | PASS (exit 0) | AM-06 | PASS (exit 0) | **Yes** |
| SM-07 | A 2xx verification step keeps only its status (PGU-001 read) | rejected (3) | 1 protected change: modified PGU-001 | not run (invalid) | – | – | – |
| SM-08 | PGU-009 and PGU-010 (invalid tokens) removed | accepted (0) | 1 protected change: removed PGU-009, removed PGU-010 | PASS (exit 0) | AM-12 | PASS (exit 0) | **Yes** |

## Scores

The denominator is executed, non-equivalent mutations. No mutation was equivalent.

| Measure | Hardened set | Baseline set |
|---|---|---|
| Application mutations executed | 24 | 24 |
| Detected (not PASS) | 20 | 12 |
| Detected as FAIL | 17 | 9 |
| Detected only as ERROR (exit 2) | 3: AM-02, AM-10, AM-24 | 3: AM-02, AM-10, AM-24 |
| False PASS (mutation undetected) | 4: AM-17, AM-18, AM-20, AM-22 | 12 |
| False FAIL (the unmodified app failing) | 0 | 0 |
| **Application mutation score** | **20/24 = 83%** | **12/24 = 50%** |

AM-24 is an environment fault, not a code change; ERROR is the correct outcome for it. Without it, the scores are 19/23 for the hardened set and 11/23 for the baseline.

| Specification mutations | Count |
|---|---|
| Executed | 8 (SM-01 to SM-08) |
| Rejected by `proctor validate` | 1 (SM-07, EXE-03) |
| Reported by `proctor diff` as a protected change | 8 of 8 |
| Accepted, and let their paired application defect pass | 6 (SM-02, SM-03, SM-04, SM-05, SM-06, SM-08) |
| Accepted, but other scenarios still caught the paired defect | 1 (SM-01) |

The only protection against a weakened specification is the review that `proctor diff` triggers. Proctor itself rejected one weakening out of eight (F-06).

## Not executed

None of these appear in the scores.

| Guideline area | Status | Reason |
|---|---|---|
| §5.E SQL verification (all 15 cases), read-only queries, injection through parameters, write/DDL rejection, timeouts, permissions | Blocked | Proctor has no PostgreSQL adapter yet (Phase 4). The PRD's design for them is DB-01 to DB-07; see `prd-improvements.md` |
| §5.A–D persistence, constraints, transactions and read-after-write, checked against stored rows | Partly executed through the API only | AM-01 to AM-07, AM-18, AM-19 and AM-22 change persistence. Proctor could only observe them through HTTP read-backs. The harness's row snapshots show the stored state, but no Proctor check reads it |
| §4.E ownership and tenant isolation (Alice and Bob, owner ID in the body, tenant ID) | Blocked by the target | The example has one user, `admin`, and its users have no owner or tenant. AM-14 is the closest executed case: an unauthenticated write that returns 401 but is still performed |
| §4.E read-only scopes, revoked access, wrong-user tokens | Blocked by the target | No roles, scopes or revocation in the example |
| §4.H idempotency keys, concurrency, retries after timeouts | Blocked by the target and the runner | The example has no idempotency contract, and the Phase 0 runner runs one request at a time with no concurrency construct |
| §4.G filtering and sorting | Blocked by the target | The list endpoint supports only `limit` and `cursor`. AM-20 covers `limit` |
| §4.B 202 and asynchronous operations, 409 conflicts | Blocked by the target | The example has neither |
| §4.C dates, enums, UUIDs, numeric bounds | Blocked by the target | The only input field is `name` |
| §4.A HTTP method mutations beyond PATCH | Not executed | Each would be another variant of AM-21 |

## What the unmodified app does that the contract doesn't settle

These came from probing the unmodified app. They aren't Proctor findings, and no specification can decide them, because nobody has stated the intended behavior:

- A name of three spaces is accepted and stored as `"   "`.
- An unknown field such as `"owner": "bob"` is silently ignored.
- `"name": null` is treated like a missing name (422).
- Deleting a user returns 200 with a message rather than 204, and a second delete returns 404.
