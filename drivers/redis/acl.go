package redis

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bezmoradi/proctor/protocol"
)

const (
	proofName    = "read_only (" + ruleReadOnly + ")"
	proveTimeout = 5 * time.Second
	grant        = "+@read +acl|whoami +acl|getuser"
)

var safeCommands = map[string]bool{
	"ping": true, "echo": true, "select": true, "hello": true, "auth": true,
	"acl|whoami": true, "acl|getuser": true, "client|getname": true, "client|id": true,
	"type": true, "pttl": true, "ttl": true, "exists": true, "get": true, "getrange": true, "strlen": true,
	"hlen": true, "hscan": true, "hget": true, "hgetall": true, "llen": true, "lrange": true,
	"scard": true, "sscan": true, "smembers": true, "zcard": true, "zrange": true, "zscan": true, "scan": true,
}

func (d *Driver) Prove(ctx context.Context) (protocol.ProveResult, error) {
	proof := func(status, format string, args ...any) (protocol.ProveResult, error) {
		return protocol.ProveResult{Proofs: []protocol.Proof{{Name: proofName, Status: status, Detail: fmt.Sprintf(format, args...)}}}, nil
	}
	if d.client == nil {
		return protocol.ProveResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the redis driver has no connection")
	}
	ctx, cancel := context.WithTimeout(ctx, proveTimeout)
	defer cancel()
	unable := func(err error, command string) (protocol.ProveResult, error) {
		msg, isServer := serverError(err)
		switch {
		case isServer && strings.HasPrefix(msg, "NOPERM"):
			return proof(protocol.ProofUnproven, "the user can't run %s, so the driver can't read its rules; grant it +acl|whoami +acl|getuser to let the driver prove it can't write. The driver itself sends only read commands", command)
		case isServer && (strings.Contains(strings.ToLower(msg), "unknown command") || strings.Contains(strings.ToLower(msg), "unknown subcommand")):
			return proof(protocol.ProofUnproven, "the server answers %s with %q: it has no ACL (Redis before 6), so nothing stops the connection from writing. The driver itself sends only read commands", command, d.conn.scrub(msg))
		}
		return proof(protocol.ProofFailed, "%s", d.conn.classify(ctx, err, command+" on "+d.conn.where()).Message)
	}
	who, err := d.client.Do(ctx, "ACL", "WHOAMI").Text()
	if err != nil {
		return unable(err, "ACL WHOAMI")
	}
	raw, err := d.client.Do(ctx, "ACL", "GETUSER", who).Result()
	if err != nil {
		return unable(err, "ACL GETUSER "+who)
	}
	info := asMap(raw)
	if info == nil {
		return proof(protocol.ProofFailed, "ACL GETUSER %s answered something other than the user's rules", who)
	}
	problems := d.ruleProblems("", info)
	if sel, ok := info["selectors"].([]any); ok {
		for i, s := range sel {
			m := asMap(s)
			if m == nil {
				problems = append(problems, fmt.Sprintf("selector %d can't be read", i+1))
				continue
			}
			problems = append(problems, d.ruleProblems(fmt.Sprintf("selector %d: ", i+1), m)...)
		}
	}
	if len(problems) > 0 {
		return proof(protocol.ProofUnproven, "user %s can do more than read the policy's keys: %s. Redis doesn't stop the connection from doing it; the driver itself sends only read commands. For a provable user, run ACL SETUSER <name> on ><password> resetkeys ~<pattern> -@all %s", who, strings.Join(problems, "; "), grant)
	}
	return proof(protocol.ProofProven, "user %s may run only read commands (%s) and reach only keys within the policy's patterns (%s)", who, strings.TrimSpace(asText(info["commands"])), d.policyText())
}

func asMap(v any) map[string]any {
	out := map[string]any{}
	switch m := v.(type) {
	case map[any]any:
		for k, e := range m {
			out[fmt.Sprint(k)] = e
		}
	case map[string]any:
		return m
	case []any:
		if len(m)%2 != 0 {
			return nil
		}
		for i := 0; i < len(m); i += 2 {
			out[fmt.Sprint(m[i])] = m[i+1]
		}
	default:
		return nil
	}
	return out
}

func asText(v any) string {
	s, _ := v.(string)
	return s
}

func (d *Driver) ruleProblems(prefix string, info map[string]any) []string {
	var out []string
	cmds, ok := info["commands"].(string)
	if !ok {
		return []string{prefix + "its command rules can't be read"}
	}
	out = append(out, commandProblems(prefix, cmds)...)
	var patterns []string
	switch k := info["keys"].(type) {
	case string:
		for _, t := range strings.Fields(k) {
			switch {
			case t == "allkeys":
				patterns = append(patterns, "*")
			case t == "resetkeys":
				patterns = nil
			case strings.HasPrefix(t, "~"):
				patterns = append(patterns, t[1:])
			case strings.HasPrefix(t, "%"):
				if _, p, found := strings.Cut(t, "~"); found {
					patterns = append(patterns, p)
				} else {
					out = append(out, fmt.Sprintf("%sits key rule %s can't be read", prefix, t))
				}
			default:
				out = append(out, fmt.Sprintf("%sits key rule %s can't be read", prefix, t))
			}
		}
	case []any:
		for _, e := range k {
			s, _ := e.(string)
			patterns = append(patterns, strings.TrimPrefix(s, "~"))
		}
	case nil:
	default:
		out = append(out, prefix+"its key rules can't be read")
	}
	for _, p := range patterns {
		g, err := parseGlob(p)
		if err != nil || len(d.covering(g)) == 0 {
			out = append(out, fmt.Sprintf("%sits key pattern ~%s reaches keys outside the policy's patterns", prefix, p))
		}
	}
	return out
}

func commandProblems(prefix, rules string) []string {
	tokens := strings.Fields(strings.ToLower(rules))
	from := 0
	for i, t := range tokens {
		if t == "-@all" || t == "nocommands" {
			from = i + 1
		}
	}
	seen := map[string]bool{}
	var unsafe []string
	for i := from; i < len(tokens); i++ {
		t := tokens[i]
		var name string
		switch {
		case t == "allcommands":
			name = "@all"
		case strings.HasPrefix(t, "+"):
			name = t[1:]
		default:
			continue
		}
		if name == "@read" || safeCommands[name] || withdrawn(name, tokens[i+1:]) || seen[name] {
			continue
		}
		seen[name] = true
		unsafe = append(unsafe, "+"+name)
	}
	if len(unsafe) == 0 {
		return nil
	}
	sort.Strings(unsafe)
	return []string{fmt.Sprintf("%sits rules %s allow commands besides @read and the few connection commands the driver needs, and may include writes", prefix, strings.Join(unsafe, " "))}
}

func withdrawn(name string, later []string) bool {
	parent, _, _ := strings.Cut(name, "|")
	for _, t := range later {
		if t == "-"+name || (parent != name && t == "-"+parent) {
			return true
		}
	}
	return false
}
