package model

type Binding struct {
	Target      Target                     `yaml:"target"`
	Connections BindingConnections         `yaml:"connections"`
	Identities  map[string]BindingIdentity `yaml:"identities"`
	Secrets     map[string]BindingSecret   `yaml:"secrets"`

	Environment string `yaml:"environment"`

	Image *BindingImage `yaml:"image"`
}

type BindingImage struct {
	BuiltDigest   string `yaml:"built_digest" json:"built_digest"`
	RunningDigest string `yaml:"running_digest" json:"running_digest"`
}

type BindingSecret struct {
	Env string `yaml:"env"`
}

type Target struct {
	BaseURL   string `yaml:"base_url"`
	UserAgent string `yaml:"user_agent"`
}

type BindingConnections struct {
	Postgres map[string]BindingPostgres `yaml:"postgres"`
}

type BindingPostgres struct {
	DSNEnv string `yaml:"dsn_env"`
}

type BindingIdentity struct {
	TokenEnv  string `yaml:"token_env"`
	UserIDEnv string `yaml:"user_id_env"`

	Variants map[string]BindingVariant `yaml:"variants"`
}

type BindingVariant struct {
	TokenEnv  string `yaml:"token_env"`
	HeaderEnv string `yaml:"header_env"`
}
