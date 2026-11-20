package mutgen

import (
	"go/ast"
	"go/token"
	"math"
	"strconv"
	"strings"
)

type fnCtx struct {
	name    string
	results *ast.FieldList
	decl    bool
	pkg     bool
	lits    int
	top     ast.Node
}

type walker struct {
	f     *goFile
	stack []ast.Node
	fns   []*fnCtx
}

func walkFile(f *goFile) {
	w := &walker{f: f}
	for _, d := range f.ast.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Body == nil {
				continue
			}
			w.fns = []*fnCtx{{name: funcName(d), results: d.Type.Results, decl: true, top: d.Body}}
			w.stack = []ast.Node{d}
			ast.Inspect(d.Body, w.visit)
		case *ast.GenDecl:
			if d.Tok != token.VAR && d.Tok != token.CONST {
				continue
			}
			for _, s := range d.Specs {
				vs, ok := s.(*ast.ValueSpec)
				if !ok || len(vs.Values) == 0 {
					continue
				}
				w.fns = []*fnCtx{{name: d.Tok.String() + " " + vs.Names[0].Name, pkg: true}}
				w.stack = []ast.Node{d, vs}
				for _, v := range vs.Values {
					ast.Inspect(v, w.visit)
				}
			}
		}
	}
}

func funcName(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name
	}
	return "(" + recvName(d.Recv.List[0].Type) + ")." + d.Name.Name
}

func recvName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return "*" + recvName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return recvName(t.X)
	case *ast.IndexListExpr:
		return recvName(t.X)
	case *ast.ParenExpr:
		return recvName(t.X)
	}
	return "?"
}

func (w *walker) visit(n ast.Node) bool {
	if n == nil {
		top := w.stack[len(w.stack)-1]
		w.stack = w.stack[:len(w.stack)-1]
		if _, ok := top.(*ast.FuncLit); ok && len(w.fns) > 1 {
			w.fns = w.fns[:len(w.fns)-1]
		}
		return true
	}
	if lit, ok := n.(*ast.FuncLit); ok {
		cur := w.fns[len(w.fns)-1]
		cur.lits++
		name := cur.name + "." + strconv.Itoa(cur.lits)
		if cur.decl {
			name = cur.name + ".func" + strconv.Itoa(cur.lits)
		}
		top := cur.top
		if top == nil {
			top = lit
		}
		w.fns = append(w.fns, &fnCtx{name: name, results: lit.Type.Results, top: top})
	}
	if _, ok := n.(*ast.GenDecl); ok && n.(*ast.GenDecl).Tok == token.TYPE {
		return false
	}
	w.candidates(n)
	w.stack = append(w.stack, n)
	return true
}

func (w *walker) parent() ast.Node {
	return w.stack[len(w.stack)-1]
}

func (w *walker) fn() *fnCtx { return w.fns[len(w.fns)-1] }

func (w *walker) add(op string, node ast.Node, repl string) {
	tf := w.f.tf
	start, end := tf.Offset(node.Pos()), tf.Offset(node.End())
	pos := tf.Position(node.Pos())
	fn := w.fn()
	w.f.cands = append(w.f.cands, &candidate{
		file: w.f, fn: fn.name, fnKey: w.f.path + "\x00" + fn.name, op: op,
		start: start, end: end, line: pos.Line, col: pos.Column, pkgLevel: fn.pkg,
		text: string(w.f.src[start:end]), repl: repl,
	})
}

func (w *walker) text(n ast.Node) string {
	return string(w.f.src[w.f.tf.Offset(n.Pos()):w.f.tf.Offset(n.End())])
}

var swaps = map[token.Token]struct {
	op string
	to token.Token
}{
	token.LSS:  {OpBoundary, token.LEQ},
	token.LEQ:  {OpBoundary, token.LSS},
	token.GTR:  {OpBoundary, token.GEQ},
	token.GEQ:  {OpBoundary, token.GTR},
	token.EQL:  {OpEquality, token.NEQ},
	token.NEQ:  {OpEquality, token.EQL},
	token.LAND: {OpLogic, token.LOR},
	token.LOR:  {OpLogic, token.LAND},
}

func (w *walker) candidates(n ast.Node) {
	switch n := n.(type) {
	case *ast.IfStmt:
		if n.Cond != nil {
			w.add(OpNegate, n.Cond, "!("+w.text(n.Cond)+")")
		}
	case *ast.ForStmt:
		if n.Cond != nil {
			w.add(OpNegate, n.Cond, "!("+w.text(n.Cond)+")")
		}
	case *ast.BinaryExpr:
		s, ok := swaps[n.Op]
		if !ok {
			return
		}
		w.add(s.op, n, w.swapOp(n, s.to))
	case *ast.ReturnStmt:
		w.swallow(n)
	case *ast.ExprStmt:
		call, ok := ast.Unparen(n.X).(*ast.CallExpr)
		if !ok || w.unsafeRemoval(n, call) {
			return
		}
		w.add(OpRemoveCall, n, w.emptyStmt())
	case *ast.DeferStmt:
		if w.unsafeRemoval(n, n.Call) {
			return
		}
		w.add(OpRemoveCall, n, w.emptyStmt())
	case *ast.Ident:
		if n.Name != "true" && n.Name != "false" || !w.valuePosition(n) {
			return
		}
		to := "false"
		if n.Name == "false" {
			to = "true"
		}
		if w.collides(n, to) {
			return
		}
		w.add(OpBool, n, to)
	case *ast.BasicLit:
		if n.Kind != token.INT || !w.valuePosition(n) || w.inIntExclusion(n) {
			return
		}
		to, ok := bumpInt(n.Value)
		if !ok || w.collides(n, to) {
			return
		}
		w.add(OpConstant, n, to)
	}
}

func (w *walker) emptyStmt() string {
	if _, ok := w.parent().(*ast.LabeledStmt); ok {
		return "_ = 0"
	}
	return ""
}

func (w *walker) swapOp(n *ast.BinaryExpr, to token.Token) string {
	tf := w.f.tf
	opAt := tf.Offset(n.OpPos)
	xs := w.text(n.X)
	mid := string(w.f.src[tf.Offset(n.X.End()):opAt]) + to.String() + string(w.f.src[opAt+len(n.Op.String()):tf.Offset(n.Y.Pos())])
	ys := w.text(n.Y)
	if n.Op != token.LAND && n.Op != token.LOR {
		return xs + mid + ys
	}
	if isLogicNode(n.X) {
		xs = "(" + xs + ")"
	}
	if isLogicNode(n.Y) {
		ys = "(" + ys + ")"
	}
	out := xs + mid + ys
	if isLogicNode(w.parent()) {
		out = "(" + out + ")"
	}
	return out
}

func isLogicNode(n ast.Node) bool {
	b, ok := n.(*ast.BinaryExpr)
	return ok && (b.Op == token.LAND || b.Op == token.LOR)
}

func (w *walker) swallow(r *ast.ReturnStmt) {
	fn := w.fn()
	if fn.results == nil || len(r.Results) == 0 {
		return
	}
	var types []ast.Expr
	for _, f := range fn.results.List {
		k := len(f.Names)
		if k == 0 {
			k = 1
		}
		for range k {
			types = append(types, f.Type)
		}
	}
	if len(types) != len(r.Results) {
		return
	}
	for i, t := range types {
		id, ok := t.(*ast.Ident)
		if !ok || id.Name != "error" {
			continue
		}
		e := r.Results[i]
		if v, ok := ast.Unparen(e).(*ast.Ident); ok && v.Name == "nil" {
			continue
		}
		if w.removesOnlyUse(e) {
			continue
		}
		w.add(OpSwallow, e, "nil")
	}
}

func (w *walker) unsafeRemoval(stmt ast.Stmt, call *ast.CallExpr) bool {
	if id, ok := ast.Unparen(call.Fun).(*ast.Ident); ok && id.Name == "panic" && w.fn().results != nil && len(w.fn().results.List) > 0 {
		return true
	}
	return w.removesOnlyUse(stmt)
}

func (w *walker) valuePosition(n ast.Node) bool {
	switch p := w.parent().(type) {
	case *ast.SelectorExpr:
		return p.Sel != n
	case *ast.KeyValueExpr:
		if p.Key == n {
			if len(w.stack) >= 2 {
				if cl, ok := w.stack[len(w.stack)-2].(*ast.CompositeLit); ok {
					if _, isMap := cl.Type.(*ast.MapType); isMap {
						return true
					}
				}
			}
			return false
		}
	case *ast.Field, *ast.LabeledStmt, *ast.BranchStmt:
		return false
	case *ast.AssignStmt:
		for _, l := range p.Lhs {
			if l == n {
				return false
			}
		}
	case *ast.ValueSpec:
		for _, l := range p.Names {
			if l == n {
				return false
			}
		}
	}
	return true
}

func (w *walker) inIntExclusion(n *ast.BasicLit) bool {
	for i := len(w.stack) - 1; i >= 0; i-- {
		switch a := w.stack[i].(type) {
		case *ast.ArrayType:
			if a.Len != nil && a.Len.Pos() <= n.Pos() && n.End() <= a.Len.End() {
				return true
			}
		case *ast.BinaryExpr:
			if a.Op == token.SHL || a.Op == token.SHR {
				return true
			}
		case *ast.AssignStmt:
			if a.Tok == token.SHL_ASSIGN || a.Tok == token.SHR_ASSIGN {
				return true
			}
		case *ast.Field:
			return true
		}
	}
	v, err := strconv.ParseUint(strings.ReplaceAll(n.Value, "_", ""), 0, 64)
	if err != nil {
		return true
	}
	switch v {
	case math.MaxInt8, math.MaxUint8, math.MaxInt16, math.MaxUint16, math.MaxInt32, math.MaxUint32, math.MaxInt64:
		return true
	}
	return false
}

func bumpInt(lit string) (string, bool) {
	clean := strings.ReplaceAll(lit, "_", "")
	v, err := strconv.ParseUint(clean, 0, 64)
	if err != nil || v == math.MaxUint64 {
		return "", false
	}
	n := v + 1
	lower := strings.ToLower(clean)
	switch {
	case strings.HasPrefix(lower, "0x"):
		digits := strconv.FormatUint(n, 16)
		if strings.ToUpper(clean[2:]) == clean[2:] {
			digits = strings.ToUpper(digits)
		}
		return clean[:2] + digits, true
	case strings.HasPrefix(lower, "0b"):
		return clean[:2] + strconv.FormatUint(n, 2), true
	case strings.HasPrefix(lower, "0o"):
		return clean[:2] + strconv.FormatUint(n, 8), true
	case len(clean) > 1 && clean[0] == '0':
		return "0" + strconv.FormatUint(n, 8), true
	}
	return strconv.FormatUint(n, 10), true
}

func litValue(e ast.Expr) (string, bool) {
	switch e := ast.Unparen(e).(type) {
	case *ast.BasicLit:
		if e.Kind != token.INT {
			return "", false
		}
		v, err := strconv.ParseUint(strings.ReplaceAll(e.Value, "_", ""), 0, 64)
		if err != nil {
			return "", false
		}
		return strconv.FormatUint(v, 10), true
	case *ast.Ident:
		if e.Name == "true" || e.Name == "false" {
			return e.Name, true
		}
	}
	return "", false
}

func normalized(to string) string {
	if to == "true" || to == "false" {
		return to
	}
	v, err := strconv.ParseUint(to, 0, 64)
	if err != nil {
		return to
	}
	return strconv.FormatUint(v, 10)
}

func (w *walker) collides(n ast.Expr, to string) bool {
	want := normalized(to)
	var siblings []ast.Expr
	p := w.parent()
	switch p := p.(type) {
	case *ast.CaseClause:
		if len(w.stack) >= 2 {
			if body, ok := w.stack[len(w.stack)-2].(*ast.BlockStmt); ok {
				for _, s := range body.List {
					if cc, ok := s.(*ast.CaseClause); ok {
						siblings = append(siblings, cc.List...)
					}
				}
			}
		}
	case *ast.KeyValueExpr:
		if p.Key == n && len(w.stack) >= 2 {
			if cl, ok := w.stack[len(w.stack)-2].(*ast.CompositeLit); ok {
				for _, e := range cl.Elts {
					if kv, ok := e.(*ast.KeyValueExpr); ok {
						siblings = append(siblings, kv.Key)
					}
				}
			}
		}
	case *ast.ValueSpec:
		if len(w.stack) >= 2 {
			if gd, ok := w.stack[len(w.stack)-2].(*ast.GenDecl); ok && gd.Tok == token.CONST {
				for _, s := range gd.Specs {
					if vs, ok := s.(*ast.ValueSpec); ok {
						siblings = append(siblings, vs.Values...)
					}
				}
			}
		}
	}
	for _, s := range siblings {
		if s == n {
			continue
		}
		if v, ok := litValue(s); ok && v == want {
			return true
		}
	}
	return false
}
