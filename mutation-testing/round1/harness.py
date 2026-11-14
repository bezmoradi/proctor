#!/usr/bin/env python3
"""Mutation-testing harness for Proctor against examples/users-api-postgres.

Each application mutant is a copy of the example with one deliberate defect,
applied as exact text edits; an edit that doesn't match exactly stops the
harness, so a mutant can't silently be the original. Each specification
mutant is the hardened specification set with one assertion weakened.

For every run the harness resets the database, starts the application, logs
in, mints an expired and a tampered token, runs `proctor run --json`, and
snapshots the users table, so a PASS can be compared with the stored data.

Run from the repository root, with Docker running and the example's database
migrated (see examples/README.md):

    python3 -I mutation-testing/harness.py            # everything
    python3 -I mutation-testing/harness.py AM-01 SM-03 # selected cases

Results go to mutation-testing/results.json.
"""

import base64
import hashlib
import hmac
import json
import os
import secrets
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.request

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
APP = os.path.join(REPO, "examples", "users-api-postgres")
SPEC_DIR = os.path.join(APP, "proctor")
PORT = 18081
BASE = f"http://127.0.0.1:{PORT}"
PASSWORD = "mutation-pass-" + secrets.token_hex(4)
JWT_SECRET = secrets.token_hex(32)
ISSUER = AUDIENCE = "users-api-postgres"

REPOSITORY = "internal/adapters/repositories/postgres/repository.go"
HANDLERS = "internal/adapters/handlers/chi/users.go"
ROUTES = "internal/adapters/handlers/chi/routes.go"
AUTH = "internal/adapters/handlers/auth/auth.go"
DTO = "internal/adapters/api/user.go"
DOMAIN = "internal/core/user/domain/user.go"
MAIN = "cmd/main.go"

UPDATE_EXEC = '\tcommandTag, err := r.db.Exec(\n\t\tctx,\n\t\tfmt.Sprintf(\n\t\t\t"UPDATE'
DELETE_EXEC = '\tcommandTag, err := r.db.Exec(\n\t\tctx,\n\t\tfmt.Sprintf(\n\t\t\t"DELETE'
NO_TOKEN = "\t\t\traw, ok := bearerToken(r)\n\t\t\tif !ok {\n\t\t\t\twriteUnauthorized(w, realm)\n\t\t\t\treturn\n\t\t\t}"

MEMORY_REPO = '''package main

import (
	"context"
	"strconv"
	"sync"

	"users-api-postgres/internal/core/user/domain"
)

// memoryRepo is the mutant's stand-in for PostgreSQL: the API stays
// consistent with itself while nothing reaches the database.
type memoryRepo struct {
	mu    sync.Mutex
	next  int
	users map[string]string
}

func newMemoryRepo() (*memoryRepo, error) {
	return &memoryRepo{users: map[string]string{}}, nil
}

func (m *memoryRepo) Ping(context.Context) error { return nil }
func (m *memoryRepo) Close() error               { return nil }

func (m *memoryRepo) Insert(_ context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	id := strconv.Itoa(m.next)
	m.users[id] = name
	return id, nil
}

func (m *memoryRepo) Update(_ context.Context, id, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[id]; !ok {
		return domain.ErrUserNotFound
	}
	m.users[id] = name
	return nil
}

func (m *memoryRepo) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[id]; !ok {
		return domain.ErrUserNotFound
	}
	delete(m.users, id)
	return nil
}

func (m *memoryRepo) Fetch(_ context.Context, id string) (domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	name, ok := m.users[id]
	if !ok {
		return domain.User{}, domain.ErrUserNotFound
	}
	return domain.User{ID: id, Name: name}, nil
}

func (m *memoryRepo) FetchAll(_ context.Context, limit int, cursor string) (domain.Page, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var page domain.Page
	for id, name := range m.users {
		page.Items = append(page.Items, domain.User{ID: id, Name: name})
	}
	return page, nil
}
'''

DISCARD_WRITER = '''
type discardWriter struct{}

func (discardWriter) Header() http.Header         { return http.Header{} }
func (discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (discardWriter) WriteHeader(int)             {}
'''

# Application mutants: one defect each. "expect" is what Proctor should
# report if the specification covers the behavior.
APP_MUTANTS = [
    {"id": "AM-01", "layer": "REST+DB", "title": "Create returns 201 with an ID but inserts nothing",
     "edits": [(REPOSITORY, "\tvar insertedID int64\n\terr := r.db.QueryRow(", "\tif true {\n\t\treturn \"424242\", nil\n\t}\n\tvar insertedID int64\n\terr := r.db.QueryRow(")]},
    {"id": "AM-02", "layer": "REST+DB", "title": "Create inserts the row, then answers 500",
     "edits": [(REPOSITORY, "\treturn strconv.FormatInt(insertedID, 10), nil", "\treturn \"\", fmt.Errorf(\"mutant: failure after insert %d\", insertedID)")]},
    {"id": "AM-03", "layer": "DB", "title": "Create stores the name in upper case",
     "edits": [(REPOSITORY, "\t\tname,\n\t).Scan(&insertedID)", "\t\tstrings.ToUpper(name),\n\t).Scan(&insertedID)"),
               (REPOSITORY, '\t"strconv"\n', '\t"strconv"\n\t"strings"\n')]},
    {"id": "AM-04", "layer": "REST+DB", "title": "Update answers 200 but changes nothing",
     "edits": [(REPOSITORY, UPDATE_EXEC, "\tif true {\n\t\treturn nil\n\t}\n" + UPDATE_EXEC)]},
    {"id": "AM-05", "layer": "DB", "title": "Update renames every user (WHERE clause broken)",
     "edits": [(REPOSITORY, '"UPDATE %v SET name = $1 WHERE id = $2"', '"UPDATE %v SET name = $1 WHERE id = $2 OR TRUE"')]},
    {"id": "AM-06", "layer": "REST+DB", "title": "Delete answers 200 but deletes nothing",
     "edits": [(REPOSITORY, DELETE_EXEC, "\tif true {\n\t\treturn nil\n\t}\n" + DELETE_EXEC)]},
    {"id": "AM-07", "layer": "DB", "title": "Delete removes every user (WHERE clause broken)",
     "edits": [(REPOSITORY, '"DELETE FROM %v WHERE id = $1"', '"DELETE FROM %v WHERE id = $1 OR TRUE"')]},
    {"id": "AM-08", "layer": "REST", "title": "A nonexistent user is returned with 200 and an empty name",
     "edits": [(REPOSITORY, "\t\t\treturn domain.User{}, domain.ErrUserNotFound\n\t\t}\n\t\treturn domain.User{}, err", "\t\t\treturn domain.User{ID: id}, nil\n\t\t}\n\t\treturn domain.User{}, err")]},
    {"id": "AM-09", "layer": "REST", "title": "An empty name is accepted",
     "edits": [(DOMAIN, '\tif name == "" {\n\t\treturn ErrNameRequired', '\tif false && name == "" {\n\t\treturn ErrNameRequired')]},
    {"id": "AM-10", "layer": "REST", "title": "Create answers 200 instead of 201",
     "edits": [(HANDLERS, "\tw.WriteHeader(http.StatusCreated)", "\tw.WriteHeader(http.StatusOK)")]},
    {"id": "AM-11", "layer": "Auth", "title": "A request without a token is let through",
     "edits": [(AUTH, NO_TOKEN, "\t\t\traw, ok := bearerToken(r)\n\t\t\tif !ok {\n\t\t\t\tnext.ServeHTTP(w, r)\n\t\t\t\treturn\n\t\t\t}")]},
    {"id": "AM-12", "layer": "Auth", "title": "Expired tokens are accepted",
     "edits": [(AUTH, "\t\t\t\tjwt.WithExpirationRequired(),\n", "\t\t\t\tjwt.WithExpirationRequired(),\n\t\t\t\tjwt.WithLeeway(100000*time.Hour),\n")]},
    {"id": "AM-13", "layer": "Auth", "title": "Tokens with an invalid signature are accepted",
     "edits": [(AUTH, "\t\t\tif err != nil {\n\t\t\t\twriteUnauthorized(w, realm+", "\t\t\tif err != nil && !errors.Is(err, jwt.ErrTokenSignatureInvalid) {\n\t\t\t\twriteUnauthorized(w, realm+")]},
    {"id": "AM-14", "layer": "Auth+DB", "title": "A request without a token gets 401, but the action is still performed",
     "edits": [(AUTH, NO_TOKEN, "\t\t\traw, ok := bearerToken(r)\n\t\t\tif !ok {\n\t\t\t\tnext.ServeHTTP(discardWriter{}, r)\n\t\t\t\twriteUnauthorized(w, realm)\n\t\t\t\treturn\n\t\t\t}")],
     "append": {AUTH: DISCARD_WRITER}},
    {"id": "AM-15", "layer": "REST", "title": "Create returns the ID as a JSON number instead of a string",
     "edits": [(HANDLERS, 'Message: "Created user successfully", Data: result}', 'Message: "Created user successfully", Data: json.Number(result)}')]},
    {"id": "AM-16", "layer": "REST", "title": "Reading a user answers with Content-Type text/plain",
     "edits": [(HANDLERS, '\tw.Header().Set("Content-Type", "application/json")\n\n\tresult, err := router.service.Show(', '\tw.Header().Set("Content-Type", "text/plain")\n\n\tresult, err := router.service.Show(')]},
    {"id": "AM-17", "layer": "REST", "title": "User responses leak a password_hash field",
     "edits": [(DTO, "\tName string `json:\"name,omitempty\"`\n}", "\tName string `json:\"name,omitempty\"`\n\tPasswordHash string `json:\"password_hash\"`\n}"),
               (DTO, "return User{ID: u.ID, Name: u.Name}", "return User{ID: u.ID, Name: u.Name, PasswordHash: \"pbkdf2$leaked\"}")]},
    {"id": "AM-18", "layer": "REST+DB", "title": "Data is kept in memory; nothing reaches PostgreSQL (the API agrees with itself)",
     "edits": [(MAIN, "db, err := postgres.New()", "db, err := newMemoryRepo()"),
               (MAIN, '\t"users-api-postgres/internal/adapters/repositories/postgres"\n', '\t_ "users-api-postgres/internal/adapters/repositories/postgres"\n')],
     "files": {"cmd/memrepo.go": MEMORY_REPO}},
    {"id": "AM-19", "layer": "REST+DB", "title": "Create inserts nothing and returns ID 1, which an old row already uses",
     "edits": [(REPOSITORY, "\tvar insertedID int64\n\terr := r.db.QueryRow(", "\tif true {\n\t\treturn \"1\", nil\n\t}\n\tvar insertedID int64\n\terr := r.db.QueryRow(")],
     "seed": ["Ada"]},
    {"id": "AM-20", "layer": "REST", "title": "The list ignores limit and returns up to 1000 users",
     "edits": [(REPOSITORY, "\tif limit < 1 {\n\t\tlimit = 1\n\t}", "\tlimit = 1000")]},
    {"id": "AM-21", "layer": "REST", "title": "PATCH is routed and answers 200 instead of 405",
     "edits": [(ROUTES, '\t\tr.Delete("/{id}", router.DeleteUserHandler)', '\t\tr.Delete("/{id}", router.DeleteUserHandler)\n\t\tr.Patch("/{id}", router.ShowUserHandler)')]},
    {"id": "AM-22", "layer": "DB", "title": "Every create inserts the row twice",
     "edits": [(REPOSITORY, "\tvar insertedID int64\n\terr := r.db.QueryRow(", "\tif _, err := r.db.Exec(ctx, \"INSERT INTO users (name) VALUES ($1)\", name); err != nil {\n\t\treturn \"\", err\n\t}\n\tvar insertedID int64\n\terr := r.db.QueryRow(")]},
    {"id": "AM-23", "layer": "REST", "title": "Update persists the new name but answers with a stale one",
     "edits": [(HANDLERS, 'Message: "User updated successfully", Data: api.NewUser(user)}', 'Message: "User updated successfully", Data: api.User{ID: user.ID, Name: "stale"}}')]},
    {"id": "AM-24", "layer": "Environment", "title": "PostgreSQL stops after the application started",
     "edits": [], "fault": "db-down"},
]


def drop_scenarios(text, ids):
    head, *blocks = text.split("\n  - id: ")
    keep = [b for b in blocks if b.split("\n", 1)[0].strip() not in ids]
    return "\n  - id: ".join([head] + keep)


def replace_once(text, old, new):
    if text.count(old) != 1:
        raise SystemExit(f"specification edit matched {text.count(old)} times: {old!r}")
    return text.replace(old, new)


HARDENED = open(os.path.join(SPEC_DIR, "specs", "users.yaml")).read()

# The baseline is the set a first draft would plausibly have: the CRUD
# round trips and one authentication check, without the cross-record,
# token and header checks the hardened set adds.
BASELINE = replace_once(
    drop_scenarios(HARDENED, {"PGU-006", "PGU-007", "PGU-009", "PGU-010", "PGU-011", "PGU-012"}),
    "          headers:\n            Content-Type: application/json\n", "")

# Specification mutants: one weakening each, paired with the application
# mutant it would let through.
SPEC_MUTANTS = [
    {"id": "SM-01", "title": "PGU-001 asserts only the ID on read-back, not the name", "pair": "AM-03",
     "spec": lambda s: replace_once(s, "            $.data.id: \"${user_id}\"\n            $.data.name: Ada\n", "            $.data.id: \"${user_id}\"\n")},
    {"id": "SM-02", "title": "PGU-004 drops the read after the rename", "pair": "AM-04",
     "spec": lambda s: replace_once(s, "            $.data.name: Grace\n      - id: read\n        request: { method: GET, path: \"/api/v1/users/${user_id}\", identity: admin }\n        expect:\n          status: 200\n          json:\n            $.data.name: Grace\n", "            $.data.name: Grace\n")},
    {"id": "SM-03", "title": "PGU-011 drops the check that the user still exists", "pair": "AM-14",
     "spec": lambda s: replace_once(s, "        expect: { status: 401 }\n      - id: still-there\n        request: { method: GET, path: \"/api/v1/users/${user_id}\", identity: admin }\n        expect:\n          status: 200\n          json:\n            $.data.name: Ada\n", "        expect: { status: 401 }\n")},
    {"id": "SM-04", "title": "PGU-006 (other users unchanged by a rename) removed", "pair": "AM-05",
     "spec": lambda s: drop_scenarios(s, {"PGU-006"})},
    {"id": "SM-05", "title": "Content-Type assertion removed from PGU-001", "pair": "AM-16",
     "spec": lambda s: replace_once(s, "          headers:\n            Content-Type: application/json\n", "")},
    {"id": "SM-06", "title": "PGU-005's deletion moved to a verification step that checks only the response message", "pair": "AM-06",
     "spec": lambda s: replace_once(s, "      - id: delete\n        request: { method: DELETE, path: \"/api/v1/users/${user_id}\", identity: admin }\n        expect: { status: 200 }\n    steps:\n      - id: read-deleted\n        request: { method: GET, path: \"/api/v1/users/${user_id}\", identity: admin }\n        expect: { status: 404 }\n",
                                     "    steps:\n      - id: delete\n        request: { method: DELETE, path: \"/api/v1/users/${user_id}\", identity: admin }\n        expect:\n          status: 200\n          json:\n            $.message: \"deleted user with ID of ${user_id} successfully\"\n")},
    {"id": "SM-07", "title": "A 2xx verification step keeps only its status (PGU-001 read)", "pair": None,
     "spec": lambda s: replace_once(s, "          headers:\n            Content-Type: application/json\n          json:\n            $.data.id: \"${user_id}\"\n            $.data.name: Ada\n", "")},
    {"id": "SM-08", "title": "PGU-009 and PGU-010 (invalid tokens) removed", "pair": "AM-12",
     "spec": lambda s: drop_scenarios(s, {"PGU-009", "PGU-010"})},
    # Hardening probe, not a weakening: does a whole-object assertion catch
    # an extra response field with today's runner?
    {"id": "HP-01", "title": "Probe: PGU-001 asserts the whole $.data object instead of two fields", "pair": "AM-17",
     "spec": lambda s: replace_once(s, "            $.data.id: \"${user_id}\"\n            $.data.name: Ada\n", "            $.data: { id: \"${user_id}\", name: Ada }\n")},
]


def sh(args, cwd=None, env=None, check=True, timeout=600):
    p = subprocess.run(args, cwd=cwd, env=env, capture_output=True, text=True, timeout=timeout)
    if check and p.returncode != 0:
        raise SystemExit(f"{' '.join(args)} failed ({p.returncode}):\n{p.stdout}\n{p.stderr}")
    return p


def psql(sql):
    return sh(["docker", "compose", "exec", "-T", "psql", "psql", "-U", "app", "-d", "test", "-At", "-c", sql], cwd=APP).stdout.strip()


def http(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.loads(resp.read())


def b64(b):
    return base64.urlsafe_b64encode(b).rstrip(b"=").decode()


def expired_token():
    now = int(time.time())
    head = b64(json.dumps({"alg": "HS256", "typ": "JWT"}).encode())
    claims = b64(json.dumps({"sub": "admin", "iss": ISSUER, "aud": [AUDIENCE], "iat": now - 7200, "exp": now - 3600}).encode())
    sig = hmac.new(JWT_SECRET.encode(), f"{head}.{claims}".encode(), hashlib.sha256).digest()
    return f"{head}.{claims}.{b64(sig)}"


def tampered(token):
    head, claims, sig = token.split(".")
    i = len(sig) // 2
    return f"{head}.{claims}.{sig[:i]}{'B' if sig[i] == 'A' else 'A'}{sig[i + 1:]}"


def build_app(work, mutant):
    dst = os.path.join(work, "apps", mutant["id"] if mutant else "original")
    if os.path.exists(os.path.join(dst, "bin")):
        return dst
    shutil.copytree(APP, dst, ignore=shutil.ignore_patterns("proctor"), dirs_exist_ok=True)
    for rel, old, new in (mutant or {}).get("edits", []):
        p = os.path.join(dst, rel)
        text = open(p).read()
        if text.count(old) != 1:
            raise SystemExit(f"{mutant['id']}: edit in {rel} matched {text.count(old)} times: {old!r}")
        open(p, "w").write(text.replace(old, new))
    for rel, extra in (mutant or {}).get("append", {}).items():
        with open(os.path.join(dst, rel), "a") as f:
            f.write(extra)
    for rel, content in (mutant or {}).get("files", {}).items():
        open(os.path.join(dst, rel), "w").write(content)
    sh(["go", "build", "-o", "bin", "./cmd"], cwd=dst)
    return dst


def password_hash(work):
    """Hash a test password once per work directory, caching both."""
    global PASSWORD
    p = os.path.join(work, "login.json")
    if not os.path.exists(p):
        proc = subprocess.run(["go", "run", "./cmd/hashpassword"], cwd=APP, input=PASSWORD + "\n", capture_output=True, text=True, timeout=600)
        if proc.returncode != 0:
            raise SystemExit(proc.stderr)
        json.dump({"password": PASSWORD, "hash": proc.stdout.strip().splitlines()[-1]}, open(p, "w"))
        os.chmod(p, 0o600)
    saved = json.load(open(p))
    PASSWORD = saved["password"]
    return saved["hash"]


def spec_repo(work, label, users_yaml):
    d = os.path.join(work, "specs", label)
    shutil.rmtree(d, ignore_errors=True)
    os.makedirs(os.path.join(d, "proctor", "specs"))
    for name in ("proctor.yaml", "manifest.yaml"):
        shutil.copy(os.path.join(SPEC_DIR, name), os.path.join(d, "proctor", name))
    open(os.path.join(d, "proctor", "specs", "users.yaml"), "w").write(users_yaml)
    git = ["git", "-c", "user.email=t@example.com", "-c", "user.name=T", "-c", "commit.gpgsign=false"]
    sh(["git", "init", "-q", "-b", "main"], cwd=d)
    sh(git + ["add", "-A"], cwd=d)
    sh(git + ["commit", "-q", "-m", label], cwd=d)
    return d


def wait_db_healthy():
    for _ in range(120):
        if sh(["docker", "compose", "exec", "-T", "psql", "pg_isready", "-U", "app", "-d", "test"], cwd=APP, check=False).returncode == 0:
            return
        time.sleep(0.5)
    raise SystemExit("PostgreSQL did not come back")


def run_case(work, proctor, mutant, spec_label, users_yaml):
    app = build_app(work, mutant)
    psql("TRUNCATE users RESTART IDENTITY")
    for name in (mutant or {}).get("seed", []):
        psql(f"INSERT INTO users (name) VALUES ('{name}')")  # fixed literals from this file only
    env = dict(os.environ, PORT=str(PORT), AUTH_PASSWORD_HASH=password_hash(work), JWT_SECRET=JWT_SECRET)
    log = open(os.path.join(work, f"app-{(mutant or {}).get('id', 'original')}-{spec_label}.log"), "w")
    proc = subprocess.Popen([os.path.join(app, "bin")], cwd=app, env=env, stdout=log, stderr=log)
    try:
        for _ in range(100):
            try:
                urllib.request.urlopen(BASE + "/healthz", timeout=1)
                break
            except Exception:
                time.sleep(0.1)
        token = http("POST", "/login", {"username": "admin", "password": PASSWORD})["token"]
        fault = (mutant or {}).get("fault")
        if fault == "db-down":
            sh(["docker", "compose", "stop", "psql"], cwd=APP)
        repo = spec_repo(work, spec_label + "-" + (mutant or {}).get("id", "original"), users_yaml)
        penv = dict(os.environ, PROCTOR_ADMIN_TOKEN=token, PROCTOR_EXPIRED_TOKEN=expired_token(), PROCTOR_TAMPERED_TOKEN=tampered(token))
        res = sh([proctor, "run", "--repo", repo, "--binding", os.path.join(SPEC_DIR, "environments", "local.yaml"), "--json"],
                 env=penv, check=False, timeout=300)
        if fault == "db-down":
            sh(["docker", "compose", "start", "psql"], cwd=APP)
            wait_db_healthy()
    finally:
        proc.terminate()
        proc.wait(timeout=10)
        log.close()
    try:
        report = json.loads(res.stdout)
    except json.JSONDecodeError:
        report = {"result": "unparsed", "exit_code": res.returncode, "stdout": res.stdout[-2000:], "stderr": res.stderr[-2000:]}
    rows = psql("SELECT id || ':' || name FROM users ORDER BY id")
    scen = {s["id"]: s["result"] for s in report.get("scenarios", [])}
    return {"exit_code": res.returncode, "result": report.get("result"),
            "not_passed": sorted(k for k, v in scen.items() if v != "PASS"), "scenarios": scen,
            "db_rows": rows.splitlines() if rows else []}


def diff_check(work, proctor, label, users_yaml):
    """Commit the hardened set, then the mutated one, and ask proctor diff."""
    d = spec_repo(work, "diff-" + label, HARDENED)
    open(os.path.join(d, "proctor", "specs", "users.yaml"), "w").write(users_yaml)
    git = ["git", "-c", "user.email=t@example.com", "-c", "user.name=T", "-c", "commit.gpgsign=false"]
    sh(git + ["commit", "-q", "-am", "weaken"], cwd=d)
    base = sh(["git", "rev-parse", "HEAD~1"], cwd=d).stdout.strip()
    head = sh(["git", "rev-parse", "HEAD"], cwd=d).stdout.strip()
    r = sh([proctor, "diff", "--repo", d, "--base", base, "--head", head, "--json"], check=False)
    v = sh([proctor, "validate", "--repo", d, "--json"], check=False)
    try:
        out = json.loads(r.stdout)
    except json.JSONDecodeError:
        out = {}
    return {"diff_exit": r.returncode, "protected_changes": out.get("protected_changes"),
            "scenarios_changed": [f"{s.get('change')} {s.get('id')}" for s in (out.get("diff") or {}).get("scenarios") or []],
            "validate_exit": v.returncode}


def main():
    wanted = set(sys.argv[1:])
    work = os.environ.get("MT_WORK") or tempfile.mkdtemp(prefix="proctor-mt-")
    os.makedirs(work, exist_ok=True)
    proctor = os.path.join(work, "proctor")
    sh(["go", "build", "-o", proctor, "./cmd/proctor"], cwd=REPO)
    by_id = {m["id"]: m for m in APP_MUTANTS}
    out_path = os.path.join(REPO, "mutation-testing", "results.json")
    results = json.load(open(out_path)) if wanted and os.path.exists(out_path) else {"controls": {}, "app": {}, "spec": {}}
    results.update({"proctor_commit_note": "uncommitted working tree", "date": time.strftime("%Y-%m-%d")})

    def log(msg):
        # Save after every case, so a crash later doesn't lose earlier results.
        json.dump(results, open(out_path, "w"), indent=2, sort_keys=True)
        print(msg, flush=True)

    if not wanted or "CONTROL" in wanted:
        for label, text in (("hardened", HARDENED), ("baseline", BASELINE)):
            r = run_case(work, proctor, None, label, text)
            results["controls"][label] = r
            log(f"control  original app / {label:8s}: exit {r['exit_code']} {r['not_passed']}")
    for m in APP_MUTANTS:
        if wanted and m["id"] not in wanted:
            continue
        entry = {"title": m["title"], "layer": m["layer"]}
        for label, text in (("hardened", HARDENED), ("baseline", BASELINE)):
            entry[label] = run_case(work, proctor, m, label, text)
        results["app"][m["id"]] = entry
        log(f"{m['id']}  hardened exit {entry['hardened']['exit_code']} {entry['hardened']['not_passed']}"
            f"  | baseline exit {entry['baseline']['exit_code']} {entry['baseline']['not_passed']}"
            f"  | rows {len(entry['hardened']['db_rows'])}")
    for s in SPEC_MUTANTS:
        if wanted and s["id"] not in wanted:
            continue
        text = s["spec"](HARDENED)
        entry = {"title": s["title"], "pair": s["pair"], **diff_check(work, proctor, s["id"], text)}
        if entry["validate_exit"] == 0:
            entry["original_app"] = run_case(work, proctor, None, s["id"], text)
            if s["pair"]:
                entry["paired_mutant"] = run_case(work, proctor, by_id[s["pair"]], s["id"], text)
        results["spec"][s["id"]] = entry
        log(f"{s['id']}  validate exit {entry['validate_exit']}  diff: {entry['protected_changes']} protected, {entry['scenarios_changed']}"
            + (f"  | original exit {entry['original_app']['exit_code']}" if "original_app" in entry else "")
            + (f"  | {s['pair']} exit {entry['paired_mutant']['exit_code']}" if "paired_mutant" in entry else ""))
    json.dump(results, open(out_path, "w"), indent=2, sort_keys=True)
    log(f"results: {out_path}\nwork dir: {work}")


if __name__ == "__main__":
    main()
