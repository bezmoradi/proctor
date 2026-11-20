package sqs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	"proctor/protocol"
	"proctor/protocol/schema"
)

var Version = "0.0.0-dev"

const (
	Name          = "sqs"
	OpSend        = "send"
	OpPeek        = "peek"
	OpDepth       = "depth"
	secretAccess  = "access_key"
	secretSecret  = "secret_key"
	fifoSuffix    = ".fifo"
	maxQueueName  = 80
	callTimeout   = 10 * time.Second
	readyAttempt  = 3 * time.Second
	maxPeek       = 1000
	maxReceives   = 150
	quietReceives = 3
	quietBehind   = 20
	receiveBatch  = 10
	receiveMargin = 2 * time.Second
	maxDelay      = 900
	excerptBytes  = 4 << 10
	maxCABundleSz = 1 << 20
	holdRule      = "SQS-03"
	redriveRule   = "SQS-04"
	maxHold       = 30
	releaseMargin = 2 * time.Second
	releaseGrace  = 500 * time.Millisecond
	releaseBatch  = 10
)

var (
	aliasRE     = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	queueNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	regionRE    = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	refRE       = regexp.MustCompile(`\$\{([^}]*)\}`)
)

var stringMap = map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}

var sendArgs = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"queue":         map[string]any{"type": "string"},
		"body":          true,
		"attributes":    stringMap,
		"group_id":      map[string]any{"type": "string"},
		"dedup_id":      map[string]any{"type": "string"},
		"delay_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": maxDelay},
	},
	"required":             []string{"queue", "body"},
	"additionalProperties": false,
}

var sendFact = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"message_id": map[string]any{"type": "string"}},
	"required":             []string{"message_id"},
	"additionalProperties": false,
}

var peekArgs = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"queue": map[string]any{"type": "string"},
		"max":   map[string]any{"type": "integer"},
	},
	"required":             []string{"queue"},
	"additionalProperties": false,
}

var peekFact = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"count":     map[string]any{"type": "integer", "minimum": 0, "maximum": maxPeek},
		"truncated": map[string]any{"type": "boolean"},
		"messages": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message_id": map[string]any{"type": "string"},
					"body":       true,
					"body_text":  map[string]any{"type": "string"},
					"attributes": stringMap,
				},
				"required":             []string{"message_id", "body_text", "attributes"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"count", "messages", "truncated"},
	"additionalProperties": false,
}

var depthArgs = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"queue": map[string]any{"type": "string"}},
	"required":             []string{"queue"},
	"additionalProperties": false,
}

var counter = map[string]any{"type": "integer", "minimum": 0}

var depthFact = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"visible":   counter,
		"in_flight": counter,
		"delayed":   counter,
	},
	"required":             []string{"visible", "in_flight", "delayed"},
	"additionalProperties": false,
}

var PolicySchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"queues": map[string]any{
			"type":                 "object",
			"additionalProperties": map[string]any{"type": "string"},
		},
		"peek_hold": map[string]any{
			"type":  "array",
			"items": map[string]any{"type": "string"},
		},
	},
	"required":             []string{"queues"},
	"additionalProperties": false,
}

var ConnectionSchema = map[string]any{
	"type": []string{"object", "null"},
	"properties": map[string]any{
		"endpoint":  map[string]any{"type": "string", "pattern": `^https?://`},
		"region":    map[string]any{"type": "string"},
		"ca_bundle": map[string]any{"type": "string"},
	},
	"required":             []string{"endpoint", "region"},
	"additionalProperties": false,
}

type policy struct {
	Queues   map[string]string `json:"queues"`
	PeekHold []string          `json:"peek_hold"`
}

type connection struct {
	Endpoint string `json:"endpoint"`
	Region   string `json:"region"`
	CABundle string `json:"ca_bundle"`
}

type queue struct {
	alias string
	name  string
	url   string
	hold  bool
}

func (q *queue) fifo() bool {
	return strings.HasSuffix(q.name, fifoSuffix)
}

func (q *queue) label() string {
	return q.alias + " (" + q.name + ")"
}

type Driver struct {
	aliases  []string
	queues   map[string]*queue
	client   *awssqs.Client
	http     *http.Client
	endpoint string
	secrets  []string
}

func New() *Driver {
	return &Driver{queues: map[string]*queue{}}
}

func (d *Driver) Hello(ctx context.Context, p protocol.HelloParams) (protocol.HelloResult, error) {
	return protocol.HelloResult{
		Name:     Name,
		Version:  Version,
		Protocol: protocol.Version,
		Operations: []protocol.Operation{
			{Name: OpSend, Kind: protocol.KindAct, ArgsSchema: sendArgs, FactSchema: sendFact, Credential: protocol.CredentialNone, Data: []string{"$.body", "$.attributes"}},
			{Name: OpPeek, Kind: protocol.KindObserve, ArgsSchema: peekArgs, FactSchema: peekFact, Credential: protocol.CredentialNone},
			{Name: OpDepth, Kind: protocol.KindObserve, ArgsSchema: depthArgs, FactSchema: depthFact, Credential: protocol.CredentialNone},
		},
		PolicySchema:     PolicySchema,
		ConnectionSchema: ConnectionSchema,
	}, nil
}

func (d *Driver) Configure(ctx context.Context, p protocol.ConfigureParams) error {
	if msgs := schema.Check(PolicySchema, p.Policy); len(msgs) > 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "policy: %s", strings.Join(msgs, "; "))
	}
	var pol policy
	if err := protocol.Decode(p.Policy, &pol); err != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "policy: %v", err)
	}
	if len(pol.Queues) == 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "policy: queues must name at least one queue, as <alias>: <queue name>")
	}
	aliases := make([]string, 0, len(pol.Queues))
	for alias := range pol.Queues {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	var msgs []string
	queues := map[string]*queue{}
	for _, alias := range aliases {
		name := pol.Queues[alias]
		if !aliasRE.MatchString(alias) {
			msgs = append(msgs, fmt.Sprintf("queue alias %q must match %s", alias, aliasRE))
		}
		if err := checkQueueName(name); err != nil {
			msgs = append(msgs, fmt.Sprintf("queues.%s: %v", alias, err))
		}
		queues[alias] = &queue{alias: alias, name: name}
	}
	if len(msgs) > 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "policy: %s", strings.Join(msgs, "; "))
	}
	if msgs := holdQueues(pol.PeekHold, queues); len(msgs) > 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "policy: %s (%s)", strings.Join(msgs, "; "), holdRule)
	}
	if p.Connection != nil {
		if err := d.connect(p.Connection, p.Secrets); err != nil {
			return err
		}
	} else if len(p.Secrets) > 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "binding: secrets were given without a connection")
	}
	d.aliases, d.queues = aliases, queues
	return nil
}

func holdQueues(list []string, queues map[string]*queue) []string {
	if list == nil {
		return nil
	}
	if len(list) == 0 {
		return []string{"peek_hold is empty; list the queue aliases whose peeks may hold messages, or remove peek_hold"}
	}
	var msgs []string
	seen := map[string]bool{}
	for i, alias := range list {
		q := queues[alias]
		switch {
		case seen[alias]:
			msgs = append(msgs, fmt.Sprintf("peek_hold[%d]: queue %q is listed twice", i, alias))
		case q == nil:
			msgs = append(msgs, fmt.Sprintf("peek_hold[%d]: %q is not a queue alias declared in queues", i, alias))
		case q.fifo():
			msgs = append(msgs, fmt.Sprintf("peek_hold[%d]: queue %s is a FIFO queue; SQS returns no more of a message group while one of its messages is held, so a hold can't read past it and would stop the group's consumers", i, q.label()))
		default:
			q.hold = true
		}
		seen[alias] = true
	}
	return msgs
}

func checkQueueName(name string) error {
	base := strings.TrimSuffix(name, fifoSuffix)
	switch {
	case name == "":
		return errors.New("the queue name is empty")
	case len(name) > maxQueueName:
		return fmt.Errorf("queue name %q is longer than %d characters", name, maxQueueName)
	case base == "" || !queueNameRE.MatchString(base):
		return fmt.Errorf("queue name %q may hold only letters, digits, - and _, with an optional %s suffix", name, fifoSuffix)
	}
	return nil
}

func (d *Driver) connect(raw any, secrets protocol.Secrets) error {
	var c connection
	if err := protocol.Decode(raw, &c); err != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: %v", err)
	}
	var msgs []string
	bad := func(format string, args ...any) { msgs = append(msgs, fmt.Sprintf(format, args...)) }
	u, err := url.Parse(c.Endpoint)
	switch {
	case c.Endpoint == "":
		bad("connection: endpoint is required; the sqs driver never falls back to AWS's own endpoints")
	case err != nil:
		bad("connection: endpoint: %v", err)
	case u.Scheme != "http" && u.Scheme != "https":
		bad("connection: endpoint must be an http or https URL")
	case u.Host == "":
		bad("connection: endpoint has no host")
	case u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || (u.Path != "" && u.Path != "/"):
		bad("connection: endpoint must be scheme://host[:port] with no credentials, path, query or fragment")
	}
	if !regionRE.MatchString(c.Region) {
		bad("connection: region must be a region name such as us-east-2")
	}
	var keys []string
	for k := range secrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k != secretAccess && k != secretSecret {
			bad("binding: unexpected secret %s; the sqs driver takes only %s_env and %s_env", k, secretAccess, secretSecret)
		}
	}
	access, secret := secrets[secretAccess], secrets[secretSecret]
	if access == "" {
		bad("binding: %s is missing; set %s_env to an environment variable that holds it", secretAccess, secretAccess)
	}
	if secret == "" {
		bad("binding: %s is missing; set %s_env to an environment variable that holds it", secretSecret, secretSecret)
	}
	var roots *x509.CertPool
	if c.CABundle != "" {
		if u != nil && u.Scheme != "https" {
			bad("connection: ca_bundle applies only to an https endpoint")
		} else if roots, err = loadBundle(c.CABundle); err != nil {
			bad("connection: ca_bundle: %v", err)
		}
	}
	if len(msgs) > 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "%s", strings.Join(msgs, "; "))
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	d.http = &http.Client{Transport: transport}
	d.endpoint = u.Scheme + "://" + u.Host
	d.secrets = []string{access, secret}
	d.client = awssqs.New(awssqs.Options{
		Region:       c.Region,
		BaseEndpoint: aws.String(d.endpoint),
		Credentials:  aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(access, secret, "")),
		HTTPClient:   d.http,
		Retryer:      aws.NopRetryer{},
	})
	return nil
}

func loadBundle(path string) (*x509.CertPool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s isn't a regular file", path)
	}
	if info.Size() > maxCABundleSz {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxCABundleSz)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("%s holds no PEM certificate", path)
	}
	return pool, nil
}

func (d *Driver) Validate(ctx context.Context, p protocol.ValidateParams) (protocol.ValidateResult, error) {
	r := protocol.ValidateResult{Kind: protocol.KindObserve}
	add := func(rule, path, format string, args ...any) {
		r.Problems = append(r.Problems, protocol.Problem{Rule: rule, Message: fmt.Sprintf(format, args...), Path: path})
	}
	switch p.Op {
	case OpSend:
		r.Kind = protocol.KindAct
	case OpPeek, OpDepth:
	default:
		add("EXE-01", "$.do", "the sqs driver has no operation %q; it has %s, %s and %s", p.Op, OpSend, OpPeek, OpDepth)
		return r, nil
	}
	r.Refs = refs(p.Args)
	m, _ := p.Args.(map[string]any)
	if raw, ok := m["queue"]; ok {
		alias, isText := raw.(string)
		switch {
		case !isText:
		case strings.Contains(alias, "${"):
			add("SQS-01", "$.with.queue", "queue must name a queue alias from the driver's policy as plain text, not %q", alias)
		case d.queues[alias] == nil:
			add("SQS-01", "$.with.queue", "queue %q is not declared in the sqs driver's policy in proctor.yaml (declared: %s)", alias, strings.Join(d.aliases, ", "))
		default:
			r.Targets = []string{alias}
		}
	}
	if p.Op == OpPeek {
		if n, ok := m["max"].(json.Number); ok {
			if i, err := strconv.ParseInt(n.String(), 10, 64); err != nil || i < 1 || i > maxPeek {
				add("SQS-02", "$.with.max", "max must be a whole number from 1 to %d, not %s", maxPeek, n)
			}
		}
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
			for k, e := range x {
				walk(k)
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

func (d *Driver) Prove(ctx context.Context) (protocol.ProveResult, error) {
	return protocol.ProveResult{Proofs: []protocol.Proof{}}, nil
}

func (d *Driver) Ready(ctx context.Context) (protocol.ReadyResult, error) {
	if d.client == nil {
		return protocol.ReadyResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the sqs driver has no connection")
	}
	for _, alias := range d.aliases {
		q := d.queues[alias]
		actx, cancel := context.WithTimeout(ctx, readyAttempt)
		err := d.resolve(actx, q)
		cancel()
		if err != nil {
			var e *protocol.Error
			if !errors.As(err, &e) {
				return protocol.ReadyResult{Detail: fmt.Sprintf("queue %s: %v", q.label(), d.scrub(err.Error()))}, nil
			}
			return protocol.ReadyResult{Detail: e.Message, Lasting: e.Data.Lasting}, nil
		}
	}
	return protocol.ReadyResult{Ready: true, Detail: fmt.Sprintf("queues ready: %s", strings.Join(d.aliases, ", "))}, nil
}

func (d *Driver) resolve(ctx context.Context, q *queue) error {
	if q.url != "" {
		return nil
	}
	out, err := d.client.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{QueueName: aws.String(q.name)})
	if err != nil {
		return d.classify(ctx, err, "queue "+q.label()+": resolving its URL")
	}
	if aws.ToString(out.QueueUrl) == "" {
		return protocol.Errorf(protocol.CauseUnreadableResponse, "queue %s: GetQueueUrl returned no URL", q.label())
	}
	q.url = aws.ToString(out.QueueUrl)
	return nil
}

func (d *Driver) Close(ctx context.Context, p protocol.CloseParams) error {
	return protocol.Errorf(protocol.CauseInvalidRequest, "the sqs driver opens no handles")
}

func (d *Driver) Shutdown() {
	if d.http != nil {
		d.http.CloseIdleConnections()
	}
}

func (d *Driver) scrub(s string) string {
	for _, secret := range d.secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	return s
}
