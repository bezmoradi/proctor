// Package gitrepo reads commits, trees, blobs and attributes through the git
// CLI, never through the working tree (INT-07 in prd.md).
//
// Every call runs git with system and global configuration disabled and
// replace objects ignored, so the user's or runner's Git setup can't change
// what Proctor reads. Any git error or stderr output is returned as an error;
// callers treat it as an integrity violation, never as "no changes".
package gitrepo

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// Repo is a Git repository on disk.
type Repo struct {
	dir string
}

// Entry is one tree entry from `git ls-tree -r`.
type Entry struct {
	Mode string // "100644", "100755", "120000", "160000"
	Type string // "blob" or "commit"
	OID  string
	Path string
}

// Regular reports whether the entry is a regular file blob (INT-13).
func (e Entry) Regular() bool {
	return e.Type == "blob" && (e.Mode == "100644" || e.Mode == "100755")
}

var oidPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// Open finds the repository containing dir.
func Open(dir string) (*Repo, error) {
	r := &Repo{dir: dir}
	out, err := r.git(nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	r.dir = strings.TrimSpace(string(out))
	return r, nil
}

// Dir returns the repository's top-level directory.
func (r *Repo) Dir() string { return r.dir }

// ResolveCommit returns the full object ID of rev, which must name a commit
// present in the object database.
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

// Parents returns the parent object IDs recorded in the commit object
// itself. Unlike `git rev-list --parents`, this isn't hidden by a shallow
// clone's grafts.
func (r *Repo) Parents(commit string) ([]string, error) {
	out, err := r.git(nil, "cat-file", "commit", commit)
	if err != nil {
		return nil, err
	}
	var parents []string
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			break // end of the header
		}
		if p, ok := strings.CutPrefix(line, "parent "); ok {
			parents = append(parents, p)
		}
	}
	return parents, nil
}

// Tree returns every entry of the commit's tree, keyed by exact path bytes.
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

// Blob returns a blob's exact bytes.
func (r *Repo) Blob(oid string) ([]byte, error) {
	if !oidPattern.MatchString(oid) {
		return nil, fmt.Errorf("invalid blob id %q", oid)
	}
	return r.git(nil, "cat-file", "blob", oid)
}

// Attributes returns, for each path, the attributes that are set, unset or
// given a value, as Git evaluates them from the commit's .gitattributes files.
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

// cleanEnv drops inherited GIT_* variables, such as GIT_DIR or
// GIT_CONFIG_PARAMETERS, and disables configuration Proctor doesn't control.
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
