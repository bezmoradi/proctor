package model

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

type SaveFrom struct {
	From    string
	Capture string
	Secret  bool
}

func (s *SaveFrom) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		var from string
		if err := n.Decode(&from); err != nil {
			return err
		}
		*s = SaveFrom{From: from}
		return nil
	case yaml.MappingNode:
	default:
		return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: a saved value is a path into the fact, or { from: <path>, capture: <pattern>, secret: true }, not %s", n.Line, nodeKind(n))}}
	}
	var bad []string
	var out SaveFrom
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		seen[k.Value] = true
		switch k.Value {
		case "from", "capture":
			if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
				bad = append(bad, fmt.Sprintf("line %d: %s must be text, not %s", v.Line, k.Value, nodeKind(v)))
				continue
			}
			switch {
			case k.Value == "from":
				out.From = v.Value
			case v.Value == "":
				bad = append(bad, fmt.Sprintf("line %d: capture is empty; give a pattern with one group, or leave capture out", v.Line))
			default:
				out.Capture = v.Value
			}
		case "secret":
			if v.Kind != yaml.ScalarNode || v.Tag != "!!bool" {
				bad = append(bad, fmt.Sprintf("line %d: secret must be true or false, not %s", v.Line, nodeKind(v)))
				continue
			}
			if err := v.Decode(&out.Secret); err != nil {
				return err
			}
		default:
			bad = append(bad, fmt.Sprintf("line %d: unknown key %q; a saved value has only from, capture and secret", k.Line, k.Value))
		}
	}
	switch {
	case !seen["from"]:
		bad = append(bad, fmt.Sprintf("line %d: from is required in { from: <path>, capture: <pattern>, secret: true }", n.Line))
	case !seen["capture"] && !seen["secret"]:
		bad = append(bad, fmt.Sprintf("line %d: give capture, secret or both with from; write the path alone to save the whole value", n.Line))
	}
	if len(bad) > 0 {
		return &yaml.TypeError{Errors: bad}
	}
	*s = out
	return nil
}

func (s SaveFrom) MarshalJSON() ([]byte, error) {
	if s.Capture == "" && !s.Secret {
		return json.Marshal(s.From)
	}
	out := map[string]any{"from": s.From}
	if s.Capture != "" {
		out["capture"] = s.Capture
	}
	if s.Secret {
		out["secret"] = true
	}
	return json.Marshal(out)
}

func SavePaths(save map[string]SaveFrom) map[string]string {
	if save == nil {
		return nil
	}
	out := make(map[string]string, len(save))
	for name, s := range save {
		out[name] = s.From
	}
	return out
}

func CapturePattern(pattern string) (*regexp.Regexp, error) {
	switch {
	case pattern == "":
		return nil, fmt.Errorf("capture is empty; give a pattern with one group, such as code=([0-9a-f]+)")
	case len(pattern) > MaxPatternBytes:
		return nil, fmt.Errorf("the capture pattern is %d bytes, longer than %d", len(pattern), MaxPatternBytes)
	case strings.Contains(pattern, "${"):
		return nil, fmt.Errorf("capture %q holds ${...}, which capture doesn't expand; write the pattern itself", pattern)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("capture %q isn't a valid RE2 regular expression: %v", pattern, err)
	}
	if re.NumSubexp() != 1 {
		return nil, fmt.Errorf("capture %q has %d groups; it needs exactly one, the part to save (write (?:...) for a group that isn't saved)", pattern, re.NumSubexp())
	}
	return re, nil
}
