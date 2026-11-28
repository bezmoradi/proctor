package main

import "net/http"

var setCookiesSchema = map[string]any{
	"type": "array",
	"items": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":        map[string]any{"type": "string"},
			"host_only":   map[string]any{"type": "boolean"},
			"domain":      map[string]any{"type": "string"},
			"path":        map[string]any{"type": "string"},
			"expires":     map[string]any{"type": "string"},
			"max_age":     map[string]any{"type": "integer"},
			"secure":      map[string]any{"type": "boolean"},
			"http_only":   map[string]any{"type": "boolean"},
			"same_site":   map[string]any{"type": "string", "enum": []string{"Strict", "Lax", "None", "unrecognized"}},
			"partitioned": map[string]any{"type": "boolean"},
		},
		"required":             []string{"name", "host_only", "secure", "http_only", "partitioned"},
		"additionalProperties": false,
	},
}

func setCookies(resp *http.Response) []any {
	var out []any
	for _, c := range resp.Cookies() {
		m := map[string]any{"name": c.Name, "host_only": c.Domain == "", "secure": c.Secure, "http_only": c.HttpOnly, "partitioned": c.Partitioned}
		if c.Domain != "" {
			m["domain"] = c.Domain
		}
		if c.Path != "" {
			m["path"] = c.Path
		}
		if c.RawExpires != "" {
			m["expires"] = c.RawExpires
		}
		switch {
		case c.MaxAge > 0:
			m["max_age"] = c.MaxAge
		case c.MaxAge < 0:
			m["max_age"] = 0
		}
		switch c.SameSite {
		case http.SameSiteStrictMode:
			m["same_site"] = "Strict"
		case http.SameSiteLaxMode:
			m["same_site"] = "Lax"
		case http.SameSiteNoneMode:
			m["same_site"] = "None"
		case http.SameSiteDefaultMode:
			m["same_site"] = "unrecognized"
		}
		out = append(out, m)
	}
	return out
}
