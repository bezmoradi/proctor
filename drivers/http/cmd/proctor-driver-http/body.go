package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"proctor/protocol"
)

const (
	formBody   = "body"
	formText   = "body_text"
	formBase64 = "body_base64"
	formFill   = "body_fill"
	formFile   = "body_file"
	bodyRule   = "HTTP-14"
)

var bodyForms = []string{formBody, formText, formBase64, formFill, formFile}

func rawForms() []string { return []string{formBase64, formFill, formFile} }

func presentForms(args map[string]any) []string {
	var out []string
	for _, f := range bodyForms {
		if _, has := args[f]; has {
			out = append(out, f)
		}
	}
	return out
}

func decodeBase64(raw any) ([]byte, error) {
	s, isText := raw.(string)
	if !isText {
		return nil, fmt.Errorf("must be base64 text, not %s", show(raw))
	}
	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, s)
	if len(compact) > base64.StdEncoding.EncodedLen(maxInlineBody) {
		return nil, fmt.Errorf("holds more than %d bytes, the most a body written in a specification can hold; send a larger body with body_fill", maxInlineBody)
	}
	b, err := base64.StdEncoding.Strict().DecodeString(compact)
	if err != nil {
		return nil, fmt.Errorf("isn't valid base64 (standard alphabet with = padding; spaces and line breaks are ignored): %v", err)
	}
	if len(b) == 0 {
		return nil, errors.New("decodes to no bytes; leave the body out to send none")
	}
	return b, nil
}

type fill struct {
	size    int64
	pattern []byte
}

func parseFill(raw any) (fill, error) {
	m, isObject := raw.(map[string]any)
	if !isObject {
		return fill{}, fmt.Errorf("must be { bytes, pattern }, not %s", show(raw))
	}
	for _, k := range sortedKeys(m) {
		if k != "bytes" && k != "pattern" && k != "pattern_base64" {
			return fill{}, fmt.Errorf("has unknown key %q; it has only bytes, and pattern or pattern_base64", k)
		}
	}
	n, isNumber := m["bytes"].(json.Number)
	size, err := strconv.ParseInt(n.String(), 10, 64)
	if !isNumber || err != nil || size < 1 {
		return fill{}, fmt.Errorf("bytes must be a whole number of bytes, at least 1, not %s", show(m["bytes"]))
	}
	rawText, hasText := m["pattern"]
	rawB64, hasB64 := m["pattern_base64"]
	var pattern []byte
	switch {
	case hasText == hasB64:
		return fill{}, fmt.Errorf("needs exactly one of pattern (text) and pattern_base64 (any bytes), repeated to fill the body")
	case hasText:
		p, isText := rawText.(string)
		switch {
		case !isText:
			return fill{}, fmt.Errorf("pattern must be text, not %s", show(rawText))
		case strings.Contains(p, "${"):
			return fill{}, fmt.Errorf("pattern %q refers to a value; a fill is sent as written", p)
		}
		pattern = []byte(p)
	default:
		s, isText := rawB64.(string)
		if !isText {
			return fill{}, fmt.Errorf("pattern_base64 must be base64 text, not %s", show(rawB64))
		}
		b, err := base64.StdEncoding.Strict().DecodeString(s)
		if err != nil {
			return fill{}, fmt.Errorf("pattern_base64 isn't valid base64 (standard alphabet with = padding): %v", err)
		}
		pattern = b
	}
	if len(pattern) == 0 || len(pattern) > maxFillPattern {
		return fill{}, fmt.Errorf("pattern must be 1 to %d bytes, not %d", maxFillPattern, len(pattern))
	}
	return fill{size: size, pattern: pattern}, nil
}

func (f fill) reader() io.Reader {
	return &fillReader{pattern: f.pattern, left: f.size}
}

type fillReader struct {
	pattern []byte
	at      int
	left    int64
}

func (r *fillReader) Read(b []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	if int64(len(b)) > r.left {
		b = b[:r.left]
	}
	n := 0
	for n < len(b) {
		c := copy(b[n:], r.pattern[r.at:])
		n += c
		r.at = (r.at + c) % len(r.pattern)
	}
	r.left -= int64(n)
	return n, nil
}

func (f fill) sum() string {
	h := sha256.New()
	_, _ = io.Copy(h, f.reader())
	return hex.EncodeToString(h.Sum(nil))
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (v *validation) checkBodyForms(args map[string]any, method string, headers map[string]any, files map[string]protocol.FileInfo, limit int64) {
	forms := presentForms(args)
	switch {
	case len(forms) == 2 && forms[0] == formBody && forms[1] == formText:
		v.add("EXE-01", "$.with.body_text", "body and body_text can't both be set; use body for JSON or body_text for text sent as written")
	case len(forms) > 1:
		v.add(bodyRule, "$.with."+forms[1], "a request has one body, but %s are set; keep one of them", strings.Join(forms, ", "))
	}
	raw := false
	for _, f := range forms {
		if method == "HEAD" && args[f] != nil {
			v.add("EXE-01", "$.with."+f, "a HEAD request can't send a body")
		}
		at := "$.with." + f
		switch f {
		case formBase64:
			raw = true
			b, err := decodeBase64(args[f])
			switch {
			case err != nil:
				v.add(bodyRule, at, "%s %v", f, err)
			case int64(len(b)) > limit:
				v.add(bodyRule, at, "%s holds %d bytes, more than the policy's %s (%d)", f, len(b), maxBodyKey, limit)
			}
		case formFill:
			raw = true
			fl, err := parseFill(args[f])
			switch {
			case err != nil:
				v.add(bodyRule, at, "%s %v", f, err)
			case fl.size > limit:
				v.add(bodyRule, at, "%s asks for %d bytes, more than the policy's %s (%d); raise %s in the http driver's policy in proctor.yaml (at most %d) if the check needs it", f, fl.size, maxBodyKey, limit, maxBodyKey, maxBodyLimit)
			}
		case formFile:
			raw = true
			fi, ok := files[bodyFileArg]
			switch {
			case !ok:
			case fi.Size == 0:
				v.add(bodyRule, at, "%s %s is empty; leave the body out to send none", f, fi.Path)
			case fi.Size > limit:
				v.add(bodyRule, at, "%s %s holds %d bytes, more than the policy's %s (%d)", f, fi.Path, fi.Size, maxBodyKey, limit)
			}
		}
	}
	if raw && !hasHeader(headers, "content-type") {
		v.add(bodyRule, "$.with.headers", "a request with %s must set Content-Type in headers, since the driver can't tell what its bytes are", strings.Join(intersect(forms, rawForms()), " or "))
	}
}

func intersect(a, b []string) []string {
	var out []string
	for _, x := range a {
		for _, y := range b {
			if x == y {
				out = append(out, x)
			}
		}
	}
	return out
}

func hasHeader(headers map[string]any, lower string) bool {
	for name := range headers {
		if strings.ToLower(name) == lower {
			return true
		}
	}
	return false
}

type rawBody struct {
	reader io.Reader
	size   int64
	note   string
	fill   *fill
}

func buildRawBody(a requestArgs, files map[string]protocol.File) (*rawBody, error) {
	switch {
	case a.BodyBase64 != nil:
		b, err := decodeBase64(a.BodyBase64)
		if err != nil {
			return nil, fmt.Errorf("%s %v", formBase64, err)
		}
		return &rawBody{reader: bytes.NewReader(b), size: int64(len(b)), note: fmt.Sprintf("[%s: %d bytes, sha256 %s]", formBase64, len(b), sum(b))}, nil
	case a.BodyFill != nil:
		f, err := parseFill(a.BodyFill)
		if err != nil {
			return nil, fmt.Errorf("%s %v", formFill, err)
		}
		return &rawBody{reader: f.reader(), size: f.size, fill: &f}, nil
	case a.BodyFile != nil:
		f, ok := files[bodyFileArg]
		if !ok {
			return nil, fmt.Errorf("%s %s: the core sent no file for it", formFile, show(a.BodyFile))
		}
		if int64(len(f.Data)) != f.Size || sum(f.Data) != f.SHA256 {
			return nil, fmt.Errorf("%s %s: the bytes received don't match its size and sha256", formFile, f.Path)
		}
		return &rawBody{reader: bytes.NewReader(f.Data), size: f.Size, note: fmt.Sprintf("[%s %s: %d bytes, sha256 %s]", formFile, f.Path, f.Size, f.SHA256)}, nil
	}
	return nil, nil
}

func (b *rawBody) describe() string {
	if b.fill != nil {
		return fmt.Sprintf("[%s: %d bytes, %s repeated, sha256 %s]", formFill, b.size, shownPattern(b.fill.pattern), b.fill.sum())
	}
	return b.note
}

func rawTarget(args map[string]any, files map[string]protocol.FileInfo) string {
	switch {
	case args[formBase64] != nil:
		if b, err := decodeBase64(args[formBase64]); err == nil {
			return " " + formBase64 + " sha256:" + sum(b)
		}
		return " " + formBase64 + " " + show(args[formBase64])
	case args[formFill] != nil:
		return " " + formFill + " " + show(args[formFill])
	case args[formFile] != nil:
		if f, ok := files[bodyFileArg]; ok {
			return " " + formFile + " " + f.Path + " sha256:" + f.SHA256
		}
		return " " + formFile + " " + show(args[formFile])
	}
	return ""
}

func shownPattern(p []byte) string {
	if utf8.Valid(p) {
		return strconv.Quote(string(p))
	}
	return "base64 " + base64.StdEncoding.EncodeToString(p)
}
