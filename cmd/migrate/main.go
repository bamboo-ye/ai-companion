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

	mysql "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type migrationConfig struct {
	driver         string
	dsn            string
	directory      string
	requiredTables []string
}

func main() {
	cfg, err := loadMigrationConfig()
	if err != nil {
		log.Fatal(err)
	}
	db, err := openDatabase(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		log.Fatalf("connect %s: %v", cfg.driver, err)
	}
	if err = ensureMigrationTable(ctx, db, cfg.driver); err != nil {
		log.Fatal(err)
	}
	if err = applyMigrations(ctx, db, cfg); err != nil {
		log.Fatal(err)
	}
	if err = validateRequiredSchema(ctx, db, cfg.driver, cfg.requiredTables); err != nil {
		log.Fatal(err)
	}
}

func loadMigrationConfig() (migrationConfig, error) {
	driver := strings.ToLower(strings.TrimSpace(os.Getenv("DATABASE_DRIVER")))
	if driver == "" {
		driver = "mysql"
	}
	cfg := migrationConfig{driver: driver}
	switch driver {
	case "mysql":
		cfg.dsn = strings.TrimSpace(os.Getenv("MYSQL_DSN"))
		cfg.directory = envOrDefault("MIGRATIONS_DIR", "migrations")
		cfg.requiredTables = splitNonEmpty(envOrDefault("MIGRATION_REQUIRED_TABLES", "conversation_summaries"))
	case "postgres", "postgresql", "pgx":
		cfg.driver = "postgres"
		cfg.dsn = strings.TrimSpace(os.Getenv("POSTGRES_DSN"))
		cfg.directory = envOrDefault("MIGRATIONS_DIR", "migrations/postgres")
		cfg.requiredTables = splitNonEmpty(envOrDefault(
			"MIGRATION_REQUIRED_TABLES",
			"agent.runs,app.users,app.conversations,app.long_term_memories,app.ledger_entries,app.reminders,app.notification_deliveries,app.documents,app.skill_runs,app.generated_files,app.workspaces,app.email_deliveries,app.billing_plans,app.user_safety_policies,app.operator_accounts,eventing.outbox_events",
		))
	default:
		return migrationConfig{}, fmt.Errorf("DATABASE_DRIVER must be mysql or postgres")
	}
	if cfg.dsn == "" {
		return migrationConfig{}, fmt.Errorf("%s is required", map[string]string{"mysql": "MYSQL_DSN", "postgres": "POSTGRES_DSN"}[cfg.driver])
	}
	return cfg, nil
}

func openDatabase(cfg migrationConfig) (*sql.DB, error) {
	if cfg.driver == "postgres" {
		db, err := sql.Open("pgx", cfg.dsn)
		if err != nil {
			return nil, fmt.Errorf("open postgres: %w", err)
		}
		return db, nil
	}
	parsed, err := mysql.ParseDSN(cfg.dsn)
	if err != nil {
		return nil, fmt.Errorf("parse MYSQL_DSN: %w", err)
	}
	parsed.MultiStatements = true
	db, err := sql.Open("mysql", parsed.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	return db, nil
}

func ensureMigrationTable(ctx context.Context, db *sql.DB, driver string) error {
	statement := `CREATE TABLE IF NOT EXISTS schema_migrations (
		version VARCHAR(32) NOT NULL PRIMARY KEY,
		filename VARCHAR(255) NOT NULL,
		applied_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`
	if driver == "postgres" {
		statement = `CREATE TABLE IF NOT EXISTS schema_migrations (
			version VARCHAR(32) PRIMARY KEY,
			filename VARCHAR(255) NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`
	}
	if _, err := db.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	return nil
}

func applyMigrations(ctx context.Context, db *sql.DB, cfg migrationConfig) error {
	files, err := filepath.Glob(filepath.Join(cfg.directory, "*.up.sql"))
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	files = visibleMigrationFiles(files)
	sort.Strings(files)
	if len(files) == 0 {
		return fmt.Errorf("no migrations found in %s", cfg.directory)
	}
	for _, path := range files {
		base := filepath.Base(path)
		version, _, ok := strings.Cut(base, "_")
		if !ok {
			return fmt.Errorf("invalid migration filename %s", base)
		}
		var exists int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=`+placeholder(cfg.driver, 1), version).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", base, err)
		}
		if exists > 0 {
			fmt.Printf("skip %s\n", base)
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", base, err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin %s: %w", base, err)
		}
		if _, err = tx.ExecContext(ctx, string(content)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply %s: %w", base, err)
		}
		insert := `INSERT INTO schema_migrations (version,filename) VALUES (` + placeholder(cfg.driver, 1) + `,` + placeholder(cfg.driver, 2) + `)`
		if _, err = tx.ExecContext(ctx, insert, version, base); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record %s: %w", base, err)
		}
		if err = tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", base, err)
		}
		fmt.Printf("applied %s\n", base)
	}
	return nil
}

func visibleMigrationFiles(files []string) []string {
	visible := make([]string, 0, len(files))
	for _, path := range files {
		if strings.HasPrefix(filepath.Base(path), ".") {
			continue
		}
		visible = append(visible, path)
	}
	return visible
}

func validateRequiredSchema(ctx context.Context, db *sql.DB, driver string, requiredTables []string) error {
	for _, qualified := range requiredTables {
		schema, table := splitQualifiedTable(qualified)
		var exists int
		var err error
		if driver == "postgres" {
			err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=$1 AND table_name=$2`, schema, table).Scan(&exists)
		} else {
			err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?`, table).Scan(&exists)
		}
		if err != nil {
			return fmt.Errorf("validate required table %s: %w", qualified, err)
		}
		if exists != 1 {
			return fmt.Errorf("required table %s is missing after migrations", qualified)
		}
	}
	fmt.Printf("validated %d required tables\n", len(requiredTables))
	return nil
}

func placeholder(driver string, position int) string {
	if driver == "postgres" {
		return fmt.Sprintf("$%d", position)
	}
	return "?"
}

func splitQualifiedTable(value string) (string, string) {
	schema, table, ok := strings.Cut(value, ".")
	if !ok {
		return "public", value
	}
	return schema, table
}

func envOrDefault(key, fallback string) string {
	if current := strings.TrimSpace(os.Getenv(key)); current != "" {
		return current
	}
	return fallback
}

func splitNonEmpty(value string) []string {
	items := strings.Split(value, ",")
	result := make([]string, 0, len(items))
	for _, item := range items {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
