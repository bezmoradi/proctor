package integrity

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"proctor/internal/model"
	"proctor/internal/transcript"
)

const maxApprovedEntries = 20000

type listedEntry struct {
	path string
	kind string
}

type lister interface {
	listUnder(dir string) ([]listedEntry, error)
}

func (r *Report) ApprovedRoot() string {
	return r.ProtectedRoot + "/" + model.ApprovedDir
}

func (r *Report) IsApproved(p string) bool {
	return r.ProtectedRoot != "" && strings.HasPrefix(p, r.ApprovedRoot()+"/")
}

func (r *Report) validateTranscripts(c *model.Config) {
	t := c.Transcripts
	if t == nil {
		return
	}
	p := r.ConfigPath
	if t.MaxBytes < 0 || t.MaxBytes > model.MaxTranscriptBytes {
		r.add(Invalid, "TRN-05", p, "transcripts.max_bytes %d must be between 1 and %d; leave it out for the default %d", t.MaxBytes, model.MaxTranscriptBytes, model.DefaultTranscriptBytes)
	}
	_, probs := transcript.Compile(t.Scrub)
	for _, m := range probs {
		r.add(Invalid, "TRN-03", p, "%s", m)
	}
}

type owned struct {
	spec, scenario string
	sc             *model.Scenario
	log            bool
}

func (r *Report) loadApproved(src Source, c *model.Config) {
	root := r.ApprovedRoot()
	want := map[string]owned{}
	folded := map[string]string{}
	var order []string
	for _, sf := range r.Specs {
		specID := r.SpecIDs[sf.Path]
		for i := range sf.Spec.Scenarios {
			sc := &sf.Spec.Scenarios[i]
			on, err := model.TranscriptOn(&sc.Transcript)
			if err != nil {
				if sc.Case == nil || sc.Case.FirstRow {
					r.add(Invalid, "TRN-01", sf.Path, "%s: %v", tableOrScenario(sc), err)
				}
				continue
			}
			if !on {
				continue
			}
			if !idPattern.MatchString(specID) || !idPattern.MatchString(sc.ID) {
				continue
			}
			ap := model.ApprovedPath(r.ProtectedRoot, specID, sc.ID)
			if err := checkRelPath(ap); err != nil || path.Dir(path.Dir(ap)) != root || path.Base(path.Dir(ap)) != specID {
				r.add(Invalid, "TRN-04", sf.Path, "%s: the approved transcript's path %q isn't inside %s/", scenarioWhere(sc), ap, root)
				continue
			}
			if other, clash := folded[strings.ToLower(ap)]; clash && other != ap {
				r.add(Invalid, "TRN-04", sf.Path, "%s: its approved transcript %s differs from %s only in letter case, which a case-insensitive file system stores as one file; give one of the scenarios another id", scenarioWhere(sc), ap, other)
				continue
			}
			folded[strings.ToLower(ap)] = ap
			if _, dup := want[ap]; dup {
				continue
			}
			want[ap] = owned{spec: specID, scenario: sc.ID, sc: sc}
			order = append(order, ap)
		}
		r.wantLogs(sf, specID, c, root, want, folded, &order)
	}
	r.checkApprovedFolder(src, root, want)
	limit := c.Transcripts.Limit()
	for _, ap := range order {
		o := want[ap]
		a := &model.Approval{SpecID: o.spec, Path: ap}
		data, found, err := readApproved(src, ap)
		if o.log {
			if r.readApprovedLog(c, o, a, data, found, err) {
				r.attachLogApproval(o, a)
			}
			continue
		}
		switch {
		case err != nil:
			r.add(Invalid, "TRN-04", ap, "the approved transcript of %s can't be read: %v", o.scenario, err)
			continue
		case found && len(data) > limit:
			r.add(Invalid, "TRN-04", ap, "the approved transcript is %d bytes, more than transcripts.max_bytes (%d), so no run could match it", len(data), limit)
			continue
		case found && !bytes.HasPrefix(data, []byte(model.TranscriptFirstLine+"\n")):
			r.add(Invalid, "TRN-04", ap, "isn't a transcript: its first line must be %q; approve the received file with proctor approve instead of writing it by hand", model.TranscriptFirstLine)
			continue
		case found:
			a.Found, a.Data, a.SHA256 = true, data, sha(data)
			r.Hashes[ap] = a.SHA256
		}
		r.attachApproval(o.scenario, o.spec, a)
	}
}

func (r *Report) attachApproval(scenario, specID string, a *model.Approval) {
	for _, sf := range r.Specs {
		if r.SpecIDs[sf.Path] != specID {
			continue
		}
		for i := range sf.Spec.Scenarios {
			if sf.Spec.Scenarios[i].ID == scenario {
				sf.Spec.Scenarios[i].Approval = a
			}
		}
	}
}

func tableOrScenario(sc *model.Scenario) string {
	if sc.Case != nil {
		return "table " + sc.Case.Label()
	}
	return "scenario " + sc.ID
}

func readApproved(src Source, p string) ([]byte, bool, error) {
	rr, ok := src.(regularReader)
	if !ok {
		return nil, false, fmt.Errorf("this source can't read approved files")
	}
	return rr.readRegular(p)
}

func (r *Report) checkApprovedFolder(src Source, root string, want map[string]owned) {
	l, ok := src.(lister)
	if !ok {
		r.add(Invalid, "INT-19", root, "this source can't list %s/, so files no scenario owns can't be found", root)
		return
	}
	entries, err := l.listUnder(root)
	if err != nil {
		r.add(Invalid, "INT-19", root, "cannot list the approved files: %v", err)
		return
	}
	for _, e := range entries {
		switch {
		case e.kind != "file":
			r.add(Invalid, "INT-19", e.path, "is a %s; everything under %s/ must be a regular file or a folder, never a link", e.kind, root)
		case want[e.path].scenario == "":
			r.add(Invalid, "INT-19", e.path, "no scenario or table of the listed specifications that sets transcript: true or approved_log: true owns this file (an approved transcript is %s/<specification id>/<scenario id>%s, an approved log %s/<specification id>/<scenario or table id>%s); remove it, or opt its scenario in", root, model.TranscriptSuffix, root, model.ApprovedLogSuffix)
		}
	}
}

func (s *CommitSource) listUnder(dir string) ([]listedEntry, error) {
	var out []listedEntry
	parts := strings.Split(dir, "/")
	for i := 1; i <= len(parts); i++ {
		sub := strings.Join(parts[:i], "/")
		if e, ok := s.tree[sub]; ok {
			return []listedEntry{{path: sub, kind: kindOf(e)}}, nil
		}
	}
	for _, p := range sortedKeys(s.tree) {
		if !strings.HasPrefix(p, dir+"/") {
			continue
		}
		e := s.tree[p]
		kind := "file"
		if !e.Regular() {
			kind = kindOf(e)
		}
		out = append(out, listedEntry{path: p, kind: kind})
		if len(out) > maxApprovedEntries {
			return nil, fmt.Errorf("more than %d entries under %s/", maxApprovedEntries, dir)
		}
	}
	return out, nil
}

func (s WorkTreeSource) listUnder(dir string) ([]listedEntry, error) {
	if err := checkRelPath(dir); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	parts := strings.Split(dir, "/")
	for i := range parts {
		sub := strings.Join(parts[:i+1], "/")
		fi, err := root.Lstat(filepath.FromSlash(sub))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, nil
		case err != nil:
			return nil, err
		case !fi.IsDir():
			return []listedEntry{{path: sub, kind: describeKind(fi.Mode())}}, nil
		}
	}
	var out []listedEntry
	var visit func(d string) error
	visit = func(d string) error {
		f, err := root.Open(filepath.FromSlash(d))
		if err != nil {
			return err
		}
		names, err := f.Readdirnames(-1)
		f.Close()
		if err != nil {
			return err
		}
		sort.Strings(names)
		for _, n := range names {
			p := d + "/" + n
			fi, err := root.Lstat(filepath.FromSlash(p))
			if err != nil {
				return err
			}
			if len(out) >= maxApprovedEntries {
				return fmt.Errorf("more than %d entries under %s/", maxApprovedEntries, dir)
			}
			switch {
			case fi.IsDir():
				if err := visit(p); err != nil {
					return err
				}
			case fi.Mode().IsRegular():
				out = append(out, listedEntry{path: p, kind: "file"})
			default:
				out = append(out, listedEntry{path: p, kind: describeKind(fi.Mode())})
			}
		}
		return nil
	}
	if err := visit(dir); err != nil {
		return nil, err
	}
	return out, nil
}

func describeKind(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return "symlink"
	case m.IsDir():
		return "folder"
	case m.IsRegular():
		return "file"
	}
	return "special file (" + m.Type().String() + ")"
}
