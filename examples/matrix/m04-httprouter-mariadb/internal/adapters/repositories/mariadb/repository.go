package mariadb

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"m04-httprouter-mariadb/internal/core/user/domain"

	"github.com/go-sql-driver/mysql"
)

const tableName = "users"

// parseID converts a client-supplied id to the width the id column actually is:
// `id INT AUTO_INCREMENT` is 4 bytes (see migrations/), not a Go int. strconv.Atoi
// parses 64 bits, so it waves through every value in (2^31-1, 2^63-1) and hands
// the driver an id the column cannot represent. Parse at the column's real width
// and an out-of-range id is simply a miss. Widen the bit size here if you widen
// the column to BIGINT.
func parseID(id string) (int32, bool) {
	parsed, err := strconv.ParseInt(id, 10, 32)
	if err != nil {
		return 0, false
	}

	return int32(parsed), true
}

// connectTimeout bounds the startup ping, so a wrong host or a firewalled port
// stops the app within seconds instead of on the first request.
const connectTimeout = 5 * time.Second

// Repository stores users in MariaDB.
type Repository struct {
	db *sql.DB
}

// New connects with the MARIADB_* environment and pings the server, so a wrong
// host, port or password stops the app at startup.
func New() (*Repository, error) {
	cfg := mysql.NewConfig()
	cfg.User = os.Getenv("MARIADB_USERNAME")
	cfg.Passwd = os.Getenv("MARIADB_PASSWORD")
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(os.Getenv("MARIADB_HOST"), os.Getenv("MARIADB_PORT"))
	cfg.DBName = os.Getenv("MARIADB_DATABASE")
	cfg.Timeout = 10 * time.Second
	// Report rows matched rather than rows changed, so an UPDATE that sets the
	// name it already has still counts as finding the user (not a 404).
	cfg.ClientFoundRows = true

	// A Config, not a formatted DSN string, so the credentials need no escaping.
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("invalid MariaDB configuration: %w", err)
	}
	db := sql.OpenDB(connector)
	db.SetConnMaxLifetime(0)
	db.SetMaxIdleConns(50)
	db.SetMaxOpenConns(50)

	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cannot reach MariaDB: %w", err)
	}

	// Schema is owned by the versioned migrations in migrations/ (run `make
	// migrate-up`), not created here — so the app never races on DDL at startup.
	return &Repository{db: db}, nil
}

// Ping reports whether the database answers; /readyz calls it.
func (r *Repository) Ping(ctx context.Context) error {
	return r.db.PingContext(ctx)
}

// Close releases the connection pool.
func (r *Repository) Close() error {
	return r.db.Close()
}

func (r *Repository) Insert(ctx context.Context, name string) (string, error) {
	result, err := r.db.ExecContext(
		ctx,
		fmt.Sprintf(
			"INSERT INTO %s (name) VALUES (?)",
			tableName,
		),
		name,
	)
	if err != nil {
		return "", err
	}

	lastInsertID, _ := result.LastInsertId()

	return strconv.FormatInt(lastInsertID, 10), nil
}

func (r *Repository) Update(ctx context.Context, id string, newName string) error {
	// An id the column can't hold can never match a row.
	numericID, ok := parseID(id)
	if !ok {
		return domain.ErrUserNotFound
	}

	result, err := r.db.ExecContext(
		ctx,
		fmt.Sprintf(
			"UPDATE %s SET name = ? WHERE id = ?",
			tableName,
		),
		newName,
		numericID,
	)
	if err != nil {
		return err
	}

	return requireRow(result)
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	// An id the column can't hold can never match a row.
	numericID, ok := parseID(id)
	if !ok {
		return domain.ErrUserNotFound
	}

	result, err := r.db.ExecContext(
		ctx,
		fmt.Sprintf(
			"DELETE FROM %s WHERE id = ?",
			tableName,
		),
		numericID,
	)
	if err != nil {
		return err
	}

	return requireRow(result)
}

func (r *Repository) Fetch(ctx context.Context, id string) (domain.User, error) {
	// An id the column can't hold can never match a row.
	numericID, ok := parseID(id)
	if !ok {
		return domain.User{}, domain.ErrUserNotFound
	}

	var user domain.User
	row := r.db.QueryRowContext(
		ctx,
		fmt.Sprintf(
			"SELECT id, name FROM %s WHERE id = ?",
			tableName,
		),
		numericID,
	)

	if err := row.Scan(&user.ID, &user.Name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.User{}, domain.ErrUserNotFound
		}
		return domain.User{}, fmt.Errorf("error fetching data: %w", err)
	}

	return user, nil
}

func (r *Repository) FetchAll(ctx context.Context, limit int, cursor string) (domain.Page, error) {
	// Self-defense: the service always clamps limit into [1, max], but a direct
	// caller must not be able to make this method panic on page.Items[limit-1].
	if limit < 1 {
		limit = 1
	}

	// Keyset pagination: the cursor is the last id from the previous page,
	// base64-encoded. Ordering by the integer primary key gives a stable,
	// gap-free walk; id > 0 selects everything for the first (cursor-less) page.
	var afterID int32
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return domain.Page{}, domain.ErrInvalidCursor
		}
		// A cursor outside the id column's range was never one we issued, so
		// it's a malformed token (400), not something to hand to the driver.
		parsed, ok := parseID(string(decoded))
		if !ok {
			return domain.Page{}, domain.ErrInvalidCursor
		}
		afterID = parsed
	}

	// Fetch one extra row to learn whether a further page exists.
	rows, err := r.db.QueryContext(
		ctx,
		fmt.Sprintf(
			"SELECT id, name FROM %s WHERE id > ? ORDER BY id ASC LIMIT ?",
			tableName,
		),
		afterID,
		limit+1,
	)
	if err != nil {
		return domain.Page{}, err
	}
	defer func() { _ = rows.Close() }()

	var users []domain.User
	for rows.Next() {
		var user domain.User
		if err := rows.Scan(&user.ID, &user.Name); err != nil {
			return domain.Page{}, err
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return domain.Page{}, err
	}

	page := domain.Page{Items: users}
	if len(users) > limit {
		page.Items = users[:limit]
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(page.Items[limit-1].ID))
	}

	return page, nil
}

// requireRow turns a write that matched no row into domain.ErrUserNotFound.
func requireRow(result sql.Result) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrUserNotFound
	}
	return nil
}
