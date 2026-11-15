package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"proctor/internal/expr"
	"proctor/internal/model"
)

func (r *Runner) httpStep(ctx context.Context, st model.Step, sr StepResult, vars expr.Vars) StepResult {
	errorf := func(cause, format string, args ...any) StepResult {
		sr.Result, sr.Cause = Error, cause
		sr.Message = fmt.Sprintf(format, args...)
		return sr
	}

	req, rec, err := r.build(ctx, st.Request, vars)
	if rec != nil {
		sr.Request = rec
	}
	if err != nil {
		return errorf(CauseInvalidRequest, "%v", err)
	}
	start := time.Now()
	resp, err := r.client.Do(req)
	sr.DurationMS = float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		if errors.Is(err, context.Canceled) {
			sr.Reason = ReasonAborted
		}
		return errorf(requestCause(err), "request failed: %v", err)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	resp.Body.Close()
	sr.Status = resp.StatusCode
	if err != nil {
		return errorf(readCause(err), "reading the response: %v", err)
	}
	if len(body) > MaxBodyBytes {
		return errorf(CauseResponseTooLarge, "response body is larger than %d bytes", MaxBodyBytes)
	}

	ex := st.Expect
	sr.Result = Pass
	add := func(a Assertion) {
		if a.Result != Pass {
			sr.Result = Fail
		}
		sr.Assertions = append(sr.Assertions, a)
	}

	a := Assertion{Kind: "status", Expected: *ex.Status, Actual: resp.StatusCode, Present: true, Result: Pass}
	if resp.StatusCode != *ex.Status {
		a.Result = Fail
		a.Message = fmt.Sprintf("expected status %d, got %d", *ex.Status, resp.StatusCode)
	}
	add(a)

	for _, name := range sortedKeys(ex.Headers) {
		want, err := vars.Text(ex.Headers[name], nil)
		if err != nil {
			return errorf(CauseInvalidRequest, "header %s: %v", name, err)
		}
		vals := resp.Header.Values(name)
		got := strings.Join(vals, ", ")
		a := Assertion{Kind: "header", Target: name, Expected: want, Actual: got, Present: len(vals) > 0, Result: Pass}
		switch {
		case len(vals) == 0:
			a.Result, a.Actual = Fail, nil
			a.Message = "header not present"
		case got != want:
			a.Result = Fail
			a.Message = "header value differs"
		}
		if r.redactH[strings.ToLower(name)] {
			a.Expected, a.Actual = redacted, redacted
		}
		add(a)
	}

	needsJSON := len(ex.JSON) > 0 || len(st.Save) > 0 || len(ex.Present) > 0 || len(ex.Absent) > 0 || len(ex.Length) > 0 || len(ex.Keys) > 0
	var doc any
	var docErr error
	if needsJSON {
		doc, docErr = decodeJSON(body)

		if docErr == nil {
			if k, dup := duplicateKey(body); dup {
				docErr = fmt.Errorf("duplicate object key %q", k)
			}
		}
		if docErr != nil {
			add(Assertion{Kind: "body", Result: Fail, Message: "response body is not a single valid JSON value: " + docErr.Error()})
		}
	}
	judge := func(kind, key string, f func(got any, ok bool) Assertion) {
		path, err := expr.ParsePath(key)
		if err != nil {
			add(Assertion{Kind: kind, Target: key, Result: Fail, Message: err.Error()})
			return
		}
		if docErr != nil {
			add(Assertion{Kind: kind, Target: key, Result: Fail, Message: "no JSON body to judge"})
			return
		}
		got, ok := path.Select(doc)
		a := f(got, ok)
		a.Kind, a.Target = kind, key
		if r.redactPath(key) {
			if a.Expected != nil {
				a.Expected = redacted
			}
			if a.Actual != nil {
				a.Actual = redacted
			}
		}
		add(a)
	}

	for _, key := range sortedKeys(ex.JSON) {
		want, err := vars.Value(ex.JSON[key])
		if err != nil {
			return errorf(CauseInvalidRequest, "%s: %v", key, err)
		}
		judge("json", key, func(got any, ok bool) Assertion {
			a := Assertion{Expected: want, Result: Pass}
			switch {
			case !ok:
				a.Result, a.Message = Fail, "not present in the response"
			default:
				a.Present, a.Actual = true, got
				if !expr.Equal(want, got) {
					a.Result, a.Message = Fail, "value differs"
				}
			}
			return a
		})
	}
	for _, key := range ex.Present {
		judge("present", key, func(got any, ok bool) Assertion {
			if !ok {
				return Assertion{Result: Fail, Message: "not present in the response"}
			}
			return Assertion{Present: true, Result: Pass}
		})
	}
	for _, key := range ex.Absent {
		judge("absent", key, func(got any, ok bool) Assertion {
			if ok {
				return Assertion{Present: true, Actual: got, Result: Fail, Message: "present in the response, expected absent"}
			}
			return Assertion{Result: Pass}
		})
	}
	for _, h := range ex.AbsentHeaders {
		a := Assertion{Kind: "absent_header", Target: h, Result: Pass}
		if vals := resp.Header.Values(h); len(vals) > 0 {
			a.Present, a.Actual, a.Result, a.Message = true, strings.Join(vals, ", "), Fail, "header present, expected absent"
			if r.redactH[strings.ToLower(h)] {
				a.Actual = redacted
			}
		}
		add(a)
	}
	for _, key := range sortedKeys(ex.Length) {
		want, err := lengthValue(ex.Length[key], vars)
		if err != nil {
			return errorf(CauseInvalidRequest, "length of %s: %v", key, err)
		}
		judge("length", key, func(got any, ok bool) Assertion {
			a := Assertion{Expected: want, Result: Pass}
			arr, isArr := got.([]any)
			switch {
			case !ok:
				a.Result, a.Message = Fail, "not present in the response"
			case !isArr:
				a.Present, a.Result, a.Message = true, Fail, "not an array"
			default:
				a.Present, a.Actual = true, len(arr)
				if len(arr) != want {
					a.Result, a.Message = Fail, fmt.Sprintf("expected %d elements, got %d", want, len(arr))
				}
			}
			return a
		})
	}

	for _, key := range sortedKeys(ex.Keys) {
		want := append([]string(nil), ex.Keys[key]...)
		sort.Strings(want)
		judge("keys", key, func(got any, ok bool) Assertion {
			a := Assertion{Expected: want, Result: Pass}
			obj, isObj := got.(map[string]any)
			switch {
			case !ok:
				a.Result, a.Message = Fail, "not present in the response"
			case !isObj:
				a.Present, a.Result, a.Message = true, Fail, "not an object"
			default:
				have := sortedKeys(obj)
				a.Present, a.Actual = true, have
				var extra, missing []string
				for _, k := range have {
					if !contains(want, k) {
						extra = append(extra, k)
					}
				}
				for _, k := range want {
					if _, ok := obj[k]; !ok {
						missing = append(missing, k)
					}
				}
				if len(extra)+len(missing) > 0 {
					a.Result = Fail
					a.Message = fmt.Sprintf("unexpected keys %v, missing keys %v", extra, missing)
				}
			}
			return a
		})
	}

	if sr.Result != Pass {
		sr.Cause = CauseUnexpectedResponse
		return sr
	}
	for _, name := range sortedKeys(st.Save) {
		path, err := expr.ParsePath(st.Save[name])
		if err != nil {
			return errorf(CauseInvalidRequest, "%v", err)
		}
		a := Assertion{Kind: "save", Target: name + " = " + path.String(), Result: Pass}
		if got, ok := path.Select(doc); !ok {
			a.Result, a.Message = Fail, "nothing to save: not present in the response"
		} else {
			a.Present = true
			vars[name] = got
			sr.Saved = append(sr.Saved, name)
		}
		add(a)
	}
	if sr.Result != Pass {
		sr.Cause = CauseUnexpectedResponse
	}
	return sr
}

func lengthValue(v any, vars expr.Vars) (int, error) {
	x, err := vars.Value(v)
	if err != nil {
		return 0, err
	}
	switch n := x.(type) {
	case int:
		return n, nil
	case json.Number:
		i, err := strconv.Atoi(n.String())
		if err != nil || i < 0 {
			return 0, fmt.Errorf("saved value %s is not a non-negative whole number", n)
		}
		return i, nil
	}
	return 0, fmt.Errorf("saved value %s is not a number", expr.ToText(x))
}

func requestCause(err error) string {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return CauseTimeout
	}
	return CauseUnreachable
}

func readCause(err error) string {
	if requestCause(err) == CauseTimeout {
		return CauseTimeout
	}
	return CauseUnreadableResponse
}

func (r *Runner) build(ctx context.Context, rq *model.Request, vars expr.Vars) (*http.Request, *Request, error) {
	rec := &Request{Method: rq.Method, Identity: rq.Identity, Credential: rq.Credential}

	p, err := vars.Text(rq.Path, url.PathEscape)
	if err != nil {
		return nil, rec, err
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "." || seg == ".." {
			return nil, rec, fmt.Errorf("path %q has a . or .. segment after inserting saved values (HTTP-05)", p)
		}
	}
	q := url.Values{}
	for _, k := range sortedKeys(rq.Query) {
		key, err := vars.Text(k, nil)
		if err != nil {
			return nil, rec, err
		}
		val, err := vars.Text(rq.Query[k], nil)
		if err != nil {
			return nil, rec, err
		}
		q.Set(key, val)
	}
	u := r.base.Scheme + "://" + r.base.Host + strings.TrimSuffix(r.base.EscapedPath(), "/") + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	rec.URL = u

	var body io.Reader
	if rq.Body != nil {
		v, err := vars.Value(rq.Body)
		if err != nil {
			return nil, rec, err
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, rec, fmt.Errorf("body: %v", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, rq.Method, u, body)
	if err != nil {
		return nil, rec, err
	}
	if req.URL.Scheme != r.base.Scheme || req.URL.Host != r.base.Host {
		return nil, rec, fmt.Errorf("request URL %s leaves the target (HTTP-05)", u)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, name := range sortedKeys(rq.Headers) {
		val, err := vars.Text(rq.Headers[name], nil)
		if err != nil {
			return nil, rec, err
		}
		if strings.ContainsAny(val, "\r\n") {
			return nil, rec, fmt.Errorf("header %s contains a line break after inserting saved values", name)
		}
		req.Header.Set(name, val)
	}
	req.Header.Set("User-Agent", r.ua)
	if auth, ok := r.tokens[credKey(rq.Identity, rq.Credential)]; ok {
		req.Header.Set("Authorization", auth)
	}
	return req, rec, nil
}

func (r *Runner) redactPath(p string) bool {
	if r.redactJS[p] {
		return true
	}
	return sensitiveName(p[strings.LastIndexAny(p, ".[")+1:])
}

func sensitiveName(name string) bool {
	last := strings.ToLower(name)
	for _, w := range []string{"password", "secret", "token", "api_key"} {
		if strings.Contains(last, w) {
			return true
		}
	}
	return false
}

func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after the JSON value")
	}
	return v, nil
}

func duplicateKey(b []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	type frame struct {
		obj    bool
		seen   map[string]bool
		expKey bool
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", false
		}
		top := (*frame)(nil)
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if top != nil && top.obj && top.expKey {
			if d, ok := tok.(json.Delim); ok && d == '}' {
				stack = stack[:len(stack)-1]
				if len(stack) > 0 && stack[len(stack)-1].obj {
					stack[len(stack)-1].expKey = true
				}
				continue
			}
			k := tok.(string)
			if top.seen[k] {
				return k, true
			}
			top.seen[k] = true
			top.expKey = false
			continue
		}
		switch d := tok.(type) {
		case json.Delim:
			switch d {
			case '{':
				stack = append(stack, &frame{obj: true, seen: map[string]bool{}, expKey: true})
				continue
			case '[':
				stack = append(stack, &frame{})
				continue
			case ']':
				stack = stack[:len(stack)-1]
			}
		}

		if len(stack) > 0 && stack[len(stack)-1].obj {
			stack[len(stack)-1].expKey = true
		}
		if len(stack) == 0 && !dec.More() {
			return "", false
		}
	}
}
