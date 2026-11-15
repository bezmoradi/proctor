package sqs

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"proctor/protocol"
)

type sendInput struct {
	Queue        string            `json:"queue"`
	Body         any               `json:"body"`
	Attributes   map[string]string `json:"attributes"`
	GroupID      *string           `json:"group_id"`
	DedupID      *string           `json:"dedup_id"`
	DelaySeconds *int              `json:"delay_seconds"`
}

type peekInput struct {
	Queue string `json:"queue"`
	Max   *int   `json:"max"`
}

type depthInput struct {
	Queue string `json:"queue"`
}

func (d *Driver) Call(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	start := time.Now()
	if d.client == nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the sqs driver has no connection")
	}
	if p.Credential != nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "sqs operations take no credential")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, callTimeout)
		defer cancel()
	}
	var (
		res protocol.CallResult
		err error
	)
	switch p.Op {
	case OpSend:
		res, err = d.send(ctx, p.Args)
	case OpPeek:
		res, err = d.peek(ctx, p.Args)
	case OpDepth:
		res, err = d.depth(ctx, p.Args)
	default:
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "unknown operation %q", p.Op)
	}
	if err != nil {
		return protocol.CallResult{}, err
	}
	res.Evidence.DurationMS = time.Since(start).Milliseconds()
	return res, nil
}

func (d *Driver) target(ctx context.Context, alias string) (*queue, error) {
	q := d.queues[alias]
	if q == nil {
		return nil, protocol.Lastingf(protocol.CauseInvalidRequest, "queue %q is not declared in the sqs driver's policy", alias)
	}
	if err := d.resolve(ctx, q); err != nil {
		return nil, err
	}
	return q, nil
}

func (d *Driver) send(ctx context.Context, raw any) (protocol.CallResult, error) {
	var in sendInput
	if err := protocol.Decode(raw, &in); err != nil {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "send: %v", err)
	}
	if m, _ := raw.(map[string]any); m == nil || !hasKey(m, "body") {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "send: body is required")
	}
	text, err := bodyText(in.Body)
	if err != nil {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "send: body: %v", err)
	}
	if text == "" {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "send: body is empty; SQS needs at least one character")
	}
	q := d.queues[in.Queue]
	if q == nil {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "queue %q is not declared in the sqs driver's policy", in.Queue)
	}
	switch {
	case q.fifo() && (in.GroupID == nil || *in.GroupID == ""):
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "send: queue %s is a FIFO queue, so group_id is required", q.label())
	case !q.fifo() && in.GroupID != nil:
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "send: queue %s is a standard queue; group_id applies only to FIFO queues", q.label())
	case !q.fifo() && in.DedupID != nil:
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "send: queue %s is a standard queue; dedup_id applies only to FIFO queues", q.label())
	case in.DedupID != nil && *in.DedupID == "":
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "send: dedup_id is empty")
	case in.DelaySeconds != nil && (*in.DelaySeconds < 0 || *in.DelaySeconds > maxDelay):
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "send: delay_seconds must be from 0 to %d, not %d", maxDelay, *in.DelaySeconds)
	}
	if err := d.resolve(ctx, q); err != nil {
		return protocol.CallResult{}, err
	}
	input := &awssqs.SendMessageInput{
		QueueUrl:               aws.String(q.url),
		MessageBody:            aws.String(text),
		MessageGroupId:         in.GroupID,
		MessageDeduplicationId: in.DedupID,
	}
	var opts []func(*awssqs.Options)
	if in.DelaySeconds != nil {
		input.DelaySeconds = int32(*in.DelaySeconds)
		if *in.DelaySeconds == 0 {
			opts = append(opts, forceFields(map[string]int{"DelaySeconds": 0}))
		}
	}
	if len(in.Attributes) > 0 {
		input.MessageAttributes = map[string]types.MessageAttributeValue{}
		for name, v := range in.Attributes {
			input.MessageAttributes[name] = types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(v)}
		}
	}
	out, err := d.client.SendMessage(ctx, input, opts...)
	if err != nil {
		return protocol.CallResult{}, d.classify(ctx, err, "send to queue "+q.label())
	}
	id := aws.ToString(out.MessageId)
	if id == "" {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnreadableResponse, "send to queue %s: SendMessage returned no message id", q.label())
	}
	return protocol.CallResult{
		Fact: map[string]any{"message_id": id},
		Evidence: protocol.Evidence{
			Summary:        fmt.Sprintf("send to %s -> message %s", q.label(), id),
			RequestExcerpt: d.scrub(cut(text, excerptBytes)),
		},
	}, nil
}

func hasKey(m map[string]any, k string) bool {
	_, ok := m[k]
	return ok
}

func bodyText(body any) (string, error) {
	if s, ok := body.(string); ok {
		return s, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

func (d *Driver) peek(ctx context.Context, raw any) (protocol.CallResult, error) {
	var in peekInput
	if err := protocol.Decode(raw, &in); err != nil {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "peek: %v", err)
	}
	limit := maxPeek
	if in.Max != nil {
		limit = *in.Max
	}
	if limit < 1 || limit > maxPeek {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "peek: max must be from 1 to %d, not %d", maxPeek, limit)
	}
	q, err := d.target(ctx, in.Queue)
	if err != nil {
		return protocol.CallResult{}, err
	}
	want, err := d.onQueue(ctx, q)
	if err != nil {
		return protocol.CallResult{}, err
	}
	seen := map[string]bool{}
	messages := make([]any, 0)
	receives, quiet := 0, 0
	settled := func() bool {
		return quiet >= quietReceives && (int64(len(messages)) >= want || quiet >= quietBehind)
	}
	for !settled() && receives < maxReceives && len(messages) < limit {
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < receiveMargin {
			break
		}
		out, err := d.client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:              aws.String(q.url),
			MaxNumberOfMessages:   int32(min(receiveBatch, limit-len(messages))),
			VisibilityTimeout:     0,
			WaitTimeSeconds:       0,
			MessageAttributeNames: []string{"All"},
		}, forceFields(map[string]int{"VisibilityTimeout": 0, "WaitTimeSeconds": 0}))
		if err != nil {
			return protocol.CallResult{}, d.classify(ctx, err, "peek at queue "+q.label())
		}
		receives++
		added := 0
		for _, msg := range out.Messages {
			id := aws.ToString(msg.MessageId)
			if id == "" {
				return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnreadableResponse, "peek at queue %s: ReceiveMessage returned a message with no message id", q.label())
			}
			if seen[id] || len(messages) >= limit {
				continue
			}
			seen[id] = true
			added++
			text := aws.ToString(msg.Body)
			m := map[string]any{
				"message_id": id,
				"body_text":  text,
				"attributes": attributes(msg.MessageAttributes),
			}
			if v, ok := parseJSON(text); ok {
				m["body"] = v
			}
			messages = append(messages, m)
		}
		if added == 0 {
			quiet++
		} else {
			quiet = 0
		}
	}
	onQueue, err := d.onQueue(ctx, q)
	if err != nil {
		return protocol.CallResult{}, err
	}
	why := ""
	switch {
	case !settled() && len(messages) >= limit:
		why = fmt.Sprintf("stopped at max %d", limit)
	case !settled() && receives >= maxReceives:
		why = fmt.Sprintf("stopped after %d receives", receives)
	case !settled():
		why = "stopped before the call's deadline"
	case int64(len(messages)) < onQueue:
		why = fmt.Sprintf("the queue reports %d messages", onQueue)
	}
	noun := "messages"
	if len(messages) == 1 {
		noun = "message"
	}
	summary := fmt.Sprintf("peek at %s -> %d %s in %d receives (left on the queue)", q.label(), len(messages), noun, receives)
	if why != "" {
		summary += ", truncated: " + why
	}
	return protocol.CallResult{
		Fact:     map[string]any{"count": len(messages), "messages": messages, "truncated": why != ""},
		Evidence: protocol.Evidence{Summary: summary},
	}, nil
}

func (d *Driver) onQueue(ctx context.Context, q *queue) (int64, error) {
	names := []types.QueueAttributeName{
		types.QueueAttributeNameApproximateNumberOfMessages,
		types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
	}
	out, err := d.client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{QueueUrl: aws.String(q.url), AttributeNames: names})
	if err != nil {
		return 0, d.classify(ctx, err, "peek at queue "+q.label()+": counting its messages")
	}
	var total int64
	for _, name := range names {
		s, ok := out.Attributes[string(name)]
		if !ok {
			return 0, protocol.Errorf(protocol.CauseUnsupportedResult, "peek at queue %s: the answer has no %s", q.label(), name)
		}
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil || v < 0 {
			return 0, protocol.Errorf(protocol.CauseUnsupportedResult, "peek at queue %s: %s is %q, not a count", q.label(), name, s)
		}
		total += v
	}
	return total, nil
}

func attributes(in map[string]types.MessageAttributeValue) map[string]any {
	out := make(map[string]any, len(in))
	for name, v := range in {
		switch {
		case v.StringValue != nil:
			out[name] = *v.StringValue
		case v.BinaryValue != nil:
			out[name] = base64.StdEncoding.EncodeToString(v.BinaryValue)
		default:
			out[name] = ""
		}
	}
	return out
}

func (d *Driver) depth(ctx context.Context, raw any) (protocol.CallResult, error) {
	var in depthInput
	if err := protocol.Decode(raw, &in); err != nil {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "depth: %v", err)
	}
	q, err := d.target(ctx, in.Queue)
	if err != nil {
		return protocol.CallResult{}, err
	}
	names := []types.QueueAttributeName{
		types.QueueAttributeNameApproximateNumberOfMessages,
		types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		types.QueueAttributeNameApproximateNumberOfMessagesDelayed,
	}
	out, err := d.client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{QueueUrl: aws.String(q.url), AttributeNames: names})
	if err != nil {
		return protocol.CallResult{}, d.classify(ctx, err, "depth of queue "+q.label())
	}
	fact := map[string]any{}
	for i, key := range []string{"visible", "in_flight", "delayed"} {
		s, ok := out.Attributes[string(names[i])]
		if !ok {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnsupportedResult, "depth of queue %s: the answer has no %s", q.label(), names[i])
		}
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil || v < 0 {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnsupportedResult, "depth of queue %s: %s is %q, not a count", q.label(), names[i], s)
		}
		fact[key] = v
	}
	return protocol.CallResult{
		Fact:     fact,
		Evidence: protocol.Evidence{Summary: fmt.Sprintf("depth of %s -> %d visible, %d in flight, %d delayed", q.label(), fact["visible"], fact["in_flight"], fact["delayed"])},
	}, nil
}

func forceFields(fields map[string]int) func(*awssqs.Options) {
	return func(o *awssqs.Options) {
		o.APIOptions = append(o.APIOptions, func(stack *middleware.Stack) error {
			return stack.Serialize.Add(middleware.SerializeMiddlewareFunc("ProctorForceFields", func(ctx context.Context, in middleware.SerializeInput, next middleware.SerializeHandler) (middleware.SerializeOutput, middleware.Metadata, error) {
				req, ok := in.Request.(*smithyhttp.Request)
				if !ok {
					return middleware.SerializeOutput{}, middleware.Metadata{}, errors.New("unexpected request type")
				}
				var b []byte
				if s := req.GetStream(); s != nil {
					var err error
					if b, err = io.ReadAll(s); err != nil {
						return middleware.SerializeOutput{}, middleware.Metadata{}, err
					}
				}
				doc := map[string]json.RawMessage{}
				if len(bytes.TrimSpace(b)) > 0 {
					if err := json.Unmarshal(b, &doc); err != nil {
						return middleware.SerializeOutput{}, middleware.Metadata{}, fmt.Errorf("the request body isn't a JSON object: %w", err)
					}
				}
				for k, v := range fields {
					doc[k] = json.RawMessage(strconv.Itoa(v))
				}
				nb, err := json.Marshal(doc)
				if err != nil {
					return middleware.SerializeOutput{}, middleware.Metadata{}, err
				}
				if in.Request, err = req.SetStream(bytes.NewReader(nb)); err != nil {
					return middleware.SerializeOutput{}, middleware.Metadata{}, err
				}
				return next.HandleSerialize(ctx, in)
			}), middleware.After)
		})
	}
}

var (
	authCodes = map[string]bool{
		"InvalidClientTokenId": true, "UnrecognizedClientException": true, "SignatureDoesNotMatch": true,
		"InvalidSecurity": true, "AccessDenied": true, "AccessDeniedException": true, "MissingAuthenticationToken": true,
		"IncompleteSignature": true, "ExpiredToken": true, "InvalidAccessKeyId": true, "AuthFailure": true,
		"AuthorizationError": true, "KmsAccessDenied": true, "OptInRequired": true, "RequestExpired": true,
	}
	missingCodes = map[string]bool{"QueueDoesNotExist": true, "AWS.SimpleQueueService.NonExistentQueue": true}
	invalidCodes = map[string]bool{
		"InvalidParameterValue": true, "InvalidParameterCombination": true, "MissingParameter": true,
		"InvalidAttributeName": true, "InvalidAttributeValue": true, "InvalidMessageContents": true,
		"ValidationError": true, "ValidationException": true, "UnsupportedOperation": true, "InvalidAddress": true,
		"AWS.SimpleQueueService.UnsupportedOperation": true, "InvalidParameterValueException": true,
	}
)

func (d *Driver) classify(ctx context.Context, err error, what string) *protocol.Error {
	var (
		ae      smithy.APIError
		ne      net.Error
		de      *smithy.DeserializationError
		verify  *tls.CertificateVerificationError
		unknown x509.UnknownAuthorityError
		host    x509.HostnameError
		invalid x509.CertificateInvalidError
		record  tls.RecordHeaderError
	)
	switch {
	case errors.As(err, &ae):
		code, msg := ae.ErrorCode(), d.scrub(ae.ErrorMessage())
		detail := fmt.Sprintf("%s: %s", what, code)
		if msg != "" {
			detail += ": " + msg
		}
		switch {
		case missingCodes[code]:
			return protocol.Errorf(protocol.CauseQueryError, "%s (the queue doesn't exist)", detail)
		case authCodes[code]:
			return protocol.Lastingf(protocol.CauseQueryError, "%s (the credentials are refused)", detail)
		case invalidCodes[code]:
			return protocol.Lastingf(protocol.CauseInvalidRequest, "%s", detail)
		}
		return protocol.Errorf(protocol.CauseQueryError, "%s", detail)
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || (errors.As(err, &ne) && ne.Timeout()):
		return protocol.Errorf(protocol.CauseTimeout, "%s: timed out", what)
	case errors.As(err, &verify) || errors.As(err, &unknown) || errors.As(err, &host) || errors.As(err, &invalid):
		return protocol.Lastingf(protocol.CauseUnreachable, "%s: the endpoint's TLS certificate isn't trusted (set ca_bundle to the CA that signed it): %s", what, d.scrub(rootCause(err)))
	case errors.As(err, &record):
		return protocol.Lastingf(protocol.CauseUnreachable, "%s: the endpoint doesn't speak TLS; check the endpoint's scheme: %s", what, d.scrub(rootCause(err)))
	case errors.As(err, &de):
		return protocol.Errorf(protocol.CauseUnreadableResponse, "%s: %s", what, d.scrub(rootCause(err)))
	}
	return protocol.Errorf(protocol.CauseUnreachable, "%s: %s", what, d.scrub(rootCause(err)))
}

func rootCause(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err.Error()
	}
	var oe *smithy.OperationError
	if errors.As(err, &oe) && oe.Err != nil {
		err = oe.Err
	}
	return err.Error()
}

func parseJSON(text string) (any, bool) {
	b := []byte(text)
	if !json.Valid(b) || hasDuplicateKey(b) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}

func hasDuplicateKey(b []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	type frame struct {
		object bool
		keys   map[string]bool
		key    bool
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return false
		}
		if err != nil {
			return false
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if delim, ok := tok.(json.Delim); ok && (delim == '}' || delim == ']') {
			stack = stack[:len(stack)-1]
			if len(stack) > 0 && stack[len(stack)-1].object {
				stack[len(stack)-1].key = true
			}
			continue
		}
		if top != nil && top.object && top.key {
			k, _ := tok.(string)
			if top.keys[k] {
				return true
			}
			top.keys[k] = true
			top.key = false
			continue
		}
		if delim, ok := tok.(json.Delim); ok {
			stack = append(stack, &frame{object: delim == '{', keys: map[string]bool{}, key: delim == '{'})
			continue
		}
		if top != nil && top.object {
			top.key = true
		}
	}
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "..."
}
