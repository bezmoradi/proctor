package integrity

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/bezmoradi/proctor/protocol/schema"
	"proctor/internal/expr"
	"proctor/internal/model"
)

const factPathsRule = "EXE-11"

var factPathNeeds = map[string][]string{
	"keys":      {"object"},
	"length":    {"array"},
	"unordered": {"array"},
	"any":       {"array"},
	"none":      {"array"},
	"count":     {"array"},
	"contains":  {"string"},
	"matches":   {"string", "number"},
	"excludes":  {"string"},
}

var needText = map[string]string{"object": "an object", "array": "a list", "string": "text", "number": "a number"}

type factPaths struct {
	do       string
	problems []model.Problem
}

func FactPaths(do string, factSchema any, ex *model.Expect, save map[string]string) []model.Problem {
	if factSchema == nil {
		return nil
	}
	root, errs := schema.ShapeOf(factSchema)
	if len(errs) > 0 {
		return nil
	}
	f := &factPaths{do: do}
	if ex != nil {
		for _, k := range sortedKeys(ex.Equal) {
			f.check(root, "equal", k)
		}
		for _, k := range ex.Present {
			f.check(root, "present", k)
		}
		for _, k := range ex.Absent {
			f.check(root, "absent", k)
		}
		for _, k := range sortedKeys(ex.Keys) {
			f.check(root, "keys", k)
		}
		for _, k := range sortedKeys(ex.Length) {
			f.check(root, "length", k)
		}
		for _, k := range sortedKeys(ex.Unordered) {
			f.check(root, "unordered", k)
		}
		for _, k := range sortedKeys(ex.Contains) {
			f.check(root, "contains", k)
		}
		for _, k := range sortedKeys(ex.Matches) {
			f.check(root, "matches", k)
		}
		for _, k := range sortedKeys(ex.Excludes) {
			f.check(root, "excludes", k)
		}
		for _, k := range sortedKeys(ex.Differs) {
			f.check(root, "differs", k)
		}
		for _, k := range sortedKeys(ex.Any) {
			f.claims(root, "any", k, ex.Any[k].All())
		}
		for _, k := range sortedKeys(ex.None) {
			f.claims(root, "none", k, ex.None[k].All())
		}
		for _, k := range sortedKeys(ex.Counts) {
			var wheres []map[string]any
			for _, c := range ex.Counts[k].All() {
				wheres = append(wheres, c.Where)
			}
			f.claims(root, "count", k, wheres)
		}
	}
	for _, name := range sortedKeys(save) {
		p, err := expr.ParsePath(save[name])
		if err != nil {
			continue
		}
		if _, why := walkShape(root, p, "$"); why != "" {
			f.add("save."+name, "%s: the fact of %s can't hold this path (%s), so nothing could ever be saved", save[name], do, why)
		}
	}
	return f.problems
}

func (f *factPaths) add(path, format string, args ...any) {
	f.problems = append(f.problems, model.Problem{Rule: factPathsRule, Path: path, Message: fmt.Sprintf(format, args...)})
}

func (f *factPaths) check(root schema.Shape, matcher, target string) (schema.Shape, bool) {
	p, err := expr.ParsePath(target)
	if err != nil {
		return schema.Shape{}, false
	}
	at := "expect." + matcher
	sh, why := walkShape(root, p, "$")
	if why != "" {
		f.add(at, "%s: the fact of %s can't hold this path (%s), so %s %s", target, f.do, why, matcher, consequence(matcher))
		return schema.Shape{}, false
	}
	if matcher == "excludes" && !sh.Open() && !sh.Allows("string") && sh.Allows("array") {
		if item := sh.Item(); !item.Open() && !item.Allows("string") {
			f.add(at, "%s: excludes needs text or a list of texts, but the fact of %s holds a list of %s there", target, f.do, item.TypeText())
			return schema.Shape{}, false
		}
		return sh, true
	}
	if needs := factPathNeeds[matcher]; len(needs) > 0 && !sh.Open() && !allowsAny(sh, needs) {
		if matcher == "excludes" {
			f.add(at, "%s: excludes needs text or a list of texts, but the fact of %s holds %s there", target, f.do, sh.TypeText())
			return schema.Shape{}, false
		}
		f.add(at, "%s: %s needs %s, but the fact of %s holds %s there", target, matcher, needsText(needs), f.do, sh.TypeText())
		return schema.Shape{}, false
	}
	return sh, true
}

func allowsAny(sh schema.Shape, types []string) bool {
	for _, t := range types {
		if sh.Allows(t) {
			return true
		}
	}
	return false
}

func needsText(types []string) string {
	words := make([]string, 0, len(types))
	for _, t := range types {
		words = append(words, needText[t])
	}
	return strings.Join(words, " or ")
}

func (f *factPaths) claims(root schema.Shape, matcher, target string, claims []map[string]any) {
	list, ok := f.check(root, matcher, target)
	if !ok {
		return
	}
	elem := list.Item()
	for i, claim := range claims {
		label := ""
		if len(claims) > 1 {
			label = " claim[" + strconv.Itoa(i) + "]"
		}
		for _, sk := range sortedKeys(claim) {
			p, err := expr.ParseRelative(sk)
			if err != nil {
				continue
			}
			if _, why := walkShape(elem, p, target+"[*]"); why != "" {
				f.add("expect."+matcher, "%s%s: claim key %s: an element of %s can't hold it (%s), so the claim %s", target, label, sk, target, why, claimConsequence(matcher))
			}
		}
	}
}

func consequence(matcher string) string {
	if matcher == "absent" {
		return "would always pass"
	}
	return "would always fail"
}

func claimConsequence(matcher string) string {
	switch matcher {
	case "any":
		return "would never match and any would always fail"
	case "none":
		return "would never match and none could never fail"
	}
	return "would never match"
}

func walkShape(sh schema.Shape, p expr.Path, at string) (schema.Shape, string) {
	for _, seg := range p.Segments() {
		switch {
		case sh.Open():
			return sh, ""
		case sh.Never():
			return sh, at + " never holds a value"
		case seg.IsIndex:
			if !sh.Allows("array") {
				return sh, fmt.Sprintf("%s is %s, not a list", at, sh.TypeText())
			}
			sh = sh.Item()
			at = fmt.Sprintf("%s[%d]", at, seg.Index)
			continue
		case !sh.Allows("object"):
			return sh, fmt.Sprintf("%s is %s, so it has no field %s", at, sh.TypeText(), seg.Name)
		}
		next, ok := sh.Field(seg.Name)
		if !ok {
			has := "no fields"
			if names := sh.Fields(); len(names) > 0 {
				has = strings.Join(names, ", ")
			}
			return sh, fmt.Sprintf("%s has no field %s; it has %s", at, seg.Name, has)
		}
		sh = next
		at = expr.Child(at, seg.Name)
	}
	if sh.Never() {
		return sh, at + " never holds a value"
	}
	return sh, ""
}
