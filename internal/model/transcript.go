package model

import (
	"fmt"
	"regexp"

	"go.yaml.in/yaml/v3"
)

const (
	TranscriptFormat       = "proctor-transcript/1"
	TranscriptFirstLine    = "format: \"" + TranscriptFormat + "\""
	TranscriptSuffix       = ".transcript.yaml"
	ApprovedDir            = "approved"
	DefaultTranscriptBytes = 1 << 20
	MaxTranscriptBytes     = MaxRequestFile
	MaxScrubRules          = 64
)

var ScrubName = regexp.MustCompile(`^[a-z_]+$`)

type Transcripts struct {
	MaxBytes int         `yaml:"max_bytes"`
	Scrub    []ScrubRule `yaml:"scrub"`
}

type ScrubRule struct {
	Name     string `yaml:"name" json:"name"`
	Pattern  string `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	Path     string `yaml:"path,omitempty" json:"path,omitempty"`
	Numbered *bool  `yaml:"numbered,omitempty" json:"numbered,omitempty"`
}

func (r ScrubRule) IsNumbered() bool {
	return r.Numbered == nil || *r.Numbered
}

func (t *Transcripts) Limit() int {
	if t == nil || t.MaxBytes <= 0 {
		return DefaultTranscriptBytes
	}
	return t.MaxBytes
}

func (t *Transcripts) Rules() []ScrubRule {
	if t == nil {
		return nil
	}
	return t.Scrub
}

type Approval struct {
	SpecID string
	Path   string
	Found  bool
	Data   []byte
	SHA256 string
}

func TranscriptOn(n *yaml.Node) (bool, error) {
	if n.Kind == 0 {
		return false, nil
	}
	if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!bool" {
		return false, fmt.Errorf("transcript must be true or false, not %s", nodeKind(n))
	}
	var on bool
	if err := n.Decode(&on); err != nil {
		return false, err
	}
	return on, nil
}

func (s *Scenario) TranscriptWanted() bool {
	on, err := TranscriptOn(&s.Transcript)
	return err == nil && on
}

func ApprovedPath(root, specID, scenarioID string) string {
	return root + "/" + ApprovedDir + "/" + specID + "/" + scenarioID + TranscriptSuffix
}
