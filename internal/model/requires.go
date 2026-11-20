package model

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

type Requirement string

const RequiresDisposableEnvironment Requirement = "disposable_environment"

func (r *Requirement) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!str" || Requirement(n.Value) != RequiresDisposableEnvironment {
		what := nodeKind(n)
		if n.Kind == yaml.ScalarNode {
			what = fmt.Sprintf("%q", n.Value)
		}
		return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: requires must be %s, not %s", n.Line, RequiresDisposableEnvironment, what)}}
	}
	*r = RequiresDisposableEnvironment
	return nil
}
