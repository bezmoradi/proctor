package model

import "strings"

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
	ID    string `yaml:"id"`
	Name  string `yaml:"name"`
	Setup []Step `yaml:"setup"`
	Steps []Step `yaml:"steps"`

	Case *TableCase `yaml:"-"`
}

type Step struct {
	ID           string            `yaml:"id"`
	Do           string            `yaml:"do"`
	Identity     string            `yaml:"identity"`
	Variant      string            `yaml:"variant"`
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
	Equal     map[string]any            `yaml:"equal" json:"equal,omitempty"`
	Present   []string                  `yaml:"present" json:"present,omitempty"`
	Absent    []string                  `yaml:"absent" json:"absent,omitempty"`
	Keys      map[string][]string       `yaml:"keys" json:"keys,omitempty"`
	Length    map[string]any            `yaml:"length" json:"length,omitempty"`
	Unordered map[string][]any          `yaml:"unordered" json:"unordered,omitempty"`
	Any       map[string]map[string]any `yaml:"any" json:"any,omitempty"`
	None      map[string]map[string]any `yaml:"none" json:"none,omitempty"`
}

func (e *Expect) Count() int {
	if e == nil {
		return 0
	}
	return len(e.Equal) + len(e.Present) + len(e.Absent) + len(e.Keys) + len(e.Length) + len(e.Unordered) + len(e.Any) + len(e.None)
}

type StepRef struct {
	Spec, Scenario, Phase string
	Index                 int
}

type StepInfo struct {
	Kind                     string
	Destructive, FixedTarget bool
	Refs                     []string
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
