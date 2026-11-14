# MVP mutation test matrix: REST and PostgreSQL

These are the smallest set of mutation cases Proctor must detect before the REST and PostgreSQL minimum viable product can be called credible. Each row picks one kind of defect that, if missed, would make a PASS misleading. Cases differing only in detail, such as other status-code swaps, are left out.

"Detected" means `proctor run` reports FAIL (exit 1) and the unmodified app still passes, unless the row says otherwise. Run the cases with `python3 -I mutation-testing/harness.py <ID>`. "Status today" is from the 2026-11-14 run with the hardened set.

## Must be detected through the REST API

| Case | Defect | Required result | Status today |
|---|---|---|---|
| AM-01 | Create answers 201 but inserts nothing | FAIL | Detected |
| AM-04 | Update answers 200 but changes nothing | FAIL | Detected |
| AM-06 | Delete answers 200 but deletes nothing | FAIL | Detected |
| AM-05 | An update changes other records too | FAIL | Detected (PGU-006) |
| AM-07 | A delete removes other records too | FAIL | Detected (PGU-007) |
| AM-08 | A nonexistent resource is returned with 200 | FAIL | Detected |
| AM-09 | A required field is accepted empty | FAIL | Detected |
| AM-11 | A protected endpoint accepts no token | FAIL | Detected |
| AM-12 | An expired token is accepted | FAIL | Detected, only through the invented `expired` identity (F-08) |
| AM-13 | A token with a broken signature is accepted | FAIL | Detected, only through the invented `tampered` identity (F-08) |
| AM-14 | A refused write is still performed | FAIL | Detected (PGU-011's read-back) |
| AM-15 | A field changes JSON type | FAIL | Detected |
| AM-17 | A response leaks a field | FAIL | **Missed**. Needs HTTP-07 or HTTP-08 in the PRD and the specification (HP-01 shows the fix) |
| AM-02 | A write is performed, then 500 is returned | FAIL, not ERROR | **Reported as ERROR** (F-04). Needs the create as a verification step |
| AM-10 | Create answers 200 instead of 201 | FAIL, not ERROR | **Reported as ERROR** (F-04). Same fix |
| AM-24 | PostgreSQL is down | ERROR or NOT_RUN, never PASS | Detected (ERROR, exit 2) |

## Needs a PostgreSQL check (not possible yet)

| Case | Defect | Required result | Status today |
|---|---|---|---|
| AM-18 | The API is consistent with itself, but nothing is stored | FAIL | **Missed**: no PostgreSQL adapter (F-01) |
| AM-22 | Each create stores two rows | FAIL | **Missed**: no PostgreSQL adapter (F-01) |
| AM-19 | A row from an earlier run matches the check | FAIL on the baseline set | **Missed**: no `${run.id}` (F-02) |
| AM-02 (database part) | The failed create's row stays in the table | FAIL: a check finds `rows: 0` for the run-scoped name | **Missed**: no PostgreSQL adapter |
| AM-14 (database part) | A refused delete removes the row | FAIL: a check finds the row still there | Detected through the API only |
| DB-A | A database check's query has a syntax error | ERROR (exit 2), never PASS (DB-05) | Not executable yet |
| DB-B | A database check's query writes (`UPDATE ...`) | Refused: ERROR or exit 4, with the table unchanged (DB-02, DB-04) | Not executable yet |
| DB-C | A parameter carries `'; DROP TABLE users; --` | The value is passed as data; the table is unchanged (DB-01) | Not executable yet |
| DB-D | A database check uses `${...}` inside `query` | Exit 3 before any request (DB-01) | Not executable yet |
| DB-E | Proctor's role has `INSERT` on a table | Exit 4 before the first scenario (DB-04) | Not executable yet |

## Specification mutations that must be visible to the reviewer

| Case | Weakening | Required result | Status today |
|---|---|---|---|
| SM-02 | The read-back after a write is removed | `proctor diff` lists a weakening, and validation warns (EXE-07) | Diff shows only "modified PGU-004"; no warning |
| SM-03 | The state check after a refused write is removed | Same | Diff shows only "modified PGU-011"; no warning |
| SM-04 | A scenario is removed | `proctor diff` lists a weakening | Diff shows "removed PGU-006" |
| SM-07 | A 2xx step keeps only its status | Exit 3 | Rejected (EXE-03) |

## Credible means

- Every row above shows its required result.
- The unmodified app passes the same specification set.
- The harness is rerun on the commit that claims the minimum viable product.

A high score here still leaves ownership between two users, tenant isolation, idempotency, concurrency and asynchronous operations untested. The example has none of them; testing them needs a richer example app.
