// Package model holds the protected file formats: proctor.yaml, the manifest
// and specifications.
//
// The specification format here is the Phase 0 subset from prd.md
// §20: scenarios of HTTP steps with stable IDs. Schema v1 (tables, time
// checks and PostgreSQL) comes in Phase 2 and later. Temporal and log
// evidence (Phase 5) is parked, so their fields aren't modelled and strict
// decoding rejects them.
package model

// Config is proctor.yaml (prd.md §14). Fields beyond those Phase 0
// uses are modelled so that strict decoding accepts the documented example.
type Config struct {
	Version         int                     `yaml:"version"`
	Project         string                  `yaml:"project"`
	Manifest        string                  `yaml:"manifest"`
	ProtectedOwners []string                `yaml:"protected_owners"`
	ProtectedPaths  []string                `yaml:"protected_paths"`
	Adapters        map[string]string       `yaml:"adapters"`
	Connections     Connections             `yaml:"connections"`
	Identities      map[string]IdentityType `yaml:"identities"`
	IdentityCheck   *IdentityCheck          `yaml:"identity_check"`
	Readiness       *Readiness              `yaml:"readiness"`
	TimeLimits      *TimeLimits             `yaml:"time_limits"`
	Redaction       *Redaction              `yaml:"redaction"`
	// Secrets names values, such as a test user's password, that requests
	// may send as ${secret.name}; the binding supplies them.
	Secrets []string      `yaml:"secrets"`
	Report  *ReportPolicy `yaml:"report"`
}

type Connections struct {
	Postgres map[string]PostgresPolicy `yaml:"postgres"`
}

type PostgresPolicy struct {
	Schemas []string `yaml:"schemas"`
	Objects []string `yaml:"objects"`
}

type IdentityType struct {
	Type string `yaml:"type"`
	// Variants names the identity's refused credential variants (AUTH-06),
	// such as expired; requests using one must expect 401 or 403.
	Variants []string `yaml:"variants"`
	// AcceptedVariants names other valid forms of the identity's credential,
	// such as the scheme spelled in lower case; requests using one may
	// expect any status.
	AcceptedVariants []string `yaml:"accepted_variants"`
}

type IdentityCheck struct {
	Path   string `yaml:"path"`
	UserID string `yaml:"user_id"`
}

type Readiness struct {
	Path           string   `yaml:"path"`
	Checks         []string `yaml:"checks"`
	StartupTimeout string   `yaml:"startup_timeout"`
}

type TimeLimits struct {
	MinInterval string `yaml:"min_interval"`
	MaxSamples  int    `yaml:"max_samples"`
	MaxWindow   string `yaml:"max_window"`
}

type Redaction struct {
	JSONPaths []string `yaml:"json_paths"`
	Headers   []string `yaml:"headers"`
}

type ReportPolicy struct {
	Formats      []string `yaml:"formats"`
	ExcerptBytes int      `yaml:"excerpt_bytes"`
}

// Manifest is manifest.yaml (prd.md §7.2). There is no `required`
// field: every listed specification is required (INT-06 retired).
type Manifest struct {
	Version        int             `yaml:"version"`
	Specifications []ManifestEntry `yaml:"specifications"`
}

type ManifestEntry struct {
	ID   string `yaml:"id"`
	Path string `yaml:"path"`
}

// Spec is one specification file (the Phase 0 subset plus the first
// PostgreSQL increment).
type Spec struct {
	Version   int        `yaml:"version"`
	Suite     string     `yaml:"suite"`
	Scenarios []Scenario `yaml:"scenarios"`
}

type Scenario struct {
	ID                       string `yaml:"id"`
	Name                     string `yaml:"name"`
	RequirementIsOperational bool   `yaml:"requirement_is_operational"`
	Setup                    []Step `yaml:"setup"`
	Steps                    []Step `yaml:"steps"`
}

// Step is one step. It has exactly one of Request (with Expect) or Verify.
type Step struct {
	ID      string   `yaml:"id"`
	Request *Request `yaml:"request"`
	Verify  *Verify  `yaml:"verify"`
	Expect  *Expect  `yaml:"expect"`
	// Save maps a name to a JSONPath of the response, or, on a database
	// check that expects exactly one row, to a column of that row.
	Save map[string]string `yaml:"save"`
	// NoStateCheck waives EXE-07 for a state-changing step whose effect
	// can't be read back; it must give the reason, which reviewers see.
	NoStateCheck string `yaml:"no_state_check"`
	// Waive maps a rule ID (FIX-05 or HTTP-09) to the written reason the
	// rule doesn't apply to this step. A waiver with nothing to waive is
	// invalid, and proctor diff lists every added waiver as a weakening.
	Waive map[string]string `yaml:"waive"`
}

// Verify is a read-only check of stored state (§11.7).
type Verify struct {
	Postgres *PostgresCheck `yaml:"postgres"`
}

// PostgresCheck runs one parameterized query (DB-01) and judges its rows.
type PostgresCheck struct {
	Connection string      `yaml:"connection"`
	Query      string      `yaml:"query"`
	Args       []any       `yaml:"args"`
	Expect     *RowsExpect `yaml:"expect"`
}

// RowsExpect is a database check's expectation. FirstRow must name every
// column the query returns (HTTP-07 applied to rows).
type RowsExpect struct {
	Rows     *int           `yaml:"rows"`
	FirstRow map[string]any `yaml:"first_row"`
}

type Request struct {
	Method   string `yaml:"method"`
	Path     string `yaml:"path"`
	Identity string `yaml:"identity"`
	// Credential names one of the identity's credential variants
	// (AUTH-06), such as expired; empty means its valid credential.
	Credential string            `yaml:"credential"`
	Headers    map[string]string `yaml:"headers"`
	Query      map[string]string `yaml:"query"`
	Body       any               `yaml:"body"`
}

type Expect struct {
	Status  *int              `yaml:"status"`
	JSON    map[string]any    `yaml:"json"`
	Headers map[string]string `yaml:"headers"`
	// Present and Absent list JSONPaths that must or must not select a
	// value; a field set to null is present (HTTP-08).
	Present []string `yaml:"present"`
	Absent  []string `yaml:"absent"`
	// Length maps a JSONPath to the exact length of the array it selects:
	// a non-negative integer, or one ${name} reference to a saved number.
	Length map[string]any `yaml:"length"`
	// AbsentHeaders lists response headers that must not be present.
	AbsentHeaders []string `yaml:"absent_headers"`
	// Keys maps a JSONPath to the exact set of keys of the object it
	// selects, so an extra field fails even when its siblings' values
	// can't be known in advance.
	Keys map[string][]string `yaml:"keys"`
}
