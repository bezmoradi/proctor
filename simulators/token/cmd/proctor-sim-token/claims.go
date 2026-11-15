package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

type entry struct {
	UserID                  string  `json:"user_id"`
	Email                   string  `json:"email"`
	Name                    string  `json:"name"`
	Tier                    string  `json:"tier"`
	CustomRoleID            *string `json:"custom_role_id"`
	OrganizationID          string  `json:"organization_id"`
	OrganizationName        string  `json:"organization_name"`
	OrganizationDisplayName string  `json:"organization_display_name"`
	Exp                     *int64  `json:"exp"`
	Expired                 bool    `json:"expired"`
}

type claimsFile struct {
	Tokens map[string]json.RawMessage `json:"tokens"`
}

func decodeStrict(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after the JSON object")
	}
	return nil
}

func isNull(raw []byte) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func loadClaims(path string) (map[string]entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read claims file: %w", err)
	}
	if isNull(data) {
		return nil, errors.New("claims file must be a JSON object")
	}
	var file claimsFile
	if err := decodeStrict(data, &file); err != nil {
		return nil, fmt.Errorf("parse claims file: %w", err)
	}
	if file.Tokens == nil {
		return nil, errors.New(`claims file must have a "tokens" object`)
	}
	keys := make([]string, 0, len(file.Tokens))
	for k := range file.Tokens {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make(map[string]entry, len(keys))
	for i, k := range keys {
		n := i + 1
		if k == "" || k != strings.TrimSpace(k) {
			return nil, fmt.Errorf("token entry %d (in key order): the token must be non-empty without surrounding whitespace", n)
		}
		raw := file.Tokens[k]
		if isNull(raw) {
			return nil, fmt.Errorf("token entry %d (in key order): claims must be a JSON object", n)
		}
		var e entry
		if err := decodeStrict(raw, &e); err != nil {
			return nil, fmt.Errorf("token entry %d (in key order): %w", n, err)
		}
		if e.Exp != nil && *e.Exp <= 0 {
			return nil, fmt.Errorf("token entry %d (in key order): exp must be a positive Unix time", n)
		}
		out[k] = e
	}
	return out, nil
}
