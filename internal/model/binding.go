package model

type Binding struct {
	Drivers     map[string]map[string]any  `yaml:"drivers"`
	Identities  map[string]BindingIdentity `yaml:"identities"`
	Secrets     map[string]BindingSecret   `yaml:"secrets"`
	Environment string                     `yaml:"environment"`
	Disposable  bool                       `yaml:"disposable"`
	Image       *BindingImage              `yaml:"image"`
	Images      map[string]*BindingImage   `yaml:"images"`
	AppLogs     *BindingAppLogs            `yaml:"app_logs"`
}

type BindingAppLogs struct {
	File string `yaml:"file"`
	OTel string `yaml:"otel"`
}

type BindingIdentity struct {
	TokenEnv string                    `yaml:"token_env"`
	Values   map[string]string         `yaml:"values"`
	Variants map[string]BindingVariant `yaml:"variants"`
}

type BindingVariant struct {
	TokenEnv  string `yaml:"token_env"`
	HeaderEnv string `yaml:"header_env"`
}

type BindingSecret struct {
	Env string `yaml:"env"`
}

type BindingImage struct {
	BuiltDigest   string `yaml:"built_digest" json:"built_digest"`
	RunningDigest string `yaml:"running_digest" json:"running_digest"`
}
