package repository

import (
	"context"
	"errors"

	"example/grpc/models"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// user_repository defines the interface for creating and fetching users.
type UserRepository interface {
	Create(ctx context.Context, user *models.User) error
	GetByEmail(ctx context.Context, email string) (*models.User, error)
	GetByID(ctx context.Context, id string) (*models.User, error)
}

// pgx_user_repository implements the user_repository using pgx for high performance.
type pgxUserRepository struct {
	pool *pgxpool.Pool
}

// new_user_repository creates a new user_repository with the given connection pool.
func NewUserRepository(pool *pgxpool.Pool) UserRepository {
	return &pgxUserRepository{
		pool: pool,
	}
}

// create inserts a new user into the database and sets its generated id and created_at.
func (r *pgxUserRepository) Create(ctx context.Context, user *models.User) error {
	query := `
		INSERT INTO users (email, password_hash, role, is_verified)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at
	`

	// role is set to 'user' by default in DB if not provided,
	// but pgx doesn't implicitly skip unless we modify the query dynamically.
	// assume user struct has defaults set before calling.
	// or we use coalesce/nullif, but it's simpler to just pass the struct values.
	role := user.Role
	if role == "" {
		role = "user"
	}

	err := r.pool.QueryRow(ctx, query,
		user.Email,
		user.PasswordHash,
		role,
		user.IsVerified,
	).Scan(&user.ID, &user.CreatedAt)

	if err != nil {
		return err
	}

	return nil
}

// get_by_email retrieves a user by their unique email address.
func (r *pgxUserRepository) GetByEmail(ctx context.Context, email string) (*models.User, error) {
	query := `
		SELECT id, email, password_hash, role, is_verified, created_at
		FROM users
		WHERE email = $1
	`

	user := &models.User{}
	err := r.pool.QueryRow(ctx, query, email).Scan(
		&user.ID,
		&user.Email,
		&user.PasswordHash,
		&user.Role,
		&user.IsVerified,
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

// get_by_id retrieves a user by their unique uuid.
func (r *pgxUserRepository) GetByID(ctx context.Context, id string) (*models.User, error) {
	query := `
		SELECT id, email, password_hash, role, is_verified, created_at
		FROM users
		WHERE id = $1
	`

	user := &models.User{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&user.ID,
		&user.Email,
		&user.PasswordHash,
		&user.Role,
		&user.IsVerified,
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
