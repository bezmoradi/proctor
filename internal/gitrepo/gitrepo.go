package gitrepo

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Repo struct {
	dir string
}

type Entry struct {
	Mode string
	Type string
	OID  string
	Path string
}

func (e Entry) Regular() bool {
	return e.Type == "blob" && (e.Mode == "100644" || e.Mode == "100755")
}

var oidPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

type NotRepositoryError struct {
	Dir string
	Git string
}

func (e *NotRepositoryError) Error() string {
	return fmt.Sprintf("not a Git repository: %s (%s); this command reads Git, and only proctor validate without --commit reads a folder outside one", e.Dir, e.Git)
}

func Open(dir string) (*Repo, error) {
	r := &Repo{dir: dir}
	out, err := r.git(nil, "rev-parse", "--show-toplevel")
	if err != nil {
		if plain, ok := plainFolder(dir); ok && strings.Contains(err.Error(), "not a git repository") {
			return nil, &NotRepositoryError{Dir: plain, Git: err.Error()}
		}
		return nil, err
	}
	r.dir = strings.TrimSpace(string(out))
	return r, nil
}

func plainFolder(dir string) (string, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return "", false
	}
	for d := real; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); !errors.Is(err, fs.ErrNotExist) {
			return "", false
		}
		if filepath.Dir(d) == d {
			return real, true
		}
	}
}

func (r *Repo) Dir() string { return r.dir }

func (r *Repo) ResolveCommit(rev string) (string, error) {
	if rev == "" {
		return "", fmt.Errorf("empty commit reference")
	}
	out, err := r.git(nil, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("commit %q is not available in this repository (a shallow clone may be missing it): %w", rev, err)
	}
	oid := strings.TrimSpace(string(out))
	if !oidPattern.MatchString(oid) {
		return "", fmt.Errorf("commit %q resolved to unexpected value %q", rev, oid)
	}
	if _, err := r.git(nil, "cat-file", "-e", oid+"^{tree}"); err != nil {
		return "", fmt.Errorf("tree of commit %s is not available: %w", oid, err)
	}
	return oid, nil
}

func (r *Repo) Parents(commit string) ([]string, error) {
	out, err := r.git(nil, "cat-file", "commit", commit)
	if err != nil {
		return nil, err
	}
	var parents []string
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			break
		}
		if p, ok := strings.CutPrefix(line, "parent "); ok {
			parents = append(parents, p)
		}
	}
	return parents, nil
}

func (r *Repo) MergeBases(a, b string) ([]string, error) {
	if !oidPattern.MatchString(a) || !oidPattern.MatchString(b) {
		return nil, fmt.Errorf("merge base of %q and %q: both must be full commit IDs", a, b)
	}
	cmd := exec.Command("git", "-C", r.dir, "merge-base", "--all", "--end-of-options", a, b)
	cmd.Env = cleanEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return nil, fmt.Errorf("git merge-base: %s", msg)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && stdout.Len() == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("git merge-base: %v", err)
	}
	bases := strings.Fields(stdout.String())
	for _, o := range bases {
		if !oidPattern.MatchString(o) {
			return nil, fmt.Errorf("git merge-base printed unexpected value %q", o)
		}
	}
	return bases, nil
}

func (r *Repo) Tree(commit string) (map[string]Entry, error) {
	out, err := r.git(nil, "ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]Entry)
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, path, ok := bytes.Cut(rec, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("unexpected ls-tree record %q", rec)
		}
		f := strings.Fields(string(meta))
		if len(f) != 3 {
			return nil, fmt.Errorf("unexpected ls-tree record %q", rec)
		}
		e := Entry{Mode: f[0], Type: f[1], OID: f[2], Path: string(path)}
		entries[e.Path] = e
	}
	return entries, nil
}

func (r *Repo) Index() ([]Entry, error) {
	out, err := r.git(nil, "ls-files", "-z", "--stage")
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, p, ok := bytes.Cut(rec, []byte{'\t'})
		f := strings.Fields(string(meta))
		if !ok || len(f) != 3 {
			return nil, fmt.Errorf("unexpected ls-files record %q", rec)
		}
		typ := "blob"
		if f[0] == "160000" {
			typ = "commit"
		}
		entries = append(entries, Entry{Mode: f[0], Type: typ, OID: f[1], Path: string(p)})
	}
	return entries, nil
}

func (r *Repo) Blob(oid string) ([]byte, error) {
	if !oidPattern.MatchString(oid) {
		return nil, fmt.Errorf("invalid blob id %q", oid)
	}
	return r.git(nil, "cat-file", "blob", oid)
}

func (r *Repo) Attributes(commit string, paths []string) (map[string]map[string]string, error) {
	result := make(map[string]map[string]string, len(paths))
	if len(paths) == 0 {
		return result, nil
	}
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	var in bytes.Buffer
	for _, p := range sorted {
		in.WriteString(p)
		in.WriteByte(0)
	}
	out, err := r.git(&in, "check-attr", "-a", "-z", "--stdin", "--source="+commit)
	if err != nil {
		return nil, err
	}
	fields := bytes.Split(out, []byte{0})
	for i := 0; i+2 < len(fields); i += 3 {
		path, attr, value := string(fields[i]), string(fields[i+1]), string(fields[i+2])
		if result[path] == nil {
			result[path] = make(map[string]string)
		}
		result[path][attr] = value
	}
	return result, nil
}

func (r *Repo) git(stdin *bytes.Buffer, args ...string) ([]byte, error) {
	full := append([]string{"-C", r.dir, "-c", "core.quotePath=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = cleanEnv()
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	msg := strings.TrimSpace(stderr.String())
	if err != nil {
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", args[0], msg)
	}
	if msg != "" {
		return nil, fmt.Errorf("git %s wrote to stderr: %s", args[0], msg)
	}
	return stdout.Bytes(), nil
}

func cleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"LC_ALL=C",
	)
}
