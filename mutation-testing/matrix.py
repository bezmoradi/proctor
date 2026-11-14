#!/usr/bin/env python3
"""Run Proctor against the gopamin combination matrix in examples/matrix/.

For each combination: start its services with Docker Compose (ports moved
where they would clash), apply its migrations, build and start the
application (and, per mutant, a copy with one defect), run `proctor run
--json` with the combination's own specification set, and record the result.

    python3 -I mutation-testing/matrix.py probe m01-echo-mysql   # print live responses
    python3 -I mutation-testing/matrix.py run [combination ...]   # original + mutants

Results go to mutation-testing/matrix-results.json (MT_MATRIX_OUT overrides).
Connection strings and passwords from each .env are passed to processes as
environment variables and never printed.
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
import urllib.error
import urllib.request

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MATRIX = os.path.join(REPO, "examples", "matrix")
WORK = os.environ.get("MT_MATRIX_WORK") or os.path.join(tempfile.gettempdir(), "proctor-matrix")
OUT = os.environ.get("MT_MATRIX_OUT") or os.path.join(REPO, "mutation-testing", "matrix-results.json")
PASSWORD = "matrix-" + secrets.token_hex(6)
JWT_SECRET = secrets.token_hex(32)
READER_PASSWORD = secrets.token_hex(16)

# Per combination: the app port, environment overrides (ports moved off
# the defaults another project uses), and what the combination has.
COMBOS = {
    "m01-echo-mysql": {"port": 18101, "migrate": True},
    "m02-gin-mongodb": {"port": 18102},
    "m03-gorilla-sqlite": {"port": 18103, "compose": False, "env": {"SQLITE_DB_URL": "./matrix.db"}},
    "m04-httprouter-mariadb": {"port": 18104, "migrate": True},
    "m05-graphql-postgres": {"port": 18105, "migrate": True, "env": {"POSTGRES_PORT": "15432"}, "postgres": True},
    "m06-chi-cassandra-rabbitmq": {"port": 18106, "jwt": True},
    "m07-echo-postgres-redis": {"port": 18107, "migrate": True, "env": {"POSTGRES_PORT": "15433"}, "postgres": True, "jwt": True},
    "m08-http-dynamodb-kafka": {"port": 18108},
    "m09-webapp-gin-badgerdb": {"port": 18109, "compose": False},
    "m10-worker-rabbitmq-postgres": {"port": None, "migrate": True, "env": {"POSTGRES_PORT": "15434"}, "postgres": True},
}

SERVICE = "internal/core/user/services/user.service.go"
DOMAIN = "internal/core/user/domain/user.go"
DTO = "internal/adapters/api/user.go"

# Defects that every generated project shares, at the service, domain and
# transport layers. XM-06 is visible only in the database.
MUTANTS = [
    {"id": "XM-01", "title": "Create answers with an ID but stores nothing",
     "edits": [(SERVICE, "\tid, err := us.repo.Insert(ctx, name)", "\tid, err := \"999999\", error(nil)")]},
    {"id": "XM-02", "title": "Update answers with the new name but stores nothing",
     "edits": [(SERVICE, "\tif err := us.repo.Update(ctx, id, newName); err != nil {", "\tif err := error(nil); err != nil {")]},
    {"id": "XM-03", "title": "Delete answers success but deletes nothing",
     "edits": [(SERVICE, "us.repo.Delete(ctx, id)", "error(nil)")]},
    {"id": "XM-04", "title": "An empty name is accepted",
     "edits": [(DOMAIN, '\tif name == "" {\n\t\treturn ErrNameRequired', '\tif false && name == "" {\n\t\treturn ErrNameRequired')]},
    {"id": "XM-05", "title": "User responses leak a password_hash field",
     "edits": [(DTO, "\tName string `json:\"name,omitempty\"`\n}", "\tName string `json:\"name,omitempty\"`\n\tPasswordHash string `json:\"password_hash\"`\n}"),
               (DTO, "return User{ID: u.ID, Name: u.Name}", "return User{ID: u.ID, Name: u.Name, PasswordHash: \"pbkdf2$leaked\"}")]},
    {"id": "XM-06", "title": "Every create stores the row twice (the API stays consistent)",
     "edits": [(SERVICE, "\tid, err := us.repo.Insert(ctx, name)", "\t_, _ = us.repo.Insert(ctx, name)\n\tid, err := us.repo.Insert(ctx, name)")]},
]


def sh(args, cwd=None, env=None, check=True, timeout=900, input_text=None):
    p = subprocess.run(args, cwd=cwd, env=env, capture_output=True, text=True, timeout=timeout, input=input_text)
    if check and p.returncode != 0:
        raise SystemExit(f"{' '.join(args[:4])} failed ({p.returncode}):\n{p.stdout[-1500:]}\n{p.stderr[-1500:]}")
    return p


def dotenv(d):
    out = {}
    for line in open(os.path.join(MATRIX, d, ".env")):
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            k, v = line.split("=", 1)
            out[k] = v.strip().strip('"')
    return out


def env_for(name):
    c = COMBOS[name]
    e = dotenv(name)
    e.update(c.get("env", {}))
    if c["port"]:
        e["PORT"] = str(c["port"])
    if c.get("jwt"):
        e["JWT_SECRET"] = JWT_SECRET
        e["AUTH_PASSWORD_HASH"] = password_hash(name)
    return e


def password_hash(name):
    d = os.path.join(WORK, name)
    os.makedirs(d, exist_ok=True)
    p = os.path.join(d, "login.json")
    if not os.path.exists(p):
        proc = subprocess.run(["go", "run", "./cmd/hashpassword"], cwd=os.path.join(MATRIX, name), input=PASSWORD + "\n",
                              capture_output=True, text=True, timeout=900)
        if proc.returncode != 0:
            raise SystemExit(proc.stderr[-1000:])
        json.dump({"password": PASSWORD, "hash": proc.stdout.strip().splitlines()[-1]}, open(p, "w"))
        os.chmod(p, 0o600)
    saved = json.load(open(p))
    globals()["PASSWORD"] = saved["password"]
    return saved["hash"]


def services_up(name):
    c = COMBOS[name]
    if c.get("compose", True):
        sh(["docker", "compose", "up", "-d", "--wait"], cwd=os.path.join(MATRIX, name), env=dict(os.environ, **env_for(name)), timeout=900)
    if c.get("migrate"):
        # Some images pass their healthcheck while still initializing (MySQL
        # answers ping before it accepts logins), so retry for a minute.
        for attempt in range(30):
            # The Makefile includes .env, which overrides the environment, so
            # the moved ports go on the command line. They hold no secrets.
            overrides = [f"{k}={v}" for k, v in sorted(c.get("env", {}).items())]
            p = sh(["make", "migrate-up"] + overrides, cwd=os.path.join(MATRIX, name), env=dict(os.environ, **env_for(name)), timeout=900, check=False)
            if p.returncode == 0:
                break
            time.sleep(2)
        else:
            raise SystemExit(f"{name}: migrations failed:\n{p.stdout[-800:]}\n{p.stderr[-800:]}")
    if c.get("postgres"):
        create_reader(name)


def services_down(name):
    if COMBOS[name].get("compose", True):
        sh(["docker", "compose", "down", "-v"], cwd=os.path.join(MATRIX, name), env=dict(os.environ, **env_for(name)), check=False)


def psql(name, sql, variables=None):
    e = env_for(name)
    args = ["docker", "compose", "exec", "-T", service_name(name, "postgres"),
            "psql", "-U", e["POSTGRES_USERNAME"], "-d", e["POSTGRES_DATABASE"], "-v", "ON_ERROR_STOP=1", "-At"]
    for k, v in (variables or {}).items():
        args += ["-v", f"{k}={v}"]
    return sh(args, cwd=os.path.join(MATRIX, name), env=dict(os.environ, **e), input_text=sql).stdout.strip()


def service_name(name, image):
    out = sh(["docker", "compose", "config", "--services"], cwd=os.path.join(MATRIX, name), env=dict(os.environ, **env_for(name))).stdout.split()
    for s in out:
        if image in s or s in ("psql", "postgres", "db"):
            return s
    return out[0]


def create_reader(name):
    e = env_for(name)
    exists = psql(name, "SELECT 1 FROM pg_roles WHERE rolname = 'proctor_reader'")
    sql = ("DROP OWNED BY proctor_reader; DROP ROLE proctor_reader;" if exists else "") + \
        f"CREATE ROLE proctor_reader LOGIN PASSWORD :'pw'; GRANT CONNECT ON DATABASE {e['POSTGRES_DATABASE']} TO proctor_reader; " \
        "GRANT USAGE ON SCHEMA public TO proctor_reader; GRANT SELECT ON public.users TO proctor_reader;"
    psql(name, sql, {"pw": READER_PASSWORD})


def reader_dsn(name):
    e = env_for(name)
    return f"postgres://proctor_reader:{READER_PASSWORD}@127.0.0.1:{e['POSTGRES_PORT']}/{e['POSTGRES_DATABASE']}?sslmode=disable"


def build(name, mutant):
    label = mutant["id"] if mutant else "original"
    dst = os.path.join(WORK, name, "apps", label)
    if os.path.exists(os.path.join(dst, "bin")):
        return dst
    shutil.rmtree(dst, ignore_errors=True)
    shutil.copytree(os.path.join(MATRIX, name), dst, ignore=shutil.ignore_patterns("proctor", "*.db", "data"))
    for rel, old, new in (mutant or {}).get("edits", []):
        p = os.path.join(dst, rel)
        if not os.path.exists(p):
            return None  # this combination has no such file
        text = open(p).read()
        if text.count(old) != 1:
            return None  # the defect's code doesn't exist in this combination
        open(p, "w").write(text.replace(old, new))
    sh(["go", "build", "-o", "bin", "./cmd"], cwd=dst)
    return dst


def b64(b):
    return base64.urlsafe_b64encode(b).rstrip(b"=").decode()


def sign(header, claims):
    h, c = b64(json.dumps(header).encode()), b64(json.dumps(claims).encode())
    return f"{h}.{c}.{b64(hmac.new(JWT_SECRET.encode(), f'{h}.{c}'.encode(), hashlib.sha256).digest())}"


def start(name, app_dir):
    c = COMBOS[name]
    e = env_for(name)
    if c["port"]:
        # A leftover process on the port would answer instead of this build,
        # and every result would silently describe the wrong application.
        import socket
        with socket.socket() as so:
            if so.connect_ex(("127.0.0.1", c["port"])) == 0:
                raise SystemExit(f"{name}: port {c['port']} is already in use; stop the process holding it")
    log = open(os.path.join(WORK, name, f"app-{os.path.basename(app_dir)}.log"), "w")
    proc = subprocess.Popen([os.path.join(app_dir, "bin")], cwd=app_dir, env=dict(os.environ, **e), stdout=log, stderr=log)
    if c["port"]:
        for _ in range(300):
            try:
                urllib.request.urlopen(f"http://127.0.0.1:{c['port']}/healthz", timeout=1)
                break
            except Exception:
                if proc.poll() is not None:
                    break
                time.sleep(0.1)
        if proc.poll() is not None:
            log.close()
            raise SystemExit(f"{name}: {os.path.basename(app_dir)} exited at startup:\n" + open(log.name).read()[-800:])
    return proc, log


def request(name, method, path, body=None, token=None, headers=None, raw=None):
    data = raw if raw is not None else (json.dumps(body).encode() if body is not None else None)
    r = urllib.request.Request(f"http://127.0.0.1:{COMBOS[name]['port']}{path}", data=data, method=method)
    if body is not None:
        r.add_header("Content-Type", "application/json")
    if token:
        r.add_header("Authorization", "Bearer " + token)
    for k, v in (headers or {}).items():
        r.add_header(k, v)
    try:
        with urllib.request.urlopen(r, timeout=10) as x:
            return x.status, dict(x.headers), x.read().decode(errors="replace")
    except urllib.error.HTTPError as err:
        return err.code, dict(err.headers), err.read().decode(errors="replace")


def login(name):
    if not COMBOS[name].get("jwt"):
        return None
    st, _, b = request(name, "POST", "/login", {"username": env_for(name).get("AUTH_USERNAME", "admin"), "password": PASSWORD})
    return json.loads(b)["token"] if st == 200 else None


def probe(name):
    os.makedirs(os.path.join(WORK, name), exist_ok=True)
    services_up(name)
    app = build(name, None)
    proc, log = start(name, app)
    try:
        tok = login(name)
        if COMBOS[name]["port"] is None:
            time.sleep(3)
            print("no HTTP server; app log:\n" + open(log.name).read()[-1500:])
            return
        cases = [("GET", "/healthz", None), ("GET", "/readyz", None), ("POST", "/api/v1/users", {"name": "Ada"}), ("GET", "/api/v1/users?limit=2", None),
                 ("POST", "/api/v1/users", {"name": ""}), ("GET", "/api/v1/users/2000000000", None), ("GET", "/api/v1/users/ffffffffffffffffffffffff", None),
                 ("GET", "/api/v1/users/00000000-0000-4000-8000-000000000000", None), ("PATCH", "/api/v1/users/1", {"name": "Z"}),
                 ("POST", "/graphql", {"query": "mutation { create(name: \"Ada\") }"}), ("POST", "/graphql", {"query": "{ users(limit: 2) { items { id name } next_cursor } }"}),
                 ("GET", "/users", None), ("GET", "/users/1", None)]
        for m, p, b in cases:
            st, h, body = request(name, m, p, b, tok)
            print(m, p, b or "", "->", st, h.get("Content-Type"), body.strip().replace("\n", " ")[:300])
            if m == "POST" and p == "/api/v1/users" and st == 201:
                created = json.loads(body).get("data")
                for m2, p2, b2 in [("GET", f"/api/v1/users/{created}", None), ("PUT", f"/api/v1/users/{created}", {"name": "Grace"}),
                                   ("DELETE", f"/api/v1/users/{created}", None), ("GET", f"/api/v1/users/{created}", None)]:
                    st, h, body = request(name, m2, p2, b2, tok)
                    print("  ", m2, p2, b2 or "", "->", st, body.strip()[:300])
        if COMBOS[name].get("jwt"):
            st, h, body = request(name, "GET", "/api/v1/users")
            print("anonymous list ->", st, h.get("Www-Authenticate"), body.strip())
    finally:
        proc.terminate()
        proc.wait(timeout=10)
        log.close()


def run_case(name, mutant, proctor):
    app = build(name, mutant)
    if app is None:
        return {"status": "not_applicable"}
    c = COMBOS[name]
    if c.get("postgres"):
        psql(name, "TRUNCATE users RESTART IDENTITY")
    proc, log = start(name, app)
    try:
        if c["port"] is None:
            return {"status": "not_runnable", "why": "no HTTP server and no broker adapter in Proctor"}
        tok = login(name)
        penv = dict(os.environ)
        if tok:
            now = int(time.time())
            e = env_for(name)
            claims = {"sub": e.get("AUTH_USERNAME", "admin"), "iss": e["JWT_ISSUER"], "aud": [e["JWT_AUDIENCE"]], "iat": now - 7200, "exp": now - 3600}
            penv.update(PROCTOR_ADMIN_TOKEN=tok, PROCTOR_ADMIN_EXPIRED_TOKEN=sign({"alg": "HS256", "typ": "JWT"}, claims))
        if c.get("postgres"):
            penv["PROCTOR_APP_DB_DSN"] = reader_dsn(name)
        repo = spec_repo(name)
        res = sh([proctor, "run", "--repo", repo, "--binding", os.path.join(MATRIX, name, "proctor", "environments", "local.yaml"), "--json"],
                 env=penv, check=False, timeout=600)
    finally:
        proc.terminate()
        proc.wait(timeout=10)
        log.close()
    open(os.path.join(WORK, name, f"report-{(mutant or {}).get('id', 'original')}.json"), "w").write(res.stdout)
    for s in [PASSWORD, READER_PASSWORD, JWT_SECRET] + ([tok] if tok else []):
        if s in res.stdout or s in res.stderr:
            raise SystemExit(f"a credential appeared in Proctor's output for {name}")
    try:
        rep = json.loads(res.stdout)
    except json.JSONDecodeError:
        return {"status": "unparsed", "exit_code": res.returncode, "stdout": res.stdout[-800:]}
    return {"status": "ran", "exit_code": res.returncode, "result": rep.get("result"),
            "not_passed": sorted(s["id"] for s in rep.get("scenarios", []) if s["result"] != "PASS"), "scenarios": len(rep.get("scenarios", []))}


def spec_repo(name):
    d = os.path.join(WORK, name, "specrepo")
    shutil.rmtree(d, ignore_errors=True)
    shutil.copytree(os.path.join(MATRIX, name, "proctor"), os.path.join(d, "proctor"))
    git = ["git", "-c", "user.email=t@example.com", "-c", "user.name=T", "-c", "commit.gpgsign=false"]
    sh(["git", "init", "-q", "-b", "main"], cwd=d)
    sh(git + ["add", "-A"], cwd=d)
    sh(git + ["commit", "-q", "-m", "specs"], cwd=d)
    return d


def main():
    mode, names = sys.argv[1], sys.argv[2:] or list(COMBOS)
    os.makedirs(WORK, exist_ok=True)
    if mode == "probe":
        for n in names:
            print(f"== {n}")
            try:
                probe(n)
            finally:
                services_down(n)
        return
    proctor = os.path.join(WORK, "proctor")
    sh(["go", "build", "-o", proctor, "./cmd/proctor"], cwd=REPO)
    results = json.load(open(OUT)) if os.path.exists(OUT) else {}
    for n in names:
        os.makedirs(os.path.join(WORK, n), exist_ok=True)
        entry = {}
        try:
            services_up(n)
            v = sh([proctor, "validate", "--repo", spec_repo(n), "--json"], check=False)
            entry["validate_exit"] = v.returncode
            entry["original"] = run_case(n, None, proctor)
            print(f"{n} original: {entry['original']}", flush=True)
            for m in MUTANTS:
                entry[m["id"]] = run_case(n, m, proctor)
                print(f"{n} {m['id']}: {entry[m['id']]}", flush=True)
        finally:
            services_down(n)
        results[n] = entry
        json.dump(results, open(OUT, "w"), indent=2, sort_keys=True)


if __name__ == "__main__":
    main()
