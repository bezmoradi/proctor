package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"proctor/protocol"
)

func (c *conn) roleProblems(ctx context.Context) ([]string, error) {
	var problems []string
	q := func(sql string, args ...any) ([][]any, error) {
		_, rows, err := c.query(ctx, sql, args)
		return rows, err
	}

	rows, err := q(`SELECT session_user = current_user, r.rolsuper, r.rolcreaterole, r.rolbypassrls, r.rolcreatedb, r.rolreplication
		FROM pg_roles r WHERE r.rolname = current_user`)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, protocol.Errorf(protocol.CauseUnsupportedResult, "can't read the current role from pg_roles")
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

	rows, err = q(`SELECT current_setting('server_version_num')::int`)
	if err != nil {
		return nil, err
	}
	version := int64(0)
	if len(rows) == 1 {
		if v, ok := rows[0][0].(json.Number); ok {
			if n, err := v.Int64(); err == nil {
				version = n
			}
		}
	}

	const me = `(SELECT oid FROM pg_roles WHERE rolname = current_user)`
	type check struct {
		sql  string
		what string
	}
	checks := []check{
		{`SELECT g.rolname FROM pg_auth_members m JOIN pg_roles g ON g.oid = m.roleid
			WHERE m.member = ` + me + ` ORDER BY 1`, "is a member of role %s"},
		{`SELECT n.nspname || '.' || c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relowner = ` + me + ` ORDER BY 1`, "owns %s"},
		{`SELECT nspname FROM pg_namespace WHERE nspowner = ` + me + ` ORDER BY 1`, "owns schema %s"},
		{`SELECT p.oid::regprocedure::text FROM pg_proc p WHERE p.proowner = ` + me + ` ORDER BY 1`, "owns function %s"},
		{`SELECT datname FROM pg_database WHERE datdba = ` + me + ` ORDER BY 1`, "owns database %s"},
		{`SELECT CASE t.typtype WHEN 'd' THEN 'domain ' ELSE 'type ' END || n.nspname || '.' || t.typname
			FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
			WHERE t.typowner = ` + me + ` AND t.typrelid = 0 AND t.typtype <> 'm'
			AND NOT EXISTS (SELECT 1 FROM pg_type e WHERE e.typarray = t.oid) ORDER BY 1`, "owns %s"},
		{`SELECT oid::text FROM pg_largeobject_metadata WHERE lomowner = ` + me + ` ORDER BY oid`, "owns large object %s"},
		{`SELECT n.nspname || '.' || c.collname FROM pg_collation c JOIN pg_namespace n ON n.oid = c.collnamespace
			WHERE c.collowner = ` + me + ` ORDER BY 1`, "owns collation %s"},
		{`SELECT n.nspname || '.' || c.conname FROM pg_conversion c JOIN pg_namespace n ON n.oid = c.connamespace
			WHERE c.conowner = ` + me + ` ORDER BY 1`, "owns conversion %s"},
		{`SELECT n.nspname || '.' || o.oprname || ' (' || COALESCE(format_type(NULLIF(o.oprleft, 0), NULL), 'NONE') || ', ' || format_type(o.oprright, NULL) || ')'
			FROM pg_operator o JOIN pg_namespace n ON n.oid = o.oprnamespace WHERE o.oprowner = ` + me + ` ORDER BY 1`, "owns operator %s"},
		{`SELECT n.nspname || '.' || c.opcname || ' using ' || a.amname FROM pg_opclass c
			JOIN pg_namespace n ON n.oid = c.opcnamespace JOIN pg_am a ON a.oid = c.opcmethod
			WHERE c.opcowner = ` + me + ` ORDER BY 1`, "owns operator class %s"},
		{`SELECT n.nspname || '.' || f.opfname || ' using ' || a.amname FROM pg_opfamily f
			JOIN pg_namespace n ON n.oid = f.opfnamespace JOIN pg_am a ON a.oid = f.opfmethod
			WHERE f.opfowner = ` + me + ` ORDER BY 1`, "owns operator family %s"},
		{`SELECT lanname FROM pg_language WHERE lanowner = ` + me + ` ORDER BY 1`, "owns language %s"},
		{`SELECT n.nspname || '.' || c.cfgname FROM pg_ts_config c JOIN pg_namespace n ON n.oid = c.cfgnamespace
			WHERE c.cfgowner = ` + me + ` ORDER BY 1`, "owns text search configuration %s"},
		{`SELECT n.nspname || '.' || d.dictname FROM pg_ts_dict d JOIN pg_namespace n ON n.oid = d.dictnamespace
			WHERE d.dictowner = ` + me + ` ORDER BY 1`, "owns text search dictionary %s"},
		{`SELECT extname FROM pg_extension WHERE extowner = ` + me + ` ORDER BY 1`, "owns extension %s"},
		{`SELECT fdwname FROM pg_foreign_data_wrapper WHERE fdwowner = ` + me + ` ORDER BY 1`, "owns foreign-data wrapper %s"},
		{`SELECT srvname FROM pg_foreign_server WHERE srvowner = ` + me + ` ORDER BY 1`, "owns foreign server %s"},
		{`SELECT evtname FROM pg_event_trigger WHERE evtowner = ` + me + ` ORDER BY 1`, "owns event trigger %s"},
		{`SELECT spcname FROM pg_tablespace WHERE spcowner = ` + me + ` ORDER BY 1`, "owns tablespace %s"},
	}
	if version >= 100000 {
		checks = append(checks,
			check{`SELECT pubname FROM pg_publication WHERE pubowner = ` + me + ` ORDER BY 1`, "owns publication %s"},
			check{`SELECT subname FROM pg_subscription WHERE subowner = ` + me + ` ORDER BY 1`, "owns subscription %s"},
			check{`SELECT n.nspname || '.' || s.stxname FROM pg_statistic_ext s JOIN pg_namespace n ON n.oid = s.stxnamespace
			WHERE s.stxowner = ` + me + ` ORDER BY 1`, "owns statistics object %s"},
		)
	}
	checks = append(checks, check{`SELECT nspname FROM pg_namespace
			WHERE nspname NOT IN ('pg_catalog', 'information_schema') AND nspname NOT LIKE 'pg\_toast%' AND nspname NOT LIKE 'pg\_temp%'
			AND has_schema_privilege(oid, 'CREATE') ORDER BY 1`, "can CREATE in schema %s"})
	for _, ch := range checks {
		rows, err := q(ch.sql)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			problems = append(problems, "the role "+fmt.Sprintf(ch.what, row[0]))
		}
	}

	privileges := []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"}
	columns := `has_any_column_privilege(c.oid, 'SELECT'),
			has_any_column_privilege(c.oid, 'INSERT'),
			has_any_column_privilege(c.oid, 'UPDATE'),
			has_table_privilege(c.oid, 'DELETE'),
			has_table_privilege(c.oid, 'TRUNCATE'),
			has_any_column_privilege(c.oid, 'REFERENCES'),
			has_table_privilege(c.oid, 'TRIGGER')`
	if version >= 170000 {
		privileges = append(privileges, "MAINTAIN")
		columns += `,
			has_table_privilege(c.oid, 'MAINTAIN')`
	}
	rows, err = q(`SELECT n.nspname || '.' || c.relname, c.relkind::text,
			` + columns + `
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f')
		  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp%'
		ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	declared, err := c.resolve(ctx)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, name := range c.objects {
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
		name, _ := row[0].(string)
		for i, priv := range privileges {
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

func (c *conn) resolve(ctx context.Context) (map[string]object, error) {
	out := map[string]object{}
	for _, o := range c.objects {
		_, rows, err := c.query(ctx, `SELECT n.nspname || '.' || c.relname, c.relkind::text
			FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.oid = to_regclass($1)`, []any{o})
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			out[o] = object{}
			continue
		}
		full, _ := rows[0][0].(string)
		kind, _ := rows[0][1].(string)
		out[o] = object{full: full, kind: kind}
	}
	return out, nil
}
