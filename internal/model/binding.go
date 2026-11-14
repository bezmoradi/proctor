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
// Temporal and logs are modelled so that strict decoding accepts the
// documented example; only PostgreSQL is used so far.
type BindingConnections struct {
	Postgres map[string]BindingPostgres `yaml:"postgres"`
	Temporal map[string]struct {
		Address   string `yaml:"address"`
		Namespace string `yaml:"namespace"`
	} `yaml:"temporal"`
	Logs map[string]struct {
		Type string `yaml:"type"`
		Path string `yaml:"path"`
	} `yaml:"logs"`
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
