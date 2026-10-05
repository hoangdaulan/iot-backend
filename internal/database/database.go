// Package database connects to PostgreSQL and applies the embedded migrations and seeds.
package database

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pool, retrying until the database accepts connections or wait elapses
// (the database container may still be starting).
func Connect(ctx context.Context, url string, wait time.Duration, logger *slog.Logger) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	deadline := time.Now().Add(wait)
	for {
		pingErr := pool.Ping(ctx)
		if pingErr == nil {
			return pool, nil
		}
		if time.Now().After(deadline) {
			pool.Close()
			return nil, fmt.Errorf("connect to database: %w", pingErr)
		}
		logger.Info("waiting for database", "error", pingErr)
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// Migrate applies, in order, each *.sql file of files that has not been applied yet. Each file
// runs in its own transaction and is recorded in schema_migrations.
func Migrate(ctx context.Context, pool *pgxpool.Pool, files fs.FS, logger *slog.Logger) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	names, err := sqlFiles(files)
	if err != nil {
		return err
	}
	for _, name := range names {
		var applied bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name,
		).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied {
			continue
		}
		sql, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
		logger.Info("applied migration", "version", name)
	}
	return nil
}

// Seed runs every *.sql file of files. Seed scripts must be idempotent.
func Seed(ctx context.Context, pool *pgxpool.Pool, files fs.FS, logger *slog.Logger) error {
	names, err := sqlFiles(files)
	if err != nil {
		return err
	}
	for _, name := range names {
		sql, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("run seed %s: %w", name, err)
		}
		logger.Info("applied seed", "file", name)
	}
	return nil
}

func sqlFiles(files fs.FS) ([]string, error) {
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}
