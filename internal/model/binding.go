package model

// Binding is an environment binding (prd.md §14): where the
// application under test runs and where credentials come from. It is
// supplied by the run, not protected, and never holds a credential itself;
// it names environment variables instead (SEC-02).
type Binding struct {
	Target      Target                     `yaml:"target"`
	Connections BindingConnections         `yaml:"connections"`
	Identities  map[string]BindingIdentity `yaml:"identities"`
	Secrets     map[string]BindingSecret   `yaml:"secrets"`
	// Environment names the environment in the report (§13.1).
	Environment string `yaml:"environment"`
	// Image carries the two digests the trusted workflow supplies in
	// authoritative mode (REP-11): the image the build job produced and
	// the image the running application was started from.
	Image *BindingImage `yaml:"image"`
}

// BindingImage holds the REP-11 digests, each "sha256:" and 64 hex digits.
type BindingImage struct {
	BuiltDigest   string `yaml:"built_digest" json:"built_digest"`
	RunningDigest string `yaml:"running_digest" json:"running_digest"`
}

// BindingSecret names the variable holding one secret.
type BindingSecret struct {
	Env string `yaml:"env"`
}

type Target struct {
	BaseURL   string `yaml:"base_url"`
	UserAgent string `yaml:"user_agent"`
}

// BindingConnections name where each connection's settings come from.
// Only PostgreSQL exists: Temporal and logs (Phase 5) are parked, so strict
// decoding rejects them.
type BindingConnections struct {
	Postgres map[string]BindingPostgres `yaml:"postgres"`
}

// BindingPostgres names the variable holding the connection string of the
// verification role (DB-04).
type BindingPostgres struct {
	DSNEnv string `yaml:"dsn_env"`
}

type BindingIdentity struct {
	TokenEnv  string `yaml:"token_env"`
	UserIDEnv string `yaml:"user_id_env"`
	// Variants maps each credential variant to its token variable.
	Variants map[string]BindingVariant `yaml:"variants"`
}

// BindingVariant supplies one credential variant: a token sent as
// "Authorization: Bearer <token>", or a whole Authorization header value.
type BindingVariant struct {
	TokenEnv  string `yaml:"token_env"`
	HeaderEnv string `yaml:"header_env"`
}
