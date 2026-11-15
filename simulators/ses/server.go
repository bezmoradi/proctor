package ses

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"
)

const (
	SendPath      = "/v2/email/outbound-emails"
	SentPath      = "/__proctor/sent"
	MaxRequest    = 50 << 20
	MaxRawMessage = 40 << 20
	MaxRecipients = 50
	MaxKept       = 10000
)

type Content struct {
	Data    *string `json:"Data,omitempty"`
	Charset *string `json:"Charset,omitempty"`
}

type Body struct {
	Text *Content `json:"Text,omitempty"`
	Html *Content `json:"Html,omitempty"`
}

type Header struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}

type Message struct {
	Subject     *Content        `json:"Subject,omitempty"`
	Body        *Body           `json:"Body,omitempty"`
	Headers     []Header        `json:"Headers,omitempty"`
	Attachments json.RawMessage `json:"Attachments,omitempty"`
}

type RawMessage struct {
	Data []byte `json:"Data,omitempty"`
}

type Template struct {
	TemplateName    *string         `json:"TemplateName,omitempty"`
	TemplateArn     *string         `json:"TemplateArn,omitempty"`
	TemplateData    *string         `json:"TemplateData,omitempty"`
	TemplateContent json.RawMessage `json:"TemplateContent,omitempty"`
	Headers         []Header        `json:"Headers,omitempty"`
}

type EmailContent struct {
	Simple   *Message    `json:"Simple,omitempty"`
	Raw      *RawMessage `json:"Raw,omitempty"`
	Template *Template   `json:"Template,omitempty"`
}

type Destination struct {
	ToAddresses  []string `json:"ToAddresses,omitempty"`
	CcAddresses  []string `json:"CcAddresses,omitempty"`
	BccAddresses []string `json:"BccAddresses,omitempty"`
}

type Tag struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}

type SendEmailInput struct {
	FromEmailAddress                          *string         `json:"FromEmailAddress,omitempty"`
	FromEmailAddressIdentityArn               *string         `json:"FromEmailAddressIdentityArn,omitempty"`
	Destination                               *Destination    `json:"Destination,omitempty"`
	ReplyToAddresses                          []string        `json:"ReplyToAddresses,omitempty"`
	FeedbackForwardingEmailAddress            *string         `json:"FeedbackForwardingEmailAddress,omitempty"`
	FeedbackForwardingEmailAddressIdentityArn *string         `json:"FeedbackForwardingEmailAddressIdentityArn,omitempty"`
	Content                                   *EmailContent   `json:"Content,omitempty"`
	EmailTags                                 []Tag           `json:"EmailTags,omitempty"`
	ConfigurationSetName                      *string         `json:"ConfigurationSetName,omitempty"`
	ListManagementOptions                     json.RawMessage `json:"ListManagementOptions,omitempty"`
	EndpointId                                *string         `json:"EndpointId,omitempty"`
	TenantName                                *string         `json:"TenantName,omitempty"`
}

type Sent struct {
	MessageID string         `json:"message_id"`
	SentAt    string         `json:"sent_at"`
	Request   SendEmailInput `json:"request"`
}

type Server struct {
	identities []string
	logger     *log.Logger
	mu         sync.Mutex
	sent       []Sent
	seq        uint64
}

func New(identities []string, logger *log.Logger) *Server {
	ids := make([]string, 0, len(identities))
	for _, id := range identities {
		if id = strings.ToLower(strings.TrimSpace(id)); id != "" {
			ids = append(ids, id)
		}
	}
	return &Server{identities: ids, logger: logger}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == SendPath && r.Method == http.MethodPost:
		s.send(w, r)
	case r.URL.Path == SentPath && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		s.list(w)
	case r.URL.Path == SentPath:
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	default:
		awsError(w, http.StatusNotFound, "UnknownOperationException", fmt.Sprintf("proctor-sim-ses answers only POST %s; %s %s is not an operation it knows", SendPath, r.Method, r.URL.Path))
	}
}

type awsFault struct {
	status int
	kind   string
	msg    string
}

func badRequest(format string, args ...any) *awsFault {
	return &awsFault{http.StatusBadRequest, "BadRequestException", fmt.Sprintf(format, args...)}
}

func awsError(w http.ResponseWriter, status int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Amzn-ErrorType", kind)
	w.Header().Set("X-Amzn-RequestId", requestID())
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(io.LimitReader(r.Body, MaxRequest+1))
	if err != nil {
		awsError(w, http.StatusBadRequest, "BadRequestException", "the request body could not be read")
		return
	}
	if len(b) > MaxRequest {
		awsError(w, http.StatusRequestEntityTooLarge, "BadRequestException", "Request is too large")
		return
	}
	var in SendEmailInput
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&in); err != nil {
		awsError(w, http.StatusBadRequest, "SerializationException", "the request body is not a valid SendEmail JSON document: "+err.Error())
		return
	}
	if fault := s.check(&in, signingRegion(r)); fault != nil {
		if s.logger != nil {
			s.logger.Printf("refused SendEmail: %s: %s", fault.kind, fault.msg)
		}
		awsError(w, fault.status, fault.kind, fault.msg)
		return
	}
	s.mu.Lock()
	s.seq++
	id := messageID(s.seq)
	s.sent = append(s.sent, Sent{MessageID: id, SentAt: time.Now().UTC().Format(time.RFC3339Nano), Request: in})
	if len(s.sent) > MaxKept {
		s.sent = append([]Sent(nil), s.sent[len(s.sent)-MaxKept:]...)
	}
	s.mu.Unlock()
	if s.logger != nil {
		s.logger.Printf("accepted SendEmail %s (%s content)", id, kind(in.Content))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Amzn-RequestId", requestID())
	_ = json.NewEncoder(w).Encode(map[string]string{"MessageId": id})
}

func (s *Server) list(w http.ResponseWriter) {
	s.mu.Lock()
	out := append([]Sent{}, s.sent...)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"emails": out})
}

func kind(c *EmailContent) string {
	switch {
	case c == nil:
		return "no"
	case c.Simple != nil:
		return "simple"
	case c.Raw != nil:
		return "raw"
	default:
		return "template"
	}
}

func (s *Server) check(in *SendEmailInput, region string) *awsFault {
	c := in.Content
	if c == nil {
		return badRequest("Content is required")
	}
	n := 0
	for _, set := range []bool{c.Simple != nil, c.Raw != nil, c.Template != nil} {
		if set {
			n++
		}
	}
	if n != 1 {
		return badRequest("Content must contain exactly one of Simple, Raw or Template")
	}
	var from string
	if in.FromEmailAddress != nil {
		from = strings.TrimSpace(*in.FromEmailAddress)
	}
	var headers mail.Header
	switch {
	case c.Simple != nil:
		m := c.Simple
		if m.Subject == nil || m.Subject.Data == nil {
			return badRequest("Subject is required")
		}
		if m.Body == nil || (m.Body.Text == nil && m.Body.Html == nil) {
			return badRequest("Body is required and must contain Text, Html or both")
		}
		for name, part := range map[string]*Content{"Text": m.Body.Text, "Html": m.Body.Html} {
			if part != nil && part.Data == nil {
				return badRequest("Body.%s.Data is required", name)
			}
		}
	case c.Raw != nil:
		if len(c.Raw.Data) == 0 {
			return badRequest("Raw.Data is required")
		}
		if len(c.Raw.Data) > MaxRawMessage {
			return &awsFault{http.StatusBadRequest, "MessageRejected", "Message length is more than 40 MB"}
		}
		msg, err := mail.ReadMessage(bytes.NewReader(c.Raw.Data))
		if err != nil {
			return &awsFault{http.StatusBadRequest, "MessageRejected", "the raw message is not a valid MIME message: " + err.Error()}
		}
		headers = msg.Header
		if from == "" {
			from = strings.TrimSpace(headers.Get("From"))
		}
	default:
		t := c.Template
		switch {
		case t.TemplateName != nil && *t.TemplateName != "":
			return &awsFault{http.StatusNotFound, "NotFoundException", fmt.Sprintf("Template %s does not exist.", *t.TemplateName)}
		case t.TemplateArn != nil && *t.TemplateArn != "":
			return &awsFault{http.StatusNotFound, "NotFoundException", fmt.Sprintf("Template %s does not exist.", *t.TemplateArn)}
		case len(t.TemplateContent) > 0:
			return badRequest("proctor-sim-ses does not render inline TemplateContent; send Simple or Raw content")
		}
		return badRequest("Template must name TemplateName, TemplateArn or TemplateContent")
	}
	if from == "" {
		return badRequest("Missing required field FromEmailAddress")
	}
	sender, err := mail.ParseAddress(from)
	if err != nil {
		return badRequest("Illegal address: FromEmailAddress %q: %v", from, err)
	}
	recipients := 0
	if d := in.Destination; d != nil {
		for _, list := range [][]string{d.ToAddresses, d.CcAddresses, d.BccAddresses} {
			for _, a := range list {
				if _, err := mail.ParseAddress(a); err != nil {
					return badRequest("Illegal address: %q: %v", a, err)
				}
				recipients++
			}
		}
	}
	if recipients == 0 && headers != nil {
		for _, h := range []string{"To", "Cc", "Bcc"} {
			list, err := headers.AddressList(h)
			if err != nil && !errors.Is(err, mail.ErrHeaderNotPresent) {
				return badRequest("Illegal address in the %s header: %v", h, err)
			}
			recipients += len(list)
		}
	}
	for _, a := range in.ReplyToAddresses {
		if _, err := mail.ParseAddress(a); err != nil {
			return badRequest("Illegal address: ReplyToAddresses %q: %v", a, err)
		}
	}
	if recipients == 0 {
		return badRequest("Missing required field Destination: the message has no recipients")
	}
	if recipients > MaxRecipients {
		return &awsFault{http.StatusBadRequest, "MessageRejected", fmt.Sprintf("Recipient count exceeds %d.", MaxRecipients)}
	}
	if !s.verified(sender.Address) {
		return &awsFault{http.StatusBadRequest, "MessageRejected", fmt.Sprintf("Email address is not verified. The following identities failed the check in region %s: %s", strings.ToUpper(region), sender.Address)}
	}
	return nil
}

func (s *Server) verified(address string) bool {
	if len(s.identities) == 0 {
		return true
	}
	address = strings.ToLower(address)
	domain := address[strings.LastIndex(address, "@")+1:]
	for _, id := range s.identities {
		if id == address || id == domain {
			return true
		}
	}
	return false
}

func signingRegion(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	_, cred, ok := strings.Cut(auth, "Credential=")
	if !ok {
		return "us-east-1"
	}
	cred, _, _ = strings.Cut(cred, ",")
	parts := strings.Split(cred, "/")
	if len(parts) >= 3 && parts[2] != "" {
		return parts[2]
	}
	return "us-east-1"
}

func messageID(seq uint64) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	h := hex.EncodeToString(b)
	return fmt.Sprintf("0100%012x-%s-%s-%s-%s-%012x-000000", time.Now().UnixMilli(), h[0:8], h[8:12], h[12:16], h[16:20], seq)
}

func requestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	h := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}
