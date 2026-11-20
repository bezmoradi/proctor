package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bezmoradi/proctor/protocol"
)

const (
	readsRule      = "HTTP-11"
	unusedRule     = "HTTP-12"
	readsKey       = "reads"
	showsKey       = "shows"
	destructiveKey = "destructive"
	actsKey        = "acts"
	maxBodyKey     = "max_body_bytes"
	readMethod     = "POST"
	showsMethod    = "GET"
	anyValue       = "{}"
)

type readRequest struct {
	method string
	path   string
}

func (r readRequest) String() string {
	return r.method + " " + r.path
}

type policy struct {
	reads       map[readRequest][]string
	shows       map[string][]string
	destructive map[readRequest]bool
	maxBody     int64
}

func (p policy) bodyLimit() int64 {
	if p.maxBody > 0 {
		return p.maxBody
	}
	return defaultMaxBody
}

func readEntry(r readRequest) string {
	return "read " + r.String()
}

func destructiveEntry(r readRequest) string {
	return "destructive " + r.String()
}

func showsEntry(read, act string) string {
	return "shows " + read + " -> " + act
}

func sent(r readRequest) string {
	return "a step, in setup or not, that sends " + r.String()
}

func showsUse(read, act string) string {
	return fmt.Sprintf("a scenario where a %s read checks an act on %s through this entry: after the act as its state check, or before a destructive act on a fixed target as its guard, stating an expectation, with no refused credential variant and an outcome that reads state", read, act)
}

func (p policy) entries() []protocol.PolicyEntry {
	out := []protocol.PolicyEntry{}
	for r, shown := range p.reads {
		out = append(out, protocol.PolicyEntry{Key: readEntry(r), Rule: unusedRule, Use: sent(r)})
		for _, act := range shown {
			out = append(out, protocol.PolicyEntry{Key: showsEntry(r.String(), act), Rule: unusedRule, Use: showsUse(r.String(), act)})
		}
	}
	for path, acts := range p.shows {
		read := showsMethod + " " + path
		for _, act := range acts {
			out = append(out, protocol.PolicyEntry{Key: showsEntry(read, act), Rule: unusedRule, Use: showsUse(read, act)})
		}
	}
	for r := range p.destructive {
		out = append(out, protocol.PolicyEntry{Key: destructiveEntry(r), Rule: unusedRule, Use: sent(r)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func template(path string) string {
	return refRE.ReplaceAllString(path, anyValue)
}

type policyCheck struct {
	msgs    []string
	bodyBad bool
}

func (c *policyCheck) bad(format string, args ...any) {
	c.msgs = append(c.msgs, "policy: "+fmt.Sprintf(format, args...))
}

func parsePolicy(raw any) (policy, error) {
	if raw == nil {
		return policy{}, nil
	}
	c := &policyCheck{}
	pol, isObject := raw.(map[string]any)
	if !isObject {
		c.bad("must be an object with %s, %s, %s or %s, not %s", readsKey, showsKey, destructiveKey, maxBodyKey, show(raw))
		return policy{}, c.err()
	}
	for _, k := range sortedKeys(pol) {
		if k != readsKey && k != showsKey && k != destructiveKey && k != maxBodyKey {
			c.bad("unknown key %q; the http driver's policy has only %s, %s, %s and %s", k, readsKey, showsKey, destructiveKey, maxBodyKey)
		}
	}
	_, hasReads := pol[readsKey]
	_, hasShows := pol[showsKey]
	_, hasDestructive := pol[destructiveKey]
	rawMax, hasMax := pol[maxBodyKey]
	if !hasReads && !hasShows && !hasDestructive && !hasMax {
		c.bad("%s, %s, %s or %s is required; leave policy out when no request is declared a read or destructive, no read is declared to show a path and the request body limit is the default", readsKey, showsKey, destructiveKey, maxBodyKey)
	}
	out := policy{reads: map[readRequest][]string{}, shows: map[string][]string{}, destructive: map[readRequest]bool{}}
	if hasReads {
		c.reads(pol[readsKey], out.reads)
	}
	if hasShows {
		c.showsList(pol[showsKey], out.shows)
	}
	if hasDestructive {
		c.destructiveList(pol[destructiveKey], out.destructive, out.reads)
	}
	if hasMax {
		out.maxBody = c.maxBody(rawMax)
	}
	if len(c.msgs) > 0 {
		return policy{}, c.err()
	}
	return out, nil
}

func (c *policyCheck) err() error {
	rules := readsRule
	switch {
	case c.bodyBad && len(c.msgs) == 1:
		rules = bodyRule
	case c.bodyBad:
		rules = readsRule + ", " + bodyRule
	}
	return fmt.Errorf("%s (%s)", strings.Join(c.msgs, "; "), rules)
}

func (c *policyCheck) list(key string, raw any, what string) []any {
	entries, isList := raw.([]any)
	switch {
	case !isList:
		c.bad("%s must be a list of %s entries, not %s", key, what, show(raw))
	case len(entries) == 0:
		c.bad("%s is empty; list at least one entry, or leave %s out", key, key)
	}
	return entries
}

func (c *policyCheck) reads(raw any, out map[readRequest][]string) {
	first := map[readRequest]int{}
	for i, e := range c.list(readsKey, raw, "{ method, path }") {
		at := fmt.Sprintf("%s[%d]", readsKey, i)
		entry, isObject := e.(map[string]any)
		if !isObject {
			c.bad("%s must be an object with method and path, not %s", at, show(e))
			continue
		}
		for _, k := range sortedKeys(entry) {
			if k != "method" && k != "path" && k != showsKey {
				c.bad("%s: unknown key %q; an entry has only method, path and shows", at, k)
			}
		}
		method, methodIsText := entry["method"].(string)
		path, pathIsText := entry["path"].(string)
		ok := true
		switch {
		case !methodIsText:
			c.bad("%s: method must be POST, not %s", at, show(entry["method"]))
			ok = false
		case method != readMethod:
			c.bad("%s: method %q: only POST requests can be declared reads; GET, HEAD and OPTIONS already are, and PUT, PATCH and DELETE change state", at, method)
			ok = false
		}
		switch {
		case !pathIsText:
			c.bad("%s: path must be text starting with /, not %s", at, show(entry["path"]))
			ok = false
		default:
			if err := checkRequestPath(path); err != nil {
				c.bad("%s: %v", at, err)
				ok = false
			}
		}
		var shown []string
		if rawShows, has := entry[showsKey]; has {
			var showsOK bool
			shown, showsOK = c.paths(at+"."+showsKey, rawShows, path)
			ok = ok && showsOK
		}
		if !ok {
			continue
		}
		r := readRequest{method: method, path: path}
		if j, dup := first[r]; dup {
			c.bad("%s repeats %s[%d] (%s)", at, readsKey, j, r)
			continue
		}
		first[r] = i
		out[r] = shown
	}
}

func (c *policyCheck) showsList(raw any, out map[string][]string) {
	first := map[string]int{}
	for i, e := range c.list(showsKey, raw, "{ path, acts }") {
		at := fmt.Sprintf("%s[%d]", showsKey, i)
		entry, isObject := e.(map[string]any)
		if !isObject {
			c.bad("%s must be an object with path and acts, not %s", at, show(e))
			continue
		}
		for _, k := range sortedKeys(entry) {
			if k != "path" && k != actsKey {
				c.bad("%s: unknown key %q; an entry has only path and acts", at, k)
			}
		}
		path, pathIsText := entry["path"].(string)
		ok := true
		switch {
		case !pathIsText:
			c.bad("%s: path must be text starting with /, not %s", at, show(entry["path"]))
			ok = false
		default:
			if err := checkTemplate(path); err != nil {
				c.bad("%s: %v", at, err)
				ok = false
			}
		}
		rawActs, has := entry[actsKey]
		if !has {
			c.bad("%s: acts is required: the paths of the requests whose effect a %s of %s shows", at, showsMethod, show(entry["path"]))
			continue
		}
		acts, actsOK := c.paths(at+"."+actsKey, rawActs, path)
		if !ok || !actsOK {
			continue
		}
		if j, dup := first[path]; dup {
			c.bad("%s repeats the path of %s[%d] (%s)", at, showsKey, j, path)
			continue
		}
		first[path] = i
		out[path] = acts
	}
}

func (c *policyCheck) destructiveList(raw any, out map[readRequest]bool, reads map[readRequest][]string) {
	first := map[readRequest]int{}
	for i, e := range c.list(destructiveKey, raw, "{ method, path }") {
		at := fmt.Sprintf("%s[%d]", destructiveKey, i)
		entry, isObject := e.(map[string]any)
		if !isObject {
			c.bad("%s must be an object with method and path, not %s", at, show(e))
			continue
		}
		for _, k := range sortedKeys(entry) {
			if k != "method" && k != "path" {
				c.bad("%s: unknown key %q; an entry has only method and path", at, k)
			}
		}
		method, methodIsText := entry["method"].(string)
		path, pathIsText := entry["path"].(string)
		ok := true
		switch {
		case !methodIsText:
			c.bad("%s: method must be POST, not %s", at, show(entry["method"]))
			ok = false
		case method != readMethod:
			c.bad("%s: method %q: only POST requests can be declared destructive; PUT, PATCH and DELETE already are, and GET, HEAD and OPTIONS observe", at, method)
			ok = false
		}
		switch {
		case !pathIsText:
			c.bad("%s: path must be text starting with /, not %s", at, show(entry["path"]))
			ok = false
		default:
			if err := checkRequestPath(path); err != nil {
				c.bad("%s: %v", at, err)
				ok = false
			}
		}
		if !ok {
			continue
		}
		r := readRequest{method: method, path: path}
		if j, dup := first[r]; dup {
			c.bad("%s repeats %s[%d] (%s)", at, destructiveKey, j, r)
			continue
		}
		first[r] = i
		if _, isRead := reads[r]; isRead {
			c.bad("%s: %s is also in %s; a request can't both observe and destroy", at, r, readsKey)
			continue
		}
		out[r] = true
	}
}

func (c *policyCheck) paths(at string, raw any, own string) ([]string, bool) {
	list, isList := raw.([]any)
	switch {
	case !isList:
		c.bad("%s must be a list of paths, not %s", at, show(raw))
		return nil, false
	case len(list) == 0:
		c.bad("%s is empty; list at least one path, or leave it out", at)
		return nil, false
	}
	ok := true
	seen := map[string]bool{}
	var out []string
	for i, e := range list {
		p, isText := e.(string)
		if !isText {
			c.bad("%s[%d] must be a path starting with /, not %s", at, i, show(e))
			ok = false
			continue
		}
		if err := checkTemplate(p); err != nil {
			c.bad("%s[%d]: %v", at, i, err)
			ok = false
			continue
		}
		switch {
		case p == own:
			c.bad("%s[%d]: %s is the entry's own path, which a read always shows", at, i, p)
			ok = false
		case seen[p]:
			c.bad("%s lists %s twice", at, p)
			ok = false
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, ok
}

func checkTemplate(p string) error {
	if strings.Contains(p, "${") {
		return fmt.Errorf("path %q has a ${...} reference; write %s where a step's path has one, since a step's path is matched with each reference replaced by %s", p, anyValue, anyValue)
	}
	return checkRequestPath(p)
}

func (c *policyCheck) maxBody(raw any) int64 {
	n, isNumber := raw.(json.Number)
	v, err := strconv.ParseInt(n.String(), 10, 64)
	if !isNumber || err != nil || v < 1 || v > maxBodyLimit {
		c.bad("%s must be a whole number of bytes from 1 to %d (1 GiB), not %s; it bounds every request body a step sends", maxBodyKey, maxBodyLimit, show(raw))
		c.bodyBad = true
		return 0
	}
	return v
}
