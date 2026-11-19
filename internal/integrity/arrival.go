package integrity

import (
	"encoding/json"
	"fmt"
	"strconv"

	"proctor/internal/model"
)

const ArrivalRule = "TIME-14"

func ArrivalProblem(do string, st *model.Step) (model.Problem, bool) {
	if st.Eventually == nil || st.Expect == nil || st.Expect.Count() == 0 || waitsForArrival(st.Expect) {
		return model.Problem{}, false
	}
	return model.Problem{Rule: ArrivalRule, Path: "eventually", Message: fmt.Sprintf("%s reports what has arrived so far, and what has arrived only grows, so expectations that only say what must not be there (none, absent, excludes, a count or length of 0, equal to false, 0 or an empty value, or differs from anything else) all hold at the first sample, before anything arrives, and eventually passes there; watch for it over a window with consistently or never instead, or add an expectation that holds only once something has arrived (any, a count or length above 0, present, contains, matches, differs from false, 0 or an empty value, or equal $.closed: true)", do)}, true
}

func waitsForArrival(ex *model.Expect) bool {
	for _, v := range ex.Equal {
		if !emptyValue(v) {
			return true
		}
	}
	if len(ex.Present) > 0 || len(ex.Keys) > 0 || len(ex.Any) > 0 || len(ex.Matches) > 0 {
		return true
	}
	for _, v := range ex.Differs {
		if emptyValue(v) {
			return true
		}
	}
	for _, v := range ex.Contains {
		if !emptyValue(v) {
			return true
		}
	}
	for _, v := range ex.Length {
		if !emptyValue(v) {
			return true
		}
	}
	for _, l := range ex.Unordered {
		if len(l) > 0 {
			return true
		}
	}
	for _, c := range ex.Counts {
		for _, cm := range c.All() {
			if !emptyValue(cm.Is) {
				return true
			}
		}
	}
	return false
}

func emptyValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case string:
		return x == ""
	case int:
		return x == 0
	case int64:
		return x == 0
	case uint64:
		return x == 0
	case float64:
		return x == 0
	case json.Number:
		f, err := strconv.ParseFloat(x.String(), 64)
		return err == nil && f == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}
