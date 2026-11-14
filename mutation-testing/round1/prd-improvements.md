# PRD improvements from mutation testing

Proposed changes to `prd-revised.md`, one per finding in `mutation-test-findings.md`. They are proposals; neither `prd.md` nor `prd-revised.md` has been changed. Each change names the section it touches and leaves the rest alone.

## 1. §11.2: define how JSON values are matched (F-03)

Add after the "Assertions" paragraph:

> - **HTTP-07.** A `json` assertion's expected value is matched by type and value. An object matches only an object with exactly the same keys, and an array only an array with the same length and elements in the same order; extra keys or elements fail. A specification that needs a looser check uses an explicit assertion: `present`, `absent`, `length`, or `contains`.
> - **HTTP-08.** `absent: [<path>, ...]` fails if any listed path selects a value, including `null`. `present: [<path>, ...]` fails if any listed path selects nothing. A field set to `null` is present.

Why: AM-17 (a leaked `password_hash`) passes field-by-field assertions and fails a whole-object assertion (HP-01). The PRD has to fix which behavior schema v1 keeps.

## 2. §11.1: require a state check after every write and every denial (F-01, F-07)

Add after EXE-06:

> - **EXE-07.** In each scenario, a state-changing verification step (POST, PUT, PATCH or DELETE) is followed by a check of the state it was meant to change: a read through the API by an identity allowed to read it, or a database check (§11.7). A denied state-changing request (expecting 401, 403 or 404) is followed by a check that the state did not change. Validation warns when a scenario lacks this check; it doesn't fail, because some operations have no readable state.

Why: AM-14 (401, but the delete happened) passed whenever the read-back was missing (baseline set, SM-03). AM-04 and AM-06 were caught only by read-backs.

## 3. §11.5: name the cause of a setup failure, and require the operation under test as a step (F-04)

Replace FIX-04's first sentence with:

> - **FIX-04.** Setup steps are reported separately and are not counted as assertions. A setup step whose expectation fails, or that can't be executed, is ERROR with reason `setup_failed` and a `cause`: `unexpected_response` (the report names the expectation that failed and the value received), `unreachable`, or `timeout`.

Add:

> - **FIX-08.** Every operation a specification set uses in setup is also exercised, in at least one scenario, as a verification step with the same expectations. Validation warns when an operation (method and path template) appears only in setup.

Why: AM-02 (insert, then 500) and AM-10 (200 instead of 201) were reported as ERROR with exit 2, which pipelines commonly retry as infrastructure flakes.

## 4. §11.5: make `${run.id}` part of the minimum viable product, and say where it must be used (F-02)

Extend FIX-05:

> Any value a later assertion or database check matches on (a name, an email, a key) includes `${run.id}`. Validation warns when a scenario's request body sets a value that a later step asserts on and the value contains no `${run.id}`.

And in §20, move `${run.id}` from schema v1 (Phase 2) into the first increment that ships PostgreSQL checks.

Why: AM-19 passed on the baseline set because a row left by an earlier run matched.

## 5. §11.4: credential variants for negative authentication tests (F-08)

Add:

> - **AUTH-06.** An identity can have credential variants supplied by the environment: `expired`, `invalid_signature`, `wrong_audience`, `malformed`. A request names one with `credential: <variant>`. The binding names the variable that holds each variant's token. Variants are test data from the disposable environment; Proctor never creates or alters tokens itself.

Why: AM-12 and AM-13 were caught only because the hardened set used two invented identities, `expired` and `tampered`.

## 6. §7.5: show weakenings in the semantic diff (F-06)

Add to the semantic diff's output:

> The semantic diff lists, separately from other modifications, every **weakening**: a removed scenario, a removed step, a removed assertion, an expectation replaced by a broader one (a status by a list of statuses, an exact value by `present`), and a removed database or time-based check. Each entry names the scenario, the step and the number of assertions lost.

Why: all eight specification mutations were reported only as "modified PGU-004" or "removed PGU-006". In six of them the weakened set let a real defect through.

## 7. §11.2: list assertions scoped to the run (F-05)

Add to §11.2:

> List assertions (`length`, `contains`) are only meaningful over records the scenario created. A specification either filters the list by a value containing `${run.id}`, where the API supports a filter, or runs against a database created for the run (FIX-06). Validation warns when a `length` assertion is applied to a list request that has no query parameter containing `${run.id}`.

Why: AM-20 (the list ignores `limit`) couldn't be specified safely in a shared database.

## 8. §20: order the PostgreSQL work before the REST and PostgreSQL MVP is claimed (F-01)

Add to Phase 4's acceptance:

> Phase 4 is accepted only when the mutation cases in `mutation-testing/mvp-test-matrix.md` marked "needs PostgreSQL check" are detected.

Why: AM-18 and AM-22 show that no HTTP-only specification detects an API that is consistent with itself while storing the wrong data.

## Not proposed

- **Concurrency and idempotency constructs** (two requests at once, the same idempotency key twice): the guideline asks for them, but the example has no idempotency contract, so nothing here shows what the construct must express. Revisit with a target that has one.
- **Schema validation** (JSON Schema): HTTP-07 and HTTP-08 cover the observed miss. A schema construct would add a second assertion language.
