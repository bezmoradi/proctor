package model

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

	Variants []string `yaml:"variants"`

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

type Manifest struct {
	Version        int             `yaml:"version"`
	Specifications []ManifestEntry `yaml:"specifications"`
}

type ManifestEntry struct {
	ID   string `yaml:"id"`
	Path string `yaml:"path"`
}

type Spec struct {
	Version   int        `yaml:"version"`
	Suite     string     `yaml:"suite"`
	Scenarios []Scenario `yaml:"scenarios"`
	Tables    []Table    `yaml:"tables"`
}

type Scenario struct {
	ID                       string `yaml:"id"`
	Name                     string `yaml:"name"`
	RequirementIsOperational bool   `yaml:"requirement_is_operational"`
	Setup                    []Step `yaml:"setup"`
	Steps                    []Step `yaml:"steps"`

	Case *TableCase `yaml:"-"`
}

type Step struct {
	ID      string   `yaml:"id"`
	Request *Request `yaml:"request"`
	Verify  *Verify  `yaml:"verify"`
	Expect  *Expect  `yaml:"expect"`

	Save map[string]string `yaml:"save"`

	NoStateCheck string `yaml:"no_state_check"`

	Waive map[string]string `yaml:"waive"`
}

type Verify struct {
	Postgres *PostgresCheck `yaml:"postgres"`
}

type PostgresCheck struct {
	Connection string      `yaml:"connection"`
	Query      string      `yaml:"query"`
	Args       []any       `yaml:"args"`
	Expect     *RowsExpect `yaml:"expect"`
}

type RowsExpect struct {
	Rows     *int           `yaml:"rows"`
	FirstRow map[string]any `yaml:"first_row"`
}

type Request struct {
	Method   string `yaml:"method"`
	Path     string `yaml:"path"`
	Identity string `yaml:"identity"`

	Credential string            `yaml:"credential"`
	Headers    map[string]string `yaml:"headers"`
	Query      map[string]string `yaml:"query"`
	Body       any               `yaml:"body"`
}

type Expect struct {
	Status  *int              `yaml:"status"`
	JSON    map[string]any    `yaml:"json"`
	Headers map[string]string `yaml:"headers"`

	Present []string `yaml:"present"`
	Absent  []string `yaml:"absent"`

	Length map[string]any `yaml:"length"`

	AbsentHeaders []string `yaml:"absent_headers"`

	Keys map[string][]string `yaml:"keys"`
}
