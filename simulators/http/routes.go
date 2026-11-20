package simhttp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	MaxRouteFile = 8 << 20
	MaxFileBody  = 8 << 20
	MinDelay     = time.Millisecond
	MaxDelay     = 10 * time.Minute
	ControlRoot  = "__proctor"
)

var Methods = []string{"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"}

var (
	idRE         = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	paramRE      = regexp.MustCompile(`^\{([A-Za-z_][A-Za-z0-9_]*)\}$`)
	templateRE   = regexp.MustCompile(`\{\{([^{}]*)\}\}`)
	headerNameRE = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")
)

var builtinRedacted = []string{"authorization", "proxy-authorization", "cookie", "set-cookie", "x-auth-token", "x-subject-token", "private-token", "x-api-key"}

var forbiddenResponseHeaders = map[string]bool{"content-length": true, "transfer-encoding": true, "connection": true}

type LoadError struct {
	Line int
	Msg  string
}

func (e *LoadError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
	}
	return e.Msg
}

func errAt(n *yaml.Node, format string, args ...any) *LoadError {
	line := 0
	if n != nil {
		line = n.Line
	}
	return &LoadError{Line: line, Msg: fmt.Sprintf(format, args...)}
}

type fileSpec struct {
	Version int         `yaml:"version"`
	Redact  redactSpec  `yaml:"redact"`
	Routes  []routeSpec `yaml:"routes"`
}

type redactSpec struct {
	Headers   []string `yaml:"headers"`
	JSONPaths []string `yaml:"json_paths"`
	Query     []string `yaml:"query"`
}

type routeSpec struct {
	ID      string      `yaml:"id"`
	Match   matchSpec   `yaml:"match"`
	Respond respondSpec `yaml:"respond"`
}

type matchSpec struct {
	Method  string    `yaml:"method"`
	Path    string    `yaml:"path"`
	Query   yaml.Node `yaml:"query"`
	Headers yaml.Node `yaml:"headers"`
	Body    yaml.Node `yaml:"body"`
}

type respondSpec struct {
	Status  *int      `yaml:"status"`
	Headers yaml.Node `yaml:"headers"`
	JSON    yaml.Node `yaml:"json"`
	Text    yaml.Node `yaml:"text"`
	File    string    `yaml:"file"`
	Delay   string    `yaml:"delay"`
}

type segment struct {
	lit   string
	param string
}

type valueMatch struct {
	name  string
	exact string
	re    *regexp.Regexp
	src   string
}

type Route struct {
	ID      string
	Method  string
	Path    string
	segs    []segment
	params  map[string]bool
	query   []valueMatch
	headers []valueMatch
	body    any
	hasBody bool
	status  int
	header  [][2]string
	json    any
	hasJSON bool
	text    string
	hasText bool
	file    []byte
	hasFile bool
	delay   time.Duration
}

type Routes struct {
	Path          string
	SHA256        string
	List          []*Route
	byID          map[string]*Route
	redactHeaders map[string]bool
	redactPaths   [][]string
	redactQuery   map[string]bool
}

func (rs *Routes) Has(id string) bool {
	_, ok := rs.byID[id]
	return ok
}

func Load(path string) (*Routes, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symbolic link; name the route file itself", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > MaxRouteFile {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, MaxRouteFile)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	doc, err := parseStrict(data)
	if err != nil {
		return nil, err
	}
	var spec fileSpec
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&spec); err != nil {
		return nil, &LoadError{Msg: explain(err, reflect.TypeOf(&spec))}
	}
	rs := &Routes{Path: path, SHA256: hex.EncodeToString(sum[:]), byID: map[string]*Route{}}
	if err := rs.build(&spec, doc, filepath.Dir(path)); err != nil {
		return nil, err
	}
	return rs, nil
}

func (rs *Routes) build(spec *fileSpec, doc *yaml.Node, dir string) error {
	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return errAt(top, "the route file must be a mapping with version and routes")
	}
	if spec.Version != 1 {
		return errAt(child(top, "version"), "version must be 1")
	}
	if err := rs.buildRedact(spec.Redact, child(top, "redact")); err != nil {
		return err
	}
	routesNode := child(top, "routes")
	if len(spec.Routes) == 0 {
		return errAt(orNode(routesNode, top), "routes must list at least one route")
	}
	seen := map[string]int{}
	firstID := map[string]string{}
	for i := range spec.Routes {
		n := item(routesNode, i)
		r, key, err := buildRoute(&spec.Routes[i], n, dir)
		if err != nil {
			return err
		}
		if line, dup := seen[r.ID]; dup {
			return errAt(orNode(child(n, "id"), n), "duplicate route id %q (first at line %d)", r.ID, line)
		}
		seen[r.ID] = n.Line
		if other, dup := firstID[key]; dup {
			return errAt(orNode(child(n, "match"), n), "route %s has the same match as route %s, which comes first and always wins; change one of them or remove one", r.ID, other)
		}
		firstID[key] = r.ID
		rs.List = append(rs.List, r)
		rs.byID[r.ID] = r
	}
	return nil
}

func (rs *Routes) buildRedact(r redactSpec, n *yaml.Node) error {
	rs.redactHeaders = map[string]bool{}
	for _, h := range builtinRedacted {
		rs.redactHeaders[h] = true
	}
	for i, h := range r.Headers {
		if !headerNameRE.MatchString(h) {
			return errAt(orNode(item(child(n, "headers"), i), n), "redact.headers: %q is not a header name", h)
		}
		rs.redactHeaders[strings.ToLower(h)] = true
	}
	for i, p := range r.JSONPaths {
		parts := strings.Split(p, ".")
		for _, part := range parts {
			if part == "" {
				return errAt(orNode(item(child(n, "json_paths"), i), n), "redact.json_paths: %q must be names joined by dots, such as login.credentials.password", p)
			}
		}
		rs.redactPaths = append(rs.redactPaths, parts)
	}
	rs.redactQuery = map[string]bool{}
	for i, q := range r.Query {
		if q == "" {
			return errAt(orNode(item(child(n, "query"), i), n), "redact.query: a name must not be empty")
		}
		rs.redactQuery[q] = true
	}
	return nil
}

func buildRoute(s *routeSpec, n *yaml.Node, dir string) (*Route, string, error) {
	r := &Route{ID: s.ID, params: map[string]bool{}}
	if !idRE.MatchString(s.ID) {
		return nil, "", errAt(orNode(child(n, "id"), n), "route id %q must match ^[a-z][a-z0-9_]{0,62}$", s.ID)
	}
	m := child(n, "match")
	if m == nil {
		return nil, "", errAt(n, "route %s has no match", s.ID)
	}
	if !contains(Methods, s.Match.Method) {
		return nil, "", errAt(orNode(child(m, "method"), m), "route %s: method must be one of %s", s.ID, strings.Join(Methods, ", "))
	}
	r.Method = s.Match.Method
	if err := r.buildPath(s.Match.Path, orNode(child(m, "path"), m)); err != nil {
		return nil, "", err
	}
	var err error
	if r.query, err = valueMatches(&s.Match.Query, "query", s.ID, false); err != nil {
		return nil, "", err
	}
	if r.headers, err = valueMatches(&s.Match.Headers, "headers", s.ID, true); err != nil {
		return nil, "", err
	}
	if s.Match.Body.Kind != 0 {
		v, err := nodeJSON(&s.Match.Body)
		if err != nil {
			return nil, "", err
		}
		r.body, r.hasBody = v, true
	}
	key, err := r.matchKey()
	if err != nil {
		return nil, "", errAt(m, "route %s: %v", s.ID, err)
	}
	if err := r.buildRespond(&s.Respond, child(n, "respond"), n, dir); err != nil {
		return nil, "", err
	}
	return r, key, nil
}

func (r *Route) buildPath(p string, n *yaml.Node) error {
	switch {
	case p == "":
		return errAt(n, "route %s: path is required", r.ID)
	case !strings.HasPrefix(p, "/"):
		return errAt(n, "route %s: path %q must start with /", r.ID, p)
	case strings.ContainsAny(p, "?#"):
		return errAt(n, "route %s: path %q can't hold ? or #; match the query with query", r.ID, p)
	}
	for _, c := range p {
		if c <= ' ' || c == 0x7f {
			return errAt(n, "route %s: path %q holds a space or a control character; escape it", r.ID, p)
		}
	}
	for i, raw := range strings.Split(p[1:], "/") {
		if strings.ContainsAny(raw, "{}") {
			g := paramRE.FindStringSubmatch(raw)
			if g == nil {
				return errAt(n, "route %s: path segment %q: a parameter is a whole segment written {name}", r.ID, raw)
			}
			if r.params[g[1]] {
				return errAt(n, "route %s: path names {%s} twice", r.ID, g[1])
			}
			r.params[g[1]] = true
			r.segs = append(r.segs, segment{param: g[1]})
			continue
		}
		lit, err := url.PathUnescape(raw)
		if err != nil {
			return errAt(n, "route %s: path segment %q is not a valid escaped segment: %v", r.ID, raw, err)
		}
		if i == 0 && lit == ControlRoot {
			return errAt(n, "route %s: path %q is under /%s/, which the sim keeps for its control API", r.ID, p, ControlRoot)
		}
		r.segs = append(r.segs, segment{lit: lit})
	}
	r.Path = p
	return nil
}

func valueMatches(n *yaml.Node, what, id string, header bool) ([]valueMatch, error) {
	if n.Kind == 0 {
		return nil, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, errAt(n, "route %s: match.%s must be a mapping of names to values", id, what)
	}
	var out []valueMatch
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		name := k.Value
		if name == "" {
			return nil, errAt(k, "route %s: match.%s: a name must not be empty", id, what)
		}
		if header {
			if !headerNameRE.MatchString(name) {
				return nil, errAt(k, "route %s: match.headers: %q is not a header name", id, name)
			}
			name = strings.ToLower(name)
			if seen[name] {
				return nil, errAt(k, "route %s: match.headers names %s twice (header names ignore case)", id, name)
			}
			seen[name] = true
		}
		vm := valueMatch{name: name}
		switch v.Kind {
		case yaml.ScalarNode:
			if v.ShortTag() == "!!null" {
				return nil, errAt(v, "route %s: match.%s.%s: give the exact text or { matches: <RE2> }", id, what, k.Value)
			}
			vm.exact = v.Value
		case yaml.MappingNode:
			if len(v.Content) != 2 || v.Content[0].Value != "matches" || v.Content[1].Kind != yaml.ScalarNode {
				return nil, errAt(v, "route %s: match.%s.%s: a mapping takes only matches: <RE2 pattern>", id, what, k.Value)
			}
			src := v.Content[1].Value
			re, err := regexp.Compile(`^(?:` + src + `)$`)
			if err != nil {
				return nil, errAt(v.Content[1], "route %s: match.%s.%s: invalid RE2 pattern: %v", id, what, k.Value, err)
			}
			vm.re, vm.src = re, src
		default:
			return nil, errAt(v, "route %s: match.%s.%s: give the exact text or { matches: <RE2> }", id, what, k.Value)
		}
		out = append(out, vm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

func (r *Route) matchKey() (string, error) {
	segs := make([]string, len(r.segs))
	for i, s := range r.segs {
		if s.param != "" {
			segs[i] = "{}"
		} else {
			segs[i] = "=" + s.lit
		}
	}
	vm := func(list []valueMatch) []any {
		out := make([]any, len(list))
		for i, v := range list {
			if v.re != nil {
				out[i] = []string{v.name, "re", v.src}
			} else {
				out[i] = []string{v.name, "exact", v.exact}
			}
		}
		return out
	}
	key := []any{r.Method, segs, vm(r.query), vm(r.headers), r.hasBody, r.body}
	b, err := json.Marshal(key)
	return string(b), err
}

func (r *Route) buildRespond(s *respondSpec, n, routeNode *yaml.Node, dir string) error {
	at := orNode(n, routeNode)
	r.status = 200
	if s.Status != nil {
		if *s.Status < 200 || *s.Status > 599 {
			return errAt(orNode(child(n, "status"), at), "route %s: status must be from 200 to 599", r.ID)
		}
		r.status = *s.Status
	}
	bodies := 0
	for _, set := range []bool{s.JSON.Kind != 0, s.Text.Kind != 0, s.File != ""} {
		if set {
			bodies++
		}
	}
	if bodies > 1 {
		return errAt(at, "route %s: respond takes at most one of json, text and file", r.ID)
	}
	if bodies == 1 && (r.status == 204 || r.status == 304) {
		return errAt(at, "route %s: a %d answer has no body; remove json, text or file", r.ID, r.status)
	}
	contentType := ""
	if s.Headers.Kind != 0 {
		if s.Headers.Kind != yaml.MappingNode {
			return errAt(&s.Headers, "route %s: respond.headers must be a mapping of names to text", r.ID)
		}
		seen := map[string]bool{}
		for i := 0; i+1 < len(s.Headers.Content); i += 2 {
			k, v := s.Headers.Content[i], s.Headers.Content[i+1]
			lower := strings.ToLower(k.Value)
			switch {
			case !headerNameRE.MatchString(k.Value):
				return errAt(k, "route %s: respond.headers: %q is not a header name", r.ID, k.Value)
			case seen[lower]:
				return errAt(k, "route %s: respond.headers names %s twice (header names ignore case)", r.ID, k.Value)
			case forbiddenResponseHeaders[lower]:
				return errAt(k, "route %s: respond.headers can't set %s; the sim sets it", r.ID, k.Value)
			case v.Kind != yaml.ScalarNode || v.ShortTag() == "!!null":
				return errAt(v, "route %s: respond.headers.%s must be text", r.ID, k.Value)
			case strings.ContainsAny(v.Value, "\r\n"):
				return errAt(v, "route %s: respond.headers.%s holds a line break", r.ID, k.Value)
			}
			if err := r.checkTemplates(v.Value, v); err != nil {
				return err
			}
			seen[lower] = true
			if lower == "content-type" {
				contentType = v.Value
			}
			r.header = append(r.header, [2]string{k.Value, v.Value})
		}
	}
	switch {
	case s.JSON.Kind != 0:
		v, err := nodeJSON(&s.JSON)
		if err != nil {
			return err
		}
		if err := r.checkValueTemplates(v, &s.JSON); err != nil {
			return err
		}
		r.json, r.hasJSON = v, true
	case s.Text.Kind != 0:
		if s.Text.Kind != yaml.ScalarNode || s.Text.ShortTag() == "!!null" {
			return errAt(&s.Text, "route %s: respond.text must be text", r.ID)
		}
		if err := r.checkTemplates(s.Text.Value, &s.Text); err != nil {
			return err
		}
		r.text, r.hasText = s.Text.Value, true
	case s.File != "":
		fn := orNode(child(n, "file"), at)
		if contentType == "" {
			return errAt(fn, "route %s: a file answer needs a Content-Type in respond.headers", r.ID)
		}
		b, err := readBodyFile(dir, s.File)
		if err != nil {
			return errAt(fn, "route %s: file %s: %v", r.ID, s.File, err)
		}
		r.file, r.hasFile = b, true
	}
	if s.Delay != "" {
		d, err := time.ParseDuration(s.Delay)
		if err != nil || d < MinDelay || d > MaxDelay {
			return errAt(orNode(child(n, "delay"), at), "route %s: delay %q must be a duration from 1ms to 10m, such as 200ms", r.ID, s.Delay)
		}
		r.delay = d
	}
	return nil
}

func readBodyFile(dir, name string) ([]byte, error) {
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) {
		return nil, errors.New("the path must be relative to the route file's folder")
	}
	parts := strings.Split(name, "/")
	for _, p := range parts {
		if p == ".." {
			return nil, errors.New("the path can't hold ..")
		}
		if p == "" || p == "." {
			return nil, errors.New("the path must be in clean form, without empty or . segments")
		}
	}
	at := dir
	for i, p := range parts {
		at = filepath.Join(at, p)
		info, err := os.Lstat(at)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s is a symbolic link", filepath.Join(parts[:i+1]...))
		}
		if i == len(parts)-1 {
			if !info.Mode().IsRegular() {
				return nil, errors.New("not a regular file")
			}
			if info.Size() > MaxFileBody {
				return nil, fmt.Errorf("larger than %d bytes", MaxFileBody)
			}
		}
	}
	b, err := os.ReadFile(at)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxFileBody {
		return nil, fmt.Errorf("larger than %d bytes", MaxFileBody)
	}
	return b, nil
}

func (r *Route) checkValueTemplates(v any, n *yaml.Node) error {
	switch x := v.(type) {
	case string:
		return r.checkTemplates(x, n)
	case map[string]any:
		for _, k := range sortedKeys(x) {
			if templateRE.MatchString(k) || strings.Contains(k, "{{") {
				return errAt(n, "route %s: respond.json key %q: templates go only in values", r.ID, k)
			}
			if err := r.checkValueTemplates(x[k], n); err != nil {
				return err
			}
		}
	case []any:
		for _, e := range x {
			if err := r.checkValueTemplates(e, n); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Route) checkTemplates(s string, n *yaml.Node) error {
	for _, m := range templateRE.FindAllStringSubmatch(s, -1) {
		name := m[1]
		switch {
		case name == "seq":
		case strings.HasPrefix(name, "path."):
			if !r.params[strings.TrimPrefix(name, "path.")] {
				return errAt(n, "route %s: template {{%s}}: the path has no {%s}", r.ID, name, strings.TrimPrefix(name, "path."))
			}
		case strings.HasPrefix(name, "query.") && len(name) > len("query."):
		default:
			return errAt(n, "route %s: unknown template {{%s}}; only {{path.<name>}}, {{query.<name>}} and {{seq}} are supported", r.ID, name)
		}
	}
	rest := templateRE.ReplaceAllString(s, "")
	if strings.Contains(rest, "{{") || strings.Contains(rest, "}}") {
		return errAt(n, "route %s: %q holds {{ or }} that isn't a template; only {{path.<name>}}, {{query.<name>}} and {{seq}} are supported", r.ID, s)
	}
	return nil
}

func nodeJSON(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		switch n.ShortTag() {
		case "!!null":
			return nil, nil
		case "!!bool":
			var b bool
			if err := n.Decode(&b); err != nil {
				return nil, errAt(n, "%v", err)
			}
			return b, nil
		case "!!int":
			var i int64
			if err := n.Decode(&i); err == nil {
				return json.Number(strconv.FormatInt(i, 10)), nil
			}
			var f float64
			if err := n.Decode(&f); err != nil {
				return nil, errAt(n, "%v", err)
			}
			return json.Number(strconv.FormatFloat(f, 'g', -1, 64)), nil
		case "!!float":
			var f float64
			if err := n.Decode(&f); err != nil {
				return nil, errAt(n, "%v", err)
			}
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, errAt(n, "%s is not a JSON number", n.Value)
			}
			return json.Number(strconv.FormatFloat(f, 'g', -1, 64)), nil
		default:
			return n.Value, nil
		}
	case yaml.MappingNode:
		out := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			v, err := nodeJSON(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			out[n.Content[i].Value] = v
		}
		return out, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := nodeJSON(c)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	return nil, errAt(n, "not a JSON value")
}

func child(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func item(n *yaml.Node, i int) *yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode || i >= len(n.Content) {
		return n
	}
	return n.Content[i]
}

func orNode(n, fallback *yaml.Node) *yaml.Node {
	if n != nil {
		return n
	}
	return fallback
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func parseStrict(data []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, &LoadError{Msg: "empty document"}
		}
		return nil, &LoadError{Msg: err.Error()}
	}
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case err == nil:
		return nil, &LoadError{Line: extra.Line, Msg: "more than one YAML document"}
	case !errors.Is(err, io.EOF):
		return nil, &LoadError{Msg: err.Error()}
	}
	if err := checkStrict(&doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, &LoadError{Msg: "empty document"}
	}
	return &doc, nil
}

func checkStrict(n *yaml.Node) error {
	if n.Anchor != "" {
		return &LoadError{Line: n.Line, Msg: fmt.Sprintf("anchor &%s is not allowed; write the repeated part out in full", n.Anchor)}
	}
	if n.Kind == yaml.AliasNode {
		return &LoadError{Line: n.Line, Msg: "alias is not allowed; write the repeated part out in full"}
	}
	if n.Style&yaml.TaggedStyle != 0 {
		return &LoadError{Line: n.Line, Msg: fmt.Sprintf("explicit tag %s is not allowed", n.Tag)}
	}
	if n.Kind == yaml.MappingNode {
		seen := make(map[string]int, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode {
				return &LoadError{Line: k.Line, Msg: "mapping keys must be scalars"}
			}
			if k.Tag == "!!merge" {
				return &LoadError{Line: k.Line, Msg: "merge key << is not allowed"}
			}
			if first, dup := seen[k.Value]; dup {
				return &LoadError{Line: k.Line, Msg: fmt.Sprintf("duplicate key %q (first at line %d)", k.Value, first)}
			}
			seen[k.Value] = k.Line
		}
	}
	for _, c := range n.Content {
		if err := checkStrict(c); err != nil {
			return err
		}
	}
	return nil
}

var (
	unknownField = regexp.MustCompile(`field (\S+) not found in type ([\w.\[\]*]+)`)
	badValue     = regexp.MustCompile("cannot unmarshal !!(\\w+)(?: `([^`]*)`)? into ([\\w.\\[\\]*]+)")
)

type shape struct {
	where map[string][]string
	keys  map[string][]string
	kinds map[string]string
}

func explain(err error, root reflect.Type) string {
	sc := shape{where: map[string][]string{}, keys: map[string][]string{}, kinds: map[string]string{}}
	sc.walk(root, "the top level", map[reflect.Type]bool{})
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "yaml: unmarshal errors:\n  ")
	msg = strings.ReplaceAll(msg, "\n  ", "; ")
	msg = unknownField.ReplaceAllStringFunc(msg, func(m string) string {
		g := unknownField.FindStringSubmatch(m)
		out := fmt.Sprintf("unknown key %q", g[1])
		if w := sc.where[g[2]]; len(w) > 0 {
			out += " in " + strings.Join(w, " or ")
		}
		if k := sc.keys[g[2]]; len(k) > 0 {
			out += " (allowed: " + strings.Join(k, ", ") + ")"
		}
		return out
	})
	msg = badValue.ReplaceAllStringFunc(msg, func(m string) string {
		g := badValue.FindStringSubmatch(m)
		want := sc.kinds[g[3]]
		if want == "" {
			want = "another kind of value"
		}
		got := map[string]string{"str": "text", "int": "a number", "float": "a number", "bool": "true or false", "seq": "a list", "map": "a mapping", "null": "null", "timestamp": "a date"}[g[1]]
		if got == "" {
			got = "!!" + g[1]
		}
		if g[2] != "" {
			got += " " + g[2]
		}
		return "expected " + want + ", got " + got
	})
	return msg
}

func (sc shape) walk(t reflect.Type, where string, seen map[reflect.Type]bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	name := t.String()
	if _, ok := sc.kinds[name]; !ok {
		sc.kinds[name] = kindName(t)
	}
	if t.Kind() == reflect.Struct && !contains(sc.where[name], where) {
		sc.where[name] = append(sc.where[name], where)
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		sc.walk(t.Elem(), where, seen)
	case reflect.Struct:
		if seen[t] {
			return
		}
		seen[t] = true
		if t.PkgPath() != "" && strings.HasPrefix(t.PkgPath(), "go.yaml.in/") {
			return
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
			if tag == "-" || !f.IsExported() {
				continue
			}
			if tag == "" {
				tag = strings.ToLower(f.Name)
			}
			sc.keys[name] = append(sc.keys[name], tag)
			sc.walk(f.Type, tag, seen)
		}
	}
}

func kindName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Struct, reflect.Map:
		return "a mapping"
	case reflect.Slice, reflect.Array:
		return "a list"
	case reflect.String:
		return "text"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "a whole number"
	case reflect.Float32, reflect.Float64:
		return "a number"
	}
	return "a value"
}
