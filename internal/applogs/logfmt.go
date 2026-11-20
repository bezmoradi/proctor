package applogs

import (
	"strconv"
	"strings"
)

type logfmtValue struct {
	text   string
	quoted bool
}

func (r Rules) logfmtEntry(line string) (Entry, bool) {
	fields, ok := parseLogfmt(line)
	if !ok {
		return Entry{}, false
	}
	lv, ok := fields[r.LevelField]
	if !ok {
		return Entry{}, false
	}
	e := Entry{Level: lv.text, Message: fields[r.MessageField].text, Time: fields["time"].text, pairs: fields}
	if !lv.quoted && numberText.MatchString(lv.text) {
		e.Level, e.Number = "", lv.text
	}
	return e, true
}

type logfmtPair struct {
	key        string
	value      logfmtValue
	start, end int
}

func parseLogfmt(line string) (map[string]logfmtValue, bool) {
	pairs, ok := scanLogfmt(line)
	if !ok {
		return nil, false
	}
	fields := map[string]logfmtValue{}
	for _, p := range pairs {
		if _, dup := fields[p.key]; !dup {
			fields[p.key] = p.value
		}
	}
	return fields, true
}

func scanLogfmt(line string) ([]logfmtPair, bool) {
	var pairs []logfmtPair
	base := strings.TrimRight(line, " ")
	s := base
	for {
		s = strings.TrimLeft(s, " ")
		if s == "" {
			break
		}
		key, rest, ok := logfmtToken(s, false)
		if !ok || key.text == "" || !strings.HasPrefix(rest, "=") {
			return nil, false
		}
		start := len(base) - len(rest) + 1
		value, rest, ok := logfmtToken(rest[1:], true)
		if !ok || (rest != "" && rest[0] != ' ') {
			return nil, false
		}
		pairs = append(pairs, logfmtPair{key: key.text, value: value, start: start, end: len(base) - len(rest)})
		s = rest
	}
	return pairs, len(pairs) > 0
}

func logfmtToken(s string, value bool) (logfmtValue, string, bool) {
	if strings.HasPrefix(s, `"`) {
		q, err := strconv.QuotedPrefix(s)
		if err != nil {
			return logfmtValue{}, "", false
		}
		t, err := strconv.Unquote(q)
		if err != nil {
			return logfmtValue{}, "", false
		}
		return logfmtValue{text: t, quoted: true}, s[len(q):], true
	}
	end := 0
	for end < len(s) {
		c := s[end]
		if c == ' ' || (c == '=' && !value) {
			break
		}
		if c < ' ' || c == 0x7f || c == '"' || c == '=' {
			return logfmtValue{}, "", false
		}
		end++
	}
	return logfmtValue{text: s[:end]}, s[end:], true
}
