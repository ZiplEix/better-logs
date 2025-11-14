package betterauth

import (
	"context"
	"database/sql"
	"fmt"
)

const logsTableDDL = `
CREATE TABLE IF NOT EXISTS logs (
    id      BIGSERIAL PRIMARY KEY,
    ts      TIMESTAMPTZ NOT NULL DEFAULT now(),
    req_id  TEXT,
    raw     JSONB NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_logs_ts ON logs (ts DESC);
CREATE INDEX IF NOT EXISTS idx_logs_req_id ON logs (req_id);
`

// EnsureLogsTable creates the logs table (and indexes) if they don't already exist.
// It is safe to call multiple times.
func EnsureLogsTable(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("better-logs: db is nil in EnsureLogsTable")
	}

	// ExecContext can run multiple statements in one string for Postgres.
	if _, err := db.ExecContext(ctx, logsTableDDL); err != nil {
		return fmt.Errorf("better-logs: creating logs table failed: %w", err)
	}

	return nil
}
