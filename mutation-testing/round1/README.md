# Mutation testing, round 1 (2026-11-14)

The first round, kept as it was run: `harness.py`, the specification set it ran against (`proctor/`, scenarios PGU-001 to PGU-012), the raw results (`results.json`) and the reports written from them. Round 2 (`../`) replaced the specification set and the harness; these files describe the state before the gaps were closed.

To rerun it, copy `proctor/` over `examples/users-api-postgres/proctor/` in a scratch checkout of the commit that holds this folder, and use Proctor from that commit. The current Proctor rejects this set (EXE-07, FIX-08, and FIX-05 since REQ-37 made it an error), which is one of round 2's results.
