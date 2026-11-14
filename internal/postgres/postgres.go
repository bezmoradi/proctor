// Package postgres is Proctor's read-only PostgreSQL verification adapter
// (prd.md §11.7, DB-01 to DB-07).
//
// Every query is sent with driver parameter binding (DB-01) inside its own
// transaction opened READ ONLY at READ COMMITTED, with a transaction-local
// statement_timeout and a client-side deadline (DB-02). Results are read in
// PostgreSQL's text format and converted by column type, so numbers keep
// their exact value and nothing depends on Go's type mapping.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// MaxRows bounds how many rows one check reads; more is an error, so a
// check can't silently judge a truncated result.
const MaxRows = 10000

// Conn is one verification connection.
type Conn struct {
	name string
	conn *pgx.Conn
}

// ErrorClass says why a database operation failed, for the report's cause.
type ErrorClass string

const (
	Unreachable ErrorClass = "unreachable"
	Timeout     ErrorClass = "timeout"
	QueryError  ErrorClass = "query_error"
	BadResult   ErrorClass = "unsupported_result"
)

// Error is a database failure with its class. Messages never include the
// connection string.
type Error struct {
	Class    ErrorClass
	SQLState string
	Msg      string
}

func (e *Error) Error() string {
	if e.SQLState != "" {
		return fmt.Sprintf("%s (SQLSTATE %s)", e.Msg, e.SQLState)
	}
	return e.Msg
}

func classify(ctx context.Context, err error, what string) *Error {
	var pe *pgconn.PgError
	switch {
	case errors.As(err, &pe):
		cls := QueryError
		if pe.Code == "57014" { // query_canceled: statement_timeout
			cls = Timeout
		}
		return &Error{Class: cls, SQLState: pe.Code, Msg: what + ": " + pe.Message}
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil:
		return &Error{Class: Timeout, Msg: what + ": timed out"}
	case pgconn.Timeout(err):
		return &Error{Class: Timeout, Msg: what + ": timed out"}
	}
	return &Error{Class: Unreachable, Msg: what + ": " + scrub(err.Error())}
}

// scrub removes anything that looks like a password assignment from a
// driver message, in case a future driver version echoes the DSN.
func scrub(s string) string {
	out := []string{}
	for _, f := range strings.Fields(s) {
		if strings.HasPrefix(strings.ToLower(strings.Trim(f, "`'\"(")), "password=") {
			f = "password=[redacted]"
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

// Open connects with the given connection string and pins the session:
// read-only by default and search_path set to schemas, or to nothing when
// none are declared, so unqualified names fail (DB-03).
func Open(ctx context.Context, name, dsn string, schemas []string, timeout time.Duration) (*Conn, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// pgx's parse errors can quote the string; never show it.
		return nil, &Error{Class: Unreachable, Msg: fmt.Sprintf("connection %s: the connection string doesn't parse", name)}
	}
	cfg.ConnectTimeout = timeout
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["application_name"] = "proctor"
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, classify(ctx, err, "connection "+name)
	}
	quoted := make([]string, len(schemas))
	for i, s := range schemas {
		quoted[i] = pgx.Identifier{s}.Sanitize()
	}
	if _, err := c.Exec(ctx, "SELECT set_config('search_path', $1, false)", strings.Join(quoted, ", ")); err != nil {
		c.Close(context.Background())
		return nil, classify(ctx, err, "connection "+name+": setting search_path")
	}
	return &Conn{name: name, conn: c}, nil
}

// Close ends the session.
func (c *Conn) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.conn.Close(ctx)
}

// Ping runs SELECT 1 as the verification role (FIX-07).
func (c *Conn) Ping(ctx context.Context, timeout time.Duration) error {
	_, _, err := c.Query(ctx, "SELECT 1", nil, timeout)
	return err
}

// Query runs one query in its own read-only transaction and returns the
// column names and rows, each value converted to a JSON-comparable form:
// integers and numerics as json.Number, booleans as bool, json and jsonb
// decoded, NULL as nil, and everything else as PostgreSQL's text form.
func (c *Conn) Query(ctx context.Context, sql string, args []any, timeout time.Duration) ([]string, [][]any, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout+time.Second)
	defer cancel()
	tx, err := c.conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, nil, classify(ctx, err, "connection "+c.name+": begin")
	}
	defer func() {
		rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer rcancel()
		_ = tx.Rollback(rctx)
	}()
	if _, err := tx.Exec(ctx, "SELECT set_config('statement_timeout', $1, true)", fmt.Sprintf("%dms", timeout.Milliseconds())); err != nil {
		return nil, nil, classify(ctx, err, "connection "+c.name+": setting statement_timeout")
	}
	all := append([]any{pgx.QueryResultFormats{pgx.TextFormatCode}}, args...)
	rows, err := tx.Query(ctx, sql, all...)
	if err != nil {
		return nil, nil, classify(ctx, err, "query")
	}
	defer rows.Close()
	fds := rows.FieldDescriptions()
	cols := make([]string, len(fds))
	for i, fd := range fds {
		cols[i] = fd.Name
	}
	var out [][]any
	for rows.Next() {
		if len(out) == MaxRows {
			return nil, nil, &Error{Class: BadResult, Msg: fmt.Sprintf("query returned more than %d rows", MaxRows)}
		}
		raw := rows.RawValues()
		row := make([]any, len(raw))
		for i, b := range raw {
			v, err := convert(fds[i].DataTypeOID, b)
			if err != nil {
				return nil, nil, &Error{Class: BadResult, Msg: fmt.Sprintf("column %s: %v", cols[i], err)}
			}
			row[i] = v
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, classify(ctx, err, "query")
	}
	return cols, out, nil
}

// PostgreSQL type OIDs Proctor converts specially.
const (
	oidBool    = 16
	oidInt8    = 20
	oidInt2    = 21
	oidInt4    = 23
	oidOID     = 26
	oidJSON    = 114
	oidFloat4  = 700
	oidFloat8  = 701
	oidNumeric = 1700
	oidJSONB   = 3802
	oidBytea   = 17
)

func convert(oid uint32, b []byte) (any, error) {
	if b == nil {
		return nil, nil
	}
	s := string(b)
	switch oid {
	case oidBool:
		switch s {
		case "t":
			return true, nil
		case "f":
			return false, nil
		}
		return nil, fmt.Errorf("unexpected boolean %q", s)
	case oidInt2, oidInt4, oidInt8, oidOID:
		return json.Number(s), nil
	case oidFloat4, oidFloat8, oidNumeric:
		switch s {
		case "NaN", "Infinity", "-Infinity":
			return s, nil // not JSON numbers; compared as text
		}
		return json.Number(s), nil
	case oidJSON, oidJSONB:
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		return v, nil
	case oidBytea:
		return nil, errors.New("bytea can't be compared; select encode(column, 'hex') instead")
	}
	return s, nil
}

// RoleProblems checks the session's role against DB-04 and the declared
// objects against DB-07, and returns every problem found. objects are the
// tables and views proctor.yaml declares for this connection.
func (c *Conn) RoleProblems(ctx context.Context, objects []string, timeout time.Duration) ([]string, error) {
	var problems []string
	q := func(sql string, args ...any) ([][]any, error) {
		_, rows, err := c.Query(ctx, sql, args, timeout)
		return rows, err
	}

	rows, err := q(`SELECT session_user = current_user, r.rolsuper, r.rolcreaterole, r.rolbypassrls, r.rolcreatedb, r.rolreplication
		FROM pg_roles r WHERE r.rolname = current_user`)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, &Error{Class: BadResult, Msg: "can't read the current role from pg_roles"}
	}
	r := rows[0]
	for i, what := range []string{"", "is a superuser", "has CREATEROLE", "has BYPASSRLS", "has CREATEDB", "has REPLICATION"} {
		if i == 0 {
			if r[0] != true {
				problems = append(problems, "the session switched roles (session_user differs from current_user)")
			}
			continue
		}
		if r[i] == true {
			problems = append(problems, "the role "+what)
		}
	}

	checks := []struct {
		sql  string
		what string
	}{
		{`SELECT g.rolname FROM pg_auth_members m JOIN pg_roles g ON g.oid = m.roleid
			WHERE m.member = (SELECT oid FROM pg_roles WHERE rolname = current_user) ORDER BY 1`, "is a member of role %s"},
		{`SELECT n.nspname || '.' || c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relowner = (SELECT oid FROM pg_roles WHERE rolname = current_user) ORDER BY 1`, "owns %s"},
		{`SELECT nspname FROM pg_namespace WHERE nspowner = (SELECT oid FROM pg_roles WHERE rolname = current_user) ORDER BY 1`, "owns schema %s"},
		{`SELECT p.oid::regprocedure::text FROM pg_proc p WHERE p.proowner = (SELECT oid FROM pg_roles WHERE rolname = current_user) ORDER BY 1`, "owns function %s"},
		{`SELECT datname FROM pg_database WHERE datdba = (SELECT oid FROM pg_roles WHERE rolname = current_user) ORDER BY 1`, "owns database %s"},
		{`SELECT nspname FROM pg_namespace
			WHERE nspname NOT IN ('pg_catalog', 'information_schema') AND nspname NOT LIKE 'pg\_toast%' AND nspname NOT LIKE 'pg\_temp%'
			AND has_schema_privilege(oid, 'CREATE') ORDER BY 1`, "can CREATE in schema %s"},
	}
	for _, ch := range checks {
		rows, err := q(ch.sql)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			problems = append(problems, "the role "+fmt.Sprintf(ch.what, row[0]))
		}
	}

	// Table privileges, including column-level grants, and sequences.
	rows, err = q(`SELECT n.nspname || '.' || c.relname, c.relkind::text,
			has_any_column_privilege(c.oid, 'SELECT'),
			has_any_column_privilege(c.oid, 'INSERT'),
			has_any_column_privilege(c.oid, 'UPDATE'),
			has_table_privilege(c.oid, 'DELETE'),
			has_table_privilege(c.oid, 'TRUNCATE'),
			has_any_column_privilege(c.oid, 'REFERENCES'),
			has_table_privilege(c.oid, 'TRIGGER')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f')
		  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp%'
		ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	declared, err := c.resolve(ctx, objects, timeout)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, name := range objects {
		o := declared[name]
		switch {
		case o.kind == "":
			problems = append(problems, fmt.Sprintf("declared object %s doesn't exist", name))
		case !strings.Contains("rpvmf", o.kind):
			problems = append(problems, fmt.Sprintf("declared object %s is not a table or view (relkind %s)", name, o.kind))
		default:
			allowed[o.full] = true
		}
	}
	for _, row := range rows {
		name := row[0].(string)
		for i, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"} {
			if row[2+i] != true {
				continue
			}
			if priv == "SELECT" && allowed[name] {
				continue
			}
			if priv == "SELECT" {
				problems = append(problems, fmt.Sprintf("the role can read %s, which proctor.yaml doesn't declare (DB-07)", name))
			} else {
				problems = append(problems, fmt.Sprintf("the role holds %s on %s", priv, name))
			}
		}
	}
	rows, err = q(`SELECT n.nspname || '.' || c.relname, has_sequence_privilege(c.oid, 'USAGE'), has_sequence_privilege(c.oid, 'UPDATE')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'S' AND n.nspname NOT IN ('pg_catalog', 'information_schema') ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row[1] == true {
			problems = append(problems, fmt.Sprintf("the role holds USAGE on sequence %s", row[0]))
		}
		if row[2] == true {
			problems = append(problems, fmt.Sprintf("the role holds UPDATE on sequence %s", row[0]))
		}
	}
	return problems, nil
}

type object struct{ full, kind string }

// resolve finds each declared object's schema-qualified name and relkind
// through the session's search_path; kind is "" when it doesn't resolve.
func (c *Conn) resolve(ctx context.Context, objects []string, timeout time.Duration) (map[string]object, error) {
	out := map[string]object{}
	for _, o := range objects {
		_, rows, err := c.Query(ctx, `SELECT n.nspname || '.' || c.relname, c.relkind::text
			FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.oid = to_regclass($1)`, []any{o}, timeout)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			out[o] = object{}
			continue
		}
		out[o] = object{full: rows[0][0].(string), kind: rows[0][1].(string)}
	}
	return out, nil
}
