# Held-out application mutants

Each file was written by an independent agent that could read the example application's source but not its specifications, the harness, earlier mutant sets or these reports. They measure whether the specifications cover the application's contract, not whether they fit defects already known.

| File | Mutants | Written | Detected when first run | Detected now |
|---|---|---|---|---|
| `set-a.json` | HM-01 to HM-25 | 2026-11-14, after round 2's specification revision | 6 of 25 | see `../mutation-test-results.md` |
| `set-b.json` | HN-01 to HN-25 | 2026-11-14, after scenarios PGU-020 to PGU-037 were added for set A | 11 of 25 | see `../mutation-test-results.md` |
| `set-c.json` | HP-01 to HP-30 | 2026-11-14, after the scenarios added for sets A and B; run against the final set PGU-001 to PGU-049 | 17 of 30 (`set-c-first-run.json`; the unmodified app passed in the same run) | not changed since |

"Detected now" is no longer a held-out measurement: scenarios were added after each set's first run to catch what it missed. The next set written the same way gives the next held-out score.

Run them with the harness:

    MT_HELDOUT=mutation-testing/heldout/set-a.json,mutation-testing/heldout/set-b.json python3 -I mutation-testing/harness.py

## Set C misses, by cause

Set C ran once against the final specification set, on a quiet machine (no other test load), and no detection came from a timeout. These 13 were missed:

| Cause | Mutants | What would catch them |
|---|---|---|
| A scenario Proctor can already express is missing from the example's specification | HP-03 (HS512-signed token accepted), HP-08 (an oversized `/login` body answered 401, not 413), HP-09 (`/login` outside the cross-site check), HP-10 (case-sensitive `Content-Type`), HP-13 (CORS origin matched by prefix), HP-15 (`/healthz` loses its JSON content type), HP-18 (an update accepts `{"name":123}`), HP-27 (IDs parsed with base prefixes, `010` is 8), HP-28 (an update stores a trimmed name but echoes the untrimmed one) | A credential variant signed with HS512, `${fill(N)}`, request headers (`Origin`, `Sec-Fetch-Site`, `Content-Type`), `absent_headers`, a database check after the update. A specification gap, not a Proctor gap |
| Proctor can't express it | HP-05 (OPTIONS skips authentication): Proctor's methods are GET, POST, PUT, PATCH and DELETE. HP-07 (`Retry-After` truncated): the value depends on timing, and Proctor has no range assertion | OPTIONS as a method; a numeric range or comparison assertion. Neither is in the PRD |
| The scenario would have to break the environment | HP-16 (readiness answers 200 with the database down), HP-21 (500 responses leak the driver error) | Stopping the database during a run: a fault injected by the trusted environment (OD-23), not by a specification |

These are listed rather than fixed, so set C stays a held-out measurement. Adding the nine missing scenarios is the next specification change, and the next held-out set will measure it.
