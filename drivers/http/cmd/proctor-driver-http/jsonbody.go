package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func parseJSON(b []byte) (value any, reason string, syntaxOK bool) {
	v, err := decodeJSON(b)
	if err != nil {
		return nil, err.Error(), false
	}
	if k, dup := duplicateKey(b); dup {
		return nil, fmt.Sprintf("duplicate object key %q", k), true
	}
	return v, "", true
}

func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after the JSON value")
	}
	return v, nil
}

func duplicateKey(b []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	type frame struct {
		obj    bool
		seen   map[string]bool
		expKey bool
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", false
		}
		top := (*frame)(nil)
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if top != nil && top.obj && top.expKey {
			if d, ok := tok.(json.Delim); ok && d == '}' {
				stack = stack[:len(stack)-1]
				if len(stack) > 0 && stack[len(stack)-1].obj {
					stack[len(stack)-1].expKey = true
				}
				continue
			}
			k := tok.(string)
			if top.seen[k] {
				return k, true
			}
			top.seen[k] = true
			top.expKey = false
			continue
		}
		switch d := tok.(type) {
		case json.Delim:
			switch d {
			case '{':
				stack = append(stack, &frame{obj: true, seen: map[string]bool{}, expKey: true})
				continue
			case '[':
				stack = append(stack, &frame{})
				continue
			case ']':
				stack = stack[:len(stack)-1]
			}
		}

		if len(stack) > 0 && stack[len(stack)-1].obj {
			stack[len(stack)-1].expKey = true
		}
		if len(stack) == 0 && !dec.More() {
			return "", false
		}
	}
}
