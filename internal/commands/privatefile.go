package commands

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func writePrivate(path string, data []byte) (err error) {
	if err := refuseNonRegular(path); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".proctor-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	closed := false
	defer func() {
		if err == nil {
			return
		}
		if !closed {
			f.Close()
		}
		os.Remove(tmp)
	}()
	if err = f.Chmod(0o600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	closed = true
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func refuseNonRegular(path string) error {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case fi.Mode().IsRegular():
		return nil
	case fi.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%s is a symbolic link; Proctor writes a report only to a new path or over a regular file, so remove the link or choose another path", path)
	case fi.IsDir():
		return fmt.Errorf("%s is a directory; give the path of the report file", path)
	}
	return fmt.Errorf("%s isn't a regular file (%s); Proctor writes a report only to a new path or over a regular file", path, fi.Mode().Type())
}

func openAppend(path string) (*os.File, error) {
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is a %s; Proctor appends the job summary only to a new path or a regular file", path, describeMode(fi.Mode()))
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE|noFollow, 0o644)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = fmt.Errorf("%s isn't a regular file (%s)", path, fi.Mode().Type())
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func describeMode(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return "symbolic link"
	case m.IsDir():
		return "directory"
	}
	return "special file (" + m.Type().String() + ")"
}
