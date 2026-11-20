package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/bezmoradi/proctor/protocol"
)

type keyPair struct {
	partition string
	sort      string
}

type tableKeys struct {
	table   keyPair
	indexes map[string]keyPair
}

func pairOf(ks []types.KeySchemaElement) keyPair {
	var k keyPair
	for _, e := range ks {
		switch e.KeyType {
		case types.KeyTypeHash:
			k.partition = aws.ToString(e.AttributeName)
		case types.KeyTypeRange:
			k.sort = aws.ToString(e.AttributeName)
		}
	}
	return k
}

func (d *driver) describe(ctx context.Context, alias string) (*tableKeys, string, error) {
	out, err := d.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(d.tables[alias])})
	if err != nil {
		return nil, "", err
	}
	t := out.Table
	if t == nil {
		return nil, "", fmt.Errorf("DescribeTable returned no table")
	}
	k := &tableKeys{table: pairOf(t.KeySchema), indexes: map[string]keyPair{}}
	status := ""
	if t.TableStatus != types.TableStatusActive {
		status = string(t.TableStatus)
	}
	for _, g := range t.GlobalSecondaryIndexes {
		k.indexes[aws.ToString(g.IndexName)] = pairOf(g.KeySchema)
		if status == "" && g.IndexStatus != types.IndexStatusActive {
			status = fmt.Sprintf("ACTIVE, but its index %s is %s", aws.ToString(g.IndexName), g.IndexStatus)
		}
	}
	for _, l := range t.LocalSecondaryIndexes {
		k.indexes[aws.ToString(l.IndexName)] = pairOf(l.KeySchema)
	}
	if k.table.partition == "" {
		return nil, "", fmt.Errorf("DescribeTable returned no partition key")
	}
	return k, status, nil
}

func (d *driver) keysOf(ctx context.Context, alias string) (*tableKeys, error) {
	if k := d.keys[alias]; k != nil {
		return k, nil
	}
	k, _, err := d.describe(ctx, alias)
	if err != nil {
		return nil, d.classify(ctx, err, fmt.Sprintf("describing table %s (%s)", alias, d.tables[alias]))
	}
	d.keys[alias] = k
	return k, nil
}

type getArgsT struct {
	Table      string         `json:"table"`
	Key        map[string]any `json:"key"`
	Consistent *bool          `json:"consistent"`
}

type queryArgsT struct {
	Table string         `json:"table"`
	Index *string        `json:"index"`
	Key   map[string]any `json:"key"`
	Limit *int           `json:"limit"`
}

type putArgsT struct {
	Table string         `json:"table"`
	Item  map[string]any `json:"item"`
}

func (d *driver) Call(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	if d.client == nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the dynamodb driver has no connection")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}
	start := time.Now()
	var r protocol.CallResult
	var err error
	switch p.Op {
	case opGet:
		var a getArgsT
		if err := decodeArgs(p.Args, &a); err != nil {
			return r, err
		}
		r, err = d.get(ctx, a)
	case opQuery:
		var a queryArgsT
		if err := decodeArgs(p.Args, &a); err != nil {
			return r, err
		}
		r, err = d.query(ctx, a)
	case opPut:
		var a putArgsT
		if err := decodeArgs(p.Args, &a); err != nil {
			return r, err
		}
		r, err = d.put(ctx, a)
	default:
		return r, protocol.Lastingf(protocol.CauseInvalidRequest, "the dynamodb driver has no operation %q", p.Op)
	}
	if err != nil {
		return protocol.CallResult{}, err
	}
	r.Evidence.DurationMS = time.Since(start).Milliseconds()
	return r, nil
}

func decodeArgs(in any, out any) error {
	if err := protocol.Decode(in, out); err != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "with: %v", err)
	}
	return nil
}

func (d *driver) table(alias string) (string, error) {
	name, ok := d.tables[alias]
	if !ok {
		return "", protocol.Lastingf(protocol.CauseInvalidRequest, "table %q is not an alias in the driver's policy (DDB-01)", alias)
	}
	return name, nil
}

func (d *driver) get(ctx context.Context, a getArgsT) (protocol.CallResult, error) {
	name, err := d.table(a.Table)
	if err != nil {
		return protocol.CallResult{}, err
	}
	if len(a.Key) == 0 {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "key must name at least one attribute (DDB-02)")
	}
	key, err := toAttrMap(a.Key, "$.key")
	if err != nil {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "%v", err)
	}
	consistent := a.Consistent == nil || *a.Consistent
	out, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(name), Key: key, ConsistentRead: aws.Bool(consistent)})
	if err != nil {
		return protocol.CallResult{}, d.classify(ctx, err, "get on "+a.Table)
	}
	fact := map[string]any{"found": false, "item": nil}
	outcome := "not found"
	var found any
	if out.Item != nil {
		item, err := fromAttrMap(out.Item, "$.item")
		if err != nil {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnsupportedResult, "%v", err)
		}
		fact = map[string]any{"found": true, "item": item}
		outcome = "found"
		found = item
	}
	return protocol.CallResult{
		Fact: fact,
		Evidence: protocol.Evidence{
			Summary:         fmt.Sprintf("get on %s (%s) -> %s", a.Table, name, outcome),
			RequestExcerpt:  d.excerpt(fmt.Sprintf("GetItem %s consistent=%t key=", name, consistent), a.Key),
			ResponseExcerpt: d.excerpt("item=", found),
		},
	}, nil
}

func (d *driver) query(ctx context.Context, a queryArgsT) (protocol.CallResult, error) {
	name, err := d.table(a.Table)
	if err != nil {
		return protocol.CallResult{}, err
	}
	if len(a.Key) == 0 {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "key must name the partition key (DDB-02)")
	}
	want, capped := maxItems, false
	if a.Limit != nil {
		if *a.Limit < 1 || *a.Limit > maxItems {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "limit must be from 1 to %d", maxItems)
		}
		want, capped = *a.Limit, true
	}
	tk, err := d.keysOf(ctx, a.Table)
	if err != nil {
		return protocol.CallResult{}, err
	}
	pair, target := tk.table, "table "+a.Table
	if a.Index != nil {
		ip, ok := tk.indexes[*a.Index]
		if !ok {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "table %s (%s) has no index %q (indexes: %s)", a.Table, name, *a.Index, strings.Join(sortedKeys(tk.indexes), ", "))
		}
		pair, target = ip, "index "+*a.Index+" of table "+a.Table
	}
	allowed := pair.partition
	if pair.sort != "" {
		allowed += " and sort key " + pair.sort
	}
	if _, ok := a.Key[pair.partition]; !ok {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "key must name the partition key %s of %s", pair.partition, target)
	}
	names := map[string]string{}
	values := map[string]types.AttributeValue{}
	var conds []string
	for i, attr := range sortedKeys(a.Key) {
		if attr != pair.partition && (pair.sort == "" || attr != pair.sort) {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "key attribute %q isn't a key of %s; key may name only partition key %s", attr, target, allowed)
		}
		av, err := toAttr(a.Key[attr], "$.key."+attr)
		if err != nil {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "%v", err)
		}
		n, v := fmt.Sprintf("#k%d", i), fmt.Sprintf(":v%d", i)
		names[n], values[v] = attr, av
		conds = append(conds, n+" = "+v)
	}
	in := &dynamodb.QueryInput{
		TableName:                 aws.String(name),
		IndexName:                 a.Index,
		KeyConditionExpression:    aws.String(strings.Join(conds, " AND ")),
		ExpressionAttributeNames:  names,
		ExpressionAttributeValues: values,
		ConsistentRead:            aws.Bool(a.Index == nil),
	}
	items := []any{}
	for {
		in.Limit = aws.Int32(int32(want - len(items) + 1))
		out, err := d.client.Query(ctx, in)
		if err != nil {
			return protocol.CallResult{}, d.classify(ctx, err, "query on "+a.Table)
		}
		for _, it := range out.Items {
			item, err := fromAttrMap(it, fmt.Sprintf("$.items[%d]", len(items)))
			if err != nil {
				return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnsupportedResult, "%v", err)
			}
			items = append(items, item)
		}
		if len(items) > want || len(out.LastEvaluatedKey) == 0 {
			break
		}
		in.ExclusiveStartKey = out.LastEvaluatedKey
	}
	if len(items) > want {
		if !capped {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseUnsupportedResult, "query on %s matches more than %d items; set limit to read only the first ones", a.Table, maxItems)
		}
		items = items[:want]
	}
	noun := "items"
	if len(items) == 1 {
		noun = "item"
	}
	via := ""
	if a.Index != nil {
		via = " index " + *a.Index
	}
	return protocol.CallResult{
		Fact: map[string]any{"count": len(items), "items": items},
		Evidence: protocol.Evidence{
			Summary:         fmt.Sprintf("query on %s (%s)%s -> %d %s", a.Table, name, via, len(items), noun),
			RequestExcerpt:  d.excerpt(fmt.Sprintf("Query %s%s key=", name, via), a.Key),
			ResponseExcerpt: d.excerpt("items=", items),
		},
	}, nil
}

func (d *driver) put(ctx context.Context, a putArgsT) (protocol.CallResult, error) {
	name, err := d.table(a.Table)
	if err != nil {
		return protocol.CallResult{}, err
	}
	if len(a.Item) == 0 {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "item must have at least one attribute (DDB-02)")
	}
	item, err := toAttrMap(a.Item, "$.item")
	if err != nil {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "%v", err)
	}
	tk, err := d.keysOf(ctx, a.Table)
	if err != nil {
		return protocol.CallResult{}, err
	}
	key := map[string]any{}
	for _, attr := range []string{tk.table.partition, tk.table.sort} {
		if attr == "" {
			continue
		}
		v, ok := a.Item[attr]
		if !ok {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "item has no %s, a key attribute of table %s (%s)", attr, a.Table, name)
		}
		key[attr] = v
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                aws.String(name),
		Item:                     item,
		ConditionExpression:      aws.String("attribute_not_exists(#pk)"),
		ExpressionAttributeNames: map[string]string{"#pk": tk.table.partition},
	})
	if err != nil {
		var cf *types.ConditionalCheckFailedException
		if errors.As(err, &cf) {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "put on %s: an item with key %s already exists in table %s; put writes only new items", a.Table, d.excerpt("", key), name)
		}
		return protocol.CallResult{}, d.classify(ctx, err, "put on "+a.Table)
	}
	return protocol.CallResult{
		Fact: map[string]any{"written": true},
		Evidence: protocol.Evidence{
			Summary:         fmt.Sprintf("put on %s (%s) -> written", a.Table, name),
			RequestExcerpt:  d.excerpt(fmt.Sprintf("PutItem %s if attribute_not_exists(%s) key=", name, tk.table.partition), key),
			ResponseExcerpt: d.excerpt("written item=", a.Item),
		},
	}, nil
}

func (d *driver) excerpt(prefix string, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return prefix
	}
	s := d.scrub(prefix + string(b))
	if len(s) > excerptBytes {
		n := excerptBytes
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n] + "..."
	}
	return s
}
