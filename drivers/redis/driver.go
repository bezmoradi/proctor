package redis

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/redis/go-redis/v9"

	"github.com/bezmoradi/proctor/protocol"
	"github.com/bezmoradi/proctor/protocol/schema"
)

var Version = "0.0.0-dev"

const (
	Name         = "redis"
	OpKey        = "key"
	OpKeys       = "keys"
	MaxValue     = 64 << 10
	MaxKeys      = 1000
	MaxPatterns  = 64
	CallTimeout  = 10 * time.Second
	ReadyAttempt = 3 * time.Second
	ruleArgs     = "REDIS-01"
	ruleScope    = "REDIS-02"
	rulePolicy   = "REDIS-03"
	ruleBinding  = "REDIS-04"
	ruleReadOnly = "REDIS-05"
)

var (
	text     = map[string]any{"type": "string"}
	flag     = map[string]any{"type": "boolean"}
	count    = map[string]any{"type": "integer", "minimum": 0}
	whole    = map[string]any{"type": "integer"}
	textList = map[string]any{"type": "array", "items": text}
	Types    = []string{"none", "string", "hash", "list", "set", "zset"}
)

var KeyArgsSchema = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"key": text},
	"required":             []string{"key"},
	"additionalProperties": false,
}

var KeysArgsSchema = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"match": text},
	"required":             []string{"match"},
	"additionalProperties": false,
}

var KeyFactSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"exists":          flag,
		"type":            map[string]any{"type": "string", "enum": Types},
		"value":           true,
		"value_json":      true,
		"value_truncated": flag,
		"size":            count,
		"ttl_ms":          whole,
		"overflow":        flag,
		"overflow_reason": text,
	},
	"required":             []string{"exists", "overflow", "size", "ttl_ms", "type", "value", "value_truncated"},
	"additionalProperties": false,
}

var KeysFactSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"count":           count,
		"keys":            textList,
		"overflow":        flag,
		"overflow_reason": text,
	},
	"required":             []string{"count", "keys", "overflow"},
	"additionalProperties": false,
}

var PolicySchema = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"keys": textList},
	"required":             []string{"keys"},
	"additionalProperties": false,
}

var ConnectionSchema = map[string]any{
	"type": []string{"object", "null"},
	"properties": map[string]any{
		"address": text,
		"sentinel": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"addresses": textList,
				"master":    text,
			},
			"required":             []string{"addresses", "master"},
			"additionalProperties": false,
		},
		"db":        map[string]any{"type": "integer", "minimum": 0, "maximum": 1 << 20},
		"username":  text,
		"tls":       flag,
		"ca_bundle": text,
	},
	"additionalProperties": false,
}

type pattern struct {
	text string
	g    glob
}

type Driver struct {
	patterns []pattern
	conn     *binding
	client   redis.UniversalClient
}

func New() *Driver {
	redis.SetLogger(quiet{})
	return &Driver{}
}

type quiet struct{}

func (quiet) Printf(context.Context, string, ...any) {}

func (d *Driver) Hello(ctx context.Context, p protocol.HelloParams) (protocol.HelloResult, error) {
	return protocol.HelloResult{
		Name:     Name,
		Version:  Version,
		Protocol: protocol.Version,
		Operations: []protocol.Operation{{
			Name:       OpKey,
			Kind:       protocol.KindObserve,
			ArgsSchema: KeyArgsSchema,
			FactSchema: KeyFactSchema,
			Credential: protocol.CredentialNone,
		}, {
			Name:       OpKeys,
			Kind:       protocol.KindObserve,
			ArgsSchema: KeysArgsSchema,
			FactSchema: KeysFactSchema,
			Credential: protocol.CredentialNone,
		}},
		PolicySchema:     PolicySchema,
		ConnectionSchema: ConnectionSchema,
	}, nil
}

func (d *Driver) Configure(ctx context.Context, p protocol.ConfigureParams) error {
	patterns, err := parsePolicy(p.Policy)
	if err != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "%v", err)
	}
	d.patterns = patterns
	if p.Connection == nil {
		if len(p.Secrets) > 0 {
			return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: a secret was given without a connection (%s)", ruleBinding)
		}
		return nil
	}
	b, err := parseBinding(p.Connection, p.Secrets)
	if err != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "%v", err)
	}
	d.conn = b
	d.client = b.client()
	return nil
}

func parsePolicy(raw any) ([]pattern, error) {
	if msgs := schema.Check(PolicySchema, raw); len(msgs) > 0 {
		return nil, fmt.Errorf("policy: %s; the redis driver's policy is keys: [<pattern>, ...], the key patterns its steps may read (%s)", strings.Join(msgs, "; "), rulePolicy)
	}
	var pol struct {
		Keys []string `json:"keys"`
	}
	if err := protocol.Decode(raw, &pol); err != nil {
		return nil, fmt.Errorf("policy: %v (%s)", err, rulePolicy)
	}
	var msgs []string
	bad := func(format string, args ...any) {
		msgs = append(msgs, "policy: "+fmt.Sprintf(format, args...))
	}
	switch {
	case len(pol.Keys) == 0:
		bad("keys is empty; list the key patterns the steps may read, such as session:*")
	case len(pol.Keys) > MaxPatterns:
		bad("keys has %d patterns; at most %d are allowed", len(pol.Keys), MaxPatterns)
	}
	seen := map[string]int{}
	var out []pattern
	for i, k := range pol.Keys {
		at := fmt.Sprintf("keys[%d]", i)
		if why := patternProblem(k); why != "" {
			bad("%s: pattern %q %s", at, k, why)
			continue
		}
		if j, dup := seen[k]; dup {
			bad("%s repeats keys[%d] (%s)", at, j, k)
			continue
		}
		seen[k] = i
		g, err := parseGlob(k)
		if err != nil {
			bad("%s: pattern %q: %v", at, k, err)
			continue
		}
		out = append(out, pattern{text: k, g: g})
	}
	if len(msgs) > 0 {
		return nil, fmt.Errorf("%s (%s)", strings.Join(msgs, "; "), rulePolicy)
	}
	return out, nil
}

func patternProblem(k string) string {
	switch {
	case k == "":
		return "is empty"
	case strings.Contains(k, "${"):
		return "can't hold ${...}; the policy is fixed in proctor.yaml"
	case strings.ContainsFunc(k, unicode.IsControl):
		return "can't hold a newline or another control character"
	case len(k) > 1024:
		return "is longer than 1024 bytes"
	}
	return ""
}

func entryKey(p string) string {
	return "keys: " + p
}

func (d *Driver) PolicyEntries() []protocol.PolicyEntry {
	out := make([]protocol.PolicyEntry, 0, len(d.patterns))
	for _, p := range d.patterns {
		out = append(out, protocol.PolicyEntry{Key: entryKey(p.text), Rule: rulePolicy, Use: "a redis.key or redis.keys read of a key that matches " + p.text})
	}
	return out
}

func (d *Driver) covering(g glob) []string {
	var out []string
	for _, p := range d.patterns {
		if g.within(p.g) {
			out = append(out, p.text)
		}
	}
	return out
}

func (d *Driver) allows(key string) bool {
	for _, p := range d.patterns {
		if p.g.match(key) {
			return true
		}
	}
	return false
}

func (d *Driver) policyText() string {
	list := make([]string, len(d.patterns))
	for i, p := range d.patterns {
		list[i] = p.text
	}
	return strings.Join(list, ", ")
}

func (d *Driver) Ready(ctx context.Context) (protocol.ReadyResult, error) {
	if d.client == nil {
		return protocol.ReadyResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the redis driver has no connection")
	}
	ctx, cancel := context.WithTimeout(ctx, ReadyAttempt)
	defer cancel()
	if err := d.client.Ping(ctx).Err(); err != nil {
		if msg, ok := serverError(err); ok && strings.HasPrefix(msg, "NOPERM") && strings.Contains(strings.ToLower(msg), "'ping'") {
			return protocol.ReadyResult{Ready: true, Detail: fmt.Sprintf("%s answered PING with NOPERM: the user may not run PING, but the server is up and accepted the login; the driver reads keys matching %s", d.conn.where(), d.policyText())}, nil
		}
		e := d.conn.classify(ctx, err, "PING on "+d.conn.where())
		return protocol.ReadyResult{Detail: e.Message, Lasting: e.Data.Lasting}, nil
	}
	return protocol.ReadyResult{Ready: true, Detail: fmt.Sprintf("PING on %s answered PONG; the driver reads keys matching %s", d.conn.where(), d.policyText())}, nil
}

func (d *Driver) Close(ctx context.Context, p protocol.CloseParams) error {
	return protocol.Errorf(protocol.CauseInvalidRequest, "the redis driver opens no handles")
}

func (d *Driver) Shutdown() {
	if d.client != nil {
		_ = d.client.Close()
	}
}

func callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, CallTimeout)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
