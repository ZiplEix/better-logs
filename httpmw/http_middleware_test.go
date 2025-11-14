package httpmw

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type logEntry struct {
	Entry  zapcore.Entry
	Fields []zap.Field
}

type captureCore struct {
	mu      sync.Mutex
	entries []logEntry
	level   zapcore.LevelEnabler
}

func newCaptureCore(level zapcore.LevelEnabler) *captureCore {
	return &captureCore{
		entries: make([]logEntry, 0),
		level:   level,
	}
}

func (c *captureCore) Enabled(lvl zapcore.Level) bool {
	return c.level.Enabled(lvl)
}

func (c *captureCore) With(fields []zapcore.Field) zapcore.Core {
	return c
}

func (c *captureCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}
	return ce
}

func (c *captureCore) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, logEntry{
		Entry:  ent,
		Fields: fields,
	})
	return nil
}

func (c *captureCore) Sync() error { return nil }

func (c *captureCore) Entries() []logEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]logEntry, len(c.entries))
	copy(out, c.entries)
	return out
}

func fieldsToMap(fields []zap.Field) map[string]any {
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range fields {
		f.AddTo(enc)
	}
	return enc.Fields
}

func TestFieldsHelpers(t *testing.T) {
	ctx := context.Background()

	f0 := FieldsFrom(ctx)
	if len(f0) != 0 {
		t.Fatalf("expected empty fields from a fresh context, got %v", f0)
	}

	ctx1 := WithFields(ctx, map[string]any{"a": 1})
	f1 := FieldsFrom(ctx1)
	if f1["a"] != 1 {
		t.Fatalf("expected field a=1, got %v", f1["a"])
	}

	ctx2 := AddField(ctx1, "b", "x")
	f2 := FieldsFrom(ctx2)
	if f2["a"] != 1 || f2["b"] != "x" {
		t.Fatalf("expected fields a=1, b=x, got %v", f2)
	}

	src := map[string]any{"k": "v"}
	ctx3 := WithFields(ctx, src)
	src["k"] = "changed"
	f3 := FieldsFrom(ctx3)
	if f3["k"] != "v" {
		t.Fatalf("expected cloned map, got %v", f3["k"])
	}
}

func TestMiddleware_LogsInfoOn200_WithContextFieldsAndRequestID(t *testing.T) {
	core := newCaptureCore(zapcore.InfoLevel)
	logger := zap.New(core)
	zap.ReplaceGlobals(logger)
	defer zap.ReplaceGlobals(zap.NewNop())

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := AddField(r.Context(), "user_id", "user-123")
		r = r.WithContext(ctx)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	handler := Middleware(next)

	req := httptest.NewRequest(http.MethodGet, "/test/path?foo=bar", nil)
	req.Header.Set("User-Agent", "test-agent")
	req.Header.Set("X-Request-ID", "req-xyz")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	entries := core.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.Entry.Level != zapcore.InfoLevel {
		t.Fatalf("expected Info level, got %v", entry.Entry.Level)
	}
	if entry.Entry.Message != "http_request" {
		t.Fatalf("expected message 'http_request', got %q", entry.Entry.Message)
	}

	fm := fieldsToMap(entry.Fields)

	if fm["method"] != "GET" {
		t.Errorf("expected method=GET, got %v", fm["method"])
	}
	if fm["path"] != "/test/path" {
		t.Errorf("expected path=/test/path, got %v", fm["path"])
	}
	if fm["status"] != int64(200) && fm["status"] != 200 {
		t.Errorf("expected status=200, got %v", fm["status"])
	}
	if fm["user_agent"] != "test-agent" {
		t.Errorf("expected user_agent=test-agent, got %v", fm["user_agent"])
	}
	if fm["request_id"] != "req-xyz" {
		t.Errorf("expected request_id=req-xyz, got %v", fm["request_id"])
	}
	if fm["user_id"] != "user-123" {
		t.Errorf("expected user_id=user-123 from context, got %v", fm["user_id"])
	}
	if fm["query"] != "foo=bar" {
		t.Errorf("expected query=foo=bar, got %v", fm["query"])
	}
	if fm["remote_ip"] != "127.0.0.1" {
		t.Errorf("expected remote_ip=127.0.0.1, got %v", fm["remote_ip"])
	}
}

func TestMiddleware_LogsWarnOn4xx_ErrorOn5xx(t *testing.T) {
	core := newCaptureCore(zapcore.InfoLevel)
	logger := zap.New(core)
	zap.ReplaceGlobals(logger)
	defer zap.ReplaceGlobals(zap.NewNop())

	handler404 := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))

	req := httptest.NewRequest(http.MethodGet, "/not-found", nil)
	rr := httptest.NewRecorder()
	handler404.ServeHTTP(rr, req)

	handler500 := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal", http.StatusInternalServerError)
	}))
	req2 := httptest.NewRequest(http.MethodGet, "/error", nil)
	rr2 := httptest.NewRecorder()
	handler500.ServeHTTP(rr2, req2)

	entries := core.Entries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 log entries (one for 404, one for 500), got %d", len(entries))
	}

	var hasWarn404, hasError500 bool
	for _, e := range entries {
		fm := fieldsToMap(e.Fields)
		st, _ := fm["status"].(int64)
		switch st {
		case 404:
			if e.Entry.Level == zapcore.WarnLevel {
				hasWarn404 = true
			}
		case 500:
			if e.Entry.Level == zapcore.ErrorLevel {
				hasError500 = true
			}
		}
	}

	if !hasWarn404 {
		t.Errorf("expected a Warn-level log for status 404")
	}
	if !hasError500 {
		t.Errorf("expected an Error-level log for status 500")
	}
}

func TestMiddleware_LogsBodyWhenEnabled(t *testing.T) {
	core := newCaptureCore(zapcore.InfoLevel)
	logger := zap.New(core)
	zap.ReplaceGlobals(logger)
	defer zap.ReplaceGlobals(zap.NewNop())

	cfg := DefaultConfig()
	cfg.LogRequestBody = true
	cfg.MaxBodyBytes = 1024

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	})

	handler := WithConfig(cfg)(next)

	body := []byte("hello body")
	req := httptest.NewRequest(http.MethodPost, "/with-body", bytes.NewReader(body))
	req.RemoteAddr = "10.0.0.1:12345"
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	entries := core.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(entries))
	}

	fm := fieldsToMap(entries[0].Fields)
	if fm["body"] != "hello body" {
		t.Errorf("expected body=\"hello body\", got %v", fm["body"])
	}
}
