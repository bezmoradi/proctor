# Held-out application mutants

Each file was written by an independent agent that could read the example application's source but not its specifications, the harness, earlier mutant sets or these reports. They measure whether the specifications cover the application's contract, not whether they fit defects already known.

| File | Mutants | Written | Detected when first run | Detected now |
|---|---|---|---|---|
| `set-a.json` | HM-01 to HM-25 | 2026-11-14, after round 2's specification revision | 6 of 25 | see `../mutation-test-results.md` |
| `set-b.json` | HN-01 to HN-25 | 2026-11-14, after scenarios PGU-020 to PGU-037 were added for set A | 11 of 25 | see `../mutation-test-results.md` |

"Detected now" is no longer a held-out measurement: scenarios were added after each set's first run to catch what it missed. The next set written the same way gives the next held-out score.

Run them with the harness:

    MT_HELDOUT=mutation-testing/heldout/set-a.json,mutation-testing/heldout/set-b.json python3 -I mutation-testing/harness.py
