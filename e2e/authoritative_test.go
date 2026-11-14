package e2e

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests cover authoritative mode (§5.2): MODE-01 to MODE-05, INT-09
// to INT-11, REP-11 and the report (§13.1). A fake GitHub serves the OIDC
// token, the issuer's keys and the CODEOWNERS errors endpoint.

const digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

const workflowYAML = "name: proctor\non: pull_request\njobs: {}\n"

type fakeGitHub struct {
	srv        *httptest.Server
	key        *rsa.PrivateKey
	mu         sync.Mutex
	claims     map[string]any
	signWith   *rsa.PrivateKey
	coErrors   []map[string]any
	coStatus   int
	coRefs     []string
	coAuth     []string
	tokenCalls int
}

var (
	keyOnce   sync.Once
	githubKey *rsa.PrivateKey
	otherKey  *rsa.PrivateKey
)

func keys(t *testing.T) {
	keyOnce.Do(func() {
		var err error
		if githubKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
		if otherKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
}

func newGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	keys(t)
	g := &fakeGitHub{key: githubKey, signWith: githubKey, coStatus: 200, coErrors: []map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks", func(w http.ResponseWriter, r *http.Request) {
		pub := g.key.PublicKey
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.tokenCalls++
		if r.Header.Get("Authorization") != "Bearer runtime-request-token" || r.URL.Query().Get("audience") != "proctor" {
			http.Error(w, "bad request", 401)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"value": g.sign(t)})
	})
	mux.HandleFunc("/repos/acme/shop/codeowners/errors", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.coRefs = append(g.coRefs, r.URL.Query().Get("ref"))
		g.coAuth = append(g.coAuth, r.Header.Get("Authorization"))
		if g.coStatus != 200 {
			w.WriteHeader(g.coStatus)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"errors": g.coErrors})
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) sign(t *testing.T) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	head := enc(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	body := enc(g.claims)
	sum := sha256.Sum256([]byte(head + "." + body))
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.signWith, crypto.SHA256, sum[:])
	if err != nil {
		t.Error(err)
	}
	return head + "." + body + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// ciEnv clears every GitHub Actions variable Proctor reads, so tests
// behave the same inside and outside GitHub Actions, then sets the given
// ones.
func ciEnv(t *testing.T, set map[string]string) {
	for _, k := range []string{"ACTIONS_ID_TOKEN_REQUEST_URL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "GITHUB_REPOSITORY", "GITHUB_TOKEN",
		"GITHUB_API_URL", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "GITHUB_STEP_SUMMARY", "PROCTOR_TEST_OIDC_ISSUER", "CI", "GITHUB_ACTIONS"} {
		t.Setenv(k, set[k])
	}
}

// prRepo is a repository with a base commit B on main (with CODEOWNERS
// and the workflow), a pull request head H changing the application, and
// GitHub's test merge M of H into B.
type prRepo struct {
	*testRepo
	base, head, merge string
}

func newPRRepo(t *testing.T, change func(r *testRepo)) *prRepo {
	t.Helper()
	r := newRepo(t)
	r.write(".github/CODEOWNERS", "/proctor/ @acme/verification\n/.github/ @acme/verification\n")
	r.write(".github/workflows/proctor.yml", workflowYAML)
	r.write("proctor/proctor.yaml", configYAML+"protected_owners: [\"@acme/verification\"]\n")
	base := r.commit("base with owners")
	r.git("checkout", "-q", "-b", "feature")
	r.write("app/main.go", "package main\n\nfunc main() {}\n")
	if change != nil {
		change(r)
	}
	head := r.commit("feature")
	r.git("checkout", "-q", "main")
	r.git("merge", "-q", "--no-ff", "-m", "test merge", "feature")
	merge := r.git("rev-parse", "HEAD")
	return &prRepo{testRepo: r, base: base, head: head, merge: merge}
}

func (g *fakeGitHub) env(p *prRepo, withToken bool, summary string) map[string]string {
	e := map[string]string{
		"GITHUB_REPOSITORY": "acme/shop", "GITHUB_TOKEN": "ghs-secret-repo-token", "GITHUB_API_URL": g.srv.URL,
		"GITHUB_RUN_ID": "777", "GITHUB_RUN_ATTEMPT": "1", "GITHUB_STEP_SUMMARY": summary,
		"PROCTOR_TEST_OIDC_ISSUER": g.srv.URL,
	}
	if withToken {
		e["ACTIONS_ID_TOKEN_REQUEST_URL"] = g.srv.URL + "/token?api-version=2.0"
		e["ACTIONS_ID_TOKEN_REQUEST_TOKEN"] = "runtime-request-token"
	}
	g.claims = map[string]any{
		"iss": g.srv.URL, "aud": "proctor", "exp": time.Now().Add(5 * time.Minute).Unix(), "nbf": time.Now().Add(-time.Minute).Unix(),
		"repository": "acme/shop", "sha": p.merge, "event_name": "pull_request", "run_id": "777", "run_attempt": "1",
		"workflow_ref": "acme/shop/.github/workflows/proctor.yml@refs/pull/1/merge", "workflow_sha": p.merge,
		"job_workflow_ref": "acme/shop/.github/workflows/proctor.yml@refs/pull/1/merge", "job_workflow_sha": p.merge,
	}
	return e
}

func authBinding(t *testing.T, base, built, running string) string {
	img := ""
	if built != "" || running != "" {
		img = fmt.Sprintf("image: { built_digest: %q, running_digest: %q }\n", built, running)
	}
	return binding(t, fmt.Sprintf(`environment: ci
target:
  base_url: %s
  user_agent: proctor-test/1.0
identities:
  alice: { token_env: TEST_ALICE_TOKEN }
  bob: { token_env: TEST_BOB_TOKEN }
%s`, base, img))
}

type authResult struct {
	Result     string   `json:"result"`
	ExitCode   int      `json:"exit_code"`
	Mode       string   `json:"mode"`
	Violations []string `json:"violations"`
	Errors     []string `json:"errors"`
	Trust      struct {
		WorkflowIdentity      json.RawMessage `json:"workflow_identity"`
		WorkflowChanged       any             `json:"workflow_changed"`
		UnownedProtectedPaths []string        `json:"unowned_protected_paths"`
		CodeownersErrors      []string        `json:"codeowners_errors"`
		ProtectedDiffers      *bool           `json:"protected_differs_from_pr_head"`
		ApprovalRequired      bool            `json:"approval_required"`
	} `json:"trust"`
	Provenance struct {
		Repository   string            `json:"repository"`
		TestedCommit string            `json:"tested_commit"`
		Parents      []string          `json:"parents"`
		PRHead       string            `json:"pr_head"`
		Base         string            `json:"base"`
		SHA256       map[string]string `json:"sha256"`
		Environment  string            `json:"environment"`
		Image        map[string]string `json:"image"`
		CIRunID      string            `json:"ci_run_id"`
		RunID        string            `json:"run_id"`
		StartedAt    string            `json:"started_at"`
		EndedAt      string            `json:"ended_at"`
	} `json:"provenance"`
	ProtectedChanges *struct {
		Files []struct{ Change, Path string } `json:"files"`
	} `json:"protected_changes"`
	Scenarios []struct{ ID, Result string } `json:"scenarios"`
}

// authRun runs `proctor run --authoritative` with the OIDC test binary
// and returns the exit code, terminal output and the parsed report.
func authRun(t *testing.T, p *prRepo, bindingPath string, extra ...string) (int, string, authResult, string) {
	t.Helper()
	report := filepath.Join(t.TempDir(), "report.json")
	args := append([]string{"run", "--authoritative", "--commit", p.merge, "--pr-head", p.head, "--binding", bindingPath, "--report", report, "--repo", p.dir}, extra...)
	code, out, errOut := runBinary(t, proctorOIDCBin, args...)
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("no report (exit %d): %v\n%s\n%s", code, err, out, errOut)
	}
	var rep authResult
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("report isn't JSON: %v\n%s", err, raw)
	}
	return code, out + errOut, rep, string(raw)
}

func TestAuthoritativeRun(t *testing.T) {
	withTokens(t)
	app := newApp(t, projectsApp("Alice's project", 0))
	g := newGitHub(t)
	p := newPRRepo(t, nil)
	summary := filepath.Join(t.TempDir(), "summary.md")
	ciEnv(t, g.env(p, true, summary))
	code, out, rep, raw := authRun(t, p, authBinding(t, app.srv.URL, digest, digest))
	if code != 0 || rep.Result != "passed" || rep.Mode != "authoritative" {
		t.Fatalf("exit %d, result %s, mode %s\n%s", code, rep.Result, rep.Mode, out)
	}
	// INT-09: base is M's first parent, after checking its second is H.
	pv := rep.Provenance
	if pv.TestedCommit != p.merge || pv.Base != p.base || pv.PRHead != p.head || len(pv.Parents) != 2 {
		t.Errorf("provenance %+v; want tested %s base %s head %s", pv, p.merge, p.base, p.head)
	}
	if pv.Image["built_digest"] != digest || pv.Image["running_digest"] != digest {
		t.Errorf("provenance image %v", pv.Image)
	}
	if pv.Repository != "acme/shop" || pv.Environment != "ci" || pv.CIRunID != "777" || pv.RunID == "" || pv.StartedAt == "" || pv.EndedAt == "" {
		t.Errorf("provenance %+v", pv)
	}
	for _, k := range []string{"binding", "proctor/proctor.yaml", "proctor/manifest.yaml", "proctor/specs/project-access.yaml"} {
		if pv.SHA256[k] == "" {
			t.Errorf("no hash for %s in %v", k, pv.SHA256)
		}
	}
	// MODE-03: the verified identity, with every claim.
	var id map[string]string
	if err := json.Unmarshal(rep.Trust.WorkflowIdentity, &id); err != nil {
		t.Fatalf("workflow_identity: %s", rep.Trust.WorkflowIdentity)
	}
	for k, want := range map[string]string{"workflow_ref": "acme/shop/.github/workflows/proctor.yml@refs/pull/1/merge", "workflow_sha": p.merge,
		"job_workflow_sha": p.merge, "sha": p.merge, "event_name": "pull_request", "run_id": "777", "run_attempt": "1"} {
		if id[k] != want {
			t.Errorf("workflow_identity.%s = %q, want %q", k, id[k], want)
		}
	}
	// MODE-04: the workflow didn't change; INT-10: no protected change.
	if rep.Trust.WorkflowChanged != false || rep.Trust.ApprovalRequired || len(rep.ProtectedChanges.Files) != 0 {
		t.Errorf("workflow_changed %v, approval_required %v, changes %v", rep.Trust.WorkflowChanged, rep.Trust.ApprovalRequired, rep.ProtectedChanges)
	}
	if rep.Trust.ProtectedDiffers == nil || *rep.Trust.ProtectedDiffers {
		t.Errorf("protected_differs_from_pr_head %v", rep.Trust.ProtectedDiffers)
	}
	// INT-11: GitHub was asked about CODEOWNERS at the base commit.
	if len(g.coRefs) != 1 || g.coRefs[0] != p.base || g.coAuth[0] != "Bearer ghs-secret-repo-token" {
		t.Errorf("CODEOWNERS calls: refs %v", g.coRefs)
	}
	if len(rep.Trust.UnownedProtectedPaths) != 0 {
		t.Errorf("unowned %v", rep.Trust.UnownedProtectedPaths)
	}
	for _, secret := range []string{"ghs-secret-repo-token", "runtime-request-token", "tok-alice"} {
		if strings.Contains(raw+out, secret) {
			t.Errorf("%s appears in the report or output", secret)
		}
	}
	if !strings.Contains(out, "PROCTOR RUN  (authoritative)") || !strings.Contains(out, "RESULT: PASSED (exit 0, authoritative)") {
		t.Errorf("terminal output:\n%s", out)
	}
	s, _ := os.ReadFile(summary)
	if !strings.Contains(string(s), "## Proctor: PASSED (exit 0)") || strings.Contains(string(s), "not authoritative") {
		t.Errorf("job summary:\n%s", s)
	}
}

func TestAuthoritativeRefusals(t *testing.T) {
	withTokens(t)
	app := newApp(t, projectsApp("Alice's project", 0))
	cases := []struct {
		name    string
		change  func(r *testRepo)
		setup   func(g *fakeGitHub, p *prRepo, env map[string]string)
		binding func(t *testing.T) string
		args    []string
		want    int
		says    string
	}{
		{name: "running image differs from the built one (REP-11)", binding: func(t *testing.T) string {
			return authBinding(t, app.srv.URL, digest, strings.Replace(digest, "1", "2", 1))
		}, want: 4, says: "REP-11"},
		{name: "no image digests (REP-11)", binding: func(t *testing.T) string { return authBinding(t, app.srv.URL, "", "") }, want: 4, says: "REP-11"},
		{name: "malformed digest (REP-11)", binding: func(t *testing.T) string { return authBinding(t, app.srv.URL, "sha256:abc", "sha256:abc") }, want: 4, says: "REP-11"},
		{name: "second parent isn't the pull request head (INT-09)", setup: func(g *fakeGitHub, p *prRepo, env map[string]string) {
			p.head = p.base
		}, want: 4, says: "INT-09"},
		{name: "no GitHub token for the CODEOWNERS check (INT-11)", setup: func(g *fakeGitHub, p *prRepo, env map[string]string) {
			env["GITHUB_TOKEN"] = ""
		}, want: 4, says: "INT-11"},
		{name: "GitHub reports a CODEOWNERS error (INT-11)", setup: func(g *fakeGitHub, p *prRepo, env map[string]string) {
			g.coErrors = []map[string]any{{"line": 2, "kind": "Unknown owner", "path": ".github/CODEOWNERS", "message": "x"}}
		}, want: 4, says: "Unknown owner"},
		{name: "GitHub finds no CODEOWNERS (INT-11)", setup: func(g *fakeGitHub, p *prRepo, env map[string]string) { g.coStatus = 404 }, want: 4, says: "no CODEOWNERS"},
		{name: "token signed by another key (MODE-03)", setup: func(g *fakeGitHub, p *prRepo, env map[string]string) { g.signWith = otherKey }, want: 4, says: "signature doesn't verify"},
		{name: "expired token (MODE-03)", setup: func(g *fakeGitHub, p *prRepo, env map[string]string) {
			g.claims["exp"] = time.Now().Add(-10 * time.Minute).Unix()
		}, want: 4, says: "expired"},
		{name: "token for another audience (MODE-03)", setup: func(g *fakeGitHub, p *prRepo, env map[string]string) { g.claims["aud"] = "sts.amazonaws.com" }, want: 4, says: "audience"},
		{name: "token from another issuer (MODE-03)", setup: func(g *fakeGitHub, p *prRepo, env map[string]string) { g.claims["iss"] = "https://evil.example" }, want: 4, says: "issuer"},
		{name: "token for another commit (MODE-03)", setup: func(g *fakeGitHub, p *prRepo, env map[string]string) { g.claims["sha"] = p.head }, want: 4, says: "the workflow ran for commit"},
		{name: "token for another repository (MODE-03)", setup: func(g *fakeGitHub, p *prRepo, env map[string]string) { g.claims["repository"] = "acme/other" }, want: 4, says: "OIDC token is for acme/other"},
		{name: "token missing a claim (MODE-03)", setup: func(g *fakeGitHub, p *prRepo, env map[string]string) { delete(g.claims, "job_workflow_sha") }, want: 4, says: "no job_workflow_sha claim"},
		{name: "unlisted specification (INT-05)", change: func(r *testRepo) { r.write("proctor/specs/extra.yaml", specYAML) }, want: 4, says: "INT-05"},
		{name: "no base given (INT-09)", args: []string{"--pr-head", ""}, want: 3, says: "--pr-head or --base is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newGitHub(t)
			p := newPRRepo(t, c.change)
			env := g.env(p, true, "")
			if c.setup != nil {
				c.setup(g, p, env)
			}
			ciEnv(t, env)
			b := authBinding(t, app.srv.URL, digest, digest)
			if c.binding != nil {
				b = c.binding(t)
			}
			before := len(app.requests())
			code, out, rep, _ := authRun(t, p, b, c.args...)
			if code != c.want || !strings.Contains(out, c.says) {
				t.Fatalf("exit %d, want %d with %q\n%s", code, c.want, c.says, out)
			}
			if rep.ExitCode != code || rep.Mode != "authoritative" {
				t.Errorf("report exit %d, mode %s", rep.ExitCode, rep.Mode)
			}
			if n := len(app.requests()) - before; n != 0 {
				t.Errorf("the application got %d requests; nothing may run while a check fails", n)
			}
		})
	}
}

// §13.3: an authoritative run whose scenario fails reports "failed" with
// exit 1, like a local run.
func TestAuthoritativeFailure(t *testing.T) {
	withTokens(t)
	app := newApp(t, projectsApp("Someone else's project", 0))
	g := newGitHub(t)
	p := newPRRepo(t, nil)
	ciEnv(t, g.env(p, true, ""))
	code, out, rep, _ := authRun(t, p, authBinding(t, app.srv.URL, digest, digest))
	if code != 1 || rep.Result != "failed" || rep.ExitCode != 1 {
		t.Fatalf("exit %d, result %q\n%s", code, rep.Result, out)
	}
	if !strings.Contains(out, "RESULT: FAILED (exit 1, authoritative)") {
		t.Errorf("output:\n%s", out)
	}
}

// MODE-04, INT-10: a pull request that changes the workflow and a
// specification still runs, but the report says the workflow changed and
// approval is required, and the job summary says it's not authoritative.
func TestAuthoritativeWorkflowChanged(t *testing.T) {
	withTokens(t)
	app := newApp(t, projectsApp("Alice's project", 0))
	g := newGitHub(t)
	p := newPRRepo(t, func(r *testRepo) {
		r.write(".github/workflows/proctor.yml", workflowYAML+"# edited by the pull request\n")
		r.write("proctor/specs/project-access.yaml", strings.Replace(specYAML, "Anonymous users cannot list projects", "Anonymous users can't list projects", 1))
	})
	summary := filepath.Join(t.TempDir(), "summary.md")
	ciEnv(t, g.env(p, true, summary))
	code, out, rep, _ := authRun(t, p, authBinding(t, app.srv.URL, digest, digest))
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if rep.Trust.WorkflowChanged != true || !rep.Trust.ApprovalRequired {
		t.Errorf("workflow_changed %v, approval_required %v", rep.Trust.WorkflowChanged, rep.Trust.ApprovalRequired)
	}
	paths := []string{}
	for _, f := range rep.ProtectedChanges.Files {
		paths = append(paths, f.Change+" "+f.Path)
	}
	if strings.Join(paths, ",") != "modified .github/workflows/proctor.yml,modified proctor/specs/project-access.yaml" {
		t.Errorf("protected changes %v", paths)
	}
	if !strings.Contains(out, "APPROVAL REQUIRED  modified proctor/specs/project-access.yaml") {
		t.Errorf("output:\n%s", out)
	}
	s, _ := os.ReadFile(summary)
	if !strings.Contains(string(s), "not authoritative until the workflow change is approved") || !strings.Contains(string(s), "approval required") {
		t.Errorf("job summary:\n%s", s)
	}
}

// MODE-03, AT-W-13, AT-W-16: without id-token: write the identity is
// "unverified" and workflow_changed is "unknown"; INT-11 still runs.
// A reusable workflow from another repository is option A: MODE-04 is
// not_applicable.
func TestAuthoritativeIdentity(t *testing.T) {
	withTokens(t)
	app := newApp(t, projectsApp("Alice's project", 0))
	t.Run("no OIDC token", func(t *testing.T) {
		g := newGitHub(t)
		p := newPRRepo(t, nil)
		ciEnv(t, g.env(p, false, ""))
		code, out, rep, _ := authRun(t, p, authBinding(t, app.srv.URL, digest, digest))
		if code != 0 || string(rep.Trust.WorkflowIdentity) != `"unverified"` || rep.Trust.WorkflowChanged != "unknown" || g.tokenCalls != 0 {
			t.Fatalf("exit %d, identity %s, changed %v\n%s", code, rep.Trust.WorkflowIdentity, rep.Trust.WorkflowChanged, out)
		}
	})
	t.Run("required workflow from another repository", func(t *testing.T) {
		g := newGitHub(t)
		p := newPRRepo(t, nil)
		ciEnv(t, g.env(p, true, ""))
		g.claims["job_workflow_ref"] = "acme/policies/.github/workflows/proctor.yml@refs/heads/main"
		g.claims["job_workflow_sha"] = "0123456789012345678901234567890123456789"
		code, out, rep, _ := authRun(t, p, authBinding(t, app.srv.URL, digest, digest))
		if code != 0 || rep.Trust.WorkflowChanged != "not_applicable" {
			t.Fatalf("exit %d, changed %v\n%s", code, rep.Trust.WorkflowChanged, out)
		}
	})
	t.Run("the release build ignores the test issuer", func(t *testing.T) {
		g := newGitHub(t)
		p := newPRRepo(t, nil)
		ciEnv(t, g.env(p, true, ""))
		report := filepath.Join(t.TempDir(), "report.json")
		code, out, errOut := runProctor(t, "run", "--authoritative", "--commit", p.merge, "--pr-head", p.head,
			"--binding", authBinding(t, app.srv.URL, digest, digest), "--report", report, "--repo", p.dir)
		// The release binary fetches keys only from GitHub's issuer, so a
		// token from the fake issuer can never verify.
		if code != 4 || !strings.Contains(out+errOut, "MODE-03") {
			t.Fatalf("exit %d\n%s%s", code, out, errOut)
		}
	})
}

// INT-11 (report only, OD-16): a protected path whose last matching
// CODEOWNERS rule names no protected owner is listed, without failing.
func TestAuthoritativeUnownedPaths(t *testing.T) {
	withTokens(t)
	app := newApp(t, projectsApp("Alice's project", 0))
	g := newGitHub(t)
	r := newRepo(t)
	r.write(".github/CODEOWNERS", "* @someone\n/.github/ @acme/verification\n/proctor/specs/ @acme/verification\n")
	r.write("proctor/proctor.yaml", configYAML+"protected_owners: [\"@acme/verification\"]\n")
	base := r.commit("base")
	r.git("checkout", "-q", "-b", "feature")
	head := r.commit("feature")
	r.git("checkout", "-q", "main")
	r.git("merge", "-q", "--no-ff", "-m", "test merge", "feature")
	p := &prRepo{testRepo: r, base: base, head: head, merge: r.git("rev-parse", "HEAD")}
	ciEnv(t, g.env(p, false, ""))
	code, out, rep, _ := authRun(t, p, authBinding(t, app.srv.URL, digest, digest))
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if strings.Join(rep.Trust.UnownedProtectedPaths, ",") != "proctor/manifest.yaml,proctor/proctor.yaml" {
		t.Errorf("unowned %v", rep.Trust.UnownedProtectedPaths)
	}
}

// MODE-01, AT-W-15: the binding can't come from the checkout; CI variables
// alone never make a run authoritative; authoritative flags need
// --authoritative; the report is required.
func TestAuthoritativeInvocation(t *testing.T) {
	withTokens(t)
	app := newApp(t, projectsApp("Alice's project", 0))
	g := newGitHub(t)
	p := newPRRepo(t, nil)
	ciEnv(t, g.env(p, false, ""))
	inside := filepath.Join(p.dir, "binding.yaml")
	data, _ := os.ReadFile(authBinding(t, app.srv.URL, digest, digest))
	os.WriteFile(inside, data, 0o600)
	code, out, rep, _ := authRun(t, p, inside)
	if code != 3 || !strings.Contains(out, "MODE-01") || len(rep.Errors) == 0 {
		t.Errorf("binding inside the checkout: exit %d\n%s", code, out)
	}
	t.Setenv("CI", "true")
	t.Setenv("GITHUB_ACTIONS", "true")
	code, text, errOut := p.proctor("run", "--binding", bindingFor(t, app.srv.URL))
	expect(t, code, text, errOut, 0, "PROCTOR RUN  (local, non-authoritative)", "RESULT: PASSED (exit 0, local, non-authoritative)")
	code, text, errOut = p.proctor("run", "--binding", bindingFor(t, app.srv.URL), "--report", filepath.Join(t.TempDir(), "r.json"))
	expect(t, code, text, errOut, 3, "only for --authoritative")
	code, text, errOut = p.proctor("run", "--authoritative", "--pr-head", p.head, "--binding", bindingFor(t, app.srv.URL))
	expect(t, code, text, errOut, 3, "--report is required")
}
