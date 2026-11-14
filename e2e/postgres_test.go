package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// These tests run proctor against a real PostgreSQL server (§11.7, DB-01 to
// DB-07). PROCTOR_E2E_POSTGRES_DSN names an administrator connection string
// for a disposable server; each test creates its own schema and role and
// drops them afterwards. Without it the tests fail, unless
// PROCTOR_E2E_SKIP_POSTGRES=1 says to skip them on purpose.
//
// A server for these tests:
//
//	docker run -d --name proctor-e2e-pg -p 127.0.0.1:55432:5432 \
//	  -e POSTGRES_PASSWORD=<choose one> postgres:17
//	export PROCTOR_E2E_POSTGRES_DSN='postgres://postgres:<it>@127.0.0.1:55432/postgres?sslmode=disable'

type pgEnv struct {
	t        *testing.T
	admin    *pgx.Conn
	adminDSN string
	schema   string
	role     string
	dsn      string // the verification role's connection string
	password string
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func newPG(t *testing.T) *pgEnv {
	t.Helper()
	dsn := os.Getenv("PROCTOR_E2E_POSTGRES_DSN")
	if dsn == "" {
		if os.Getenv("PROCTOR_E2E_SKIP_POSTGRES") == "1" {
			t.Skip("PROCTOR_E2E_SKIP_POSTGRES=1: PostgreSQL tests skipped on purpose")
		}
		t.Fatal("set PROCTOR_E2E_POSTGRES_DSN to an administrator connection string for a disposable PostgreSQL server, or PROCTOR_E2E_SKIP_POSTGRES=1 to skip these tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting as administrator: %v", err)
	}
	e := &pgEnv{t: t, admin: admin, adminDSN: dsn, schema: "e2e_" + randHex(4), role: "e2e_r_" + randHex(4), password: randHex(16)}
	cfg, _ := pgx.ParseConfig(dsn)
	e.dsn = fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", e.role, e.password, cfg.Host, cfg.Port, cfg.Database)
	e.exec(fmt.Sprintf(`CREATE SCHEMA %s`, e.schema))
	e.exec(fmt.Sprintf(`CREATE TABLE %s.items (id serial PRIMARY KEY, name text NOT NULL, price numeric(10,2), tags jsonb, active boolean, blob bytea)`, e.schema))
	e.exec(fmt.Sprintf(`CREATE TABLE %s.secrets (id int)`, e.schema))
	e.exec(fmt.Sprintf(`INSERT INTO %s.items (name, price, tags, active) VALUES ('apple', 9.50, '{"a": [1, 2]}', true), ('pear', NULL, NULL, false)`, e.schema))
	e.exec(fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s'`, e.role, e.password)) // random hex only
	e.exec(fmt.Sprintf(`GRANT USAGE ON SCHEMA %s TO %s`, e.schema, e.role))
	e.exec(fmt.Sprintf(`GRANT SELECT ON %s.items TO %s`, e.schema, e.role))
	t.Cleanup(func() {
		e.exec(fmt.Sprintf(`DROP SCHEMA %s CASCADE`, e.schema))
		e.exec(fmt.Sprintf(`DROP OWNED BY %s`, e.role))
		e.exec(fmt.Sprintf(`DROP ROLE %s`, e.role))
		admin.Close(context.Background())
	})
	return e
}

func (e *pgEnv) exec(sql string) {
	e.t.Helper()
	if _, err := e.admin.Exec(context.Background(), sql); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *pgEnv) names() []string {
	e.t.Helper()
	rows, err := e.admin.Query(context.Background(), fmt.Sprintf(`SELECT name FROM %s.items ORDER BY id`, e.schema))
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func (e *pgEnv) config(extra string) string {
	return fmt.Sprintf(`version: 1
project: example
manifest: manifest.yaml
identities:
  alice: { type: bearer }
  bob: { type: bearer }
  anonymous: { type: none }
connections:
  postgres:
    db: { schemas: [%s], objects: [items] }
%s`, e.schema, extra)
}

// dbSpec is one scenario of database checks; steps are indented as
// verification steps.
func dbSpec(steps string) string {
	return "version: 1\nsuite: s\nscenarios:\n  - id: D-1\n    name: database checks\n    steps:\n" + steps
}

func check(id, query, args, expect string) string {
	s := "      - id: " + id + "\n        verify:\n          postgres:\n            connection: db\n            query: " + query + "\n"
	if args != "" {
		s += "            args: " + args + "\n"
	}
	return s + "            expect: " + expect + "\n"
}

// run runs proctor with the verification role's DSN, or dsn when given.
func (e *pgEnv) run(t *testing.T, config, spec string, dsn string, args ...string) (int, string, string) {
	t.Helper()
	withTokens(t)
	if dsn == "" {
		dsn = e.dsn
	}
	t.Setenv("TEST_DB_DSN", dsn)
	app := newApp(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/items/") {
			io.WriteString(w, `{"id": "`+strings.TrimPrefix(r.URL.Path, "/items/")+`"}`)
			return
		}
		io.WriteString(w, `{"list": [1, 2]}`)
	})
	r := specRepo(t, config, spec)
	b := binding(t, fmt.Sprintf(`target: { base_url: %s, user_agent: t/1 }
identities:
  alice: { token_env: TEST_ALICE_TOKEN }
  bob: { token_env: TEST_BOB_TOKEN }
connections:
  postgres:
    db: { dsn_env: TEST_DB_DSN }
`, app.srv.URL))
	code, out, errOut := r.proctor(append([]string{"run", "--binding", b}, args...)...)
	if strings.Contains(out+errOut, e.password) {
		t.Fatal("the verification role's password appears in the output")
	}
	return code, out, errOut
}

// DB-01, DB-02, DB-05, DB-06: checks judge rows exactly and by type, and
// every failure of the database itself is ERROR, never FAIL or PASS.
func TestPostgresChecks(t *testing.T) {
	e := newPG(t)
	cases := []struct {
		name, steps string
		want        int
		text        []string
	}{
		{"exact row", check("q", "SELECT name, price, tags, active FROM items WHERE name = $1", `["apple"]`,
			`{ rows: 1, first_row: { name: apple, price: 9.5, tags: { a: [1, 2] }, active: true } }`), 0, nil},
		{"null values", check("q", "SELECT price, tags FROM items WHERE name = $1", `["pear"]`, `{ first_row: { price: null, tags: null } }`), 0, nil},
		{"integer argument as text", check("q", "SELECT name FROM items WHERE id = $1", `["1"]`, `{ rows: 1, first_row: { name: apple } }`), 0, nil},
		{"row count differs", check("q", "SELECT id FROM items", "", `{ rows: 3 }`), 1, []string{"expected 3 rows, got 2"}},
		{"value differs", check("q", "SELECT name FROM items WHERE id = $1", "[1]", `{ first_row: { name: pear } }`), 1, nil},
		{"type differs", check("q", "SELECT id FROM items WHERE name = $1", `["apple"]`, `{ first_row: { id: "1" } }`), 1, nil},
		{"unexpected column", check("q", "SELECT id, name FROM items WHERE name = $1", `["apple"]`, `{ first_row: { name: apple } }`), 1, []string{"first_row must name every column"}},
		{"missing column", check("q", "SELECT name FROM items WHERE name = $1", `["apple"]`, `{ first_row: { name: apple, id: 1 } }`), 1, nil},
		{"no rows for first_row", check("q", "SELECT name FROM items WHERE name = $1", `["plum"]`, `{ first_row: { name: plum } }`), 1, nil},
		{"syntax error", check("q", "SELEC name FROM items", "", `{ rows: 1 }`), 2, []string{"query_error", "42601"}},
		{"unknown table", check("q", "SELECT 1 FROM nothing_here", "", `{ rows: 1 }`), 2, []string{"query_error"}},
		{"write refused", check("q", "UPDATE items SET name = 'x' RETURNING name", "", `{ rows: 2 }`), 2, []string{"read-only transaction"}},
		{"delete refused", check("q", "DELETE FROM items RETURNING id", "", `{ rows: 2 }`), 2, nil},
		{"function with side effects refused", check("q", "SELECT nextval(pg_get_serial_sequence('items', 'id')) AS n", "", `{ rows: 1 }`), 2, nil},
		{"duplicate column names", check("q", "SELECT name, name FROM items WHERE id = 1", "", `{ rows: 1 }`), 2, []string{"two columns named"}},
		{"bytea column", check("q", `"SELECT '\\x01'::bytea AS blob"`, "", `{ rows: 1 }`), 2, []string{"bytea"}},
		{"too many rows", check("q", "SELECT g FROM generate_series(1, 10001) AS g", "", `{ rows: 10001 }`), 2, []string{"more than 10000 rows"}},
		{"injection is data", check("q", "SELECT count(*) AS n FROM items WHERE name = $1", `["'; DROP TABLE items; --"]`, `{ first_row: { n: 0 } }`), 0, nil},
		{"statement timeout", check("q", "SELECT pg_sleep(3) IS NULL AS slept", "", `{ rows: 1 }`), 2, []string{"timeout"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := e.run(t, e.config(""), dbSpec(c.steps), "", "--query-timeout", "500ms")
			expect(t, code, out, errOut, c.want, c.text...)
			if got := strings.Join(e.names(), ","); got != "apple,pear" {
				t.Fatalf("the table changed: %s", got)
			}
		})
	}
}

// DB-01, EXE-02: values move between steps only as parameters, saved from a
// single row.
func TestPostgresSaves(t *testing.T) {
	e := newPG(t)
	spec := dbSpec(check("find", "SELECT id::text AS item, (SELECT count(*) FROM items) AS n FROM items WHERE name = $1", `["apple"]`, `{ rows: 1 }`) +
		"        save: { item_id: item, n: n }\n" +
		`      - id: read
        request: { method: GET, path: "/items/${item_id}", identity: anonymous }
        expect:
          status: 200
          json:
            $.id: "${item_id}"
      - id: list
        request: { method: GET, path: /list, identity: anonymous, query: { limit: "${n}" } }
        expect:
          status: 200
          length:
            $.list: "${n}"
` + check("again", "SELECT name FROM items WHERE id = $1", `["${item_id}"]`, `{ first_row: { name: apple } }`))
	code, out, errOut := e.run(t, e.config(""), spec, "")
	expect(t, code, out, errOut, 0)

	t.Run("save needs exactly one row", func(t *testing.T) {
		bad := dbSpec(check("find", "SELECT id FROM items", "", `{ rows: 2 }`) + "        save: { x: id }\n")
		code, out, errOut := e.run(t, e.config(""), bad, "")
		expect(t, code, out, errOut, 3, "EXE-02")
	})
	t.Run("reference in the query is invalid", func(t *testing.T) {
		bad := dbSpec(check("q", "SELECT name FROM items WHERE id = ${run.id}", "", `{ rows: 1 }`))
		code, out, errOut := e.run(t, e.config(""), bad, "")
		expect(t, code, out, errOut, 3, "DB-01")
	})
	t.Run("database check in setup is invalid", func(t *testing.T) {
		bad := strings.Replace(dbSpec(check("q", "SELECT 1 AS x", "", `{ rows: 1 }`)), "    steps:\n", "    setup:\n", 1) + "    steps:\n" + check("r", "SELECT 1 AS x", "", `{ rows: 1 }`)
		code, out, errOut := e.run(t, e.config(""), bad, "")
		expect(t, code, out, errOut, 3, "FIX-03")
	})
}

// DB-03: with no schemas declared, unqualified names don't resolve.
func TestPostgresSearchPath(t *testing.T) {
	e := newPG(t)
	cfg := strings.Replace(e.config(""), fmt.Sprintf("{ schemas: [%s], objects: [items] }", e.schema), fmt.Sprintf("{ objects: [%s.items] }", e.schema), 1)
	code, out, errOut := e.run(t, cfg, dbSpec(check("q", "SELECT name FROM items WHERE id = 1", "", `{ rows: 1 }`)), "")
	expect(t, code, out, errOut, 2, "query_error")
	code, out, errOut = e.run(t, cfg, dbSpec(check("q", "SELECT name FROM "+e.schema+".items WHERE id = 1", "", `{ rows: 1 }`)), "")
	expect(t, code, out, errOut, 0)
	t.Run("unqualified object without schemas is invalid", func(t *testing.T) {
		bad := strings.Replace(e.config(""), fmt.Sprintf("{ schemas: [%s], objects: [items] }", e.schema), "{ objects: [items] }", 1)
		code, out, errOut := e.run(t, bad, dbSpec(check("q", "SELECT 1 AS x", "", `{ rows: 1 }`)), "")
		expect(t, code, out, errOut, 3, "DB-03")
	})
}

// DB-04, DB-07: a verification role that could change data, or read what
// proctor.yaml doesn't declare, stops the run before any scenario (exit 4).
func TestPostgresRoleCheck(t *testing.T) {
	e := newPG(t)
	spec := dbSpec(check("q", "SELECT name FROM items WHERE id = 1", "", `{ rows: 1 }`))
	cases := []struct{ name, grant, revoke, text string }{
		{"INSERT on a column", "GRANT INSERT (name) ON %[1]s.items TO %[2]s", "REVOKE INSERT (name) ON %[1]s.items FROM %[2]s", "holds INSERT"},
		{"UPDATE", "GRANT UPDATE ON %[1]s.items TO %[2]s", "REVOKE UPDATE ON %[1]s.items FROM %[2]s", "holds UPDATE"},
		{"DELETE", "GRANT DELETE ON %[1]s.items TO %[2]s", "REVOKE DELETE ON %[1]s.items FROM %[2]s", "holds DELETE"},
		{"TRUNCATE", "GRANT TRUNCATE ON %[1]s.items TO %[2]s", "REVOKE TRUNCATE ON %[1]s.items FROM %[2]s", "holds TRUNCATE"},
		{"TRIGGER", "GRANT TRIGGER ON %[1]s.items TO %[2]s", "REVOKE TRIGGER ON %[1]s.items FROM %[2]s", "holds TRIGGER"},
		{"undeclared table", "GRANT SELECT ON %[1]s.secrets TO %[2]s", "REVOKE SELECT ON %[1]s.secrets FROM %[2]s", "doesn't declare"},
		{"sequence", "GRANT USAGE ON SEQUENCE %[1]s.items_id_seq TO %[2]s", "REVOKE USAGE ON SEQUENCE %[1]s.items_id_seq FROM %[2]s", "sequence"},
		{"CREATE in schema", "GRANT CREATE ON SCHEMA %[1]s TO %[2]s", "REVOKE CREATE ON SCHEMA %[1]s FROM %[2]s", "can CREATE"},
		{"member of a role", "GRANT pg_read_all_data TO %[2]s", "REVOKE pg_read_all_data FROM %[2]s", "member of role pg_read_all_data"},
		{"CREATEROLE", "ALTER ROLE %[2]s CREATEROLE", "ALTER ROLE %[2]s NOCREATEROLE", "CREATEROLE"},
		{"BYPASSRLS", "ALTER ROLE %[2]s BYPASSRLS", "ALTER ROLE %[2]s NOBYPASSRLS", "BYPASSRLS"},
		{"owns an object", "CREATE TABLE %[1]s.owned (id int); ALTER TABLE %[1]s.owned OWNER TO %[2]s", "DROP TABLE %[1]s.owned", "owns"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, s := range strings.Split(fmt.Sprintf(c.grant, e.schema, e.role), "; ") {
				e.exec(s)
			}
			defer func() {
				for _, s := range strings.Split(fmt.Sprintf(c.revoke, e.schema, e.role), "; ") {
					e.exec(s)
				}
			}()
			code, out, errOut := e.run(t, e.config(""), spec, "")
			expect(t, code, out, errOut, 4, "DB-04", c.text, "trust_violation")
		})
	}
	t.Run("superuser", func(t *testing.T) {
		code, out, errOut := e.run(t, e.config(""), spec, e.adminDSN)
		expect(t, code, out, errOut, 4, "is a superuser")
	})
	t.Run("declared object missing", func(t *testing.T) {
		cfg := strings.Replace(e.config(""), "objects: [items]", "objects: [items, nothing_here]", 1)
		code, out, errOut := e.run(t, cfg, spec, "")
		expect(t, code, out, errOut, 4, "nothing_here doesn't exist")
	})
	t.Run("a clean role passes", func(t *testing.T) {
		code, out, errOut := e.run(t, e.config(""), spec, "")
		expect(t, code, out, errOut, 0)
	})
}

// FIX-07, AUTH-02: an unreachable database is environment_not_ready
// (exit 2); a missing connection string is a configuration error (exit 3);
// connection strings never appear in output.
func TestPostgresConnectionErrors(t *testing.T) {
	e := newPG(t)
	spec := dbSpec(check("q", "SELECT name FROM items WHERE id = 1", "", `{ rows: 1 }`))
	t.Run("unreachable", func(t *testing.T) {
		start := time.Now()
		code, out, errOut := e.run(t, e.config("readiness:\n  startup_timeout: 1s\n"), spec, "postgres://nobody:pw-"+e.password+"@127.0.0.1:1/x?sslmode=disable&connect_timeout=1")
		expect(t, code, out, errOut, 2, "environment_not_ready")
		if time.Since(start) > 20*time.Second {
			t.Errorf("took %s", time.Since(start))
		}
	})
	t.Run("wrong password", func(t *testing.T) {
		dsn := strings.Replace(e.dsn, e.password, "wrong-"+e.password[:8], 1)
		code, out, errOut := e.run(t, e.config("readiness:\n  startup_timeout: 1s\n"), spec, dsn)
		expect(t, code, out, errOut, 2, "environment_not_ready")
		if strings.Contains(out+errOut, "wrong-"+e.password[:8]) {
			t.Error("the connection string's password appears in the output")
		}
	})
	t.Run("connection string not set", func(t *testing.T) {
		withTokens(t)
		app := newApp(t, fixedApp(`{}`))
		r := specRepo(t, e.config(""), spec)
		b := binding(t, fmt.Sprintf("target: { base_url: %s, user_agent: t/1 }\nconnections:\n  postgres:\n    db: { dsn_env: TEST_DB_DSN_UNSET }\n", app.srv.URL))
		code, out, errOut := r.proctor("run", "--binding", b)
		expect(t, code, out, errOut, 3, "TEST_DB_DSN_UNSET is not set")
	})
	t.Run("connection not declared", func(t *testing.T) {
		bad := strings.Replace(spec, "connection: db", "connection: other", 1)
		code, out, errOut := e.run(t, e.config(""), bad, "")
		expect(t, code, out, errOut, 3, `connection "other" is not declared`)
	})
}
