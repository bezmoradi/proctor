package model

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

var HandleName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type Config struct {
	Version         int                     `yaml:"version"`
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
}

type AppLogs struct {
	Levels       []string `yaml:"levels"`
	LevelField   string   `yaml:"level_field"`
	MessageField string   `yaml:"message_field"`
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
	Official string `yaml:"official"`
	Policy   any    `yaml:"policy"`
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
}

type Redaction struct {
	JSONPaths []string `yaml:"json_paths"`
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
	ID         string           `yaml:"id"`
	Name       string           `yaml:"name"`
	Setup      []Step           `yaml:"setup"`
	Steps      []Step           `yaml:"steps"`
	ExpectLogs []LogExpectation `yaml:"expect_logs"`

	Case *TableCase `yaml:"-"`
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
	ID           string            `yaml:"id"`
	Do           string            `yaml:"do"`
	Identity     string            `yaml:"identity"`
	Variant      string            `yaml:"variant"`
	As           string            `yaml:"as"`
	On           string            `yaml:"on"`
	With         any               `yaml:"with"`
	Expect       *Expect           `yaml:"expect"`
	Eventually   *Within           `yaml:"eventually"`
	Consistently *Window           `yaml:"consistently"`
	Never        *Window           `yaml:"never"`
	Save         map[string]string `yaml:"save"`
	NoStateCheck string            `yaml:"no_state_check"`
	Waive        map[string]string `yaml:"waive"`
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
	n := len(e.Equal) + len(e.Present) + len(e.Absent) + len(e.Keys) + len(e.Length) + len(e.Unordered) + len(e.Contains)
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
	OpensHandle, UsesHandle  bool
	Refs                     []string
	Data                     []string
	Problems                 []Problem
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
