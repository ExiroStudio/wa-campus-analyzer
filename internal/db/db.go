package db

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"wa-campus-analyzer/migrations"

	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB
}

func Open(dbPath string) (*DB, error) {
	dir := filepath.Dir(dbPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create db directory %s: %w", dir, err)
		}
	}

	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)", dbPath)
	rawDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	rawDB.SetMaxOpenConns(1) // SQLite single writer recommended for WAL to avoid lock contention

	if err := rawDB.Ping(); err != nil {
		rawDB.Close()
		return nil, fmt.Errorf("failed to ping sqlite database: %w", err)
	}

	d := &DB{DB: rawDB}
	if err := d.RunMigrations(); err != nil {
		d.Close()
		return nil, fmt.Errorf("migration failed: %w", err)
	}

	return d, nil
}

func (d *DB) RunMigrations() error {
	_, err := d.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
	`)
	if err != nil {
		return fmt.Errorf("failed to initialize schema_migrations table: %w", err)
	}

	entries, err := fs.ReadDir(migrations.MigrationFS, ".")
	if err != nil {
		return fmt.Errorf("failed to read migrations: %w", err)
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)

	for _, filename := range files {
		var exists int
		err := d.QueryRow("SELECT COUNT(1) FROM schema_migrations WHERE version = ?", filename).Scan(&exists)
		if err != nil {
			return fmt.Errorf("failed to check migration status for %s: %w", filename, err)
		}
		if exists > 0 {
			continue
		}

		content, err := fs.ReadFile(migrations.MigrationFS, filename)
		if err != nil {
			return fmt.Errorf("failed to read migration file %s: %w", filename, err)
		}

		tx, err := d.Begin()
		if err != nil {
			return fmt.Errorf("failed to begin transaction for migration %s: %w", filename, err)
		}

		if _, err := tx.Exec(string(content)); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to execute migration %s: %w", filename, err)
		}

		if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", filename); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to record migration %s: %w", filename, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit migration %s: %w", filename, err)
		}
	}

	return nil
}
