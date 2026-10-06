// Package postgres implements the repository interfaces with pgx.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"iot-backend/internal/model"
	"iot-backend/internal/repository"
)

const uniqueViolation = "23505"

type UserRepository struct{ pool *pgxpool.Pool }

func NewUserRepository(pool *pgxpool.Pool) *UserRepository { return &UserRepository{pool: pool} }

const userColumns = `id, name, email, password, username, phone, avatar, github, figma, swagger, role,
	created_at, updated_at`

func scanUser(row pgx.Row) (*model.User, error) {
	var u model.User
	err := row.Scan(&u.ID, &u.Name, &u.Email, &u.PasswordHash, &u.Username, &u.Phone, &u.Avatar,
		&u.Github, &u.Figma, &u.Swagger, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *UserRepository) Create(ctx context.Context, u *model.User) error {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO users (name, email, password, username, phone, avatar, github, figma, swagger, role)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id, created_at, updated_at`,
		u.Name, u.Email, u.PasswordHash, u.Username, u.Phone, u.Avatar, u.Github, u.Figma, u.Swagger, u.Role,
	).Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return repository.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

func (r *UserRepository) FindByLogin(ctx context.Context, login string) (*model.User, error) {
	return scanUser(r.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = $1 OR email = lower($1) LIMIT 1`, login))
}

func (r *UserRepository) FindByID(ctx context.Context, id int64) (*model.User, error) {
	return scanUser(r.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

func (r *UserRepository) UpdateProfile(
	ctx context.Context, id int64, p repository.ProfileUpdate,
) (*model.User, error) {
	return scanUser(r.pool.QueryRow(ctx, `
		UPDATE users SET
			name = COALESCE($2, name),
			phone = COALESCE($3, phone),
			avatar = COALESCE($4, avatar),
			github = COALESCE($5, github),
			figma = COALESCE($6, figma),
			swagger = COALESCE($7, swagger),
			updated_at = now()
		WHERE id = $1
		RETURNING `+userColumns,
		id, p.Name, p.Phone, p.Avatar, p.Github, p.Figma, p.Swagger))
}

func (r *UserRepository) UpdatePassword(ctx context.Context, id int64, passwordHash string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE users SET password = $2, updated_at = now() WHERE id = $1`, id, passwordHash)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}
