#!/usr/bin/env python3
"""Summarize repeated harness runs into mutation-test-results.md tables.

    python3 -I mutation-testing/report.py runs/run-01.json runs/run-02.json ...

A case is stable when every run gives it the same exit code and the same
set of scenarios that didn't pass; it is met when every run met its
required result. Prints Markdown to standard output.
"""

import json
import sys


def sig(r):
    return (r.get("exit_code"), tuple(r.get("not_passed") or []))


def main():
    runs = [json.load(open(p)) for p in sys.argv[1:]]
    n = len(runs)
    out = []
    unstable, unmet = [], []

    def collect(section):
        ids = sorted(set().union(*(r.get(section, {}).keys() for r in runs)))
        rows = []
        for i in ids:
            per = [r[section].get(i) for r in runs]
            missing = sum(p is None for p in per)
            per = [p for p in per if p is not None]
            stable = missing == 0 and len({json.dumps(sig(p)) for p in per}) == 1
            if section == "spec":
                stable = missing == 0 and len({(p["validate_exit"], tuple(p["weakenings"]),
                                                 (p.get("original_app") or {}).get("exit_code"),
                                                 (p.get("paired_mutant") or {}).get("exit_code")) for p in per}) == 1
            met = missing == 0 and all(p.get("met") for p in per)
            if not stable:
                unstable.append(i)
            if not met:
                unmet.append(i)
            rows.append((i, per[0] if per else {}, stable, met, missing))
        return rows

    out.append(f"Runs summarized: {n}.\n")
    out.append("## Controls\n\n| Case | Required | Exit codes seen | Stable | Met in every run |\n|---|---|---|---|---|")
    for i, p, stable, met, _ in collect("controls"):
        codes = sorted({r["controls"][i]["exit_code"] for r in runs if i in r["controls"]})
        out.append(f"| unmodified app | 0 | {codes} | {'yes' if stable else '**no**'} | {'yes' if met else '**no**'} |")

    out.append("\n## Application mutants\n\n| ID | Layer | Defect | Required | Result | Scenarios that caught it | Rows after run | Stable | Met in every run |\n|---|---|---|---|---|---|---|---|---|")
    for i, p, stable, met, _ in collect("app"):
        res = {0: "PASS (exit 0)", 1: "FAIL (exit 1)", 2: "ERROR (exit 2)", 3: "invalid (exit 3)", 4: "integrity (exit 4)"}.get(p.get("exit_code"), str(p.get("exit_code")))
        req = "FAIL" if p.get("required") == 1 else "ERROR, never PASS"
        caught = ", ".join(p.get("not_passed") or []) or "–"
        if len(caught) > 60:
            caught = f"{len(p['not_passed'])} scenarios"
        out.append(f"| {i} | {p.get('layer')} | {p.get('title')} | {req} | {res} | {caught} | {len(p.get('db_rows') or [])} | "
                   f"{'yes' if stable else '**no**'} | {'yes' if met else '**no**'} |")

    out.append("\n## Specification mutants\n\n| ID | Weakening | Required | `proctor validate` | Weakenings `proctor diff` lists | Original app | Paired mutant | Stable | Met in every run |\n|---|---|---|---|---|---|---|---|---|")
    for i, p, stable, met, _ in collect("spec"):
        req = "rejected (exit 3)" if p.get("required") == "invalid" else "accepted and listed as a weakening"
        val = f"exit {p.get('validate_exit')}" + (f" ({', '.join(p.get('validate_rules') or [])})" if p.get("validate_rules") else "")
        w = "; ".join(p.get("weakenings") or []) or "none"
        orig = f"exit {p['original_app']['exit_code']}" if p.get("original_app") else "not run (invalid)"
        pair = f"{p['pair']}: exit {p['paired_mutant']['exit_code']}" if p.get("paired_mutant") else "–"
        out.append(f"| {i} | {p.get('title')} | {req} | {val} | {w} | {orig} | {pair} | {'yes' if stable else '**no**'} | {'yes' if met else '**no**'} |")

    out.append("\n## Database mutants\n\n| ID | Mutation | Required | Result | Violations reported | Rows after run | Stable | Met in every run |\n|---|---|---|---|---|---|---|---|")
    for i, p, stable, met, _ in collect("db"):
        out.append(f"| {i} | {p.get('title')} | exit {p.get('required')} | exit {p.get('exit_code')} | {len(p.get('violations') or [])} | "
                   f"{len(p.get('db_rows') or [])} | {'yes' if stable else '**no**'} | {'yes' if met else '**no**'} |")

    out.append(f"\nUnstable cases: {', '.join(unstable) or 'none'}. Cases not met in every run: {', '.join(unmet) or 'none'}.")
    print("\n".join(out))


if __name__ == "__main__":
    main()
