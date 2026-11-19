package integrity

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"proctor/internal/gitrepo"
)

type Source interface {
	ReadFile(p string) (data []byte, ok bool, err error)
}

var CodeownersPaths = []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}

var repoMetaPaths = []string{".gitattributes", ".gitmodules", ".lfsconfig"}

func Protected(p, root string, extra []string) bool {
	if strings.HasPrefix(p, root+"/") || strings.HasPrefix(p, ".github/") {
		return true
	}
	for _, m := range append(append([]string(nil), repoMetaPaths...), CodeownersPaths...) {
		if p == m {
			return true
		}
	}
	for _, e := range extra {
		if p == e || (strings.HasSuffix(e, "/") && strings.HasPrefix(p, e)) {
			return true
		}
	}
	return false
}

func checkRelPath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("path is empty")
	case strings.HasPrefix(p, "/"):
		return fmt.Errorf("path %q is absolute", p)
	case strings.ContainsAny(p, "\\\x00"):
		return fmt.Errorf("path %q contains a backslash or NUL", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return fmt.Errorf("path %q leaves its directory", p)
		}
	}
	if path.Clean(p) != p || p == "." {
		return fmt.Errorf("path %q is not in clean form (expected %q)", p, path.Clean(p))
	}
	return nil
}

func CheckCommit(repo *gitrepo.Repo, commit, configPath string) *Report {
	src, err := NewCommitSource(repo, commit)
	if err != nil {
		r := &Report{ConfigPath: configPath, Hashes: map[string]string{}}
		r.add(Violation, "INT-08", "", "%v", err)
		return r
	}
	oid, tree := src.oid, src.tree
	r := Load(src, configPath)
	r.Commit = oid
	if r.ProtectedRoot == "" {
		return r
	}
	var extra []string
	if r.Config != nil {
		extra = r.Config.ProtectedPaths
	}

	reported := map[string]bool{}
	for _, d := range protectedDirs(r.ProtectedRoot, extra) {
		if e, ok := tree[d]; ok && !reported[d] {
			reported[d] = true
			r.add(Violation, "INT-13", d, "should be a directory holding protected inputs, but is a %s (mode %s)", kindOf(e), e.Mode)
		}
	}

	var protectedPaths []string
	codeowners := 0
	for _, p := range sortedKeys(tree) {
		e := tree[p]
		if !Protected(p, r.ProtectedRoot, extra) {
			continue
		}
		protectedPaths = append(protectedPaths, p)
		if !e.Regular() {
			r.add(Violation, "INT-13", p, "protected input is a %s (mode %s); only regular files are allowed", kindOf(e), e.Mode)
			continue
		}
		data, err := repo.Blob(e.OID)
		if err != nil {
			r.add(Violation, "INT-07", p, "cannot read blob: %v", err)
			continue
		}
		if bytes.HasPrefix(data, []byte("version https://git-lfs.github.com/spec/")) {
			r.add(Violation, "INT-13", p, "protected input is a Git LFS pointer")
		}
		for _, c := range CodeownersPaths {
			if p == c {
				codeowners++
			}
		}
		if isSpecCandidate(p, r) && !r.listed[p] {
			r.Unlisted = append(r.Unlisted, p)
		}
	}
	if codeowners > 1 {
		r.add(Violation, "INT-11", "", "more than one CODEOWNERS file (%s); keep exactly one", strings.Join(CodeownersPaths, ", "))
	}

	attrs, err := repo.Attributes(oid, protectedPaths)
	if err != nil {
		r.add(Violation, "INT-14", "", "cannot evaluate Git attributes: %v", err)
	}
	for _, p := range protectedPaths {
		for _, a := range sortedKeys(attrs[p]) {
			if forbiddenAttr(a) {
				r.add(Violation, "INT-14", p, "Git attribute %q is %s on a protected path", a, attrs[p][a])
			}
		}
	}
	return r
}

func protectedDirs(root string, extra []string) []string {
	var dirs []string
	addWithAncestors := func(d string) {
		for ; d != "." && d != "" && d != "/"; d = path.Dir(d) {
			dirs = append(dirs, d)
		}
	}
	addWithAncestors(root)
	addWithAncestors(".github")
	for _, e := range extra {
		if strings.HasSuffix(e, "/") {
			addWithAncestors(strings.TrimSuffix(e, "/"))
		} else {
			addWithAncestors(path.Dir(e))
		}
	}
	return dirs
}

func forbiddenAttr(a string) bool {
	switch a {
	case "diff", "binary", "text", "eol", "crlf", "filter", "working-tree-encoding", "ident",
		"linguist-generated", "export-ignore", "export-subst":
		return true
	}
	return false
}

func isSpecCandidate(p string, r *Report) bool {
	if !strings.HasPrefix(p, r.ProtectedRoot+"/") {
		return false
	}
	if !strings.HasSuffix(p, ".yaml") && !strings.HasSuffix(p, ".yml") {
		return false
	}
	if p == r.ConfigPath || strings.HasPrefix(p, r.ProtectedRoot+"/environments/") {
		return false
	}
	if r.Config != nil && p == r.ProtectedRoot+"/"+r.Config.Manifest {
		return false
	}
	if r.MutantsPath != "" && p == r.MutantsPath {
		return false
	}
	if r.DimensionsPath != "" && p == r.DimensionsPath {
		return false
	}
	if r.IsApproved(p) {
		return false
	}
	return true
}

func kindOf(e gitrepo.Entry) string {
	switch {
	case e.Mode == "120000":
		return "symlink"
	case e.Type == "commit":
		return "submodule"
	default:
		return e.Type
	}
}

type CommitSource struct {
	repo *gitrepo.Repo
	oid  string
	tree map[string]gitrepo.Entry
}

func NewCommitSource(repo *gitrepo.Repo, commit string) (*CommitSource, error) {
	oid, err := repo.ResolveCommit(commit)
	if err != nil {
		return nil, err
	}
	tree, err := repo.Tree(oid)
	if err != nil {
		return nil, err
	}
	return &CommitSource{repo: repo, oid: oid, tree: tree}, nil
}

func (s *CommitSource) ReadFile(p string) ([]byte, bool, error) {
	e, ok := s.tree[p]
	if !ok {
		return nil, false, nil
	}
	if !e.Regular() {
		return nil, false, fmt.Errorf("%s is a %s, not a regular file", p, kindOf(e))
	}
	data, err := s.repo.Blob(e.OID)
	return data, err == nil, err
}

type WorkTreeSource struct {
	Root string
}

func (s WorkTreeSource) ReadFile(p string) ([]byte, bool, error) {
	if err := checkRelPath(p); err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(filepath.Join(s.Root, filepath.FromSlash(p)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}
