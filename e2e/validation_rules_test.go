package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin each validation and integrity rule to its own finding:
// the exit code, the rule ID and a distinctive part of the message, and no
// finding beyond the ones listed. Counting the findings catches a rule that
// stops reporting, a check that reports twice, and an early exit that no
// longer stops later checks from adding a spurious finding. Boundaries are
// tested on both sides.

// rulesConfig is the fixture configuration plus a credential variant, a
// secret and a declared PostgreSQL connection, so that specifications can
// use all three.
const rulesConfig = `version: 1
project: example
manifest: manifest.yaml
identities:
  alice: { type: bearer, variants: [expired] }
  bob: { type: bearer }
  anonymous: { type: none }
secrets: [pw]
connections:
  postgres:
    app_db: { schemas: [app], objects: [app.items] }
`

type finding struct {
	Kind    int    `json:"kind"`
	Rule    string `json:"rule"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

type checkReport struct {
	ExitCode int `json:"exit_code"`
	Report   struct {
		Unlisted []string  `json:"unlisted"`
		Findings []finding `json:"findings"`
	} `json:"report"`
}

// expectFindings checks a --json report from validate or integrity: the
// exit code, and that the findings are exactly want. Each entry is "RULE
// text" and matches one finding of that rule whose "path: message"
// contains text; a finding no entry matches fails the test.
func expectFindings(t *testing.T, code int, out, errOut string, wantCode int, want ...string) checkReport {
	t.Helper()
	var rep checkReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("exit %d, report isn't JSON: %v\nstdout:\n%s\nstderr:\n%s", code, err, out, errOut)
	}
	if code != wantCode || rep.ExitCode != code {
		t.Fatalf("exit %d (report says %d), want %d\n%s\n%s", code, rep.ExitCode, wantCode, out, errOut)
	}
	used := make([]bool, len(rep.Report.Findings))
	for _, w := range want {
		rule, text, _ := strings.Cut(w, " ")
		found := false
		for i, f := range rep.Report.Findings {
			if !used[i] && f.Rule == rule && strings.Contains(f.Path+": "+f.Message, text) {
				used[i], found = true, true
				break
			}
		}
		if !found {
			t.Errorf("no %s finding containing %q", rule, text)
		}
	}
	for i, f := range rep.Report.Findings {
		if !used[i] {
			t.Errorf("unexpected finding %s %s: %s", f.Rule, f.Path, f.Message)
		}
	}
	if t.Failed() {
		t.Fatalf("output:\n%s", out)
	}
	return rep
}

// ruleCase changes the fixture repository, commits it, and runs one command
// with --json. Empty config, manifest and spec keep the fixture's files.
type ruleCase struct {
	name                   string
	config, manifest, spec string
	change                 func(r *testRepo)
	args                   []string // the command and its flags; validate when empty
	code                   int
	want                   []string
}

func runRuleCases(t *testing.T, cases []ruleCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRepo(t)
			for p, content := range map[string]string{"proctor/proctor.yaml": c.config, "proctor/manifest.yaml": c.manifest, "proctor/specs/project-access.yaml": c.spec} {
				if content != "" {
					r.write(p, content)
				}
			}
			if c.change != nil {
				c.change(r)
			}
			r.commit(c.name)
			args := c.args
			if len(args) == 0 {
				args = []string{"validate"}
			}
			code, out, errOut := r.proctor(append(append([]string{}, args...), "--json")...)
			expectFindings(t, code, out, errOut, c.code, c.want...)
		})
	}
}

// scenarioWith is a specification with one scenario, S-1, made of the given
// setup and verification steps.
func scenarioWith(setup, steps string) string {
	s := "version: 1\nsuite: s\nscenarios:\n  - id: S-1\n    name: rules\n"
	if setup != "" {
		s += "    setup:\n" + setup
	}
	return s + "    steps:\n" + steps
}

func spec(steps ...string) string { return scenarioWith("", strings.Join(steps, "")) }

// st is one step: request and expect are the contents of flow mappings, and
// each extra is one more line of the step.
func st(id, request, expect string, extra ...string) string {
	s := "      - id: " + id + "\n"
	if request != "" {
		s += "        request: { " + request + " }\n"
	}
	if expect != "" {
		s += "        expect: { " + expect + " }\n"
	}
	for _, e := range extra {
		s += "        " + e + "\n"
	}
	return s
}

// dbq is a database check step with the given query and other check fields.
func dbq(id, query, fields string, extra ...string) string {
	return st(id, "", "", append([]string{fmt.Sprintf("verify: { postgres: { connection: app_db, query: %q, %s } }", query, fields)}, extra...)...)
}

func db(id, fields string, extra ...string) string {
	return dbq(id, "select name from app.items where id = $1", fields, extra...)
}

const (
	readDoc   = "method: GET, path: /doc, identity: anonymous"
	ok200     = "status: 200, present: [$.a]"
	postItem  = `method: POST, path: /items, identity: alice, body: { name: "n ${run.id}" }`
	postFixed = "method: POST, path: /items, identity: alice, body: { name: Fixed }"
	readItem  = "method: GET, path: /items/1, identity: alice"
	listItems = `method: GET, path: /items, identity: alice, query: { owner: "${run.id}" }`
	created   = "status: 201, present: [$.id]"
	variant   = "method: GET, path: /items/1, identity: alice, credential: expired"
)

var (
	read   = st("read", readItem, "status: 200, present: [$.id]")
	create = st("create", postItem, created)
)

// TestValidationRulesSpecifications covers the rules on scenarios and steps
// (prd.md §11), with validate on the working tree.
func TestValidationRulesSpecifications(t *testing.T) {
	cases := []ruleCase{
		{name: "valid read", spec: spec(st("read", readDoc, ok200))},
		{name: "INT-15 specification version", spec: strings.Replace(spec(st("read", readDoc, ok200)), "version: 1", "version: 2", 1), code: 3,
			want: []string{"INT-15 proctor/specs/project-access.yaml: version must be 1, got 2"}},
		{name: "INT-02 invalid scenario id", spec: strings.Replace(spec(st("read", readDoc, ok200)), "id: S-1", `id: "bad id"`, 1), code: 3,
			want: []string{`INT-02 scenarios[0]: invalid id "bad id"`}},
		{name: "EXE-01 blank scenario name", spec: strings.Replace(spec(st("read", readDoc, ok200)), "name: rules", `name: "  "`, 1), code: 3,
			want: []string{"EXE-01 scenario S-1: name is required"}},
		{name: "EXE-01 scenario without steps", spec: "version: 1\nsuite: s\nscenarios:\n  - id: S-1\n    name: rules\n    steps: []\n", code: 3,
			want: []string{"EXE-01 scenario S-1: at least one verification step is required"}},

		// EXE-07: what counts as a later state check.
		{name: "EXE-07 write checked by a database check", spec: spec(create, db("check", `args: ["1"], expect: { rows: 1 }`))},
		{name: "EXE-07 a later write isn't a state check", spec: spec(create, st("create2", postItem, created)), code: 3,
			want: []string{"EXE-07 step create: POST changes state", "EXE-07 step create2: POST changes state"}},
		{name: "EXE-07 a later read without a status isn't a state check", spec: spec(create, st("read", readItem, "present: [$.id]")), code: 3,
			want: []string{"EXE-03 steps[1] (read): expect.status is required", "EXE-07 step create: POST changes state"}},
		{name: "EXE-07 blank waiver on a checked write", spec: spec(st("create", postItem, created, `no_state_check: "  "`), read), code: 3,
			want: []string{"EXE-07 no_state_check must state a reason"}},
		{name: "EXE-07 waiver in setup", spec: scenarioWith(st("seed", postItem, created, "no_state_check: setup data"), create+read), code: 3,
			want: []string{"EXE-07 setup[0] (seed): no_state_check is only for verification steps"}},

		// FIX-05: a sent value a later step matches needs ${run.id}.
		{name: "FIX-05 one finding however many steps match", code: 3,
			spec: spec(st("create", postFixed, created), st("read1", readItem, "status: 200, json: { $.name: Fixed }"), st("read2", readItem, "status: 200, json: { $.name: Fixed }")),
			want: []string{`FIX-05 step create sends "Fixed" and step read1 expects it`}},
		{name: "FIX-05 a value no later step expects", spec: spec(st("create", postFixed, created), read)},
		{name: "FIX-05 matched by database args", spec: spec(st("create", postFixed, created), db("check", `args: [Fixed], expect: { rows: 1 }`)), code: 3,
			want: []string{`FIX-05 step create sends "Fixed" and step check expects it`}},
		{name: "FIX-05 matched by first_row", spec: spec(st("create", postFixed, created), db("check", `args: ["1"], expect: { rows: 1, first_row: { name: Fixed } }`)), code: 3,
			want: []string{`FIX-05 step create sends "Fixed" and step check expects it`}},
		{name: "FIX-05 run-scoped value matched later", spec: spec(create, st("read", readItem, `status: 200, json: { $.name: "n ${run.id}" }`))},
		{name: "FIX-05 empty value matched later", spec: spec(st("create", `method: POST, path: /items, identity: alice, body: { name: "n ${run.id}", note: "" }`, created),
			st("read", readItem, `status: 200, json: { $.note: "" }`))},

		// HTTP-09: a length on an unfiltered GET.
		{name: "HTTP-09 length on a write", spec: spec(st("create", postItem, "status: 201, length: { $.items: 1 }"), read)},
		{name: "HTTP-09 one length on an unfiltered read", spec: spec(st("list", "method: GET, path: /items, identity: alice", "status: 200, length: { $.items: 1 }")), code: 3,
			want: []string{"HTTP-09 step list asserts a list length on a request with no query parameters"}},
		{name: "HTTP-09 length on a filtered read", spec: spec(st("list", listItems, "status: 200, length: { $.items: 1 }"))},

		// EXE-03: the expected status.
		{name: "EXE-03 no expect", spec: spec(st("read", readDoc, "")), code: 3, want: []string{"EXE-03 steps[0] (read): expect.status is required"}},
		{name: "EXE-03 expect without status", spec: spec(st("read", readDoc, "present: [$.a]")), code: 3, want: []string{"EXE-03 expect.status is required"}},
		{name: "EXE-03 status 99", spec: spec(st("read", readDoc, "status: 99, present: [$.a]")), code: 3, want: []string{"EXE-03 status 99 is not an HTTP status"}},
		{name: "EXE-03 status 100", spec: spec(st("read", readDoc, "status: 100, present: [$.a]"))},
		{name: "EXE-03 status 599", spec: spec(st("read", readDoc, "status: 599, present: [$.a]"))},
		{name: "EXE-03 status 600", spec: spec(st("read", readDoc, "status: 600, present: [$.a]")), code: 3, want: []string{"EXE-03 status 600 is not an HTTP status"}},
		{name: "EXE-03 status 299 alone", spec: spec(st("read", readDoc, "status: 299")), code: 3, want: []string{"EXE-03 a verification step expecting 299 must also assert"}},
		{name: "EXE-03 status 300 alone", spec: spec(st("read", readDoc, "status: 300"))},

		// INT-02, EXE-01, INT-12, AUTH-06: steps and requests.
		{name: "INT-02 invalid step id", spec: spec(st(`"bad id"`, readDoc, ok200)), code: 3, want: []string{`INT-02 invalid step id "bad id"`}},
		{name: "INT-02 duplicate step id", spec: spec(st("read", readDoc, ok200), st("read", readDoc, ok200)), code: 3, want: []string{`INT-02 steps[1]: duplicate step id "read"`}},
		{name: "EXE-01 unsupported method", spec: spec(st("read", "method: HEAD, path: /doc, identity: anonymous", ok200)), code: 3, want: []string{`EXE-01 unsupported method "HEAD"`}},
		{name: "EXE-01 body isn't JSON", spec: spec(st("create", `method: POST, path: /items, identity: alice, body: { name: "n ${run.id}", score: .inf }`, created), read), code: 3,
			want: []string{"EXE-01 body can't be sent as JSON: +Inf is not a JSON number"}},
		{name: "INT-12 no identity", spec: spec(st("read", "method: GET, path: /doc", ok200)), code: 3, want: []string{"INT-12 identity is required"}},
		{name: "AUTH-06 variant in setup", spec: scenarioWith(st("seed", variant, "status: 401"), st("read", variant, "status: 401")), code: 3,
			want: []string{"AUTH-06 setup[0] (seed): a credential variant is for negative tests, not setup"}},
		{name: "AUTH-06 variant expecting 401", spec: spec(st("read", variant, "status: 401"))},
		{name: "AUTH-06 variant expecting 403", spec: spec(st("read", variant, "status: 403"))},
		{name: "AUTH-06 variant expecting 404", spec: spec(st("read", variant, "status: 404")), code: 3, want: []string{`AUTH-06 credential variant "expired" must expect 401 or 403, not 404`}},

		// HTTP-08, HTTP-09: assertion paths and contradictions.
		{name: "HTTP-08 keys path", spec: spec(st("read", readDoc, "status: 200, keys: { a.b: [x] }")), code: 3, want: []string{`HTTP-08 keys: JSONPath "a.b" must start with $`}},
		{name: "HTTP-08 absent and keys", spec: spec(st("read", readDoc, "status: 200, absent: [$.a], keys: { $.a: [x] }")), code: 3, want: []string{"HTTP-08 $.a is both absent and given keys"}},
		{name: "HTTP-09 length path", spec: spec(st("list", listItems, "status: 200, length: { items: 1 }")), code: 3, want: []string{`HTTP-09 length: JSONPath "items" must start with $`}},
		{name: "HTTP-09 length 0", spec: spec(st("list", listItems, "status: 200, length: { $.items: 0 }"))},
		{name: "HTTP-09 length -1", spec: spec(st("list", listItems, "status: 200, length: { $.items: -1 }")), code: 3, want: []string{"HTTP-09 length of $.items can't be negative"}},
		{name: "HTTP-09 length as text", spec: spec(st("list", listItems, "status: 200, length: { $.items: abc }")), code: 3,
			want: []string{"HTTP-09 length of $.items must be an integer or one ${name} reference"}},
		{name: "HTTP-09 length as two references", spec: spec(st("count", readDoc, "status: 200, present: [$.a]", "save: { a: $.a, b: $.b }"), st("list", listItems, `status: 200, length: { $.items: "${a}${b}" }`)), code: 3,
			want: []string{"HTTP-09 length of $.items must be an integer or one ${name} reference"}},
		{name: "HTTP-09 length as a saved value", spec: spec(st("count", readDoc, "status: 200, present: [$.a]", "save: { a: $.a }"), st("list", listItems, `status: 200, length: { $.items: "${a}" }`))},
		{name: "HTTP-08 absent and length", spec: spec(st("list", listItems, "status: 200, absent: [$.items], length: { $.items: 1 }")), code: 3, want: []string{"HTTP-08 $.items is both absent and given a length"}},
		{name: "HTTP-08 absent header with a space", spec: spec(st("read", readDoc, "status: 200, absent_headers: [X Bad]")), code: 3, want: []string{`HTTP-08 absent_headers: invalid header name "X Bad"`}},
		{name: "HTTP-08 index 0 and 12", spec: spec(st("read", readDoc, `status: 200, present: ["$.list[0]", "$.list[12]"]`))},

		// EXE-02: saved values.
		{name: "EXE-02 save name", spec: spec(st("read", readDoc, ok200, "save: { 1x: $.a }")), code: 3, want: []string{`EXE-02 save name "1x" must be a letter`}},
		{name: "EXE-02 saved twice", spec: spec(st("read1", readDoc, ok200, "save: { id: $.a }"), st("read2", readDoc, ok200, "save: { id: $.a }")), code: 3,
			want: []string{`EXE-02 steps[1] (read2): "id" is already saved by an earlier step`}},
		{name: "EXE-02 save path", spec: spec(st("read", readDoc, ok200, "save: { id: a }")), code: 3, want: []string{`EXE-02 save id: JSONPath "a" must start with $`}},

		// EXE-03: expected values must be JSON values.
		{name: "EXE-03 NaN", spec: spec(st("read", readDoc, "status: 200, json: { $.a: .nan }")), code: 3, want: []string{"EXE-03 expected value for $.a isn't a JSON value: NaN is not a JSON number"}},
		{name: "EXE-03 -Inf", spec: spec(st("read", readDoc, "status: 200, json: { $.a: -.inf }")), code: 3, want: []string{"EXE-03 -Inf is not a JSON number"}},
		{name: "EXE-03 Inf in an object", spec: spec(st("read", readDoc, "status: 200, json: { $.obj: { a: 1, b: .inf } }")), code: 3, want: []string{"EXE-03 expected value for $.obj isn't a JSON value: +Inf is not a JSON number"}},
		{name: "EXE-03 Inf in a list", spec: spec(st("read", readDoc, "status: 200, json: { $.list: [1, .inf] }")), code: 3, want: []string{"EXE-03 expected value for $.list isn't a JSON value: +Inf is not a JSON number"}},
		{name: "EXE-03 largest finite numbers", spec: spec(st("read", readDoc, "status: 200, json: { $.a: 1.7976931348623157e308, $.b: -1.7976931348623157e308 }"))},

		// HTTP-02, AUTH-07, HTTP-05: references and paths.
		{name: "HTTP-02 malformed reference after an unsaved one", spec: spec(st("read", readDoc, `status: 200, json: { $.a: ["${missing}", "${"] }`)), code: 3, want: []string{"HTTP-02 unclosed ${"}},
		{name: "HTTP-02 malformed reference after plain text", spec: spec(st("read", readDoc, `status: 200, json: { $.a: [ok, "${"] }`)), code: 3, want: []string{"HTTP-02 unclosed ${"}},
		{name: "AUTH-07 undeclared secret", spec: spec(st("create", `method: POST, path: /items, identity: alice, body: { name: "n ${run.id}", p: "${secret.other}" }`, created), read), code: 3,
			want: []string{`AUTH-07 secret "other" is not declared in proctor.yaml`}},
		{name: "HTTP-02 fill at the limit", spec: spec(st("create", `method: POST, path: /items, identity: alice, body: { name: "n ${run.id}", pad: "${fill(16777216)}" }`, created), read)},
		{name: "HTTP-02 fill over the limit", spec: spec(st("create", `method: POST, path: /items, identity: alice, body: { name: "n ${run.id}", pad: "${fill(16777217)}" }`, created), read), code: 3,
			want: []string{"HTTP-02 unsupported reference ${fill(16777217)}"}},
		{name: "HTTP-05 space in path", spec: spec(st("read", `method: GET, path: "/a b", identity: anonymous`, ok200)), code: 3, want: []string{"HTTP-05 contains a space or control character"}},
		{name: "HTTP-05 DEL in path", spec: spec(st("read", `method: GET, path: "/a\x7f", identity: anonymous`, ok200)), code: 3, want: []string{"HTTP-05 contains a space or control character"}},
	}
	for _, idx := range []string{"x", "-1", "01", "+1"} {
		cases = append(cases, ruleCase{name: "HTTP-08 index " + idx, spec: spec(st("read", readDoc, `status: 200, present: ["$.list[`+idx+`]"]`)), code: 3,
			want: []string{`HTTP-08 present: JSONPath "$.list[` + idx + `]": only non-negative array indexes`}})
	}
	for i := range cases {
		cases[i].config = rulesConfig
	}
	runRuleCases(t, cases)
}

// TestValidationRulesDatabaseChecks covers §11.7's rules for database check
// steps. validate never connects to a database, so no server is needed.
func TestValidationRulesDatabaseChecks(t *testing.T) {
	rows1 := `args: ["1"], expect: { rows: 1 }`
	cases := []ruleCase{
		{name: "valid check", spec: spec(db("check", rows1))},
		{name: "EXE-01 verify without postgres", spec: spec(st("check", "", "", "verify: {}")), code: 3, want: []string{"EXE-01 verify needs postgres"}},
		{name: "EXE-01 HTTP expectation on a database check", spec: spec(st("check", "", "status: 200", "verify: { postgres: { connection: app_db, query: select 1, expect: { rows: 1 } } }")), code: 3,
			want: []string{"EXE-01 a database check's expectation goes in verify.postgres.expect"}},
		{name: "DB-01 blank query", spec: spec(dbq("check", "  ", "expect: { rows: 1 }")), code: 3, want: []string{"DB-01 query is required"}},
		{name: "DB-01 NUL in query", spec: spec(dbq("check", "select 1\x00", "expect: { rows: 1 }")), code: 3, want: []string{"DB-01 query contains a NUL character"}},
		{name: "HTTP-02 unsaved reference in args", spec: spec(db("check", `args: ["${missing}"], expect: { rows: 1 }`)), code: 3, want: []string{"HTTP-02 ${missing} isn't saved by an earlier step"}},
		{name: "EXE-03 no expectation", spec: spec(db("check", `args: ["1"]`)), code: 3, want: []string{"EXE-03 verify.postgres.expect needs rows, first_row or both"}},
		{name: "EXE-03 empty expectation", spec: spec(db("check", `args: ["1"], expect: {}`)), code: 3, want: []string{"EXE-03 verify.postgres.expect needs rows, first_row or both"}},
		{name: "EXE-03 rows 0", spec: spec(db("check", `args: ["1"], expect: { rows: 0 }`))},
		{name: "EXE-03 rows -1", spec: spec(db("check", `args: ["1"], expect: { rows: -1 }`)), code: 3, want: []string{"EXE-03 rows can't be negative"}},
		{name: "EXE-03 first_row alone", spec: spec(db("check", `args: ["1"], expect: { first_row: { name: n } }`))},
		{name: "EXE-03 first_row with rows 0", spec: spec(db("check", `args: ["1"], expect: { rows: 0, first_row: { name: n } }`)), code: 3, want: []string{"EXE-03 first_row can't be expected when rows is 0"}},
		{name: "EXE-03 empty first_row", spec: spec(db("check", `args: ["1"], expect: { rows: 1, first_row: {} }`)), code: 3, want: []string{"EXE-03 first_row is empty"}},
		{name: "EXE-03 non-finite first_row value", spec: spec(db("check", `args: ["1"], expect: { rows: 1, first_row: { score: .inf } }`)), code: 3, want: []string{"EXE-03 first_row.score isn't a JSON value"}},
		{name: "HTTP-02 unsaved reference in first_row", spec: spec(db("check", `args: ["1"], expect: { rows: 1, first_row: { id: "${missing}" } }`)), code: 3, want: []string{"HTTP-02 ${missing} isn't saved by an earlier step"}},
		{name: "EXE-02 save with rows 1", spec: spec(db("check", rows1, "save: { n: name }"))},
		{name: "EXE-02 save without expectation", spec: spec(db("check", `args: ["1"]`, "save: { n: name }")), code: 3,
			want: []string{"EXE-03 needs rows, first_row or both", "EXE-02 can save values only when it expects rows: 1"}},
		{name: "EXE-02 save with first_row alone", spec: spec(db("check", `args: ["1"], expect: { first_row: { name: n } }`, "save: { n: name }")), code: 3,
			want: []string{"EXE-02 can save values only when it expects rows: 1"}},
		{name: "EXE-02 save with rows 2", spec: spec(db("check", `args: ["1"], expect: { rows: 2 }`, "save: { n: name }")), code: 3,
			want: []string{"EXE-02 can save values only when it expects rows: 1"}},
		{name: "EXE-02 save name", spec: spec(db("check", rows1, "save: { 1x: name }")), code: 3, want: []string{`EXE-02 save name "1x" must be a letter`}},
		{name: "EXE-02 saved twice", spec: spec(st("read", readDoc, ok200, "save: { id: $.a }"), db("check", rows1, "save: { id: name }")), code: 3,
			want: []string{`EXE-02 steps[1] (check): "id" is already saved by an earlier step`}},
		{name: "EXE-02 save of a non-column", spec: spec(db("check", rows1, "save: { n: a.b }")), code: 3, want: []string{`EXE-02 save n: "a.b" is not a column name`}},
		{name: "FIX-03 database check in setup", spec: scenarioWith(db("seed", rows1), st("read", readDoc, ok200)), code: 3, want: []string{"FIX-03 setup[0] (seed): database checks are verification steps"}},
		{name: "EXE-03 setup request without expect", spec: scenarioWith(st("seed", readDoc, ""), st("read", readDoc, ok200)), code: 3, want: []string{"EXE-03 setup[0] (seed): expect.status is required"}},
	}
	for i := range cases {
		cases[i].config = rulesConfig
	}
	runRuleCases(t, cases)
}

// TestValidationRulesConfiguration covers proctor.yaml's rules (§9), with
// the fixture's specification.
func TestValidationRulesConfiguration(t *testing.T) {
	with := func(extra string) string { return configYAML + extra }
	ids := func(extra string) string {
		return strings.Replace(configYAML, "  anonymous: { type: none }\n", "  anonymous: { type: none }\n"+extra, 1)
	}
	pg := func(conn string) string { return with("connections:\n  postgres:\n" + conn) }
	cases := []ruleCase{
		{name: "INT-15 configuration version", config: strings.Replace(configYAML, "version: 1", "version: 2", 1), code: 3, want: []string{"INT-15 proctor/proctor.yaml: version must be 1, got 2"}},
		{name: "INT-15 no manifest", config: strings.Replace(configYAML, "manifest: manifest.yaml\n", "", 1), code: 3, want: []string{"INT-15 manifest is required"}},
		{name: "INT-15 empty configuration", config: "\n", code: 3, want: []string{"INT-15 proctor/proctor.yaml: "}},
		{name: "INT-12 unknown adapter", config: with("adapters: { http: enabled, redis: enabled }\n"), code: 3, want: []string{`INT-12 unknown adapter "redis"`}},
		{name: "INT-12 adapter neither enabled nor disabled", config: with("adapters: { http: enabled, postgres: on }\n"), code: 3, want: []string{`INT-12 adapter "postgres" must be enabled or disabled, got "on"`}},
		{name: "INT-12 adapters enabled and disabled", config: with("adapters: { http: enabled, postgres: disabled, temporal: enabled }\n")},
		{name: "INT-12 http disabled", config: with("adapters: { http: disabled }\n"), code: 3, want: []string{"INT-12 the http adapter can't be disabled"}},
		{name: "INT-12 invalid identity name", config: ids("  \"bad name\": { type: none }\n"), code: 3, want: []string{`INT-12 invalid identity name "bad name"`}},
		{name: "AUTH-01 unsupported identity type", config: ids("  carol: { type: magic }\n"), code: 3, want: []string{`AUTH-01 identity "carol" has unsupported type "magic"`}},
		{name: "AUTH-06 type none with one variant", config: ids("  ghost: { type: none, variants: [expired] }\n"), code: 3, want: []string{`AUTH-06 identity "ghost" has type none`}},
		{name: "AUTH-06 type none with one accepted variant", config: ids("  ghost: { type: none, accepted_variants: [lower] }\n"), code: 3, want: []string{`AUTH-06 identity "ghost" has type none`}},
		{name: "AUTH-06 invalid variant name", config: ids("  carol: { type: bearer, variants: [\"bad name\"] }\n"), code: 3, want: []string{`AUTH-06 identity "carol": invalid credential variant name "bad name"`}},
		{name: "AUTH-06 variant listed twice", config: ids("  carol: { type: bearer, variants: [old], accepted_variants: [old] }\n"), code: 3,
			want: []string{`AUTH-06 identity "carol": credential variant "old" is listed twice`}},
		{name: "AUTH-06 variants", config: ids("  carol: { type: bearer, variants: [old], accepted_variants: [lower] }\n")},
		{name: "AUTH-07 invalid secret name", config: with("secrets: [1pw]\n"), code: 3, want: []string{`AUTH-07 secrets: invalid or repeated name "1pw"`}},
		{name: "AUTH-07 repeated secret", config: with("secrets: [pw, pw]\n"), code: 3, want: []string{`AUTH-07 secrets: invalid or repeated name "pw"`}},
		{name: "AUTH-07 secrets", config: with("secrets: [pw, token]\n")},
		{name: "INT-12 connection with postgres disabled", config: with("adapters: { http: enabled, postgres: disabled }\nconnections:\n  postgres:\n    app_db: { schemas: [app], objects: [app.items] }\n"), code: 3,
			want: []string{"INT-12 connections.postgres is set but the postgres adapter is disabled"}},
		{name: "INT-12 invalid connection name", config: pg("    \"bad name\": { schemas: [app], objects: [app.items] }\n"), code: 3, want: []string{`INT-12 invalid PostgreSQL connection name "bad name"`}},
		{name: "DB-03 upper-case schema", config: pg("    app_db: { schemas: [App], objects: [items] }\n"), code: 3, want: []string{`DB-03 connection "app_db": invalid schema name "App"`}},
		{name: "DB-03 qualified schema", config: pg("    app_db: { schemas: [app.x], objects: [items] }\n"), code: 3, want: []string{`DB-03 connection "app_db": invalid schema name "app.x"`}},
		{name: "DB-07 no objects", config: pg("    app_db: { schemas: [app] }\n"), code: 3, want: []string{`DB-07 connection "app_db" declares no objects`}},
		{name: "DB-07 invalid object name", config: pg("    app_db: { schemas: [app], objects: [App.Items] }\n"), code: 3, want: []string{`DB-07 connection "app_db": invalid object name "App.Items"`}},
		{name: "DB-07 connection", config: pg("    app_db: { schemas: [app], objects: [app.items, users] }\n")},
		{name: "INT-13 protected path outside the repository", config: with("protected_paths: [../outside.yaml]\n"), code: 3, want: []string{"INT-13 protected_paths: path \"../outside.yaml\" leaves its directory"}},
		{name: "INT-13 protected paths", config: with("protected_paths: [compose.verify.yaml, deploy/]\n")},

		// MODE-01: where proctor.yaml is.
		{name: "MODE-01 configuration outside the repository", args: []string{"validate", "--config", "../proctor.yaml"}, code: 3, want: []string{"MODE-01 configuration path: path \"../proctor.yaml\" leaves its directory"}},
		{name: "MODE-01 configuration at the root", args: []string{"validate", "--config", "proctor.yaml"}, code: 3, want: []string{"MODE-01 must sit inside a protected root directory"}},
		{name: "MODE-01 configuration path not clean", args: []string{"validate", "--config", "proctor/./proctor.yaml"}, code: 3, want: []string{"MODE-01 is not in clean form"}},
		{name: "MODE-01 configuration path is a dot", args: []string{"validate", "--config", "."}, code: 3, want: []string{"MODE-01 configuration path: path \".\" is not in clean form"}},
	}
	runRuleCases(t, cases)
}

// TestValidationRulesManifest covers the manifest and how listed files are
// read (INT-01, INT-02, INT-04, INT-07, INT-15).
func TestValidationRulesManifest(t *testing.T) {
	entries := func(e ...string) string { return "version: 1\nspecifications:\n" + strings.Join(e, "") }
	entry := func(id, p string) string { return "  - id: " + id + "\n    path: " + p + "\n" }
	other := strings.ReplaceAll(specYAML, "PA-00", "OT-00")
	symlink := func(name string) func(r *testRepo) {
		return func(r *testRepo) {
			if err := os.Rename(filepath.Join(r.dir, name), filepath.Join(r.dir, name+".real")); err != nil {
				r.t.Fatal(err)
			}
			if err := os.Symlink(filepath.Base(name)+".real", filepath.Join(r.dir, name)); err != nil {
				r.t.Fatal(err)
			}
		}
	}
	integrity := []string{"integrity"}
	cases := []ruleCase{
		{name: "INT-15 manifest version", manifest: strings.Replace(manifestYAML, "version: 1", "version: 2", 1), code: 3, want: []string{"INT-15 proctor/manifest.yaml: version must be 1, got 2"}},
		{name: "INT-02 invalid specification id", manifest: entries(entry("-bad", "specs/project-access.yaml")), code: 3, want: []string{`INT-02 specifications[0]: invalid id "-bad"`}},
		{name: "INT-02 duplicate specification id", manifest: entries(entry("pa", "specs/project-access.yaml"), entry("pa", "specs/other.yaml")),
			change: func(r *testRepo) { r.write("proctor/specs/other.yaml", other) }, code: 3, want: []string{`INT-02 specifications[1]: duplicate specification id "pa"`}},
		{name: "INT-02 specification listed twice", manifest: entries(entry("a", "specs/project-access.yaml"), entry("b", "specs/project-access.yaml")), code: 3,
			want: []string{"INT-02 specifications[1]: proctor/specs/project-access.yaml is listed twice"}},
		{name: "INT-01 specification path leaves the root", manifest: entries(entry("x", "../app/x.yaml")), args: integrity, code: 4, want: []string{"INT-01 leaves its directory"}},
		{name: "INT-01 specification not found", manifest: entries(entry("x", "specs/missing.yaml")), args: integrity, code: 4,
			want: []string{"INT-01 proctor/specs/missing.yaml: listed in the manifest but not found"}},
		{name: "INT-07 specification is a symlink", change: symlink("proctor/specs/project-access.yaml"), args: integrity, code: 4,
			want: []string{"INT-07 proctor/specs/project-access.yaml: cannot read: proctor/specs/project-access.yaml is a symlink", "INT-13 proctor/specs/project-access.yaml: protected input is a symlink"}},
		{name: "INT-01 manifest path leaves the root", config: strings.Replace(configYAML, "manifest: manifest.yaml", "manifest: ../manifest.yaml", 1), args: integrity, code: 4,
			want: []string{`INT-01 proctor/proctor.yaml: manifest path: path "../manifest.yaml" leaves its directory`}},
		{name: "INT-01 configuration not found", args: []string{"integrity", "--config", "proctor/missing.yaml"}, code: 4, want: []string{"INT-01 proctor/missing.yaml: not found"}},
		{name: "INT-07 configuration is a symlink", change: symlink("proctor/proctor.yaml"), args: integrity, code: 4,
			want: []string{"INT-07 proctor/proctor.yaml: cannot read", "INT-13 proctor/proctor.yaml: protected input is a symlink"}},
	}
	runRuleCases(t, cases)
}

// TestValidationRulesCommit covers the checks that need Git (INT-08,
// INT-11, INT-13, INT-14) and which paths count as protected inputs.
func TestValidationRulesCommit(t *testing.T) {
	symlinkDir := func(r *testRepo, link, target string) {
		if err := os.MkdirAll(filepath.Join(r.dir, target), 0o755); err != nil {
			r.t.Fatal(err)
		}
		r.write(target+"/a.yaml", "a: 1\n")
		if err := os.Symlink(filepath.Base(target), filepath.Join(r.dir, link)); err != nil {
			r.t.Fatal(err)
		}
	}
	cases := []ruleCase{
		{name: "INT-08 unknown commit", args: []string{"integrity", "--commit", "no-such-revision"}, code: 4, want: []string{`INT-08 commit "no-such-revision" is not available in this repository`}},
		{name: "INT-13 directory named twice is a symlink", config: configYAML + "protected_paths: [deploy/a.yaml, deploy/b.yaml]\n",
			change: func(r *testRepo) { symlinkDir(r, "deploy", "real-deploy") }, args: []string{"integrity"}, code: 4,
			want: []string{"INT-13 deploy: should be a directory holding protected inputs, but is a symlink"}},
		{name: "INT-13 unprotected symlink", change: func(r *testRepo) {
			if err := os.Symlink("main.go", filepath.Join(r.dir, "app/link.go")); err != nil {
				r.t.Fatal(err)
			}
		}, args: []string{"integrity"}},
		{name: "INT-13 submodule", change: func(r *testRepo) {
			nested := &testRepo{t: r.t, dir: filepath.Join(r.dir, "proctor", "vendored")}
			if err := os.MkdirAll(nested.dir, 0o755); err != nil {
				r.t.Fatal(err)
			}
			nested.git("init", "-q")
			nested.git("-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "-q", "--allow-empty", "-m", "x")
		}, args: []string{"integrity"}, code: 4, want: []string{"INT-13 proctor/vendored: protected input is a submodule"}},
		{name: "INT-11 one CODEOWNERS file", change: func(r *testRepo) { r.write(".github/CODEOWNERS", "/proctor/ @example-org/owners\n") }, args: []string{"integrity"}},
		{name: "INT-14 attribute value", change: func(r *testRepo) { r.write(".gitattributes", "proctor/specs/** -diff\n") }, args: []string{"integrity"}, code: 4,
			want: []string{`INT-14 proctor/specs/project-access.yaml: Git attribute "diff" is unset on a protected path`}},
		{name: "INT-14 only protected input", change: func(r *testRepo) {
			r.remove("proctor")
			r.write(".gitattributes", ".gitattributes -diff\n")
		}, args: []string{"integrity"}, code: 4,
			want: []string{"INT-01 proctor/proctor.yaml: not found", `INT-14 .gitattributes: Git attribute "diff" is unset on a protected path`}},
	}
	runRuleCases(t, cases)

	// The base repository has no unlisted specification: listed ones and
	// files outside the protected root aren't unlisted.
	t.Run("INT-05 nothing unlisted", func(t *testing.T) {
		r := newRepo(t)
		r.write(".github/workflows/proctor.yml", "on: pull_request\n")
		r.commit("workflow")
		code, out, errOut := r.proctor("integrity", "--json")
		rep := expectFindings(t, code, out, errOut, 0)
		if len(rep.Report.Unlisted) != 0 {
			t.Errorf("unlisted %v, want none", rep.Report.Unlisted)
		}
	})

	// A protected blob missing from the object database is INT-07, and the
	// file isn't examined further.
	t.Run("INT-07 blob missing", func(t *testing.T) {
		r := newRepo(t)
		r.write("proctor/specs/draft.yaml", "version: 1\n")
		r.commit("draft")
		oid := r.git("rev-parse", "HEAD:proctor/specs/draft.yaml")
		if err := os.Remove(filepath.Join(r.dir, ".git", "objects", oid[:2], oid[2:])); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := r.proctor("integrity", "--json")
		rep := expectFindings(t, code, out, errOut, 4, "INT-07 proctor/specs/draft.yaml: cannot read blob: git cat-file: fatal:")
		if len(rep.Report.Unlisted) != 0 {
			t.Errorf("unlisted %v, want none", rep.Report.Unlisted)
		}
	})

	// INT-08 shows why Git failed, even when Git wrote nothing to stderr.
	t.Run("INT-08 Git's exit status", func(t *testing.T) {
		r := newRepo(t)
		code, out, errOut := r.proctor("integrity", "--commit", "no-such-revision", "--json")
		expectFindings(t, code, out, errOut, 4, "INT-08 git rev-parse: exit status")
	})
}

// TestValidationRulesProtectedPaths: an entry of protected_paths protects
// that path, or everything under it when it ends in /, and nothing else.
func TestValidationRulesProtectedPaths(t *testing.T) {
	r := newRepo(t)
	r.write("proctor/proctor.yaml", configYAML+"protected_paths: [compose.verify.yaml, deploy/]\n")
	base := r.commit("protect")
	r.write("compose.verify.yaml.bak", "services: {}\n")
	r.write("deployment.yaml", "x: 1\n")
	r.write("app/main.go", "package main // changed\n")
	head := r.commit("near misses")
	code, out, errOut := r.proctor("diff", "--base", base, "--head", head)
	expect(t, code, out, errOut, 0, "Protected changes against base: none")

	r.write("deploy/app.yaml", "x: 1\n")
	r.write("compose.verify.yaml", "services: {}\n")
	head = r.commit("protected")
	code, out, errOut = r.proctor("diff", "--base", base, "--head", head)
	expect(t, code, out, errOut, 0, "added     compose.verify.yaml", "added     deploy/app.yaml", "Protected changes against base: 2")
}

// TestValidationRulesTestMergeParents: only the commit header names the
// parents; a message line that starts with "parent " doesn't.
func TestValidationRulesTestMergeParents(t *testing.T) {
	r := newRepo(t)
	r.git("switch", "-q", "-c", "feature")
	r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "status: 401", "status: 403", 1))
	prHead := r.commit("pr change")
	r.git("switch", "-q", "main")
	r.write("app/main.go", "package main // main moved on\n")
	other := r.commit("main change")
	r.git("merge", "-q", "--no-ff", "-m", "test merge", "-m", "parent "+other, "feature")
	merge := r.git("rev-parse", "HEAD")
	code, out, errOut := r.proctor("diff", "--head", merge, "--pr-head", prHead)
	expect(t, code, out, errOut, 0, "modified  proctor/specs/project-access.yaml", "Protected changes against base: 1")
}

// TestValidationRulesComparisons covers how a run selects and compares
// values (HTTP-03, HTTP-08) and expands references (HTTP-02).
func TestValidationRulesComparisons(t *testing.T) {
	cases := []struct {
		name, body, expect string
		want               int
	}{
		{"absent at an index equal to the length", `{"list": [1, 2]}`, "          absent: [\"$.list[2]\"]\n", 0},
		{"present at the last index", `{"list": [1, 2]}`, "          present: [\"$.list[1]\"]\n", 0},
		{"absent under a scalar", `{"a": 1}`, "          absent: [$.a.x]\n", 0},
		{"false isn't 0", `{"flag": 0}`, "          json:\n            $.flag: false\n", 1},
		{"false isn't a string", `{"flag": "false"}`, "          json:\n            $.flag: false\n", 1},
		{"false matches false", `{"flag": false}`, "          json:\n            $.flag: false\n", 0},
		{"object member differs", `{"obj": {"x": 1, "y": "three"}}`, "          json:\n            $.obj: { x: 1, y: two }\n", 1},
		{"object member differs first", `{"obj": {"x": 2, "y": "two"}}`, "          json:\n            $.obj: { x: 1, y: two }\n", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut, _ := runOne(t, fixedApp(c.body), getSpec(c.expect))
			expect(t, code, out, errOut, c.want)
		})
	}

	t.Run("saved object inserted as JSON text", func(t *testing.T) {
		h := func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/obj" {
				io.WriteString(w, `{"v": {"a": 1}}`)
				return
			}
			b, _ := json.Marshal(map[string]string{"q": r.URL.Query().Get("q")})
			w.Write(b)
		}
		s := spec(st("obj", "method: GET, path: /obj, identity: anonymous", "status: 200, present: [$.v]", "save: { v: $.v }"),
			st("echo", `method: GET, path: /echo, identity: anonymous, query: { q: "${v}" }`, `status: 200, json: { $.q: '{"a":1}' }`))
		code, out, errOut, _ := runOne(t, h, s)
		expect(t, code, out, errOut, 0)
	})

	t.Run("fill at the limit is sent", func(t *testing.T) {
		h := func(w http.ResponseWriter, r *http.Request) {
			n, _ := io.Copy(io.Discard, r.Body)
			fmt.Fprintf(w, `{"size": %d}`, n)
		}
		s := spec(st("send", `method: POST, path: /echo, identity: anonymous, body: { p: "${fill(16777216)}" }`, "status: 200, json: { $.size: 16777224 }", "no_state_check: the app keeps nothing"))
		code, out, errOut, _ := runOne(t, h, s)
		expect(t, code, out, errOut, 0)
	})
}
