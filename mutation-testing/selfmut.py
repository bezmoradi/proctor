#!/usr/bin/env python3
"""Mutation testing of Proctor itself.

Each mutant changes one operator, constant, boolean, result value or
statement in internal/ (sites listed by mutation-testing/selfmut, which
parses the Go source). The black-box tests in e2e/ then build the mutated
binary and run it; a mutant is killed when any test fails. A mutant that
doesn't compile is "not viable" and isn't counted.

Every 50th job is an unmutated control: if a control ever fails, the
tests are flaky under load and kills can't be trusted, so the run says so.

Run from the repository root, with PROCTOR_E2E_POSTGRES_DSN set:

    MT_SELF_WORK=/tmp/selfmut python3 -I mutation-testing/selfmut.py [workers]

Results go to mutation-testing/self-mutation-results.json, saved as they
arrive, so an interrupted run can be resumed (finished mutants are kept).
"""

import concurrent.futures
import json
import os
import re
import shutil
import subprocess
import sys
import threading
import time

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(REPO, "mutation-testing", "self-mutation-results.json")
COPY = ["cmd", "internal", "e2e", "go.mod", "go.sum"]


def sites():
    files = sorted(os.path.join("internal", d, f) for d in os.listdir(os.path.join(REPO, "internal"))
                   for f in os.listdir(os.path.join(REPO, "internal", d)) if f.endswith(".go"))
    p = subprocess.run(["go", "run", "./mutation-testing/selfmut"] + files, cwd=REPO, capture_output=True, text=True, check=True)
    out = [json.loads(l) for l in p.stdout.splitlines()]
    for i, s in enumerate(out):
        s["id"] = f"PM-{i + 1:04d}"
    return out


def prepare(work, n):
    for i in range(n):
        d = os.path.join(work, f"w{i}")
        shutil.rmtree(d, ignore_errors=True)
        os.makedirs(d)
        for c in COPY:
            src = os.path.join(REPO, c)
            (shutil.copytree if os.path.isdir(src) else shutil.copy)(src, os.path.join(d, c))


def run_one(d, mutant):
    """Apply one mutant in worker copy d, test it, and restore the file."""
    path = None
    original = None
    if mutant:
        path = os.path.join(d, mutant["file"])
        original = open(path, "rb").read()
        if original[mutant["start"]:mutant["end"]].decode() != mutant["old"]:
            raise SystemExit(f"{mutant['id']}: site text differs; the source changed since the sites were listed")
        open(path, "wb").write(original[:mutant["start"]] + mutant["new"].encode() + original[mutant["end"]:])
    try:
        b = subprocess.run(["go", "build", "./..."], cwd=d, capture_output=True, text=True, timeout=300)
        if b.returncode != 0:
            return {"status": "not_viable"}
        start = time.time()
        t = subprocess.run(["go", "test", "-count=1", "-failfast", "./e2e"], cwd=d, capture_output=True, text=True, timeout=900)
        failed = re.findall(r"--- FAIL: (\S+)", t.stdout)
        return {"status": "survived" if t.returncode == 0 else "killed", "seconds": round(time.time() - start, 1),
                "failed": failed[:3], "tail": "" if t.returncode == 0 or failed else t.stdout[-400:]}
    except subprocess.TimeoutExpired:
        return {"status": "killed", "failed": ["timeout"]}
    finally:
        if mutant:
            open(path, "wb").write(original)


def main():
    if not os.environ.get("PROCTOR_E2E_POSTGRES_DSN"):
        raise SystemExit("set PROCTOR_E2E_POSTGRES_DSN")
    workers = int(sys.argv[1]) if len(sys.argv) > 1 else 6
    work = os.environ.get("MT_SELF_WORK") or "/tmp/proctor-selfmut"
    os.makedirs(work, exist_ok=True)
    all_sites = sites()
    results = json.load(open(OUT)) if os.path.exists(OUT) else {"mutants": {}, "controls": []}
    todo = [s for s in all_sites if s["id"] not in results["mutants"] or results["mutants"][s["id"]]["old"] != s["old"]]
    jobs = []
    for i, s in enumerate(todo):
        if i % 50 == 0:
            jobs.append(None)
        jobs.append(s)
    jobs.append(None)
    prepare(work, workers)
    free = list(range(workers))
    lock = threading.Lock()

    def job(m):
        with lock:
            w = free.pop()
        try:
            return m, run_one(os.path.join(work, f"w{w}"), m)
        finally:
            with lock:
                free.append(w)

    done = 0
    with concurrent.futures.ThreadPoolExecutor(workers) as ex:
        for m, r in ex.map(job, jobs):
            with lock:
                if m is None:
                    results["controls"].append(r)
                    if r["status"] != "survived":
                        print(f"CONTROL FAILED: {r}", flush=True)
                else:
                    results["mutants"][m["id"]] = {**m, **r}
                    done += 1
                json.dump(results, open(OUT, "w"), indent=1, sort_keys=True)
                if m and (done % 25 == 0 or r["status"] == "survived"):
                    print(f"{done}/{len(todo)} {m['id']} {m['file']}:{m['line']} {m['op']} {m['old']!r}->{m['new']!r} {r['status']}", flush=True)
    st = {}
    for r in results["mutants"].values():
        st[r["status"]] = st.get(r["status"], 0) + 1
    print("summary", st, "controls", [c["status"] for c in results["controls"]], flush=True)


if __name__ == "__main__":
    main()
