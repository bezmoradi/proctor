package protocol

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

type Driver interface {
	Hello(ctx context.Context, p HelloParams) (HelloResult, error)
	Configure(ctx context.Context, p ConfigureParams) error
	Validate(ctx context.Context, p ValidateParams) (ValidateResult, error)
	Prove(ctx context.Context) (ProveResult, error)
	Ready(ctx context.Context) (ReadyResult, error)
	Call(ctx context.Context, p CallParams) (CallResult, error)
	Close(ctx context.Context, p CloseParams) error
}

func Serve(d Driver) error {
	out := os.Stdout
	os.Stdout = os.Stderr
	return serve(d, os.Stdin, out)
}

type server struct {
	d            Driver
	hello        bool
	configured   bool
	validateOnly bool
}

func serve(d Driver, r io.Reader, w io.Writer) error {
	br := newReader(r)
	bw := bufio.NewWriter(w)
	s := &server{d: d}
	for {
		line, err := readLine(br)
		var resp Response
		stop := false
		switch {
		case errors.Is(err, errTooLarge):
			resp = failure(0, &Error{Code: CodeInvalidRequest, Message: err.Error(), Data: ErrorData{Cause: CauseInvalidRequest}})
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return err
		default:
			if len(line) == 0 {
				continue
			}
			resp, stop = s.handle(line)
		}
		if err := send(bw, resp); err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
}

func send(w *bufio.Writer, resp Response) error {
	b, err := json.Marshal(resp)
	if err == nil && len(b) > MaxMessage {
		err = errTooLarge
	}
	if err != nil {
		cause := CauseDriverFailed
		if errors.Is(err, errTooLarge) {
			cause = CauseResponseTooLarge
		}
		b, err = json.Marshal(failure(resp.ID, Errorf(cause, "cannot send the result: %v", err)))
		if err != nil {
			return err
		}
	}
	b = append(b, '\n')
	if _, err := w.Write(b); err != nil {
		return err
	}
	return w.Flush()
}

func failure(id int, e *Error) Response {
	return Response{JSONRPC: JSONRPC, ID: id, Error: e}
}

func invalid(id int, code int, format string, args ...any) Response {
	return failure(id, &Error{Code: code, Message: fmt.Sprintf(format, args...), Data: ErrorData{Cause: CauseInvalidRequest}})
}

func (s *server) handle(line []byte) (Response, bool) {
	env, err := envelope(line, "jsonrpc", "id", "method", "params")
	id, idErr := messageID(env)
	if err != nil {
		return invalid(id, CodeInvalidRequest, "invalid request: %v", err), false
	}
	if idErr != nil {
		return invalid(0, CodeInvalidRequest, "invalid request: %v", idErr), false
	}
	var method string
	if err := json.Unmarshal(env["method"], &method); err != nil || method == "" {
		return invalid(id, CodeInvalidRequest, "invalid request: method must be a non-empty string"), false
	}
	params := env["params"]
	ctx := context.Background()

	switch method {
	case MethodHello, MethodConfigure, MethodValidate, MethodProve, MethodReady, MethodCall, MethodClose, MethodShutdown:
	default:
		return invalid(id, CodeUnknownMethod, "unknown method %q", method), false
	}
	if method == MethodShutdown {
		var p Empty
		if err := decodeParams(params, &p); err != nil {
			return invalid(id, CodeInvalidParams, "shutdown: %v", err), false
		}
		return s.result(id, Empty{}, nil), true
	}
	if method == MethodHello {
		if s.hello {
			return invalid(id, CodeInvalidRequest, "hello was already received"), false
		}
		var p HelloParams
		if err := decodeParams(params, &p); err != nil {
			return invalid(id, CodeInvalidParams, "hello: %v", err), false
		}
		if p.Protocol != Version {
			return invalid(id, CodeInvalidParams, "the core speaks protocol %d; this driver speaks protocol %d", p.Protocol, Version), false
		}
		r, err := s.d.Hello(ctx, p)
		if err == nil {
			s.hello = true
			if r.Protocol == 0 {
				r.Protocol = Version
			}
			if r.Operations == nil {
				r.Operations = []Operation{}
			}
		}
		return s.result(id, r, err), false
	}
	if !s.hello {
		return invalid(id, CodeInvalidRequest, "hello must come before %s", method), false
	}
	if method == MethodConfigure {
		if s.configured {
			return invalid(id, CodeInvalidRequest, "configure was already received"), false
		}
		var p ConfigureParams
		if err := decodeParams(params, &p); err != nil {
			return invalid(id, CodeInvalidParams, "configure: %v", err), false
		}
		err := s.d.Configure(ctx, p)
		if err == nil {
			s.configured = true
			s.validateOnly = p.Connection == nil
		}
		return s.result(id, Empty{}, err), false
	}
	if !s.configured {
		return s.result(id, nil, Errorf(CauseNotConfigured, "configure must come before %s", method)), false
	}
	if s.validateOnly && (method == MethodProve || method == MethodReady || method == MethodCall) {
		return s.result(id, nil, Errorf(CauseNotConfigured, "the driver is configured for validation only, so %s isn't available", method)), false
	}

	switch method {
	case MethodValidate:
		var p ValidateParams
		if err := decodeParams(params, &p); err != nil {
			return invalid(id, CodeInvalidParams, "validate: %v", err), false
		}
		r, err := s.d.Validate(ctx, p)
		if r.Problems == nil {
			r.Problems = []Problem{}
		}
		if r.Refs == nil {
			r.Refs = []string{}
		}
		return s.result(id, r, err), false
	case MethodProve:
		var p Empty
		if err := decodeParams(params, &p); err != nil {
			return invalid(id, CodeInvalidParams, "prove: %v", err), false
		}
		r, err := s.d.Prove(ctx)
		if r.Proofs == nil {
			r.Proofs = []Proof{}
		}
		return s.result(id, r, err), false
	case MethodReady:
		var p Empty
		if err := decodeParams(params, &p); err != nil {
			return invalid(id, CodeInvalidParams, "ready: %v", err), false
		}
		r, err := s.d.Ready(ctx)
		return s.result(id, r, err), false
	case MethodCall:
		var p CallParams
		if err := decodeParams(params, &p); err != nil {
			return invalid(id, CodeInvalidParams, "call: %v", err), false
		}
		if p.DeadlineMS > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(p.DeadlineMS)*time.Millisecond)
			defer cancel()
		}
		r, err := s.d.Call(ctx, p)
		return s.result(id, r, err), false
	default:
		var p CloseParams
		if err := decodeParams(params, &p); err != nil {
			return invalid(id, CodeInvalidParams, "close: %v", err), false
		}
		err := s.d.Close(ctx, p)
		return s.result(id, Empty{}, err), false
	}
}

func decodeParams(raw json.RawMessage, out any) error {
	if !present(raw) {
		return nil
	}
	return decodeStrict(raw, out)
}

func (s *server) result(id int, v any, err error) Response {
	if err != nil {
		var pe *Error
		if !errors.As(err, &pe) {
			return failure(id, Errorf(CauseDriverFailed, "%v", err))
		}
		out := *pe
		out.local = nil
		if out.Code == 0 {
			out.Code = CodeOperationFailed
		}
		if out.Data.Cause == "" {
			out.Data.Cause = CauseDriverFailed
		}
		return failure(id, &out)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return failure(id, Errorf(CauseDriverFailed, "cannot encode the result: %v", err))
	}
	return Response{JSONRPC: JSONRPC, ID: id, Result: b}
}
