package mutgen

import (
	"go/ast"
	"go/token"
)

type localInfo struct {
	declared map[string]bool
	uses     map[string]int
}

func (w *walker) removesOnlyUse(removed ast.Node) bool {
	fn := w.fn()
	inside := map[string]int{}
	usesIn(removed, func(name string) { inside[name]++ })
	var li *localInfo
	if fn.top != nil {
		li = w.f.localsFor(fn.top)
		for name, k := range inside {
			if li.declared[name] && li.uses[name]-k <= 0 {
				return true
			}
		}
	}
	pkgsInside := map[string]int{}
	selectorBases(removed, func(name string) { pkgsInside[name]++ })
	if len(pkgsInside) == 0 {
		return false
	}
	if w.f.pkgUses == nil {
		w.f.pkgUses = map[string]int{}
		selectorBases(w.f.ast, func(name string) { w.f.pkgUses[name]++ })
	}
	for name, k := range pkgsInside {
		if !w.f.imports[name] {
			continue
		}
		if li != nil && li.declared[name] {
			continue
		}
		if w.f.pkgUses[name]-k <= 0 {
			return true
		}
	}
	return false
}

func (f *goFile) localsFor(top ast.Node) *localInfo {
	if li, ok := f.locals[top]; ok {
		return li
	}
	if f.locals == nil {
		f.locals = map[ast.Node]*localInfo{}
	}
	li := &localInfo{declared: map[string]bool{}, uses: map[string]int{}}
	declare := func(e ast.Expr) {
		if id, ok := e.(*ast.Ident); ok && id.Name != "_" {
			li.declared[id.Name] = true
		}
	}
	ast.Inspect(top, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE {
				for _, l := range n.Lhs {
					declare(l)
				}
			}
		case *ast.ValueSpec:
			for _, id := range n.Names {
				declare(id)
			}
		case *ast.RangeStmt:
			if n.Tok == token.DEFINE {
				if n.Key != nil {
					declare(n.Key)
				}
				if n.Value != nil {
					declare(n.Value)
				}
			}
		}
		return true
	})
	usesIn(top, func(name string) { li.uses[name]++ })
	f.locals[top] = li
	return li
}

func usesIn(root ast.Node, use func(string)) {
	var walk func(n ast.Node) bool
	walkExpr := func(e ast.Node) {
		if e != nil {
			ast.Inspect(e, walk)
		}
	}
	walk = func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Ident:
			if n.Name != "_" {
				use(n.Name)
			}
			return false
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE || n.Tok == token.ASSIGN {
				for _, l := range n.Lhs {
					if _, ok := l.(*ast.Ident); !ok {
						walkExpr(l)
					}
				}
			} else {
				for _, l := range n.Lhs {
					walkExpr(l)
				}
			}
			for _, r := range n.Rhs {
				walkExpr(r)
			}
			return false
		case *ast.ValueSpec:
			for _, v := range n.Values {
				walkExpr(v)
			}
			return false
		case *ast.RangeStmt:
			if n.Tok == token.ILLEGAL {
				walkExpr(n.Key)
				walkExpr(n.Value)
			} else {
				for _, kv := range []ast.Expr{n.Key, n.Value} {
					if _, ok := kv.(*ast.Ident); !ok && kv != nil {
						walkExpr(kv)
					}
				}
			}
			walkExpr(n.X)
			walkExpr(n.Body)
			return false
		case *ast.SelectorExpr:
			walkExpr(n.X)
			return false
		case *ast.KeyValueExpr:
			if _, ok := n.Key.(*ast.Ident); !ok {
				walkExpr(n.Key)
			}
			walkExpr(n.Value)
			return false
		case *ast.FuncLit:
			walkExpr(n.Body)
			return false
		case *ast.CompositeLit:
			for _, e := range n.Elts {
				walkExpr(e)
			}
			return false
		case *ast.LabeledStmt:
			walkExpr(n.Stmt)
			return false
		case *ast.BranchStmt:
			return false
		case *ast.Field, *ast.FieldList:
			return false
		}
		return true
	}
	ast.Inspect(root, walk)
}

func selectorBases(root ast.Node, base func(string)) {
	ast.Inspect(root, func(n ast.Node) bool {
		if s, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := s.X.(*ast.Ident); ok {
				base(id.Name)
			}
		}
		return true
	})
}
