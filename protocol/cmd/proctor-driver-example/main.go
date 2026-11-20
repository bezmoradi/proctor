package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"github.com/bezmoradi/proctor/protocol"
	"github.com/bezmoradi/proctor/protocol/schema"
)

const Version = "0.1.0"

const (
	opRead   = "read"
	maxBytes = 1 << 20
)

var operations = []protocol.Operation{{
	Name: opRead,
	Kind: protocol.KindObserve,
	ArgsSchema: map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"path": map[string]any{"type": "string"}},
		"required":             []string{"path"},
		"additionalProperties": false,
	},
	FactSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"exists": map[string]any{"type": "boolean"},
			"text":   map[string]any{"type": "string"},
		},
	},
	Credential: protocol.CredentialNone,
}}

type policy struct {
	Paths []string `json:"paths"`
}

type connection struct {
	Root string `json:"root"`
}

type example struct {
	policy policy
	root   string
}

func (e *example) Hello(ctx context.Context, p protocol.HelloParams) (protocol.HelloResult, error) {
	return protocol.HelloResult{
		Name:       "example",
		Version:    Version,
		Protocol:   protocol.Version,
		Operations: operations,
		PolicySchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
			"required":             []string{"paths"},
			"additionalProperties": false,
		},
		ConnectionSchema: map[string]any{
			"type":                 []string{"object", "null"},
			"properties":           map[string]any{"root": map[string]any{"type": "string"}},
			"required":             []string{"root"},
			"additionalProperties": false,
		},
	}, nil
}

func (e *example) Configure(ctx context.Context, p protocol.ConfigureParams) error {
	if err := protocol.Decode(p.Policy, &e.policy); err != nil {
		return protocol.Errorf(protocol.CauseInvalidRequest, "policy: %v", err)
	}
	if len(e.policy.Paths) == 0 {
		return protocol.Errorf(protocol.CauseInvalidRequest, "policy: paths must list at least one folder the steps may read under")
	}
	for _, prefix := range e.policy.Paths {
		if !clean(prefix) {
			return protocol.Errorf(protocol.CauseInvalidRequest, "policy: paths: %q isn't a clean relative path", prefix)
		}
	}
	if p.Connection == nil {
		return nil
	}
	var c connection
	if err := protocol.Decode(p.Connection, &c); err != nil {
		return protocol.Errorf(protocol.CauseInvalidRequest, "connection: %v", err)
	}
	if c.Root == "" {
		return protocol.Errorf(protocol.CauseInvalidRequest, "connection: root must name a folder")
	}
	e.root = c.Root
	return nil
}

func (e *example) PolicyEntries() []protocol.PolicyEntry {
	out := make([]protocol.PolicyEntry, 0, len(e.policy.Paths))
	for _, p := range e.policy.Paths {
		out = append(out, protocol.PolicyEntry{Key: "paths: " + p, Rule: "EXAMPLE-02", Use: "a read of a file under " + p})
	}
	return out
}

func clean(p string) bool {
	return p != "" && path.Clean(p) == p && fs.ValidPath(p)
}

func (e *example) allowed(p string) (string, bool) {
	for _, prefix := range e.policy.Paths {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return "paths: " + prefix, true
		}
	}
	return "", false
}

func (e *example) Validate(ctx context.Context, p protocol.ValidateParams) (protocol.ValidateResult, error) {
	r := protocol.ValidateResult{Kind: protocol.KindObserve}
	if p.Op != opRead {
		r.Problems = append(r.Problems, protocol.Problem{Rule: "EXAMPLE-01", Message: fmt.Sprintf("unknown operation %q", p.Op), Path: "$.do"})
		return r, nil
	}
	for _, msg := range schema.Check(operations[0].ArgsSchema, p.Args) {
		r.Problems = append(r.Problems, protocol.Problem{Rule: "EXAMPLE-01", Message: msg})
	}
	var a struct {
		Path string `json:"path"`
	}
	if len(r.Problems) > 0 || protocol.Decode(p.Args, &a) != nil {
		return r, nil
	}
	key, ok := e.allowed(a.Path)
	switch {
	case !clean(a.Path):
		r.Problems = append(r.Problems, protocol.Problem{Rule: "EXAMPLE-01", Message: fmt.Sprintf("%q isn't a clean relative path", a.Path), Path: "$.path"})
	case !ok:
		r.Problems = append(r.Problems, protocol.Problem{Rule: "EXAMPLE-02", Message: fmt.Sprintf("%q is under no entry of policy.paths", a.Path), Path: "$.path"})
	default:
		r.Uses = []string{key}
		r.Targets = []string{"file/" + a.Path}
	}
	return r, nil
}

func (e *example) Prove(ctx context.Context) (protocol.ProveResult, error) {
	return protocol.ProveResult{Proofs: []protocol.Proof{{Name: "example", Status: protocol.ProofUnproven, Detail: "the example driver proves nothing"}}}, nil
}

func (e *example) Ready(ctx context.Context) (protocol.ReadyResult, error) {
	fi, err := os.Stat(e.root)
	if err != nil || !fi.IsDir() {
		return protocol.ReadyResult{Ready: false, Detail: fmt.Sprintf("root %s isn't a folder", e.root)}, nil
	}
	return protocol.ReadyResult{Ready: true, Detail: "root " + e.root + " is a folder"}, nil
}

func (e *example) Call(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	start := time.Now()
	var a struct {
		Path string `json:"path"`
	}
	if p.Op != opRead {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "unknown operation %q", p.Op)
	}
	if err := protocol.Decode(p.Args, &a); err != nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "read: %v", err)
	}
	if _, ok := e.allowed(a.Path); !ok || !clean(a.Path) {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "read: %q is under no entry of policy.paths", a.Path)
	}
	root, err := os.OpenRoot(e.root)
	if err != nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnreachable, "read: %v", err)
	}
	defer root.Close()
	fact := map[string]any{"exists": false, "text": ""}
	f, err := root.Open(a.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnreachable, "read %s: %v", a.Path, err)
	default:
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
		if err != nil {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnreachable, "read %s: %v", a.Path, err)
		}
		if len(data) > maxBytes {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseResponseTooLarge, "read %s: larger than %d bytes", a.Path, maxBytes)
		}
		fact = map[string]any{"exists": true, "text": string(data)}
	}
	return protocol.CallResult{Fact: fact, Evidence: protocol.Evidence{Summary: "read file/" + a.Path, DurationMS: time.Since(start).Milliseconds()}}, nil
}

func (e *example) Close(ctx context.Context, p protocol.CloseParams) error {
	return protocol.Errorf(protocol.CauseInvalidRequest, "the example driver opens no handles, so it has no %q to close", p.Handle)
}

func main() {
	if err := protocol.Serve(&example{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
