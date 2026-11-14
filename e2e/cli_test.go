package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests run the built proctor binary against throwaway Git
// repositories, from the outside (black-box only; no function-level tests).
// Each case names the prd.md rule it covers.

const configYAML = `version: 1
project: example
manifest: manifest.yaml
identities:
  alice: { type: bearer }
  bob: { type: bearer }
  anonymous: { type: none }
`

const manifestYAML = `version: 1
specifications:
  - id: project-access
    path: specs/project-access.yaml
`

const specYAML = `version: 1
suite: project-access
scenarios:
  - id: PA-001
    name: Owner can create and read a project
    steps:
      - id: create
        request:
          method: POST
          path: /api/projects
          identity: alice
          body:
            name: "Alice's project"
        expect:
          status: 201
          json:
            $.name: "Alice's project"
        save:
          project_id: $.id
      - id: read
        request:
          method: GET
          path: /api/projects/${project_id}
          identity: alice
        expect:
          status: 200
          json:
            $.id: "${project_id}"

  - id: PA-002
    name: Anonymous users cannot list projects
    setup:
      - id: seed
        request:
          method: POST
          path: /api/projects
          identity: bob
        expect:
          status: 201
    steps:
      - id: list
        request:
          method: GET
          path: /api/projects
          identity: anonymous
        expect:
          status: 401
`

type testRepo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *testRepo {
	t.Helper()
	r := &testRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	r.git("config", "user.email", "test@example.com")
	r.git("config", "user.name", "Test")
	r.git("config", "commit.gpgsign", "false")
	r.write("proctor/proctor.yaml", configYAML)
	r.write("proctor/manifest.yaml", manifestYAML)
	r.write("proctor/specs/project-access.yaml", specYAML)
	r.write("app/main.go", "package main\n")
	r.commit("base")
	return r
}

func (r *testRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *testRepo) write(p, content string) {
	r.t.Helper()
	full := filepath.Join(r.dir, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *testRepo) remove(p string) {
	r.t.Helper()
	if err := os.RemoveAll(filepath.Join(r.dir, filepath.FromSlash(p))); err != nil {
		r.t.Fatal(err)
	}
}

func (r *testRepo) commit(msg string) string {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", msg)
	return r.git("rev-parse", "HEAD")
}

func (r *testRepo) proctor(args ...string) (int, string, string) {
	r.t.Helper()
	return runProctor(r.t, append(args, "--repo", r.dir)...)
}

func expect(t *testing.T, code int, out, errOut string, wantCode int, wantText ...string) {
	t.Helper()
	if code != wantCode {
		t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, wantCode, out, errOut)
	}
	for _, w := range wantText {
		if !strings.Contains(out+errOut, w) {
			t.Fatalf("output doesn't contain %q\nstdout:\n%s\nstderr:\n%s", w, out, errOut)
		}
	}
}

func TestValidSetPasses(t *testing.T) {
	r := newRepo(t)
	code, out, errOut := r.proctor("integrity")
	expect(t, code, out, errOut, 0, "Specifications:  1   Scenarios: 2", "RESULT: PASSED")
	code, out, errOut = r.proctor("validate")
	expect(t, code, out, errOut, 0)
}

func TestPRDConfigExampleIsAccepted(t *testing.T) {
	r := newRepo(t)
	r.write("proctor/proctor.yaml", `version: 1
project: example-application
manifest: manifest.yaml
protected_owners: ["@example-org/verification-owners"]
adapters:
  http: enabled
  postgres: enabled
  temporal: enabled
  logs: enabled
connections:
  postgres:
    app_db:
      schemas: [app]
      objects: [app.projects, app.exports]
  temporal:
    workflows:
      task_queues: [exports]
  logs:
    application:
      trace_id_field: trace_id
identities:
  alice: { type: bearer }
  bob: { type: bearer }
  anonymous: { type: none }
readiness:
  path: /healthz/ready
  startup_timeout: 120s
time_limits:
  min_interval: 100ms
  max_samples: 1000
  max_window: 10m
temporal:
  max_clock_skew: 1s
redaction:
  json_paths: [$.email]
  headers: [X-Api-Key]
report:
  formats: [terminal, json]
  excerpt_bytes: 2048
`)
	r.commit("prd example config")
	code, out, errOut := r.proctor("integrity")
	expect(t, code, out, errOut, 0)
}

func TestIntegrityFindings(t *testing.T) {
	cases := []struct {
		name     string
		change   func(r *testRepo)
		wantCode int
		wantText []string
	}{
		{"INT-01 listed file missing", func(r *testRepo) {
			r.remove("proctor/specs/project-access.yaml")
		}, 4, []string{"INT-01", "not found"}},
		{"INT-01 path leaves the root", func(r *testRepo) {
			r.write("proctor/manifest.yaml", "version: 1\nspecifications:\n  - id: x\n    path: ../app/x.yaml\n")
		}, 4, []string{"INT-01", "leaves its directory"}},
		{"INT-01 absolute path", func(r *testRepo) {
			r.write("proctor/manifest.yaml", "version: 1\nspecifications:\n  - id: x\n    path: /etc/x.yaml\n")
		}, 4, []string{"INT-01", "absolute"}},
		{"INT-01 path not byte-exact", func(r *testRepo) {
			r.write("proctor/manifest.yaml", "version: 1\nspecifications:\n  - id: x\n    path: specs/Project-Access.yaml\n")
		}, 4, []string{"INT-01", "byte for byte"}},
		{"INT-06 leftover required flag", func(r *testRepo) {
			r.write("proctor/manifest.yaml", manifestYAML+"    required: false\n")
		}, 3, []string{"INT-15", "required"}},
		{"INT-15 duplicate key", func(r *testRepo) {
			r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "status: 401\n", "status: 401\n          status: 200\n", 1))
		}, 3, []string{"INT-15", "duplicate key"}},
		{"INT-15 anchor", func(r *testRepo) {
			r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "suite: project-access", "suite: &s project-access", 1))
		}, 3, []string{"INT-15", "anchor"}},
		{"INT-15 nested duplicate key", func(r *testRepo) {
			r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "$.name: \"Alice's project\"\n", "$.name: \"Alice's project\"\n            $.name: \"anything\"\n", 1))
		}, 3, []string{"INT-15", "duplicate key"}},
		{"INT-15 alias", func(r *testRepo) {
			r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "suite: project-access", "suite: &s project-access\nx: *s", 1))
		}, 3, []string{"INT-15", "anchor"}},
		{"INT-15 merge key", func(r *testRepo) {
			r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "          json:\n            $.name:", "          json:\n            <<: {$.id: x}\n            $.name:", 1))
		}, 3, []string{"INT-15", "merge key"}},
		{"INT-15 explicit tag", func(r *testRepo) {
			r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "status: 401", "status: !!str 401", 1))
		}, 3, []string{"INT-15", "explicit tag"}},
		{"INT-15 custom tag", func(r *testRepo) {
			r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "status: 401", "status: !env 401", 1))
		}, 3, []string{"INT-15", "explicit tag"}},
		{"INT-15 unknown key", func(r *testRepo) {
			r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "    name: Owner can", "    skip: true\n    name: Owner can", 1))
		}, 3, []string{"INT-15", "not found"}},
		{"INT-15 two documents", func(r *testRepo) {
			r.write("proctor/specs/project-access.yaml", specYAML+"---\nversion: 1\n")
		}, 3, []string{"INT-15", "more than one"}},
		{"INT-15 empty file", func(r *testRepo) {
			r.write("proctor/specs/project-access.yaml", "")
		}, 3, []string{"INT-15", "empty document"}},
		{"INT-15 complex key", func(r *testRepo) {
			r.write("proctor/specs/project-access.yaml", "? [a, b]\n: 1\n")
		}, 3, []string{"INT-15", "scalars"}},
		{"INT-02 duplicate scenario id across files", func(r *testRepo) {
			r.write("proctor/specs/other.yaml", specYAML)
			r.write("proctor/manifest.yaml", manifestYAML+"  - id: other\n    path: specs/other.yaml\n")
		}, 3, []string{"INT-02", "PA-001", "also used in"}},
		{"INT-04 empty manifest", func(r *testRepo) {
			r.write("proctor/manifest.yaml", "version: 1\nspecifications: []\n")
		}, 4, []string{"INT-04", "no specifications"}},
		{"INT-04 specification without scenarios", func(r *testRepo) {
			r.write("proctor/specs/project-access.yaml", "version: 1\nsuite: x\nscenarios: []\n")
		}, 4, []string{"INT-04", "no scenarios"}},
		{"INT-12 undeclared identity", func(r *testRepo) {
			r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "identity: anonymous", "identity: mallory", 1))
		}, 3, []string{"INT-12", "mallory"}},
		{"EXE-03 2xx verification step with status only", func(r *testRepo) {
			r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "status: 401", "status: 200", 1))
		}, 3, []string{"EXE-03", "must also assert"}},
		{"INT-13 specification is a symlink", func(r *testRepo) {
			r.remove("proctor/specs/project-access.yaml")
			r.write("app/real.yaml", specYAML)
			if err := os.Symlink("../../app/real.yaml", filepath.Join(r.dir, "proctor/specs/project-access.yaml")); err != nil {
				r.t.Fatal(err)
			}
		}, 4, []string{"INT-13", "symlink"}},
		{"INT-13 protected root is a symlink", func(r *testRepo) {
			if err := os.Rename(filepath.Join(r.dir, "proctor"), filepath.Join(r.dir, "real")); err != nil {
				r.t.Fatal(err)
			}
			if err := os.Symlink("real", filepath.Join(r.dir, "proctor")); err != nil {
				r.t.Fatal(err)
			}
		}, 4, []string{"INT-13", "should be a directory"}},
		{"INT-13 submodule under the protected root", func(r *testRepo) {
			// A nested repository is recorded as a gitlink (mode 160000).
			nested := &testRepo{t: r.t, dir: filepath.Join(r.dir, "proctor", "vendored")}
			if err := os.MkdirAll(nested.dir, 0o755); err != nil {
				r.t.Fatal(err)
			}
			nested.git("init", "-q")
			nested.git("-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "-q", "--allow-empty", "-m", "x")
		}, 4, []string{"INT-13", "submodule"}},
		{"INT-13 Git LFS pointer", func(r *testRepo) {
			r.write("proctor/specs/project-access.yaml", "version https://git-lfs.github.com/spec/v1\noid sha256:4d7a\nsize 12\n")
		}, 4, []string{"INT-13", "LFS pointer"}},
		{"INT-14 diff hidden", func(r *testRepo) {
			r.write(".gitattributes", "proctor/** -diff\n")
		}, 4, []string{"INT-14", `"diff"`}},
		{"INT-14 linguist-generated", func(r *testRepo) {
			r.write(".gitattributes", "proctor/specs/*.yaml linguist-generated\n")
		}, 4, []string{"INT-14", "linguist-generated"}},
		{"INT-14 line endings rewritten", func(r *testRepo) {
			r.write(".gitattributes", "*.yaml eol=crlf\n")
		}, 4, []string{"INT-14", `"eol"`}},
		{"INT-11 two CODEOWNERS files", func(r *testRepo) {
			r.write("CODEOWNERS", "* @example-org/owners\n")
			r.write(".github/CODEOWNERS", "/proctor/ @example-org/owners\n")
		}, 4, []string{"INT-11", "more than one CODEOWNERS"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRepo(t)
			c.change(r)
			r.commit(c.name)
			code, out, errOut := r.proctor("integrity")
			expect(t, code, out, errOut, c.wantCode, c.wantText...)
		})
	}
}

func TestUnlistedSpecificationIsReportedNotRun(t *testing.T) {
	r := newRepo(t)
	r.write("proctor/specs/draft.yaml", strings.ReplaceAll(specYAML, "PA-00", "DR-00"))
	r.commit("unlisted spec")
	code, out, errOut := r.proctor("integrity")
	expect(t, code, out, errOut, 0, "unlisted (not executed): proctor/specs/draft.yaml")
}

func TestValidateReadsWorkingTreeIntegrityReadsCommit(t *testing.T) {
	r := newRepo(t)
	r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "status: 401", "status: 200", 1))
	code, out, errOut := r.proctor("validate")
	expect(t, code, out, errOut, 3, "EXE-03")
	code, out, errOut = r.proctor("integrity")
	expect(t, code, out, errOut, 0)
	// validate --commit reads the commit, not the working tree.
	code, out, errOut = r.proctor("validate", "--commit", "HEAD")
	expect(t, code, out, errOut, 0)
	head := r.commit("invalid")
	code, out, errOut = r.proctor("validate", "--commit", head)
	expect(t, code, out, errOut, 3, "EXE-03")
	code, out, errOut = r.proctor("validate", "--commit", "no-such-revision")
	expect(t, code, out, errOut, 3)
}

func TestInheritedGitEnvironmentIsIgnored(t *testing.T) {
	r := newRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "elsewhere"))
	t.Setenv("GIT_CONFIG_PARAMETERS", "'core.attributesFile=/nonexistent'")
	code, out, errOut := r.proctor("integrity")
	expect(t, code, out, errOut, 0)
}

func TestDiff(t *testing.T) {
	t.Run("changed expectation lists file and scenario", func(t *testing.T) {
		r := newRepo(t)
		base := r.git("rev-parse", "HEAD")
		r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "status: 401", "status: 403", 1))
		head := r.commit("weaken")
		code, out, errOut := r.proctor("diff", "--base", base, "--head", head)
		expect(t, code, out, errOut, 0, "modified  proctor/specs/project-access.yaml  approval required", "modified  PA-002", "Protected changes against base: 1")
	})
	t.Run("formatting-only change is still listed (INT-10)", func(t *testing.T) {
		r := newRepo(t)
		base := r.git("rev-parse", "HEAD")
		r.write("proctor/specs/project-access.yaml", specYAML+"# comment \n")
		head := r.commit("format")
		code, out, errOut := r.proctor("diff", "--base", base, "--head", head, "--json")
		expect(t, code, out, errOut, 0)
		var got struct {
			Changes int `json:"protected_changes"`
			Diff    struct {
				Scenarios []any `json:"scenarios"`
			} `json:"diff"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if got.Changes != 1 || len(got.Diff.Scenarios) != 0 {
			t.Fatalf("want 1 file change and no scenario change, got %+v\n%s", got, out)
		}
	})
	t.Run("pure move is a move", func(t *testing.T) {
		r := newRepo(t)
		base := r.git("rev-parse", "HEAD")
		r.git("mv", "proctor/specs/project-access.yaml", "proctor/specs/access.yaml")
		r.write("proctor/manifest.yaml", strings.Replace(manifestYAML, "specs/project-access.yaml", "specs/access.yaml", 1))
		head := r.commit("move")
		code, out, errOut := r.proctor("diff", "--base", base, "--head", head)
		expect(t, code, out, errOut, 0, "moved     proctor/specs/access.yaml (from proctor/specs/project-access.yaml)", "modified  proctor/manifest.yaml")
	})
	t.Run("move with edit is a removal and an addition", func(t *testing.T) {
		r := newRepo(t)
		base := r.git("rev-parse", "HEAD")
		r.remove("proctor/specs/project-access.yaml")
		r.write("proctor/specs/access.yaml", strings.Replace(specYAML, "status: 401", "status: 200\n          json: {$.items: []}", 1))
		r.write("proctor/manifest.yaml", strings.Replace(manifestYAML, "specs/project-access.yaml", "specs/access.yaml", 1))
		head := r.commit("move and edit")
		code, out, errOut := r.proctor("diff", "--base", base, "--head", head)
		expect(t, code, out, errOut, 0, "removed   proctor/specs/project-access.yaml", "added     proctor/specs/access.yaml", "moved     PA-002  proctor/specs/access.yaml (from proctor/specs/project-access.yaml), modified")
	})
	t.Run("workflow and attribute changes are protected", func(t *testing.T) {
		r := newRepo(t)
		base := r.git("rev-parse", "HEAD")
		r.write(".github/workflows/proctor.yml", "on: pull_request\n")
		r.write(".gitattributes", "*.go text\n")
		r.write("app/main.go", "package main // not protected\n")
		head := r.commit("workflow")
		code, out, errOut := r.proctor("diff", "--base", base, "--head", head)
		expect(t, code, out, errOut, 0, "added     .github/workflows/proctor.yml", "added     .gitattributes", "Protected changes against base: 2")
	})
	t.Run("protected_paths entries are protected on either side", func(t *testing.T) {
		r := newRepo(t)
		r.write("compose.verify.yaml", "services: {}\n")
		r.write("proctor/proctor.yaml", configYAML+"protected_paths: [compose.verify.yaml]\n")
		base := r.commit("protect compose")
		r.write("compose.verify.yaml", "services: {stub: {}}\n")
		r.write("proctor/proctor.yaml", configYAML)
		head := r.commit("unprotect and edit")
		code, out, errOut := r.proctor("diff", "--base", base, "--head", head)
		expect(t, code, out, errOut, 0, "modified  compose.verify.yaml", "modified  proctor/proctor.yaml")
	})
	t.Run("missing base commit is an integrity violation", func(t *testing.T) {
		r := newRepo(t)
		code, out, errOut := r.proctor("diff", "--base", strings.Repeat("a", 40), "--head", "HEAD")
		expect(t, code, out, errOut, 4, "INT-08", "not available")
	})
	t.Run("shallow clone without the base fails loudly", func(t *testing.T) {
		r := newRepo(t)
		base := r.git("rev-parse", "HEAD")
		r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "status: 401", "status: 403", 1))
		r.commit("change")
		clone := t.TempDir()
		r.git("clone", "-q", "--depth", "1", "file://"+r.dir, clone)
		code, out, errOut := runProctor(t, "diff", "--base", base, "--head", "HEAD", "--repo", clone)
		expect(t, code, out, errOut, 4, "INT-08")
	})
	t.Run("test merge base is its first parent", func(t *testing.T) {
		r := newRepo(t)
		r.git("switch", "-q", "-c", "feature")
		r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "status: 401", "status: 403", 1))
		prHead := r.commit("pr change")
		r.git("switch", "-q", "main")
		r.write("app/main.go", "package main // main moved on\n")
		r.commit("main change")
		r.git("merge", "-q", "--no-ff", "-m", "test merge", "feature")
		merge := r.git("rev-parse", "HEAD")
		code, out, errOut := r.proctor("diff", "--head", merge, "--pr-head", prHead)
		expect(t, code, out, errOut, 0, "modified  proctor/specs/project-access.yaml", "Protected changes against base: 1")

		code, out, errOut = r.proctor("diff", "--head", merge, "--pr-head", "main~1")
		expect(t, code, out, errOut, 4, "second parent")
	})
	t.Run("usage error", func(t *testing.T) {
		r := newRepo(t)
		code, out, errOut := r.proctor("diff", "--head", "HEAD")
		expect(t, code, out, errOut, 3, "exactly one of --base or --pr-head")
	})
}

func TestUnknownCommand(t *testing.T) {
	code, out, errOut := runProctor(t, "approve")
	expect(t, code, out, errOut, 3, "unknown command")
}
