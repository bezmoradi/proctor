package auth

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/proctor/examples/sample/internal/core/shared"

	"github.com/golang-jwt/jwt/v5"
)

const signingMethod = "HS256"

const minSecretLen = 32

const (
	placeholderSecret   = "change-me-before-prod-use-a-32-byte-random-secret"
	placeholderPassword = "change-me"
)

type Config struct {
	secret       []byte
	issuer       string
	audience     string
	ttl          time.Duration
	username     string
	passwordHash passwordHash
}

func LoadConfig() (Config, error) {
	cfg := Config{
		secret:   []byte(os.Getenv("JWT_SECRET")),
		issuer:   os.Getenv("JWT_ISSUER"),
		audience: os.Getenv("JWT_AUDIENCE"),
		username: os.Getenv("AUTH_USERNAME"),
		ttl:      15 * time.Minute,
	}
	switch {
	case len(cfg.secret) == 0:
		return Config{}, errors.New("JWT_SECRET is not set")
	case string(cfg.secret) == placeholderSecret:
		return Config{}, errors.New("JWT_SECRET is the documented placeholder; generate a random one (e.g. openssl rand -base64 32)")
	case len(cfg.secret) < minSecretLen:
		return Config{}, fmt.Errorf("JWT_SECRET must be at least %d bytes", minSecretLen)
	case cfg.issuer == "":
		return Config{}, errors.New("JWT_ISSUER is not set")
	case cfg.audience == "":
		return Config{}, errors.New("JWT_AUDIENCE is not set")
	case cfg.username == "":
		return Config{}, errors.New("AUTH_USERNAME is not set")
	case os.Getenv("AUTH_PASSWORD") != "":
		return Config{}, errors.New("AUTH_PASSWORD (plaintext) is no longer read; remove it and set AUTH_PASSWORD_HASH (make hash-password)")
	}
	if raw := os.Getenv("JWT_TTL"); raw != "" {
		ttl, err := time.ParseDuration(raw)
		if err != nil || ttl <= 0 {
			return Config{}, fmt.Errorf("JWT_TTL %q is not a positive duration", raw)
		}
		cfg.ttl = ttl
	}
	hash, err := parsePasswordHash(os.Getenv("AUTH_PASSWORD_HASH"))
	if err != nil {
		return Config{}, fmt.Errorf("AUTH_PASSWORD_HASH: %w (make hash-password prints a valid one)", err)
	}
	if hash.matches(placeholderPassword) {
		return Config{}, errors.New("AUTH_PASSWORD_HASH is the hash of the old demo password; set a new one with make hash-password")
	}
	cfg.passwordHash = hash
	return cfg, nil
}

type ctxKey int

const subjectKey ctxKey = 0

func SubjectFrom(ctx context.Context) (string, bool) {
	subject, ok := ctx.Value(subjectKey).(string)
	return subject, ok
}

func writeUnauthorized(w http.ResponseWriter, challenge string) {
	if challenge != "" {
		w.Header().Set("WWW-Authenticate", challenge)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

func Middleware(cfg Config) func(http.Handler) http.Handler {
	realm := fmt.Sprintf("Bearer realm=%q", cfg.audience)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok {
				writeUnauthorized(w, realm)
				return
			}

			claims := &jwt.RegisteredClaims{}
			_, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
				if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, jwt.ErrTokenSignatureInvalid
				}
				return cfg.secret, nil
			},
				jwt.WithValidMethods([]string{signingMethod}),
				jwt.WithExpirationRequired(),
				jwt.WithIssuer(cfg.issuer),
				jwt.WithAudience(cfg.audience),
			)
			if err != nil {
				writeUnauthorized(w, realm+`, error="invalid_token"`)
				return
			}

			ctx := context.WithValue(r.Context(), subjectKey, claims.Subject)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

const (
	loginWindow     = time.Minute
	loginsPerUser   = 5
	loginsPerClient = 20
	maxTrackedKeys  = 10000
)

func LoginHandler(cfg Config, logger shared.Logger) http.HandlerFunc {
	perUser := newLimiter(loginsPerUser, loginWindow, maxTrackedKeys)
	perClient := newLimiter(loginsPerClient, loginWindow, maxTrackedKeys)

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		client := clientIP(r)
		if ok, wait := perClient.allow(client, time.Now()); !ok {
			logger.Warn("auth.login.throttled", "client", client, "limit", "client")
			writeTooManyRequests(w, wait)
			return
		}

		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Username == "" || body.Password == "" {
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				return
			}
			logger.Warn("auth.login.failure", "client", client, "reason", "malformed request")
			writeUnauthorized(w, "")
			return
		}

		if ok, wait := perUser.allow(body.Username, time.Now()); !ok {
			logger.Warn("auth.login.throttled", "client", client, "username", body.Username, "limit", "username")
			writeTooManyRequests(w, wait)
			return
		}

		passOK := cfg.passwordHash.matches(body.Password)
		userOK := subtle.ConstantTimeCompare([]byte(body.Username), []byte(cfg.username)) == 1
		if !userOK || !passOK {
			logger.Warn("auth.login.failure", "client", client, "username", body.Username, "reason", "bad credentials")
			writeUnauthorized(w, "")
			return
		}
		perUser.forget(body.Username)

		now := time.Now()
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
			Subject:   body.Username,
			Issuer:    cfg.issuer,
			Audience:  jwt.ClaimStrings{cfg.audience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(cfg.ttl)),
		})
		signed, err := token.SignedString(cfg.secret)
		if err != nil {
			logger.Error("auth.login.sign failed", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		logger.Info("auth.login.success", "client", client, "username", body.Username)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": signed})
	}
}

func writeTooManyRequests(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "too many login attempts"})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type limiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	maxKeys int
	windows map[string]*window
}

type window struct {
	start time.Time
	count int
}

func newLimiter(limit int, period time.Duration, maxKeys int) *limiter {
	return &limiter{limit: limit, window: period, maxKeys: maxKeys, windows: make(map[string]*window)}
}

func (l *limiter) allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	w, ok := l.windows[key]
	if ok && now.Sub(w.start) < l.window {
		w.count++
		if w.count > l.limit {
			return false, l.window - now.Sub(w.start)
		}
		return true, 0
	}
	if !ok && len(l.windows) >= l.maxKeys {
		for k, old := range l.windows {
			if now.Sub(old.start) >= l.window {
				delete(l.windows, k)
			}
		}
		if len(l.windows) >= l.maxKeys {
			return false, l.window
		}
	}
	l.windows[key] = &window{start: now, count: 1}
	return true, 0
}

func (l *limiter) forget(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.windows, key)
}

const (
	hashScheme     = "pbkdf2-sha256"
	hashIterations = 600000
	hashSaltLen    = 16
	hashKeyLen     = 32
)

var b64 = base64.RawURLEncoding

func HashPassword(password string) (string, error) {
	salt := make([]byte, hashSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, hashIterations, hashKeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%d:%s:%s", hashScheme, hashIterations, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

type passwordHash struct {
	iterations int
	salt       []byte
	key        []byte
}

func parsePasswordHash(encoded string) (passwordHash, error) {
	if encoded == "" {
		return passwordHash{}, errors.New("not set")
	}
	parts := strings.Split(encoded, ":")
	if len(parts) != 4 || parts[0] != hashScheme {
		return passwordHash{}, fmt.Errorf("not a %s hash", hashScheme)
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < hashIterations {
		return passwordHash{}, fmt.Errorf("needs at least %d iterations", hashIterations)
	}
	salt, err := b64.DecodeString(parts[2])
	if err != nil || len(salt) < hashSaltLen {
		return passwordHash{}, errors.New("bad salt")
	}
	key, err := b64.DecodeString(parts[3])
	if err != nil || len(key) != hashKeyLen {
		return passwordHash{}, errors.New("bad key")
	}
	return passwordHash{iterations: iterations, salt: salt, key: key}, nil
}

func (h passwordHash) matches(password string) bool {
	key, err := pbkdf2.Key(sha256.New, password, h.salt, h.iterations, len(h.key))
	return err == nil && subtle.ConstantTimeCompare(key, h.key) == 1
}
