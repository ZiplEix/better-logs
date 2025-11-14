package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/lib/pq"
)

// We rely on Postgres interval parsing by concatenating the string and casting:
// DELETE FROM logs WHERE ts < NOW() - ($1 || ”)::interval;
const retentionSQL = `
DELETE FROM logs
WHERE ts < NOW() - ($1 || '' )::interval;
`

func main() {
	var (
		dsn       string
		olderThan string
		timeout   time.Duration
		dryRun    bool
	)

	flag.StringVar(&dsn, "database-url", "", "PostgreSQL connection URL (e.g. postgres://user:pass@host:5432/dbname?sslmode=disable)")
	flag.StringVar(&olderThan, "older-than", "", "interval for log retention (e.g. '168h', '7 days', '1 month')")
	flag.DurationVar(&timeout, "timeout", 30*time.Second, "operation timeout (e.g. 30s, 2m)")
	flag.BoolVar(&dryRun, "dry-run", false, "if true, show how many rows would be deleted without actually deleting")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s -database-url <dsn> -older-than <interval> [-timeout 30s] [-dry-run]\n\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "Deletes logs older than the specified interval.\n\n")
		fmt.Fprintln(flag.CommandLine.Output(), "Examples:")
		fmt.Fprintln(flag.CommandLine.Output(), "  -older-than '168h'       # 7 days")
		fmt.Fprintln(flag.CommandLine.Output(), "  -older-than '7 days'")
		fmt.Fprintln(flag.CommandLine.Output(), "  -older-than '1 month'")
		fmt.Fprintln(flag.CommandLine.Output(), "")
		flag.PrintDefaults()
	}
	flag.Parse()

	if dsn == "" {
		fmt.Fprintln(os.Stderr, "error: -database-url is required")
		flag.Usage()
		os.Exit(2)
	}
	if olderThan == "" {
		fmt.Fprintln(os.Stderr, "error: -older-than is required")
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

	if dryRun {
		// Just count rows that would be deleted
		query := `
SELECT count(*) FROM logs
WHERE ts < NOW() - ($1 || '' )::interval;
`
		var n int64
		if err := db.QueryRowContext(ctx, query, olderThan).Scan(&n); err != nil {
			fmt.Fprintf(os.Stderr, "error: dry-run count query failed: %v\n", err)
			os.Exit(3)
		}
		log.Printf("Dry run: %d rows would be deleted (older than %q).\n", n, olderThan)
		return
	}

	res, err := db.ExecContext(ctx, retentionSQL, olderThan)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to apply retention: %v\n", err)
		os.Exit(3)
	}

	n, err := res.RowsAffected()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not read affected rows: %v\n", err)
		os.Exit(3)
	}

	log.Printf("Retention applied: %d rows deleted (older than %q).\n", n, olderThan)
}
