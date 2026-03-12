package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// user represents a registered user.
type User struct {
	ID           string
	Email        string
	PasswordHash string
	Role         string
	CreatedAt    string
}

// user_store defines the interface for user operations.
type UserStore interface {
	Save(ctx context.Context, user *User) error
	Find(ctx context.Context, email string) (*User, error)
}

// pg_user_store implements user_store using pgx.
type pgUserStore struct {
	pool *pgxpool.Pool
}

// new_user_store creates a new postgres user store.
func NewUserStore(pool *pgxpool.Pool) UserStore {
	return &pgUserStore{pool: pool}
}

// save inserts a new user into db.
func (s *pgUserStore) Save(ctx context.Context, user *User) error {
	// $1::uuid and $5::timestamptz avoid pgx v5 type-inference ambiguity when
	// passing plain Go strings for native UUID / TIMESTAMP WITH TIME ZONE columns.
	query := `
		INSERT INTO users (id, email, password_hash, role, created_at)
		VALUES ($1::uuid, $2, $3, $4, $5::timestamptz)
	`
	_, err := s.pool.Exec(ctx, query, user.ID, user.Email, user.PasswordHash, user.Role, user.CreatedAt)
	return err
}

// find retrieves a user by their email address.
func (s *pgUserStore) Find(ctx context.Context, email string) (*User, error) {
	// cast uuid and timestamptz columns to text so pgx v5 can scan them into
	// plain go strings without requiring pgtype.uuid / pgtype.timestamptz.
	query := `
		SELECT id::text, email, password_hash, role, created_at::text
		FROM users
		WHERE email = $1
	`

	user := &User{}
	err := s.pool.QueryRow(ctx, query, email).Scan(
		&user.ID,
		&user.Email,
		&user.PasswordHash,
		&user.Role,
		&user.CreatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("user not found")
		}
		return nil, err
	}

	return user, nil
}
