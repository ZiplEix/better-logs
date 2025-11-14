package betterlogs

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/ZiplEix/better-logs/pgcore"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New initializes global logging based on the provided Config.
//
// It returns:
//   - a *zap.Logger instance
//   - a cleanup func(ctx) error to flush buffers and close background workers
//   - an error if initialization fails
func New(cfg Config) (*zap.Logger, func(context.Context) error, error) {
	if cfg.ServiceName == "" {
		cfg.ServiceName = "app"
	}

	encCfg := zap.NewProductionEncoderConfig()
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	encCfg.EncodeDuration = zapcore.MillisDurationEncoder

	var cores []zapcore.Core

	// stdout core
	if cfg.Stdout {
		consoleCore := zapcore.NewCore(
			zapcore.NewJSONEncoder(encCfg),
			zapcore.AddSync(os.Stdout),
			cfg.Level,
		)
		cores = append(cores, consoleCore)
	}

	// postgres core
	var pgClose func(context.Context) error
	if cfg.EnablePostgres {
		if cfg.DB == nil {
			return nil, nil, errors.New("better-logs: EnablePostgres is true but DB is nil")
		}

		pgCfg := pgcore.Config{
			Level:      cfg.Level,
			BatchSize:  cfg.PG.BatchSize,
			MaxWait:    cfg.PG.MaxWait,
			BufferSize: cfg.PG.BufferSize,
		}

		pgCore, closeFn, err := pgcore.New(cfg.DB, pgCfg)
		if err != nil {
			return nil, nil, fmt.Errorf("better-logs: failed to init pg core: %w", err)
		}
		cores = append(cores, pgCore)
		pgClose = closeFn
	}

	if len(cores) == 0 {
		return nil, nil, errors.New("better-logs: no logging core enabled (Stdout=false and EnablePostgres=false)")
	}

	tee := zapcore.NewTee(cores...)

	logger := zap.New(
		tee,
		zap.AddCaller(),
		zap.AddStacktrace(zapcore.ErrorLevel),
		zap.Fields(
			zap.String("service", cfg.ServiceName),
		),
	)

	zap.ReplaceGlobals(logger)

	cleanup := func(ctx context.Context) error {
		var firstErr error

		// Sync logger first.
		if err := logger.Sync(); err != nil && !isSyncNoop(err) {
			firstErr = err
		}

		if pgClose != nil {
			// Give pgClose some time (but respect ctx deadline).
			done := make(chan struct{})
			go func() {
				_ = pgClose(ctx)
				close(done)
			}()

			select {
			case <-ctx.Done():
				if firstErr == nil {
					firstErr = ctx.Err()
				}
			case <-done:
			}
		}

		return firstErr
	}

	return logger, cleanup, nil
}

// isSyncNoop filters out common non-critical sync errors (e.g. EOF on stdout/stderr).
func isSyncNoop(err error) bool {
	// Often zap.Sync() on stdout/stderr returns "invalid argument" on some platforms.
	// You can enrich this if you encounter noisy errors.
	return false
}
