package docker

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"proctor/protocol"
)

const MaxObjects = 200

var objectName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)

type callArgs struct {
	Name      *string           `json:"name"`
	Labels    map[string]string `json:"labels"`
	HasLabels []string          `json:"has_labels"`
}

type reader struct {
	ctx      context.Context
	c        *client
	scope    []scopeEntry
	requests []string
}

func (d *Driver) Call(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	start := time.Now()
	o, known := ops[p.Op]
	if !known {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "unknown operation %q; the docker driver has %s", p.Op, strings.Join(operations, ", "))
	}
	if d.c == nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the docker driver has no connection")
	}
	if p.Credential != nil || p.Handle != "" {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "%s takes no credential and no handle", p.Op)
	}
	var a callArgs
	if p.Args != nil {
		if err := protocol.Decode(p.Args, &a); err != nil {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "%s: with: %v", p.Op, err)
		}
	}
	r := &reader{ctx: ctx, c: d.c, scope: d.scope}
	var fact any
	var summary string
	var err error
	if o.list {
		if a.Name != nil {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "%s takes labels and has_labels, not name", p.Op)
		}
		terms, terr := filterTerms(a)
		if terr != nil {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "%s: %v", p.Op, terr)
		}
		fact, summary, err = r.readList(o.kind, terms)
	} else {
		if a.Name == nil || len(a.Labels) > 0 || len(a.HasLabels) > 0 {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "%s takes only a name", p.Op)
		}
		if !objectName.MatchString(*a.Name) {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "%s %q: a name must match %s", p.Op, cut(oneLine(*a.Name), 300), objectName.String())
		}
		fact, summary, err = r.readOne(o.kind, *a.Name)
	}
	if err != nil {
		return protocol.CallResult{}, err
	}
	return protocol.CallResult{
		Fact: fact,
		Evidence: protocol.Evidence{
			Summary:        summary,
			RequestExcerpt: strings.Join(r.requests, "; "),
			DurationMS:     time.Since(start).Milliseconds(),
		},
	}, nil
}

func filterTerms(a callArgs) ([]string, error) {
	var terms []string
	for _, k := range sortedKeys(a.Labels) {
		if !labelKey.MatchString(k) {
			return nil, fmt.Errorf("label key %q must match %s", k, labelKey.String())
		}
		if hasControl(a.Labels[k]) {
			return nil, fmt.Errorf("the value of label %s holds a control character", k)
		}
		terms = append(terms, k+"="+a.Labels[k])
	}
	seen := map[string]bool{}
	for _, k := range a.HasLabels {
		_, both := a.Labels[k]
		switch {
		case !labelKey.MatchString(k):
			return nil, fmt.Errorf("has_labels key %q must match %s", k, labelKey.String())
		case seen[k] || both:
			return nil, fmt.Errorf("label key %s is named twice", k)
		}
		seen[k] = true
		terms = append(terms, k)
	}
	if len(terms)+1 > maxFilterTerms {
		return nil, fmt.Errorf("%d label conditions; at most %d are allowed", len(terms), maxFilterTerms-1)
	}
	return terms, nil
}

func carries(labels map[string]string, term string) bool {
	k, v, withValue := strings.Cut(term, "=")
	got, has := labels[k]
	return has && (!withValue || got == v)
}

func (r *reader) inScope(labels map[string]string) bool {
	for _, s := range r.scope {
		if carries(labels, s.String()) {
			return true
		}
	}
	return false
}

func (r *reader) matches(labels map[string]string, terms []string) bool {
	if !r.inScope(labels) {
		return false
	}
	for _, t := range terms {
		if !carries(labels, t) {
			return false
		}
	}
	return true
}

func outsideFilter(k *kind) error {
	return protocol.Lastingf(protocol.CauseUnsupportedResult, "Docker returned a %s outside the filter, so its answers can't be trusted (DKR-06)", k.single)
}

func (r *reader) listQuery(k *kind, s scopeEntry, terms []string) (string, url.Values, error) {
	b, err := json.Marshal(map[string][]string{"label": append([]string{s.String()}, terms...)})
	if err != nil {
		return "", nil, protocol.Lastingf(protocol.CauseInvalidRequest, "the filter can't be written: %v", err)
	}
	q := url.Values{"filters": {string(b)}}
	p := pathNetworks
	switch k {
	case kindContainer:
		p = pathContainers
		q.Set("all", "1")
	case kindVolume:
		p = pathVolumes
	}
	return p, q, nil
}

func (r *reader) get(p string, q url.Values, out any) error {
	line := "GET " + apiPrefix + p
	terms := make([]string, 0, len(q))
	for _, k := range sortedKeys(q) {
		for _, v := range q[k] {
			terms = append(terms, k+"="+v)
		}
	}
	if len(terms) > 0 {
		line += "?" + strings.Join(terms, "&")
	}
	r.requests = append(r.requests, line)
	return r.c.get(r.ctx, p, q, out)
}

func (r *reader) containers(terms []string) ([]containerSummary, error) {
	byID := map[string]containerSummary{}
	for _, s := range r.scope {
		p, q, err := r.listQuery(kindContainer, s, terms)
		if err != nil {
			return nil, err
		}
		var got []containerSummary
		if err := r.get(p, q, &got); err != nil {
			return nil, listError(kindContainer, err)
		}
		for _, c := range got {
			if !objectID.MatchString(c.ID) {
				return nil, protocol.Errorf(protocol.CauseUnreadableResponse, "Docker listed a container whose ID isn't 64 hex digits")
			}
			if !carries(c.Labels, s.String()) || !r.matches(c.Labels, terms) {
				return nil, outsideFilter(kindContainer)
			}
			byID[c.ID] = c
		}
		if len(byID) > MaxObjects {
			return nil, tooMany(kindContainer)
		}
	}
	out := make([]containerSummary, 0, len(byID))
	for _, c := range byID {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b containerSummary) int {
		return cmp.Or(cmp.Compare(primaryName(a.Names), primaryName(b.Names)), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

func (r *reader) networks(terms []string) ([]networkSummary, error) {
	byID := map[string]networkSummary{}
	for _, s := range r.scope {
		p, q, err := r.listQuery(kindNetwork, s, terms)
		if err != nil {
			return nil, err
		}
		var got []networkSummary
		if err := r.get(p, q, &got); err != nil {
			return nil, listError(kindNetwork, err)
		}
		for _, n := range got {
			if !objectID.MatchString(n.ID) {
				return nil, protocol.Errorf(protocol.CauseUnreadableResponse, "Docker listed a network whose ID isn't 64 hex digits")
			}
			if !carries(n.Labels, s.String()) || !r.matches(n.Labels, terms) {
				return nil, outsideFilter(kindNetwork)
			}
			byID[n.ID] = n
		}
		if len(byID) > MaxObjects {
			return nil, tooMany(kindNetwork)
		}
	}
	out := make([]networkSummary, 0, len(byID))
	for _, n := range byID {
		out = append(out, n)
	}
	slices.SortFunc(out, func(a, b networkSummary) int { return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID)) })
	return out, nil
}

func (r *reader) volumes(terms []string) ([]volumeSummary, error) {
	byName := map[string]volumeSummary{}
	for _, s := range r.scope {
		p, q, err := r.listQuery(kindVolume, s, terms)
		if err != nil {
			return nil, err
		}
		var got volumeList
		if err := r.get(p, q, &got); err != nil {
			return nil, listError(kindVolume, err)
		}
		for _, v := range got.Volumes {
			if v.Name == "" {
				return nil, protocol.Errorf(protocol.CauseUnreadableResponse, "Docker listed a volume with no name")
			}
			if !carries(v.Labels, s.String()) || !r.matches(v.Labels, terms) {
				return nil, outsideFilter(kindVolume)
			}
			byName[v.Name] = v
		}
		if len(byName) > MaxObjects {
			return nil, tooMany(kindVolume)
		}
	}
	out := make([]volumeSummary, 0, len(byName))
	for _, k := range sortedKeys(byName) {
		out = append(out, byName[k])
	}
	return out, nil
}

func listError(k *kind, err error) error {
	if errors.Is(err, errNotFound) {
		return protocol.Errorf(protocol.CauseQueryError, "listing %s answered 404; the socket may not be Docker's", k.plural)
	}
	return err
}

func tooMany(k *kind) error {
	return protocol.Lastingf(protocol.CauseUnsupportedResult, "more than %d %s are in scope; narrow the step with labels or the policy's scope", MaxObjects, k.plural)
}

func primaryName(names []string) string {
	for _, n := range names {
		if t := strings.TrimPrefix(n, "/"); t != "" && !strings.Contains(t, "/") {
			return t
		}
	}
	return ""
}

func (r *reader) visibleFor(k *kind, cs []containerSummary) (*visible, error) {
	v := &visible{containerIDs: map[string]string{}, containerNames: map[string]string{}, networkIDs: map[string]string{}, networkNames: map[string]string{}, volumes: map[string]volumeOptions{}, volumeDrivers: map[string]string{}}
	for _, c := range cs {
		name := primaryName(c.Names)
		v.containerIDs[c.ID] = name
		if name != "" {
			v.containerNames[name] = c.ID
		}
	}
	if k != kindContainer {
		return v, nil
	}
	ns, err := r.networks(nil)
	if err != nil {
		return nil, err
	}
	for _, n := range ns {
		v.networkIDs[n.ID] = n.Name
		v.networkNames[n.Name] = n.ID
	}
	vs, err := r.volumes(nil)
	if err != nil {
		return nil, err
	}
	for _, s := range vs {
		v.volumes[s.Name] = s.Options
		v.volumeDrivers[s.Name] = s.Driver
	}
	return v, nil
}

func (r *reader) inspectContainer(id string, terms []string) (*containerInspect, error) {
	var c containerInspect
	if err := r.get("/containers/"+id+"/json", nil, &c); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if c.ID != id || !r.matches(c.Config.Labels, terms) {
		return nil, outsideFilter(kindContainer)
	}
	return &c, nil
}

func (r *reader) inspectNetwork(id string, terms []string) (*networkInspect, error) {
	var n networkInspect
	if err := r.get("/networks/"+id, nil, &n); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if n.ID != id || !r.matches(n.Labels, terms) {
		return nil, outsideFilter(kindNetwork)
	}
	return &n, nil
}

func needsAllContainers(list []*containerInspect, v *visible) bool {
	for _, c := range list {
		for _, mode := range []string{c.HostConfig.NetworkMode, c.HostConfig.IpcMode, c.HostConfig.PidMode} {
			if target, ok := strings.CutPrefix(mode, "container:"); ok {
				if _, in := v.container(target); !in {
					return true
				}
			}
		}
	}
	return false
}

func (r *reader) readOne(k *kind, name string) (any, string, error) {
	notFound := map[string]any{"found": false, k.single: nil}
	missing := fmt.Sprintf("%s %s -> not found among objects labelled %s", k.single, name, describeScope(r.scope))
	switch k {
	case kindVolume:
		vs, err := r.volumes(nil)
		if err != nil {
			return nil, "", err
		}
		for i := range vs {
			if vs[i].Name == name {
				return map[string]any{"found": true, k.single: projectVolume(&vs[i])}, fmt.Sprintf("volume %s -> found, driver %s", name, vs[i].Driver), nil
			}
		}
		return notFound, missing, nil
	case kindNetwork:
		ns, err := r.networks(nil)
		if err != nil {
			return nil, "", err
		}
		id, err := uniqueID(k, name, len(ns), func(i int) (string, string) { return ns[i].Name, ns[i].ID })
		if err != nil || id == "" {
			return notFound, missing, err
		}
		n, err := r.inspectNetwork(id, nil)
		if err != nil || n == nil {
			return notFound, missing, err
		}
		cs, err := r.containers(nil)
		if err != nil {
			return nil, "", err
		}
		v, err := r.visibleFor(k, cs)
		if err != nil {
			return nil, "", err
		}
		f := projectNetwork(n, v)
		return map[string]any{"found": true, k.single: f}, fmt.Sprintf("network %s -> found, %d members", name, len(f.Members)), nil
	default:
		cs, err := r.containers(nil)
		if err != nil {
			return nil, "", err
		}
		id, err := uniqueID(k, name, len(cs), func(i int) (string, string) { return primaryName(cs[i].Names), cs[i].ID })
		if err != nil || id == "" {
			return notFound, missing, err
		}
		c, err := r.inspectContainer(id, nil)
		if err != nil || c == nil {
			return notFound, missing, err
		}
		v, err := r.visibleFor(k, cs)
		if err != nil {
			return nil, "", err
		}
		f, err := projectContainer(c, v)
		if err != nil {
			return nil, "", err
		}
		return map[string]any{"found": true, k.single: f}, fmt.Sprintf("container %s -> %s", name, stateText(f.State)), nil
	}
}

func uniqueID(k *kind, name string, n int, at func(int) (string, string)) (string, error) {
	id := ""
	for i := range n {
		got, gotID := at(i)
		if got != name {
			continue
		}
		if id != "" {
			return "", protocol.Lastingf(protocol.CauseUnsupportedResult, "two %s in scope are named %s", k.plural, name)
		}
		id = gotID
	}
	return id, nil
}

func stateText(s containerState) string {
	if s.Health != nil {
		return s.Status + " (" + *s.Health + ")"
	}
	return s.Status
}

func (r *reader) readList(k *kind, terms []string) (any, string, error) {
	var facts []any
	switch k {
	case kindVolume:
		vs, err := r.volumes(terms)
		if err != nil {
			return nil, "", err
		}
		for i := range vs {
			facts = append(facts, projectVolume(&vs[i]))
		}
	case kindNetwork:
		ns, err := r.networks(terms)
		if err != nil {
			return nil, "", err
		}
		var found []*networkInspect
		for _, s := range ns {
			n, err := r.inspectNetwork(s.ID, terms)
			if err != nil {
				return nil, "", err
			}
			if n != nil {
				found = append(found, n)
			}
		}
		if len(found) > 0 {
			cs, err := r.containers(nil)
			if err != nil {
				return nil, "", err
			}
			v, err := r.visibleFor(k, cs)
			if err != nil {
				return nil, "", err
			}
			slices.SortFunc(found, func(a, b *networkInspect) int { return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID)) })
			for _, n := range found {
				facts = append(facts, projectNetwork(n, v))
			}
		}
	default:
		cs, err := r.containers(terms)
		if err != nil {
			return nil, "", err
		}
		var found []*containerInspect
		for _, s := range cs {
			c, err := r.inspectContainer(s.ID, terms)
			if err != nil {
				return nil, "", err
			}
			if c != nil {
				found = append(found, c)
			}
		}
		if len(found) > 0 {
			v, err := r.visibleFor(k, cs)
			if err != nil {
				return nil, "", err
			}
			if len(terms) > 0 && needsAllContainers(found, v) {
				all, err := r.containers(nil)
				if err != nil {
					return nil, "", err
				}
				if v, err = r.visibleFor(k, all); err != nil {
					return nil, "", err
				}
			}
			slices.SortFunc(found, func(a, b *containerInspect) int { return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID)) })
			for _, c := range found {
				f, err := projectContainer(c, v)
				if err != nil {
					return nil, "", err
				}
				facts = append(facts, f)
			}
		}
	}
	if facts == nil {
		facts = []any{}
	}
	what := describeScope(r.scope)
	if len(terms) > 0 {
		what = strings.Join(terms, ", ") + " within " + what
	}
	return map[string]any{"count": len(facts), k.plural: facts}, fmt.Sprintf("%s {%s} -> %d in scope", k.plural, what, len(facts)), nil
}
