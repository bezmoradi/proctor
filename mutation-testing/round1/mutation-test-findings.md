# Proctor mutation testing: findings

These findings come from the executed runs in `mutation-test-results.md`. Each one names the mutations that show it. "Gap in" says where the fix belongs: the specification, the runner (Proctor's REST execution), the PostgreSQL adapter, the PRD's specification model, or the application.

To reproduce a mutation, start the example's database and run its migrations (`examples/README.md`), then run `python3 -I mutation-testing/harness.py <ID>` from the repository root. The result is written to `mutation-testing/results.json`.

## F-01. Critical: the API can agree with itself while the database is wrong

**Mutations:** AM-18 (data kept in memory; nothing reaches PostgreSQL) and AM-22 (every create inserts the row twice). Both PASS on the hardened and the baseline sets.

**What happened:**
- AM-18: every scenario passed and the `users` table ended empty. The original app leaves 6 rows.
- AM-22: every scenario passed and the table held 14 rows instead of 6, every name duplicated.

**Why Proctor missed it:** every check goes through the same API that produces the defect. An HTTP read-back shows only that the API is consistent with itself, not what was stored. Proctor has no PostgreSQL adapter yet. The PRD's design for one (DB-01 to DB-07, Phase 4) would let a check read the row by its captured ID, or count the rows with a run-scoped name.

**Gap in:** PostgreSQL adapter (not built).

**Correction:** build the minimal PostgreSQL check from §11.7: a parameterized query, a `rows` count and `first_row` values, read-only. Use it in the specifications after every state-changing step (F-07).

**Acceptance criteria:**
- With a check `SELECT name FROM users WHERE id = $1` (argument: the captured ID) expecting `rows: 1` and `first_row.name: Ada`, AM-18 FAILs.
- With a check `SELECT count(*) AS n FROM users WHERE name = $1` (argument: a name containing `${run.id}`) expecting `first_row.n: 1`, AM-22 FAILs.
- The unmodified app still passes.

## F-02. High: data left by earlier runs can hide a defect

**Mutation:** AM-19. Create inserts nothing and returns ID 1, which a row left by an earlier run already has (seeded as "Ada"). It PASSes on the baseline set; the hardened set catches it only by accident, in PGU-006, PGU-007 and PGU-011.

**What happened:** PGU-001 created "Ada", got ID 1 back, read user 1 and found "Ada", the old row. It passed although nothing was created.

**Why Proctor missed it:** the specification can't make its data unique to the run. The PRD gives every run a `${run.id}` for this (FIX-05), but the Phase 0 runner rejects it (`${run.id}` is invalid, exit 3). Every run writes the same values, so stale data matches.

**Gap in:** runner (`${run.id}` not implemented) and specification (values not run-scoped).

**Correction:** implement `${run.id}`, and use it in every value a later assertion matches on.

**Acceptance criteria:** with the name `Ada ${run.id}`, AM-19 FAILs in PGU-001 on the baseline set while the seeded row exists, and the unmodified app still passes.

## F-03. High: extra fields in a response pass unnoticed

**Mutation:** AM-17. User responses gain a `password_hash` field. It PASSes on both sets.

**Why Proctor missed it:** each `json` key checks one field and says nothing about fields it doesn't name. The Phase 0 runner has no absence assertion. The PRD lists "presence and absence of fields when stated" (§11.2) but doesn't say how a specification states it.

**What closes it today:** HP-01 changed the read-back to compare the whole object, `$.data: { id: "${user_id}", name: Ada }`. The runner compares objects exactly, so AM-17 FAILed and the unmodified app still passed.

**The catch:** exact object comparison is how the runner happens to work, and the PRD doesn't define it. If schema v1 makes object matches partial, the probe would pass again.

**Gap in:** PRD specification model (matching semantics undefined) and runner (no absence assertion).

**Correction:** define in §11.2 that an object or array value is matched exactly: extra fields or elements fail. Also add an explicit absence assertion, for responses whose other fields can't be fixed in advance.

**Acceptance criteria:** AM-17 FAILs both with a whole-object assertion and with an absence assertion on `$.data.password_hash`.

## F-04. Medium: a defect that shows up in setup is reported as an error, not a failure

**Mutations:** AM-02 (create inserts the row, then answers 500) and AM-10 (create answers 200 instead of 201). Both exit 2, ERROR with `setup_failed`, on both sets.

**What happened:** PGU-001 and five other scenarios create their user in a setup step. That step's unexpected status made each scenario ERROR (FIX-04), and the run exited 2.

**Why it matters:**
- Exit 2 means "missing evidence", and pipelines commonly retry it as an infrastructure flake. A real defect is reported like a broken environment.
- AM-02 also left 6 rows behind, one per failed create. Nothing reports that a write happened despite the 500.

**Why it happened:** this is FIX-04 working as designed. The operation under test (create) sits in setup, because the specifications follow §11.5's pattern of creating data in setup.

**Gap in:** specification (the create is never itself a verification step) and the PRD's result model (a setup failure says nothing about its cause).

**Correction:**
1. Every operation the contract covers must appear in at least one scenario as a verification step. Here that means a "create returns 201" scenario whose create is a step, not setup.
2. Report a setup failure's cause: `unexpected_response` (with the assertion that failed) versus `unreachable` or `timeout`. Then a reader, or a pipeline, can tell an application defect from an environment problem.

**Acceptance criteria:**
- With a create-as-step scenario, AM-02 and AM-10 FAIL (exit 1).
- The setup ERROR's report names `unexpected_response` and the status received.

## F-05. Medium: list and pagination behavior can't be expressed in Phase 0

**Mutation:** AM-20. The list ignores `limit`. It PASSes on both sets, which have no list scenario.

**Why there is no list scenario:**
- The Phase 0 runner has no array-length or presence-only assertion. `next_cursor` is an opaque value, so it can only be matched exactly.
- In a shared database, the list's contents depend on earlier scenarios, so an exact list can't be written safely. This is the PRD's shared-environment problem (FIX-06).

**Gap in:** runner (array length and presence assertions from §11.2 not implemented) and PRD (no way to scope a list assertion to the run's own records).

**Correction:**
- Implement array length and presence assertions.
- Recommend a filter parameter containing `${run.id}`, where the API has one; otherwise a disposable database per run (FIX-06).

**Acceptance criteria:** a scenario that creates two users and lists with `limit=1` asserts `$.data` has length 1 and `$.next_cursor` is present. AM-20 FAILs.

## F-06. High: weakened specifications pass silently; only review catches them

**Mutations:** SM-02, SM-03, SM-04, SM-05, SM-06 and SM-08. Each weakened specification passed `proctor validate`, passed against the unmodified app, and let its paired defect through. Of the eight weakenings, only SM-07 was rejected (EXE-03: a 2xx step with only a status).

**What does catch it:** `proctor diff` reported each of the eight as one protected change, naming the modified or removed scenario. That is the designed defense (INT-07 to INT-10, §6): a weakened specification can't merge without code-owner approval.

**What the review loses:**
- The diff says "modified PGU-004", not "the read-back after the rename was removed".
- A reviewer has to spot the weakening in the YAML diff.
- An agent that weakens a specification and changes the code in the same pull request depends entirely on that reviewer noticing.

**Gap in:** PRD (semantic diff detail, §7.5) and validation (no rule for state checks).

**Correction:**
- The semantic diff lists removed steps, removed assertions and removed scenarios as "weakening" entries, separate from other modifications.
- Add the state-check rule in F-07.

**Acceptance criteria:**
- For SM-02, `proctor diff` reports "PGU-004: step `read` removed (1 assertion)" as a weakening.
- For SM-04, it reports "PGU-006 removed (2 steps, 2 assertions)".

## F-07. High: a refused request can still have changed the data

**Mutation:** AM-14. A request without a token gets 401, but the action is still performed.
- On the baseline set, it PASSes.
- The hardened set catches it only because PGU-011 reads the user back after the refused delete.
- SM-03 removes that read-back, and AM-14 then passes (exit 0).

**Why it matters:** a status assertion on a denied request shows what the client was told, not what happened. The same holds for Alice being refused on Bob's resource, which couldn't be executed against this example.

**Gap in:** specification, and the PRD's authoring rules. EXE-06 asks for a positive control, but not for a state check after a denial.

**Correction:**
- A denied state-changing request (POST, PUT, PATCH or DELETE expecting 401, 403 or 404) must be followed in the same scenario by a check that the state didn't change: a read-back by an allowed identity, or a database check.
- Validation warns when it isn't, like EXE-06's warning.
- The same applies to successful writes: each must be followed by a read-back or a database check (F-01).

**Acceptance criteria:**
- `proctor validate` warns on SM-03's PGU-011.
- With the read-back or a database check in place, AM-14 FAILs.

## F-08. Medium: testing invalid credentials needs fake identities

**Mutations:** AM-12 (expired tokens accepted) and AM-13 (bad signatures accepted). Both are caught only because the hardened set declares two extra "identities", `expired` and `tampered`. The harness mints their tokens and passes them in through environment variables.

**Why it matters:** the PRD's identities are users (§11.4). Credential variants aren't modelled, so each team invents its own way, or skips these cases. SM-08 shows that removing them lets AM-12 through.

**Gap in:** PRD (AUTH-*).

**Correction:**
- Define credential variants an environment can supply for an identity: expired, wrong signature, wrong audience, malformed.
- Add a standard negative-authentication scenario pattern that uses them.

**Acceptance criteria:** a specification can request `identity: admin` with `credential: expired`; AM-12 FAILs, and the binding names where the variant comes from.

## F-09. Information: the example's contract is unsettled

The unmodified app accepts a whitespace-only name, ignores unknown fields such as `owner`, and treats `null` like a missing name. These aren't Proctor defects. No specification can decide them until someone states the intended behavior. A guinea pig for the full guideline also needs two owners, a tenant, an idempotency key and an asynchronous operation (see "Not executed" in `mutation-test-results.md`).

## Final assessment

Each answer is about Proctor as built (Phase 0, HTTP only), with what the PRD's design would add noted separately.

1. **Can Proctor detect an API returning success without performing the database operation?**
   - Only when an HTTP read-back reaches the database: yes for AM-01, AM-04 and AM-06.
   - Not when the API is consistent with itself: AM-18 passed with an empty table.
   - Needs the PostgreSQL adapter (F-01).
2. **Can it detect unauthorized database mutations when the API returns the expected 401 or 403?**
   - Only if the specification checks the state afterwards: AM-14 was caught by PGU-011's read-back and missed without it.
   - Not executable for 403 between two owners on this example.
3. **Can it verify that the correct user's data was persisted?**
   - Not directly: there's no database check, and the example has no owners.
   - Through the API, a wrong value is caught (AM-03), and so is a wrong record (AM-05 and AM-07, by the cross-record scenarios).
4. **Can it distinguish missing, null, empty and incorrect values?**
   - Partly. Comparisons are type-strict (AM-15 caught a number in place of a string), and `null` is compared as a value.
   - A missing field is reported as "not present".
   - There's no absence assertion, and object matching semantics aren't in the PRD (F-03).
5. **Can it detect partial transaction failures?**
   - It reports the failing status, as ERROR from setup (F-04).
   - It doesn't detect the leftover write; that needs a database check.
6. **Can it detect duplicate and unintended side effects?**
   - Not duplicates: AM-22 passed with 14 rows.
   - Unintended changes to other records, yes, when the specification checks another record (AM-05, AM-07).
7. **Can it verify database state without the verification query mutating it?** Not yet testable: no adapter. The PRD's design (a READ ONLY transaction, role checks, no SQL interpolation; DB-01 to DB-04) is sound on paper and untested.
8. **What can't the PRD's specification model express?**
   - Exact versus partial object matching (undefined).
   - Credential variants (F-08).
   - Run-scoped list assertions (F-05).
   - Concurrency and idempotency checks (two requests at the same time, or the same key twice), for which there is no construct.
   - A rule that writes and denials be followed by a state check (F-07).
   - Setup failure causes (F-04).
9. **What is the smallest set of changes for the most serious gaps?**
   1. The minimal PostgreSQL check (F-01).
   2. `${run.id}` (F-02).
   3. Exact object matching stated in the PRD, plus an absence assertion (F-03).
   4. The state-check validation warning (F-07).
   5. Weakening entries in `proctor diff` (F-06).

**Recommendation: no-go for the REST and PostgreSQL MVP as it stands.** The success criterion, detecting mismatches between the API's claims and the stored PostgreSQL state, can't be met without a database check; F-01 shows a false PASS with an empty table.

**Go for continuing:** the REST layer caught every defect its specifications covered: 20 of 24 on the hardened set, with no false failures. The remaining misses map to the five changes above. Rerun this harness after each one; `mvp-test-matrix.md` lists the cases that must then be detected.
