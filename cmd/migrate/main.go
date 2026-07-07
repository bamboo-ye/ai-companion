package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	driver "github.com/go-sql-driver/mysql"
)

func main() {
	dsn := strings.TrimSpace(os.Getenv("MYSQL_DSN"))
	if dsn == "" {
		log.Fatal("MYSQL_DSN is required")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		log.Fatalf("parse MYSQL_DSN: %v", err)
	}
	cfg.MultiStatements = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		log.Fatalf("connect mysql: %v", err)
	}
	if _, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version VARCHAR(32) NOT NULL PRIMARY KEY, filename VARCHAR(255) NOT NULL, applied_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`); err != nil {
		log.Fatal(err)
	}
	files, err := filepath.Glob("migrations/*.up.sql")
	if err != nil {
		log.Fatal(err)
	}
	sort.Strings(files)
	for _, path := range files {
		base := filepath.Base(path)
		version, _, ok := strings.Cut(base, "_")
		if !ok {
			log.Fatalf("invalid migration filename %s", base)
		}
		var exists int
		err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=?`, version).Scan(&exists)
		if err != nil {
			log.Fatal(err)
		}
		if exists > 0 {
			fmt.Printf("skip %s\n", base)
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			log.Fatal(err)
		}
		if _, err = db.ExecContext(ctx, string(content)); err != nil {
			log.Fatalf("apply %s: %v", base, err)
		}
		if _, err = db.ExecContext(ctx, `INSERT INTO schema_migrations (version,filename) VALUES (?,?)`, version, base); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("applied %s\n", base)
	}
}
