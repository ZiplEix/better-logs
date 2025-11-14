package betterlogs

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

func getTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://postgres:password@localhost:5433/better-logs_test?sslmode=disable"
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("Failed to open test database: %v", err)
	}

	if err := db.Ping(); err != nil {
		t.Fatalf("Failed to ping test database: %v", err)
	}

	return db
}

func TestEnsureLogsTable_CreatesTableAndIsIdempotent(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), (2 * time.Second))
	defer cancel()

	if err := EnsureLogsTable(ctx, db); err != nil {
		t.Fatalf("EnsureLogsTable first call failed: %v", err)
	}

	// test idepotent
	if err := EnsureLogsTable(ctx, db); err != nil {
		t.Fatalf("EnsureLogsTable second call (idempotent) failed: %v", err)
	}

	const query = `
        SELECT column_name
        FROM information_schema.columns
        WHERE table_name = 'logs'
        ORDER BY ordinal_position;
    `
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("failed to query information_schema for logs table: %v", err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("failed to scan column name: %v", err)
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v\n", err)
	}

	if len(cols) == 0 {
		t.Fatalf("logs table has no columns; seems it does not exist")
	}

	hasID := false
	hasRaw := false
	for _, c := range cols {
		if c == "id" {
			hasID = true
		}
		if c == "raw" {
			hasRaw = true
		}
	}

	if !hasID || !hasRaw {
		t.Fatalf("logs table missing expected columns: id=%v raw=%v (columns: %v)", hasID, hasRaw, cols)
	}
}

func TestEnsureLogsTable_NilDB(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := EnsureLogsTable(ctx, nil)
	if err == nil {
		t.Fatalf("expected error when db is nil, got nil")
	}
}
