package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"proctor/internal/expr"
	"proctor/internal/model"
	"proctor/internal/postgres"
)

// pgStep runs one database check (§11.7). Connection failures and SQL
// errors are ERROR (DB-05); only a result that differs from the expectation
// is FAIL.
func (r *Runner) pgStep(ctx context.Context, st model.Step, sr StepResult, vars expr.Vars) StepResult {
	pc := st.Verify.Postgres
	sr.Check = &Check{Connection: pc.Connection, Query: strings.TrimSpace(pc.Query)}
	errorf := func(cause, format string, args ...any) StepResult {
		sr.Result, sr.Cause = Error, cause
		sr.Message = fmt.Sprintf(format, args...)
		return sr
	}
	args := make([]any, len(pc.Args))
	for i, a := range pc.Args {
		v, err := vars.Value(a)
		if err != nil {
			return errorf(CauseInvalidRequest, "args[%d]: %v", i, err)
		}
		args[i] = argValue(v)
	}
	conn := r.pg[pc.Connection]
	if conn == nil {
		return errorf(CauseUnreachable, "connection %s is not open", pc.Connection)
	}
	start := time.Now()
	cols, rows, err := conn.Query(ctx, pc.Query, args, r.queryTimeout)
	sr.DurationMS = float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		cause := CauseUnreachable
		var pe *postgres.Error
		if errors.As(err, &pe) {
			cause = string(pe.Class)
		}
		if ctx.Err() != nil {
			sr.Reason = ReasonAborted
		}
		return errorf(cause, "%v", err)
	}
	seen := map[string]bool{}
	for _, c := range cols {
		if seen[c] {
			return errorf(string(postgres.BadResult), "the query returns two columns named %q; give each column a distinct name", c)
		}
		seen[c] = true
	}

	sr.Result = Pass
	add := func(a Assertion) {
		if a.Result != Pass {
			sr.Result = Fail
		}
		sr.Assertions = append(sr.Assertions, a)
	}
	ex := pc.Expect
	if ex.Rows != nil {
		a := Assertion{Kind: "rows", Expected: *ex.Rows, Actual: len(rows), Present: true, Result: Pass}
		if len(rows) != *ex.Rows {
			a.Result, a.Message = Fail, fmt.Sprintf("expected %d rows, got %d", *ex.Rows, len(rows))
		}
		add(a)
	}
	if ex.FirstRow != nil {
		if len(rows) == 0 {
			add(Assertion{Kind: "first_row", Result: Fail, Message: "the query returned no rows"})
		} else {
			first := map[string]any{}
			for i, c := range cols {
				first[c] = rows[0][i]
			}
			for _, c := range cols {
				if _, ok := ex.FirstRow[c]; !ok {
					a := Assertion{Kind: "first_row", Target: c, Present: true, Actual: first[c], Result: Fail,
						Message: "the query returns this column but the expectation doesn't name it (first_row must name every column)"}
					if sensitiveName(c) {
						a.Actual = redacted
					}
					add(a)
				}
			}
			for _, c := range sortedKeys(ex.FirstRow) {
				want, err := vars.Value(ex.FirstRow[c])
				if err != nil {
					return errorf(CauseInvalidRequest, "first_row.%s: %v", c, err)
				}
				a := Assertion{Kind: "first_row", Target: c, Expected: want, Result: Pass}
				got, ok := first[c]
				switch {
				case !ok:
					a.Result, a.Message = Fail, "the query doesn't return this column"
				default:
					a.Present, a.Actual = true, got
					if !expr.Equal(want, got) {
						a.Result, a.Message = Fail, "value differs"
					}
				}
				if sensitiveName(c) {
					a.Expected, a.Actual = redacted, redacted
				}
				add(a)
			}
		}
	}
	if sr.Result == Pass && len(st.Save) > 0 {
		for _, name := range sortedKeys(st.Save) {
			col := st.Save[name]
			a := Assertion{Kind: "save", Target: name + " = column " + col, Result: Pass}
			idx := -1
			for i, c := range cols {
				if c == col {
					idx = i
				}
			}
			if idx < 0 || len(rows) == 0 {
				a.Result, a.Message = Fail, "nothing to save: the query doesn't return this column"
			} else {
				a.Present = true
				vars[name] = rows[0][idx]
				sr.Saved = append(sr.Saved, name)
			}
			add(a)
		}
	}
	if sr.Result != Pass {
		sr.Cause = CauseUnexpectedResponse
	}
	return sr
}

// argValue turns a decoded value into a driver argument. Strings are sent as
// text and PostgreSQL converts them to the parameter's type, so an ID saved
// from JSON as "7" matches an integer column; numbers keep their exact
// spelling.
func argValue(v any) any {
	switch t := v.(type) {
	case nil, string, bool:
		return t
	}
	return expr.ToText(v)
}
