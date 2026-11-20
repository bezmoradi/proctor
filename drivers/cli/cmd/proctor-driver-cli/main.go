package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/bezmoradi/proctor/protocol"
)

var Version = "0.0.0-dev"

const (
	driverName  = "cli"
	opRun       = "run"
	streamLimit = 1 << 20
	stdinLimit  = 1 << 20
	maxCommands = 100
	maxArgs     = 256
	maxExtra    = 64
	maxTargets  = 16
	maxEnv      = 64
)

var (
	text       = map[string]any{"type": "string"}
	flag       = map[string]any{"type": "boolean"}
	textList   = map[string]any{"type": "array", "items": text}
	commandRef = map[string]any{"type": "string", "pattern": `^[a-z][a-z0-9_-]{0,63}$`}
)

var argsSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"command": commandRef,
		"args":    textList,
		"stdin":   text,
	},
	"required":             []string{"command"},
	"additionalProperties": false,
}

var factSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"command":          text,
		"exit_code":        map[string]any{"type": "integer", "minimum": -1, "maximum": 255},
		"duration_ms":      map[string]any{"type": "integer", "minimum": 0},
		"timed_out":        flag,
		"stdout_text":      text,
		"stdout_json":      true,
		"stdout_truncated": flag,
		"stderr_text":      text,
		"stderr_truncated": flag,
		"overflow":         flag,
		"overflow_reason":  text,
	},
	"required":             []string{"command", "exit_code", "duration_ms", "timed_out", "stdout_text", "stdout_truncated", "stderr_text", "stderr_truncated", "overflow"},
	"additionalProperties": false,
}

var commandSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"program":     text,
		"args":        textList,
		"kind":        map[string]any{"type": "string", "enum": []string{protocol.KindAct, protocol.KindObserve}},
		"targets":     textList,
		"destructive": flag,
		"extra_args":  map[string]any{"type": "integer", "minimum": 0, "maximum": maxExtra},
		"dir":         text,
		"env":         textList,
		"timeout":     text,
	},
	"required":             []string{"program", "kind"},
	"additionalProperties": false,
}

var policySchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"commands": map[string]any{"type": "object", "additionalProperties": commandSchema},
	},
	"required":             []string{"commands"},
	"additionalProperties": false,
}

var connectionSchema = map[string]any{
	"type": []string{"object", "null"},
	"properties": map[string]any{
		"repository": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"root":            text,
				"protected_root":  text,
				"protected_paths": textList,
			},
			"required":             []string{"root", "protected_root"},
			"additionalProperties": false,
		},
	},
	"required":             []string{"repository"},
	"additionalProperties": false,
}

type driver struct {
	commands map[string]*command
	repo     *repository
	env      map[string]string
}

func (d *driver) Hello(ctx context.Context, p protocol.HelloParams) (protocol.HelloResult, error) {
	return protocol.HelloResult{
		Name:     driverName,
		Version:  Version,
		Protocol: protocol.Version,
		Operations: []protocol.Operation{{
			Name:       opRun,
			Kind:       protocol.KindByArgs,
			ArgsSchema: argsSchema,
			FactSchema: factSchema,
			Credential: protocol.CredentialNone,
			Data:       []string{"$.args", "$.stdin"},
		}},
		PolicySchema:     policySchema,
		ConnectionSchema: connectionSchema,
	}, nil
}

func (d *driver) Configure(ctx context.Context, p protocol.ConfigureParams) error {
	commands, err := parsePolicy(p.Policy)
	if err != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "%v", err)
	}
	d.commands = commands
	if p.Connection == nil {
		if len(p.Secrets) > 0 {
			return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: secrets were given without a connection (%s)", connectionRule)
		}
		return nil
	}
	repo, env, err := parseConnection(p.Connection, p.Secrets, commands)
	if err != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "%v", err)
	}
	var msgs []string
	for _, name := range sortedKeys(commands) {
		c := commands[name]
		if _, err := repo.resolve(c); err != nil {
			msgs = append(msgs, fmt.Sprintf("policy: commands.%s: %v", name, err))
			continue
		}
		if _, err := repo.workDir(c.Dir); err != nil {
			msgs = append(msgs, fmt.Sprintf("policy: commands.%s: dir: %v (%s)", name, err, policyRule))
		}
		if c.Kind == protocol.KindObserve {
			if err := repo.checkArgFiles(c.Args, c.Dir); err != nil {
				msgs = append(msgs, fmt.Sprintf("policy: commands.%s: %v", name, err))
			}
		}
	}
	if len(msgs) > 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "%s", strings.Join(msgs, "; "))
	}
	d.repo, d.env = repo, env
	return nil
}

func (d *driver) PolicyEntries() []protocol.PolicyEntry {
	out := make([]protocol.PolicyEntry, 0, len(d.commands))
	for _, name := range sortedKeys(d.commands) {
		out = append(out, protocol.PolicyEntry{Key: entryKey(name), Rule: policyRule, Use: fmt.Sprintf("a step that runs it, cli.run with command: %s", name)})
	}
	return out
}

func entryKey(name string) string {
	return "commands." + name
}

func (d *driver) Prove(ctx context.Context) (protocol.ProveResult, error) {
	observe := 0
	for _, c := range d.commands {
		if c.Kind == protocol.KindObserve {
			observe++
		}
	}
	return protocol.ProveResult{Proofs: []protocol.Proof{{
		Name:   "observe_programs",
		Status: protocol.ProofProven,
		Detail: fmt.Sprintf("each of the %d observe commands runs a program outside %s or under a protected folder, with no symbolic link inside the repository on its path; the driver checks it again before every run (%s)", observe, d.repo.root, locationRule),
	}, {
		Name:   "sandbox",
		Status: protocol.ProofUnproven,
		Detail: "a command runs as Proctor's user, with that user's files and network; the driver runs only declared programs, without a shell, in the repository with only PATH, HOME, TMPDIR and the variables the command lists, and kills its process group at the deadline, but it can't limit what the program does",
	}}}, nil
}

func (d *driver) Ready(ctx context.Context) (protocol.ReadyResult, error) {
	if d.repo == nil {
		return protocol.ReadyResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the cli driver has no connection")
	}
	for _, name := range sortedKeys(d.commands) {
		c := d.commands[name]
		if _, err := d.repo.resolve(c); err != nil {
			return protocol.ReadyResult{Detail: fmt.Sprintf("command %s: %v", name, err), Lasting: c.Kind == protocol.KindObserve}, nil
		}
	}
	return protocol.ReadyResult{Ready: true, Detail: fmt.Sprintf("%d commands resolve to programs; they run in %s", len(d.commands), d.repo.root)}, nil
}

func (d *driver) Close(ctx context.Context, p protocol.CloseParams) error {
	return protocol.Errorf(protocol.CauseInvalidRequest, "the cli driver opens no handles")
}

func main() {
	if err := protocol.Serve(&driver{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
