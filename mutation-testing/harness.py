#!/usr/bin/env python3
"""Mutation-testing harness for Proctor against examples/users-api-postgres.

Each application mutant is a copy of the example with one deliberate defect,
applied as exact text edits; an edit that doesn't match exactly stops the
harness, so a mutant can't silently be the original. Each specification
mutant is the hardened specification set with one assertion weakened.

For every run the harness resets the database, starts the application, logs
in, mints the invalid credential variants (expired, bad signature, wrong
audience, wrong issuer, unsigned, malformed), runs `proctor run --json` with a
read-only verification role, and snapshots the users table, so a PASS can be
compared with the stored data. Database mutants weaken a check or the
verification role itself.

Run from the repository root, with Docker running and the example's database
migrated (see examples/README.md):

    python3 -I mutation-testing/harness.py                 # everything
    python3 -I mutation-testing/harness.py AM-01 SM-03      # selected cases
    MT_OUT=/tmp/r1.json python3 -I mutation-testing/harness.py  # another file

Results go to mutation-testing/results-round2.json unless MT_OUT names a file. Each
case records its required result and whether it was met.
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
    {"id": "AM-25", "layer": "REST+DB", "title": "Create stores the row before validating, so a refused empty name is still inserted",
     "edits": [("internal/core/user/services/user.service.go",
                "\tif err := domain.ValidateName(name); err != nil {\n\t\treturn \"\", err\n\t}\n\n\tid, err := us.repo.Insert(ctx, name)\n\tif err != nil {\n\t\treturn \"\", err\n\t}",
                "\tid, err := us.repo.Insert(ctx, name)\n\tif err != nil {\n\t\treturn \"\", err\n\t}\n\tif err := domain.ValidateName(name); err != nil {\n\t\treturn \"\", err\n\t}")]},
    {"id": "AM-26", "layer": "Auth", "title": "Tokens for another audience are accepted",
     "edits": [(AUTH, "\t\t\t\tjwt.WithAudience(cfg.audience),\n", "")]},
    {"id": "AM-27", "layer": "Auth", "title": "Tokens from another issuer are accepted",
     "edits": [(AUTH, "\t\t\t\tjwt.WithIssuer(cfg.issuer),\n", "")]},
    {"id": "AM-28", "layer": "Auth", "title": "Unsigned tokens (alg none) are accepted",
     "edits": [(AUTH, "\t\t\t\tif _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {", "\t\t\t\tif token.Method == jwt.SigningMethodNone {\n\t\t\t\t\treturn jwt.UnsafeAllowNoneSignatureType, nil\n\t\t\t\t}\n\t\t\t\tif _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {"),
               (AUTH, "\t\t\t\tjwt.WithValidMethods([]string{signingMethod}),\n", "")]},
]



def sh(args, cwd=None, env=None, check=True, timeout=600, input_text=None):
    p = subprocess.run(args, cwd=cwd, env=env, capture_output=True, text=True, timeout=timeout, input=input_text)
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


def sign(header, claims):
    head = b64(json.dumps(header).encode())
    body = b64(json.dumps(claims).encode())
    sig = hmac.new(JWT_SECRET.encode(), f"{head}.{body}".encode(), hashlib.sha256).digest()
    return f"{head}.{body}.{b64(sig)}"


def claims(**over):
    now = int(time.time())
    c = {"sub": "admin", "iss": ISSUER, "aud": [AUDIENCE], "iat": now - 60, "exp": now + 3600}
    c.update(over)
    return c


def variants(token):
    """Invalid credential variants of the admin identity (AUTH-06)."""
    now = int(time.time())
    head, body, sig = token.split(".")
    i = len(sig) // 2
    bad_sig = f"{head}.{body}.{sig[:i]}{'B' if sig[i] == 'A' else 'A'}{sig[i + 1:]}"
    unsigned = b64(json.dumps({"alg": "none", "typ": "JWT"}).encode()) + "." + b64(json.dumps(claims()).encode()) + "."
    hs = {"alg": "HS256", "typ": "JWT"}
    return {
        "PROCTOR_ADMIN_EXPIRED_TOKEN": sign(hs, claims(iat=now - 7200, exp=now - 3600)),
        "PROCTOR_ADMIN_NO_EXPIRY_TOKEN": sign(hs, {k: v for k, v in claims().items() if k != "exp"}),
        "PROCTOR_ADMIN_BAD_SIGNATURE_TOKEN": bad_sig,
        "PROCTOR_ADMIN_WRONG_AUDIENCE_TOKEN": sign(hs, claims(aud=["another-api"])),
        "PROCTOR_ADMIN_WRONG_ISSUER_TOKEN": sign(hs, claims(iss="another-issuer")),
        "PROCTOR_ADMIN_UNSIGNED_TOKEN": unsigned,
        "PROCTOR_ADMIN_MALFORMED_TOKEN": "not-a-jwt",
        # An accepted variant: the same valid token, scheme in lower case.
        "PROCTOR_ADMIN_LOWERCASE_AUTH": "bearer " + token,
    }


READER_PASSWORD = secrets.token_hex(16)


def reader_dsn():
    port = env_value("POSTGRES_PORT")
    return f"postgres://proctor_reader:{READER_PASSWORD}@127.0.0.1:{port}/test?sslmode=disable"


def env_value(key):
    """Read one value from the example's .env without printing it."""
    for line in open(os.path.join(APP, ".env")):
        if line.startswith(key + "="):
            return line.split("=", 1)[1].strip().strip('"')
    raise SystemExit(f"{key} missing from the example's .env")


def create_reader():
    """The verification role: SELECT on users only (DB-04, DB-07). The
    password is random per harness run and passed to psql as a variable."""
    sql = ("DROP OWNED BY proctor_reader; DROP ROLE proctor_reader;" if psql("SELECT 1 FROM pg_roles WHERE rolname = 'proctor_reader'") else "") + \
          "CREATE ROLE proctor_reader LOGIN PASSWORD :'pw'; GRANT CONNECT ON DATABASE test TO proctor_reader; " \
          "GRANT USAGE ON SCHEMA public TO proctor_reader; GRANT SELECT ON public.users TO proctor_reader;"
    sh(["docker", "compose", "exec", "-T", "psql", "psql", "-U", "app", "-d", "test", "-v", "ON_ERROR_STOP=1", "-v", "pw=" + READER_PASSWORD],
       cwd=APP, input_text=sql)


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


def drop_scenarios(text, ids):
    head, *blocks = text.split("\n  - id: ")
    keep = [b for b in blocks if b.split("\n", 1)[0].strip() not in ids]
    return "\n  - id: ".join([head] + keep)


def replace_once(text, old, new):
    if text.count(old) != 1:
        raise SystemExit(f"specification edit matched {text.count(old)} times: {old!r}")
    return text.replace(old, new)


SPEC = open(os.path.join(SPEC_DIR, "specs", "users.yaml")).read()

PGU001_READ_BODY = '            $: { message: Fetched user successfully, data: { id: "${user_id}", name: "Ada ${run.id}" } }\n\n  - id: PGU-002'
PGU001_STORED = ('            query: SELECT id::text AS id, name FROM users WHERE id = $1\n            args: ["${user_id}"]\n'
                 '            expect:\n              rows: 1\n              first_row: { id: "${user_id}", name: "Ada ${run.id}" }\n')
PGU004_CHECKS = ('      - id: stored\n        verify:\n          postgres:\n            connection: app\n'
                 '            query: SELECT id::text AS id, name FROM users WHERE id = $1\n            args: ["${user_id}"]\n'
                 '            expect:\n              rows: 1\n              first_row: { id: "${user_id}", name: "Grace ${run.id}" }\n'
                 '      - id: read\n        request: { method: GET, path: "/api/v1/users/${user_id}", identity: admin }\n'
                 '        expect:\n          status: 200\n          json:\n'
                 '            $: { message: Fetched user successfully, data: { id: "${user_id}", name: "Grace ${run.id}" } }\n')
PGU011_CHECKS = ('      - id: still-stored\n        verify:\n          postgres:\n            connection: app\n'
                 '            query: SELECT name FROM users WHERE id = $1\n            args: ["${user_id}"]\n'
                 '            expect:\n              rows: 1\n              first_row: { name: "Ada ${run.id}" }\n'
                 '      - id: still-there\n        request: { method: GET, path: "/api/v1/users/${user_id}", identity: admin }\n'
                 '        expect:\n          status: 200\n          json:\n'
                 '            $: { message: Fetched user successfully, data: { id: "${user_id}", name: "Ada ${run.id}" } }\n\n  - id: PGU-012')
PGU005_DELETE = ('    steps:\n      - id: delete\n        request: { method: DELETE, path: "/api/v1/users/${user_id}", identity: admin }\n'
                 '        expect:\n          status: 200\n          json:\n            $: { message: "deleted user with ID of ${user_id} successfully" }\n'
                 '      - id: gone-from-database')
PGU016_CHECK = ('            $: { error: unauthorized }\n      - id: nothing-stored\n        verify:\n          postgres:\n            connection: app\n'
                '            query: SELECT count(*) AS n FROM users WHERE name = $1\n            args: ["Eve ${run.id}"]\n'
                '            expect:\n              first_row: { n: 0 }\n')
PGU018_ASSERTS = ('          keys:\n            $: [message, data, next_cursor]\n            $.data[0]: [id, name]\n'
                  '          length:\n            $.data: 1\n          json:\n            $.message: Fetched users successfully\n')
READ_CT = '          headers:\n            Content-Type: application/json\n          json:\n            $: { message: Fetched user successfully'

# Specification mutants: one weakening each. "required" is what Proctor must
# do: reject it at validation (exit 3), or accept it and show it to the
# reviewer as a weakening in `proctor diff`. "pair" is the application
# mutant the weakening might let through; letting it through is recorded,
# not required either way, since a reviewer who approves a weakening owns it.
SPEC_MUTANTS = [
    {"id": "SM-01", "title": "PGU-001's read asserts only $.data.id", "pair": "AM-03", "required": "weakening",
     "spec": lambda s: replace_once(s, PGU001_READ_BODY, '            $.data.id: "${user_id}"\n\n  - id: PGU-002')},
    {"id": "SM-02", "title": "PGU-004 drops every check after the rename", "pair": None, "required": "invalid",
     "spec": lambda s: replace_once(s, PGU004_CHECKS, "")},
    {"id": "SM-03", "title": "PGU-011 drops the checks that the user still exists", "pair": None, "required": "invalid",
     "spec": lambda s: replace_once(s, PGU011_CHECKS, "\n  - id: PGU-012")},
    {"id": "SM-04", "title": "PGU-006 (other users unchanged by a rename) removed", "pair": "AM-05", "required": "weakening",
     "spec": lambda s: drop_scenarios(s, {"PGU-006"})},
    {"id": "SM-05", "title": "Content-Type assertion removed from PGU-001's read", "pair": "AM-16", "required": "weakening",
     "spec": lambda s: replace_once(s, READ_CT, '          json:\n            $: { message: Fetched user successfully')},
    {"id": "SM-06", "title": "PGU-005's delete moved into setup", "pair": "AM-06", "required": "weakening",
     "spec": lambda s: replace_once(s, PGU005_DELETE, '      - id: delete\n        request: { method: DELETE, path: "/api/v1/users/${user_id}", identity: admin }\n'
                                                       '        expect: { status: 200 }\n    steps:\n      - id: gone-from-database')},
    {"id": "SM-07", "title": "A 2xx verification step keeps only its status (PGU-018)", "pair": None, "required": "invalid",
     "spec": lambda s: replace_once(s, PGU018_ASSERTS, "")},
    {"id": "SM-08", "title": "PGU-009 and PGU-010 (expired and badly signed tokens) removed", "pair": "AM-12", "required": "weakening",
     "spec": lambda s: drop_scenarios(s, {"PGU-009", "PGU-010"})},
    {"id": "SM-09", "title": "PGU-016's database check replaced by a no_state_check waiver", "pair": "AM-14", "required": "weakening",
     "spec": lambda s: replace_once(s, PGU016_CHECK, "            $: { error: unauthorized }\n        no_state_check: assumed harmless\n")},
    {"id": "SM-10", "title": "PGU-018 keeps only the message", "pair": "AM-20", "required": "weakening",
     "spec": lambda s: replace_once(s, PGU018_ASSERTS, "          json:\n            $.message: Fetched users successfully\n")},
    # REQ-37: a value without ${run.id} is invalid, not a warning.
    {"id": "SM-11", "title": "PGU-001 drops ${run.id} from the name it creates and checks", "pair": None, "required": "invalid",
     "spec": lambda s: in_scenario(s, "PGU-001", lambda b: b.replace("Ada ${run.id}", "Ada"))},
    {"id": "SM-12", "title": "SM-11 with a FIX-05 waiver", "pair": None, "required": "weakening",
     "spec": lambda s: in_scenario(s, "PGU-001", lambda b: b.replace("Ada ${run.id}", "Ada").replace(
         'body: { name: "Ada" } }\n', 'body: { name: "Ada" } }\n        waive: { FIX-05: assumed no other run creates Ada }\n', 1))},
]


def in_scenario(s, sid, f):
    """Apply f to one scenario's text only."""
    start = s.index(f"  - id: {sid}\n")
    end = s.find("\n  - id: ", start + 1)
    end = len(s) if end < 0 else end
    out = s[:start] + f(s[start:end]) + s[end:]
    if out == s:
        raise SystemExit(f"{sid}: the mutation changed nothing")
    return out

# Database mutants: a broken or hostile database check, or a verification
# role with more rights than DB-04 allows. "required" is an exit code, plus
# "unchanged" when the users table must be left as the application wrote it.
DB_MUTANTS = [
    {"id": "DB-A", "title": "A database check's query has a syntax error", "required": 2,
     "spec": lambda s: replace_once(s, PGU001_STORED, PGU001_STORED.replace("SELECT id::text", "SELEC id::text"))},
    {"id": "DB-B", "title": "A database check's query writes (UPDATE ... RETURNING)", "required": 2, "unchanged": True,
     "spec": lambda s: replace_once(s, PGU001_STORED, PGU001_STORED.replace("SELECT id::text AS id, name FROM users WHERE id = $1",
                                                                          "UPDATE users SET name = 'mutant' WHERE id = $1 RETURNING id::text AS id, name"))},
    {"id": "DB-C", "title": "A database check's argument carries SQL: '; DROP TABLE users; --", "required": 1, "unchanged": True,
     "spec": lambda s: replace_once(s, 'args: ["Ada ${run.id}"]', "args: [\"'; DROP TABLE users; --\"]")},
    {"id": "DB-D", "title": "A database check puts ${...} inside the query text", "required": 3,
     "spec": lambda s: replace_once(s, PGU001_STORED, PGU001_STORED.replace("WHERE id = $1", "WHERE id = ${user_id}"))},
    {"id": "DB-E", "title": "The verification role holds INSERT on users (column-level)", "required": 4,
     "grant": "GRANT INSERT (name) ON users TO proctor_reader", "revoke": "REVOKE INSERT (name) ON users FROM proctor_reader"},
    {"id": "DB-F", "title": "The verification role can read a table proctor.yaml doesn't declare", "required": 4,
     "grant": "GRANT SELECT ON schema_migrations TO proctor_reader", "revoke": "REVOKE SELECT ON schema_migrations FROM proctor_reader"},
    {"id": "DB-G", "title": "The verification role is a member of pg_read_all_data", "required": 4,
     "grant": "GRANT pg_read_all_data TO proctor_reader", "revoke": "REVOKE pg_read_all_data FROM proctor_reader"},
    {"id": "DB-H", "title": "The verification role can use the users ID sequence", "required": 4,
     "grant": "GRANT USAGE ON SEQUENCE users_id_seq TO proctor_reader", "revoke": "REVOKE USAGE ON SEQUENCE users_id_seq FROM proctor_reader"},
    {"id": "DB-I", "title": "Proctor connects as the application's own role (a superuser that owns the table)", "required": 4,
     "dsn": "owner"},
]

# Held-out mutants, written by an independent agent that never saw the
# specifications (MT_HELDOUT names their JSON file). Each must FAIL, or be
# reported as a gap with its cause.
# MT_HELDOUT lists one or more such files, separated by commas.
HELDOUT = [m for f in os.environ.get("MT_HELDOUT", "").split(",") if f for m in json.load(open(f))]
APP_MUTANTS = APP_MUTANTS + HELDOUT

# Required result per application mutant: FAIL (exit 1), except a database
# outage, which must be ERROR (exit 2) and never PASS.
REQUIRED_APP = {m["id"]: (2 if m.get("fault") else 1) for m in APP_MUTANTS}


def run_case(work, proctor, mutant, spec_label, users_yaml, db=None):
    app = build_app(work, mutant)
    psql("TRUNCATE users RESTART IDENTITY")
    for name in (mutant or {}).get("seed", []):
        psql(f"INSERT INTO users (name) VALUES ('{name}')")  # fixed literals from this file only
    env = dict(os.environ, PORT=str(PORT), AUTH_PASSWORD_HASH=password_hash(work), JWT_SECRET=JWT_SECRET)
    label = (mutant or {}).get("id", "original")
    log = open(os.path.join(work, f"app-{label}-{spec_label}.log"), "w")
    proc = subprocess.Popen([os.path.join(app, "bin")], cwd=app, env=env, stdout=log, stderr=log)
    granted = False
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
        if db and db.get("grant"):
            psql(db["grant"])
            granted = True
        repo = spec_repo(work, spec_label + "-" + label, users_yaml)
        dsn = reader_dsn()
        if db and db.get("dsn") == "owner":
            dsn = f"postgres://app:{env_value('POSTGRES_PASSWORD')}@127.0.0.1:{env_value('POSTGRES_PORT')}/test?sslmode=disable"
        penv = dict(os.environ, PROCTOR_ADMIN_TOKEN=token, PROCTOR_APP_DB_DSN=dsn, PROCTOR_ADMIN_PASSWORD=PASSWORD, **variants(token))
        res = sh([proctor, "run", "--repo", repo, "--binding", os.path.join(SPEC_DIR, "environments", "local.yaml"), "--json"],
                 env=penv, check=False, timeout=300)
        if fault == "db-down":
            sh(["docker", "compose", "start", "psql"], cwd=APP)
            wait_db_healthy()
    finally:
        if granted:
            psql(db["revoke"])
        proc.terminate()
        proc.wait(timeout=10)
        log.close()
    open(os.path.join(work, f"report-{label}-{spec_label}.json"), "w").write(res.stdout)
    try:
        report = json.loads(res.stdout)
    except json.JSONDecodeError:
        report = {"result": "unparsed"}
    for secret in [token, READER_PASSWORD, PASSWORD, env_value("POSTGRES_PASSWORD")] + [v.split(" ")[-1] for v in variants(token).values()]:
        if secret and len(secret) > 8 and (secret in res.stdout or secret in res.stderr):
            raise SystemExit(f"a credential appeared in Proctor's output for {label}/{spec_label}")
    rows = psql("SELECT id || ':' || name FROM users ORDER BY id")
    scen = {s["id"]: s["result"] for s in report.get("scenarios", [])}
    causes = {s["id"]: s.get("cause") or s.get("reason") for s in report.get("scenarios", []) if s["result"] != "PASS"}
    return {"exit_code": res.returncode, "result": report.get("result"),
            "not_passed": sorted(k for k, v in scen.items() if v != "PASS"), "causes": causes,
            "violations": report.get("violations") or [],
            "scenarios": scen, "db_rows": rows.splitlines() if rows else [],
            "stderr": res.stderr[-500:] if res.returncode == 3 else ""}


def diff_check(work, proctor, label, users_yaml):
    """Commit the specification set, then the mutated one, and ask
    `proctor diff` and `proctor validate`."""
    d = spec_repo(work, "diff-" + label, SPEC)
    open(os.path.join(d, "proctor", "specs", "users.yaml"), "w").write(users_yaml)
    git = ["git", "-c", "user.email=t@example.com", "-c", "user.name=T", "-c", "commit.gpgsign=false"]
    sh(git + ["commit", "-q", "-am", "weaken"], cwd=d)
    base = sh(["git", "rev-parse", "HEAD~1"], cwd=d).stdout.strip()
    head = sh(["git", "rev-parse", "HEAD"], cwd=d).stdout.strip()
    r = sh([proctor, "diff", "--repo", d, "--base", base, "--head", head, "--json"], check=False)
    v = sh([proctor, "validate", "--repo", d, "--json"], check=False)
    out = json.loads(r.stdout) if r.stdout.strip().startswith("{") else {}
    val = json.loads(v.stdout) if v.stdout.strip().startswith("{") else {}
    diff = out.get("diff") or {}
    return {"diff_exit": r.returncode, "protected_changes": out.get("protected_changes"),
            "weakenings": [f"{w['kind']} {w['scenario']} {w.get('step', '')}".strip() for w in diff.get("weakenings") or []],
            "validate_exit": v.returncode,
            "validate_rules": sorted({f["rule"] for f in (val.get("report") or {}).get("findings") or [] if f["kind"] == 3})}


def main():
    wanted = set(sys.argv[1:])
    work = os.environ.get("MT_WORK") or tempfile.mkdtemp(prefix="proctor-mt-")
    os.makedirs(work, exist_ok=True)
    proctor = os.path.join(work, "proctor")
    sh(["go", "build", "-o", proctor, "./cmd/proctor"], cwd=REPO)
    create_reader()
    by_id = {m["id"]: m for m in APP_MUTANTS}
    out_path = os.environ.get("MT_OUT") or os.path.join(REPO, "mutation-testing", "results-round2.json")
    results = json.load(open(out_path)) if wanted and os.path.exists(out_path) else {"controls": {}, "app": {}, "spec": {}, "db": {}}
    results.update({"proctor_commit_note": "uncommitted working tree", "date": time.strftime("%Y-%m-%d"), "round": 2})

    def log(msg):
        # Save after every case, so a crash later doesn't lose earlier results.
        json.dump(results, open(out_path, "w"), indent=2, sort_keys=True)
        print(msg, flush=True)

    if not wanted or "CONTROL" in wanted:
        r = run_case(work, proctor, None, "spec", SPEC)
        r["required"], r["met"] = 0, r["exit_code"] == 0
        results["controls"]["spec"] = r
        log(f"control  original app: exit {r['exit_code']} {r['not_passed']}  met={r['met']}")
    for m in APP_MUTANTS:
        if wanted and m["id"] not in wanted:
            continue
        r = run_case(work, proctor, m, "spec", SPEC)
        r.update({"title": m["title"], "layer": m["layer"], "required": REQUIRED_APP[m["id"]]})
        r["met"] = r["exit_code"] == r["required"]
        results["app"][m["id"]] = r
        log(f"{m['id']}  exit {r['exit_code']} {r['not_passed']}  rows {len(r['db_rows'])}  met={r['met']}")
    for s in SPEC_MUTANTS:
        if wanted and s["id"] not in wanted:
            continue
        text = s["spec"](SPEC)
        e = {"title": s["title"], "pair": s["pair"], "required": s["required"], **diff_check(work, proctor, s["id"], text)}
        if s["required"] == "invalid":
            e["met"] = e["validate_exit"] == 3
        else:
            e["met"] = e["validate_exit"] == 0 and len(e["weakenings"]) > 0
        if e["validate_exit"] == 0:
            e["original_app"] = run_case(work, proctor, None, s["id"], text)
            e["met"] = e["met"] and e["original_app"]["exit_code"] == 0
            if s["pair"]:
                e["paired_mutant"] = run_case(work, proctor, by_id[s["pair"]], s["id"], text)
        results["spec"][s["id"]] = e
        log(f"{s['id']}  validate {e['validate_exit']} {e['validate_rules']}  weakenings {e['weakenings']}"
            + (f"  | original exit {e['original_app']['exit_code']}" if "original_app" in e else "")
            + (f"  | {s['pair']} exit {e['paired_mutant']['exit_code']}" if "paired_mutant" in e else "")
            + f"  met={e['met']}")
    for d in DB_MUTANTS:
        if wanted and d["id"] not in wanted:
            continue
        text = d["spec"](SPEC) if "spec" in d else SPEC
        r = run_case(work, proctor, None, d["id"], text, db=d)
        r.update({"title": d["title"], "required": d["required"]})
        ok = r["exit_code"] == d["required"]
        if d.get("unchanged"):
            # The original app writes only names with a run ID; any other
            # name, or a missing table, means the check changed data.
            ok = ok and len(r["db_rows"]) > 0 and all(" " in row for row in r["db_rows"]) and not any(row.endswith(":mutant") for row in r["db_rows"])
        r["met"] = ok
        results["db"][d["id"]] = r
        log(f"{d['id']}  exit {r['exit_code']} {r['not_passed']}  violations {len(r['violations'])}  rows {len(r['db_rows'])}  met={ok}")
    json.dump(results, open(out_path, "w"), indent=2, sort_keys=True)
    unmet = [k for sec in ("controls", "app", "spec", "db") for k, v in results[sec].items() if not v.get("met")]
    log(f"results: {out_path}\nwork dir: {work}\nunmet: {unmet}")


if __name__ == "__main__":
    main()
