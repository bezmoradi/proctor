package redis

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"

	"github.com/bezmoradi/proctor/protocol"
	"github.com/bezmoradi/proctor/protocol/schema"
)

const (
	DialTimeout  = 3 * time.Second
	ReadTimeout  = 5 * time.Second
	maxCABundle  = 1 << 20
	maxSentinels = 16
	secretKey    = "password"
)

type sentinel struct {
	Addresses []string `json:"addresses"`
	Master    string   `json:"master"`
}

type connection struct {
	Address  string    `json:"address"`
	Sentinel *sentinel `json:"sentinel"`
	DB       int       `json:"db"`
	Username string    `json:"username"`
	TLS      bool      `json:"tls"`
	CABundle string    `json:"ca_bundle"`
}

type binding struct {
	c        connection
	password string
	tls      *tls.Config
}

func parseBinding(raw any, secrets protocol.Secrets) (*binding, error) {
	var msgs []string
	bad := func(format string, args ...any) {
		msgs = append(msgs, "connection: "+fmt.Sprintf(format, args...))
	}
	fail := func() error {
		return fmt.Errorf("%s (%s)", strings.Join(msgs, "; "), ruleBinding)
	}
	for _, k := range sortedKeys(secrets) {
		if k != secretKey {
			bad("%s_env isn't a key of the redis driver's binding; the only secret is password_env, the variable that holds the user's password", k)
		}
	}
	if m := schema.Check(ConnectionSchema, raw); len(m) > 0 {
		for _, s := range m {
			bad("%s", s)
		}
		return nil, fail()
	}
	if len(msgs) > 0 {
		return nil, fail()
	}
	var c connection
	if err := protocol.Decode(raw, &c); err != nil {
		bad("%v", err)
		return nil, fail()
	}
	switch {
	case c.Address == "" && c.Sentinel == nil:
		bad("give address, the host:port of the Redis server, or sentinel: { addresses, master }")
	case c.Address != "" && c.Sentinel != nil:
		bad("give address or sentinel, not both")
	case c.Address != "":
		if why := addressProblem(c.Address); why != "" {
			bad("address %q %s", c.Address, why)
		}
	default:
		s := c.Sentinel
		switch {
		case len(s.Addresses) == 0:
			bad("sentinel.addresses must list at least one host:port")
		case len(s.Addresses) > maxSentinels:
			bad("sentinel.addresses lists %d sentinels; at most %d are allowed", len(s.Addresses), maxSentinels)
		}
		for i, a := range s.Addresses {
			if why := addressProblem(a); why != "" {
				bad("sentinel.addresses[%d] %q %s", i, a, why)
			}
		}
		if strings.TrimSpace(s.Master) == "" || strings.ContainsFunc(s.Master, unicode.IsControl) || strings.ContainsAny(s.Master, " \t") {
			bad("sentinel.master must be the name the sentinels know the master by, without spaces")
		}
	}
	if strings.ContainsFunc(c.Username, unicode.IsControl) || strings.ContainsAny(c.Username, " \t") {
		bad("username can't hold a space or a control character")
	}
	if c.CABundle != "" && !c.TLS {
		bad("ca_bundle is set but tls isn't true")
	}
	if c.CABundle != "" && !filepath.IsAbs(c.CABundle) {
		bad("ca_bundle must be an absolute path")
	}
	password := secrets[secretKey]
	if password != "" && strings.ContainsAny(password, "\r\n") {
		bad("the password in password_env holds a line break")
	}
	if c.Username != "" && password == "" {
		bad("username %s is set without password_env; name the variable that holds its password", c.Username)
	}
	if len(msgs) > 0 {
		return nil, fail()
	}
	b := &binding{c: c, password: password}
	if c.TLS {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if c.CABundle != "" {
			pool, err := readCABundle(c.CABundle)
			if err != nil {
				bad("ca_bundle %s: %v", c.CABundle, err)
				return nil, fail()
			}
			cfg.RootCAs = pool
		}
		b.tls = cfg
	}
	return b, nil
}

func addressProblem(a string) string {
	host, port, err := net.SplitHostPort(a)
	switch {
	case err != nil || host == "" || port == "":
		return "must be host:port"
	case strings.ContainsFunc(a, unicode.IsControl) || strings.Contains(a, "/"):
		return "can't hold a / or a control character"
	}
	return ""
}

func readCABundle(path string) (*x509.CertPool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("can't be read")
	}
	defer f.Close()
	pem, err := io.ReadAll(io.LimitReader(f, maxCABundle+1))
	switch {
	case err != nil:
		return nil, errors.New("can't be read")
	case len(pem) > maxCABundle:
		return nil, fmt.Errorf("is larger than %d bytes", maxCABundle)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("holds no PEM certificate")
	}
	return pool, nil
}

func (b *binding) client() redis.UniversalClient {
	if b.c.Sentinel != nil {
		return redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:            b.c.Sentinel.Master,
			SentinelAddrs:         b.c.Sentinel.Addresses,
			DB:                    b.c.DB,
			Username:              b.c.Username,
			Password:              b.password,
			Protocol:              3,
			MaxRetries:            1,
			DialTimeout:           DialTimeout,
			DialerRetries:         1,
			ReadTimeout:           ReadTimeout,
			WriteTimeout:          ReadTimeout,
			ContextTimeoutEnabled: true,
			PoolSize:              2,
			MaxIdleConns:          2,
			PoolTimeout:           ReadTimeout,
			TLSConfig:             b.tls,
			DisableIdentity:       true,
		})
	}
	return redis.NewClient(&redis.Options{
		Addr:                     b.c.Address,
		DB:                       b.c.DB,
		Username:                 b.c.Username,
		Password:                 b.password,
		Protocol:                 3,
		MaxRetries:               1,
		DialTimeout:              DialTimeout,
		DialerRetries:            1,
		ReadTimeout:              ReadTimeout,
		WriteTimeout:             ReadTimeout,
		ContextTimeoutEnabled:    true,
		PoolSize:                 2,
		MaxIdleConns:             2,
		PoolTimeout:              ReadTimeout,
		TLSConfig:                b.tls,
		DisableIdentity:          true,
		MaintNotificationsConfig: &maintnotifications.Config{Mode: maintnotifications.ModeDisabled},
	})
}

func (b *binding) where() string {
	if b.c.Sentinel != nil {
		return fmt.Sprintf("master %s through the sentinels %s", b.c.Sentinel.Master, strings.Join(b.c.Sentinel.Addresses, ", "))
	}
	return b.c.Address
}

func (b *binding) scrub(s string) string {
	if b.password != "" {
		s = strings.ReplaceAll(s, b.password, "[redacted]")
	}
	return s
}

func serverError(err error) (string, bool) {
	var re redis.Error
	if errors.As(err, &re) && !errors.Is(err, redis.Nil) {
		return re.Error(), true
	}
	return "", false
}

func (b *binding) classify(ctx context.Context, err error, what string) *protocol.Error {
	if msg, ok := serverError(err); ok {
		msg = b.scrub(msg)
		code, _, _ := strings.Cut(msg, " ")
		switch code {
		case "WRONGPASS", "NOAUTH":
			return protocol.Lastingf(protocol.CauseUnreachable, "%s: the server refused the binding's username and password: %s", what, msg)
		case "NOPERM":
			return protocol.Lastingf(protocol.CauseQueryError, "%s: the user lacks a permission the driver needs (grant +@read, and +select for a db other than 0): %s", what, msg)
		case "LOADING", "MASTERDOWN", "TRYAGAIN", "BUSY":
			return protocol.Errorf(protocol.CauseUnreachable, "%s: %s", what, msg)
		}
		return protocol.Errorf(protocol.CauseQueryError, "%s: %s", what, msg)
	}
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || (errors.As(err, &ne) && ne.Timeout()):
		return protocol.Errorf(protocol.CauseTimeout, "%s: timed out", what)
	}
	return protocol.Errorf(protocol.CauseUnreachable, "%s: %s", what, b.scrub(err.Error()))
}
