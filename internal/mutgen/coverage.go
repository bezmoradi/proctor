package mutgen

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type block struct {
	startLine, startCol, endLine, endCol int
	count                                int64
}

type Coverage struct {
	blocks    map[string][]block
	unmatched map[string]bool
	entries   int
}

type module struct {
	path, dir string
}

func ReadCoverage(profile, root string, files []string) (*Coverage, error) {
	data, err := os.ReadFile(profile)
	if err != nil {
		return nil, fmt.Errorf("--coverage: %v", err)
	}
	mods, err := modules(root, files)
	if err != nil {
		return nil, err
	}
	listed := map[string]bool{}
	for _, f := range files {
		listed[f] = true
	}
	c := &Coverage{blocks: map[string][]block{}, unmatched: map[string]bool{}}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimRight(sc.Text(), "\r")
		if n == 1 {
			if !strings.HasPrefix(line, "mode: ") {
				return nil, fmt.Errorf("--coverage %s: the first line must be \"mode: set\", \"mode: count\" or \"mode: atomic\"; give a text profile from go tool covdata textfmt or -coverprofile", profile)
			}
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "mode: ") {
			continue
		}
		file, b, err := parseBlock(line)
		if err != nil {
			return nil, fmt.Errorf("--coverage %s: line %d: %v", profile, n, err)
		}
		c.entries++
		rel, ok := mapPath(file, root, mods, listed)
		if !ok {
			c.unmatched[file] = true
			continue
		}
		c.blocks[rel] = append(c.blocks[rel], b)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("--coverage %s: %v", profile, err)
	}
	if c.entries == 0 {
		return nil, fmt.Errorf("--coverage %s has no blocks", profile)
	}
	if len(c.blocks) == 0 {
		return nil, fmt.Errorf("--coverage %s names none of the repository's files (%d blocks, none under the module paths in its go.mod files); was it made from this repository?", profile, c.entries)
	}
	return c, nil
}

func parseBlock(line string) (string, block, error) {
	var b block
	sp := strings.LastIndexByte(line, ' ')
	if sp < 0 {
		return "", b, fmt.Errorf("expected \"file:start,end statements count\"")
	}
	count, err := strconv.ParseInt(line[sp+1:], 10, 64)
	if err != nil {
		return "", b, fmt.Errorf("bad count %q", line[sp+1:])
	}
	rest := line[:sp]
	sp = strings.LastIndexByte(rest, ' ')
	if sp < 0 {
		return "", b, fmt.Errorf("expected \"file:start,end statements count\"")
	}
	if _, err := strconv.Atoi(rest[sp+1:]); err != nil {
		return "", b, fmt.Errorf("bad statement count %q", rest[sp+1:])
	}
	rest = rest[:sp]
	colon := strings.LastIndexByte(rest, ':')
	if colon < 0 {
		return "", b, fmt.Errorf("expected \"file:start,end\"")
	}
	file, span := rest[:colon], rest[colon+1:]
	start, end, ok := strings.Cut(span, ",")
	if !ok {
		return "", b, fmt.Errorf("bad range %q", span)
	}
	var e1, e2 error
	b.startLine, b.startCol, e1 = lineCol(start)
	b.endLine, b.endCol, e2 = lineCol(end)
	if e1 != nil || e2 != nil {
		return "", b, fmt.Errorf("bad range %q", span)
	}
	b.count = count
	return file, b, nil
}

func lineCol(s string) (int, int, error) {
	l, c, ok := strings.Cut(s, ".")
	if !ok {
		return 0, 0, fmt.Errorf("bad position")
	}
	ln, err := strconv.Atoi(l)
	if err != nil {
		return 0, 0, err
	}
	cn, err := strconv.Atoi(c)
	if err != nil {
		return 0, 0, err
	}
	return ln, cn, nil
}

func modules(root string, files []string) ([]module, error) {
	var mods []module
	for _, f := range files {
		if path.Base(f) != "go.mod" || skippedDir(path.Dir(f)) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		mp := modulePath(data)
		if mp == "" {
			continue
		}
		dir := path.Dir(f)
		if dir == "." {
			dir = ""
		}
		mods = append(mods, module{path: mp, dir: dir})
	}
	sort.Slice(mods, func(i, j int) bool { return len(mods[i].path) > len(mods[j].path) })
	return mods, nil
}

func modulePath(data []byte) string {
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if i := strings.Index(l, "//"); i >= 0 {
			l = strings.TrimSpace(l[:i])
		}
		if rest, ok := strings.CutPrefix(l, "module"); ok && (strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\t")) {
			rest = strings.TrimSpace(rest)
			if u, err := strconv.Unquote(rest); err == nil {
				return u
			}
			return rest
		}
	}
	return ""
}

func mapPath(file, root string, mods []module, listed map[string]bool) (string, bool) {
	if filepath.IsAbs(file) {
		if rel, err := filepath.Rel(root, file); err == nil && !strings.HasPrefix(rel, "..") {
			rel = filepath.ToSlash(rel)
			return rel, listed[rel]
		}
		return "", false
	}
	for _, m := range mods {
		if rest, ok := strings.CutPrefix(file, m.path+"/"); ok {
			rel := rest
			if m.dir != "" {
				rel = m.dir + "/" + rest
			}
			return rel, listed[rel]
		}
	}
	return "", false
}

func (c *Coverage) covers(file string, line, col int) string {
	bs := c.blocks[file]
	if len(bs) == 0 {
		return "no"
	}
	inside := false
	for _, b := range bs {
		if before(line, col, b.startLine, b.startCol) || before(b.endLine, b.endCol, line, col) {
			continue
		}
		inside = true
		if b.count > 0 {
			return "yes"
		}
	}
	if inside {
		return "no"
	}
	for _, b := range bs {
		if b.count > 0 && b.startLine <= line && line <= b.endLine {
			return "yes"
		}
	}
	return "no"
}

func (c *Coverage) packageRan(file string) string {
	dir := path.Dir(file)
	for f, bs := range c.blocks {
		if path.Dir(f) != dir {
			continue
		}
		for _, b := range bs {
			if b.count > 0 {
				return "yes"
			}
		}
	}
	return "no"
}

func before(l1, c1, l2, c2 int) bool {
	return l1 < l2 || (l1 == l2 && c1 < c2)
}

func (c *Coverage) check(files []*goFile) []string {
	var warn []string
	missing := 0
	for _, f := range files {
		if len(f.cands) > 0 && len(c.blocks[f.path]) == 0 {
			missing++
		}
	}
	if missing > 0 {
		warn = append(warn, fmt.Sprintf("%d scanned files with candidates aren't in the coverage profile; their lines count as not run", missing))
	}
	if len(c.unmatched) > 0 {
		warn = append(warn, fmt.Sprintf("%d files named in the coverage profile aren't files of this repository (another module, or files Git doesn't list); they were ignored", len(c.unmatched)))
	}
	return warn
}
