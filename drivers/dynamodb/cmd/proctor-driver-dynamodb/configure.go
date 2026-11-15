package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/ratelimit"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"proctor/protocol"
	"proctor/protocol/schema"
)

const (
	secretAccessKey = "access_key"
	secretSecretKey = "secret_key"
	maxCABundle     = 1 << 20
	retryAttempts   = 2
)

var (
	aliasRE  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	regionRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

var policySchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"tables": map[string]any{
			"type":                 "object",
			"additionalProperties": map[string]any{"type": "string", "pattern": `^[A-Za-z0-9_.-]{3,255}$`},
		},
	},
	"required":             []string{"tables"},
	"additionalProperties": false,
}

var connectionSchema = map[string]any{
	"type": []string{"object", "null"},
	"properties": map[string]any{
		"endpoint":  map[string]any{"type": "string", "pattern": `^https?://`},
		"region":    map[string]any{"type": "string", "pattern": `^[a-z0-9]+(-[a-z0-9]+)*$`},
		"ca_bundle": map[string]any{"type": "string"},
	},
	"required":             []string{"endpoint", "region"},
	"additionalProperties": false,
}

type policy struct {
	Tables map[string]string `json:"tables"`
}

type connection struct {
	Endpoint string `json:"endpoint"`
	Region   string `json:"region"`
	CABundle string `json:"ca_bundle"`
}

func (d *driver) Configure(ctx context.Context, p protocol.ConfigureParams) error {
	if msgs := schema.Check(policySchema, p.Policy); len(msgs) > 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "policy: %s", strings.Join(msgs, "; "))
	}
	var pol policy
	if err := protocol.Decode(p.Policy, &pol); err != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "policy: %v", err)
	}
	if len(pol.Tables) == 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "policy: tables must name at least one table as <alias>: <table name>")
	}
	aliases := sortedKeys(pol.Tables)
	for _, a := range aliases {
		if !aliasRE.MatchString(a) {
			return protocol.Lastingf(protocol.CauseInvalidRequest, "policy: table alias %q must match %s", a, aliasRE)
		}
	}
	if p.Connection == nil {
		if len(p.Secrets) > 0 {
			return protocol.Lastingf(protocol.CauseInvalidRequest, "binding: secrets were given without a connection")
		}
		d.tables, d.aliases = pol.Tables, aliases
		return nil
	}
	client, secrets, err := buildClient(p.Connection, p.Secrets)
	if err != nil {
		return err
	}
	d.tables, d.aliases, d.client, d.secrets = pol.Tables, aliases, client, secrets
	return nil
}

func buildClient(raw any, secrets protocol.Secrets) (*dynamodb.Client, []string, error) {
	if msgs := schema.Check(connectionSchema, raw); len(msgs) > 0 {
		return nil, nil, protocol.Lastingf(protocol.CauseInvalidRequest, "connection: %s", strings.Join(msgs, "; "))
	}
	var c connection
	if err := protocol.Decode(raw, &c); err != nil {
		return nil, nil, protocol.Lastingf(protocol.CauseInvalidRequest, "connection: %v", err)
	}
	var msgs []string
	bad := func(format string, args ...any) { msgs = append(msgs, fmt.Sprintf(format, args...)) }
	u, err := url.Parse(c.Endpoint)
	switch {
	case c.Endpoint == "":
		bad("connection: endpoint is required; the driver never falls back to AWS")
	case err != nil:
		bad("connection: endpoint doesn't parse")
	case u.Scheme != "http" && u.Scheme != "https":
		bad("connection: endpoint must be an http:// or https:// URL")
	case u.Host == "" || u.Hostname() == "":
		bad("connection: endpoint has no host")
	case u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || (u.Path != "" && u.Path != "/"):
		bad("connection: endpoint must be scheme://host[:port] with no credentials, path, query or fragment")
	}
	if c.Region == "" {
		bad("connection: region is required")
	} else if !regionRE.MatchString(c.Region) {
		bad("connection: region %q isn't a region name such as us-east-1", c.Region)
	}
	for _, k := range sortedKeys(secrets) {
		if k != secretAccessKey && k != secretSecretKey {
			bad("binding: unexpected secret %s; the dynamodb driver takes only access_key_env and secret_key_env", k)
		}
	}
	ak, sk := secrets[secretAccessKey], secrets[secretSecretKey]
	if ak == "" {
		bad("binding: access_key is missing; set access_key_env to an environment variable that holds it (the driver uses static credentials only)")
	}
	if sk == "" {
		bad("binding: secret_key is missing; set secret_key_env to an environment variable that holds it (the driver uses static credentials only)")
	}
	var pool *x509.CertPool
	if c.CABundle != "" {
		if err == nil && u.Scheme == "http" {
			bad("connection: ca_bundle applies only to an https endpoint")
		} else if pool, err = loadCABundle(c.CABundle); err != nil {
			bad("connection: ca_bundle: %v", err)
		}
	}
	if len(msgs) > 0 {
		return nil, nil, protocol.Lastingf(protocol.CauseInvalidRequest, "%s", strings.Join(msgs, "; "))
	}
	httpClient := awshttp.NewBuildableClient().WithTransportOptions(func(t *http.Transport) {
		t.Proxy = nil
		t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	})
	endpoint := u.Scheme + "://" + u.Host
	client := dynamodb.New(dynamodb.Options{
		Region:                c.Region,
		BaseEndpoint:          aws.String(endpoint),
		Credentials:           credentials.NewStaticCredentialsProvider(ak, sk, ""),
		HTTPClient:            httpClient,
		AccountIDEndpointMode: aws.AccountIDEndpointModeDisabled,
		Retryer: retry.NewStandard(func(o *retry.StandardOptions) {
			o.MaxAttempts = retryAttempts
			o.RateLimiter = ratelimit.None
		}),
	})
	return client, []string{ak, sk}, nil
}

func loadCABundle(path string) (*x509.CertPool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxCABundle {
		return nil, fmt.Errorf("%s must be a regular PEM file of at most 1 MiB", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("%s holds no PEM certificate", path)
	}
	return pool, nil
}
