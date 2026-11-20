package ses

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
)

const maxMIMEDepth = 16

type content struct {
	Data *string `json:"Data"`
}

type record struct {
	MessageID string `json:"message_id"`
	SentAt    string `json:"sent_at"`
	Request   struct {
		FromEmailAddress *string `json:"FromEmailAddress"`
		Destination      *struct {
			ToAddresses  []string `json:"ToAddresses"`
			CcAddresses  []string `json:"CcAddresses"`
			BccAddresses []string `json:"BccAddresses"`
		} `json:"Destination"`
		ReplyToAddresses []string `json:"ReplyToAddresses"`
		Content          *struct {
			Simple *struct {
				Subject *content `json:"Subject"`
				Body    *struct {
					Text *content `json:"Text"`
					Html *content `json:"Html"`
				} `json:"Body"`
			} `json:"Simple"`
			Raw *struct {
				Data []byte `json:"Data"`
			} `json:"Raw"`
		} `json:"Content"`
	} `json:"request"`
}

type email struct {
	MessageID string
	From      string
	To        []string
	Cc        []string
	Bcc       []string
	ReplyTo   []string
	Subject   string
	Text      string
	HTML      string
	SentAt    string
}

func (e email) fact() map[string]any {
	return map[string]any{
		"message_id": e.MessageID,
		"from":       e.From,
		"to":         list(e.To),
		"cc":         list(e.Cc),
		"bcc":        list(e.Bcc),
		"reply_to":   list(e.ReplyTo),
		"subject":    e.Subject,
		"text":       e.Text,
		"html":       e.HTML,
		"sent_at":    e.SentAt,
	}
}

func list(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

func text(c *content) string {
	if c == nil || c.Data == nil {
		return ""
	}
	return *c.Data
}

func bare(l []string) []string {
	out := make([]string, 0, len(l))
	for _, a := range l {
		out = append(out, bareAddress(a))
	}
	return out
}

func bareAddress(a string) string {
	a = strings.TrimSpace(a)
	if p, err := mail.ParseAddress(a); err == nil {
		return p.Address
	}
	return a
}

func toEmail(r record) email {
	q := r.Request
	e := email{MessageID: r.MessageID, SentAt: r.SentAt, To: []string{}, Cc: []string{}, Bcc: []string{}, ReplyTo: bare(q.ReplyToAddresses)}
	if q.FromEmailAddress != nil {
		e.From = bareAddress(*q.FromEmailAddress)
	}
	if d := q.Destination; d != nil {
		e.To, e.Cc, e.Bcc = bare(d.ToAddresses), bare(d.CcAddresses), bare(d.BccAddresses)
	}
	if q.Content == nil {
		return e
	}
	if s := q.Content.Simple; s != nil {
		e.Subject = text(s.Subject)
		if s.Body != nil {
			e.Text, e.HTML = text(s.Body.Text), text(s.Body.Html)
		}
		return e
	}
	if raw := q.Content.Raw; raw != nil {
		fillFromMIME(&e, raw.Data, q.Destination == nil || len(q.Destination.ToAddresses)+len(q.Destination.CcAddresses)+len(q.Destination.BccAddresses) == 0)
	}
	return e
}

var words = &mime.WordDecoder{CharsetReader: charsetReader}

func fillFromMIME(e *email, data []byte, recipientsFromHeaders bool) {
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return
	}
	h := msg.Header
	if subject, err := words.DecodeHeader(h.Get("Subject")); err == nil {
		e.Subject = subject
	} else {
		e.Subject = h.Get("Subject")
	}
	if e.From == "" {
		if l, err := h.AddressList("From"); err == nil && len(l) > 0 {
			e.From = l[0].Address
		}
	}
	if recipientsFromHeaders {
		e.To, e.Cc, e.Bcc = headerAddresses(h, "To"), headerAddresses(h, "Cc"), headerAddresses(h, "Bcc")
	}
	if len(e.ReplyTo) == 0 {
		e.ReplyTo = headerAddresses(h, "Reply-To")
	}
	walk(e, h, msg.Body, 0)
	e.Text = strings.ReplaceAll(e.Text, "\r\n", "\n")
	e.HTML = strings.ReplaceAll(e.HTML, "\r\n", "\n")
}

func headerAddresses(h mail.Header, key string) []string {
	l, err := h.AddressList(key)
	if err != nil {
		return []string{}
	}
	out := make([]string, len(l))
	for i, a := range l {
		out[i] = a.Address
	}
	return out
}

type partHeader interface {
	Get(key string) string
}

func walk(e *email, h partHeader, body io.Reader, depth int) {
	if depth > maxMIMEDepth {
		return
	}
	ct := h.Get("Content-Type")
	if ct == "" {
		ct = "text/plain; charset=us-ascii"
	}
	media, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return
	}
	if strings.HasPrefix(media, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return
		}
		mr := multipart.NewReader(body, boundary)
		for {
			part, err := mr.NextRawPart()
			if err != nil {
				return
			}
			walk(e, part.Header, part, depth+1)
		}
	}
	if disp, _, err := mime.ParseMediaType(h.Get("Content-Disposition")); err == nil && strings.EqualFold(disp, "attachment") {
		return
	}
	var target *string
	switch media {
	case "text/plain":
		target = &e.Text
	case "text/html":
		target = &e.HTML
	default:
		return
	}
	if *target != "" {
		return
	}
	decoded, err := io.ReadAll(transferDecoder(h.Get("Content-Transfer-Encoding"), body))
	if err != nil {
		return
	}
	*target = toUTF8(params["charset"], decoded)
}

func transferDecoder(cte string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, r)
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	}
	return r
}

func toUTF8(charset string, b []byte) string {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "iso-8859-1", "latin1", "latin-1", "us-ascii", "ascii":
		if isASCII(b) {
			return string(b)
		}
		r := make([]rune, len(b))
		for i, c := range b {
			r[i] = rune(c)
		}
		return string(r)
	}
	return string(b)
}

func isASCII(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return false
		}
	}
	return true
}

func charsetReader(charset string, r io.Reader) (io.Reader, error) {
	switch strings.ToLower(charset) {
	case "iso-8859-1", "latin1", "latin-1":
		b, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		return strings.NewReader(toUTF8(charset, b)), nil
	}
	return nil, fmt.Errorf("unsupported charset %q", charset)
}
