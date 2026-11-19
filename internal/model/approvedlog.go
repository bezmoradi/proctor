package model

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

const (
	ApprovedLogFormat       = "proctor-log/1"
	ApprovedLogFirstLine    = "format: \"" + ApprovedLogFormat + "\""
	ApprovedLogTrailer      = "unparsed lines: "
	ApprovedLogSuffix       = ".log"
	ApprovedLogOrderWritten = "written"
	ApprovedLogOrderSorted  = "sorted"
	DefaultApprovedLogBytes = 1 << 20
	MaxApprovedLogBytes     = MaxRequestFile
	MaxApprovedLogFields    = 64
)

type ApprovedLogs struct {
	Fields   []string       `yaml:"fields" json:"fields"`
	Order    string         `yaml:"order" json:"order,omitempty"`
	Scrub    []LogScrubRule `yaml:"scrub" json:"scrub,omitempty"`
	MaxBytes int            `yaml:"max_bytes" json:"max_bytes,omitempty"`
}

type LogScrubRule struct {
	Name     string `yaml:"name" json:"name"`
	Pattern  string `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	Field    string `yaml:"field,omitempty" json:"field,omitempty"`
	Numbered *bool  `yaml:"numbered,omitempty" json:"numbered,omitempty"`
}

func (r LogScrubRule) IsNumbered() bool {
	return r.Numbered == nil || *r.Numbered
}

func (a *ApprovedLogs) Limit() int {
	if a == nil || a.MaxBytes <= 0 {
		return DefaultApprovedLogBytes
	}
	return a.MaxBytes
}

func (a *ApprovedLogs) Sorted() bool {
	return a != nil && a.Order == ApprovedLogOrderSorted
}

func (a *ApprovedLogs) OrderName() string {
	if a == nil || a.Order == "" {
		return ApprovedLogOrderWritten
	}
	return a.Order
}

func (a *AppLogs) ApprovedSettings() *ApprovedLogs {
	if a == nil {
		return nil
	}
	return a.Approved
}

func FlagOn(n *yaml.Node, key string) (bool, error) {
	if n.Kind == 0 {
		return false, nil
	}
	if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!bool" {
		return false, fmt.Errorf("%s must be true or false, not %s", key, nodeKind(n))
	}
	var on bool
	if err := n.Decode(&on); err != nil {
		return false, err
	}
	return on, nil
}

func (s *Scenario) ApprovedLogWanted() bool {
	on, err := FlagOn(&s.ApprovedLog, "approved_log")
	return err == nil && on
}

func (s *Scenario) ApprovedLogOwner() string {
	if s.Case != nil {
		return s.Case.Table
	}
	return s.ID
}

func ApprovedLogPath(root, specID, owner string) string {
	return root + "/" + ApprovedDir + "/" + specID + "/" + owner + ApprovedLogSuffix
}
