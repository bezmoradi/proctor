package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/bezmoradi/proctor/protocol"
)

const (
	policyRule     = "CLI-01"
	connectionRule = "CLI-02"
	argsRule       = "CLI-03"
	locationRule   = "CLI-04"
	expectRule     = "CLI-05"
	maxTimeout     = 10 * time.Minute
)

var (
	commandName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	envName     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	targetName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,254}$`)
	passedEnv   = []string{"PATH", "HOME", "TMPDIR"}
)

type command struct {
	Name        string   `json:"-"`
	Program     string   `json:"program"`
	Args        []string `json:"args"`
	Kind        string   `json:"kind"`
	Targets     []string `json:"targets"`
	Destructive bool     `json:"destructive"`
	ExtraArgs   int      `json:"extra_args"`
	Dir         string   `json:"dir"`
	Env         []string `json:"env"`
	Timeout     string   `json:"timeout"`
	limit       time.Duration
}

func (c *command) reported() []string {
	if len(c.Targets) > 0 {
		return c.Targets
	}
	return []string{"command/" + c.Name}
}

func parsePolicy(raw any) (map[string]*command, error) {
	var msgs []string
	bad := func(format string, args ...any) {
		msgs = append(msgs, "policy: "+fmt.Sprintf(format, args...))
	}
	pol, isObject := raw.(map[string]any)
	switch {
	case raw == nil:
		bad("is required: the cli driver runs only commands its policy declares, as commands: { <name>: { program, args, kind } }")
		return nil, ruleError(msgs, policyRule)
	case !isObject:
		bad("must be an object with commands, not %s", show(raw))
		return nil, ruleError(msgs, policyRule)
	}
	for _, k := range sortedKeys(pol) {
		if k != "commands" {
			bad("unknown key %q; the cli driver's policy has only commands", k)
		}
	}
	list, isMap := pol["commands"].(map[string]any)
	switch {
	case !isMap:
		bad("commands must be an object of named commands, not %s", show(pol["commands"]))
	case len(list) == 0:
		bad("commands is empty; declare at least one command")
	case len(list) > maxCommands:
		bad("commands declares %d commands; at most %d are allowed", len(list), maxCommands)
	}
	out := map[string]*command{}
	for _, name := range sortedKeys(list) {
		at := "commands." + name
		if !commandName.MatchString(name) {
			bad("%s: the name must match %s", at, commandName.String())
			continue
		}
		c := &command{Name: name}
		if err := protocol.Decode(list[name], c); err != nil {
			bad("%s: %v", at, err)
			continue
		}
		for _, m := range c.check() {
			bad("%s: %s", at, m)
		}
		out[name] = c
	}
	if len(msgs) > 0 {
		return nil, ruleError(msgs, policyRule)
	}
	return out, nil
}

func (c *command) check() []string {
	var msgs []string
	bad := func(format string, args ...any) {
		msgs = append(msgs, fmt.Sprintf(format, args...))
	}
	if err := checkProgram(c.Program); err != nil {
		bad("program: %v", err)
	}
	switch c.Kind {
	case protocol.KindAct:
		if len(c.Targets) == 0 {
			bad("targets is required for an act command: name what it changes, such as targets: [users], so a later step can check that state (EXE-07)")
		}
	case protocol.KindObserve:
		if c.Destructive {
			bad("destructive is only for an act command")
		}
	default:
		bad("kind must be act or observe, not %s", show(c.Kind))
	}
	if len(c.Args) > maxArgs {
		bad("args has %d arguments; at most %d are allowed", len(c.Args), maxArgs)
	}
	for i, a := range c.Args {
		if strings.ContainsRune(a, 0) {
			bad("args[%d] contains a NUL character", i)
		}
		if strings.Contains(a, "${") {
			bad("args[%d] %q can't hold ${...}; fixed arguments are written as they are passed, and a step passes its own in args", i, a)
		}
	}
	if c.ExtraArgs < 0 || c.ExtraArgs > maxExtra {
		bad("extra_args must be 0 to %d", maxExtra)
	}
	if len(c.Args)+c.ExtraArgs > maxArgs {
		bad("args and extra_args allow %d arguments together; at most %d are allowed", len(c.Args)+c.ExtraArgs, maxArgs)
	}
	if len(c.Targets) > maxTargets {
		bad("targets lists %d targets; at most %d are allowed", len(c.Targets), maxTargets)
	}
	seen := map[string]bool{}
	for i, t := range c.Targets {
		switch {
		case !targetName.MatchString(t):
			bad("targets[%d] %q must match %s", i, t, targetName.String())
		case strings.HasPrefix(t, "command/"):
			bad("targets[%d] %q can't start with command/, which the driver reports for an observe command without targets", i, t)
		case seen[t]:
			bad("targets[%d] repeats %q", i, t)
		}
		seen[t] = true
	}
	if c.Dir != "" {
		if err := checkLocal(c.Dir); err != nil {
			bad("dir: %v", err)
		}
	}
	if len(c.Env) > maxEnv {
		bad("env lists %d variables; at most %d are allowed", len(c.Env), maxEnv)
	}
	envSeen := map[string]bool{}
	for i, e := range c.Env {
		switch {
		case !envName.MatchString(e):
			bad("env[%d] %q isn't a variable name", i, e)
		case slices.Contains(passedEnv, e):
			bad("env[%d] %s is always passed, from Proctor's own environment; leave it out", i, e)
		case envSeen[e]:
			bad("env[%d] repeats %s", i, e)
		}
		envSeen[e] = true
	}
	if c.Timeout != "" {
		d, err := time.ParseDuration(c.Timeout)
		switch {
		case err != nil:
			bad("timeout %q isn't a duration such as 30s", c.Timeout)
		case d <= 0 || d > maxTimeout:
			bad("timeout must be above 0 and at most %s", maxTimeout)
		default:
			c.limit = d
		}
	}
	return msgs
}

func checkProgram(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("is required: a program on PATH, such as psql, an absolute path, or a path from the repository root, such as bin/admin")
	case hasControl(p):
		return fmt.Errorf("can't hold a control character")
	case strings.ContainsAny(p, "\\"):
		return fmt.Errorf("can't hold a backslash")
	case strings.HasPrefix(p, "/"):
		if path.Clean(p) != p {
			return fmt.Errorf("%q must be a clean path, with no .., . or repeated /", p)
		}
	case strings.Contains(p, "/"):
		return checkLocal(p)
	case p == "." || p == "..":
		return fmt.Errorf("%q isn't a program", p)
	}
	return nil
}

func checkLocal(p string) error {
	switch {
	case hasControl(p) || strings.ContainsAny(p, "\\"):
		return fmt.Errorf("%q can't hold a backslash or a control character", p)
	case strings.HasPrefix(p, "/"):
		return fmt.Errorf("%q must be relative to the repository root", p)
	case path.Clean(p) != p || !filepath.IsLocal(filepath.FromSlash(p)):
		return fmt.Errorf("%q must be a clean path inside the repository, with no .., . or repeated /", p)
	}
	return nil
}

func parseConnection(raw any, secrets protocol.Secrets, commands map[string]*command) (*repository, map[string]string, error) {
	var msgs []string
	bad := func(format string, args ...any) {
		msgs = append(msgs, "connection: "+fmt.Sprintf(format, args...))
	}
	var conn struct {
		Repository *struct {
			Root           string   `json:"root"`
			ProtectedRoot  string   `json:"protected_root"`
			ProtectedPaths []string `json:"protected_paths"`
		} `json:"repository"`
	}
	if err := protocol.Decode(raw, &conn); err != nil {
		bad("%v; the cli binding has repository and <NAME>_env keys only", err)
		return nil, nil, ruleError(msgs, connectionRule)
	}
	if conn.Repository == nil {
		bad("repository is required: { root, protected_root, protected_paths }, the repository the commands run in and the folders a pull request can't change without review")
		return nil, nil, ruleError(msgs, connectionRule)
	}
	r := conn.Repository
	repo := &repository{root: r.Root, protectedRoot: r.ProtectedRoot}
	switch {
	case !filepath.IsAbs(r.Root) || filepath.Clean(r.Root) != r.Root:
		bad("repository.root %q must be a clean absolute path", r.Root)
	default:
		real, err := filepath.EvalSymlinks(r.Root)
		if err != nil {
			bad("repository.root %s can't be read: %v", r.Root, pathProblem(err))
		}
		repo.real = real
	}
	if err := checkLocal(r.ProtectedRoot); err != nil || r.ProtectedRoot == "" {
		bad("repository.protected_root %q must be a folder relative to the repository root, such as proctor", r.ProtectedRoot)
	}
	for i, e := range r.ProtectedPaths {
		if err := checkLocal(strings.TrimSuffix(e, "/")); err != nil || e == "" || e == "/" {
			bad("repository.protected_paths[%d] %q must be a path relative to the repository root, ending in / for a folder", i, e)
			continue
		}
		repo.protectedPaths = append(repo.protectedPaths, e)
	}
	listed := map[string]bool{}
	for _, c := range commands {
		for _, e := range c.Env {
			listed[e] = true
		}
	}
	env := map[string]string{}
	for _, name := range sortedKeys(secrets) {
		switch {
		case !listed[name]:
			bad("%s_env gives %s, which no command lists in env; remove it", name, name)
		case strings.ContainsRune(secrets[name], 0):
			bad("the value of %s_env contains a NUL character", name)
		default:
			env[name] = secrets[name]
		}
	}
	for _, name := range sortedKeys(listed) {
		if _, ok := secrets[name]; !ok {
			bad("%s_env is required: a command lists %s in env, so name the environment variable that holds its value, as %s_env: <VARIABLE>", name, name, name)
		}
	}
	if len(msgs) > 0 {
		return nil, nil, ruleError(msgs, connectionRule)
	}
	return repo, env, nil
}

func ruleError(msgs []string, rule string) error {
	return fmt.Errorf("%s (%s)", strings.Join(msgs, "; "), rule)
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
