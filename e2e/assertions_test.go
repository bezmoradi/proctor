package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests cover the checks added after mutation round 1: exact JSON
// matching (HTTP-07), present, absent, keys and length (HTTP-08, HTTP-09),
// ambiguous JSON (HTTP-10), ${run.id} (FIX-05), the state-check rule
// (EXE-07), setup operations exercised as steps (FIX-08), setup-failure
// causes (FIX-04), credential variants (AUTH-06, AUTH-03), and injected
// network faults, which must never give PASS.

// specRepo is a repository whose only specification is spec; config
// replaces proctor.yaml when it isn't empty.
func specRepo(t *testing.T, config, spec string) *testRepo {
	t.Helper()
	r := newRepo(t)
	if config != "" {
		r.write("proctor/proctor.yaml", config)
	}
	r.write("proctor/specs/project-access.yaml", spec)
	return r
}

// getSpec is one scenario with one GET step expecting 200 and the given
// expectation lines (indented under expect:).
func getSpec(expect string) string {
	return `version: 1
suite: s
scenarios:
  - id: S-1
    name: one read
    steps:
      - id: read
        request: { method: GET, path: /doc, identity: anonymous }
        expect:
          status: 200
` + expect
}

func fixedApp(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }
}

func runOne(t *testing.T, h http.HandlerFunc, spec string, args ...string) (int, string, string, *fakeApp) {
	t.Helper()
	withTokens(t)
	app := newApp(t, h)
	r := specRepo(t, "", spec)
	code, out, errOut := r.proctor(append([]string{"run", "--binding", bindingFor(t, app.srv.URL)}, args...)...)
	return code, out, errOut, app
}

// HTTP-07, HTTP-08, HTTP-09: each assertion passes only on an exact match.
func TestAssertionsAreExact(t *testing.T) {
	doc := `{"a": 1, "b": null, "list": [1, 2], "obj": {"x": 1, "y": "two"}}`
	cases := []struct {
		name, expect string
		want         int
	}{
		{"whole object matches", "          json:\n            $.obj: { x: 1, y: two }\n", 0},
		{"object with a missing key fails", "          json:\n            $.obj: { x: 1 }\n", 1},
		{"object with an extra key fails", "          json:\n            $.obj: { x: 1, y: two, z: 3 }\n", 1},
		{"whole document matches", "          json:\n            $: { a: 1, b: null, list: [1, 2], obj: { x: 1, y: two } }\n", 0},
		{"array in another order fails", "          json:\n            $.list: [2, 1]\n", 1},
		{"array prefix fails", "          json:\n            $.list: [1]\n", 1},
		{"null is present", "          present: [$.b]\n", 0},
		{"a missing field isn't present", "          present: [$.c]\n", 1},
		{"a missing field is absent", "          absent: [$.c, $.obj.z, \"$.list[5]\"]\n", 0},
		{"a null field isn't absent", "          absent: [$.b]\n", 1},
		{"keys match exactly", "          keys:\n            $: [a, b, list, obj]\n            $.obj: [y, x]\n", 0},
		{"an unexpected key fails", "          keys:\n            $.obj: [x]\n", 1},
		{"a missing key fails", "          keys:\n            $.obj: [x, y, z]\n", 1},
		{"keys of a non-object fail", "          keys:\n            $.list: []\n", 1},
		{"length matches", "          length:\n            $.list: 2\n", 0},
		{"length differs", "          length:\n            $.list: 3\n", 1},
		{"length of a non-array fails", "          length:\n            $.obj: 2\n", 1},
		{"string is not a number", "          json:\n            $.a: \"1\"\n", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A query parameter, so the length cases satisfy HTTP-09's
			// filtered-list rule.
			spec := strings.Replace(getSpec(c.expect), "identity: anonymous }", `identity: anonymous, query: { v: "1" } }`, 1)
			code, out, errOut, _ := runOne(t, fixedApp(doc), spec)
			expect(t, code, out, errOut, c.want)
		})
	}
}

// HTTP-08, HTTP-09: contradictory or malformed assertions are invalid.
func TestAssertionValidation(t *testing.T) {
	cases := map[string]struct{ expect, rule string }{
		"absent and asserted":       {"          absent: [$.a]\n          json:\n            $.a: 1\n", "HTTP-08"},
		"absent and present":        {"          absent: [$.a]\n          present: [$.a]\n", "HTTP-08"},
		"negative length":           {"          length:\n            $.list: -1\n", "HTTP-09"},
		"bad present path":          {"          present: [a.b]\n", "HTTP-08"},
		"duplicate key in keys":     {"          keys:\n            $: [a, a]\n", "HTTP-08"},
		"non-finite expected value": {"          json:\n            $.a: .inf\n", "EXE-03"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			code, out, errOut, app := runOne(t, fixedApp(`{}`), getSpec(c.expect))
			expect(t, code, out, errOut, 3, c.rule)
			if n := len(app.requests()); n != 0 {
				t.Errorf("%d requests reached the application", n)
			}
		})
	}
}

// HTTP-10: a body with a duplicate key, or more than one JSON value, can't
// pass, whatever the value Go's parser would keep.
func TestAmbiguousJSONFails(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate top-level key": `{"a": 1, "a": 2}`,
		"duplicate nested key":    `{"a": 2, "o": {"k": 1, "k": 1}}`,
		"duplicate in array item": `{"a": 2, "l": [{"k": 1, "k": 2}]}`,
		"two values":              `{"a": 2} {"a": 2}`,
		"trailing garbage":        `{"a": 2} x`,
		"not JSON":                `<html>a: 2</html>`,
		"empty body":              ``,
		"truncated":               `{"a": 2`,
	} {
		t.Run(name, func(t *testing.T) {
			code, out, errOut, _ := runOne(t, fixedApp(body), getSpec("          json:\n            $.a: 2\n"))
			expect(t, code, out, errOut, 1)
		})
	}
	t.Run("same key in sibling objects is fine", func(t *testing.T) {
		code, out, errOut, _ := runOne(t, fixedApp(`{"a": 2, "l": [{"k": 1}, {"k": 2}], "o": {"k": 3}}`), getSpec("          json:\n            $.a: 2\n"))
		expect(t, code, out, errOut, 0)
	})
}

// FIX-05: ${run.id} is the run's ID followed by the scenario's ID, so it
// differs between scenarios and between runs.
func TestRunIDIsUniquePerScenarioAndRun(t *testing.T) {
	var mu sync.Mutex
	var names []string
	h := func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		if r.Method == "POST" {
			mu.Lock()
			names = append(names, b.Name)
			mu.Unlock()
			w.WriteHeader(201)
		}
		fmt.Fprintf(w, `{"name": %q, "path": %q}`, b.Name, r.URL.Path)
	}
	scenario := func(id string) string {
		return `
  - id: ` + id + `
    name: create
    steps:
      - id: create
        request: { method: POST, path: "/items/${run.id}", identity: anonymous, body: { name: "item ${run.id}" } }
        no_state_check: the fake app keeps nothing
        expect:
          status: 201
          json:
            $.name: "item ${run.id}"
            $.path: "/items/${run.id}"
`
	}
	spec := "version: 1\nsuite: s\nscenarios:" + scenario("A-1") + scenario("B.2")
	withTokens(t)
	app := newApp(t, h)
	r := specRepo(t, "", spec)
	b := bindingFor(t, app.srv.URL)
	for i := 0; i < 2; i++ {
		code, out, errOut := r.proctor("run", "--binding", b)
		expect(t, code, out, errOut, 0)
	}
	re := regexp.MustCompile(`^item [0-9a-f]{12}-(A-1|B\.2)$`)
	seen := map[string]bool{}
	for _, n := range names {
		if !re.MatchString(n) {
			t.Errorf("name %q doesn't have the form item <12 hex>-<scenario>", n)
		}
		if seen[n] {
			t.Errorf("name %q was used twice", n)
		}
		seen[n] = true
	}
	if len(names) != 4 {
		t.Errorf("got %d creates, want 4", len(names))
	}
}

// FIX-05, HTTP-09 (REQ-37: every warning is a failure): a value sent and
// matched later without ${run.id}, or a length asserted on an unfiltered
// list, is invalid in validate and run, unless the step waives the rule
// with a written reason. A waiver needs a reason, must name a waivable
// rule, and must have something to waive.
func TestRunScopedValueRequired(t *testing.T) {
	spec := func(name, waive string) string {
		return `version: 1
suite: s
scenarios:
  - id: S-1
    name: create and read
    steps:
      - id: create
        request: { method: POST, path: /api/projects, identity: alice, body: { name: ` + name + ` } }
` + waive + `        expect:
          status: 201
          json:
            $.name: ` + name + `
      - id: read
        request: { method: GET, path: /api/projects/p1, identity: alice }
        expect:
          status: 200
          json:
            $.name: ` + name + `
`
	}
	list := func(query, waive string) string {
		return `version: 1
suite: s
scenarios:
  - id: S-1
    name: list
    steps:
      - id: list
        request: { method: GET, path: /api/projects, identity: alice` + query + ` }
` + waive + `        expect:
          status: 200
          length:
            $.items: 1
`
	}
	cases := []struct {
		name, spec string
		want       int
		says       []string
	}{
		{"value without run.id", spec("Fixed", ""), 3, []string{"INVALID", "FIX-05", `sends "Fixed"`}},
		{"value with run.id", spec(`"n ${run.id}"`, ""), 0, nil},
		{"FIX-05 waived with a reason", spec("Fixed", "        waive: { FIX-05: the API accepts only this fixed value }\n"), 0, nil},
		{"FIX-05 waiver without a reason", spec("Fixed", "        waive: { FIX-05: \"  \" }\n"), 3, []string{"FIX-05", "must state a reason"}},
		{"FIX-05 waiver with nothing to waive", spec(`"n ${run.id}"`, "        waive: { FIX-05: not needed }\n"), 3, []string{"FIX-05", "doesn't apply"}},
		{"waiver for a rule that can't be waived", spec("Fixed", "        waive: { FIX-05: fixed value, EXE-07: no reason }\n"), 3, []string{"EXE-07", "only FIX-05 and HTTP-09"}},
		{"length on an unfiltered list", list("", ""), 3, []string{"INVALID", "HTTP-09", "no query parameters"}},
		{"length on a filtered list", list(", query: { owner: \"${run.id}\" }", ""), 0, nil},
		{"HTTP-09 waived with a reason", list("", "        waive: { HTTP-09: the environment holds only this scenario's data }\n"), 0, nil},
		{"HTTP-09 waiver with nothing to waive", list(", query: { owner: \"${run.id}\" }", "        waive: { HTTP-09: not needed }\n"), 3, []string{"HTTP-09", "doesn't apply"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := specRepo(t, "", c.spec)
			code, out, errOut := r.proctor("validate")
			expect(t, code, out, errOut, c.want, c.says...)
			if strings.Contains(out, "WARNING") {
				t.Errorf("output has a warning level:\n%s", out)
			}
		})
	}
	t.Run("run refuses it before sending anything", func(t *testing.T) {
		code, out, errOut, app := runOne(t, fixedApp(`{"name":"Fixed"}`), spec("Fixed", ""))
		expect(t, code, out, errOut, 3, "FIX-05")
		if n := len(app.requests()); n != 0 {
			t.Errorf("the application got %d requests", n)
		}
	})
}

// EXE-07: a state-changing verification step needs a later state check.
func TestStateCheckRequired(t *testing.T) {
	write := func(after, waiver string) string {
		return `version: 1
suite: s
scenarios:
  - id: S-1
    name: write
    steps:
      - id: create
        request: { method: POST, path: /api/projects, identity: alice, body: { name: "n ${run.id}" } }
` + waiver + `        expect:
          status: 201
          present: [$.id]
` + after
	}
	readExpecting := func(status int) string {
		return fmt.Sprintf(`      - id: read
        request: { method: GET, path: /api/projects/p1, identity: alice }
        expect:
          status: %d
          present: [$.id]
`, status)
	}
	cases := []struct {
		name, spec string
		want       int
	}{
		{"no later step", write("", ""), 3},
		{"later read expected to be refused", write(strings.Replace(readExpecting(401), "          present: [$.id]\n", "", 1), ""), 3},
		{"later read", write(readExpecting(200), ""), 0},
		{"later read expecting 404", write(strings.Replace(readExpecting(404), "          present: [$.id]\n", "", 1), ""), 0},
		{"waiver with a reason", write("", "        no_state_check: the create has no readable state\n"), 0},
		{"waiver without a reason", write("", "        no_state_check: \"  \"\n"), 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := specRepo(t, "", c.spec)
			code, out, errOut := r.proctor("validate")
			want := []string{}
			if c.want == 3 {
				want = append(want, "EXE-07")
			}
			expect(t, code, out, errOut, c.want, want...)
		})
	}
	t.Run("waiver on a read", func(t *testing.T) {
		r := specRepo(t, "", strings.Replace(getSpec("          present: [$.a]\n"), "identity: anonymous }\n", "identity: anonymous }\n        no_state_check: nothing\n", 1))
		code, out, errOut := r.proctor("validate")
		expect(t, code, out, errOut, 3, "EXE-07")
	})
}

// FIX-08: an operation used in setup must also be a verification step.
func TestSetupOperationMustBeVerified(t *testing.T) {
	spec := `version: 1
suite: s
scenarios:
  - id: S-1
    name: delete in setup only
    setup:
      - id: remove
        request: { method: DELETE, path: /api/projects/p1, identity: alice }
        expect: { status: 204 }
    steps:
      - id: read
        request: { method: GET, path: /api/projects/p1, identity: alice }
        expect: { status: 404 }
`
	r := specRepo(t, "", spec)
	code, out, errOut := r.proctor("validate")
	expect(t, code, out, errOut, 3, "FIX-08", "DELETE /api/projects/p1 expecting 204")
}

// FIX-04: a setup failure is ERROR with reason setup_failed and a cause
// that tells an application defect from the environment.
func TestSetupFailureCause(t *testing.T) {
	spec := `version: 1
suite: s
scenarios:
  - id: S-1
    name: setup then read
    setup:
      - id: seed
        request: { method: GET, path: /seed, identity: anonymous }
        expect: { status: 200 }
    steps:
      - id: read
        request: { method: GET, path: /seed, identity: anonymous }
        expect: { status: 200, present: [$.ok] }
`
	cause := func(t *testing.T, base string, args ...string) string {
		t.Helper()
		r := specRepo(t, "", spec)
		code, out, errOut := r.proctor(append([]string{"run", "--binding", bindingFor(t, base), "--json"}, args...)...)
		var rep struct {
			Scenarios []struct {
				Result, Reason, Cause string
			}
		}
		if err := json.Unmarshal([]byte(out), &rep); err != nil || len(rep.Scenarios) != 1 {
			t.Fatalf("exit %d: %v\n%s\n%s", code, err, out, errOut)
		}
		s := rep.Scenarios[0]
		if s.Result == "PASS" || s.Reason != "setup_failed" {
			t.Fatalf("scenario %s (%s), want ERROR setup_failed", s.Result, s.Reason)
		}
		return s.Cause
	}
	t.Run("unexpected response", func(t *testing.T) {
		app := newApp(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
		if c := cause(t, app.srv.URL); c != "unexpected_response" {
			t.Errorf("cause %q", c)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		app := newApp(t, nil)
		app.srv.Close()
		if c := cause(t, app.srv.URL); c != "unreachable" {
			t.Errorf("cause %q", c)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		app := newApp(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(2 * time.Second) })
		if c := cause(t, app.srv.URL, "--request-timeout", "200ms"); c != "timeout" {
			t.Errorf("cause %q", c)
		}
	})
}

const variantsConfig = `version: 1
project: example
manifest: manifest.yaml
identities:
  alice: { type: bearer, variants: [expired] }
  bob: { type: bearer }
  anonymous: { type: none }
`

func variantSpec(status int) string {
	return fmt.Sprintf(`version: 1
suite: s
scenarios:
  - id: S-1
    name: expired token refused
    steps:
      - id: read
        request: { method: GET, path: /api/projects/p1, identity: alice, credential: expired }
        expect: { status: %d }
`, status)
}

// AUTH-06, AUTH-03: a credential variant sends its own token, is only for
// negative tests, and must differ from every other token.
func TestCredentialVariants(t *testing.T) {
	var mu sync.Mutex
	var got []string
	h := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok-alice" {
			w.WriteHeader(401)
			return
		}
		io.WriteString(w, `{"id": "p1"}`)
	}
	variantBinding := func(t *testing.T, base, extra string) string {
		return binding(t, fmt.Sprintf(`target: { base_url: %s, user_agent: t/1 }
identities:
  alice:
    token_env: TEST_ALICE_TOKEN
    variants:
      expired: { token_env: TEST_ALICE_EXPIRED }
%s`, base, extra))
	}
	t.Run("variant token is sent and refused", func(t *testing.T) {
		withTokens(t)
		t.Setenv("TEST_ALICE_EXPIRED", "tok-alice-expired")
		app := newApp(t, h)
		r := specRepo(t, variantsConfig, variantSpec(401))
		code, out, errOut := r.proctor("run", "--binding", variantBinding(t, app.srv.URL, ""))
		expect(t, code, out, errOut, 0)
		if strings.Contains(out+errOut, "tok-alice-expired") {
			t.Error("the variant's token appears in the output")
		}
		mu.Lock()
		defer mu.Unlock()
		if got[len(got)-1] != "Bearer tok-alice-expired" {
			t.Errorf("sent %q", got[len(got)-1])
		}
	})
	t.Run("variant accepted by the app fails", func(t *testing.T) {
		withTokens(t)
		t.Setenv("TEST_ALICE_EXPIRED", "tok-alice-expired")
		app := newApp(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
		r := specRepo(t, variantsConfig, variantSpec(401))
		code, out, errOut := r.proctor("run", "--binding", variantBinding(t, app.srv.URL, ""))
		expect(t, code, out, errOut, 1)
	})
	t.Run("variant must expect 401 or 403", func(t *testing.T) {
		r := specRepo(t, variantsConfig, variantSpec(200))
		code, out, errOut := r.proctor("validate")
		expect(t, code, out, errOut, 3, "AUTH-06")
	})
	t.Run("undeclared variant", func(t *testing.T) {
		r := specRepo(t, variantsConfig, strings.Replace(variantSpec(401), "credential: expired", "credential: revoked", 1))
		code, out, errOut := r.proctor("validate")
		expect(t, code, out, errOut, 3, "AUTH-06", `no credential variant "revoked"`)
	})
	t.Run("variant with the valid token", func(t *testing.T) {
		withTokens(t)
		t.Setenv("TEST_ALICE_EXPIRED", "tok-alice")
		app := newApp(t, h)
		r := specRepo(t, variantsConfig, variantSpec(401))
		code, out, errOut := r.proctor("run", "--binding", variantBinding(t, app.srv.URL, ""))
		expect(t, code, out, errOut, 3, "AUTH-03")
		if len(app.requests()) != 0 {
			t.Error("requests were sent")
		}
	})
	t.Run("variant variable not set", func(t *testing.T) {
		withTokens(t)
		app := newApp(t, h)
		r := specRepo(t, variantsConfig, variantSpec(401))
		code, out, errOut := r.proctor("run", "--binding", variantBinding(t, app.srv.URL, ""))
		expect(t, code, out, errOut, 3, "TEST_ALICE_EXPIRED is not set")
	})
	t.Run("binding variant not declared", func(t *testing.T) {
		withTokens(t)
		t.Setenv("TEST_ALICE_EXPIRED", "tok-alice-expired")
		app := newApp(t, h)
		r := specRepo(t, variantsConfig, variantSpec(401))
		code, out, errOut := r.proctor("run", "--binding", variantBinding(t, app.srv.URL, "  bob:\n    token_env: TEST_BOB_TOKEN\n    variants:\n      stolen: { token_env: TEST_ALICE_EXPIRED }\n"))
		expect(t, code, out, errOut, 3, `credential variant "stolen" is not declared`)
	})
	t.Run("two identities with one token", func(t *testing.T) {
		withTokens(t)
		t.Setenv("TEST_BOB_TOKEN", "tok-alice")
		app := newApp(t, projectsApp("x", 0))
		r := newRepo(t)
		code, out, errOut := r.proctor("run", "--binding", bindingFor(t, app.srv.URL))
		expect(t, code, out, errOut, 3, "AUTH-03")
	})
}

// Injected network faults: whatever the transport does, a step expecting a
// 200 with a JSON field never passes. Each fault is ERROR (exit 2) or FAIL
// (exit 1), as noted.
func TestFaultsNeverPass(t *testing.T) {
	hijack := func(f func(c net.Conn)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			c, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				panic(err)
			}
			f(c)
			c.Close()
		}
	}
	cases := []struct {
		name string
		h    http.HandlerFunc
		want int
	}{
		{"connection closed before a response", hijack(func(c net.Conn) {}), 2},
		{"connection reset mid-body", hijack(func(c net.Conn) {
			io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"a\": 2")
			if tc, ok := c.(*net.TCPConn); ok {
				tc.SetLinger(0)
			}
		}), 2},
		{"body shorter than Content-Length", hijack(func(c net.Conn) {
			io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 50\r\n\r\n{\"a\": 2}")
		}), 2},
		{"broken chunked encoding", hijack(func(c net.Conn) {
			io.WriteString(c, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\n{\"a\": 2}\r\n0\r\n\r\n")
		}), 2},
		{"garbage instead of HTTP", hijack(func(c net.Conn) { io.WriteString(c, "SSH-2.0-nope\r\n\r\n") }), 2},
		{"invalid gzip", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			io.WriteString(w, `{"a": 2}`)
		}, 2},
		{"slower than the request timeout", func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(2 * time.Second)
			io.WriteString(w, `{"a": 2}`)
		}, 2},
		{"body stalls after the headers", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			io.WriteString(w, `{"a": `)
			w.(http.Flusher).Flush()
			time.Sleep(2 * time.Second)
			io.WriteString(w, `2}`)
		}, 2},
		{"body over 10 MiB", func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"a": 2, "pad": "`)
			chunk := strings.Repeat("x", 1<<20)
			for i := 0; i < 11; i++ {
				io.WriteString(w, chunk)
			}
			io.WriteString(w, `"}`)
		}, 2},
		{"status 200 with HTML", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html>a 2</html>")
		}, 1},
		{"redirect to a page that would pass", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/doc" {
				http.Redirect(w, r, "/ok", http.StatusFound)
				return
			}
			io.WriteString(w, `{"a": 2}`)
		}, 1},
		{"server error", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(503)
			io.WriteString(w, `{"a": 2}`)
		}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut, _ := runOne(t, c.h, getSpec("          json:\n            $.a: 2\n"), "--request-timeout", "500ms")
			expect(t, code, out, errOut, c.want)
			if strings.Contains(out, "RESULT: PASSED") {
				t.Fatal("a fault produced PASS")
			}
		})
	}
}

// HTTP-08: a listed response header must be absent.
func TestAbsentHeaders(t *testing.T) {
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		io.WriteString(w, `{"a": 2}`)
	}
	code, out, errOut, _ := runOne(t, h, getSpec("          absent_headers: [Access-Control-Allow-Origin]\n"))
	expect(t, code, out, errOut, 1, "header present, expected absent")
	code, out, errOut, _ = runOne(t, h, getSpec("          absent_headers: [X-Debug]\n"))
	expect(t, code, out, errOut, 0)
	code, out, errOut, _ = runOne(t, h, getSpec("          absent_headers: [X-Debug]\n          headers:\n            x-debug: \"1\"\n"))
	expect(t, code, out, errOut, 3, "both expected and absent")
}

// AUTH-06: an accepted variant is another valid form of the credential,
// sent as a whole Authorization value, and may expect success.
func TestAcceptedCredentialVariant(t *testing.T) {
	withTokens(t)
	t.Setenv("TEST_ALICE_LOWER", "bearer tok-alice")
	var got []string
	var mu sync.Mutex
	app := newApp(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Get("Authorization"))
		mu.Unlock()
		if !strings.EqualFold(r.Header.Get("Authorization"), "Bearer tok-alice") {
			w.WriteHeader(401)
			return
		}
		io.WriteString(w, `{"id": "p1"}`)
	})
	cfg := strings.Replace(variantsConfig, "variants: [expired]", "accepted_variants: [lowercase]", 1)
	spec := strings.Replace(strings.Replace(variantSpec(200), "credential: expired", "credential: lowercase", 1), "expect: { status: 200 }", "expect: { status: 200, json: { $.id: p1 } }", 1)
	r := specRepo(t, cfg, spec)
	b := binding(t, fmt.Sprintf(`target: { base_url: %s, user_agent: t/1 }
identities:
  alice:
    token_env: TEST_ALICE_TOKEN
    variants:
      lowercase: { header_env: TEST_ALICE_LOWER }
`, app.srv.URL))
	code, out, errOut := r.proctor("run", "--binding", b)
	expect(t, code, out, errOut, 0)
	mu.Lock()
	if got[len(got)-1] != "bearer tok-alice" {
		t.Errorf("sent %q", got[len(got)-1])
	}
	mu.Unlock()
	t.Run("token_env and header_env together", func(t *testing.T) {
		b := binding(t, fmt.Sprintf(`target: { base_url: %s, user_agent: t/1 }
identities:
  alice:
    token_env: TEST_ALICE_TOKEN
    variants:
      lowercase: { header_env: TEST_ALICE_LOWER, token_env: TEST_ALICE_TOKEN }
`, app.srv.URL))
		code, out, errOut := r.proctor("run", "--binding", b)
		expect(t, code, out, errOut, 3, "exactly one of")
	})
}

// §7.5: the semantic diff lists each weakening separately, with the
// assertions it loses, and changed expectations with old and new values.
func TestDiffWeakenings(t *testing.T) {
	base := `version: 1
suite: s
scenarios:
  - id: W-1
    name: create and read
    steps:
      - id: create
        request: { method: POST, path: /api/projects, identity: alice, body: { name: "n ${run.id}" } }
        expect:
          status: 201
          json:
            $.name: "n ${run.id}"
          present: [$.id]
        save: { id: $.id }
      - id: read
        request: { method: GET, path: "/api/projects/${id}", identity: alice }
        expect:
          status: 200
          json:
            $.id: "${id}"
  - id: W-2
    name: refused
    steps:
      - id: list
        request: { method: GET, path: /api/projects, identity: anonymous }
        expect: { status: 401 }
`
	cases := []struct {
		name, head string
		want       []string
	}{
		{"assertion removed", strings.Replace(base, "          present: [$.id]\n", "", 1), []string{"assertion_removed", "present $.id is no longer asserted"}},
		{"step removed", strings.Replace(base, "        save: { id: $.id }\n      - id: read\n        request: { method: GET, path: \"/api/projects/${id}\", identity: alice }\n        expect:\n          status: 200\n          json:\n            $.id: \"${id}\"\n", "        save: { id: $.id }\n        no_state_check: none\n", 1),
			[]string{"step_removed", "W-1 read", "state_check_waived"}},
		{"step moved to setup", strings.Replace(base, "    steps:\n      - id: create", "    setup:\n      - id: create", 1) + "", []string{"step_moved_to_setup", "W-1 create"}},
		{"scenario removed", base[:strings.Index(base, "  - id: W-2")], []string{"scenario_removed", "W-2"}},
		{"rule waived", strings.Replace(strings.Replace(strings.Replace(base, `body: { name: "n ${run.id}" } }
`, `body: { name: Fixed } }
        waive: { FIX-05: the API accepts only this name }
`, 1), `$.name: "n ${run.id}"`, `$.name: Fixed`, 1), `            $.id: "${id}"
`, `            $.id: "${id}"
            $.name: Fixed
`, 1), []string{"rule_waived", "FIX-05 waived: the API accepts only this name"}},
		{"status changed", strings.Replace(base, "expect: { status: 401 }", "expect: { status: 403 }", 1), []string{"W-2 list status: 401 -> 403"}},
		{"identity changed", strings.Replace(base, "path: /api/projects, identity: anonymous", "path: /api/projects, identity: bob", 1), []string{`identity: "anonymous" -> "bob"`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRepo(t)
			r.write("proctor/specs/project-access.yaml", base)
			b := r.commit("base")
			r.write("proctor/specs/project-access.yaml", c.head)
			h := r.commit("change")
			code, out, errOut := r.proctor("diff", "--base", b, "--head", h)
			expect(t, code, out, errOut, 0, c.want...)
		})
	}
}

// ${fill(N)} sends N characters; ${secret.name} sends a binding-supplied
// value; both only in request bodies and headers, and no report shows a
// secret or token even when the application echoes it (AUTH-02, AUTH-07).
func TestFillAndSecrets(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	echo := func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		var in map[string]any
		_ = json.Unmarshal(b, &in)
		out, _ := json.Marshal(map[string]any{"got": in, "auth": r.Header.Get("Authorization"), "size": len(b)})
		w.Write(out)
	}
	cfg := configYAML + "secrets: [pw]\n"
	spec := func(body, expect string) string {
		return `version: 1
suite: s
scenarios:
  - id: S-1
    name: send
    steps:
      - id: send
        request: { method: POST, path: /echo, identity: alice, body: ` + body + ` }
        no_state_check: the echo app keeps nothing
        expect:
          status: 200
` + expect
	}
	b := func(t *testing.T, base string) string {
		return binding(t, fmt.Sprintf("target: { base_url: %s, user_agent: t/1 }\nidentities:\n  alice: { token_env: TEST_ALICE_TOKEN }\nsecrets:\n  pw: { env: TEST_PW }\n", base))
	}
	t.Run("fill", func(t *testing.T) {
		withTokens(t)
		app := newApp(t, echo)
		r := specRepo(t, cfg, spec(`{ name: "${fill(1048577)}" }`, "          json:\n            $.size: 1048588\n"))
		code, out, errOut := r.proctor("run", "--binding", b(t, app.srv.URL))
		expect(t, code, out, errOut, 0)
	})
	t.Run("secret sent, echoed value redacted", func(t *testing.T) {
		withTokens(t)
		t.Setenv("TEST_PW", "hunter2-secret-value")
		app := newApp(t, echo)
		r := specRepo(t, cfg, spec(`{ password: "${secret.pw}" }`, "          json:\n            $.got: { password: wrong }\n            $.auth: wrong\n"))
		code, out, errOut := r.proctor("run", "--binding", b(t, app.srv.URL), "--json")
		expect(t, code, out, errOut, 1, "[redacted]")
		if strings.Contains(out+errOut, "hunter2-secret-value") || strings.Contains(out+errOut, "tok-alice") {
			t.Fatalf("a secret or token appears in the report:\n%s", out)
		}
		mu.Lock()
		last := bodies[len(bodies)-1]
		mu.Unlock()
		if last != `{"password":"hunter2-secret-value"}` {
			t.Errorf("sent %s", last)
		}
	})
	for name, c := range map[string]struct{ spec, cfg, text string }{
		"secret in an expectation": {spec(`{ a: 1 }`, "          json:\n            $.got: \"${secret.pw}\"\n"), cfg, "may be used only in a request body"},
		"secret in the path":       {strings.Replace(spec(`{ a: 1 }`, "          present: [$.got]\n"), "path: /echo", `path: "/echo/${secret.pw}"`, 1), cfg, "may be used only in a request body"},
		"fill in a query":          {strings.Replace(spec(`{ a: 1 }`, "          present: [$.got]\n"), "path: /echo,", `path: /echo, query: { q: "${fill(10)}" },`, 1), cfg, "may be used only in a request body"},
		"undeclared secret":        {spec(`{ p: "${secret.other}" }`, "          present: [$.got]\n"), cfg, `secret "other" is not declared`},
		"fill too large":           {spec(`{ p: "${fill(99999999)}" }`, "          present: [$.got]\n"), cfg, "unsupported reference"},
	} {
		t.Run(name, func(t *testing.T) {
			r := specRepo(t, c.cfg, c.spec)
			code, out, errOut := r.proctor("validate")
			expect(t, code, out, errOut, 3, c.text)
		})
	}
	t.Run("secret variable not set", func(t *testing.T) {
		withTokens(t)
		app := newApp(t, echo)
		r := specRepo(t, cfg, spec(`{ password: "${secret.pw}" }`, "          present: [$.got]\n"))
		code, out, errOut := r.proctor("run", "--binding", b(t, app.srv.URL))
		expect(t, code, out, errOut, 3, "TEST_PW is not set")
	})
}
