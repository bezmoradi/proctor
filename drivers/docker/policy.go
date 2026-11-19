package docker

import (
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"proctor/protocol"
)

const (
	scopeRule      = "DKR-01"
	connectionRule = "DKR-02"
	maxScope       = 16
	maxScopeValue  = 1024
)

var labelKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)

type scopeEntry struct {
	label string
	value string
}

func (s scopeEntry) String() string {
	return s.label + "=" + s.value
}

func describeScope(scope []scopeEntry) string {
	parts := make([]string, len(scope))
	for i, s := range scope {
		parts[i] = s.String()
	}
	return strings.Join(parts, " or ")
}

func parseScope(raw any) ([]scopeEntry, error) {
	var msgs []string
	bad := func(format string, args ...any) {
		msgs = append(msgs, "policy: "+fmt.Sprintf(format, args...))
	}
	pol, isObject := raw.(map[string]any)
	switch {
	case raw == nil:
		bad("is required: the docker driver reads only objects that carry a label its policy lists, as scope: [{ label, value }, ...]")
		return nil, ruleError(msgs, scopeRule)
	case !isObject:
		bad("must be an object with scope, not %s", show(raw))
		return nil, ruleError(msgs, scopeRule)
	}
	for _, k := range sortedKeys(pol) {
		if k != "scope" {
			bad("unknown key %q; the docker driver's policy has only scope", k)
		}
	}
	list, has := pol["scope"]
	entries, isList := list.([]any)
	switch {
	case !has:
		bad("scope is required")
	case !isList:
		bad("scope must be a list of { label, value } entries, not %s", show(list))
	case len(entries) == 0:
		bad("scope is empty; list at least one label and its value")
	case len(entries) > maxScope:
		bad("scope has %d entries; at most %d are allowed", len(entries), maxScope)
	}
	var out []scopeEntry
	first := map[scopeEntry]int{}
	for i, e := range entries {
		at := fmt.Sprintf("scope[%d]", i)
		entry, isObject := e.(map[string]any)
		if !isObject {
			bad("%s must be an object with label and value, not %s", at, show(e))
			continue
		}
		for _, k := range sortedKeys(entry) {
			if k != "label" && k != "value" {
				bad("%s: unknown key %q; an entry has only label and value", at, k)
			}
		}
		label, labelIsText := entry["label"].(string)
		value, valueIsText := entry["value"].(string)
		_, hasValue := entry["value"]
		ok := true
		switch {
		case !labelIsText:
			bad("%s: label must be a label key, not %s", at, show(entry["label"]))
			ok = false
		case !labelKey.MatchString(label):
			bad("%s: label %q must match %s", at, label, labelKey.String())
			ok = false
		}
		switch {
		case !hasValue:
			bad("%s: value is required; a label without a value would show every object that carries it, such as every Compose project", at)
			ok = false
		case !valueIsText:
			bad("%s: value must be text, not %s", at, show(entry["value"]))
			ok = false
		case value == "":
			bad("%s: value is empty; give the exact value the label must have", at)
			ok = false
		case utf8.RuneCountInString(value) > maxScopeValue:
			bad("%s: value is longer than %d characters", at, maxScopeValue)
			ok = false
		case strings.Contains(value, "${"):
			bad("%s: value %q can't hold ${...}; the scope is fixed in proctor.yaml", at, value)
			ok = false
		case strings.Contains(value, "*"):
			bad("%s: value %q can't hold *; the scope matches exact values only", at, value)
			ok = false
		case hasControl(value):
			bad("%s: value can't hold a newline or another control character", at)
			ok = false
		}
		if !ok {
			continue
		}
		s := scopeEntry{label: label, value: value}
		if j, dup := first[s]; dup {
			bad("%s repeats scope[%d] (%s)", at, j, s)
			continue
		}
		first[s] = i
		out = append(out, s)
	}
	if len(msgs) > 0 {
		return nil, ruleError(msgs, scopeRule)
	}
	return out, nil
}

func parseConnection(raw any, secrets protocol.Secrets) (string, error) {
	var msgs []string
	bad := func(format string, args ...any) {
		msgs = append(msgs, "connection: "+fmt.Sprintf(format, args...))
	}
	if len(secrets) > 0 {
		bad("the docker driver takes no secrets; name the socket with socket")
		return "", ruleError(msgs, connectionRule)
	}
	conn, isObject := raw.(map[string]any)
	if !isObject {
		bad("must be an object with socket, not %s", jsonKind(raw))
		return "", ruleError(msgs, connectionRule)
	}
	for _, k := range sortedKeys(conn) {
		if k != "socket" {
			bad("unknown key %q; the docker driver's binding has only socket, the absolute path of Docker's Unix socket", k)
		}
	}
	v, has := conn["socket"]
	socket, isText := v.(string)
	switch {
	case !has:
		bad("socket is required: the absolute path of Docker's Unix socket, such as /var/run/docker.sock; the driver reads no DOCKER_HOST or Docker context")
	case !isText:
		bad("socket must be a path, not %s", jsonKind(v))
	case strings.Contains(socket, "://"):
		bad("socket must be the path of a Unix socket; TCP, TLS and SSH Docker hosts aren't supported")
	case !strings.HasPrefix(socket, "/"):
		bad("socket must be an absolute path")
	case hasControl(socket):
		bad("socket can't hold a control character")
	case filepath.Clean(socket) != socket || slices.Contains(strings.Split(socket, "/"), ".."):
		bad("socket must be a clean path, with no .., . or repeated /")
	}
	if len(msgs) > 0 {
		return "", ruleError(msgs, connectionRule)
	}
	return socket, nil
}

func ruleError(msgs []string, rule string) error {
	return fmt.Errorf("%s (%s)", strings.Join(msgs, "; "), rule)
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "text"
	case bool:
		return "a boolean"
	case float64, int, int64, json.Number:
		return "a number"
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	}
	return "another kind of value"
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
