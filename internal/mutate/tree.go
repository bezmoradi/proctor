package mutate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type TreeError struct {
	Msg string
}

func (e *TreeError) Error() string { return e.Msg }

func ListFiles(dir string) ([]string, error) {
	cmd := exec.Command("git", "-C", dir, "-c", "core.quotePath=false", "-c", "core.fsmonitor=false",
		"ls-files", "-z", "-c", "-o", "--exclude-standard")
	cmd.Env = gitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git ls-files: %s", msg)
	}
	seen := map[string]bool{}
	var out []string
	for _, rec := range bytes.Split(stdout.Bytes(), []byte{0}) {
		p := string(rec)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func gitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_NO_REPLACE_OBJECTS=1",
		"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
}

func snapshot(repo string, files []string) (string, error) {
	real, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return "", err
	}
	type entry struct {
		rel  string
		mode fs.FileMode
	}
	var keep []entry
	for _, f := range files {
		if strings.HasPrefix(f, "/") || strings.Contains(f, "\x00") || filepath.Clean(f) != filepath.FromSlash(f) || f == ".git" || strings.HasPrefix(f, ".git/") {
			return "", &TreeError{Msg: fmt.Sprintf("git ls-files listed %q, which isn't a clean path inside the repository", f)}
		}
		full := filepath.Join(real, filepath.FromSlash(f))
		fi, err := os.Lstat(full)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return "", err
		case fi.Mode()&fs.ModeSymlink != 0:
			return "", &TreeError{Msg: fmt.Sprintf("%s is a symbolic link; proctor mutate copies only regular files, so a copy with the link would build differently from the working tree", f)}
		case fi.IsDir():
			return "", &TreeError{Msg: fmt.Sprintf("%s is a directory (a submodule or a nested repository); proctor mutate copies only regular files", f)}
		case !fi.Mode().IsRegular():
			return "", &TreeError{Msg: fmt.Sprintf("%s isn't a regular file (%s); proctor mutate copies only regular files", f, fi.Mode().Type())}
		}
		if resolved, err := filepath.EvalSymlinks(full); err != nil {
			return "", err
		} else if resolved != full {
			return "", &TreeError{Msg: fmt.Sprintf("%s is under a symbolic link to a directory; proctor mutate copies only regular files", f)}
		}
		keep = append(keep, entry{rel: f, mode: fi.Mode().Perm()})
	}
	dir, err := os.MkdirTemp("", "proctor-mutate-source-*")
	if err != nil {
		return "", err
	}
	for _, e := range keep {
		if err := copyFile(filepath.Join(real, filepath.FromSlash(e.rel)), filepath.Join(dir, filepath.FromSlash(e.rel)), e.mode); err != nil {
			removeTree(dir)
			return "", err
		}
	}
	return dir, nil
}

func clone(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil || rel == "." {
			return err
		}
		if d.IsDir() {
			return os.Mkdir(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s in the source copy isn't a regular file", rel)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(p, filepath.Join(dst, rel), fi.Mode().Perm())
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Chmod(mode | 0o600); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func removeTree(dir string) error {
	if err := os.RemoveAll(dir); err == nil {
		return nil
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(dir)
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
