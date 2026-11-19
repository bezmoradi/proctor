package mutgen

func (c *Coverage) Profiled(file string) bool { return len(c.blocks[file]) > 0 }

func (c *Coverage) LineRan(file string, line int) (tracked, ran bool) {
	for _, b := range c.blocks[file] {
		if b.startLine <= line && line <= b.endLine {
			tracked = true
			if b.count > 0 {
				return true, true
			}
		}
	}
	return tracked, false
}

func (c *Coverage) Unmatched() int { return len(c.unmatched) }
