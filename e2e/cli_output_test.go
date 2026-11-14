package e2e

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// These tests cover the command line itself: argument checking, where
// errors go, and the parts of the human-readable reports that carry
// information (which commit, file, target and run, which step failed and
// why). They don't pin column widths or blank lines.

// TestCLIOutputInvocation: every command rejects extra arguments and
// unknown flags, and reports why it couldn't start on stderr.
func TestCLIOutputInvocation(t *testing.T) {
	r := newRepo(t)
	withTokens(t)
	app := newApp(t, projectsApp("Alice's project", 0))
	b := bindingFor(t, app.srv.URL)
	notRepo := t.TempDir()
	cases := []struct {
		name   string
		args   []string
		code   int
		stdout []string
		stderr []string
	}{
		{"no arguments", nil, 3, nil, []string{"Usage:", "proctor validate"}},
		{"help", []string{"help"}, 0, []string{"Usage:", "proctor run", "Exit codes:"}, nil},

		{"validate", []string{"validate", "--repo", r.dir}, 0, []string{"RESULT: PASSED"}, nil},
		{"validate extra argument", []string{"validate", "--repo", r.dir, "extra"}, 3, nil, nil},
		{"validate unknown flag", []string{"validate", "--repo", r.dir, "--bogus"}, 3, nil, []string{"flag provided but not defined: -bogus"}},
		{"validate --commit", []string{"validate", "--repo", r.dir, "--commit", "HEAD"}, 0, []string{"RESULT: PASSED"}, nil},
		{"validate unknown commit", []string{"validate", "--repo", r.dir, "--commit", "no-such-revision"}, 3, nil, []string{`proctor: commit "no-such-revision" is not available`}},
		{"validate outside a repository", []string{"validate", "--repo", notRepo}, 3, nil, []string{"proctor: git rev-parse: fatal:"}},

		{"integrity", []string{"integrity", "--repo", r.dir}, 0, []string{"RESULT: PASSED"}, nil},
		{"integrity extra argument", []string{"integrity", "--repo", r.dir, "extra"}, 3, nil, nil},
		{"integrity unknown flag", []string{"integrity", "--repo", r.dir, "--bogus"}, 3, nil, []string{"flag provided but not defined: -bogus"}},
		{"integrity outside a repository", []string{"integrity", "--repo", notRepo}, 3, nil, []string{"proctor: git rev-parse: fatal:"}},

		{"diff", []string{"diff", "--repo", r.dir, "--base", "HEAD", "--head", "HEAD"}, 0, []string{"Protected changes against base: none"}, nil},
		{"diff extra argument", []string{"diff", "--repo", r.dir, "--base", "HEAD", "--head", "HEAD", "extra"}, 3, nil, nil},
		{"diff unknown flag", []string{"diff", "--repo", r.dir, "--base", "HEAD", "--head", "HEAD", "--bogus"}, 3, nil, []string{"flag provided but not defined: -bogus"}},
		{"diff outside a repository", []string{"diff", "--repo", notRepo, "--base", "HEAD", "--head", "HEAD"}, 3, nil, []string{"proctor: git rev-parse: fatal:"}},

		{"run", []string{"run", "--repo", r.dir, "--binding", b}, 0, []string{"RESULT: PASSED"}, nil},
		{"run extra argument", []string{"run", "--repo", r.dir, "--binding", b, "extra"}, 3, nil, nil},
		{"run unknown flag", []string{"run", "--repo", r.dir, "--binding", b, "--bogus"}, 3, nil, []string{"flag provided but not defined: -bogus"}},
		{"run outside a repository", []string{"run", "--repo", notRepo, "--binding", b}, 3, nil, []string{"proctor: git rev-parse: fatal:"}},
		{"run request timeout 0", []string{"run", "--repo", r.dir, "--binding", b, "--request-timeout", "0s"}, 3, nil, []string{"--request-timeout and --query-timeout must be positive"}},
		{"run query timeout 0", []string{"run", "--repo", r.dir, "--binding", b, "--query-timeout", "0s"}, 3, nil, []string{"--request-timeout and --query-timeout must be positive"}},
		{"run query timeout 1ns", []string{"run", "--repo", r.dir, "--binding", b, "--query-timeout", "1ns"}, 0, []string{"RESULT: PASSED"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := runProctor(t, c.args...)
			if code != c.code {
				t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, c.code, out, errOut)
			}
			for _, w := range c.stdout {
				if !strings.Contains(out, w) {
					t.Errorf("stdout doesn't contain %q:\n%s", w, out)
				}
			}
			for _, w := range c.stderr {
				if !strings.Contains(errOut, w) {
					t.Errorf("stderr doesn't contain %q:\n%s", w, errOut)
				}
			}
		})
	}
}

// matches fails the test unless every pattern matches out and no
// forbidden pattern does.
func matches(t *testing.T, out string, want []string, forbidden ...string) {
	t.Helper()
	for _, w := range want {
		if !regexp.MustCompile(w).MatchString(out) {
			t.Errorf("output doesn't match %q", w)
		}
	}
	for _, f := range forbidden {
		if regexp.MustCompile(f).MatchString(out) {
			t.Errorf("output matches %q", f)
		}
	}
	if t.Failed() {
		t.Logf("output:\n%s", out)
	}
}

// TestCLIOutputCheckReports: validate and integrity name the mode, the
// commit (integrity only), the configuration and each finding's file.
func TestCLIOutputCheckReports(t *testing.T) {
	r := newRepo(t)
	head := r.git("rev-parse", "HEAD")
	code, out, _ := r.proctor("validate")
	if code != 0 {
		t.Fatalf("validate exit %d\n%s", code, out)
	}
	matches(t, out, []string{`PROCTOR VALIDATE  \(local, non-authoritative\)`, `\nConfiguration:\s+proctor/proctor\.yaml\n`}, `Commit:`)

	code, out, _ = r.proctor("integrity")
	if code != 0 {
		t.Fatalf("integrity exit %d\n%s", code, out)
	}
	matches(t, out, []string{`PROCTOR INTEGRITY  \(local, non-authoritative\)`, `\nCommit:\s+` + head + `\n`, `\nConfiguration:\s+proctor/proctor\.yaml\n`})

	r.write("proctor/manifest.yaml", strings.Replace(manifestYAML, "version: 1", "version: 2", 1))
	code, out, _ = r.proctor("validate")
	if code != 3 {
		t.Fatalf("validate exit %d\n%s", code, out)
	}
	matches(t, out, []string{`INVALID\s+INT-15\s+proctor/manifest\.yaml: version must be 1, got 2\n`})

	r.write("proctor/manifest.yaml", manifestYAML)
	r.write("CODEOWNERS", "* @example-org/owners\n")
	r.write(".github/CODEOWNERS", "/proctor/ @example-org/owners\n")
	r.commit("two owners files")
	code, out, _ = r.proctor("integrity")
	if code != 4 {
		t.Fatalf("integrity exit %d\n%s", code, out)
	}
	matches(t, out, []string{`INTEGRITY INT-11\s+more than one CODEOWNERS`})
}

// TestCLIOutputDiff: the diff names both commits, each file's mode change,
// and lists scenarios, weakenings and changed expectations only when there
// are some.
func TestCLIOutputDiff(t *testing.T) {
	cases := []struct {
		name      string
		change    func(r *testRepo)
		want      []string
		forbidden []string
	}{
		{"no protected change", func(r *testRepo) { r.write("app/main.go", "package main // changed\n") },
			[]string{`Protected changes against base: none`}, []string{`approval required`}},
		{"added workflow", func(r *testRepo) { r.write(".github/workflows/proctor.yml", "on: pull_request\n") },
			[]string{`added\s+\.github/workflows/proctor\.yml \[mode - -> 100644\]  approval required`},
			[]string{`Scenarios:`, `Weakenings`, `Changed requests and expectations:`}},
		{"changed status", func(r *testRepo) {
			r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "status: 401", "status: 403", 1))
		}, []string{`\nScenarios:\n\s+modified\s+PA-002\s+proctor/specs/project-access\.yaml\n`, `\nChanged requests and expectations:\n\s+PA-002 list status: 401 -> 403\n`},
			[]string{`Weakenings`, `\[mode`}},
		{"removed assertion", func(r *testRepo) {
			r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "          json:\n            $.name: \"Alice's project\"\n", "", 1))
		}, []string{`\nWeakenings \(review these first\):\n\s+assertion_removed\s+PA-001 create: .*\(1 assertion\(s\) lost\)\n`}, nil},
		{"waived state check", func(r *testRepo) {
			r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "        save:\n          project_id: $.id\n", "        save:\n          project_id: $.id\n        no_state_check: none possible\n", 1))
		}, []string{`\nWeakenings \(review these first\):\n\s+state_check_waived\s+PA-001 create: `}, []string{`assertion\(s\) lost`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRepo(t)
			base := r.git("rev-parse", "HEAD")
			c.change(r)
			head := r.commit(c.name)
			code, out, errOut := r.proctor("diff", "--base", base, "--head", head)
			if code != 0 {
				t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
			}
			matches(t, out, append([]string{`\nBase: ` + base + `\nHead: ` + head + `\n`}, c.want...), c.forbidden...)
		})
	}
}

// TestCLIOutputRun: the run report names the target, configuration, run ID
// and readiness, lists only the steps of scenarios that didn't pass, and
// for each such step its request, status, reason and cause, and only the
// assertions that failed.
func TestCLIOutputRun(t *testing.T) {
	t.Run("passing run", func(t *testing.T) {
		withTokens(t)
		app := newApp(t, projectsApp("Alice's project", 0))
		code, out, _ := newRepo(t).proctor("run", "--binding", bindingFor(t, app.srv.URL))
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		matches(t, out, []string{
			`PROCTOR RUN  \(local, non-authoritative\)`,
			`\nTarget:\s+` + regexp.QuoteMeta(app.srv.URL) + `\n`,
			`\nConfiguration:\s+proctor/proctor\.yaml\n`,
			`\nRun ID:\s+[0-9a-f]{12}\n`,
			`\nSpecifications:  1   Scenarios: 2\n`,
			`\nPASS\s+PA-001\s+Owner can create and read a project\n`,
		}, `Readiness:`, `create  POST`, `read  GET`, `No scenario was run`)
	})

	t.Run("readiness", func(t *testing.T) {
		withTokens(t)
		projects := projectsApp("Alice's project", 0)
		app := newApp(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/ready" {
				return
			}
			projects(w, r)
		})
		r := newRepo(t)
		r.write("proctor/proctor.yaml", configYAML+"readiness:\n  path: /ready\n  startup_timeout: 5s\n")
		code, out, _ := r.proctor("run", "--binding", bindingFor(t, app.srv.URL))
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		matches(t, out, []string{`\nReadiness:\s+ready\n`})
	})

	t.Run("failure and setup error", func(t *testing.T) {
		withTokens(t)
		app := newApp(t, projectsApp("Someone else's project", 500))
		code, out, _ := newRepo(t).proctor("run", "--binding", bindingFor(t, app.srv.URL))
		if code != 1 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		url := regexp.QuoteMeta(app.srv.URL)
		matches(t, out, []string{
			`\nFAIL\s+PA-001\s+Owner can create and read a project\n`,
			`\n\s+FAIL\s+create  POST ` + url + `/api/projects -> 201\n\s+json \$\.name: expected "Alice's project", got "Someone else's project"\n`,
			`\n\s+NOT_RUN\s+read  \(earlier_step_failed\)\n`,
			`\nERROR\s+PA-002\s+Anonymous users cannot list projects  \(setup_failed, cause unexpected_response\)\n`,
			`\n\s+ERROR\s+setup seed  POST ` + url + `/api/projects -> 500  \(setup_failed, cause unexpected_response\)\n`,
			`\n\s+NOT_RUN\s+list  \(setup_failed\)\n`,
		})
	})

	t.Run("failed assertions", func(t *testing.T) {
		code, out, _, _ := runOne(t, fixedApp(`{"a": 2, "b": 4, "s": "x"}`), getSpec("          json:\n            $.a: 2\n            $.b: 3\n            $.c: 1\n            $.s: y\n"))
		if code != 1 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		matches(t, out, []string{
			`json \$\.b: expected 3, got 4\n`,
			`json \$\.c: expected 1, but not present in the response\n`,
			`json \$\.s: expected "y", got "x"\n`,
		}, `json \$\.a:`, `status:`)
	})

	t.Run("request errors", func(t *testing.T) {
		withTokens(t)
		app := newApp(t, projectsApp("Alice's project", 0))
		code, out, _ := newRepo(t).proctor("run", "--binding", bindingFor(t, app.srv.URL), "--request-timeout", "1ns")
		if code != 2 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		matches(t, out, []string{
			`\nERROR\s+PA-001\s+Owner can create and read a project  \(timeout\)\n`,
			`\n\s+ERROR\s+create  POST ` + regexp.QuoteMeta(app.srv.URL) + `/api/projects  \(timeout\)\n`,
		}, `-> 0`)
	})

	t.Run("local JSON report isn't authoritative", func(t *testing.T) {
		withTokens(t)
		app := newApp(t, projectsApp("Alice's project", 0))
		code, out, _ := newRepo(t).proctor("run", "--binding", bindingFor(t, app.srv.URL), "--json")
		var rep struct {
			Mode  string `json:"mode"`
			Trust struct {
				Authoritative *bool `json:"authoritative"`
			} `json:"trust"`
		}
		if err := json.Unmarshal([]byte(out), &rep); err != nil || code != 0 {
			t.Fatalf("exit %d, %v\n%s", code, err, out)
		}
		if rep.Mode != "local" || rep.Trust.Authoritative == nil || *rep.Trust.Authoritative {
			t.Errorf("mode %q, trust.authoritative %v, want local and false", rep.Mode, rep.Trust.Authoritative)
		}
	})

	t.Run("binding error", func(t *testing.T) {
		withTokens(t)
		code, out, _ := newRepo(t).proctor("run", "--binding", binding(t, "target:\n  base_url: http://127.0.0.1:1\n"))
		if code != 3 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		matches(t, out, []string{`PROCTOR RUN  \(local, non-authoritative\)`, `\nConfiguration:\s+proctor/proctor\.yaml\n`, `Nothing was sent to the application`})
	})
}
