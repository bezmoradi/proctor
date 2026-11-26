package runner

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/bezmoradi/proctor/protocol"
	"github.com/bezmoradi/proctor/protocol/schema"
	"proctor/internal/applogs"
	"proctor/internal/expr"
	"proctor/internal/integrity"
	"proctor/internal/model"
)

//go:embed official.sum
var officialSum string

var (
	officialName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	sumHex       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	refPattern   = regexp.MustCompile(`\$\{([^}]*)\}`)
)

type host struct {
	name     string
	official string
	program  string
	platform string
	declared map[string]string
	tmp      string
	path     string
	sha      string
	origin   string
	client   *protocol.Client
	hello    protocol.HelloResult
	ops      map[string]protocol.Operation
	entries  map[string]protocol.PolicyEntry
	proofs   []Proof
	stderr   string
	dropped  int64
	closed   bool
	dead     bool
}

type session struct {
	opt   Options
	hosts map[string]*host
	scrub *scrubber
	files model.ProtectedFiles
}

func pins() (map[string]string, error) {
	out := map[string]string{}
	for i, line := range strings.Split(officialSum, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || !sumHex.MatchString(f[0]) || !strings.HasPrefix(f[1], "proctor-driver-") {
			return nil, fmt.Errorf("the built-in official.sum line %d isn't \"<sha256>  proctor-driver-<name>\" (INT-17)", i+1)
		}
		if _, dup := out[f[1]]; dup {
			return nil, fmt.Errorf("the built-in official.sum lists %s twice (INT-17)", f[1])
		}
		out[f[1]] = f[0]
	}
	return out, nil
}

func fileSHA(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

const maxProgram = 256 << 20

func runPlatform() string { return runtime.GOOS + "/" + runtime.GOARCH }

func resolveRepoHost(name string, dc model.DriverConfig, opt Options, probs *problems) *host {
	h := &host{name: name, program: dc.Program, platform: runPlatform(), declared: dc.Operations}
	if opt.RepoDir == "" {
		probs.add(ExitError, "driver %s: this command has no repository to read the program %s from", name, dc.Program)
		return nil
	}
	tmp, file, sha, code, err := copyRepoProgram(opt.RepoDir, dc.Program)
	if err != nil {
		probs.add(code, "driver %s: %v", name, err)
		return nil
	}
	h.tmp, h.path, h.sha = tmp, file, sha
	want, pinned := dc.SHA256[h.platform]
	switch {
	case pinned && want != sha:
		probs.add(ExitIntegrity, "driver %s: program %s has checksum %s on %s, but drivers.%s.sha256.%s pins %s; rebuild it from the source, or update drivers.%s.sha256.%s (INT-17)", name, dc.Program, sha, h.platform, name, h.platform, want, name, h.platform)
	case !pinned && opt.Authoritative:
		probs.add(ExitIntegrity, "driver %s: drivers.%s.sha256 pins no checksum for %s, this run's platform, and an authoritative run refuses unpinned drivers (INT-17)", name, name, h.platform)
	case pinned:
		h.origin = OriginRepository
		return h
	default:
		h.origin = OriginUnpinned
		return h
	}
	_ = os.RemoveAll(tmp)
	return nil
}

func copyRepoProgram(repoDir, rel string) (tmp, file, sum string, code int, err error) {
	if !filepath.IsLocal(filepath.FromSlash(rel)) || path.Clean(rel) != rel || strings.ContainsAny(rel, "\\\x00") {
		return "", "", "", ExitInvalid, fmt.Errorf("program %q isn't a clean path inside the repository (INT-12)", rel)
	}
	root, err := os.OpenRoot(repoDir)
	if err != nil {
		return "", "", "", ExitError, fmt.Errorf("cannot open the repository %s: %v", repoDir, err)
	}
	defer root.Close()
	parts := strings.Split(rel, "/")
	var last fs.FileInfo
	for i := range parts {
		sub := strings.Join(parts[:i+1], "/")
		fi, err := root.Lstat(filepath.FromSlash(sub))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return "", "", "", ExitError, fmt.Errorf("program %s doesn't exist; build it from its source first, with the command in Proctor's docs/guides/repository-drivers.md", rel)
		case err != nil:
			return "", "", "", ExitError, fmt.Errorf("cannot read program %s: %v", rel, err)
		case fi.Mode()&fs.ModeSymlink != 0:
			return "", "", "", ExitIntegrity, fmt.Errorf("%s is a symbolic link; a repository driver's program and the folders above it must be a regular file and folders in the repository (INT-21)", sub)
		case i < len(parts)-1 && !fi.IsDir():
			return "", "", "", ExitError, fmt.Errorf("program %s doesn't exist: %s isn't a folder", rel, sub)
		}
		last = fi
	}
	switch {
	case !last.Mode().IsRegular():
		return "", "", "", ExitIntegrity, fmt.Errorf("program %s isn't a regular file (INT-21)", rel)
	case last.Size() > maxProgram:
		return "", "", "", ExitIntegrity, fmt.Errorf("program %s is larger than 256 MiB, the most Proctor runs (INT-21)", rel)
	}
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return "", "", "", ExitError, fmt.Errorf("cannot read program %s: %v", rel, err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return "", "", "", ExitError, fmt.Errorf("cannot read program %s: %v", rel, err)
	}
	if !os.SameFile(opened, last) || !opened.Mode().IsRegular() {
		return "", "", "", ExitIntegrity, fmt.Errorf("program %s changed while it was read (INT-21)", rel)
	}
	tmp, err = os.MkdirTemp("", "proctor-driver-")
	if err != nil {
		return "", "", "", ExitError, fmt.Errorf("cannot make a private folder for program %s under TMPDIR: %v", rel, err)
	}
	fail := func(code int, format string, args ...any) (string, string, string, int, error) {
		_ = os.RemoveAll(tmp)
		return "", "", "", code, fmt.Errorf(format, args...)
	}
	file = filepath.Join(tmp, path.Base(rel))
	out, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return fail(ExitError, "cannot copy program %s to %s: %v", rel, tmp, err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(f, maxProgram+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return fail(ExitError, "cannot copy program %s to %s: %v", rel, tmp, err)
	case n > maxProgram:
		return fail(ExitIntegrity, "program %s is larger than 256 MiB, the most Proctor runs (INT-21)", rel)
	}
	return tmp, file, hex.EncodeToString(h.Sum(nil)), 0, nil
}

func ResolveDrivers(cfg *model.Config, opt Options) []string {
	var out []string
	if cfg == nil {
		return nil
	}
	for _, name := range sortedKeys(cfg.Drivers) {
		dc := cfg.Drivers[name]
		if dc.Program == "" || opt.Withheld[name] != "" {
			continue
		}
		var probs problems
		if h := resolveRepoHost(name, dc, opt, &probs); h != nil {
			_ = os.RemoveAll(h.tmp)
		}
		if probs.code == ExitIntegrity {
			out = append(out, probs.msgs...)
		}
	}
	return out
}

func WithoutDrivers(cfg *model.Config, names map[string]string) *model.Config {
	if cfg == nil || len(names) == 0 {
		return cfg
	}
	out := *cfg
	out.Drivers = map[string]model.DriverConfig{}
	for name, dc := range cfg.Drivers {
		if names[name] == "" {
			out.Drivers[name] = dc
		}
	}
	return &out
}

func driverEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func resolveConnection(name string, conn map[string]any, lookup func(string) (string, bool), probs *problems, s *scrubber) (any, protocol.Secrets) {
	secrets := protocol.Secrets{}
	if conn == nil {
		return nil, secrets
	}
	out := map[string]any{}
	for _, k := range sortedKeys(conn) {
		if !strings.HasSuffix(k, "_env") {
			out[k] = conn[k]
		}
	}
	for _, k := range sortedKeys(conn) {
		if !strings.HasSuffix(k, "_env") {
			continue
		}
		key := strings.TrimSuffix(k, "_env")
		env, ok := conn[k].(string)
		switch {
		case key == "":
			probs.add(ExitInvalid, "binding: drivers.%s: %q isn't a valid key; name the value before _env", name, k)
		case !ok || env == "":
			probs.add(ExitInvalid, "binding: drivers.%s.%s must name an environment variable", name, k)
		case out[key] != nil:
			probs.add(ExitInvalid, "binding: drivers.%s has both %s and %s; use one", name, key, k)
		default:
			v, ok := lookup(env)
			if !ok || v == "" {
				probs.add(ExitInvalid, "binding: drivers.%s.%s: environment variable %s is not set", name, k, env)
				continue
			}
			secrets[key] = v
			s.know(v, fmt.Sprintf("the value of binding drivers.%s.%s", name, k))
		}
	}
	return out, secrets
}

func checkValue(sch any, v any) []string {
	if sch == nil {
		if v == nil {
			return nil
		}
		return []string{"$: the driver takes no value here; leave it out"}
	}
	return schema.Check(sch, v)
}

func openSession(ctx context.Context, cfg *model.Config, b *model.Binding, opt Options, s *scrubber) (*session, error) {
	sess := &session{opt: opt, hosts: map[string]*host{}, scrub: s, files: cfg.Files()}
	var probs problems
	sums, err := pins()
	if err != nil {
		probs.add(ExitIntegrity, "%v", err)
		return nil, probs.err(s)
	}
	for _, name := range sortedKeys(cfg.Drivers) {
		dc := cfg.Drivers[name]
		if opt.Withheld[name] != "" {
			continue
		}
		if dc.Program != "" {
			if h := resolveRepoHost(name, dc, opt, &probs); h != nil {
				sess.hosts[name] = h
			}
			continue
		}
		if !officialName.MatchString(dc.Official) {
			probs.add(ExitInvalid, "proctor.yaml: drivers.%s: official %q must name an official driver, such as http", name, dc.Official)
			continue
		}
		file := "proctor-driver-" + dc.Official
		h := &host{name: name, official: dc.Official, path: filepath.Join(opt.DriverDir, file)}
		sha, err := fileSHA(h.path)
		if err != nil {
			probs.add(ExitError, "driver %s: cannot read %s: %v", name, h.path, err)
			continue
		}
		h.sha = sha
		want, pinned := sums[file]
		switch {
		case pinned && want != sha:
			probs.add(ExitIntegrity, "driver %s: %s has checksum %s, but this proctor expects %s (INT-17)", name, h.path, sha, want)
			continue
		case !pinned && opt.Authoritative:
			probs.add(ExitIntegrity, "driver %s: %s isn't pinned in this proctor's official.sum, and an authoritative run refuses unpinned drivers (INT-17)", name, file)
			continue
		case pinned:
			h.origin = OriginOfficial
		default:
			h.origin = OriginUnpinned
		}
		sess.hosts[name] = h
	}
	conns := map[string]any{}
	secrets := map[string]protocol.Secrets{}
	if b != nil {
		for _, name := range sortedKeys(b.Drivers) {
			if _, ok := cfg.Drivers[name]; !ok && opt.Withheld[name] == "" {
				probs.add(ExitInvalid, "binding: driver %q is not declared in proctor.yaml", name)
			}
		}
		for _, name := range sortedKeys(cfg.Drivers) {
			conns[name], secrets[name] = resolveConnection(name, b.Drivers[name], opt.LookupEnv, &probs, s)
		}
		addRepository(cfg, b, opt, conns, &probs)
		checkBindingAppLogs(cfg, b, &probs)
	}
	if err := probs.err(s); err != nil {
		sess.close()
		return nil, err
	}
	policies := map[string]any{}
	for _, h := range sess.ordered() {
		policy, err := cfg.PolicyOf(h.name)
		if err != nil {
			probs.add(ExitInvalid, "proctor.yaml: %v (INT-18)", err)
			continue
		}
		policies[h.name] = policy
	}
	if err := probs.err(s); err != nil {
		sess.close()
		return nil, err
	}
	for _, h := range sess.ordered() {
		sec := secrets[h.name]
		if sec == nil {
			sec = protocol.Secrets{}
		}
		sess.start(ctx, h, policies[h.name], conns[h.name], sec, &probs)
	}
	if err := probs.err(s); err != nil {
		sess.close()
		return nil, err
	}
	return sess, nil
}

const (
	officialCLI   = "cli"
	repositoryKey = "repository"
)

func addRepository(cfg *model.Config, b *model.Binding, opt Options, conns map[string]any, probs *problems) {
	for _, name := range sortedKeys(cfg.Drivers) {
		dc := cfg.Drivers[name]
		if dc.Program != "" || dc.Official != officialCLI || opt.Withheld[name] != "" {
			continue
		}
		set := false
		for _, k := range []string{repositoryKey, repositoryKey + "_env"} {
			if _, has := b.Drivers[name][k]; has {
				probs.add(ExitInvalid, "binding: drivers.%s.%s is set by Proctor; remove it", name, k)
				set = true
			}
		}
		if set {
			continue
		}
		root, err := filepath.Abs(opt.Checkout)
		if opt.Checkout == "" || err != nil || opt.ProtectedRoot == "" {
			probs.add(ExitError, "driver %s: this command has no repository for the commands to run in", name)
			continue
		}
		paths := []any{}
		for _, p := range cfg.ProtectedPaths {
			paths = append(paths, p)
		}
		conn, _ := conns[name].(map[string]any)
		if conn == nil {
			conn = map[string]any{}
		}
		conn[repositoryKey] = map[string]any{"root": root, "protected_root": opt.ProtectedRoot, "protected_paths": paths}
		conns[name] = conn
	}
}

func checkBindingAppLogs(cfg *model.Config, b *model.Binding, probs *problems) {
	switch {
	case cfg.AppLogs == nil && b.AppLogs != nil:
		probs.add(ExitInvalid, "binding: app_logs is set, but proctor.yaml has no app_logs, so the application's log would not be checked; remove app_logs from the binding or add it to proctor.yaml (EXE-10)")
	case cfg.AppLogs == nil:
	case b.AppLogs == nil || (b.AppLogs.File == "" && b.AppLogs.OTel == ""):
		probs.add(ExitInvalid, "binding: app_logs.file or app_logs.otel is required, because proctor.yaml has app_logs: give the path of the application's log file, or the endpoint of the proctor-sim-otel receiver the application exports its logs to (EXE-10)")
	case b.AppLogs.File != "" && b.AppLogs.OTel != "":
		probs.add(ExitInvalid, "binding: app_logs has both file and otel; give exactly one (EXE-10)")
	case b.AppLogs.OTel != "":
		if err := applogs.CheckOTelEndpoint(b.AppLogs.OTel); err != nil {
			probs.add(ExitInvalid, "binding: app_logs.otel %q %v", b.AppLogs.OTel, err)
		}
		a := cfg.AppLogs
		for _, f := range []struct {
			key string
			set bool
		}{{"format", a.Format != ""}, {"level_field", a.LevelField != ""}, {"message_field", a.MessageField != ""}, {"level_numbers", a.LevelNumbers != nil}} {
			if f.set {
				probs.add(ExitInvalid, "binding: app_logs.otel reads records whose severity and body the receiver gives, so proctor.yaml app_logs.%s can't be set with it; remove it (EXE-10)", f.key)
			}
		}
	case !filepath.IsAbs(b.AppLogs.File):
		probs.add(ExitInvalid, "binding: app_logs.file %q must be an absolute path", b.AppLogs.File)
	}
}

func (sess *session) scans() bool {
	for _, h := range sess.hosts {
		for _, op := range h.ops {
			if len(op.Scan) > 0 {
				return true
			}
		}
	}
	return false
}

func (sess *session) ordered() []*host {
	out := make([]*host, 0, len(sess.hosts))
	for _, k := range sortedKeys(sess.hosts) {
		out = append(out, sess.hosts[k])
	}
	return out
}

func (sess *session) callCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, sess.opt.CallTimeout+callMargin)
}

func (sess *session) start(ctx context.Context, h *host, policy, conn any, secrets protocol.Secrets, probs *problems) {
	c, err := protocol.Start(ctx, h.path, nil, driverEnv())
	if err != nil && h.program != "" {
		probs.add(ExitError, "driver %s: cannot start the private copy %s of program %s: %v; it sits under TMPDIR (%s), which must allow running programs", h.name, h.path, h.program, err, os.TempDir())
		return
	}
	if err != nil {
		probs.add(ExitError, "driver %s: cannot start %s: %v", h.name, h.path, err)
		return
	}
	h.client = c
	cctx, cancel := sess.callCtx(ctx)
	err = c.Call(cctx, protocol.MethodHello, protocol.HelloParams{Protocol: protocol.Version, CoreVersion: CoreVersion}, &h.hello)
	cancel()
	if err != nil {
		probs.add(ExitError, "driver %s: hello failed: %v%s", h.name, err, stderrHint(c))
		return
	}
	if msgs := checkHello(h); len(msgs) > 0 {
		for _, m := range msgs {
			probs.add(ExitError, "driver %s: %s", h.name, m)
		}
		return
	}
	if msgs := h.checkOperations(); len(msgs) > 0 {
		for _, m := range msgs {
			probs.add(ExitInvalid, "proctor.yaml: drivers.%s.operations: %s (INT-21)", h.name, m)
		}
		return
	}
	bad := false
	for _, m := range checkValue(h.hello.PolicySchema, policy) {
		probs.add(ExitInvalid, "proctor.yaml: drivers.%s.policy%s", h.name, strings.TrimPrefix(m, "$"))
		bad = true
	}
	for _, m := range checkValue(h.hello.ConnectionSchema, conn) {
		probs.add(ExitInvalid, "binding: drivers.%s%s", h.name, strings.TrimPrefix(m, "$"))
		bad = true
	}
	if bad {
		return
	}
	cctx, cancel = sess.callCtx(ctx)
	var configured protocol.ConfigureResult
	err = c.Call(cctx, protocol.MethodConfigure, protocol.ConfigureParams{Policy: policy, Connection: conn, Secrets: secrets}, &configured)
	cancel()
	if err != nil {
		code := ExitInvalid
		if brokenClient(err) || asProtocolError(err).Data.Cause == protocol.CauseDriverFailed {
			code = ExitError
		}
		probs.add(code, "driver %s: configure was refused: %v", h.name, err)
		return
	}
	for _, m := range h.takeEntries(configured.Entries) {
		probs.add(ExitError, "driver %s: configure: %s", h.name, m)
	}
}

func (h *host) takeEntries(entries []protocol.PolicyEntry) []string {
	if entries == nil {
		return []string{"the answer has no entries list, so the policy entries no step uses can't be found; the driver is older than this proctor"}
	}
	var msgs []string
	h.entries = map[string]protocol.PolicyEntry{}
	for _, e := range entries {
		switch {
		case strings.TrimSpace(e.Key) == "" || strings.TrimSpace(e.Rule) == "" || strings.TrimSpace(e.Use) == "":
			msgs = append(msgs, fmt.Sprintf("policy entry %q needs a key, a rule and a use", e.Key))
		case h.entries[e.Key].Key != "":
			msgs = append(msgs, fmt.Sprintf("policy entry %q is listed twice", e.Key))
		default:
			h.entries[e.Key] = e
		}
	}
	return msgs
}

func (sess *session) entries() map[string][]model.PolicyEntry {
	out := map[string][]model.PolicyEntry{}
	for _, h := range sess.ordered() {
		list := []model.PolicyEntry{}
		for _, k := range sortedKeys(h.entries) {
			e := h.entries[k]
			list = append(list, model.PolicyEntry{Key: e.Key, Rule: e.Rule, Use: e.Use})
		}
		out[h.name] = list
	}
	return out
}

func stderrHint(c *protocol.Client) string {
	b := strings.TrimSpace(string(c.Stderr()))
	if b == "" {
		return ""
	}
	if i := strings.LastIndexByte(b, '\n'); i >= 0 {
		b = b[i+1:]
	}
	if len(b) > 200 {
		b = b[:200]
	}
	return "; its last log line: " + b
}

func checkHello(h *host) []string {
	var msgs []string
	r := h.hello
	if r.Protocol != protocol.Version {
		msgs = append(msgs, fmt.Sprintf("speaks protocol %d; this proctor speaks protocol %d", r.Protocol, protocol.Version))
	}
	for what, sch := range map[string]any{"policy_schema": r.PolicySchema, "connection_schema": r.ConnectionSchema} {
		if sch == nil {
			continue
		}
		for _, e := range schema.CheckSchema(sch) {
			msgs = append(msgs, what+": "+e)
		}
	}
	h.ops = map[string]protocol.Operation{}
	for _, op := range r.Operations {
		where := fmt.Sprintf("operation %q", op.Name)
		if op.Name == "" {
			msgs = append(msgs, "an operation has no name")
			continue
		}
		if _, dup := h.ops[op.Name]; dup {
			msgs = append(msgs, where+" is listed twice")
			continue
		}
		switch op.Kind {
		case protocol.KindAct, protocol.KindObserve, protocol.KindByArgs:
		default:
			msgs = append(msgs, fmt.Sprintf("%s: unknown kind %q", where, op.Kind))
		}
		switch op.Credential {
		case protocol.CredentialNone, protocol.CredentialOptional, protocol.CredentialRequired:
		default:
			msgs = append(msgs, fmt.Sprintf("%s: unknown credential %q", where, op.Credential))
		}
		if op.Accumulates && op.Kind != protocol.KindObserve {
			msgs = append(msgs, fmt.Sprintf("%s: accumulates is only for an operation of kind observe", where))
		}
		for what, sch := range map[string]any{"args_schema": op.ArgsSchema, "fact_schema": op.FactSchema} {
			if sch == nil {
				continue
			}
			for _, e := range schema.CheckSchema(sch) {
				msgs = append(msgs, where+": "+what+": "+e)
			}
		}
		escape := map[string]string{}
		for _, p := range sortedKeys(op.Escape) {
			c, ok := expr.Canonical(p)
			if !ok {
				_, err := expr.ParsePath(p)
				msgs = append(msgs, fmt.Sprintf("%s: escape: %v", where, err))
				c = p
			}
			if op.Escape[p] != protocol.EscapePathSegment {
				msgs = append(msgs, fmt.Sprintf("%s: escape %s: unknown escaping %q (this proctor knows %q)", where, p, op.Escape[p], protocol.EscapePathSegment))
			}
			if _, dup := escape[c]; dup {
				msgs = append(msgs, fmt.Sprintf("%s: escape names %s twice", where, c))
			}
			escape[c] = op.Escape[p]
		}
		if op.Escape != nil {
			op.Escape = escape
		}
		for what, list := range map[string][]string{"redact": op.Redact, "data": op.Data, "files": op.Files, "scan": op.Scan} {
			for i, p := range list {
				c, ok := expr.Canonical(p)
				if !ok {
					_, err := expr.ParsePath(p)
					msgs = append(msgs, fmt.Sprintf("%s: %s: %v", where, what, err))
					continue
				}
				list[i] = c
			}
		}
		h.ops[op.Name] = op
	}
	sort.Strings(msgs)
	return msgs
}

func (h *host) checkOperations() []string {
	if h.program == "" {
		return nil
	}
	var msgs []string
	for _, name := range sortedKeys(h.ops) {
		kind := h.ops[name].Kind
		want, listed := h.declared[name]
		switch {
		case kind == protocol.KindByArgs:
			msgs = append(msgs, fmt.Sprintf("the driver's operation %q has kind by_args, which a repository driver can't use; give it one kind, act or observe", name))
		case !listed:
			msgs = append(msgs, fmt.Sprintf("the driver has operation %q (%s), which operations doesn't list", name, kind))
		case want != kind:
			msgs = append(msgs, fmt.Sprintf("operation %q is %s in the driver but %s here", name, kind, want))
		}
	}
	for _, name := range sortedKeys(h.declared) {
		if _, ok := h.ops[name]; !ok {
			msgs = append(msgs, fmt.Sprintf("operations lists %q, which the driver doesn't have", name))
		}
	}
	return msgs
}

func (sess *session) close() {
	for _, h := range sess.ordered() {
		if h.tmp != "" && (h.client == nil || h.closed) {
			_ = os.RemoveAll(h.tmp)
		}
		if h.closed || h.client == nil {
			continue
		}
		h.closed = true
		_ = h.client.Close()
		raw := sess.scrub.clean(string(h.client.Stderr()))
		h.dropped = h.client.StderrDropped()
		if len(raw) > stderrKeep {
			cut := len(raw) - stderrKeep
			if i := strings.IndexByte(raw[cut:], '\n'); i >= 0 {
				cut += i + 1
			}
			h.dropped += int64(cut)
			raw = raw[cut:]
		}
		h.stderr = raw
		if h.tmp != "" {
			_ = os.RemoveAll(h.tmp)
		}
	}
}

func (sess *session) infos() []DriverInfo {
	var out []DriverInfo
	for _, h := range sess.ordered() {
		d := DriverInfo{Name: h.name, Official: h.official, Program: h.program, Platform: h.platform, Version: h.hello.Version, Origin: h.origin, SHA256: h.sha,
			Proofs: h.proofs, Stderr: h.stderr, StderrDropped: h.dropped}
		out = append(out, d)
	}
	return out
}

func Describe(ctx context.Context, cfg *model.Config, specs []SpecFile, b *model.Binding, opt Options) (Description, []DriverInfo, error) {
	opt, err := opt.withDefaults(cfg)
	if err != nil {
		return Description{}, nil, asSetup(err)
	}
	s := &scrubber{}
	sess, err := openSession(ctx, cfg, b, opt, s)
	if err != nil {
		return Description{}, nil, err
	}
	infos, err := sess.describe(ctx, specs)
	if err == nil {
		err = sess.describeInvariants(ctx, cfg, infos)
	}
	sess.close()
	if err != nil {
		return Description{}, nil, err
	}
	return Description{Steps: infos, Entries: sess.entries()}, sess.infos(), nil
}

func asSetup(err error) *SetupError {
	var se *SetupError
	if errors.As(err, &se) {
		return se
	}
	return &SetupError{ExitCode: ExitError, Msgs: []string{err.Error()}}
}

type Description struct {
	Steps   map[model.StepRef]model.StepInfo
	Entries map[string][]model.PolicyEntry
}

func (sess *session) describe(ctx context.Context, specs []SpecFile) (map[model.StepRef]model.StepInfo, error) {
	out := map[model.StepRef]model.StepInfo{}
	var probs problems
	for _, f := range specs {
		if f.Spec == nil {
			continue
		}
		for _, sc := range f.Spec.Scenarios {
			for _, part := range []struct {
				phase string
				steps []model.Step
			}{{"setup", sc.Setup}, {"steps", sc.Steps}, {"cleanup", sc.Cleanup}} {
				phase := part.phase
				for i, st := range part.steps {
					if name, _, ok := model.SplitDo(st.Do); !ok || sess.hosts[name] == nil {
						continue
					}
					info, err := sess.validateStep(ctx, st, phase != "steps")
					if err != nil {
						probs.add(ExitError, "%s: scenario %s %s[%d]: %v", f.Path, sc.ID, phase, i, err)
						continue
					}
					out[model.StepRef{Spec: f.Path, Scenario: sc.ID, Phase: phase, Index: i}] = info
				}
			}
		}
	}
	if err := probs.err(sess.scrub); err != nil {
		return nil, err
	}
	return out, nil
}

func (sess *session) validateStep(ctx context.Context, st model.Step, setup bool) (model.StepInfo, error) {
	info := model.StepInfo{StateCheck: true}
	problem := func(rule, path, format string, args ...any) {
		info.Problems = append(info.Problems, model.Problem{Rule: rule, Path: path, Message: sess.scrub.clean(fmt.Sprintf(format, args...))})
	}
	name, opName, ok := model.SplitDo(st.Do)
	if !ok {
		problem("EXE-01", "do", "do %q must be <driver>.<operation>", st.Do)
		return info, nil
	}
	h := sess.hosts[name]
	if h == nil {
		problem("EXE-01", "do", "driver %q isn't declared in proctor.yaml", name)
		return info, nil
	}
	op, ok := h.ops[opName]
	if !ok {
		problem("EXE-01", "do", "driver %s has no operation %q (it has %s)", name, opName, strings.Join(sortedKeys(h.ops), ", "))
		return info, nil
	}
	info.OpensHandle, info.UsesHandle = op.OpensHandle, op.UsesHandle
	switch {
	case st.Variant != "" && st.Identity == "":
		problem("EXE-01", "variant", "variant %q needs an identity", st.Variant)
	case st.Identity != "" && op.Credential == protocol.CredentialNone:
		problem("EXE-01", "identity", "operation %s takes no credential, so it can't have an identity", st.Do)
	case st.Identity == "" && op.Credential == protocol.CredentialRequired:
		problem("EXE-01", "identity", "operation %s needs a credential; name an identity", st.Do)
	}
	switch {
	case op.ArgsSchema == nil && st.With != nil:
		problem("EXE-01", "with", "operation %s takes no arguments; leave with out", st.Do)
	case op.ArgsSchema != nil:
		for _, m := range schema.Check(op.ArgsSchema, argsToCheck(op.ArgsSchema, st.With), schema.AllowRefs) {
			at, msg, _ := strings.Cut(m, ": ")
			problem("EXE-01", "with"+strings.TrimPrefix(at, "$"), "%s", msg)
		}
	}
	for _, p := range integrity.FactPaths(st.Do, op.FactSchema, st.Expect, st.Save) {
		problem(p.Rule, p.Path, "%s", p.Message)
	}
	if p, found := integrity.ArrivalProblem(st.Do, &st); op.Accumulates && found {
		problem(p.Rule, p.Path, "%s", p.Message)
	}
	files, fileProblems := sess.readFiles(op, st.With)
	for _, p := range fileProblems {
		problem(p.Rule, p.Path, "%s", p.Message)
	}
	var sent map[string]protocol.FileInfo
	if len(files) > 0 {
		info.Files = map[string]model.FileRef{}
		sent = map[string]protocol.FileInfo{}
		for at, f := range files {
			info.Files[at] = model.FileRef{Path: f.Path, Size: f.Size, SHA256: f.SHA256}
			sent[at] = f.FileInfo
		}
	}
	var res protocol.ValidateResult
	cctx, cancel := sess.callCtx(ctx)
	err := h.client.Call(cctx, protocol.MethodValidate, protocol.ValidateParams{Op: opName, Args: st.With, Identity: st.Identity,
		Variant: st.Variant, Expect: st.Expect, Setup: setup, Save: st.Save, Files: sent}, &res)
	cancel()
	if err != nil {
		return info, fmt.Errorf("driver %s: validate failed: %v", name, err)
	}
	for _, p := range res.Problems {
		info.Problems = append(info.Problems, model.Problem{Rule: p.Rule, Message: sess.scrub.clean(p.Message), Path: p.Path})
	}
	switch op.Kind {
	case protocol.KindByArgs:
		switch res.Kind {
		case protocol.KindAct, protocol.KindObserve:
			info.Kind = res.Kind
		default:
			info.Kind = protocol.KindAct
			problem("EXE-01", "do", "driver %s answered kind %q for %s; expected act or observe", name, res.Kind, st.Do)
		}
	default:
		info.Kind = op.Kind
	}
	info.Destructive, info.FixedTarget = res.Destructive, res.FixedTarget
	info.Data = append([]string(nil), op.Data...)
	info.Targets = append([]string(nil), res.Targets...)
	info.Shows = append([]string(nil), res.Shows...)
	info.ExactTargets = append([]string(nil), res.ExactTargets...)
	if err := h.checkUses(res); err != nil {
		return info, fmt.Errorf("driver %s: validate: %v", name, err)
	}
	info.Uses = append([]string(nil), res.Uses...)
	if len(res.ShowsEntries) > 0 {
		info.ShowsEntries = map[string]string{}
		for k, v := range res.ShowsEntries {
			info.ShowsEntries[k] = v
		}
	}
	if res.StateCheck != nil {
		info.StateCheck = *res.StateCheck
	}
	switch {
	case res.NoStateChange && h.program != "":
		problem("INT-21", "do", "driver %s answered no_state_change for %s, which a repository driver can't use; an act on a repository driver needs a state check like any act", name, st.Do)
	case res.NoStateChange:
		info.NoStateChange = info.Kind == protocol.KindAct
	}
	seen := map[string]bool{}
	for _, n := range res.Refs {
		seen[n] = true
	}
	for _, n := range refsIn(st.With) {
		seen[n] = true
	}
	info.Refs = sortedKeys(seen)
	return info, nil
}

func (h *host) checkUses(res protocol.ValidateResult) error {
	for _, k := range res.Uses {
		if _, ok := h.entries[k]; !ok {
			return fmt.Errorf("the answer uses policy entry %q, which configure didn't list", k)
		}
	}
	if len(res.ShowsEntries) != len(res.Shows) {
		return fmt.Errorf("the answer shows %d paths but names the policy entries of %d", len(res.Shows), len(res.ShowsEntries))
	}
	for _, p := range res.Shows {
		k, ok := res.ShowsEntries[p]
		if !ok {
			return fmt.Errorf("the answer shows %s but names no policy entry for it", p)
		}
		if _, listed := h.entries[k]; !listed {
			return fmt.Errorf("the answer shows %s through policy entry %q, which configure didn't list", p, k)
		}
	}
	return nil
}

func argsToCheck(argsSchema, with any) any {
	if with != nil || len(schema.Check(argsSchema, nil)) == 0 {
		return with
	}
	if sh, errs := schema.ShapeOf(argsSchema); len(errs) == 0 && sh.Allows("object") {
		return map[string]any{}
	}
	return with
}

func refsIn(v any) []string {
	n, err := normalizeJSON(v)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var walk func(any)
	scan := func(s string) {
		for _, m := range refPattern.FindAllStringSubmatch(s, -1) {
			seen[m[1]] = true
		}
	}
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			scan(x)
		case map[string]any:
			for k, e := range x {
				scan(k)
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(n)
	return sortedKeys(seen)
}

func asProtocolError(err error) *protocol.Error {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe
	}
	return protocol.Errorf(protocol.CauseDriverFailed, "%v", err)
}

func brokenClient(err error) bool {
	return errors.Is(err, protocol.ErrDriverFailed) || errors.Is(err, protocol.ErrTimeout)
}

func (sess *session) readFiles(op protocol.Operation, with any) (map[string]protocol.File, []model.Problem) {
	var out map[string]protocol.File
	var probs []model.Problem
	bad := func(at, format string, args ...any) {
		probs = append(probs, model.Problem{Rule: fileRule, Path: "with" + strings.TrimPrefix(at, "$"), Message: fmt.Sprintf(format, args...)})
	}
	for _, at := range op.Files {
		p, err := expr.ParsePath(at)
		if err != nil {
			continue
		}
		raw, ok := p.Select(with)
		if !ok {
			continue
		}
		rel, isText := raw.(string)
		switch {
		case !isText:
			bad(at, "must be the path of a file inside the protected root, as text, not %s", describeValue(raw))
			continue
		case strings.Contains(rel, "${"):
			bad(at, "%q refers to a value; name the file as written, since Proctor reads and hashes it before the run", rel)
			continue
		case sess.files == nil:
			bad(at, "%q can't be read: this command has no protected files to read it from", rel)
			continue
		}
		full, data, err := sess.files.ReadProtected(rel)
		if err != nil {
			bad(at, "%v", err)
			continue
		}
		if out == nil {
			out = map[string]protocol.File{}
		}
		sum := sha256.Sum256(data)
		out[at] = protocol.File{FileInfo: protocol.FileInfo{Path: full, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}, Data: data}
	}
	return out, probs
}

const fileRule = "INT-13"

func describeValue(v any) string {
	switch v.(type) {
	case nil:
		return "nothing"
	case map[string]any:
		return "a mapping"
	case []any:
		return "a list"
	case bool:
		return "true or false"
	case string:
		return "text"
	}
	return "a number"
}

func (sess *session) describeInvariants(ctx context.Context, cfg *model.Config, out map[model.StepRef]model.StepInfo) error {
	var probs problems
	for i, st := range cfg.Invariants {
		if name, _, ok := model.SplitDo(st.Do); !ok || sess.hosts[name] == nil {
			continue
		}
		info, err := sess.validateStep(ctx, st, false)
		if err != nil {
			probs.add(ExitError, "proctor.yaml: invariants[%d]: %v", i, err)
			continue
		}
		out[model.InvariantRef(i)] = info
	}
	return probs.err(sess.scrub)
}
