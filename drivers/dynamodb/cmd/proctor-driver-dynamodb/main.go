package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"proctor/protocol"
)

var Version = "0.0.0-dev"

const (
	driverName     = "dynamodb"
	opGet          = "get"
	opQuery        = "query"
	opPut          = "put"
	maxItems       = 1000
	defaultTimeout = 10 * time.Second
	readyAttempt   = 3 * time.Second
	excerptBytes   = 4 << 10
)

var (
	keySchema  = map[string]any{"type": "object"}
	itemSchema = map[string]any{"type": "object"}
)

var getArgs = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"table":      map[string]any{"type": "string"},
		"key":        keySchema,
		"consistent": map[string]any{"type": "boolean"},
	},
	"required":             []string{"table", "key"},
	"additionalProperties": false,
}

var getFact = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"found": map[string]any{"type": "boolean"},
		"item":  map[string]any{"type": []string{"object", "null"}},
	},
	"required":             []string{"found", "item"},
	"additionalProperties": false,
}

var queryArgs = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"table": map[string]any{"type": "string"},
		"index": map[string]any{"type": "string"},
		"key":   keySchema,
		"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": maxItems},
	},
	"required":             []string{"table", "key"},
	"additionalProperties": false,
}

var queryFact = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"count": map[string]any{"type": "integer", "minimum": 0},
		"items": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
	},
	"required":             []string{"count", "items"},
	"additionalProperties": false,
}

var putArgs = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"table": map[string]any{"type": "string"},
		"item":  itemSchema,
	},
	"required":             []string{"table", "item"},
	"additionalProperties": false,
}

var putFact = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"written": map[string]any{"type": "boolean"},
	},
	"required":             []string{"written"},
	"additionalProperties": false,
}

type driver struct {
	tables  map[string]string
	aliases []string
	client  *dynamodb.Client
	secrets []string
	keys    map[string]*tableKeys
}

func (d *driver) Hello(ctx context.Context, p protocol.HelloParams) (protocol.HelloResult, error) {
	return protocol.HelloResult{
		Name:     driverName,
		Version:  Version,
		Protocol: protocol.Version,
		Operations: []protocol.Operation{
			{Name: opGet, Kind: protocol.KindObserve, ArgsSchema: getArgs, FactSchema: getFact, Credential: protocol.CredentialNone},
			{Name: opQuery, Kind: protocol.KindObserve, ArgsSchema: queryArgs, FactSchema: queryFact, Credential: protocol.CredentialNone},
			{Name: opPut, Kind: protocol.KindAct, ArgsSchema: putArgs, FactSchema: putFact, Credential: protocol.CredentialNone, Data: []string{"$.item"}},
		},
		PolicySchema:     policySchema,
		ConnectionSchema: connectionSchema,
	}, nil
}

func (d *driver) Prove(ctx context.Context) (protocol.ProveResult, error) {
	return protocol.ProveResult{Proofs: []protocol.Proof{{
		Name:   "read_only",
		Status: protocol.ProofUnproven,
		Detail: "the DynamoDB driver can't prove the credential can't write",
	}}}, nil
}

func (d *driver) Ready(ctx context.Context) (protocol.ReadyResult, error) {
	if d.client == nil {
		return protocol.ReadyResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the dynamodb driver has no connection")
	}
	for _, alias := range d.aliases {
		actx, cancel := context.WithTimeout(ctx, readyAttempt)
		k, status, err := d.describe(actx, alias)
		if err != nil {
			e := d.classify(actx, err, fmt.Sprintf("table %s (%s)", alias, d.tables[alias]))
			cancel()
			return protocol.ReadyResult{Detail: e.Data.Cause + ": " + e.Message, Lasting: e.Data.Lasting}, nil
		}
		cancel()
		if status != "" {
			return protocol.ReadyResult{Detail: fmt.Sprintf("table %s (%s) is %s, not ACTIVE", alias, d.tables[alias], status)}, nil
		}
		d.keys[alias] = k
	}
	return protocol.ReadyResult{Ready: true, Detail: fmt.Sprintf("tables ACTIVE: %s", strings.Join(d.aliases, ", "))}, nil
}

func (d *driver) Close(ctx context.Context, p protocol.CloseParams) error {
	return protocol.Errorf(protocol.CauseInvalidRequest, "the dynamodb driver opens no handles")
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func main() {
	if err := protocol.Serve(&driver{keys: map[string]*tableKeys{}}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
