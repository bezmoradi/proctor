package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bezmoradi/proctor/protocol"
)

const (
	MaxRows        = 10000
	QueryTimeout   = 5 * time.Second
	ConnectTimeout = 5 * time.Second
	deadlineMargin = 250 * time.Millisecond
	minStatement   = 10 * time.Millisecond
)

type conn struct {
	name    string
	cfg     *pgx.ConnConfig
	secrets []string
	schemas []string
	objects []string
	pg      *pgx.Conn
}

func lastingState(code string) bool {
	return strings.HasPrefix(code, "28") || code == "3D000" || code == "42501"
}

func sqlState(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

var (
	passwordField = regexp.MustCompile(`(?i)password=\S+`)
	urlPassword   = regexp.MustCompile(`(://[^:/@\s]*):[^@\s]*@`)
)

func (c *conn) scrub(s string) string {
	for _, secret := range c.secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	s = passwordField.ReplaceAllString(s, "password=[redacted]")
	return urlPassword.ReplaceAllString(s, "$1:[redacted]@")
}

func (c *conn) classify(ctx context.Context, err error, what string) *protocol.Error {
	var pe *pgconn.PgError
	switch {
	case errors.As(err, &pe):
		cause := protocol.CauseQueryError
		if pe.Code == "57014" {
			cause = protocol.CauseTimeout
		}
		return protocol.Errorf(cause, "%s: %s (SQLSTATE %s)", what, c.scrub(pe.Message), pe.Code)
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || pgconn.Timeout(err):
		return protocol.Errorf(protocol.CauseTimeout, "%s: timed out", what)
	}
	return protocol.Errorf(protocol.CauseUnreachable, "%s: %s", what, c.scrub(err.Error()))
}

func (c *conn) open(ctx context.Context) error {
	if c.pg != nil && !c.pg.IsClosed() {
		return nil
	}
	c.drop()
	if c.cfg == nil {
		return protocol.Lastingf(protocol.CauseUnreachable, "connection %s: the connection string doesn't parse", c.name)
	}
	cfg := c.cfg.Copy()
	cfg.ConnectTimeout = ConnectTimeout
	ctx, cancel := context.WithTimeout(ctx, ConnectTimeout)
	defer cancel()
	pg, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		e := c.classify(ctx, err, "connection "+c.name)
		if e.Data.Cause != protocol.CauseTimeout {
			e.Data.Cause = protocol.CauseUnreachable
		}
		e.Data.Lasting = lastingState(sqlState(err))
		return e
	}
	quoted := make([]string, len(c.schemas))
	for i, s := range c.schemas {
		quoted[i] = pgx.Identifier{s}.Sanitize()
	}
	if _, err := pg.Exec(ctx, "SELECT set_config('search_path', $1, false)", strings.Join(quoted, ", ")); err != nil {
		e := c.classify(ctx, err, "connection "+c.name+": setting search_path")
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = pg.Close(closeCtx)
		closeCancel()
		return e
	}
	c.pg = pg
	return nil
}

func (c *conn) drop() {
	if c.pg == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.pg.Close(ctx)
	c.pg = nil
}

func (c *conn) ping(ctx context.Context) error {
	_, _, err := c.query(ctx, "SELECT 1", nil)
	return err
}

func statementTimeout(ctx context.Context) (time.Duration, error) {
	t := QueryTimeout
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl) - deadlineMargin; left < t {
			t = left
		}
	}
	if t < minStatement {
		return 0, protocol.Errorf(protocol.CauseTimeout, "the deadline leaves no time to run the query")
	}
	return t, nil
}

func (c *conn) query(ctx context.Context, sql string, args []any) ([]string, [][]any, error) {
	if err := c.open(ctx); err != nil {
		return nil, nil, err
	}
	timeout, err := statementTimeout(ctx)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout+time.Second)
	defer cancel()
	tx, err := c.pg.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, nil, c.classify(ctx, err, "connection "+c.name+": begin")
	}
	defer func() {
		rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer rcancel()
		_ = tx.Rollback(rctx)
	}()
	if _, err := tx.Exec(ctx, "SELECT set_config('statement_timeout', $1, true)", fmt.Sprintf("%dms", timeout.Milliseconds())); err != nil {
		return nil, nil, c.classify(ctx, err, "connection "+c.name+": setting statement_timeout")
	}
	all := append([]any{pgx.QueryResultFormats{pgx.TextFormatCode}}, args...)
	rows, err := tx.Query(ctx, sql, all...)
	if err != nil {
		return nil, nil, c.classify(ctx, err, "query")
	}
	defer rows.Close()
	fds := rows.FieldDescriptions()
	cols := make([]string, len(fds))
	for i, fd := range fds {
		cols[i] = fd.Name
	}
	var out [][]any
	for rows.Next() {
		if len(out) == MaxRows {
			return nil, nil, protocol.Errorf(protocol.CauseUnsupportedResult, "query returned more than %d rows", MaxRows)
		}
		raw := rows.RawValues()
		row := make([]any, len(raw))
		for i, b := range raw {
			v, err := convert(fds[i].DataTypeOID, b)
			if err != nil {
				return nil, nil, protocol.Errorf(protocol.CauseUnsupportedResult, "column %s: %v", cols[i], err)
			}
			row[i] = v
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, c.classify(ctx, err, "query")
	}
	return cols, out, nil
}

const (
	oidBool    = 16
	oidBytea   = 17
	oidInt8    = 20
	oidInt2    = 21
	oidInt4    = 23
	oidOID     = 26
	oidJSON    = 114
	oidFloat4  = 700
	oidFloat8  = 701
	oidNumeric = 1700
	oidJSONB   = 3802
)

func convert(oid uint32, b []byte) (any, error) {
	if b == nil {
		return nil, nil
	}
	s := string(b)
	switch oid {
	case oidBool:
		switch s {
		case "t":
			return true, nil
		case "f":
			return false, nil
		}
		return nil, fmt.Errorf("unexpected boolean %q", s)
	case oidInt2, oidInt4, oidInt8, oidOID:
		return json.Number(s), nil
	case oidFloat4, oidFloat8, oidNumeric:
		switch s {
		case "NaN", "Infinity", "-Infinity":
			return s, nil
		}
		return json.Number(s), nil
	case oidJSON, oidJSONB:
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		return v, nil
	case oidBytea:
		return nil, errors.New("bytea can't be compared; select encode(column, 'hex') instead")
	}
	return s, nil
}

func argValue(v any) any {
	switch t := v.(type) {
	case nil, string, bool:
		return t
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
