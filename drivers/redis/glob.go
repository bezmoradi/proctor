package redis

import "errors"

type tokenKind int

const (
	tokByte tokenKind = iota
	tokOne
	tokStar
	tokClass
)

type token struct {
	kind tokenKind
	set  [256]bool
}

type glob []token

func (t token) bytes() [256]bool {
	switch t.kind {
	case tokOne:
		var all [256]bool
		for i := range all {
			all[i] = true
		}
		return all
	case tokStar:
		var none [256]bool
		return none
	}
	return t.set
}

func literalByte(b byte) token {
	t := token{kind: tokByte}
	t.set[b] = true
	return t
}

func literal(s string) glob {
	out := make(glob, 0, len(s))
	for i := 0; i < len(s); i++ {
		out = append(out, literalByte(s[i]))
	}
	return out
}

var errOpenClass = errors.New("a [ has no closing ]")

func parseGlob(s string) (glob, error) {
	var out glob
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '*':
			if len(out) == 0 || out[len(out)-1].kind != tokStar {
				out = append(out, token{kind: tokStar})
			}
		case '?':
			out = append(out, token{kind: tokOne})
		case '\\':
			if i+1 < len(s) {
				i++
			}
			out = append(out, literalByte(s[i]))
		case '[':
			t, next, err := parseClass(s, i+1)
			if err != nil {
				return nil, err
			}
			out = append(out, t)
			i = next
		default:
			out = append(out, literalByte(c))
		}
	}
	return out, nil
}

func parseClass(s string, i int) (token, int, error) {
	t := token{kind: tokClass}
	negate := i < len(s) && s[i] == '^'
	if negate {
		i++
	}
	for {
		if i >= len(s) {
			return token{}, 0, errOpenClass
		}
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			t.set[s[i+1]] = true
			i += 2
			continue
		case c == ']':
			if negate {
				for b := range t.set {
					t.set[b] = !t.set[b]
				}
			}
			return t, i, nil
		case i+2 < len(s) && s[i+1] == '-':
			lo, hi := c, s[i+2]
			if lo > hi {
				lo, hi = hi, lo
			}
			for b := int(lo); b <= int(hi); b++ {
				t.set[b] = true
			}
			i += 3
			continue
		}
		t.set[c] = true
		i++
	}
}

func (g glob) match(s string) bool {
	prev := make([]bool, len(s)+1)
	prev[0] = true
	for _, t := range g {
		cur := make([]bool, len(s)+1)
		if t.kind == tokStar {
			cur[0] = prev[0]
			for j := 1; j <= len(s); j++ {
				cur[j] = prev[j] || cur[j-1]
			}
		} else {
			set := t.bytes()
			for j := 1; j <= len(s); j++ {
				cur[j] = prev[j-1] && set[s[j-1]]
			}
		}
		prev = cur
	}
	return prev[len(s)]
}

func (g glob) within(outer glob) bool {
	memo := map[[2]int]bool{}
	var f func(i, j int) bool
	f = func(i, j int) bool {
		key := [2]int{i, j}
		if v, ok := memo[key]; ok {
			return v
		}
		var r bool
		switch {
		case j == len(outer):
			r = i == len(g)
		case outer[j].kind == tokStar:
			r = f(i, j+1) || (i < len(g) && f(i+1, j))
		case i == len(g) || g[i].kind == tokStar:
			r = false
		default:
			r = subset(g[i].bytes(), outer[j].bytes()) && f(i+1, j+1)
		}
		memo[key] = r
		return r
	}
	return f(0, 0)
}

func subset(a, b [256]bool) bool {
	for i := range a {
		if a[i] && !b[i] {
			return false
		}
	}
	return true
}
