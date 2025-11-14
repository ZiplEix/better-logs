package betterlogs

import (
	"context"
	"errors"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestNew_NoCoreEnabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Stdout = false
	cfg.EnablePostgres = false

	logger, cleanup, err := New(cfg)
	if err == nil {
		t.Fatalf("expected error when no core is enabled, got nil")
	}
	if !errors.Is(err, errors.New("better-logs: no logging core enabled (Stdout=false and EnablePostgres=false)")) {
		t.Logf("got error as expected: %v", err)
	}
	if logger != nil || cleanup != nil {
		t.Fatalf("expected nil logger and cleanup on error")
	}
}

func TestNew_PostgresEnabledButNilDB(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Stdout = false
	cfg.EnablePostgres = true
	cfg.DB = nil

	logger, cleanup, err := New(cfg)
	if err == nil {
		t.Fatalf("expected error when EnablePostgres=true and DB=nil, got nil")
	}
	if logger != nil || cleanup != nil {
		t.Fatalf("expected nil logger and cleanup on error")
	}
}

func TestNew_StdoutOnly(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ServiceName = "test-stdout-only"
	cfg.Stdout = true
	cfg.EnablePostgres = false
	cfg.Level = zapcore.InfoLevel

	logger, cleanup, err := New(cfg)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if logger == nil {
		t.Fatalf("expected non-nil logger")
	}
	if cleanup == nil {
		t.Fatalf("expected non-nil cleanup function")
	}

	logger.Info("stdout-only log", zap.String("test_case", "stdout_only"))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := cleanup(ctx); err != nil {
		t.Logf("cleanup returned error (ignored): %v", err)
	}
}

func TestNew_WithPostgresCore_InsertsLog(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := EnsureLogsTable(ctx, db); err != nil {
		t.Fatalf("EnsureLogsTable failed: %v", err)
	}

	cfg := DefaultConfig()
	cfg.ServiceName = "test-pg-core"
	cfg.Stdout = false
	cfg.EnablePostgres = true
	cfg.DB = db
	cfg.Level = zapcore.InfoLevel

	logger, cleanup, err := New(cfg)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if logger == nil || cleanup == nil {
		t.Fatalf("expected non-nil logger and cleanup")
	}

	zap.ReplaceGlobals(logger)

	const msg = "pgcore test log"
	logger.Info(msg, zap.String("request_id", "test-req-123"))

	if err := cleanup(ctx); err != nil {
		t.Logf("cleanup returned error (ignored): %v", err)
	}

	var count int
	query := `SELECT count(*) FROM logs WHERE raw->>'msg' = $1`
	if err := db.QueryRowContext(ctx, query, msg).Scan(&count); err != nil {
		t.Fatalf("failed to query logs table: %v", err)
	}

	if count == 0 {
		t.Fatalf("expected at least one log row with msg=%q, got 0", msg)
	}
}
