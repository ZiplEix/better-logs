package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"time"

	betterauth "github.com/ZiplEix/better-logs"
	"github.com/ZiplEix/better-logs/httpmw"
	_ "github.com/lib/pq"
	"go.uber.org/zap"
)

func main() {
	// DSN must match docker-compose.yml
	dsn := "postgres://postgres:postgres@localhost:5432/logsdb?sslmode=disable"

	// 1) Connect to PostgreSQL
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("failed to ping db: %v", err)
	}

	// 2) Ensure logs table exists
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := betterauth.EnsureLogsTable(ctx, db); err != nil {
		log.Fatalf("failed to ensure logs table: %v", err)
	}

	// 3) Initialize logger (stdout + Postgres)
	cfg := betterauth.DefaultConfig()
	cfg.ServiceName = "betterlogs-example"
	cfg.DB = db
	cfg.EnablePostgres = true

	logger, cleanup, err := betterauth.New(cfg)
	if err != nil {
		log.Fatalf("failed to init logger: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := cleanup(ctx); err != nil {
			log.Printf("cleanup error: %v", err)
		}
	}()

	zap.ReplaceGlobals(logger)

	// 4) Simple http mux
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		zap.L().Info("hello endpoint hit",
			zap.String("path", r.URL.Path),
		)
		fmt.Fprintln(w, "ok")
	})

	// 5) Wrap with middleware
	handler := httpmw.Middleware(mux)

	addr := ":8080"
	log.Printf("Starting example server on %s ...", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
