package schema

import (
	"slices"
	"strings"
)

type Shape struct {
	n *node
}

func ShapeOf(schema any) (Shape, []string) {
	n, errs := compileAny(schema)
	if len(errs) > 0 {
		return Shape{}, errs
	}
	return Shape{n: n}, nil
}

func (s Shape) Open() bool {
	n := s.n
	switch {
	case n == nil:
		return true
	case n.always != nil:
		return *n.always
	}
	return len(n.types) == 0 && n.props == nil && n.additional == nil && n.items == nil && n.enum == nil
}

func (s Shape) Never() bool {
	return s.n != nil && s.n.always != nil && !*s.n.always
}

func (s Shape) Allows(t string) bool {
	switch {
	case s.Never():
		return false
	case s.n == nil || s.n.always != nil || len(s.n.types) == 0:
		return true
	case t == "number" && slices.Contains(s.n.types, "integer"):
		return true
	}
	return slices.Contains(s.n.types, t)
}

func (s Shape) TypeText() string {
	switch {
	case s.Never():
		return "nothing"
	case s.n == nil || s.n.always != nil || len(s.n.types) == 0:
		return "any value"
	}
	return strings.Join(s.n.types, " or ")
}

func (s Shape) Field(name string) (Shape, bool) {
	if s.n == nil || s.n.always != nil {
		return s, !s.Never()
	}
	if p, ok := s.n.props[name]; ok {
		return Shape{n: p}, true
	}
	if s.n.additional == nil {
		return Shape{}, true
	}
	if s.n.additional.always != nil && !*s.n.additional.always {
		return Shape{}, false
	}
	return Shape{n: s.n.additional}, true
}

func (s Shape) Fields() []string {
	if s.n == nil {
		return nil
	}
	return sortedKeys(s.n.props)
}

func (s Shape) Item() Shape {
	if s.n == nil || s.n.always != nil {
		return s
	}
	if s.n.items == nil {
		return Shape{}
	}
	return Shape{n: s.n.items}
}
