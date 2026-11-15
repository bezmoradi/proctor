package main

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const (
	unauthorizedBody = `{"error":"UNAUTHORIZED","code":"UNAUTHORIZED"}`
	invalidTokenBody = `{"error":"INVALID_TOKEN","code":"INVALID_TOKEN"}`
	defaultLifetime  = time.Hour
)

type answeredClaims struct {
	UserID                  string   `json:"user_id"`
	Email                   string   `json:"email"`
	Name                    string   `json:"name"`
	Tier                    string   `json:"tier"`
	CustomRoleID            *string  `json:"custom_role_id,omitempty"`
	OrganizationID          string   `json:"organization_id"`
	OrganizationName        string   `json:"organization_name"`
	OrganizationDisplayName string   `json:"organization_display_name"`
	OrganizationKind        string   `json:"organization_kind"`
	GroupIDs                []string `json:"group_ids"`
	Exp                     int64    `json:"exp,omitempty"`
}

type validResponse struct {
	Message string         `json:"message"`
	Data    answeredClaims `json:"data"`
}

type sim struct {
	claims atomic.Pointer[map[string]entry]
	secret []byte
	logger *log.Logger
}

type recorder struct {
	http.ResponseWriter
	status  int
	outcome string
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func setOutcome(w http.ResponseWriter, outcome string) {
	if rec, ok := w.(*recorder); ok {
		rec.outcome = outcome
	}
}

func (s *sim) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/internal/validate-token", s.validateToken)
	mux.HandleFunc("GET /healthz", s.healthz)
	return s.logRequests(s.requireSecret(mux))
}

func (s *sim) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &recorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		outcome := rec.outcome
		if outcome == "" {
			switch rec.status {
			case http.StatusNotFound:
				outcome = "not_found"
			case http.StatusMethodNotAllowed:
				outcome = "method_not_allowed"
			default:
				outcome = "other"
			}
		}
		s.logger.Printf("request method=%q path=%q status=%d outcome=%s", r.Method, r.URL.Path, rec.status, outcome)
	})
}

func (s *sim) requireSecret(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.secret != nil && strings.HasPrefix(r.URL.Path, "/v1/internal/") {
			provided := []byte(r.Header.Get("X-Internal-Secret"))
			if len(provided) == 0 || subtle.ConstantTimeCompare(provided, s.secret) != 1 {
				setOutcome(w, "bad_secret")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "FORBIDDEN", "code": "FORBIDDEN"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	scheme, rest, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(rest)
}

func (s *sim) validateToken(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		setOutcome(w, "missing_token")
		http.Error(w, unauthorizedBody, http.StatusUnauthorized)
		return
	}
	e, ok := (*s.claims.Load())[token]
	if !ok {
		setOutcome(w, "unknown")
		http.Error(w, invalidTokenBody, http.StatusUnauthorized)
		return
	}
	now := time.Now()
	exp := now.Add(defaultLifetime).Unix()
	if e.Exp != nil {
		exp = *e.Exp
	}
	if e.Expired || !now.Before(time.Unix(exp, 0)) {
		setOutcome(w, "expired")
		http.Error(w, invalidTokenBody, http.StatusUnauthorized)
		return
	}
	setOutcome(w, "known")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(validResponse{
		Message: "Token is valid",
		Data: answeredClaims{
			UserID:                  e.UserID,
			Email:                   e.Email,
			Name:                    e.Name,
			Tier:                    e.Tier,
			CustomRoleID:            e.CustomRoleID,
			OrganizationID:          e.OrganizationID,
			OrganizationName:        e.OrganizationName,
			OrganizationDisplayName: e.OrganizationDisplayName,
			GroupIDs:                []string{},
			Exp:                     exp,
		},
	})
}

func (s *sim) healthz(w http.ResponseWriter, _ *http.Request) {
	setOutcome(w, "health")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
}
