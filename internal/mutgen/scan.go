package mutgen

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type goFile struct {
	path    string
	src     []byte
	tf      *token.File
	ast     *ast.File
	imports map[string]bool
	pkgUses map[string]int
	locals  map[ast.Node]*localInfo
	cands   []*candidate
}

type candidate struct {
	file     *goFile
	fn       string
	fnKey    string
	op       string
	start    int
	end      int
	line     int
	col      int
	text     string
	repl     string
	find     string
	replace  string
	covered  string
	mutated  string
	rank     uint64
	position int
	pkgLevel bool
}

func skippedDir(dir string) bool {
	if dir == "." || dir == "" {
		return false
	}
	for _, s := range strings.Split(dir, "/") {
		if s == "vendor" || s == "testdata" || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "_") {
			return true
		}
	}
	return false
}

func scan(o Options, inc, exc glob, res *Result) ([]*goFile, error) {
	bctx := build.Default
	bctx.GOOS, bctx.GOARCH, bctx.CgoEnabled = o.GOOS, o.GOARCH, false
	var out []*goFile
	for _, p := range o.Files {
		if !strings.HasSuffix(p, ".go") {
			continue
		}
		reason := ""
		switch {
		case strings.HasSuffix(p, "_test.go"):
			reason = "test file"
		case skippedDir(path.Dir(p)):
			reason = "under vendor/, testdata/ or a folder Go ignores"
		case o.Protected != nil && o.Protected(p):
			reason = "protected input"
		case len(inc) > 0 && !inc.any(p):
			reason = "not matched by --include"
		case len(exc) > 0 && exc.any(p):
			reason = "matched by --exclude"
		}
		if reason != "" {
			res.Skipped[reason]++
			continue
		}
		full := filepath.Join(o.Root, filepath.FromSlash(p))
		fi, err := os.Lstat(full)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return nil, err
		case !fi.Mode().IsRegular():
			res.Skipped["not a regular file"]++
			continue
		}
		ok, err := bctx.MatchFile(filepath.Dir(full), filepath.Base(full))
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v; skipped", p, err))
			res.Skipped["unreadable build constraints"]++
			continue
		}
		if !ok {
			res.Skipped["not built for "+o.GOOS+"/"+o.GOARCH]++
			continue
		}
		src, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, p, src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s doesn't parse (%v); skipped", p, firstLine(err.Error())))
			res.Skipped["doesn't parse"]++
			continue
		}
		if ast.IsGenerated(af) {
			res.Skipped["generated"]++
			continue
		}
		f := &goFile{path: p, src: src, tf: fset.File(af.Pos()), ast: af, imports: importNames(af)}
		walkFile(f)
		res.Files++
		out = append(out, f)
	}
	return out, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func importNames(af *ast.File) map[string]bool {
	out := map[string]bool{}
	for _, im := range af.Imports {
		if im.Name != nil {
			if im.Name.Name != "_" && im.Name.Name != "." {
				out[im.Name.Name] = true
			}
			continue
		}
		p := strings.Trim(im.Path.Value, "\"`")
		parts := strings.Split(p, "/")
		last := parts[len(parts)-1]
		if len(parts) > 1 && isMajor(last) {
			last = parts[len(parts)-2]
		}
		if i := strings.Index(last, ".v"); i > 0 && isMajor(last[i+1:]) {
			last = last[:i]
		}
		out[last] = true
		trimmed := strings.TrimPrefix(strings.TrimSuffix(last, "-go"), "go-")
		out[trimmed] = true
		out[strings.ReplaceAll(trimmed, "-", "")] = true
		out[strings.ReplaceAll(last, "-", "")] = true
	}
	return out
}

func isMajor(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
