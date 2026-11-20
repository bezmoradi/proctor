package mutgen

import (
	"fmt"
	"path"
	"strings"
)

type glob [][]string

func compileGlobs(flag string, raw []string) (glob, error) {
	var out glob
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("%s has an empty pattern", flag)
		}
		if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
			return nil, fmt.Errorf("%s pattern %q must be a slash-separated path relative to the repository root", flag, p)
		}
		p = strings.TrimPrefix(p, "./")
		if strings.HasSuffix(p, "/") {
			p += "**"
		}
		segs := strings.Split(p, "/")
		for _, s := range segs {
			if s == "**" {
				continue
			}
			if _, err := path.Match(s, ""); err != nil {
				return nil, fmt.Errorf("%s pattern %q: %v", flag, p, err)
			}
		}
		out = append(out, segs)
	}
	return out, nil
}

func (g glob) any(p string) bool {
	parts := strings.Split(p, "/")
	for _, segs := range g {
		if matchSegs(segs, parts) {
			return true
		}
	}
	return false
}

func matchSegs(pat, parts []string) bool {
	if len(pat) == 0 {
		return len(parts) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(parts); i++ {
			if matchSegs(pat[1:], parts[i:]) {
				return true
			}
		}
		return false
	}
	if len(parts) == 0 {
		return false
	}
	ok, _ := path.Match(pat[0], parts[0])
	return ok && matchSegs(pat[1:], parts[1:])
}
