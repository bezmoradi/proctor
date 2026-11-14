// Command selfmut lists mutation sites in Proctor's own Go source, for
// mutation-testing/selfmut.py. It parses each file with go/parser, so it
// mutates real operators and identifiers, never text inside strings or
// comments, and prints one JSON object per site.
//
//	go run ./mutation-testing/selfmut internal/runner/*.go ...
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
)

type site struct {
	File  string `json:"file"`
	Line  int    `json:"line"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	Old   string `json:"old"`
	New   string `json:"new"`
	Op    string `json:"op"`
	Func  string `json:"func"`
}

var swaps = map[token.Token][]string{
	token.EQL:  {"!="},
	token.NEQ:  {"=="},
	token.LSS:  {"<="},
	token.LEQ:  {"<"},
	token.GTR:  {">="},
	token.GEQ:  {">"},
	token.LAND: {"||"},
	token.LOR:  {"&&"},
	token.ADD:  {"-"},
	token.SUB:  {"+"},
}

var results = map[string]string{"Pass": "Fail", "Fail": "Pass", "Error": "Pass", "NotRun": "Pass"}

func main() {
	var out []site
	for _, path := range os.Args[1:] {
		src, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fn := ""
		add := func(start, end token.Pos, repl, op string) {
			s, e := fset.Position(start).Offset, fset.Position(end).Offset
			out = append(out, site{File: path, Line: fset.Position(start).Line, Start: s, End: e, Old: string(src[s:e]), New: repl, Op: op, Func: fn})
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			fn = fd.Name.Name
			ast.Inspect(fd, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BinaryExpr:
					for _, r := range swaps[x.Op] {
						add(x.OpPos, x.OpPos+token.Pos(len(x.Op.String())), r, "operator")
					}
					if lit, ok := x.Y.(*ast.BasicLit); ok && lit.Kind == token.INT {
						if v, err := strconv.Atoi(lit.Value); err == nil {
							add(lit.Pos(), lit.End(), strconv.Itoa(v+1), "constant")
						}
					}
				case *ast.UnaryExpr:
					if x.Op == token.NOT {
						add(x.Pos(), x.X.Pos(), "", "negation")
					}
				case *ast.Ident:
					switch x.Name {
					case "true":
						add(x.Pos(), x.End(), "false", "boolean")
					case "false":
						add(x.Pos(), x.End(), "true", "boolean")
					}
					if r, ok := results[x.Name]; ok {
						add(x.Pos(), x.End(), r, "result")
					}
				case *ast.BlockStmt:
					for _, st := range x.List {
						switch s := st.(type) {
						case *ast.ExprStmt:
							if _, ok := s.X.(*ast.CallExpr); ok {
								add(s.Pos(), s.End(), "_ = 0", "statement")
							}
						case *ast.BranchStmt:
							if s.Tok == token.CONTINUE || s.Tok == token.BREAK {
								add(s.Pos(), s.End(), "_ = 0", "statement")
							}
						case *ast.ReturnStmt:
							if len(s.Results) == 0 {
								add(s.Pos(), s.End(), "_ = 0", "statement")
							}
						}
					}
				}
				return true
			})
		}
	}
	enc := json.NewEncoder(os.Stdout)
	for _, s := range out {
		_ = enc.Encode(s)
	}
}
