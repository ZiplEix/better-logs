# Better Logs

A lightweight, production‑ready logging toolkit designed for Go services that want **structured logs**, **database persistence**, and **framework‑agnostic HTTP middleware**, without relying on any external SaaS.

This README provides:

* Overview
* Why this package exists
* Install
* Basic usage
* HTTP middleware
* Postgres log core (batch writer)
* Context fields
* Integration examples

  * `net/http`
  * Echo
* Best practices (PII, redaction, retention)
* Technical choices

---

# 📌 Overview

Better Logs is a small, focused toolkit that provides:

* A **Zap core** that writes logs into PostgreSQL in **batches**
* A generic **HTTP middleware** compatible with any `net/http` router
* Context‑based structured logging fields (similar to `logctx`)
* Safe batching, backpressure and non‑blocking writes
* Plug‑and‑play usage: combine your stdout logger + pgcore

It is designed to be:

* **Framework‑agnostic** (Echo, Chi, net/http, Fiber* except Fiber doesn't use stdlib)
* **Fully self‑hosted**
* **Simple to audit** (no magic, no global state outside Zap)
* **Low overhead**

---

# ❓ Why This Package Exists

Most Go projects evolve into the same pattern:

* You start with stdout logs using Zap or Zerolog.
* Then you want logs searchable over time.
* But you do **not** want:

  * Loki + Grafana
  * Elasticsearch + Kibana
  * Cloud Logging / Datadog / Sentry (pricing, compliance, vendor lock‑in)

You just want:

* Keep stdout for local dev & simple operations
* Also write logs into a **PostgreSQL `logs` table**
* With retention
* And without blocking your handlers

Better Logs solves exactly that.
Small footprint. No external dependencies. Just **PostgreSQL + Zap**.

---

# 📦 Install

```bash
go get github.com/ZiplEix/better-logs
```

---

# 🚀 Basic Usage

## 1. Initialize your logger

```go
import (
    betterlogs "github.com/ZiplEix/better-logs"
    "go.uber.org/zap"
)

db := /* your *sql.DB */

cfg := betterlogs.DefaultConfig()
cfg.ServiceName = "my-service"
cfg.DB = db
cfg.EnablePostgres = true

logger, closeFn, err := betterlogs.New(cfg)
if err != nil {
    log.Fatalf("failed to init logger: %v", err)
}

zap.ReplaceGlobals(logger)
```

This returns:

* A `*zap.Logger`
* A cleanup function you call on shutdown
* An error if initialization failed

## 2. Use Zap everywhere

```go
zap.L().Info("server started", zap.String("addr", ":8080"))
```

## 3. Close cleanly on shutdown

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

if err := closeFn(ctx); err != nil {
    log.Printf("logger cleanup error: %v", err)
}
```

---

# 🧱 Creating the `logs` table (CLI)

Better Logs ships a set of CLI utilities to manage the `logs` table in your PostgreSQL database.

Available commands:

* **`logs-migrate`** → Creates the table and indexes (idempotent)
* **`logs-drop`** → Drops the table
* **`logs-retention`** → Purges logs older than a given duration

---

## 🧱 1. Create the table — `logs-migrate`

```bash
go run github.com/ZiplEix/better-logs/cmd/logs-migrate@latest \
  -database-url "postgres://user:pass@host:5432/dbname?sslmode=disable"
```

This will:

* Connect to your PostgreSQL database
* Create the `logs` table and indexes if they do not exist
* Do nothing if the table already exists (idempotent)

Under the hood it runs:

* [`EnsureLogsTable`](./schema.go#L23)

---

## 🗑️ 2. Drop the table — `logs-drop`

Useful for development or resetting logs.

```bash
go run github.com/ZiplEix/better-logs/cmd/logs-drop@latest \
  -database-url "postgres://user:pass@host:5432/dbname?sslmode=disable"
```

This will:

* Drop the `logs` table if it exists
* Fail silently if the table does not exist

Internally it runs:

```sql
DROP TABLE IF EXISTS logs;
```

---

## 🧹 3. Purge old logs — `logs-retention`

Deletes logs older than the provided duration.

Example: purge logs older than 7 days

```bash
go run github.com/ZiplEix/better-logs/cmd/logs-retention@latest \
  -database-url "$DATABASE_URL" \
  -older-than "168h"   # 7 days
```

Example: purge logs older than 30 days

```bash
go run github.com/ZiplEix/better-logs/cmd/logs-retention@latest \
  -database-url "$DATABASE_URL" \
  -older-than "720h"
```

Under the hood, the utility executes:

```sql
DELETE FROM logs WHERE ts < NOW() - ($1 || '' )::interval;
```

Where `$1` is the parsed value of `-older-than`.

---

# 🌐 HTTP Middleware
Better Logs includes a fully generic `net/http` middleware.

### Features
- Measures latency
- Captures status code
- Captures request body (optional)
- Captures remote IP (header or TCP)
- Injects context fields
- Logs every request as structured JSON

### Add it to any router
```go
handler := httpmw.Middleware(mux)
http.ListenAndServe(":8080", handler)
````

### With config

```go
cfg := httpmw.DefaultConfig()
cfg.LogRequestBody = true
cfg.RemoteIPHeader = "X-Real-IP"

handler := httpmw.WithConfig(cfg)(mux)
```

---

# 🗄️ PostgreSQL Log Core (pgcore)

The `pgcore` package implements a Zap core that writes logs to PostgreSQL.

### Highlights

* Batched inserts using `COPY` for maximum throughput
* Backpressure channel buffer (default 10k entries)
* Timeout-based flush + size-based flush
* Non-blocking `Write` (logs dropped only if buffer is full)
* Automatic JSON parsing to extract request ID

### Usage

```go
pgCfg := pgcore.Config{
    Level:      zap.InfoLevel,
    BatchSize:  1000,
    MaxWait:    2 * time.Second,
    BufferSize: 10_000,
}

pgCore, closePg, err := pgcore.New(db, pgCfg)
if err != nil {
    log.Fatalf("failed to init pg core: %v", err)
}

core := zapcore.NewTee(stdoutCore, pgCore)
logger := zap.New(core)
```

### PostgreSQL table schema

```sql
CREATE TABLE IF NOT EXISTS logs (
    id BIGSERIAL PRIMARY KEY,
    ts timestamptz NOT NULL DEFAULT now(),
    req_id text,
    raw jsonb NOT NULL
);
```

---

# 🧩 Context Fields

Sometimes you need to attach additional metadata to logs during a request.

Better Logs provides:

* `WithFields(ctx, map[string]any)`
* `AddField(ctx, key, value)`
* `FieldsFrom(ctx)`

The middleware automatically merges these into the final log entry.

### Example

```go
func UserHandler(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    ctx = httpmw.AddField(ctx, "user_id", "abc123")
    ctx = httpmw.AddField(ctx, "scope", "admin")
    r = r.WithContext(ctx)

    // handler logic...
}
```

---

# 🔒 Best Practices

## Sensitive Data (PII)

Never log:

* Passwords
* OAuth tokens
* Raw JWTs
* Bank information
* Personal identifiers unless necessary

Use a redaction helper if needed.

## Log Retention

Implement a cron or background goroutine that removes logs older than X days:

```sql
DELETE FROM logs WHERE ts < NOW() - INTERVAL '7 days';
```

## Batch Size

Keep batch sizes between **500–2000** for optimal throughput.

## Avoid Logging Large Bodies

If you enable `LogRequestBody`, always:

* Set a `MaxBodyBytes` limit
* Avoid logging file uploads or binary data

---

# ⚙️ Technical Choices

## Zap

Chosen because:

* Fastest widely used structured logger in Go
* Supported by Uber
* Has pluggable cores

## PostgreSQL

Chosen because:

* Reliable
* Easy to query
* JSONB support for structured logs

## Batching via COPY

This gives **10x+ better performance** than individual INSERTs.

## net/http Compatibility

The middleware is intentionally written for:

* `net/http`
* Echo
* Chi
* Httprouter
* Any stdlib-compatible router

Fiber is intentionally excluded because it does **not** use `net/http`.

---

# 🎉 Summary

Better Logs gives you:

* Structured logging
* Batching into PostgreSQL
* Nice HTTP middleware
* Context-based metadata
* Fully self-hosted architecture

Minimal, composable, production-friendly. Ready to drop into any Go project.
