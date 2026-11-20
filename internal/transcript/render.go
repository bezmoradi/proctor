package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"proctor/internal/model"
)

type Poll struct {
	Mode   string
	Window time.Duration
}

type Step struct {
	ID, Phase, Do     string
	Identity, Variant string
	With, Fact        any
	HasWith, HasFact  bool
	Poll              *Poll
	Saved             []string
	Result            string
	Reason, Cause     string
}

type Doc struct {
	Spec, Scenario, Table string
	Steps                 []Step
}

func (d Doc) Tree() map[string]any {
	steps := make([]any, 0, len(d.Steps))
	for _, s := range d.Steps {
		m := map[string]any{"id": s.ID, "phase": s.Phase, "do": s.Do}
		if s.Identity != "" {
			m["identity"] = s.Identity
		}
		if s.Variant != "" {
			m["variant"] = s.Variant
		}
		if s.HasWith {
			m["with"] = s.With
		}
		if s.HasFact {
			m["fact"] = s.Fact
		}
		if s.Poll != nil {
			m["poll"] = map[string]any{"mode": s.Poll.Mode, "window": s.Poll.Window.String()}
		}
		if len(s.Saved) > 0 {
			saved := make([]any, len(s.Saved))
			for i, n := range s.Saved {
				saved[i] = n
			}
			m["saved"] = saved
		}
		if s.Result != "" {
			m["result"] = s.Result
		}
		if s.Reason != "" {
			m["reason"] = s.Reason
		}
		if s.Cause != "" {
			m["cause"] = s.Cause
		}
		steps = append(steps, m)
	}
	t := map[string]any{"format": model.TranscriptFormat, "specification": d.Spec, "scenario": d.Scenario, "steps": steps}
	if d.Table != "" {
		t["table"] = d.Table
	}
	return t
}

var plainKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

var yamlWords = map[string]bool{"y": true, "yes": true, "n": true, "no": true, "true": true, "false": true, "on": true, "off": true, "null": true}

func Render(tree map[string]any) ([]byte, error) {
	var b bytes.Buffer
	if err := writeMap(&b, tree, 0); err != nil {
		return nil, err
	}
	out := b.Bytes()
	if !bytes.HasPrefix(out, []byte(model.TranscriptFirstLine+"\n")) {
		return nil, fmt.Errorf("the transcript doesn't start with %q", model.TranscriptFirstLine)
	}
	return out, nil
}

func key(k string) (string, error) {
	if plainKey.MatchString(k) && !yamlWords[strings.ToLower(k)] {
		return k, nil
	}
	return scalar(k)
}

func scalar(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "null", nil
	case bool:
		return strconv.FormatBool(x), nil
	case json.Number:
		if !json.Valid([]byte(x.String())) {
			return scalar(x.String())
		}
		return x.String(), nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

func pad(n int) string { return strings.Repeat("  ", n) }

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func writeMap(b *bytes.Buffer, m map[string]any, depth int) error {
	for _, k := range sortedKeys(m) {
		name, err := key(k)
		if err != nil {
			return err
		}
		if err := writeEntry(b, pad(depth)+name+":", m[k], depth); err != nil {
			return err
		}
	}
	return nil
}

func writeEntry(b *bytes.Buffer, head string, v any, depth int) error {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			b.WriteString(head + " {}\n")
			return nil
		}
		b.WriteString(head + "\n")
		return writeMap(b, x, depth+1)
	case []any:
		if len(x) == 0 {
			b.WriteString(head + " []\n")
			return nil
		}
		b.WriteString(head + "\n")
		return writeList(b, x, depth+1)
	}
	s, err := scalar(v)
	if err != nil {
		return err
	}
	b.WriteString(head + " " + s + "\n")
	return nil
}

func writeList(b *bytes.Buffer, list []any, depth int) error {
	for _, item := range list {
		switch x := item.(type) {
		case map[string]any:
			if len(x) == 0 {
				b.WriteString(pad(depth) + "- {}\n")
				continue
			}
			var inner bytes.Buffer
			if err := writeMap(&inner, x, depth+1); err != nil {
				return err
			}
			text := inner.String()
			b.WriteString(pad(depth) + "- " + strings.TrimPrefix(text, pad(depth+1)))
		case []any:
			if len(x) == 0 {
				b.WriteString(pad(depth) + "- []\n")
				continue
			}
			b.WriteString(pad(depth) + "-\n")
			if err := writeList(b, x, depth+1); err != nil {
				return err
			}
		default:
			s, err := scalar(item)
			if err != nil {
				return err
			}
			b.WriteString(pad(depth) + "- " + s + "\n")
		}
	}
	return nil
}
