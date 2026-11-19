package integrity

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"proctor/internal/model"
)

type regularReader interface {
	readRegular(p string) ([]byte, bool, error)
}

type protectedFiles struct {
	src  Source
	root string
}

func (f protectedFiles) ReadProtected(rel string) (string, []byte, error) {
	if err := checkRelPath(rel); err != nil {
		return "", nil, fmt.Errorf("%v; name a file inside %s/ by its path relative to %s/", err, f.root, f.root)
	}
	full := f.root + "/" + rel
	if ext := strings.ToLower(path.Ext(rel)); ext == ".yaml" || ext == ".yml" {
		return full, nil, fmt.Errorf("%s is a YAML file; Proctor reads YAML under %s/ as configuration and specifications, so give a request file another extension", full, f.root)
	}
	rr, ok := f.src.(regularReader)
	if !ok {
		return full, nil, fmt.Errorf("%s: this source can't read request files", full)
	}
	data, found, err := rr.readRegular(full)
	switch {
	case err != nil:
		return full, nil, err
	case !found:
		return full, nil, fmt.Errorf("%s doesn't exist (the path is relative to %s/ and must match byte for byte)", full, f.root)
	case len(data) > model.MaxRequestFile:
		return full, nil, fmt.Errorf("%s is larger than %d bytes, the most a request file can hold; send a larger body with body_fill", full, model.MaxRequestFile)
	}
	return full, data, nil
}

func (s *CommitSource) readRegular(p string) ([]byte, bool, error) {
	parts := strings.Split(p, "/")
	for i := 1; i < len(parts); i++ {
		sub := strings.Join(parts[:i], "/")
		if e, ok := s.tree[sub]; ok {
			return nil, false, fmt.Errorf("%s is a %s, not a folder; a request file and the folders above it must be a regular file and folders in the commit (INT-13)", sub, kindOf(e))
		}
	}
	return s.ReadFile(p)
}

func (s WorkTreeSource) readRegular(p string) ([]byte, bool, error) {
	if err := checkRelPath(p); err != nil {
		return nil, false, err
	}
	root, err := os.OpenRoot(s.Root)
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	parts := strings.Split(p, "/")
	var last fs.FileInfo
	for i := range parts {
		sub := strings.Join(parts[:i+1], "/")
		fi, err := root.Lstat(filepath.FromSlash(sub))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, false, nil
		case err != nil:
			return nil, false, err
		case fi.Mode()&fs.ModeSymlink != 0:
			return nil, false, fmt.Errorf("%s is a symbolic link; a request file and the folders above it must be a regular file and folders in the repository (INT-13)", sub)
		case i < len(parts)-1 && !fi.IsDir():
			return nil, false, fmt.Errorf("%s isn't a folder", sub)
		}
		last = fi
	}
	if !last.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s isn't a regular file (INT-13)", p)
	}
	f, err := root.Open(filepath.FromSlash(p))
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !os.SameFile(opened, last) || !opened.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s changed while it was read", p)
	}
	data, err := io.ReadAll(io.LimitReader(f, model.MaxRequestFile+1))
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}
