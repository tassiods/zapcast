// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

// Migrations.
//
// Each .sql file under migrations/ is applied in lexicographic order
// inside its own transaction. A row in schema_migrations records the
// filename once the transaction commits, so re-runs are idempotent.
//
// New migrations: create migrations/NNNN_some_change.sql with a NNNN higher
// than any existing one — the runner picks them up at the next boot.

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// runMigrations applies every pending migration in lexicographic order.
// Stops at the first failure with the SQL state preserved (transaction
// rolled back, schema_migrations untouched for that version).
func runMigrations(ctx context.Context, lg *slog.Logger, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := loadAppliedMigrations(ctx, db)
	if err != nil {
		return err
	}

	pending, err := listPendingMigrations(applied)
	if err != nil {
		return err
	}

	for _, name := range pending {
		if err := applyMigration(ctx, db, name); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if lg != nil {
			lg.Info("migration applied", "version", name)
		}
	}
	return nil
}

func loadAppliedMigrations(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("list applied: %w", err)
	}
	defer rows.Close()

	applied := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func listPendingMigrations(applied map[string]bool) ([]string, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	var pending []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		if applied[name] {
			continue
		}
		pending = append(pending, name)
	}
	sort.Strings(pending)
	return pending, nil
}

func applyMigration(ctx context.Context, db *sql.DB, name string) error {
	body, err := fs.ReadFile(migrationsFS, "migrations/"+name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		return fmt.Errorf("exec %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES (?)", name); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return tx.Commit()
}
