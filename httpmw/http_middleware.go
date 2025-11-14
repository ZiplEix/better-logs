package httpmw

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ctxKey is the private key type used to store extra log fields in context.
type ctxKey struct{}

// fieldsHolder holds mutable log fields shared across middleware + handlers.
type fieldsHolder struct {
	mu sync.Mutex
	m  map[string]any
}

// getHolder returns the fieldsHolder stored in the context, if any.
func getHolder(ctx context.Context) *fieldsHolder {
	if v := ctx.Value(ctxKey{}); v != nil {
		if h, ok := v.(*fieldsHolder); ok {
			return h
		}
	}
	return nil
}

// ensureHolder returns an existing fieldsHolder from ctx or creates a new one
// and returns the (possibly) updated context.
func ensureHolder(ctx context.Context) (*fieldsHolder, context.Context) {
	if h := getHolder(ctx); h != nil {
		return h, ctx
	}
	h := &fieldsHolder{m: make(map[string]any)}
	ctx = context.WithValue(ctx, ctxKey{}, h)
	return h, ctx
}

// FieldsFrom returns a COPY of all custom log fields stored in the context, if any.
func FieldsFrom(ctx context.Context) map[string]any {
	h := getHolder(ctx)
	if h == nil {
		return map[string]any{}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	out := make(map[string]any, len(h.m))
	for k, v := range h.m {
		out[k] = v
	}
	return out
}

// WithFields merges the provided key/value pairs into the context's logging fields.
// It returns the (possibly) updated context. The underlying holder is shared, so
// even if the returned context is ignored, the fields are still added.
func WithFields(ctx context.Context, kv map[string]any) context.Context {
	h, ctx2 := ensureHolder(ctx)
	if len(kv) == 0 {
		return ctx2
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for k, v := range kv {
		h.m[k] = v
	}
	return ctx2
}

// AddField adds a single field into the context's logging fields.
func AddField(ctx context.Context, key string, value any) context.Context {
	return WithFields(ctx, map[string]any{key: value})
}

// Config controls how the HTTP middleware behaves.
type Config struct {
	// LogRequestBody controls whether request bodies are logged.
	// Be careful with large bodies and sensitive data (clear credentials...).
	LogRequestBody bool

	// MaxBodyBytes is the maximum number of bytes to read from the request body
	// when LogRequestBody is true. If zero or negative, a default is used.
	MaxBodyBytes int64

	// RemoteIPHeader, if non-empty, is the name of an HTTP header to trust for
	// the client IP (e.g. "X-Real-IP" or "X-Forwarded-For").
	// If empty, r.RemoteAddr is used.
	RemoteIPHeader string
}

// DefaultConfig returns a sane default configuration.
func DefaultConfig() Config {
	return Config{
		LogRequestBody: false,
		MaxBodyBytes:   64 * 1024, // 64KB
		RemoteIPHeader: "",
	}
}

// Middleware is a convenience wrapper using DefaultConfig.
func Middleware(next http.Handler) http.Handler {
	return WithConfig(DefaultConfig())(next)
}

// WithConfig returns a net/http middleware using the provided configuration.
func WithConfig(cfg Config) func(http.Handler) http.Handler {
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 64 * 1024
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Ensure a shared fields holder exists in the context BEFORE the handler runs,
			// so handlers can call AddField/WithFields and enrich the final http_request log.
			ctxWithHolder := WithFields(r.Context(), nil)
			r = r.WithContext(ctxWithHolder)

			// Optionally read and buffer the request body
			var bodyStr string
			if cfg.LogRequestBody && r.Body != nil {
				// Limit the amount we read to avoid huge allocations
				limited := io.LimitReader(r.Body, cfg.MaxBodyBytes)
				bodyBytes, _ := io.ReadAll(limited)
				bodyStr = string(bodyBytes)

				// Restore body for downstream handlers
				r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(bodyBytes), r.Body))
			}

			// Wrap ResponseWriter to capture status and bytes written
			rw := &responseWriter{
				ResponseWriter: w,
				status:         http.StatusOK,
			}

			next.ServeHTTP(rw, r)

			lat := time.Since(start)
			status := rw.status

			// Collect base fields
			fields := []zap.Field{
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", status),
				zap.Int64("latency_ms", lat.Milliseconds()),
				zap.String("remote_ip", remoteIP(r, cfg.RemoteIPHeader)),
				zap.String("user_agent", r.UserAgent()),
			}

			if cfg.LogRequestBody && bodyStr != "" {
				fields = append(fields, zap.String("body", bodyStr))
			}

			// Add query parameters as a string (optional, but often useful)
			if rawQuery := r.URL.RawQuery; rawQuery != "" {
				fields = append(fields, zap.String("query", rawQuery))
			}

			// Extract custom fields from context (if any)
			if ctxFields := FieldsFrom(r.Context()); len(ctxFields) > 0 {
				for k, v := range ctxFields {
					fields = append(fields, zap.Any(k, v))
				}
			}

			// Try to infer a request/correlation ID from headers, if present
			if reqID := headerRequestID(r); reqID != "" {
				fields = append(fields, zap.String("request_id", reqID))
			}

			// Log with appropriate level
			switch {
			case status >= 500:
				zap.L().Error("http_request", fields...)
			case status >= 400:
				zap.L().Warn("http_request", fields...)
			default:
				zap.L().Info("http_request", fields...)
			}
		})
	}
}

// responseWriter is a wrapper around http.ResponseWriter that captures status code
// and bytes written.
type responseWriter struct {
	http.ResponseWriter
	status int
	size   int64
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.size += int64(n)
	return n, err
}

// remoteIP returns the IP address of the client, optionally using a trusted header.
func remoteIP(r *http.Request, header string) string {
	if header != "" {
		if v := r.Header.Get(header); v != "" {
			// X-Forwarded-For may contain a list: use the first value
			if header == "X-Forwarded-For" {
				parts := strings.Split(v, ",")
				if len(parts) > 0 {
					return strings.TrimSpace(parts[0])
				}
			}
			return v
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// headerRequestID tries to extract a request/correlation ID from common headers.
func headerRequestID(r *http.Request) string {
	// common candidates
	keys := []string{
		"X-Request-ID",
		"X-Correlation-ID",
		"X-Request-Id",
		"X-Requestid",
	}
	for _, k := range keys {
		if v := r.Header.Get(k); v != "" {
			return v
		}
	}
	return ""
}
