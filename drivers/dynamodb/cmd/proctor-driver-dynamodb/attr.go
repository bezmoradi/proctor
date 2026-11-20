package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

var jsonNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

func toAttr(v any, at string) (types.AttributeValue, error) {
	switch t := v.(type) {
	case nil:
		return &types.AttributeValueMemberNULL{Value: true}, nil
	case bool:
		return &types.AttributeValueMemberBOOL{Value: t}, nil
	case string:
		return &types.AttributeValueMemberS{Value: t}, nil
	case json.Number:
		if !jsonNumber.MatchString(t.String()) {
			return nil, fmt.Errorf("%s: %q isn't a JSON number", at, t.String())
		}
		return &types.AttributeValueMemberN{Value: t.String()}, nil
	case map[string]any:
		m, err := toAttrMap(t, at)
		if err != nil {
			return nil, err
		}
		return &types.AttributeValueMemberM{Value: m}, nil
	case []any:
		l := make([]types.AttributeValue, len(t))
		for i, e := range t {
			av, err := toAttr(e, fmt.Sprintf("%s[%d]", at, i))
			if err != nil {
				return nil, err
			}
			l[i] = av
		}
		return &types.AttributeValueMemberL{Value: l}, nil
	}
	return nil, fmt.Errorf("%s: unsupported value of type %T", at, v)
}

func toAttrMap(m map[string]any, at string) (map[string]types.AttributeValue, error) {
	out := make(map[string]types.AttributeValue, len(m))
	for _, k := range sortedKeys(m) {
		av, err := toAttr(m[k], at+"."+k)
		if err != nil {
			return nil, err
		}
		out[k] = av
	}
	return out, nil
}

func fromAttr(av types.AttributeValue, at string) (any, error) {
	switch t := av.(type) {
	case *types.AttributeValueMemberS:
		return t.Value, nil
	case *types.AttributeValueMemberN:
		return number(t.Value, at)
	case *types.AttributeValueMemberBOOL:
		return t.Value, nil
	case *types.AttributeValueMemberNULL:
		return nil, nil
	case *types.AttributeValueMemberB:
		return base64.StdEncoding.EncodeToString(t.Value), nil
	case *types.AttributeValueMemberM:
		return fromAttrMap(t.Value, at)
	case *types.AttributeValueMemberL:
		out := make([]any, len(t.Value))
		for i, e := range t.Value {
			v, err := fromAttr(e, fmt.Sprintf("%s[%d]", at, i))
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	case *types.AttributeValueMemberSS:
		out := make([]any, len(t.Value))
		for i, s := range t.Value {
			out[i] = s
		}
		return out, nil
	case *types.AttributeValueMemberNS:
		out := make([]any, len(t.Value))
		for i, s := range t.Value {
			n, err := number(s, fmt.Sprintf("%s[%d]", at, i))
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	case *types.AttributeValueMemberBS:
		out := make([]any, len(t.Value))
		for i, b := range t.Value {
			out[i] = base64.StdEncoding.EncodeToString(b)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s: unsupported attribute value %T", at, av)
}

func fromAttrMap(m map[string]types.AttributeValue, at string) (map[string]any, error) {
	out := make(map[string]any, len(m))
	for _, k := range sortedKeys(m) {
		v, err := fromAttr(m[k], at+"."+k)
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

func number(s, at string) (json.Number, error) {
	if !jsonNumber.MatchString(s) {
		return "", fmt.Errorf("%s: the number %q can't be written as JSON", at, s)
	}
	return json.Number(s), nil
}
