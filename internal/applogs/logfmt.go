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

func parseLogfmt(line string) (map[string]logfmtValue, bool) {
	fields := map[string]logfmtValue{}
	s := strings.TrimRight(line, " ")
	for {
		s = strings.TrimLeft(s, " ")
		if s == "" {
			break
		}
		key, rest, ok := logfmtToken(s, false)
		if !ok || key.text == "" || !strings.HasPrefix(rest, "=") {
			return nil, false
		}
		value, rest, ok := logfmtToken(rest[1:], true)
		if !ok || (rest != "" && rest[0] != ' ') {
			return nil, false
		}
		if _, dup := fields[key.text]; !dup {
			fields[key.text] = value
		}
		s = rest
	}
	return fields, len(fields) > 0
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
