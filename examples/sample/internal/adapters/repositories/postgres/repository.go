package postgres

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/bezmoradi/proctor/examples/sample/internal/core/user/domain"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const tableName = "users"

func parseID(id string) (int32, bool) {
	parsed, err := strconv.ParseInt(id, 10, 32)
	if err != nil {
		return 0, false
	}

	return int32(parsed), true
}

const connectTimeout = 5 * time.Second

type Repository struct {
	db *pgxpool.Pool
}

func New() (*Repository, error) {
	sslmode := os.Getenv("POSTGRES_SSLMODE")
	if sslmode == "" {
		return nil, errors.New("POSTGRES_SSLMODE is not set: use verify-full for a real server, disable for the local compose database")
	}

	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(os.Getenv("POSTGRES_USERNAME"), os.Getenv("POSTGRES_PASSWORD")),
		Host:     net.JoinHostPort(os.Getenv("POSTGRES_HOST"), os.Getenv("POSTGRES_PORT")),
		Path:     "/" + os.Getenv("POSTGRES_DATABASE"),
		RawQuery: url.Values{"sslmode": {sslmode}}.Encode(),
	}

	db, err := pgxpool.New(context.Background(), dsn.String())
	if err != nil {
		return nil, fmt.Errorf("invalid Postgres configuration: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if err := db.Ping(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot reach Postgres: %w", err)
	}

	return &Repository{db: db}, nil
}

func (r *Repository) Ping(ctx context.Context) error {
	return r.db.Ping(ctx)
}

func (r *Repository) Close() error {
	r.db.Close()
	return nil
}

func (r *Repository) Insert(ctx context.Context, name string) (string, error) {
	var insertedID int64
	err := r.db.QueryRow(
		ctx,
		fmt.Sprintf(
			"INSERT INTO %v (name) VALUES ($1) RETURNING id",
			tableName,
		),
		name,
	).Scan(&insertedID)
	if err != nil {
		return "", err
	}

	return strconv.FormatInt(insertedID, 10), nil
}

func (r *Repository) Update(ctx context.Context, id string, newName string) error {
	numericID, ok := parseID(id)
	if !ok {
		return domain.ErrUserNotFound
	}

	commandTag, err := r.db.Exec(
		ctx,
		fmt.Sprintf(
			"UPDATE %v SET name = $1 WHERE id = $2",
			tableName,
		),
		newName,
		numericID,
	)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() == 0 {
		return domain.ErrUserNotFound
	}

	return nil
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	numericID, ok := parseID(id)
	if !ok {
		return domain.ErrUserNotFound
	}

	commandTag, err := r.db.Exec(
		ctx,
		fmt.Sprintf(
			"DELETE FROM %v WHERE id = $1",
			tableName,
		),
		numericID,
	)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() == 0 {
		return domain.ErrUserNotFound
	}

	return nil
}

func (r *Repository) Fetch(ctx context.Context, id string) (domain.User, error) {
	numericID, ok := parseID(id)
	if !ok {
		return domain.User{}, domain.ErrUserNotFound
	}

	var user domain.User

	err := r.db.QueryRow(
		ctx,
		fmt.Sprintf(
			"SELECT id, name FROM %v WHERE id = $1",
			tableName,
		),
		numericID,
	).Scan(&user.ID, &user.Name)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrUserNotFound
		}
		return domain.User{}, err
	}

	return user, nil
}

func (r *Repository) FetchAll(ctx context.Context, limit int, cursor string) (domain.Page, error) {
	if limit < 1 {
		limit = 1
	}

	var afterID int32
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return domain.Page{}, domain.ErrInvalidCursor
		}

		parsed, ok := parseID(string(decoded))
		if !ok {
			return domain.Page{}, domain.ErrInvalidCursor
		}
		afterID = parsed
	}

	rows, err := r.db.Query(
		ctx,
		fmt.Sprintf(
			"SELECT id, name FROM %v WHERE id > $1 ORDER BY id ASC LIMIT $2",
			tableName,
		),
		afterID,
		limit+1,
	)
	if err != nil {
		return domain.Page{}, err
	}
	defer rows.Close()

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
