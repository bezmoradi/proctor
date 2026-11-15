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
}

type fake struct{}

func (fake) Hello(ctx context.Context, p protocol.HelloParams) (protocol.HelloResult, error) {
	return protocol.HelloResult{
		Name:             "fake",
		Version:          "0.0.0",
		Protocol:         protocol.Version,
		Operations:       operations,
		PolicySchema:     map[string]any{"type": []string{"object", "null"}, "additionalProperties": false},
		ConnectionSchema: map[string]any{"type": []string{"object", "null"}, "properties": map[string]any{"label": map[string]any{"type": "string"}}, "additionalProperties": false},
	}, nil
}

func (fake) Configure(ctx context.Context, p protocol.ConfigureParams) error {
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

func (fake) Validate(ctx context.Context, p protocol.ValidateParams) (protocol.ValidateResult, error) {
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

func (fake) Prove(ctx context.Context) (protocol.ProveResult, error) {
	return protocol.ProveResult{Proofs: []protocol.Proof{{Name: "fake", Status: protocol.ProofUnproven, Detail: "the fake driver proves nothing"}}}, nil
}

func (fake) Ready(ctx context.Context) (protocol.ReadyResult, error) {
	return protocol.ReadyResult{Ready: true, Detail: "always ready"}, nil
}

func (fake) Call(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
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
	}
	return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "unknown operation %q", p.Op)
}

func (fake) Close(ctx context.Context, p protocol.CloseParams) error {
	return protocol.Errorf(protocol.CauseInvalidRequest, "the fake driver opens no handles")
}

func main() {
	if err := protocol.Serve(fake{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
