package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"proctor/protocol"
	"proctor/protocol/jsonpath"
	"proctor/protocol/schema"
)

var Version = "0.0.0-dev"

const (
	Name       = "postgres"
	OpQuery    = "query"
	dsnSuffix  = "_dsn"
	proofShown = 50
)

var (
	connectionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	objectName     = regexp.MustCompile(`^([a-z_][a-z0-9_$]*\.)?[a-z_][a-z0-9_$]*$`)
	refRE          = regexp.MustCompile(`\$\{([^}]*)\}`)
	varName        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	fillRE         = regexp.MustCompile(`^fill\(([1-9][0-9]{0,7})\)$`)
	secretRE       = regexp.MustCompile(`^secret\.([A-Za-z_][A-Za-z0-9_]*)$`)
)

var stringList = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}

var ArgsSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"connection": map[string]any{"type": "string"},
		"query":      map[string]any{"type": "string"},
		"args":       map[string]any{"type": "array"},
	},
	"required":             []string{"connection", "query"},
	"additionalProperties": false,
}

var FactSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"row_count": map[string]any{"type": "integer", "minimum": 0},
		"rows":      map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
	},
	"required":             []string{"row_count", "rows"},
	"additionalProperties": false,
}

var PolicySchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"connections": map[string]any{
			"type": "object",
			"additionalProperties": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"schemas": stringList,
					"objects": stringList,
				},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"connections"},
	"additionalProperties": false,
}

var ConnectionSchema = map[string]any{
	"type":                 []string{"object", "null"},
	"additionalProperties": map[string]any{"type": "string", "pattern": `^[A-Za-z_][A-Za-z0-9_]*$`},
}

type policy struct {
	Connections map[string]struct {
		Schemas []string `json:"schemas"`
		Objects []string `json:"objects"`
	} `json:"connections"`
}

type Driver struct {
	names    []string
	conns    map[string]*conn
	problems map[string][]protocol.Problem
}

func New() *Driver {
	return &Driver{conns: map[string]*conn{}, problems: map[string][]protocol.Problem{}}
}

func (d *Driver) Hello(ctx context.Context, p protocol.HelloParams) (protocol.HelloResult, error) {
	return protocol.HelloResult{
		Name:     Name,
		Version:  Version,
		Protocol: protocol.Version,
		Operations: []protocol.Operation{{
			Name:       OpQuery,
			Kind:       protocol.KindObserve,
			ArgsSchema: ArgsSchema,
			FactSchema: FactSchema,
			Credential: protocol.CredentialNone,
		}},
		PolicySchema:     PolicySchema,
		ConnectionSchema: ConnectionSchema,
	}, nil
}

func (d *Driver) Configure(ctx context.Context, p protocol.ConfigureParams) error {
	if msgs := schema.Check(PolicySchema, p.Policy); len(msgs) > 0 {
		return protocol.Errorf(protocol.CauseInvalidRequest, "policy: %s", strings.Join(msgs, "; "))
	}
	var pol policy
	if err := protocol.Decode(p.Policy, &pol); err != nil {
		return protocol.Errorf(protocol.CauseInvalidRequest, "policy: %v", err)
	}
	names := make([]string, 0, len(pol.Connections))
	for name := range pol.Connections {
		names = append(names, name)
	}
	sort.Strings(names)
	conns := map[string]*conn{}
	problems := map[string][]protocol.Problem{}
	for _, name := range names {
		pc := pol.Connections[name]
		conns[name] = &conn{name: name, schemas: pc.Schemas, objects: pc.Objects}
		problems[name] = policyProblems(name, pc.Schemas, pc.Objects)
	}
	if p.Connection != nil {
		if err := bindDSNs(names, conns, p.Connection, p.Secrets); err != nil {
			return err
		}
	}
	d.names, d.conns, d.problems = names, conns, problems
	return nil
}

func bindDSNs(names []string, conns map[string]*conn, connection any, secrets protocol.Secrets) error {
	if msgs := schema.Check(ConnectionSchema, connection); len(msgs) > 0 {
		return protocol.Errorf(protocol.CauseInvalidRequest, "connection: %s", strings.Join(msgs, "; "))
	}
	if m, ok := connection.(map[string]any); ok && len(m) > 0 {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return protocol.Errorf(protocol.CauseInvalidRequest, "connection: unexpected keys %s; give each connection's DSN as <name>_dsn_env in the binding", strings.Join(keys, ", "))
	}
	keys := make([]string, 0, len(secrets))
	for k := range secrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		name, ok := strings.CutSuffix(k, dsnSuffix)
		if _, declared := conns[name]; !ok || !declared {
			return protocol.Errorf(protocol.CauseInvalidRequest, "binding: secret %s doesn't name a connection declared in the policy (expected <name>_dsn_env for one of: %s)", k, strings.Join(names, ", "))
		}
	}
	for _, name := range names {
		dsn := secrets[name+dsnSuffix]
		if dsn == "" {
			return protocol.Errorf(protocol.CauseInvalidRequest, "binding: connection %s has no DSN; set %s%s_env to an environment variable that holds it", name, name, dsnSuffix)
		}
		c := conns[name]
		c.secrets = []string{dsn}
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			continue
		}
		if cfg.Password != "" {
			c.secrets = append(c.secrets, cfg.Password)
		}
		if cfg.RuntimeParams == nil {
			cfg.RuntimeParams = map[string]string{}
		}
		cfg.RuntimeParams["application_name"] = "proctor"
		cfg.RuntimeParams["default_transaction_read_only"] = "on"
		c.cfg = cfg
	}
	return nil
}

func policyProblems(name string, schemas, objects []string) []protocol.Problem {
	var out []protocol.Problem
	add := func(rule, format string, args ...any) {
		out = append(out, protocol.Problem{Rule: rule, Message: "policy: " + fmt.Sprintf(format, args...)})
	}
	if !connectionName.MatchString(name) {
		add("INT-12", "invalid PostgreSQL connection name %q", name)
	}
	for _, sch := range schemas {
		if !objectName.MatchString(sch) || strings.Contains(sch, ".") {
			add("DB-03", "connection %q: invalid schema name %q (lower-case, unquoted identifiers only)", name, sch)
		}
	}
	if len(objects) == 0 {
		add("DB-07", "connection %q declares no objects; list the tables and views its checks may read", name)
	}
	for _, o := range objects {
		if !objectName.MatchString(o) {
			add("DB-07", "connection %q: invalid object name %q (schema.name or name, lower-case, unquoted)", name, o)
		} else if len(schemas) == 0 && !strings.Contains(o, ".") {
			add("DB-03", "connection %q declares no schemas, so object %q must be schema-qualified", name, o)
		}
	}
	return out
}

func (d *Driver) Validate(ctx context.Context, p protocol.ValidateParams) (protocol.ValidateResult, error) {
	r := protocol.ValidateResult{Kind: protocol.KindObserve}
	add := func(rule, path, format string, args ...any) {
		r.Problems = append(r.Problems, protocol.Problem{Rule: rule, Message: fmt.Sprintf(format, args...), Path: path})
	}
	if p.Op != OpQuery {
		add("EXE-01", "$.do", "unknown operation %q; the postgres driver has only %q", p.Op, OpQuery)
		return r, nil
	}
	r.Refs = refs(p.Args)
	for _, msg := range schema.Check(ArgsSchema, p.Args, schema.AllowRefs) {
		add("DB-01", "$.with", "%s", msg)
	}
	if p.Setup {
		add("FIX-03", "$.do", "database checks are verification steps; setup goes through the application")
	}
	m, _ := p.Args.(map[string]any)
	if name, ok := m["connection"].(string); ok {
		if _, declared := d.conns[name]; !declared {
			add("INT-12", "$.with.connection", "PostgreSQL connection %q is not declared in the driver's policy in proctor.yaml", name)
		} else {
			r.Problems = append(r.Problems, d.problems[name]...)
			r.Targets = []string{name}
		}
	}
	if q, ok := m["query"].(string); ok {
		if strings.TrimSpace(q) == "" {
			add("DB-01", "$.with.query", "query is required")
		}
		if strings.Contains(q, "${") {
			add("DB-01", "$.with.query", "query can't contain ${...}; pass values as args ($1, $2, ...)")
		}
		if strings.ContainsRune(q, 0) {
			add("DB-01", "$.with.query", "query contains a NUL character")
		}
	}
	args, _ := m["args"].([]any)
	for i, a := range args {
		path := fmt.Sprintf("$.with.args[%d]", i)
		switch t := a.(type) {
		case nil, bool, json.Number:
		case string:
			names, err := references(t)
			if err != nil {
				add("HTTP-02", path, "%v", err)
				continue
			}
			for _, n := range names {
				if isFill(n) || secretRE.MatchString(n) {
					add("HTTP-02", path, "${%s} may be used only in a request body or request headers, not in a database query's args", n)
				}
			}
		default:
			add("DB-01", path, "args[%d] must be a string, number, boolean or null", i)
		}
	}
	if matchers(p.Expect) == 0 {
		add("EXE-03", "$.expect", "a database check needs at least one expectation")
	}
	if len(p.Save) > 0 && !oneRow(p.Expect) {
		add("EXE-02", "$.save", "a database check can save values only when it expects exactly one row (expect.equal[%q]: 1)", rowCountPath)
	}
	for _, name := range sortedNames(p.Save) {
		if !firstRowColumn(p.Save[name]) {
			add("EXE-02", "$.save", "save %s: %q must name a column of the only row, as %s<column> or %s['<column>']", name, p.Save[name], firstRowPrefix, strings.TrimSuffix(firstRowPrefix, "."))
		}
	}
	return r, nil
}

const (
	rowCountPath   = "$.row_count"
	firstRowPrefix = "$.rows[0]."
)

func firstRowColumn(path string) bool {
	segs, err := jsonpath.Parse(path)
	return err == nil && len(segs) >= 3 && jsonpath.String(segs[:2])+"." == firstRowPrefix && !segs[2].IsIndex
}

func oneRow(expect any) bool {
	m, _ := expect.(map[string]any)
	equal, _ := m["equal"].(map[string]any)
	n, ok := equal[rowCountPath].(json.Number)
	if !ok {
		return false
	}
	i, err := strconv.ParseInt(n.String(), 10, 64)
	return err == nil && i == 1
}

func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func matchers(expect any) int {
	m, _ := expect.(map[string]any)
	n := 0
	for _, v := range m {
		switch t := v.(type) {
		case map[string]any:
			n += len(t)
		case []any:
			n += len(t)
		}
	}
	return n
}

func references(s string) ([]string, error) {
	var names []string
	for _, m := range refRE.FindAllStringSubmatch(s, -1) {
		if m[1] != "run.id" && !varName.MatchString(m[1]) && !isFill(m[1]) && !secretRE.MatchString(m[1]) {
			return nil, fmt.Errorf("unsupported reference ${%s}: supported are ${run.id}, ${fill(N)}, ${secret.name} and ${name} for values saved by earlier steps", m[1])
		}
		names = append(names, m[1])
	}
	if strings.Count(s, "${") != len(names) {
		return nil, fmt.Errorf("unclosed ${ in %q", s)
	}
	return names, nil
}

func isFill(ref string) bool {
	m := fillRE.FindStringSubmatch(ref)
	if m == nil {
		return false
	}
	n, err := strconv.Atoi(m[1])
	return err == nil && n <= 16<<20
}

func refs(v any) []string {
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			for _, m := range refRE.FindAllStringSubmatch(x, -1) {
				seen[m[1]] = true
			}
		case map[string]any:
			for k, e := range x {
				walk(k)
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (d *Driver) Prove(ctx context.Context) (protocol.ProveResult, error) {
	var out protocol.ProveResult
	for _, name := range d.names {
		c := d.conns[name]
		problems, err := c.roleProblems(ctx)
		if err != nil {
			return protocol.ProveResult{}, wrap(err, "connection %s: checking the read-only role (DB-04)", name)
		}
		proof := protocol.Proof{Name: "connection " + name + ": read-only role (DB-04)"}
		if len(problems) == 0 {
			proof.Status = protocol.ProofProven
			proof.Detail = fmt.Sprintf("connection %s: the role can't write, create or own anything and can read only the declared objects %s", name, strings.Join(c.objects, ", "))
		} else {
			proof.Status = protocol.ProofFailed
			var lines []string
			for i, p := range problems {
				if i == proofShown {
					lines = append(lines, fmt.Sprintf("connection %s: and %d more problems with the verification role (DB-04)", name, len(problems)-proofShown))
					break
				}
				lines = append(lines, fmt.Sprintf("connection %s: %s (DB-04)", name, p))
			}
			proof.Detail = strings.Join(lines, "; ")
		}
		out.Proofs = append(out.Proofs, proof)
	}
	return out, nil
}

func wrap(err error, format string, args ...any) error {
	var e *protocol.Error
	if !errors.As(err, &e) {
		return protocol.Errorf(protocol.CauseDriverFailed, "%s: %v", fmt.Sprintf(format, args...), err)
	}
	out := *e
	out.Message = fmt.Sprintf(format, args...) + ": " + e.Message
	return &out
}

func (d *Driver) Ready(ctx context.Context) (protocol.ReadyResult, error) {
	for _, name := range d.names {
		c := d.conns[name]
		if err := c.ping(ctx); err != nil {
			c.drop()
			var e *protocol.Error
			if !errors.As(err, &e) {
				return protocol.ReadyResult{Detail: fmt.Sprintf("connection %s: %v", name, err)}, nil
			}
			return protocol.ReadyResult{Detail: e.Message, Lasting: e.Data.Lasting}, nil
		}
	}
	return protocol.ReadyResult{Ready: true, Detail: fmt.Sprintf("connections ready: %s", strings.Join(d.names, ", "))}, nil
}

type queryArgs struct {
	Connection string `json:"connection"`
	Query      string `json:"query"`
	Args       []any  `json:"args"`
}

func (d *Driver) Call(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	start := time.Now()
	if p.Op != OpQuery {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "unknown operation %q", p.Op)
	}
	var a queryArgs
	if err := protocol.Decode(p.Args, &a); err != nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "query: %v", err)
	}
	c := d.conns[a.Connection]
	if c == nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "connection %q is not declared in the policy", a.Connection)
	}
	args := make([]any, len(a.Args))
	for i, v := range a.Args {
		args[i] = argValue(v)
	}
	cols, rows, err := c.query(ctx, a.Query, args)
	if err != nil {
		return protocol.CallResult{}, err
	}
	seen := map[string]bool{}
	for _, col := range cols {
		if seen[col] {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnsupportedResult, "the query returns two columns named %q; give each column a distinct name", col)
		}
		seen[col] = true
	}
	out := make([]any, len(rows))
	for i, row := range rows {
		m := make(map[string]any, len(cols))
		for j, col := range cols {
			m[col] = row[j]
		}
		out[i] = m
	}
	noun := "rows"
	if len(rows) == 1 {
		noun = "row"
	}
	return protocol.CallResult{
		Fact: map[string]any{"row_count": len(rows), "rows": out},
		Evidence: protocol.Evidence{
			Summary:        fmt.Sprintf("query on %s -> %d %s", a.Connection, len(rows), noun),
			RequestExcerpt: strings.TrimSpace(a.Query),
			DurationMS:     time.Since(start).Milliseconds(),
		},
	}, nil
}

func (d *Driver) Close(ctx context.Context, p protocol.CloseParams) error {
	return protocol.Errorf(protocol.CauseInvalidRequest, "the postgres driver opens no handles")
}

func (d *Driver) Shutdown() {
	for _, name := range d.names {
		d.conns[name].drop()
	}
}
