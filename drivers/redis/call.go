package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/redis/go-redis/v9"

	"github.com/bezmoradi/proctor/protocol"
)

const (
	chunk          = 100
	scanCount      = 1000
	deadlineMargin = 250 * time.Millisecond
	excerptBytes   = 4096
)

var errWrongType = errors.New("the key changed type while it was read")

type keyArgs struct {
	Key string `json:"key"`
}

type keysArgs struct {
	Match string `json:"match"`
}

type reader struct {
	d        *Driver
	key      string
	commands []string
	budget   int
	cut      bool
	gone     bool
	excerpt  string
}

func (d *Driver) Call(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	start := time.Now()
	if p.Op != OpKey && p.Op != OpKeys {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "unknown operation %q", p.Op)
	}
	if d.client == nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the redis driver has no connection")
	}
	ctx, cancel := callContext(ctx)
	defer cancel()
	if p.Op == OpKey {
		var a keyArgs
		if err := protocol.Decode(p.Args, &a); err != nil {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "key: %v", err)
		}
		if a.Key == "" || !d.allows(a.Key) {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "key %q doesn't match the policy's key patterns (%s), so it isn't read (%s)", a.Key, d.policyText(), rulePolicy)
		}
		fact, r, err := d.readKey(ctx, a.Key)
		if err != nil {
			return protocol.CallResult{}, err
		}
		return protocol.CallResult{Fact: fact, Evidence: protocol.Evidence{
			Summary:         keySummary(a.Key, fact),
			RequestExcerpt:  strings.Join(r.commands, "\n"),
			ResponseExcerpt: r.excerpt,
			DurationMS:      time.Since(start).Milliseconds(),
		}}, nil
	}
	var a keysArgs
	if err := protocol.Decode(p.Args, &a); err != nil {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "keys: %v", err)
	}
	g, err := parseGlob(a.Match)
	if err != nil {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "match %q, with the saved values inserted, isn't a valid pattern: every [ needs its ], so nothing is read (%s)", a.Match, ruleArgs)
	}
	if a.Match == "" || len(d.covering(g)) == 0 {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "match %q, with the saved values inserted, can name keys outside the policy's key patterns (%s), so nothing is read (%s)", a.Match, d.policyText(), rulePolicy)
	}
	fact, calls, err := d.scanKeys(ctx, a.Match)
	if err != nil {
		return protocol.CallResult{}, err
	}
	summary := fmt.Sprintf("keys matching %s -> %d %s", a.Match, fact["count"], plural(fact["count"].(int), "key", "keys"))
	if fact["overflow"] == true {
		summary += " (overflow)"
	}
	return protocol.CallResult{Fact: fact, Evidence: protocol.Evidence{
		Summary:        summary,
		RequestExcerpt: fmt.Sprintf("SCAN <cursor> MATCH %s COUNT %d (%d calls)", a.Match, scanCount, calls),
		DurationMS:     time.Since(start).Milliseconds(),
	}}, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func keySummary(key string, fact map[string]any) string {
	if fact["exists"] != true {
		return fmt.Sprintf("key %s -> doesn't exist", key)
	}
	size := fact["size"].(int64)
	var what string
	switch fact["type"] {
	case "string":
		what = fmt.Sprintf("string of %d %s", size, plural(int(size), "byte", "bytes"))
	case "hash":
		what = fmt.Sprintf("hash of %d %s", size, plural(int(size), "field", "fields"))
	default:
		what = fmt.Sprintf("%s of %d %s", fact["type"], size, plural(int(size), "element", "elements"))
	}
	ttl := "no expiry"
	if t := fact["ttl_ms"].(int64); t >= 0 {
		ttl = fmt.Sprintf("expires in %d ms", t)
	}
	out := fmt.Sprintf("key %s -> %s, %s", key, what, ttl)
	if fact["value_truncated"] == true {
		out += fmt.Sprintf(" (value over %d KiB, overflow)", MaxValue>>10)
	}
	return out
}

func (d *Driver) readKey(ctx context.Context, key string) (map[string]any, *reader, error) {
	for attempt := 0; ; attempt++ {
		r := &reader{d: d, key: key}
		fact, err := r.read(ctx)
		if errors.Is(err, errWrongType) && attempt == 0 {
			continue
		}
		if errors.Is(err, errWrongType) {
			return nil, nil, protocol.Errorf(protocol.CauseQueryError, "key %s changed type twice while it was read", key)
		}
		return fact, r, err
	}
}

func (r *reader) fail(ctx context.Context, err error, command string) error {
	if msg, ok := serverError(err); ok && strings.HasPrefix(msg, "WRONGTYPE") {
		return errWrongType
	}
	return r.d.conn.classify(ctx, err, command+" "+r.key)
}

func (r *reader) sent(command string) {
	r.commands = append(r.commands, command+" "+r.key)
}

func missing() map[string]any {
	return map[string]any{"exists": false, "type": "none", "value": nil, "value_truncated": false, "size": int64(0), "ttl_ms": int64(-2), "overflow": false}
}

func (r *reader) read(ctx context.Context) (map[string]any, error) {
	c := r.d.client
	var typ *redis.StatusCmd
	var ttl *redis.Cmd
	_, err := c.Pipelined(ctx, func(p redis.Pipeliner) error {
		typ = p.Type(ctx, r.key)
		ttl = p.Do(ctx, "PTTL", r.key)
		return nil
	})
	r.sent("TYPE")
	r.sent("PTTL")
	if err != nil {
		return nil, r.fail(ctx, err, "TYPE")
	}
	kind := typ.Val()
	ms, err := ttl.Int64()
	if err != nil {
		return nil, r.fail(ctx, err, "PTTL")
	}
	var value any
	var size int64
	switch kind {
	case "none":
		return missing(), nil
	case "string":
		value, size, err = r.str(ctx)
	case "hash":
		value, size, err = r.hash(ctx)
	case "list":
		value, size, err = r.list(ctx)
	case "set":
		value, size, err = r.set(ctx)
	case "zset":
		value, size, err = r.zset(ctx)
	default:
		return nil, protocol.Errorf(protocol.CauseUnsupportedResult, "key %s holds a %s, which the redis driver doesn't read; it reads strings, hashes, lists, sets and sorted sets", r.key, kind)
	}
	if err != nil {
		return nil, err
	}
	if r.gone || (kind != "string" && size == 0) {
		return missing(), nil
	}
	fact := map[string]any{"exists": true, "type": kind, "value": value, "value_truncated": r.cut, "size": size, "ttl_ms": ms, "overflow": r.cut}
	if r.cut {
		fact["overflow_reason"] = r.cutReason(kind)
	}
	if s, ok := value.(string); ok && !r.cut {
		if j, ok := parseJSON(s); ok {
			fact["value_json"] = j
		}
	}
	return fact, nil
}

func (r *reader) cutReason(kind string) string {
	switch kind {
	case "list", "zset":
		return fmt.Sprintf("the value is larger than %d KiB, so the fact holds only the elements before the cut", MaxValue>>10)
	case "string":
		return fmt.Sprintf("the value is larger than %d KiB, so the fact holds none of it (value is null); the step's response excerpt shows its start", MaxValue>>10)
	}
	return fmt.Sprintf("the value is larger than %d KiB, so the fact holds none of it (value is null)", MaxValue>>10)
}

func (r *reader) take(n int) bool {
	if r.budget+n > MaxValue {
		r.cut = true
		return false
	}
	r.budget += n
	return true
}

func (r *reader) text(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", protocol.Errorf(protocol.CauseUnsupportedResult, "key %s holds bytes that aren't UTF-8 text, which a fact can't hold", r.key)
	}
	return s, nil
}

func (r *reader) str(ctx context.Context) (any, int64, error) {
	c := r.d.client
	r.sent("STRLEN")
	n, err := c.StrLen(ctx, r.key).Result()
	if err != nil {
		return nil, 0, r.fail(ctx, err, "STRLEN")
	}
	if n > MaxValue {
		r.cut = true
		r.sent("GETRANGE")
		start, err := c.GetRange(ctx, r.key, 0, excerptBytes-1).Result()
		if err != nil {
			return nil, 0, r.fail(ctx, err, "GETRANGE")
		}
		r.excerpt = fmt.Sprintf("the first %d of %d bytes: %s", len(start), n, strings.ToValidUTF8(start, "\uFFFD"))
		return nil, n, nil
	}
	r.sent("GET")
	v, err := c.Get(ctx, r.key).Result()
	if errors.Is(err, redis.Nil) {
		r.gone = true
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, r.fail(ctx, err, "GET")
	}
	if len(v) > MaxValue {
		r.cut = true
		r.excerpt = fmt.Sprintf("the first %d of %d bytes: %s", excerptBytes, len(v), strings.ToValidUTF8(v[:excerptBytes], "\uFFFD"))
		return nil, int64(len(v)), nil
	}
	s, err := r.text(v)
	return s, int64(len(v)), err
}

func (r *reader) hash(ctx context.Context) (any, int64, error) {
	c := r.d.client
	r.sent("HLEN")
	n, err := c.HLen(ctx, r.key).Result()
	if err != nil {
		return nil, 0, r.fail(ctx, err, "HLEN")
	}
	out := map[string]any{}
	r.sent("HSCAN")
	var cursor uint64
	for {
		kv, next, err := c.HScan(ctx, r.key, cursor, "", chunk).Result()
		if err != nil {
			return nil, 0, r.fail(ctx, err, "HSCAN")
		}
		for i := 0; i+1 < len(kv); i += 2 {
			if _, seen := out[kv[i]]; seen {
				continue
			}
			if !r.take(len(kv[i]) + len(kv[i+1])) {
				return nil, n, nil
			}
			f, err := r.text(kv[i])
			if err != nil {
				return nil, 0, err
			}
			v, err := r.text(kv[i+1])
			if err != nil {
				return nil, 0, err
			}
			out[f] = v
		}
		if cursor = next; cursor == 0 {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, 0, r.fail(ctx, err, "HSCAN")
		}
	}
	return out, n, nil
}

func (r *reader) list(ctx context.Context) (any, int64, error) {
	c := r.d.client
	r.sent("LLEN")
	n, err := c.LLen(ctx, r.key).Result()
	if err != nil {
		return nil, 0, r.fail(ctx, err, "LLEN")
	}
	out := []any{}
	r.sent("LRANGE")
	for i := int64(0); i < n && !r.cut; i += chunk {
		part, err := c.LRange(ctx, r.key, i, i+chunk-1).Result()
		if err != nil {
			return nil, 0, r.fail(ctx, err, "LRANGE")
		}
		for _, e := range part {
			if !r.take(len(e)) {
				break
			}
			s, err := r.text(e)
			if err != nil {
				return nil, 0, err
			}
			out = append(out, s)
		}
		if len(part) < chunk {
			break
		}
	}
	return out, n, nil
}

func (r *reader) set(ctx context.Context) (any, int64, error) {
	c := r.d.client
	r.sent("SCARD")
	n, err := c.SCard(ctx, r.key).Result()
	if err != nil {
		return nil, 0, r.fail(ctx, err, "SCARD")
	}
	seen := map[string]bool{}
	r.sent("SSCAN")
	var cursor uint64
	for {
		members, next, err := c.SScan(ctx, r.key, cursor, "", chunk).Result()
		if err != nil {
			return nil, 0, r.fail(ctx, err, "SSCAN")
		}
		for _, m := range members {
			if seen[m] {
				continue
			}
			if !r.take(len(m)) {
				return nil, n, nil
			}
			if _, err := r.text(m); err != nil {
				return nil, 0, err
			}
			seen[m] = true
		}
		if cursor = next; cursor == 0 {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, 0, r.fail(ctx, err, "SSCAN")
		}
	}
	out := make([]any, 0, len(seen))
	for _, m := range sortedKeys(seen) {
		out = append(out, m)
	}
	return out, n, nil
}

func (r *reader) zset(ctx context.Context) (any, int64, error) {
	c := r.d.client
	r.sent("ZCARD")
	n, err := c.ZCard(ctx, r.key).Result()
	if err != nil {
		return nil, 0, r.fail(ctx, err, "ZCARD")
	}
	out := []any{}
	r.sent("ZRANGE WITHSCORES")
	for i := int64(0); i < n && !r.cut; i += chunk {
		part, err := c.ZRangeWithScores(ctx, r.key, i, i+chunk-1).Result()
		if err != nil {
			return nil, 0, r.fail(ctx, err, "ZRANGE")
		}
		for _, z := range part {
			m, _ := z.Member.(string)
			if !r.take(len(m)) {
				break
			}
			s, err := r.text(m)
			if err != nil {
				return nil, 0, err
			}
			out = append(out, map[string]any{"member": s, "score": score(z.Score)})
		}
		if len(part) < chunk {
			break
		}
	}
	return out, n, nil
}

func score(f float64) any {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	return json.Number(strconv.FormatFloat(f, 'g', -1, 64))
}

func parseJSON(s string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return v, true
}

func (d *Driver) scanKeys(ctx context.Context, match string) (map[string]any, int, error) {
	stopAt := time.Now().Add(CallTimeout)
	if dl, ok := ctx.Deadline(); ok {
		stopAt = dl.Add(-deadlineMargin)
	}
	seen := map[string]bool{}
	why := ""
	calls := 0
	var cursor uint64
	for {
		keys, next, err := d.client.Scan(ctx, cursor, match, scanCount).Result()
		calls++
		if err != nil {
			return nil, calls, d.conn.classify(ctx, err, "SCAN MATCH "+match)
		}
		for _, k := range keys {
			if !utf8.ValidString(k) {
				return nil, calls, protocol.Errorf(protocol.CauseUnsupportedResult, "SCAN MATCH %s found a key whose name isn't UTF-8 text", match)
			}
			if !d.allows(k) {
				return nil, calls, protocol.Errorf(protocol.CauseDriverFailed, "SCAN MATCH %s found key %q, which the policy's key patterns don't match", match, k)
			}
			seen[k] = true
		}
		if len(seen) > MaxKeys {
			why = fmt.Sprintf("more than %d keys match, and the fact holds %d of them; match more narrowly", MaxKeys, MaxKeys)
			break
		}
		if cursor = next; cursor == 0 {
			break
		}
		if time.Now().After(stopAt) {
			why = fmt.Sprintf("the scan stopped at the step's deadline after %d SCAN calls, before it had seen every key", calls)
			break
		}
	}
	keys := sortedKeys(seen)
	if len(keys) > MaxKeys {
		keys = keys[:MaxKeys]
	}
	list := make([]any, len(keys))
	for i, k := range keys {
		list[i] = k
	}
	fact := map[string]any{"count": len(list), "keys": list, "overflow": why != ""}
	if why != "" {
		fact["overflow_reason"] = why
	}
	return fact, calls, nil
}
