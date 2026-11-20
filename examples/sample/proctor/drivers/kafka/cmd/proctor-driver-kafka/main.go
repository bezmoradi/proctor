package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bezmoradi/proctor/protocol"
	"github.com/bezmoradi/proctor/protocol/schema"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

const Version = "0.1.0"

const (
	opEvents     = "events"
	maxScanned   = 10000
	maxReturned  = 1000
	maxValueText = 64 << 10
)

var (
	refRE     = regexp.MustCompile(`\$\{([^}]*)\}`)
	savedRE   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	valueRE   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*$`)
	aliasRE   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	topicRE   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,249}$`)
	whereKey  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
	text      = map[string]any{"type": "string"}
	count     = map[string]any{"type": "integer", "minimum": 0}
	scalar    = map[string]any{"type": []string{"string", "number", "boolean"}}
	textMap   = map[string]any{"type": "object", "additionalProperties": text}
	errScoped = "kafka.events reads every record the topic holds, other scenarios' and earlier runs' included; filter on a key, where value or value_contains that holds ${run.id} or a saved value"
)

func object(props map[string]any) map[string]any {
	required := make([]string, 0, len(props))
	for k := range props {
		required = append(required, k)
	}
	sort.Strings(required)
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

var argsSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"topic":          text,
		"key":            text,
		"where":          map[string]any{"type": "object", "additionalProperties": scalar},
		"value_contains": text,
	},
	"required":             []string{"topic"},
	"additionalProperties": false,
}

var recordSchema = object(map[string]any{
	"partition":  count,
	"offset":     count,
	"timestamp":  text,
	"key":        text,
	"headers":    textMap,
	"value_text": text,
	"value":      true,
})

var factSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"topic_exists":    map[string]any{"type": "boolean"},
		"scanned":         count,
		"count":           count,
		"records":         map[string]any{"type": "array", "items": recordSchema},
		"overflow":        map[string]any{"type": "boolean"},
		"overflow_reason": text,
	},
	"required":             []string{"count", "overflow", "records", "scanned", "topic_exists"},
	"additionalProperties": false,
}

var policySchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"topics": map[string]any{"type": "object", "additionalProperties": text},
	},
	"required":             []string{"topics"},
	"additionalProperties": false,
}

var connectionSchema = map[string]any{
	"type": []string{"object", "null"},
	"properties": map[string]any{
		"brokers": map[string]any{"type": "array", "items": text},
	},
	"required":             []string{"brokers"},
	"additionalProperties": false,
}

type args struct {
	Topic         string         `json:"topic"`
	Key           *string        `json:"key"`
	Where         map[string]any `json:"where"`
	ValueContains *string        `json:"value_contains"`
}

type kafkaDriver struct {
	topics  map[string]string
	brokers []string
	client  *kgo.Client
}

func (d *kafkaDriver) Hello(ctx context.Context, p protocol.HelloParams) (protocol.HelloResult, error) {
	return protocol.HelloResult{
		Name:     "kafka",
		Version:  Version,
		Protocol: protocol.Version,
		Operations: []protocol.Operation{{
			Name:        opEvents,
			Kind:        protocol.KindObserve,
			ArgsSchema:  argsSchema,
			FactSchema:  factSchema,
			Credential:  protocol.CredentialNone,
			Accumulates: true,
			Scan:        []string{"$.records"},
		}},
		PolicySchema:     policySchema,
		ConnectionSchema: connectionSchema,
	}, nil
}

func (d *kafkaDriver) Configure(ctx context.Context, p protocol.ConfigureParams) error {
	if len(p.Secrets) > 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: the kafka driver takes no secrets, so no key may end in _env")
	}
	if msgs := schema.Check(policySchema, p.Policy); len(msgs) > 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "policy: %s", strings.Join(msgs, "; "))
	}
	var pol struct {
		Topics map[string]string `json:"topics"`
	}
	if err := protocol.Decode(p.Policy, &pol); err != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "policy: %v", err)
	}
	if len(pol.Topics) == 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "policy: topics must name at least one topic the steps may read, as <alias>: <topic name>")
	}
	for alias, topic := range pol.Topics {
		if !aliasRE.MatchString(alias) {
			return protocol.Lastingf(protocol.CauseInvalidRequest, "policy: topics: alias %q must match %s", alias, aliasRE)
		}
		if !topicRE.MatchString(topic) {
			return protocol.Lastingf(protocol.CauseInvalidRequest, "policy: topics: %s: %q isn't a valid Kafka topic name", alias, topic)
		}
	}
	d.topics = pol.Topics
	if p.Connection == nil {
		return nil
	}
	if msgs := schema.Check(connectionSchema, p.Connection); len(msgs) > 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: %s", strings.Join(msgs, "; "))
	}
	var c struct {
		Brokers []string `json:"brokers"`
	}
	if err := protocol.Decode(p.Connection, &c); err != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: %v", err)
	}
	if len(c.Brokers) == 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: brokers must list at least one host:port")
	}
	for _, b := range c.Brokers {
		if host, port, err := net.SplitHostPort(b); err != nil || host == "" || port == "" {
			return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: broker %q isn't host:port", b)
		}
	}
	client, err := kgo.NewClient(kgo.SeedBrokers(c.Brokers...), kgo.DialTimeout(3*time.Second))
	if err != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: %v", err)
	}
	d.brokers, d.client = c.Brokers, client
	return nil
}

func (d *kafkaDriver) PolicyEntries() []protocol.PolicyEntry {
	aliases := make([]string, 0, len(d.topics))
	for a := range d.topics {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	out := make([]protocol.PolicyEntry, 0, len(aliases))
	for _, a := range aliases {
		out = append(out, protocol.PolicyEntry{Key: "topics: " + a, Rule: "KAFKA-03", Use: "a kafka.events read of topic " + a})
	}
	return out
}

func (d *kafkaDriver) Validate(ctx context.Context, p protocol.ValidateParams) (protocol.ValidateResult, error) {
	r := protocol.ValidateResult{Kind: protocol.KindObserve}
	add := func(rule, path, format string, a ...any) {
		r.Problems = append(r.Problems, protocol.Problem{Rule: rule, Message: fmt.Sprintf(format, a...), Path: path})
	}
	if p.Op != opEvents {
		add("KAFKA-01", "$.do", "unknown operation %q; the kafka driver has %q", p.Op, opEvents)
		return r, nil
	}
	if p.Setup {
		add("FIX-03", "$.do", "reading events is a verification step; setup goes through the application")
	}
	for _, msg := range schema.Check(argsSchema, p.Args, schema.AllowRefs) {
		add("KAFKA-01", "$.with", "%s", msg)
	}
	var a args
	if len(r.Problems) > 0 || protocol.Decode(p.Args, &a) != nil {
		return r, nil
	}
	if _, ok := d.topics[a.Topic]; !ok {
		add("KAFKA-03", "$.with.topic", "topic %q isn't an alias in the policy's topics", a.Topic)
		return r, nil
	}
	scoped := false
	check := func(path, s string) {
		for _, m := range refRE.FindAllStringSubmatch(s, -1) {
			n := m[1]
			switch {
			case n == "run.id", savedRE.MatchString(n):
				scoped = true
			case valueRE.MatchString(n):
			default:
				add("KAFKA-01", path, "unsupported reference ${%s}: use ${run.id}, ${<identity>.<value>} or a value saved by an earlier step", n)
			}
		}
		if strings.Count(s, "${") != len(refRE.FindAllString(s, -1)) {
			add("KAFKA-01", path, "unclosed ${ in %q", s)
		}
	}
	if a.Key != nil {
		check("$.with.key", *a.Key)
	}
	if a.ValueContains != nil {
		if *a.ValueContains == "" {
			add("KAFKA-01", "$.with.value_contains", "value_contains needs text to look for")
		}
		check("$.with.value_contains", *a.ValueContains)
	}
	for _, k := range sortedKeys(a.Where) {
		if !whereKey.MatchString(k) {
			add("KAFKA-01", "$.with.where", "where key %q must be a dotted path of names into the record's JSON value, such as data.id", k)
		}
		if s, ok := a.Where[k].(string); ok {
			check("$.with.where['"+k+"']", s)
		}
	}
	if !scoped {
		add("KAFKA-02", "$.with", "%s", errScoped)
	}
	r.Refs = refs(p.Args)
	r.Uses = []string{"topics: " + a.Topic}
	r.Targets = []string{"topic/" + a.Topic}
	return r, nil
}

func (d *kafkaDriver) Prove(ctx context.Context) (protocol.ProveResult, error) {
	return protocol.ProveResult{Proofs: []protocol.Proof{{
		Name:   "read_only",
		Status: protocol.ProofUnproven,
		Detail: "the driver only fetches records, without a consumer group, and never produces or commits; Kafka has no read-only connection to prove it",
	}}}, nil
}

func (d *kafkaDriver) Ready(ctx context.Context) (protocol.ReadyResult, error) {
	if err := d.client.Ping(ctx); err != nil {
		return protocol.ReadyResult{Ready: false, Detail: "no broker of " + strings.Join(d.brokers, ", ") + " answers: " + err.Error()}, nil
	}
	return protocol.ReadyResult{Ready: true, Detail: "a broker of " + strings.Join(d.brokers, ", ") + " answers"}, nil
}

func (d *kafkaDriver) Call(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	start := time.Now()
	if p.Op != opEvents {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "unknown operation %q", p.Op)
	}
	var a args
	if err := protocol.Decode(p.Args, &a); err != nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "events: %v", err)
	}
	topic, ok := d.topics[a.Topic]
	if !ok {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "events: topic %q isn't an alias in the policy's topics", a.Topic)
	}
	fact, scanned, err := d.read(ctx, topic, a)
	if err != nil {
		return protocol.CallResult{}, err
	}
	return protocol.CallResult{Fact: fact, Evidence: protocol.Evidence{
		Summary:    fmt.Sprintf("read %d records of topic %s, %d matched", scanned, a.Topic, fact["count"]),
		DurationMS: time.Since(start).Milliseconds(),
	}}, nil
}

func (d *kafkaDriver) read(ctx context.Context, topic string, a args) (map[string]any, int, error) {
	records := []any{}
	fact := map[string]any{"topic_exists": true, "scanned": 0, "count": 0, "records": records, "overflow": false}
	adm := kadm.NewClient(d.client)
	ends, err := adm.ListEndOffsets(ctx, topic)
	if err != nil {
		return nil, 0, failure(ctx, "list end offsets", err)
	}
	starts, err := adm.ListStartOffsets(ctx, topic)
	if err != nil {
		return nil, 0, failure(ctx, "list start offsets", err)
	}
	next := map[int32]int64{}
	end := map[int32]int64{}
	total := int64(0)
	var listErr error
	ends.Each(func(o kadm.ListedOffset) {
		if o.Topic != topic {
			return
		}
		if o.Err != nil {
			if errors.Is(o.Err, kerr.UnknownTopicOrPartition) {
				fact["topic_exists"] = false
				return
			}
			listErr = o.Err
			return
		}
		s, ok := starts.Lookup(topic, o.Partition)
		if !ok || s.Err != nil {
			listErr = fmt.Errorf("no start offset for partition %d", o.Partition)
			return
		}
		if o.Offset > s.Offset {
			next[o.Partition], end[o.Partition] = s.Offset, o.Offset
			total += o.Offset - s.Offset
		}
	})
	if listErr != nil {
		return nil, 0, failure(ctx, "list offsets", listErr)
	}
	if fact["topic_exists"] == false || len(end) == 0 {
		return fact, 0, nil
	}
	if total > maxScanned {
		fact["overflow"] = true
		fact["overflow_reason"] = fmt.Sprintf("the topic holds %d records, more than the %d the driver reads", total, maxScanned)
		return fact, 0, nil
	}
	offsets := map[int32]kgo.Offset{}
	for part, off := range next {
		offsets[part] = kgo.NewOffset().At(off)
	}
	consumer, err := kgo.NewClient(kgo.SeedBrokers(d.brokers...), kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: offsets}))
	if err != nil {
		return nil, 0, protocol.Errorf(protocol.CauseDriverFailed, "events: %v", err)
	}
	defer consumer.Close()
	scanned := 0
	for !reached(next, end) {
		fetches := consumer.PollFetches(ctx)
		if ctx.Err() != nil {
			return nil, 0, protocol.Errorf(protocol.CauseTimeout, "events: read %d of %d records of topic %s before the deadline", scanned, total, topic)
		}
		var fetchErr error
		fetches.EachError(func(_ string, _ int32, err error) {
			if fetchErr == nil {
				fetchErr = err
			}
		})
		if fetchErr != nil {
			return nil, 0, failure(ctx, "fetch", fetchErr)
		}
		fetches.EachRecord(func(rec *kgo.Record) {
			if rec.Offset >= end[rec.Partition] {
				return
			}
			if rec.Offset+1 > next[rec.Partition] {
				next[rec.Partition] = rec.Offset + 1
			}
			scanned++
			r := record(rec)
			if !matches(r, a) {
				return
			}
			if len(records) == maxReturned {
				fact["overflow"] = true
				fact["overflow_reason"] = fmt.Sprintf("more than %d records match", maxReturned)
				return
			}
			records = append(records, r)
		})
	}
	sort.SliceStable(records, func(i, j int) bool {
		ri, rj := records[i].(map[string]any), records[j].(map[string]any)
		if ri["partition"] != rj["partition"] {
			return ri["partition"].(int64) < rj["partition"].(int64)
		}
		return ri["offset"].(int64) < rj["offset"].(int64)
	})
	fact["records"], fact["count"], fact["scanned"] = records, len(records), scanned
	return fact, scanned, nil
}

func reached(next, end map[int32]int64) bool {
	for part, e := range end {
		if next[part] < e {
			return false
		}
	}
	return true
}

func failure(ctx context.Context, what string, err error) error {
	if ctx.Err() != nil {
		return protocol.Errorf(protocol.CauseTimeout, "events: %s: %v", what, err)
	}
	return protocol.Errorf(protocol.CauseUnreachable, "events: %s: %v", what, err)
}

func record(rec *kgo.Record) map[string]any {
	headers := map[string]any{}
	for _, h := range rec.Headers {
		headers[strings.ToValidUTF8(h.Key, "�")] = strings.ToValidUTF8(string(h.Value), "�")
	}
	value := rec.Value
	if len(value) > maxValueText {
		value = value[:maxValueText]
	}
	var decoded any
	dec := json.NewDecoder(bytes.NewReader(rec.Value))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil || dec.More() {
		decoded = nil
	}
	return map[string]any{
		"partition":  int64(rec.Partition),
		"offset":     rec.Offset,
		"timestamp":  rec.Timestamp.UTC().Format(time.RFC3339Nano),
		"key":        strings.ToValidUTF8(string(rec.Key), "�"),
		"headers":    headers,
		"value_text": strings.ToValidUTF8(string(value), "�"),
		"value":      decoded,
	}
}

func matches(r map[string]any, a args) bool {
	if a.Key != nil && r["key"] != *a.Key {
		return false
	}
	if a.ValueContains != nil && !strings.Contains(r["value_text"].(string), *a.ValueContains) {
		return false
	}
	for k, want := range a.Where {
		got, ok := lookup(r["value"], strings.Split(k, "."))
		if !ok || !sameJSON(got, want) {
			return false
		}
	}
	return true
}

func lookup(v any, path []string) (any, bool) {
	for _, name := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[name]; !ok {
			return nil, false
		}
	}
	return v, true
}

func sameJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
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

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (d *kafkaDriver) Close(ctx context.Context, p protocol.CloseParams) error {
	return protocol.Errorf(protocol.CauseInvalidRequest, "the kafka driver opens no handles, so it has no %q to close", p.Handle)
}

func main() {
	d := &kafkaDriver{}
	err := protocol.Serve(d)
	if d.client != nil {
		d.client.Close()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
