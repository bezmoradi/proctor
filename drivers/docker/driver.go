package docker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bezmoradi/proctor/protocol"
)

var Version = "0.0.0-dev"

const (
	Name         = "docker"
	OpContainer  = "container"
	OpContainers = "containers"
	OpNetwork    = "network"
	OpNetworks   = "networks"
	OpVolume     = "volume"
	OpVolumes    = "volumes"
	ReadyAttempt = 5 * time.Second
)

var operations = []string{OpContainer, OpContainers, OpNetwork, OpNetworks, OpVolume, OpVolumes}

var (
	text       = map[string]any{"type": "string"}
	textOrNull = map[string]any{"type": []string{"string", "null"}}
	flag       = map[string]any{"type": "boolean"}
	whole      = map[string]any{"type": "integer"}
	count      = map[string]any{"type": "integer", "minimum": 0}
	textList   = map[string]any{"type": "array", "items": text}
	pairList   = listOf(object(map[string]any{"key": text, "value": text}))
)

func object(props map[string]any) map[string]any {
	required := make([]string, 0, len(props))
	for _, k := range sortedKeys(props) {
		required = append(required, k)
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func nullable(o map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range o {
		out[k] = v
	}
	out["type"] = []string{"object", "null"}
	return out
}

func listOf(items any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

var refSchema = object(map[string]any{"name": textOrNull, "ipv4_address": textOrNull, "in_scope": flag})

var containerSchema = object(map[string]any{
	"id":        text,
	"name":      text,
	"image":     text,
	"image_ref": text,
	"state": object(map[string]any{
		"status": text, "running": flag, "paused": flag, "restarting": flag, "dead": flag,
		"oom_killed": flag, "exit_code": whole, "health": textOrNull, "restart_count": whole,
	}),
	"user":              text,
	"labels":            pairList,
	"readonly_rootfs":   flag,
	"privileged":        flag,
	"init":              flag,
	"cap_add":           textList,
	"cap_drop":          textList,
	"security_opt":      textList,
	"apparmor_profile":  text,
	"runtime":           text,
	"memory":            whole,
	"memory_swap":       whole,
	"nano_cpus":         whole,
	"pids_limit":        whole,
	"oom_score_adj":     whole,
	"oom_kill_disable":  flag,
	"network_mode":      text,
	"ipc_mode":          text,
	"pid_mode":          text,
	"uts_mode":          text,
	"userns_mode":       text,
	"cgroupns_mode":     text,
	"shm_size":          whole,
	"group_add":         textList,
	"devices":           count,
	"device_requests":   count,
	"sysctls":           pairList,
	"dns":               textList,
	"dns_options":       textList,
	"dns_search":        textList,
	"extra_hosts":       textList,
	"ports":             listOf(object(map[string]any{"container_port": text, "host_ip": text, "host_port": text})),
	"publish_all_ports": flag,
	"restart_policy":    object(map[string]any{"name": text, "maximum_retry_count": whole}),
	"log_driver":        text,
	"networks":          listOf(refSchema),
	"mounts": listOf(object(map[string]any{
		"type": text, "destination": text, "read_only": flag, "volume": textOrNull, "subpath": textOrNull, "source": textOrNull,
	})),
	"tmpfs":         textList,
	"docker_socket": flag,
	"execs":         count,
})

var networkSchema = object(map[string]any{
	"id":          text,
	"name":        text,
	"driver":      text,
	"scope":       text,
	"internal":    flag,
	"attachable":  flag,
	"enable_ipv6": flag,
	"labels":      pairList,
	"options":     pairList,
	"ipam":        listOf(object(map[string]any{"subnet": text, "gateway": text})),
	"members":     listOf(refSchema),
})

var volumeSchema = object(map[string]any{
	"name":        text,
	"driver":      text,
	"scope":       text,
	"labels":      pairList,
	"type_option": text,
	"option_keys": textList,
})

var nameArgs = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"name": text},
	"required":             []string{"name"},
	"additionalProperties": false,
}

var listArgs = map[string]any{
	"type": []string{"object", "null"},
	"properties": map[string]any{
		"labels":     map[string]any{"type": "object", "additionalProperties": text},
		"has_labels": textList,
	},
	"additionalProperties": false,
}

type kind struct {
	single, plural string
	schema         map[string]any
}

var (
	kindContainer = &kind{single: "container", plural: "containers", schema: containerSchema}
	kindNetwork   = &kind{single: "network", plural: "networks", schema: networkSchema}
	kindVolume    = &kind{single: "volume", plural: "volumes", schema: volumeSchema}
)

type opInfo struct {
	kind  *kind
	list  bool
	args  map[string]any
	facts map[string]any
}

var ops = map[string]opInfo{
	OpContainer:  single(kindContainer),
	OpContainers: plural(kindContainer),
	OpNetwork:    single(kindNetwork),
	OpNetworks:   plural(kindNetwork),
	OpVolume:     single(kindVolume),
	OpVolumes:    plural(kindVolume),
}

func single(k *kind) opInfo {
	return opInfo{kind: k, args: nameArgs, facts: object(map[string]any{"found": flag, k.single: nullable(k.schema)})}
}

func plural(k *kind) opInfo {
	return opInfo{kind: k, list: true, args: listArgs, facts: object(map[string]any{"count": count, k.plural: listOf(k.schema)})}
}

type Driver struct {
	scope  []scopeEntry
	socket string
	c      *client
}

func New() *Driver {
	return &Driver{}
}

func (d *Driver) Hello(ctx context.Context, p protocol.HelloParams) (protocol.HelloResult, error) {
	out := make([]protocol.Operation, 0, len(operations))
	for _, name := range operations {
		o := ops[name]
		out = append(out, protocol.Operation{
			Name:       name,
			Kind:       protocol.KindObserve,
			ArgsSchema: o.args,
			FactSchema: o.facts,
			Credential: protocol.CredentialNone,
		})
	}
	return protocol.HelloResult{
		Name:             Name,
		Version:          Version,
		Protocol:         protocol.Version,
		Operations:       out,
		PolicySchema:     true,
		ConnectionSchema: true,
	}, nil
}

func (d *Driver) Configure(ctx context.Context, p protocol.ConfigureParams) error {
	scope, err := parseScope(p.Policy)
	if err != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "%v", err)
	}
	d.scope = scope
	if p.Connection == nil {
		return nil
	}
	socket, err := parseConnection(p.Connection, p.Secrets)
	if err != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "%v", err)
	}
	d.socket = socket
	d.c = newClient(socket)
	return nil
}

func (d *Driver) Prove(ctx context.Context) (protocol.ProveResult, error) {
	return protocol.ProveResult{Proofs: []protocol.Proof{{
		Name:   "read_only",
		Status: protocol.ProofUnproven,
		Detail: "Docker has no read-only socket: whoever can open " + bindingSocket + " controls Docker and the machine, so the driver can't prove it can't change anything. " +
			"The driver itself sends only GET requests, only to /_ping, /version, /containers/json, /containers/<id>/json, /networks, /networks/<id> and /volumes (DKR-05), " +
			"and returns only objects that carry a label of its scope (DKR-06), never their environment or command (DKR-07); Docker enforces none of this.",
	}}}, nil
}

type versionAnswer struct {
	Version       string `json:"Version"`
	APIVersion    string `json:"ApiVersion"`
	MinAPIVersion string `json:"MinAPIVersion"`
}

func (d *Driver) Ready(ctx context.Context) (protocol.ReadyResult, error) {
	if d.c == nil {
		return protocol.ReadyResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the docker driver has no connection")
	}
	fi, err := os.Stat(d.socket)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return protocol.ReadyResult{Detail: bindingSocket + " doesn't exist yet; is Docker running?"}, nil
	case errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EACCES):
		return protocol.ReadyResult{Detail: "this user can't open " + bindingSocket + "; on Linux add it to the docker group", Lasting: true}, nil
	case err != nil:
		return protocol.ReadyResult{Detail: bindingSocket + " can't be read: " + statProblem(err)}, nil
	case fi.Mode()&fs.ModeSocket == 0:
		return protocol.ReadyResult{Detail: bindingSocket + " isn't a Unix socket", Lasting: true}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, ReadyAttempt)
	defer cancel()
	var pong string
	if err := d.c.get(ctx, pathPing, nil, &pong); err != nil {
		return readyFailure(err), nil
	}
	if strings.TrimSpace(pong) != "OK" {
		return protocol.ReadyResult{Detail: "GET /_ping on " + bindingSocket + " didn't answer OK", Lasting: true}, nil
	}
	var v versionAnswer
	if err := d.c.get(ctx, pathVersion, nil, &v); err != nil {
		return readyFailure(err), nil
	}
	lo, okLo := apiNumber(v.MinAPIVersion)
	hi, okHi := apiNumber(v.APIVersion)
	want, _ := apiNumber(APIVersion)
	if !okLo || !okHi || lo > want || want > hi {
		return protocol.ReadyResult{Detail: fmt.Sprintf("Docker answers API %s to %s; the docker driver speaks %s", oneLine(v.MinAPIVersion), oneLine(v.APIVersion), APIVersion), Lasting: true}, nil
	}
	return protocol.ReadyResult{Ready: true, Detail: fmt.Sprintf("Docker %s answers API %s on %s; the driver uses API %s and sees objects labelled %s",
		oneLine(v.Version), oneLine(v.APIVersion), bindingSocket, APIVersion, describeScope(d.scope))}, nil
}

func statProblem(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return oneLine(pe.Err.Error())
	}
	return "it can't be examined"
}

func readyFailure(err error) protocol.ReadyResult {
	var e *protocol.Error
	if errors.As(err, &e) {
		return protocol.ReadyResult{Detail: e.Message, Lasting: e.Data.Lasting}
	}
	return protocol.ReadyResult{Detail: err.Error()}
}

func apiNumber(s string) (int, bool) {
	major, minor, ok := strings.Cut(s, ".")
	if !ok {
		return 0, false
	}
	a, err1 := strconv.Atoi(major)
	b, err2 := strconv.Atoi(minor)
	if err1 != nil || err2 != nil || a < 0 || b < 0 || b >= 1000 {
		return 0, false
	}
	return a*1000 + b, true
}

func (d *Driver) Close(ctx context.Context, p protocol.CloseParams) error {
	return protocol.Errorf(protocol.CauseInvalidRequest, "the docker driver opens no handles")
}
