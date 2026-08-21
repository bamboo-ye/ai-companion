package main

import "testing"

func TestLoadMigrationConfigPostgres(t *testing.T) {
	t.Setenv("DATABASE_DRIVER", "postgres")
	t.Setenv("POSTGRES_DSN", "postgres://user:password@localhost/database")
	t.Setenv("MIGRATIONS_DIR", "")
	t.Setenv("MIGRATION_REQUIRED_TABLES", "")

	cfg, err := loadMigrationConfig()
	if err != nil {
		t.Fatalf("loadMigrationConfig() error = %v", err)
	}
	if cfg.driver != "postgres" || cfg.directory != "migrations/postgres" || cfg.dsn == "" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if len(cfg.requiredTables) != 16 || cfg.requiredTables[0] != "agent.runs" || cfg.requiredTables[15] != "eventing.outbox_events" {
		t.Fatalf("required tables = %#v", cfg.requiredTables)
	}
}

func TestLoadMigrationConfigRejectsUnknownDriver(t *testing.T) {
	t.Setenv("DATABASE_DRIVER", "sqlite")
	if _, err := loadMigrationConfig(); err == nil {
		t.Fatal("loadMigrationConfig() expected an error")
	}
}

func TestPostgresPlaceholdersAndQualifiedTables(t *testing.T) {
	if got := placeholder("postgres", 3); got != "$3" {
		t.Fatalf("placeholder() = %q", got)
	}
	schema, table := splitQualifiedTable("agent.runs")
	if schema != "agent" || table != "runs" {
		t.Fatalf("splitQualifiedTable() = %q, %q", schema, table)
	}
}

func TestVisibleMigrationFilesIgnoresMetadataFiles(t *testing.T) {
	files := []string{
		"migrations/postgres/._000001_foundation.up.sql",
		"migrations/postgres/000001_foundation.up.sql",
		"migrations/postgres/.temporary.up.sql",
	}
	got := visibleMigrationFiles(files)
	if len(got) != 1 || got[0] != "migrations/postgres/000001_foundation.up.sql" {
		t.Fatalf("visibleMigrationFiles() = %#v", got)
	}
}
