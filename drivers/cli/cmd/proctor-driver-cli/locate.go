package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bezmoradi/proctor/protocol"
)

type repository struct {
	root           string
	real           string
	protectedRoot  string
	protectedPaths []string
}

func (r *repository) resolve(c *command) (string, error) {
	var p string
	switch {
	case filepath.IsAbs(c.Program):
		p = c.Program
	case strings.Contains(c.Program, "/"):
		p = filepath.Join(r.root, filepath.FromSlash(c.Program))
	default:
		found, err := exec.LookPath(c.Program)
		switch {
		case errors.Is(err, exec.ErrDot):
			return "", fmt.Errorf("program %s is found only through a relative folder on PATH, which the driver doesn't use", c.Program)
		case err != nil:
			return "", fmt.Errorf("program %s isn't on PATH", c.Program)
		case !filepath.IsAbs(found):
			return "", fmt.Errorf("program %s resolves to %s, which isn't an absolute path", c.Program, found)
		}
		p = found
	}
	fi, err := os.Stat(p)
	switch {
	case err != nil:
		return "", fmt.Errorf("program %s can't be run: %v", c.Program, pathProblem(err))
	case !fi.Mode().IsRegular():
		return "", fmt.Errorf("program %s (%s) isn't a regular file", c.Program, p)
	case fi.Mode().Perm()&0o111 == 0:
		return "", fmt.Errorf("program %s (%s) isn't executable", c.Program, p)
	}
	if c.Kind == protocol.KindObserve {
		if err := r.checkObserved(p, "program "+c.Program); err != nil {
			return "", err
		}
	}
	return p, nil
}

func (r *repository) checkObserved(p, what string) error {
	if rel, inside := r.inside(p); inside {
		if link := r.linkOnPath(rel); link != "" {
			return fmt.Errorf("%s: %s is a symbolic link inside the repository; an observe command's program and the files it reads must be reached without one (%s)", what, link, locationRule)
		}
		if !r.protected(rel) {
			if !strings.HasSuffix(what, " "+rel) && !strings.HasSuffix(what, "("+rel+")") {
				what += " (" + rel + ")"
			}
			return fmt.Errorf("%s is in the repository's unprotected tree, where a pull request could change what the check reports; keep it under %s/ or a protected_paths folder, or use a program outside the repository (%s)", what, r.protectedRoot, locationRule)
		}
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return fmt.Errorf("%s can't be resolved: %v (%s)", what, pathProblem(err), locationRule)
	}
	if rel, inside := r.inside(real); inside && !r.protected(rel) {
		return fmt.Errorf("%s resolves to %s, in the repository's unprotected tree, where a pull request could change what the check reports (%s)", what, rel, locationRule)
	}
	return nil
}

func (r *repository) inside(p string) (string, bool) {
	for _, base := range []string{r.root, r.real} {
		if base == "" {
			continue
		}
		rel, err := filepath.Rel(base, p)
		if err == nil && rel != "." && filepath.IsLocal(rel) {
			return filepath.ToSlash(rel), true
		}
		if err == nil && rel == "." {
			return ".", true
		}
	}
	return "", false
}

func (r *repository) linkOnPath(rel string) string {
	if rel == "." {
		return ""
	}
	parts := strings.Split(rel, "/")
	for i := range parts {
		sub := strings.Join(parts[:i+1], "/")
		fi, err := os.Lstat(filepath.Join(r.root, filepath.FromSlash(sub)))
		if err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			return sub
		}
	}
	return ""
}

func (r *repository) protected(rel string) bool {
	if strings.HasPrefix(rel, r.protectedRoot+"/") {
		return true
	}
	for _, e := range r.protectedPaths {
		if rel == e || (strings.HasSuffix(e, "/") && strings.HasPrefix(rel, e)) {
			return true
		}
	}
	return false
}

func (r *repository) workDir(dir string) (string, error) {
	if dir == "" {
		return r.root, nil
	}
	if link := r.linkOnPath(dir); link != "" {
		return "", fmt.Errorf("%s is a symbolic link; the working folder must be a folder in the repository", link)
	}
	p := filepath.Join(r.root, filepath.FromSlash(dir))
	fi, err := os.Stat(p)
	switch {
	case err != nil:
		return "", fmt.Errorf("%s can't be used: %v", dir, pathProblem(err))
	case !fi.IsDir():
		return "", fmt.Errorf("%s isn't a folder", dir)
	}
	return p, nil
}

func (r *repository) checkArgFiles(args []string, dir string) error {
	wd, err := r.workDir(dir)
	if err != nil {
		return err
	}
	for i, a := range args {
		if a == "" || strings.HasPrefix(a, "-") || strings.ContainsRune(a, 0) {
			continue
		}
		p := a
		if !filepath.IsAbs(p) {
			p = filepath.Join(wd, filepath.FromSlash(a))
		}
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if err := r.checkObserved(p, fmt.Sprintf("argument %d (%s)", i+1, a)); err != nil {
			return err
		}
	}
	return nil
}

func pathProblem(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}
