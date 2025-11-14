package betterauth_test

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	betterauth "github.com/ZiplEix/better-logs"
	"github.com/ZiplEix/better-logs/httpmw"
	_ "github.com/lib/pq"
	"go.uber.org/zap"
)

// Example_basic demonstrates a minimal setup of Better Logs with PostgreSQL
// and the HTTP middleware. This example is meant for documentation purposes
// and assumes that a PostgreSQL instance is reachable at the given DSN.
func Example_basic() {
	// 1) Connect to PostgreSQL
	// In a real app, read this from env: os.Getenv("DATABASE_URL")
	dsn := "postgres://postgres:postgres@localhost:5432/logsdb?sslmode=disable"
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		// handle error in real code
		return
	}
	defer db.Close()

	// 2) Ensure logs table exists
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := betterauth.EnsureLogsTable(ctx, db); err != nil {
		// handle error in real code
		return
	}

	// 3) Initialize the logger (stdout + Postgres core)
	logger, cleanup, err := betterauth.New(betterauth.Config{
		ServiceName:    "example-service",
		DB:             db,
		Level:          zap.InfoLevel,
		Stdout:         true,
		EnablePostgres: true,
	})
	if err != nil {
		// handle error in real code
		return
	}
	defer cleanup(ctx)

	zap.ReplaceGlobals(logger)

	// 4) Create a simple http.Handler and wrap it with the middleware
	handler := httpmw.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		zap.L().Info("hello from example handler")
		w.Write([]byte("ok"))
	}))

	// In a real application you would now start your server:
	// http.ListenAndServe(":8080", handler)

	// Output:
}
