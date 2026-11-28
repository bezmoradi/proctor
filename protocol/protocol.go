package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	Version    = 1
	JSONRPC    = "2.0"
	MaxMessage = 16 << 20
)

const (
	MethodHello     = "hello"
	MethodConfigure = "configure"
	MethodValidate  = "validate"
	MethodProve     = "prove"
	MethodReady     = "ready"
	MethodCall      = "call"
	MethodClose     = "close"
	MethodShutdown  = "shutdown"
)

const (
	CodeInvalidRequest  = -32600
	CodeUnknownMethod   = -32601
	CodeInvalidParams   = -32602
	CodeOperationFailed = 1
)

const (
	CauseUnreachable        = "unreachable"
	CauseTimeout            = "timeout"
	CauseUnreadableResponse = "unreadable_response"
	CauseResponseTooLarge   = "response_too_large"
	CauseInvalidRequest     = "invalid_request"
	CauseQueryError         = "query_error"
	CauseUnsupportedResult  = "unsupported_result"
	CauseDriverFailed       = "driver_failed"
	CauseNotConfigured      = "not_configured"
)

var Causes = []string{
	CauseUnreachable,
	CauseTimeout,
	CauseUnreadableResponse,
	CauseResponseTooLarge,
	CauseInvalidRequest,
	CauseQueryError,
	CauseUnsupportedResult,
	CauseDriverFailed,
	CauseNotConfigured,
}

const (
	KindAct     = "act"
	KindObserve = "observe"
	KindByArgs  = "by_args"
)

const (
	CredentialNone     = "none"
	CredentialOptional = "optional"
	CredentialRequired = "required"
)

const (
	SchemeBearer = "bearer"
	SchemeHeader = "header"
)

const EscapePathSegment = "path_segment"

const (
	ProofProven   = "proven"
	ProofUnproven = "unproven"
	ProofFailed   = "failed"
)

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    int       `json:"code"`
	Message string    `json:"message"`
	Data    ErrorData `json:"data"`

	local error
}

type ErrorData struct {
	Cause   string `json:"cause"`
	Lasting bool   `json:"lasting"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Data.Cause, e.Message)
}

func (e *Error) Unwrap() error {
	return e.local
}

func Errorf(cause, format string, args ...any) *Error {
	return &Error{Code: CodeOperationFailed, Message: fmt.Sprintf(format, args...), Data: ErrorData{Cause: cause}}
}

func Lastingf(cause, format string, args ...any) *Error {
	e := Errorf(cause, format, args...)
	e.Data.Lasting = true
	return e
}

type HelloParams struct {
	Protocol    int    `json:"protocol"`
	CoreVersion string `json:"core_version"`
}

type HelloResult struct {
	Name             string      `json:"name"`
	Version          string      `json:"version"`
	Protocol         int         `json:"protocol"`
	Operations       []Operation `json:"operations"`
	PolicySchema     any         `json:"policy_schema"`
	ConnectionSchema any         `json:"connection_schema"`
}

type Operation struct {
	Name           string            `json:"name"`
	Kind           string            `json:"kind"`
	ArgsSchema     any               `json:"args_schema"`
	FactSchema     any               `json:"fact_schema"`
	OpensHandle    bool              `json:"opens_handle"`
	UsesHandle     bool              `json:"uses_handle"`
	HandleOptional bool              `json:"handle_optional,omitempty"`
	Credential     string            `json:"credential"`
	Escape         map[string]string `json:"escape,omitempty"`
	Redact         []string          `json:"redact,omitempty"`
	Data           []string          `json:"data,omitempty"`
	Files          []string          `json:"files,omitempty"`
	Accumulates    bool              `json:"accumulates,omitempty"`
	Scan           []string          `json:"scan,omitempty"`
}

type FileInfo struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type File struct {
	FileInfo
	Data []byte `json:"data"`
}

type Secrets map[string]string

func (s Secrets) String() string {
	return fmt.Sprintf("[%d secrets hidden]", len(s))
}

func (s Secrets) GoString() string {
	return s.String()
}

type ConfigureParams struct {
	Policy     any     `json:"policy"`
	Connection any     `json:"connection"`
	Secrets    Secrets `json:"secrets"`
}

type ConfigureResult struct {
	Entries []PolicyEntry `json:"entries"`
}

type PolicyEntry struct {
	Key  string `json:"key"`
	Rule string `json:"rule"`
	Use  string `json:"use"`
}

type ValidateParams struct {
	Op       string              `json:"op"`
	Args     any                 `json:"args"`
	Identity string              `json:"identity"`
	Variant  string              `json:"variant"`
	Expect   any                 `json:"expect"`
	Setup    bool                `json:"setup"`
	Save     map[string]string   `json:"save,omitempty"`
	Files    map[string]FileInfo `json:"files,omitempty"`
}

type ValidateResult struct {
	Kind          string            `json:"kind"`
	Destructive   bool              `json:"destructive"`
	FixedTarget   bool              `json:"fixed_target"`
	Problems      []Problem         `json:"problems"`
	Refs          []string          `json:"refs"`
	StateCheck    *bool             `json:"state_check,omitempty"`
	NoStateChange bool              `json:"no_state_change,omitempty"`
	Targets       []string          `json:"targets,omitempty"`
	Shows         []string          `json:"shows,omitempty"`
	ExactTargets  []string          `json:"exact_targets,omitempty"`
	Uses          []string          `json:"uses,omitempty"`
	ShowsEntries  map[string]string `json:"shows_entries,omitempty"`
}

type Problem struct {
	Rule    string `json:"rule"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
}

type ProveResult struct {
	Proofs []Proof `json:"proofs"`
}

type Proof struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type ReadyResult struct {
	Ready   bool   `json:"ready"`
	Detail  string `json:"detail"`
	Lasting bool   `json:"lasting"`
}

type Credential struct {
	Identity string `json:"identity"`
	Variant  string `json:"variant"`
	Scheme   string `json:"scheme"`
	Value    string `json:"value"`
}

func (c Credential) String() string {
	return fmt.Sprintf("{identity:%s variant:%s scheme:%s value:[hidden]}", c.Identity, c.Variant, c.Scheme)
}

func (c Credential) GoString() string {
	return c.String()
}

type CallParams struct {
	Op         string          `json:"op"`
	Args       any             `json:"args"`
	Credential *Credential     `json:"credential"`
	Handle     string          `json:"handle,omitempty"`
	Open       bool            `json:"open,omitempty"`
	DeadlineMS int64           `json:"deadline_ms"`
	Files      map[string]File `json:"files,omitempty"`
}

type CallResult struct {
	Fact     any      `json:"fact"`
	Evidence Evidence `json:"evidence"`
	Handle   string   `json:"handle,omitempty"`
}

type Evidence struct {
	Summary         string `json:"summary"`
	RequestExcerpt  string `json:"request_excerpt,omitempty"`
	ResponseExcerpt string `json:"response_excerpt,omitempty"`
	DurationMS      int64  `json:"duration_ms"`
}

type CloseParams struct {
	Handle string `json:"handle"`
}

type Empty struct{}

func Decode(in any, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return decodeStrict(b, out)
}

func decodeStrict(b []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}
