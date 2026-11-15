package applogs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

type Mark struct {
	Offset int64
	info   os.FileInfo
}

type Window struct {
	Lines []string
	Note  string
}

type Source interface {
	Mark() (Mark, error)
	Since(m Mark) (Window, error)
}

type File struct {
	Path string
}

const backChunk = 64 << 10

func (f File) open() (*os.File, os.FileInfo, error) {
	fh, err := os.Open(f.Path)
	if err != nil {
		return nil, nil, err
	}
	st, err := fh.Stat()
	if err != nil {
		fh.Close()
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		fh.Close()
		return nil, nil, fmt.Errorf("%s is not a regular file", f.Path)
	}
	return fh, st, nil
}

func (f File) Mark() (Mark, error) {
	fh, st, err := f.open()
	if err != nil {
		return Mark{}, err
	}
	defer fh.Close()
	off, err := lineStart(fh, st.Size())
	if err != nil {
		return Mark{}, err
	}
	return Mark{Offset: off, info: st}, nil
}

func lineStart(r io.ReaderAt, size int64) (int64, error) {
	end := size
	buf := make([]byte, backChunk)
	for end > 0 {
		start := end - backChunk
		if start < 0 {
			start = 0
		}
		n, err := r.ReadAt(buf[:end-start], start)
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if i := bytes.LastIndexByte(buf[:n], '\n'); i >= 0 {
			return start + int64(i) + 1, nil
		}
		end = start
	}
	return 0, nil
}

func (f File) Since(m Mark) (Window, error) {
	fh, st, err := f.open()
	if err != nil {
		return Window{}, err
	}
	defer fh.Close()
	var w Window
	start := m.Offset
	switch {
	case m.info != nil && !os.SameFile(m.info, st):
		start = 0
		w.Note = "the log file was replaced while the scenario ran (rotated?); its new file was read from the start"
	case st.Size() < m.Offset:
		start = 0
		w.Note = fmt.Sprintf("the log file shrank below the scenario's start position (%d bytes, now %d; truncated or rotated); it was read from the start", m.Offset, st.Size())
	}
	if _, err := fh.Seek(start, io.SeekStart); err != nil {
		return Window{}, err
	}
	rd := bufio.NewReader(fh)
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return w, nil
			}
			return Window{}, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		w.Lines = append(w.Lines, line)
	}
}

type Rules struct {
	Levels       []string
	LevelField   string
	MessageField string
}

type Entry struct {
	Level   string
	Message string
	Time    string
	Line    string
}

var runtimePrefixes = []struct{ prefix, level string }{
	{"panic:", "PANIC"},
	{"fatal error:", "FATAL"},
}

func (r Rules) failing(level string) bool {
	u := strings.ToUpper(level)
	for _, l := range r.Levels {
		if strings.ToUpper(l) == u {
			return true
		}
	}
	return false
}

func (r Rules) Candidate(line string) (Entry, bool) {
	if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "{") {
		dec := json.NewDecoder(strings.NewReader(trimmed))
		dec.UseNumber()
		var obj map[string]any
		if err := dec.Decode(&obj); err == nil && !dec.More() {
			level, ok := obj[r.LevelField].(string)
			if !ok || !r.failing(level) {
				return Entry{}, false
			}
			return Entry{Level: level, Message: text(obj[r.MessageField]), Time: text(obj["time"]), Line: line}, true
		}
	}
	for _, p := range runtimePrefixes {
		if strings.HasPrefix(line, p.prefix) {
			if !r.failing(p.level) {
				return Entry{}, false
			}
			return Entry{Level: p.level, Message: strings.TrimSpace(strings.TrimPrefix(line, p.prefix)), Line: line}, true
		}
	}
	return Entry{}, false
}

func text(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
