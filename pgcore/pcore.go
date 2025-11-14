package pgcore

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/lib/pq"
	"go.uber.org/zap/zapcore"
)

// Config controls how the Postgres zap core behaves.
type Config struct {
	// Minimum log level for this core.
	Level zapcore.Level

	// BatchSize is the maximum number of log lines to flush in a single COPY.
	// If zero or negative, a default of 1000 is used.
	BatchSize int

	// MaxWait is the maximum time to wait before flushing the current batch.
	// If zero or negative, a default of 2s is used.
	MaxWait time.Duration

	// BufferSize is the size of the in-memory channel buffer.
	// If zero or negative, a default of 10_000 is used.
	BufferSize int

	// RequestIDKeys lists possible keys in the JSON payload that may contain
	// a request/correlation ID. The first non-empty string found wins.
	// If empty, sensible defaults are used.
	RequestIDKeys []string
}

// core implements zapcore.Core and ships logs into Postgres in background.
type core struct {
	enc       zapcore.Encoder
	level     zapcore.LevelEnabler
	db        *sql.DB
	ch        chan []byte
	stop      chan struct{}
	wg        sync.WaitGroup
	batchSize int
	maxWait   time.Duration
	reqKeys   []string
}

// New creates a zapcore.Core that writes logs into the given Postgres DB,
// using COPY for batched inserts into a "logs" table with columns
//   - req_id TEXT
//   - raw   TEXT
//
// It also returns a close func(ctx) error that waits for pending logs to be
// flushed. The caller should invoke this during shutdown.
func New(db *sql.DB, cfg Config) (zapcore.Core, func(context.Context) error, error) {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 1000
	}
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = 2 * time.Second
	}
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 10_000
	}

	if len(cfg.RequestIDKeys) == 0 {
		cfg.RequestIDKeys = []string{
			"request_id",
			"req_id",
			"correlation_id",
			"X-Request-ID",
			"X-Correlation-ID",
			"requestid",
		}
	}

	encCfg := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		MessageKey:     "msg",
		CallerKey:      "caller",
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
		LineEnding:     zapcore.DefaultLineEnding,
		NameKey:        "logger",
		StacktraceKey:  "stack",
		EncodeDuration: zapcore.MillisDurationEncoder,
		EncodeName:     zapcore.FullNameEncoder,
	}

	c := &core{
		enc:       zapcore.NewJSONEncoder(encCfg),
		level:     cfg.Level,
		db:        db,
		ch:        make(chan []byte, cfg.BufferSize),
		stop:      make(chan struct{}),
		batchSize: cfg.BatchSize,
		maxWait:   cfg.MaxWait,
		reqKeys:   cfg.RequestIDKeys,
	}

	c.wg.Add(1)
	go c.loop()

	closeFn := func(ctx context.Context) error {
		close(c.stop)

		done := make(chan struct{})
		go func() {
			c.wg.Wait()
			close(done)
		}()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return nil
		}
	}

	return c, closeFn, nil
}

// Enabled implements zapcore.Core.
func (c *core) Enabled(lvl zapcore.Level) bool {
	return c.level.Enabled(lvl)
}

// With implements zapcore.Core.
func (c *core) With(fields []zapcore.Field) zapcore.Core {
	// Build a new core that shares the runtime resources (db, channels)
	// but does NOT copy the internal WaitGroup or other sync state.
	// Copying a WaitGroup leads to vet warnings and is unsafe.
	clone := &core{
		enc:       c.enc.Clone(),
		level:     c.level,
		db:        c.db,
		ch:        c.ch,
		stop:      c.stop,
		batchSize: c.batchSize,
		maxWait:   c.maxWait,
		reqKeys:   c.reqKeys,
	}

	for _, f := range fields {
		f.AddTo(clone.enc)
	}

	return clone
}

// Check implements zapcore.Core.
func (c *core) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}
	return ce
}

// Write implements zapcore.Core.
func (c *core) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	buf, err := c.enc.EncodeEntry(ent, fields)
	if err != nil {
		return err
	}

	b := append([]byte(nil), buf.Bytes()...)
	buf.Free()

	select {
	case c.ch <- b:
	default:
		// Buffer is full: we drop the log but keep the app running.
		log.Printf("pgcore: dropping log (buffer full)")
	}

	return nil
}

// Sync implements zapcore.Core. We don't need to flush anything here,
// since flushing is handled in the background goroutine with COPY.
func (c *core) Sync() error {
	return nil
}

// loop batches logs and flushes them into Postgres using COPY.
func (c *core) loop() {
	defer c.wg.Done()

	ticker := time.NewTicker(c.maxWait)
	defer ticker.Stop()

	batch := make([][]byte, 0, c.batchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}

		tx, err := c.db.Begin()
		if err != nil {
			log.Printf("pgcore: begin err: %v\n", err)
			batch = batch[:0]
			return
		}

		stmt, err := tx.Prepare(pq.CopyIn("logs", "req_id", "raw"))
		if err != nil {
			log.Printf("pgcore: prepare err: %v\n", err)
			_ = tx.Rollback()
			batch = batch[:0]
			return
		}

		for _, line := range batch {
			var tmp map[string]any
			if json.Unmarshal(line, &tmp) != nil {
				// Ignore non-JSON lines.
				continue
			}

			var reqID string
			for _, k := range c.reqKeys {
				if v, ok := tmp[k]; ok {
					if s, ok := v.(string); ok && s != "" {
						reqID = s
						break
					}
				}
			}

			if _, err := stmt.Exec(reqID, string(line)); err != nil {
				log.Printf("pgcore: exec err: %v\n", err)
			}
		}

		if _, err := stmt.Exec(); err != nil {
			log.Printf("pgcore: final exec err: %v\n", err)
		}
		if err := stmt.Close(); err != nil {
			log.Printf("pgcore: stmt close err: %v\n", err)
		}
		if err := tx.Commit(); err != nil {
			log.Printf("pgcore: commit err: %v\n", err)
		}

		batch = batch[:0]
	}

	for {
		select {
		case <-c.stop:
			flush()
			return
		case b := <-c.ch:
			batch = append(batch, b)
			if len(batch) >= c.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}
