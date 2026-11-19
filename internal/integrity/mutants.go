package integrity

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"proctor/internal/gitrepo"
	"proctor/internal/model"
	"proctor/internal/strictyaml"
)

type FileState struct {
	Listed  bool
	Regular bool
	Exec    bool
	Kind    string
}

func (r *Report) MutationScripts() (up, down string) {
	if r.Config == nil || r.Config.Mutation == nil {
		return "", ""
	}
	return r.ProtectedRoot + "/" + r.Config.Mutation.Up, r.ProtectedRoot + "/" + r.Config.Mutation.Down
}

func (r *Report) loadMutation(src Source, c *model.Config) {
	m := c.Mutation
	if m == nil {
		return
	}
	p := r.ConfigPath
	paths := true
	for _, f := range []struct{ key, value string }{{"mutants", m.Mutants}, {"up", m.Up}, {"down", m.Down}} {
		if f.value == "" {
			r.add(Invalid, "MUT-01", p, "mutation.%s is required", f.key)
			paths = false
			continue
		}
		if err := checkRelPath(f.value); err != nil {
			r.add(Invalid, "MUT-01", p, "mutation.%s: %v; give a path relative to %s/", f.key, err, r.ProtectedRoot)
			paths = false
		}
	}
	switch {
	case m.Binding == "":
		r.add(Invalid, "MUT-01", p, "mutation.binding is required: the absolute path, outside the repository, where the up script writes the binding")
	case !filepath.IsAbs(m.Binding):
		r.add(Invalid, "MUT-01", p, "mutation.binding %q must be an absolute path outside the repository", m.Binding)
	case filepath.Clean(m.Binding) != m.Binding:
		r.add(Invalid, "MUT-01", p, "mutation.binding %q is not in clean form (expected %q)", m.Binding, filepath.Clean(m.Binding))
	}
	if !paths {
		return
	}
	for _, s := range []string{m.Up, m.Down} {
		full := r.ProtectedRoot + "/" + s
		data, ok, err := src.ReadFile(full)
		switch {
		case err != nil:
			r.add(Invalid, "MUT-01", full, "cannot read the mutation script: %v", err)
		case !ok:
			r.add(Invalid, "MUT-01", full, "the mutation script named in %s isn't there", p)
		default:
			r.Hashes[full] = sha(data)
		}
	}
	full := r.ProtectedRoot + "/" + m.Mutants
	if full == r.ConfigPath || full == r.ProtectedRoot+"/"+c.Manifest || r.listed[full] {
		r.add(Invalid, "MUT-01", p, "mutation.mutants names %s, which is already the configuration, the manifest or a specification", full)
		return
	}
	r.MutantsPath = full
	data, ok, err := src.ReadFile(full)
	switch {
	case err != nil:
		r.add(Invalid, "MUT-01", full, "cannot read the mutants file: %v", err)
		return
	case !ok:
		r.add(Invalid, "MUT-01", full, "the mutants file named in %s isn't there", p)
		return
	}
	r.Hashes[full] = sha(data)
	var mutants []model.Mutant
	if err := strictyaml.Decode(data, &mutants); err != nil {
		r.add(Invalid, "MUT-01", full, "%v", err)
		return
	}
	if doc, err := strictyaml.Parse(data); err == nil && len(doc.Content) > 0 && doc.Content[0].Kind != yaml.SequenceNode {
		r.add(Invalid, "MUT-01", full, "the mutants file must be a list of mutants")
		return
	}
	r.checkMutants(full, mutants, c)
	r.Mutants = mutants
}

func (r *Report) checkMutants(p string, mutants []model.Mutant, c *model.Config) {
	if len(mutants) == 0 {
		r.add(Invalid, "MUT-01", p, "the mutants file lists no mutants")
	}
	scenarios := map[string]bool{}
	for _, id := range r.ScenarioIDs {
		scenarios[id] = true
	}
	seen := map[string]int{}
	for i, mu := range mutants {
		where := fmt.Sprintf("mutants[%d]", i)
		switch {
		case !model.MutantID.MatchString(mu.ID):
			r.add(Invalid, "MUT-01", p, "%s: id %q must be capital letters and digits, a hyphen and a number, such as M-01 or PAGE-001", where, mu.ID)
		case seen[mu.ID] > 0:
			r.add(Invalid, "MUT-01", p, "%s: id %s is also used by mutants[%d]", where, mu.ID, seen[mu.ID]-1)
		default:
			seen[mu.ID] = i + 1
			where = "mutant " + mu.ID
		}
		if strings.TrimSpace(mu.Title) == "" {
			r.add(Invalid, "MUT-01", p, "%s: title is required", where)
		}
		if len(mu.Edits) == 0 {
			r.add(Invalid, "MUT-01", p, "%s: edits lists no edits", where)
		}
		for j, e := range mu.Edits {
			ew := fmt.Sprintf("%s: edits[%d]", where, j)
			if err := checkRelPath(e.File); err != nil {
				r.add(Invalid, "MUT-01", p, "%s: file: %v; give a path relative to the repository root", ew, err)
			} else if Protected(e.File, r.ProtectedRoot, c.ProtectedPaths) {
				r.add(Invalid, "MUT-01", p, "%s: %s is a protected input; a mutant edits the application, never the specifications or Proctor's configuration", ew, e.File)
			}
			switch {
			case e.Find == "":
				r.add(Invalid, "MUT-01", p, "%s: find is empty", ew)
			case e.Find == e.Replace:
				r.add(Invalid, "MUT-01", p, "%s: replace is the same as find, so the edit changes nothing", ew)
			}
			if e.Count != nil && *e.Count < 1 {
				r.add(Invalid, "MUT-01", p, "%s: count must be 1 or more, got %d", ew, *e.Count)
			}
			if e.Line != nil {
				switch {
				case *e.Line < 1:
					r.add(Invalid, "MUT-01", p, "%s: line must be 1 or more, got %d", ew, *e.Line)
				case e.Times() != 1:
					r.add(Invalid, "MUT-01", p, "%s: line picks one match, so count must be 1 or left out, got %d", ew, e.Times())
				}
			}
		}
		if mu.KilledBy != nil && len(mu.KilledBy) == 0 {
			r.add(Invalid, "MUT-01", p, "%s: killed_by lists no scenarios; leave it out if any scenario may kill the mutant", where)
		}
		named := map[string]bool{}
		for _, id := range mu.KilledBy {
			switch {
			case named[id]:
				r.add(Invalid, "MUT-01", p, "%s: killed_by names %s twice", where, id)
			case !scenarios[id]:
				r.add(Invalid, "MUT-01", p, "%s: killed_by names %q, which isn't a scenario or table case of the listed specifications", where, id)
			}
			named[id] = true
		}
	}
}

func (r *Report) CheckMutantFiles(repoDir string, src Source, state func(p string) (FileState, error), lines bool) {
	if r.Config == nil || r.Config.Mutation == nil {
		return
	}
	p := r.ConfigPath
	if b := r.Config.Mutation.Binding; filepath.IsAbs(b) && filepath.Clean(b) == b {
		if inside, err := insideDir(repoDir, b); err != nil {
			r.add(Invalid, "MUT-01", p, "mutation.binding: %v", err)
		} else if inside {
			r.add(Invalid, "MUT-01", p, "mutation.binding %s is inside the repository; the binding must be written outside it", b)
		}
	}
	if r.MutantsPath != "" {
		up, down := r.MutationScripts()
		for _, s := range dedupeStrings([]string{up, down}) {
			st, err := state(s)
			switch {
			case err != nil:
				r.add(Invalid, "MUT-01", s, "%v", err)
			case !st.Listed:
				r.add(Invalid, "MUT-01", s, "the mutation script isn't tracked by Git, nor an untracked file Git doesn't ignore")
			case !st.Regular:
				r.add(Invalid, "MUT-01", s, "the mutation script is a %s; it must be a regular file", st.Kind)
			case !st.Exec:
				r.add(Invalid, "MUT-01", s, "the mutation script isn't executable; Proctor runs it as a program (chmod +x, and a #! line)")
			}
		}
	}
	usable := map[string]bool{}
	checked := map[string]bool{}
	for _, mu := range r.Mutants {
		for j, e := range mu.Edits {
			if checkRelPath(e.File) != nil || Protected(e.File, r.ProtectedRoot, r.Config.ProtectedPaths) {
				continue
			}
			if !checked[e.File] {
				checked[e.File] = true
				st, err := state(e.File)
				switch {
				case err != nil:
					r.add(Invalid, "MUT-01", r.MutantsPath, "mutant %s: %s: %v", mu.ID, e.File, err)
				case !st.Listed:
					r.add(Invalid, "MUT-01", r.MutantsPath, "mutant %s: %s isn't tracked by Git, nor an untracked file Git doesn't ignore, so it isn't in the copy a mutant is built from", mu.ID, e.File)
				case !st.Regular:
					r.add(Invalid, "MUT-01", r.MutantsPath, "mutant %s: %s is a %s; a mutant edits only regular files", mu.ID, e.File, st.Kind)
				default:
					usable[e.File] = true
				}
			}
			if !lines || e.Line == nil || *e.Line < 1 || e.Find == "" || !usable[e.File] {
				continue
			}
			data, ok, err := src.ReadFile(e.File)
			if err != nil || !ok {
				r.add(Invalid, "MUT-01", r.MutantsPath, "mutant %s: edits[%d]: cannot read %s to check its line", mu.ID, j, e.File)
				continue
			}
			if _, err := model.LocateAtLine(string(data), e.Find, *e.Line); err != nil {
				r.add(Invalid, "MUT-01", r.MutantsPath, "mutant %s: edits[%d]: %s: %v", mu.ID, j, e.File, err)
			}
		}
	}
}

func WorkTreeState(root string, listed map[string]bool) func(p string) (FileState, error) {
	real, rootErr := filepath.EvalSymlinks(root)
	return func(p string) (FileState, error) {
		if rootErr != nil {
			return FileState{}, rootErr
		}
		if !listed[p] {
			return FileState{}, nil
		}
		st := FileState{Listed: true}
		full := filepath.Join(real, filepath.FromSlash(p))
		fi, err := os.Lstat(full)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			st.Kind = "deleted file"
			return st, nil
		case err != nil:
			return st, err
		}
		st.Kind = fileKind(fi.Mode())
		if !fi.Mode().IsRegular() {
			return st, nil
		}
		resolved, err := filepath.EvalSymlinks(full)
		if err != nil {
			return st, err
		}
		if resolved != full {
			st.Kind = "file under a symbolic link"
			return st, nil
		}
		st.Regular, st.Exec = true, fi.Mode().Perm()&0o111 != 0
		return st, nil
	}
}

func (s *CommitSource) FileState(p string) (FileState, error) {
	return treeState(s.tree, p), nil
}

func treeState(tree map[string]gitrepo.Entry, p string) FileState {
	e, ok := tree[p]
	if !ok {
		return FileState{}
	}
	return FileState{Listed: true, Regular: e.Regular(), Exec: e.Mode == "100755", Kind: kindOf(e)}
}

func fileKind(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return "symlink"
	case m.IsDir():
		return "directory"
	case m.IsRegular():
		return "regular file"
	}
	return "special file"
}

func insideDir(dir, p string) (bool, error) {
	d, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false, err
	}
	existing, rest := p, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return false, err
	}
	full := filepath.Join(resolved, rest)
	rel, err := filepath.Rel(d, full)
	if err != nil {
		return false, err
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
