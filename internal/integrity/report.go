package integrity

import (
	"encoding/json"
	"fmt"

	"proctor/internal/model"
)

type Kind int

const (
	Invalid   Kind = 3
	Violation Kind = 4
)

func (k Kind) String() string {
	switch k {
	case Violation:
		return "integrity_violation"
	}
	return "invalid"
}

type Finding struct {
	Kind    Kind   `json:"kind"`
	Rule    string `json:"rule"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

type SpecFile struct {
	Path string
	Spec *model.Spec
}

func (s SpecFile) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.Path)
}

type Report struct {
	Commit        string            `json:"commit,omitempty"`
	ConfigPath    string            `json:"config"`
	ProtectedRoot string            `json:"protected_root"`
	Specs         []SpecFile        `json:"specifications"`
	ScenarioIDs   []string          `json:"scenario_ids"`
	Unlisted      []string          `json:"unlisted"`
	Hashes        map[string]string `json:"sha256"`
	Findings      []Finding         `json:"findings"`

	Config      *model.Config   `json:"-"`
	Manifest    *model.Manifest `json:"-"`
	Mutants     []model.Mutant  `json:"-"`
	MutantsPath string          `json:"mutants,omitempty"`

	DimensionsPath string `json:"dimensions,omitempty"`

	SpecIDs map[string]string `json:"-"`

	ProtectedPathHashes map[string]string `json:"-"`

	listed map[string]bool
	limits limits
}

func (r *Report) ExitCode() int {
	code := 0
	for _, f := range r.Findings {
		if int(f.Kind) > code {
			code = int(f.Kind)
		}
	}
	return code
}

func (r *Report) Has(k Kind) bool {
	for _, f := range r.Findings {
		if f.Kind == k {
			return true
		}
	}
	return false
}

func (r *Report) add(k Kind, rule, p, format string, args ...any) {
	r.Findings = append(r.Findings, Finding{Kind: k, Rule: rule, Path: p, Message: fmt.Sprintf(format, args...)})
}

func (r *Report) quietly(quiet bool, f func()) {
	mark := len(r.Findings)
	f()
	if quiet {
		r.Findings = r.Findings[:mark]
	}
}
