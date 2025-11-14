package betterlogs

import (
	"database/sql"
	"time"

	"go.uber.org/zap/zapcore"
)

type Config struct {
	// Identifies the service in logs (e.g. "todos-api", "auth-service").
	ServiceName string

	// Optional DB used for Postgres log sink.
	// If nil and EnablePostgres is true, New will return an error.
	DB *sql.DB

	// Global log level (applies to all cores).
	Level zapcore.Level

	// Whether to log to stdout in JSON (recommended: true).
	Stdout bool

	// Enable Postgres sink. If true, DB must not be nil.
	EnablePostgres bool

	// Postgres sink options.
	PG struct {
		BatchSize  int           // number of log lines per COPY batch.
		MaxWait    time.Duration // max wait before flushing batch.
		BufferSize int           // channel buffer size.
	}
}

// DefaultConfig returns a sane default configuration.
func DefaultConfig() Config {
	var cfg Config

	cfg.ServiceName = "app"
	cfg.Level = zapcore.InfoLevel
	cfg.Stdout = true

	cfg.EnablePostgres = false
	cfg.PG.BatchSize = 1000
	cfg.PG.MaxWait = 5 * time.Second
	cfg.PG.BufferSize = 10_000

	return cfg
}
