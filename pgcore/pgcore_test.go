package pgcore

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func getTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://postgres:password@localhost:5433/better-logs_test?sslmode=disable"
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatalf("failed to ping test database: %v", err)
	}

	return db
}

func ensureLogsTable(t *testing.T, db *sql.DB) {
	t.Helper()

	const ddl = `
CREATE TABLE IF NOT EXISTS logs (
    id      BIGSERIAL PRIMARY KEY,
    ts      TIMESTAMPTZ NOT NULL DEFAULT now(),
    req_id  TEXT,
    raw     JSONB NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_logs_ts ON logs (ts DESC);
CREATE INDEX IF NOT EXISTS idx_logs_req_id ON logs (req_id);
`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := db.ExecContext(ctx, ddl); err != nil {
		t.Fatalf("failed to ensure logs table: %v", err)
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM logs`); err != nil {
		t.Fatalf("failed to clean logs table: %v", err)
	}
}

func TestPgcore_WritesLogToDatabase(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	ensureLogsTable(t, db)

	cfg := Config{
		Level:      zapcore.InfoLevel,
		BatchSize:  1000,
		MaxWait:    2 * time.Second,
		BufferSize: 1000,
	}

	core, closeFn, err := New(db, cfg)
	if err != nil {
		t.Fatalf("pgcore.New returned error: %v", err)
	}
	if core == nil || closeFn == nil {
		t.Fatalf("expected non-nil core and closeFn")
	}

	logger := zap.New(core)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const msg = "pgcore_test_log_insert"
	const reqID = "pgcore-req-123"

	logger.Info(msg, zap.String("request_id", reqID))

	// Flush and stop the background worker
	if err := closeFn(ctx); err != nil {
		t.Logf("closeFn returned error (ignored): %v", err)
	}

	var count int
	query := `
SELECT count(*)
FROM logs
WHERE raw->>'msg' = $1
  AND (req_id = $2 OR raw->>'request_id' = $2);
`
	if err := db.QueryRowContext(ctx, query, msg, reqID).Scan(&count); err != nil {
		t.Fatalf("failed to query logs table: %v", err)
	}

	if count == 0 {
		t.Fatalf("expected at least one row with msg=%q and request_id=%q, got 0", msg, reqID)
	}
}

func TestPgcore_RespectsLogLevel(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	ensureLogsTable(t, db)

	cfg := Config{
		Level:      zapcore.WarnLevel,
		BatchSize:  1000,
		MaxWait:    2 * time.Second,
		BufferSize: 1000,
	}

	core, closeFn, err := New(db, cfg)
	if err != nil {
		t.Fatalf("pgcore.New returned error: %v", err)
	}
	if core == nil || closeFn == nil {
		t.Fatalf("expected non-nil core and closeFn")
	}

	logger := zap.New(core)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const msgInfo = "pgcore_should_not_be_logged_info"
	const msgWarn = "pgcore_should_be_logged_warn"

	logger.Info(msgInfo)

	logger.Warn(msgWarn)

	if err := closeFn(ctx); err != nil {
		t.Logf("closeFn returned error (ignored): %v", err)
	}

	var countInfo int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM logs WHERE raw->>'msg' = $1`, msgInfo).Scan(&countInfo); err != nil {
		t.Fatalf("failed to query logs table for info msg: %v", err)
	}
	if countInfo != 0 {
		t.Fatalf("expected 0 rows for msg=%q at level Info with Level=Warn, got %d", msgInfo, countInfo)
	}

	var countWarn int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM logs WHERE raw->>'msg' = $1`, msgWarn).Scan(&countWarn); err != nil {
		t.Fatalf("failed to query logs table for warn msg: %v", err)
	}
	if countWarn == 0 {
		t.Fatalf("expected at least 1 row for msg=%q at level Warn, got 0", msgWarn)
	}
}
