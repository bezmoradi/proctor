package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"time"

	"proctor/protocol"
	"proctor/protocol/schema"
)

var refRE = regexp.MustCompile(`\$\{([^}]*)\}`)

var operations = []protocol.Operation{
	{
		Name:       "echo",
		Kind:       protocol.KindObserve,
		ArgsSchema: map[string]any{"type": "object"},
		FactSchema: map[string]any{"type": "object"},
		Credential: protocol.CredentialOptional,
	},
	{
		Name:       "fail",
		Kind:       protocol.KindObserve,
		ArgsSchema: map[string]any{"type": "object", "additionalProperties": false},
		FactSchema: true,
		Credential: protocol.CredentialNone,
	},
	{
		Name: "sleep",
		Kind: protocol.KindObserve,
		ArgsSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"ms": map[string]any{"type": "integer", "minimum": 0, "maximum": 600000}},
			"required":             []string{"ms"},
			"additionalProperties": false,
		},
		FactSchema: map[string]any{"type": "object", "properties": map[string]any{"slept_ms": map[string]any{"type": "integer"}}},
		Credential: protocol.CredentialNone,
	},
	{
		Name: "open",
		Kind: protocol.KindObserve,
		ArgsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"refuse":     map[string]any{"type": "boolean"},
				"fail_close": map[string]any{"type": "boolean"},
			},
			"additionalProperties": false,
		},
		FactSchema: map[string]any{"type": "object", "properties": map[string]any{
			"opened": map[string]any{"type": "boolean"},
			"handle": map[string]any{"type": "string"},
		}},
		OpensHandle: true,
		Credential:  protocol.CredentialNone,
	},
	{
		Name:       "poke",
		Kind:       protocol.KindObserve,
		ArgsSchema: map[string]any{"type": "object", "additionalProperties": false},
		FactSchema: map[string]any{"type": "object", "properties": map[string]any{
			"handle": map[string]any{"type": "string"},
			"count":  map[string]any{"type": "integer"},
		}},
		UsesHandle: true,
		Credential: protocol.CredentialNone,
	},
	{
		Name:       "die",
		Kind:       protocol.KindObserve,
		ArgsSchema: map[string]any{"type": "object", "additionalProperties": false},
		FactSchema: true,
		Credential: protocol.CredentialNone,
	},
}

type conn struct {
	pokes     int
	failClose bool
}

type fake struct {
	next  int
	conns map[string]*conn
}

func (f *fake) Hello(ctx context.Context, p protocol.HelloParams) (protocol.HelloResult, error) {
	return protocol.HelloResult{
		Name:             "fake",
		Version:          "0.0.0",
		Protocol:         protocol.Version,
		Operations:       operations,
		PolicySchema:     map[string]any{"type": []string{"object", "null"}, "additionalProperties": false},
		ConnectionSchema: map[string]any{"type": []string{"object", "null"}, "properties": map[string]any{"label": map[string]any{"type": "string"}}, "additionalProperties": false},
	}, nil
}

func (f *fake) Configure(ctx context.Context, p protocol.ConfigureParams) error {
	fmt.Fprintf(os.Stderr, "configured with %d secrets\n", len(p.Secrets))
	return nil
}

func find(op string) (protocol.Operation, bool) {
	for _, o := range operations {
		if o.Name == op {
			return o, true
		}
	}
	return protocol.Operation{}, false
}

func (f *fake) Validate(ctx context.Context, p protocol.ValidateParams) (protocol.ValidateResult, error) {
	o, ok := find(p.Op)
	if !ok {
		return protocol.ValidateResult{Kind: protocol.KindObserve, Problems: []protocol.Problem{{Rule: "FAKE-01", Message: fmt.Sprintf("unknown operation %q", p.Op), Path: "$.do"}}}, nil
	}
	r := protocol.ValidateResult{Kind: o.Kind, Refs: refs(p.Args)}
	for _, msg := range schema.Check(o.ArgsSchema, p.Args, schema.AllowRefs) {
		r.Problems = append(r.Problems, protocol.Problem{Rule: "FAKE-02", Message: msg})
	}
	return r, nil
}

func refs(v any) []string {
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			for _, m := range refRE.FindAllStringSubmatch(x, -1) {
				seen[m[1]] = true
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (f *fake) Prove(ctx context.Context) (protocol.ProveResult, error) {
	return protocol.ProveResult{Proofs: []protocol.Proof{{Name: "fake", Status: protocol.ProofUnproven, Detail: "the fake driver proves nothing"}}}, nil
}

func (f *fake) Ready(ctx context.Context) (protocol.ReadyResult, error) {
	return protocol.ReadyResult{Ready: true, Detail: "always ready"}, nil
}

func (f *fake) Call(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	start := time.Now()
	switch p.Op {
	case "echo":
		return protocol.CallResult{Fact: p.Args, Evidence: protocol.Evidence{Summary: "echo", DurationMS: time.Since(start).Milliseconds()}}, nil
	case "fail":
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnreachable, "the fake driver always fails")
	case "sleep":
		var a struct {
			MS json.Number `json:"ms"`
		}
		if err := protocol.Decode(p.Args, &a); err != nil {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "sleep: %v", err)
		}
		ms, err := a.MS.Int64()
		if err != nil {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "sleep: ms must be an integer")
		}
		select {
		case <-time.After(time.Duration(ms) * time.Millisecond):
		case <-ctx.Done():
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseTimeout, "sleep: the deadline passed")
		}
		return protocol.CallResult{Fact: map[string]any{"slept_ms": ms}, Evidence: protocol.Evidence{Summary: fmt.Sprintf("slept %d ms", ms), DurationMS: time.Since(start).Milliseconds()}}, nil
	case "open":
		var a struct {
			Refuse    bool `json:"refuse"`
			FailClose bool `json:"fail_close"`
		}
		if err := protocol.Decode(p.Args, &a); err != nil {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "open: %v", err)
		}
		if a.Refuse {
			fmt.Fprintln(os.Stderr, "open refused, no handle returned")
			return protocol.CallResult{Fact: map[string]any{"opened": false}, Evidence: protocol.Evidence{Summary: "open refused", DurationMS: time.Since(start).Milliseconds()}}, nil
		}
		f.next++
		id := fmt.Sprintf("h%d", f.next)
		f.conns[id] = &conn{failClose: a.FailClose}
		fmt.Fprintf(os.Stderr, "opened handle %s\n", id)
		return protocol.CallResult{Fact: map[string]any{"opened": true, "handle": id}, Handle: id, Evidence: protocol.Evidence{Summary: "opened " + id, DurationMS: time.Since(start).Milliseconds()}}, nil
	case "poke":
		c, ok := f.conns[p.Handle]
		if !ok {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "poke: unknown handle %q", p.Handle)
		}
		c.pokes++
		fmt.Fprintf(os.Stderr, "poked handle %s, count %d\n", p.Handle, c.pokes)
		return protocol.CallResult{Fact: map[string]any{"handle": p.Handle, "count": c.pokes}, Evidence: protocol.Evidence{Summary: fmt.Sprintf("poked %s", p.Handle), DurationMS: time.Since(start).Milliseconds()}}, nil
	case "die":
		fmt.Fprintf(os.Stderr, "dying with %d open handles\n", len(f.conns))
		os.Exit(3)
	}
	return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "unknown operation %q", p.Op)
}

func (f *fake) Close(ctx context.Context, p protocol.CloseParams) error {
	c, ok := f.conns[p.Handle]
	if !ok {
		fmt.Fprintf(os.Stderr, "close of unknown handle %q\n", p.Handle)
		return protocol.Errorf(protocol.CauseInvalidRequest, "close: unknown handle %q", p.Handle)
	}
	delete(f.conns, p.Handle)
	if c.failClose {
		fmt.Fprintf(os.Stderr, "close of handle %s failed on purpose after %d pokes\n", p.Handle, c.pokes)
		return protocol.Errorf(protocol.CauseUnreachable, "close: handle %s failed to close", p.Handle)
	}
	fmt.Fprintf(os.Stderr, "closed handle %s after %d pokes\n", p.Handle, c.pokes)
	return nil
}

func main() {
	if err := protocol.Serve(&fake{conns: map[string]*conn{}}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
