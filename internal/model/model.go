package model

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

var HandleName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type Config struct {
	Version         int                     `yaml:"version"`
	Requires        *Requirements           `yaml:"requires"`
	Project         string                  `yaml:"project"`
	Manifest        string                  `yaml:"manifest"`
	ProtectedOwners []string                `yaml:"protected_owners"`
	ProtectedPaths  []string                `yaml:"protected_paths"`
	Drivers         map[string]DriverConfig `yaml:"drivers"`
	Identities      map[string]IdentityType `yaml:"identities"`
	TimeLimits      *TimeLimits             `yaml:"time_limits"`
	Redaction       *Redaction              `yaml:"redaction"`
	Secrets         []string                `yaml:"secrets"`
	Report          *ReportPolicy           `yaml:"report"`
	AppLogs         *AppLogs                `yaml:"app_logs"`
	Mutation        *Mutation               `yaml:"mutation"`
	Dimensions      string                  `yaml:"dimensions"`
	Transcripts     *Transcripts            `yaml:"transcripts"`
	Relations       []Relation              `yaml:"relations"`
	IdentityCheck   *IdentityCheck          `yaml:"identity_check"`
	Invariants      []Step                  `yaml:"invariants"`

	files ProtectedFiles
}

const InvariantsPhase = "invariants"

func InvariantRef(i int) StepRef {
	return StepRef{Phase: InvariantsPhase, Index: i}
}

type Requirements struct {
	Proctor  string   `yaml:"proctor"`
	Features []string `yaml:"features"`
}

type IdentityCheck struct {
	Driver string `yaml:"driver"`
	Path   string `yaml:"path"`
	UserID string `yaml:"user_id"`
}

const IdentityCheckValue = "user_id"

func (c *Config) IdentityCheckDriver() (string, error) {
	ic := c.IdentityCheck
	if ic == nil {
		return "", nil
	}
	if ic.Driver != "" {
		d, ok := c.Drivers[ic.Driver]
		switch {
		case !ok:
			return "", fmt.Errorf("identity_check.driver %q isn't a driver in proctor.yaml", ic.Driver)
		case d.Official != "http":
			return "", fmt.Errorf("identity_check.driver %q has official %q; the identity check is an http request, so name a driver with official: http", ic.Driver, d.Official)
		}
		return ic.Driver, nil
	}
	var found []string
	for name, d := range c.Drivers {
		if d.Official == "http" {
			found = append(found, name)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("identity_check needs an http driver, and proctor.yaml declares none with official: http")
	case 1:
		return found[0], nil
	}
	slices.Sort(found)
	return "", fmt.Errorf("identity_check: proctor.yaml declares more than one http driver (%s); name the one to call with identity_check.driver", strings.Join(found, ", "))
}

type Relation struct {
	Act   RelationEnd   `yaml:"act"`
	Shows []RelationEnd `yaml:"shows"`
}

type RelationEnd struct {
	Driver string `yaml:"driver"`
	Target string `yaml:"target"`
}

func (e RelationEnd) String() string {
	return fmt.Sprintf("{ driver: %s, target: %q }", e.Driver, e.Target)
}

func (r Relation) Entry(show RelationEnd) string {
	return fmt.Sprintf("{ act: %s, shows: [%s] }", r.Act, show)
}

const MaxRequestFile = 8 << 20

type ProtectedFiles interface {
	ReadProtected(rel string) (path string, data []byte, err error)
}

func (c *Config) SetFiles(f ProtectedFiles) { c.files = f }

func (c *Config) Files() ProtectedFiles { return c.files }

type FileRef struct {
	Path   string
	Size   int64
	SHA256 string
}

type AppLogs struct {
	Format       string            `yaml:"format"`
	Levels       []string          `yaml:"levels"`
	LevelField   string            `yaml:"level_field"`
	MessageField string            `yaml:"message_field"`
	LevelNumbers map[string]string `yaml:"level_numbers"`
	Unparsed     string            `yaml:"unparsed"`
	Approved     *ApprovedLogs     `yaml:"approved"`
	FlushWait    string            `yaml:"flush_wait" json:"FlushWait,omitempty"`
}

const (
	DefaultFlushWaitFile = 300 * time.Millisecond
	DefaultFlushWaitOTel = 1500 * time.Millisecond
	MaxFlushWait         = 10 * time.Second
)

func (a *AppLogs) FlushWaitFor(otel bool) time.Duration {
	if a != nil && a.FlushWait != "" {
		if d, err := time.ParseDuration(a.FlushWait); err == nil && d > 0 && d <= MaxFlushWait {
			return d
		}
	}
	if otel {
		return DefaultFlushWaitOTel
	}
	return DefaultFlushWaitFile
}

const (
	AppLogFormatJSON     = "json"
	AppLogFormatLogfmt   = "logfmt"
	AppLogUnparsedStrict = "strict"
	AppLogUnparsedAllow  = "allow"
)

func (a *AppLogs) UnparsedAllowed() bool {
	return a != nil && a.Unparsed == AppLogUnparsedAllow
}

func (a *AppLogs) FormatName() string {
	if a == nil || a.Format == "" {
		return AppLogFormatJSON
	}
	return a.Format
}

func (a *AppLogs) NumberLevels() map[int64]string {
	if a == nil || len(a.LevelNumbers) == 0 {
		return nil
	}
	out := make(map[int64]string, len(a.LevelNumbers))
	for k, v := range a.LevelNumbers {
		if n, ok := LevelNumber(k); ok {
			out[n] = strings.ToUpper(v)
		}
	}
	return out
}

func LevelNumber(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || strconv.FormatInt(n, 10) != s {
		return 0, false
	}
	return n, true
}

var DefaultAppLogLevels = []string{"ERROR", "FATAL", "PANIC"}

func (a *AppLogs) EffectiveLevels() []string {
	if a == nil {
		return nil
	}
	src := a.Levels
	if src == nil {
		src = DefaultAppLogLevels
	}
	out := make([]string, 0, len(src))
	for _, l := range src {
		out = append(out, strings.ToUpper(l))
	}
	return out
}

func (a *AppLogs) LevelKey() string {
	if a == nil || a.LevelField == "" {
		return "level"
	}
	return a.LevelField
}

func (a *AppLogs) MessageKey() string {
	if a == nil || a.MessageField == "" {
		return "msg"
	}
	return a.MessageField
}

type DriverConfig struct {
	Official   string            `yaml:"official"`
	Program    string            `yaml:"program"`
	SHA256     map[string]string `yaml:"sha256"`
	Operations map[string]string `yaml:"operations"`
	Policy     any               `yaml:"policy"`
	PolicyFrom string            `yaml:"policy_from"`
}

func (d DriverConfig) Label() string {
	if d.Program != "" {
		return "program " + d.Program
	}
	if d.Official == "" {
		return "official none"
	}
	return "official " + d.Official
}

func (c *Config) PolicyOf(name string) (any, error) {
	d, ok := c.Drivers[name]
	if !ok {
		return nil, fmt.Errorf("driver %q isn't declared", name)
	}
	if d.PolicyFrom == "" {
		return d.Policy, nil
	}
	src, ok := c.Drivers[d.PolicyFrom]
	switch {
	case d.PolicyFrom == name:
		return nil, fmt.Errorf("driver %q: policy_from names the driver itself", name)
	case !ok:
		return nil, fmt.Errorf("driver %q: policy_from names %q, which isn't a driver in proctor.yaml", name, d.PolicyFrom)
	case src.Program == "" && d.Program == "" && src.Official != d.Official:
		return nil, fmt.Errorf("driver %q: policy_from names %q, whose official driver is %q, not %q; only drivers of the same official program can share a policy", name, d.PolicyFrom, src.Official, d.Official)
	case src.Program != d.Program || src.Official != d.Official:
		return nil, fmt.Errorf("driver %q: policy_from names %q, which runs %s, not %s; only drivers of the same official program or the same program can share a policy", name, d.PolicyFrom, src.Label(), d.Label())
	case src.PolicyFrom != "":
		return nil, fmt.Errorf("driver %q: policy_from names %q, which takes its own policy from %q; name the driver that holds the policy", name, d.PolicyFrom, src.PolicyFrom)
	case d.Policy != nil:
		return nil, fmt.Errorf("driver %q has both policy and policy_from; leave policy out, since it comes from %q", name, d.PolicyFrom)
	}
	return src.Policy, nil
}

func (c *Config) PolicyGroup(name string) string {
	if d, ok := c.Drivers[name]; ok && d.PolicyFrom != "" {
		return d.PolicyFrom
	}
	return name
}

type IdentityType struct {
	Type             string   `yaml:"type"`
	Variants         []string `yaml:"variants"`
	AcceptedVariants []string `yaml:"accepted_variants"`
}

type TimeLimits struct {
	MinInterval string `yaml:"min_interval"`
	MaxSamples  int    `yaml:"max_samples"`
	MaxWindow   string `yaml:"max_window"`
	MaxCall     string `yaml:"max_call"`
}

const DefaultMaxCall = 60 * time.Second

func (t *TimeLimits) MaxCallLimit() (time.Duration, error) {
	if t == nil || t.MaxCall == "" {
		return DefaultMaxCall, nil
	}
	d, err := time.ParseDuration(t.MaxCall)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("time_limits.max_call %q is not a positive duration", t.MaxCall)
	}
	return d, nil
}

type Redaction struct {
	JSONPaths []string `yaml:"json_paths"`
	Headers   []string `yaml:"headers"`
}

func (r *Redaction) HeaderNames() map[string]bool {
	out := map[string]bool{}
	if r == nil {
		return out
	}
	for _, h := range r.Headers {
		out[strings.ToLower(h)] = true
	}
	return out
}

type ReportPolicy struct {
	Formats      []string `yaml:"formats"`
	ExcerptBytes int      `yaml:"excerpt_bytes"`
}

type Manifest struct {
	Version        int             `yaml:"version"`
	Specifications []ManifestEntry `yaml:"specifications"`
}

type ManifestEntry struct {
	ID   string `yaml:"id"`
	Path string `yaml:"path"`
}

type Spec struct {
	Version   int        `yaml:"version"`
	Suite     string     `yaml:"suite"`
	Scenarios []Scenario `yaml:"scenarios"`
	Tables    []Table    `yaml:"tables"`
}

type Scenario struct {
	ID          string           `yaml:"id"`
	Name        string           `yaml:"name"`
	Setup       []Step           `yaml:"setup"`
	Steps       []Step           `yaml:"steps"`
	Cleanup     []Step           `yaml:"cleanup"`
	ExpectLogs  []LogExpectation `yaml:"expect_logs"`
	Requires    Requirement      `yaml:"requires,omitempty"`
	Transcript  yaml.Node        `yaml:"transcript,omitempty"`
	ApprovedLog yaml.Node        `yaml:"approved_log,omitempty"`

	Case        *TableCase `yaml:"-"`
	Approval    *Approval  `yaml:"-"`
	LogApproval *Approval  `yaml:"-"`
}

type LogExpectation struct {
	Level   string `yaml:"level" json:"level"`
	Message string `yaml:"message" json:"message"`
	Count   *int   `yaml:"count,omitempty" json:"count,omitempty"`
}

func (e LogExpectation) Key() string {
	return strings.ToUpper(e.Level) + "\x00" + e.Message
}

type Step struct {
	ID           string              `yaml:"id"`
	Do           string              `yaml:"do"`
	Identity     string              `yaml:"identity"`
	Variant      string              `yaml:"variant"`
	As           string              `yaml:"as"`
	On           string              `yaml:"on"`
	With         any                 `yaml:"with"`
	Expect       *Expect             `yaml:"expect"`
	Eventually   *Within             `yaml:"eventually"`
	Consistently *Window             `yaml:"consistently"`
	Never        *Window             `yaml:"never"`
	Timeout      string              `yaml:"timeout,omitempty"`
	Save         map[string]SaveFrom `yaml:"save"`
	NoStateCheck string              `yaml:"no_state_check"`
	Waive        map[string]Waiver   `yaml:"waive"`
	UnlessPassed yaml.Node           `yaml:"unless_passed,omitempty"`
}

func (s Step) HasUnlessPassed() bool {
	return s.UnlessPassed.Kind != 0
}

func (s Step) UnlessPassedName() string {
	if s.UnlessPassed.Kind != yaml.ScalarNode || s.UnlessPassed.ShortTag() == "!!null" {
		return ""
	}
	return s.UnlessPassed.Value
}

type Within struct {
	Within string `yaml:"within" json:"within"`
	Every  string `yaml:"every" json:"every,omitempty"`
}

type Window struct {
	For   string `yaml:"for" json:"for"`
	Every string `yaml:"every" json:"every,omitempty"`
}

type Expect struct {
	Equal     map[string]any                    `yaml:"equal" json:"equal,omitempty"`
	Present   []string                          `yaml:"present" json:"present,omitempty"`
	Absent    []string                          `yaml:"absent" json:"absent,omitempty"`
	Keys      map[string][]string               `yaml:"keys" json:"keys,omitempty"`
	Length    map[string]any                    `yaml:"length" json:"length,omitempty"`
	Unordered map[string][]any                  `yaml:"unordered" json:"unordered,omitempty"`
	Any       map[string]Claims[map[string]any] `yaml:"any" json:"any,omitempty"`
	None      map[string]Claims[map[string]any] `yaml:"none" json:"none,omitempty"`
	Contains  map[string]any                    `yaml:"contains" json:"contains,omitempty"`
	Counts    map[string]Claims[CountMatch]     `yaml:"count" json:"count,omitempty"`
	Matches   map[string]any                    `yaml:"matches" json:"matches,omitempty"`
	Excludes  map[string]any                    `yaml:"excludes" json:"excludes,omitempty"`
	Differs   map[string]any                    `yaml:"differs" json:"differs,omitempty"`
}

const MaxPatternBytes = 1 << 10

func MatchPattern(pattern string) (*regexp.Regexp, error) {
	switch {
	case len(pattern) > MaxPatternBytes:
		return nil, fmt.Errorf("the pattern is %d bytes, longer than %d", len(pattern), MaxPatternBytes)
	case strings.Contains(pattern, "${"):
		return nil, fmt.Errorf("pattern %q holds ${...}, which matches doesn't expand; write the pattern itself", pattern)
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return nil, fmt.Errorf("pattern %q isn't a valid RE2 regular expression: %v", pattern, err)
	}
	re, err := regexp.Compile(`\A(?:` + pattern + `)\z`)
	if err != nil {
		return nil, fmt.Errorf("pattern %q: %v", pattern, err)
	}
	if re.MatchString("") {
		return nil, fmt.Errorf("pattern %q matches empty text, so it would pass an empty value; require at least one character (for example + instead of *)", pattern)
	}
	return re, nil
}

func ExcludedTexts(v any) ([]string, bool, bool) {
	switch x := v.(type) {
	case string:
		return []string{x}, false, true
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			s, isText := e.(string)
			if !isText {
				return nil, true, false
			}
			out = append(out, s)
		}
		return out, true, true
	}
	return nil, false, false
}

type CountMatch struct {
	Where map[string]any `yaml:"where" json:"where"`
	Is    any            `yaml:"is" json:"is"`
}

func (c *CountMatch) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: expected a mapping with where and is, got %s", n.Line, nodeKind(n))}}
	}
	var bad []string
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := n.Content[i]; k.Value != "where" && k.Value != "is" {
			bad = append(bad, fmt.Sprintf("line %d: unknown key %q in count (allowed: where, is)", k.Line, k.Value))
		}
	}
	if len(bad) > 0 {
		return &yaml.TypeError{Errors: bad}
	}
	var raw struct {
		Where map[string]any `yaml:"where"`
		Is    any            `yaml:"is"`
	}
	if err := n.Decode(&raw); err != nil {
		return err
	}
	c.Where, c.Is = raw.Where, raw.Is
	return nil
}

type Claims[T any] struct {
	List  []T  `yaml:"-" json:"-"`
	Multi bool `yaml:"-" json:"-"`
}

func (c Claims[T]) All() []T {
	if !c.Multi && len(c.List) == 0 {
		var zero T
		return []T{zero}
	}
	return c.List
}

func (c *Claims[T]) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.MappingNode:
		var one T
		if err := n.Decode(&one); err != nil {
			return err
		}
		c.List, c.Multi = []T{one}, false
		return nil
	case yaml.SequenceNode:
		list := make([]T, 0, len(n.Content))
		for _, item := range n.Content {
			if item.Kind != yaml.MappingNode {
				return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: each claim in the list must be a mapping, got %s", item.Line, nodeKind(item))}}
			}
			var one T
			if err := item.Decode(&one); err != nil {
				return err
			}
			list = append(list, one)
		}
		c.List, c.Multi = list, true
		return nil
	}
	return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: expected a mapping (one claim) or a list of mappings (several claims), got %s", n.Line, nodeKind(n))}}
}

func (c Claims[T]) MarshalJSON() ([]byte, error) {
	if c.Multi {
		if c.List == nil {
			return []byte("[]"), nil
		}
		return json.Marshal(c.List)
	}
	return json.Marshal(c.All()[0])
}

func nodeKind(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "a mapping"
	case yaml.SequenceNode:
		return "a list"
	case yaml.ScalarNode:
		if n.ShortTag() == "!!null" {
			return "null"
		}
		return fmt.Sprintf("%q", n.Value)
	}
	return "another kind of value"
}

func (e *Expect) Count() int {
	if e == nil {
		return 0
	}
	n := len(e.Equal) + len(e.Present) + len(e.Absent) + len(e.Keys) + len(e.Length) + len(e.Unordered) + len(e.Contains) + len(e.Matches) + len(e.Differs)
	for _, v := range e.Excludes {
		if texts, _, ok := ExcludedTexts(v); ok {
			n += len(texts)
		} else {
			n++
		}
	}
	for _, c := range e.Any {
		n += len(c.All())
	}
	for _, c := range e.None {
		n += len(c.All())
	}
	for _, c := range e.Counts {
		n += len(c.All())
	}
	return n
}

type StepRef struct {
	Spec, Scenario, Phase string
	Index                 int
}

type StepInfo struct {
	Kind                     string
	Destructive, FixedTarget bool
	StateCheck               bool
	NoStateChange            bool
	OpensHandle, UsesHandle  bool
	HandleOptional           bool
	Targets, Shows           []string
	ExactTargets             []string
	Refs                     []string
	Data                     []string
	Problems                 []Problem
	Uses                     []string
	ShowsEntries             map[string]string
	Files                    map[string]FileRef
}

type PolicyEntry struct {
	Key, Rule, Use string
}

type Problem struct {
	Rule, Message, Path string
}

func SplitDo(do string) (driver, op string, ok bool) {
	driver, op, found := strings.Cut(do, ".")
	if !found || driver == "" || op == "" {
		return "", "", false
	}
	return driver, op, true
}
