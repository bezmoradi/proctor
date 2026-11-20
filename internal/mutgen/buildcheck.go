package mutgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

var ErrUnchangedBuild = errors.New("the unchanged copy doesn't build")

type checker struct {
	o       Options
	env     []string
	copies  []string
	mods    []string
	mu      sync.Mutex
	vetOK   map[string]bool
	builds  map[string]bool
	checked int
}

func newChecker(ctx context.Context, o Options) (*checker, error) {
	k := &checker{o: o, vetOK: map[string]bool{}, builds: map[string]bool{}}
	for _, f := range o.Files {
		if path.Base(f) == "go.mod" && !skippedDir(path.Dir(f)) {
			k.mods = append(k.mods, path.Dir(f))
		}
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "GOOS", "GOARCH", "CGO_ENABLED":
			continue
		}
		k.env = append(k.env, kv)
	}
	k.env = append(k.env, "GOOS="+o.GOOS, "GOARCH="+o.GOARCH, "CGO_ENABLED=0")
	for range o.Jobs {
		dir, err := k.makeCopy()
		if err != nil {
			k.close()
			return nil, err
		}
		k.copies = append(k.copies, dir)
	}
	if !slices.Contains(k.mods, ".") {
		return k, nil
	}
	errs := make([]error, len(k.copies))
	var wg sync.WaitGroup
	for i, dir := range k.copies {
		wg.Go(func() {
			if out, err := k.goCmd(ctx, dir, "build", "-trimpath", "-o", os.DevNull, "./..."); err != nil {
				errs[i] = fmt.Errorf("--build-check: %w with GOOS=%s GOARCH=%s CGO_ENABLED=0 (go build ./...): %v\n%s", ErrUnchangedBuild, o.GOOS, o.GOARCH, err, tail(out))
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		k.close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errs[firstErr(errs)]
	}
	k.builds["."] = true
	if _, err := k.goCmd(ctx, k.copies[0], "vet", "-trimpath", "./..."); err == nil {
		k.vetOK[".\x00./..."] = true
	} else if ctx.Err() != nil {
		k.close()
		return nil, ctx.Err()
	}
	return k, nil
}

func firstErr(errs []error) int {
	for i, e := range errs {
		if e != nil {
			return i
		}
	}
	return 0
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 2000 {
		s = "…" + s[len(s)-2000:]
	}
	return s
}

func (k *checker) goCmd(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Env = k.env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.Bytes(), err
}

func (k *checker) makeCopy() (string, error) {
	dir, err := os.MkdirTemp("", "proctor-mutgen-*")
	if err != nil {
		return "", err
	}
	for _, f := range k.o.Files {
		src := filepath.Join(k.o.Root, filepath.FromSlash(f))
		dst := filepath.Join(dir, filepath.FromSlash(f))
		fi, err := os.Lstat(src)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			os.RemoveAll(dir)
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(src)
			if err == nil {
				err = os.Symlink(target, dst)
			}
			if err != nil {
				os.RemoveAll(dir)
				return "", err
			}
		case fi.Mode().IsRegular():
			if err := copyRegular(src, dst, fi.Mode().Perm()); err != nil {
				os.RemoveAll(dir)
				return "", err
			}
		}
	}
	return dir, nil
}

func copyRegular(src, dst string, mode fs.FileMode) error {
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
	return out.Close()
}

func (k *checker) close() {
	for _, d := range k.copies {
		if err := os.RemoveAll(d); err != nil {
			_ = filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
				if err == nil && e.IsDir() {
					_ = os.Chmod(p, 0o700)
				}
				return nil
			})
			_ = os.RemoveAll(d)
		}
	}
	k.copies = nil
}

func (k *checker) check(ctx context.Context, batch []*candidate) ([]string, error) {
	verdicts := make([]string, len(batch))
	errs := make([]error, len(k.copies))
	work := make(chan int)
	var wg sync.WaitGroup
	for w, dir := range k.copies {
		wg.Go(func() {
			for i := range work {
				if errs[w] != nil {
					continue
				}
				v, err := k.one(ctx, dir, batch[i])
				if err != nil {
					errs[w] = err
					continue
				}
				verdicts[i] = v
				k.progress()
			}
		})
	}
	for i := range batch {
		if ctx.Err() != nil {
			break
		}
		work <- i
	}
	close(work)
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return verdicts, nil
}

func (k *checker) progress() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.checked++
	if k.o.Log != nil && k.checked%25 == 0 {
		fmt.Fprintf(k.o.Log, "proctor mutants generate: build check: %d mutants checked\n", k.checked)
	}
}

func (k *checker) module(file string) string {
	best := ""
	for _, m := range k.mods {
		if m == "." {
			if best == "" {
				best = "."
			}
			continue
		}
		if strings.HasPrefix(file, m+"/") && len(m) > len(best) {
			best = m
		}
	}
	return best
}

func (k *checker) one(ctx context.Context, dir string, c *candidate) (verdict string, err error) {
	mod := k.module(c.file.path)
	if mod == "" {
		return "dropped: no go.mod above the file", nil
	}
	modDir := filepath.Join(dir, filepath.FromSlash(mod))
	rel := strings.TrimPrefix(path.Dir(c.file.path), mod+"/")
	pkg := "./" + rel
	if mod == "." {
		pkg = "./" + path.Dir(c.file.path)
	}
	if path.Dir(c.file.path) == mod {
		pkg = "."
	}
	if !k.baselineBuild(ctx, modDir, mod) {
		return "dropped: the unchanged module doesn't build", nil
	}
	if !k.baselineVet(ctx, modDir, mod, pkg) {
		return "dropped: go vet fails on the unchanged package", nil
	}
	target := filepath.Join(dir, filepath.FromSlash(c.file.path))
	fi, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(target, []byte(c.mutated), fi.Mode().Perm()); err != nil {
		return "", err
	}
	defer func() {
		if rerr := os.WriteFile(target, c.file.src, fi.Mode().Perm()); rerr != nil && err == nil {
			err = rerr
		}
	}()
	if _, err := k.goCmd(ctx, modDir, "build", "-trimpath", "-o", os.DevNull, "./..."); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "dropped: doesn't build", nil
	}
	if _, err := k.goCmd(ctx, modDir, "vet", "-trimpath", pkg); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "dropped: go vet fails", nil
	}
	return "", nil
}

func (k *checker) baselineBuild(ctx context.Context, modDir, mod string) bool {
	k.mu.Lock()
	ok, known := k.builds[mod]
	k.mu.Unlock()
	if known {
		return ok
	}
	_, err := k.goCmd(ctx, modDir, "build", "-trimpath", "-o", os.DevNull, "./...")
	k.mu.Lock()
	k.builds[mod] = err == nil
	k.mu.Unlock()
	return err == nil
}

func (k *checker) baselineVet(ctx context.Context, modDir, mod, pkg string) bool {
	k.mu.Lock()
	if k.vetOK[mod+"\x00./..."] {
		k.mu.Unlock()
		return true
	}
	ok, known := k.vetOK[mod+"\x00"+pkg]
	k.mu.Unlock()
	if known {
		return ok
	}
	_, err := k.goCmd(ctx, modDir, "vet", "-trimpath", pkg)
	k.mu.Lock()
	k.vetOK[mod+"\x00"+pkg] = err == nil
	k.mu.Unlock()
	return err == nil
}
