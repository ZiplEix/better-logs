package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	betterlogs "github.com/ZiplEix/better-logs"

	_ "github.com/lib/pq"
)

func main() {
	var (
		dsn     string
		timeout time.Duration
	)

	flag.StringVar(&dsn, "database-url", "", "PostgreSQL connection URL (e.g. postgres://user:pass@host:5432/dbname?sslmode=disable)")
	flag.DurationVar(&timeout, "timeout", 10*time.Second, "operation timeout (e.g. 10s, 1m)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s -database-url <dsn> [-timeout 10s]\n\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "Creates the 'logs' table and indexes if they do not exist.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if dsn == "" {
		fmt.Fprintln(os.Stderr, "error: -database-url is required")
		flag.Usage()
		os.Exit(2)
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to ping database: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := betterlogs.EnsureLogsTable(ctx, db); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to ensure logs table: %v\n", err)
		os.Exit(3)
	}

	log.Println("Logs table created or already exists.")
}
