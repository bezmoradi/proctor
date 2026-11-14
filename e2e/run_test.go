package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeApp is an application under test. It records every request it gets.
type fakeApp struct {
	t    *testing.T
	srv  *httptest.Server
	mu   sync.Mutex
	seen []*http.Request
}

func newApp(t *testing.T, h http.HandlerFunc) *fakeApp {
	t.Helper()
	a := &fakeApp{t: t}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.seen = append(a.seen, r.Clone(r.Context()))
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		h(w, r)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *fakeApp) requests() []*http.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]*http.Request(nil), a.seen...)
}

// projectsApp behaves as the fixture specification expects: alice can
// create and read projects, anonymous users can't list them.
func projectsApp(name string, bobStatus int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/projects":
			if auth == "Bearer tok-bob" && bobStatus != 0 {
				w.WriteHeader(bobStatus)
				return
			}
			w.WriteHeader(201)
			fmt.Fprintf(w, `{"id":"p1","name":%q}`, name)
		case r.Method == "GET" && r.URL.Path == "/api/projects/p1":
			fmt.Fprintf(w, `{"id":"p1","name":%q}`, name)
		case r.Method == "GET" && r.URL.Path == "/api/projects" && auth == "":
			w.WriteHeader(401)
		default:
			w.WriteHeader(404)
		}
	}
}

// binding writes a binding file outside the repository and returns its path.
func binding(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "local.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func bindingFor(t *testing.T, base string) string {
	return binding(t, fmt.Sprintf(`target:
  base_url: %s
  user_agent: proctor-test/1.0
identities:
  alice: { token_env: TEST_ALICE_TOKEN }
  bob: { token_env: TEST_BOB_TOKEN }
`, base))
}

// withTokens sets the identities' token variables for the proctor process.
func withTokens(t *testing.T) {
	t.Helper()
	t.Setenv("TEST_ALICE_TOKEN", "tok-alice")
	t.Setenv("TEST_BOB_TOKEN", "tok-bob")
}

type runReport struct {
	Result    string `json:"result"`
	ExitCode  int    `json:"exit_code"`
	Scenarios []struct {
		ID     string `json:"id"`
		Result string `json:"result"`
		Reason string `json:"reason"`
		Steps  []struct {
			ID         string `json:"id"`
			Result     string `json:"result"`
			Reason     string `json:"reason"`
			Assertions []struct {
				Target   string `json:"target"`
				Expected any    `json:"expected"`
				Actual   any    `json:"actual"`
				Result   string `json:"result"`
			} `json:"assertions"`
		} `json:"steps"`
	} `json:"scenarios"`
}

func runJSON(t *testing.T, r *testRepo, bindingPath string) (int, runReport, string) {
	t.Helper()
	code, out, errOut := r.proctor("run", "--binding", bindingPath, "--json")
	var rep runReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("report isn't JSON: %v\n%s\n%s", err, out, errOut)
	}
	if rep.ExitCode != code {
		t.Fatalf("report exit_code %d, process exit %d", rep.ExitCode, code)
	}
	return code, rep, out
}

func scenario(t *testing.T, rep runReport, id string) (string, string, []string) {
	t.Helper()
	for _, s := range rep.Scenarios {
		if s.ID == id {
			var steps []string
			for _, st := range s.Steps {
				steps = append(steps, st.ID+":"+st.Result+":"+st.Reason)
			}
			return s.Result, s.Reason, steps
		}
	}
	t.Fatalf("scenario %s missing from the report", id)
	return "", "", nil
}

// §13.3, AT-Q-22, REP-04: a passing run, the binding's User-Agent and the
// identity's token reach the application, and the token isn't reported.
func TestRunPasses(t *testing.T) {
	withTokens(t)
	app := newApp(t, projectsApp("Alice's project", 0))
	r := newRepo(t)
	code, rep, out := runJSON(t, r, bindingFor(t, app.srv.URL))
	if code != 0 || rep.Result != "passed" {
		t.Fatalf("exit %d, result %s\n%s", code, rep.Result, out)
	}
	for _, id := range []string{"PA-001", "PA-002"} {
		if res, _, _ := scenario(t, rep, id); res != "PASS" {
			t.Errorf("%s: %s", id, res)
		}
	}
	reqs := app.requests()
	if len(reqs) != 4 {
		t.Fatalf("got %d requests, want 4", len(reqs))
	}
	for _, q := range reqs {
		if ua := q.Header.Get("User-Agent"); ua != "proctor-test/1.0" {
			t.Errorf("User-Agent %q", ua)
		}
	}
	if got := reqs[0].Header.Get("Authorization"); got != "Bearer tok-alice" {
		t.Errorf("alice's request carried Authorization %q", got)
	}
	if got := reqs[3].Header.Get("Authorization"); got != "" {
		t.Errorf("anonymous request carried Authorization %q", got)
	}
	if strings.Contains(out, "tok-alice") || strings.Contains(out, "tok-bob") {
		t.Error("a token appears in the report")
	}

	code, text, _ := r.proctor("run", "--binding", bindingFor(t, app.srv.URL))
	expect(t, code, text, "", 0, "(local, non-authoritative)", "PASS     PA-001", "RESULT: PASSED (exit 0, local, non-authoritative)")
}

// §13.3, INT-03: a failed assertion fails the scenario; its later steps are
// NOT_RUN with a reason, never dropped.
func TestRunFailureStopsTheScenario(t *testing.T) {
	withTokens(t)
	app := newApp(t, projectsApp("Someone else's project", 0))
	code, rep, _ := runJSON(t, newRepo(t), bindingFor(t, app.srv.URL))
	res, _, steps := scenario(t, rep, "PA-001")
	if code != 1 || res != "FAIL" {
		t.Fatalf("exit %d, PA-001 %s", code, res)
	}
	want := "create:FAIL: read:NOT_RUN:earlier_step_failed"
	if got := strings.Join(steps, " "); got != want {
		t.Errorf("steps %q, want %q", got, want)
	}
}

// FIX-04, AT-R-01: a failed setup is ERROR (exit 2), not FAIL; REP-07: a
// failure elsewhere still makes the run exit 1.
func TestRunSetupFailure(t *testing.T) {
	withTokens(t)
	app := newApp(t, projectsApp("Alice's project", 500))
	code, rep, _ := runJSON(t, newRepo(t), bindingFor(t, app.srv.URL))
	res, reason, steps := scenario(t, rep, "PA-002")
	if code != 2 || res != "ERROR" || reason != "setup_failed" {
		t.Fatalf("exit %d, PA-002 %s %s", code, res, reason)
	}
	if got := strings.Join(steps, " "); got != "seed:ERROR:setup_failed list:NOT_RUN:setup_failed" {
		t.Errorf("steps %q", got)
	}

	app = newApp(t, projectsApp("Someone else's project", 500))
	code, _, _ = runJSON(t, newRepo(t), bindingFor(t, app.srv.URL))
	if code != 1 {
		t.Errorf("a FAIL plus an ERROR exited %d, want 1 (REP-07)", code)
	}
}

// §13.3: an unreachable application is ERROR (exit 2) for every scenario.
func TestRunUnreachableTarget(t *testing.T) {
	withTokens(t)
	app := newApp(t, projectsApp("x", 0))
	url := app.srv.URL
	app.srv.Close()
	code, rep, _ := runJSON(t, newRepo(t), bindingFor(t, url))
	if code != 2 || len(rep.Scenarios) != 2 {
		t.Fatalf("exit %d, %d scenarios", code, len(rep.Scenarios))
	}
	for _, s := range rep.Scenarios {
		if s.Result != "ERROR" {
			t.Errorf("%s: %s", s.ID, s.Result)
		}
	}
}

// HTTP-04, SEC-02, MODE-01: binding problems exit 3 before any request.
func TestRunBindingErrors(t *testing.T) {
	cases := map[string]struct {
		binding string
		tokens  bool
	}{
		"no user agent":        {"target:\n  base_url: %s\nidentities:\n  alice: { token_env: TEST_ALICE_TOKEN }\n  bob: { token_env: TEST_BOB_TOKEN }\n", true},
		"token variable unset": {"target:\n  base_url: %s\n  user_agent: t/1\nidentities:\n  alice: { token_env: TEST_ALICE_TOKEN }\n  bob: { token_env: TEST_BOB_TOKEN }\n", false},
		"no token_env":         {"target:\n  base_url: %s\n  user_agent: t/1\nidentities:\n  alice: { token_env: TEST_ALICE_TOKEN }\n", true},
		"unknown key":          {"target:\n  base_url: %s\n  user_agent: t/1\n  retries: 3\n", true},
		"undeclared identity":  {"target:\n  base_url: %s\n  user_agent: t/1\nidentities:\n  alice: { token_env: TEST_ALICE_TOKEN }\n  bob: { token_env: TEST_BOB_TOKEN }\n  carol: { token_env: X }\n", true},
		"base url with query":  {"target:\n  base_url: %s/?x=1\n  user_agent: t/1\nidentities:\n  alice: { token_env: TEST_ALICE_TOKEN }\n  bob: { token_env: TEST_BOB_TOKEN }\n", true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if c.tokens {
				withTokens(t)
			} else {
				t.Setenv("TEST_ALICE_TOKEN", "")
				t.Setenv("TEST_BOB_TOKEN", "")
			}
			app := newApp(t, projectsApp("x", 0))
			code, out, errOut := newRepo(t).proctor("run", "--binding", binding(t, fmt.Sprintf(c.binding, app.srv.URL)))
			expect(t, code, out, errOut, 3, "Nothing was sent to the application")
			if n := len(app.requests()); n != 0 {
				t.Errorf("%d requests reached the application", n)
			}
		})
	}

	code, _, errOut := newRepo(t).proctor("run")
	expect(t, code, "", errOut, 3, "--binding is required")
}

// Phase 0 supports identities of type none and bearer only.
func TestRunUnsupportedIdentityType(t *testing.T) {
	withTokens(t)
	app := newApp(t, projectsApp("x", 0))
	r := newRepo(t)
	r.write("proctor/proctor.yaml", strings.Replace(configYAML, "bob: { type: bearer }", "bob: { type: basic }", 1))
	code, out, errOut := r.proctor("run", "--binding", bindingFor(t, app.srv.URL))
	expect(t, code, out, errOut, 3, `type "basic" is not supported yet`)
}

// HTTP-05, AT-Q-37, AUTH-02, HTTP-02, OD-10: invalid specifications exit 3
// and nothing reaches the application.
func TestRunRejectsInvalidSpecifications(t *testing.T) {
	step := func(path, extra string) string {
		return `version: 1
suite: s
scenarios:
  - id: S-1
    name: one step
    steps:
      - id: only
        request:
          method: GET
          path: ` + path + `
          identity: anonymous
` + extra + `        expect:
          status: 404
`
	}
	cases := map[string]struct{ spec, rule string }{
		"absolute URL":         {step("https://other.example/x", ""), "HTTP-05"},
		"host prefix":          {step("//other.example/x", ""), "HTTP-05"},
		"dot-dot segment":      {step("/a/../b", ""), "HTTP-05"},
		"encoded slash":        {step("/a%2Fb", ""), "HTTP-05"},
		"relative path":        {step("a/b", ""), "HTTP-05"},
		"query in path":        {step("/x?debug=1", ""), "HTTP-05"},
		"authorization header": {step("/x", "          headers:\n            Authorization: Bearer abc\n"), "AUTH-02"},
		"host header":          {step("/x", "          headers:\n            Host: other.example\n"), "HTTP-05"},
		"unsaved variable":     {step("/x/${missing}", ""), "HTTP-02"},
		"other namespace":      {step("/x/${steps.only.id}", ""), "HTTP-02"},
		"unsupported JSONPath": {strings.Replace(step("/x", ""), "status: 404", "status: 404\n          json:\n            $..name: x", 1), "EXE-03"},
		"retry option":         {strings.Replace(step("/x", ""), "    steps:", "    retry: 3\n    steps:", 1), "INT-15"},
	}
	// HTTP-06, AT-Q-39.
	for h, rule := range map[string]string{"User-Agent": "HTTP-04", "Proxy-Authorization": "AUTH-02", "Cookie": "AUTH-02",
		"Content-Length": "EXE-01", "Transfer-Encoding": "EXE-01", "Connection": "EXE-01"} {
		cases[h+" header"] = struct{ spec, rule string }{step("/x", "          headers:\n            "+h+": v\n"), rule}
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			withTokens(t)
			app := newApp(t, projectsApp("x", 0))
			r := newRepo(t)
			r.write("proctor/specs/project-access.yaml", c.spec)
			code, out, errOut := r.proctor("run", "--binding", bindingFor(t, app.srv.URL))
			expect(t, code, out, errOut, 3, c.rule)
			if n := len(app.requests()); n != 0 {
				t.Errorf("%d requests reached the application", n)
			}
		})
	}
}

// INT-01, INT-04: an incomplete set is an integrity violation before any
// request.
func TestRunIntegrityViolation(t *testing.T) {
	withTokens(t)
	app := newApp(t, projectsApp("x", 0))
	r := newRepo(t)
	r.remove("proctor/specs/project-access.yaml")
	code, out, errOut := r.proctor("run", "--binding", bindingFor(t, app.srv.URL))
	expect(t, code, out, errOut, 4, "INT-01")
	if n := len(app.requests()); n != 0 {
		t.Errorf("%d requests reached the application", n)
	}
}

const echoSpec = `version: 1
suite: s
scenarios:
  - id: S-1
    name: a saved value goes back unchanged
    steps:
      - id: create
        request:
          method: POST
          path: /items
          identity: anonymous
          body: { name: n }
        expect:
          status: 201
          json:
            $.name: n
        save:
          item_id: $.id
      - id: read
        request:
          method: GET
          path: /items/${item_id}
          identity: anonymous
        expect:
          status: 200
          json:
            $.id: "${item_id}"
`

func itemsApp(id string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(201)
			b, _ := json.Marshal(map[string]string{"id": id, "name": "n"})
			w.Write(b)
			return
		}
		b, _ := json.Marshal(map[string]string{"id": strings.TrimPrefix(r.URL.Path, "/items/")})
		w.Write(b)
	}
}

// HTTP-02, AT-Q-01: a saved value that looks like a reference is data.
// HTTP-05: a saved value is one path segment and can't climb out.
func TestRunSavedValues(t *testing.T) {
	cases := map[string]struct {
		id, wantRaw string
		wantCode    int
	}{
		"reference-like value": {"${secret}", "/items/$%7Bsecret%7D", 0},
		"slashes":              {"a/../../admin", "/items/a%2F..%2F..%2Fadmin", 0},
		"dot-dot":              {"..", "", 2},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			app := newApp(t, itemsApp(c.id))
			r := newRepo(t)
			r.write("proctor/specs/project-access.yaml", echoSpec)
			code, rep, out := runJSON(t, r, bindingFor(t, app.srv.URL))
			if code != c.wantCode {
				t.Fatalf("exit %d, want %d\n%s", code, c.wantCode, out)
			}
			reqs := app.requests()
			if c.wantRaw == "" {
				if len(reqs) != 1 {
					t.Errorf("%d requests, want only the create", len(reqs))
				}
				return
			}
			if got := reqs[1].URL.EscapedPath(); got != c.wantRaw {
				t.Errorf("read went to %q, want %q", got, c.wantRaw)
			}
			if res, _, _ := scenario(t, rep, "S-1"); res != "PASS" {
				t.Errorf("S-1 %s", res)
			}
		})
	}
}

// HTTP-03: comparisons are type-strict; numbers compare by value.
func TestRunTypeStrictComparison(t *testing.T) {
	spec := func(want string) string {
		return `version: 1
suite: s
scenarios:
  - id: S-1
    name: count
    steps:
      - id: read
        request: { method: GET, path: /count, identity: anonymous }
        expect:
          status: 200
          json:
            $.count: ` + want + `
            $.items[1].ok: true
`
	}
	app := newApp(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"count": 7, "items": [{"ok": false}, {"ok": true}]}`)
	})
	for want, code := range map[string]int{"7": 0, "7.0": 0, `"7"`: 1, "8": 1} {
		r := newRepo(t)
		r.write("proctor/specs/project-access.yaml", spec(want))
		got, _, out := runJSON(t, r, bindingFor(t, app.srv.URL))
		if got != code {
			t.Errorf("expected %s: exit %d, want %d\n%s", want, got, code, out)
		}
	}
}

// HTTP-01, SEC-07: a redirect is judged as received and never followed.
func TestRunDoesNotFollowRedirects(t *testing.T) {
	other := newApp(t, func(w http.ResponseWriter, r *http.Request) {})
	app := newApp(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", other.srv.URL+"/landing")
		w.WriteHeader(302)
	})
	r := newRepo(t)
	r.write("proctor/specs/project-access.yaml", `version: 1
suite: s
scenarios:
  - id: S-1
    name: redirect
    steps:
      - id: go
        request: { method: GET, path: /redirect-external, identity: anonymous }
        expect:
          status: 302
          headers:
            Location: `+other.srv.URL+`/landing
`)
	code, _, out := runJSON(t, r, bindingFor(t, app.srv.URL))
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if n := len(other.requests()); n != 0 {
		t.Errorf("the redirect was followed: %d requests reached the other host", n)
	}
}

// FIX-07 (Phase 0 part): a target that never becomes ready leaves every
// scenario NOT_RUN with environment_not_ready (exit 2).
func TestRunReadiness(t *testing.T) {
	withTokens(t)
	app := newApp(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	r := newRepo(t)
	r.write("proctor/proctor.yaml", configYAML+"readiness:\n  path: /ready\n  startup_timeout: 300ms\n")
	code, rep, out := runJSON(t, r, bindingFor(t, app.srv.URL))
	if code != 2 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, s := range rep.Scenarios {
		if s.Result != "NOT_RUN" || s.Reason != "environment_not_ready" {
			t.Errorf("%s: %s %s", s.ID, s.Result, s.Reason)
		}
	}
	for _, q := range app.requests() {
		if q.URL.Path != "/ready" {
			t.Errorf("a scenario request was sent before the target was ready: %s", q.URL.Path)
		}
	}
}

// REP-06: values of fields named like credentials are redacted in reports.
func TestRunRedactsCredentialFields(t *testing.T) {
	app := newApp(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token": "s3cr3t-value", "name": "n"}`)
	})
	r := newRepo(t)
	r.write("proctor/specs/project-access.yaml", `version: 1
suite: s
scenarios:
  - id: S-1
    name: login
    steps:
      - id: login
        request: { method: POST, path: /login, identity: anonymous }
        no_state_check: a login creates no state the API can read back
        expect:
          status: 200
          json:
            $.access_token: wrong-value
`)
	code, _, out := runJSON(t, r, bindingFor(t, app.srv.URL))
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(out, "s3cr3t-value") || strings.Contains(out, "wrong-value") {
		t.Errorf("a credential-like value appears in the report:\n%s", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Error("no redaction marker in the report")
	}
}
