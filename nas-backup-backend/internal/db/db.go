// Package db provides the SQLite database layer for the NAS backup system.
// It manages schema migrations and exposes repository interfaces for all
// domain objects: files, backups, hash indices, logs, and configuration.
package db

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Database wraps a sql.DB connection and provides access to all repositories.
type Database struct {
	db              *sql.DB
	path            string // filesystem path, kept so Reopen() can re-open after PullOSSDB
	FileRepo        *FileRepository
	BackupRepo      *BackupRepository
	HashRepo        *HashRepository
	LogRepo         *LogRepository
	ConfigRepo      *ConfigRepository
	RestoreJobRepo  *RestoreJobRepository

	// dbMu serializes Reopen (and Close) against each other. Reopen swaps the
	// underlying *sql.DB (close old file, open new one); without this lock two
	// concurrent refreshes can each call Close() on the connection the other
	// just opened, leaving the shared handle in a closed state and surfacing as
	// "sql: database is closed" in unrelated in-flight operations (e.g. a
	// concurrently-starting backup's scan).
	dbMu sync.Mutex
}

// Conn exposes the underlying *sql.DB handle to callers that need it (for
// example, DBBackupService runs low-level VACUUM / backup-copy statements and
// needs direct access to the connection instead of going through the typed
// repository layer). Callers MUST NOT close the returned handle.
func (d *Database) Conn() *sql.DB {
	return d.db
}

// EnsureSchema runs any pending migrations against the current connection.
// It is safe to call multiple times: already-applied migrations are skipped.
// This is used before operations that depend on a specific table existing
// (e.g. exporting restore_jobs before an OSS DB swap), so a stale or
// older-version local DB is brought up to date instead of failing with a
// "no such table" error.
func (d *Database) EnsureSchema() error {
	if d.db == nil {
		return fmt.Errorf("database connection is not open")
	}
	if err := runMigrations(d.db); err != nil {
		return fmt.Errorf("ensure schema: %w", err)
	}
	return nil
}

// Open creates or opens the SQLite database at the given path and runs
// any pending migrations.
func Open(dbPath string) (*Database, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("database path must not be empty")
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	db, err := openConnection(dbPath)
	if err != nil {
		return nil, err
	}

	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	d := &Database{db: db, path: dbPath}
	d.initRepos(db)

	return d, nil
}

// initRepos (re)creates all repository wrappers around the given connection.
// Called by both Open and Reopen so the repos always point at the live *sql.DB.
func (d *Database) initRepos(db *sql.DB) {
	d.FileRepo = NewFileRepository(db)
	d.BackupRepo = NewBackupRepository(db)
	d.HashRepo = NewHashRepository(db)
	d.LogRepo = NewLogRepository(db)
	d.ConfigRepo = NewConfigRepository(db)
	d.RestoreJobRepo = NewRestoreJobRepository(db)
}

// Reopen closes the current database connection and opens a fresh one at the
// same path, re-running migrations and re-binding all repositories.
//
// This is used after PullOSSDB overwrites the local DB file: the old *sql.DB
// still references the pre-overwrite inode, so it must be closed and a new
// connection opened against the new file.
//
// There is a brief window during which concurrent readers using the old
// repository pointers may see "database closed" errors. Callers should handle
// such errors gracefully (the HTTP layer already does). In practice the window
// is sub-second and backups run at off-peak hours.
func (d *Database) Reopen() error {
	if d.db != nil {
		_ = d.db.Close()
	}
	db, err := openConnection(d.path)
	if err != nil {
		return fmt.Errorf("reopen database %s: %w", d.path, err)
	}
	if err := runMigrations(db); err != nil {
		db.Close()
		return fmt.Errorf("run migrations after reopen: %w", err)
	}
	d.db = db
	d.initRepos(db)
	return nil
}

// openConnection opens a SQLite connection with the standard pragmas used by
// this project. Shared by Open and Reopen.
func openConnection(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=1")
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", dbPath, err)
	}
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=-64000",
		"PRAGMA temp_store=MEMORY",
		"PRAGMA mmap_size=268435456",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("set pragma %q: %w", p, err)
		}
	}
	return db, nil
}

// Close closes the underlying database connection.
func (d *Database) Close() error {
	if d.db != nil {
		return d.db.Close()
	}
	return nil
}

// DB returns the raw *sql.DB for use in transactions.
func (d *Database) DB() *sql.DB {
	return d.db
}

// runMigrations reads and executes all embedded SQL migration files in order.
func runMigrations(db *sql.DB) error {
	// Create migrations tracking table.
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT (datetime('now'))
		)
	`)
	if err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	// Get the current migration version.
	var currentVersion int
	row := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations")
	if err := row.Scan(&currentVersion); err != nil {
		return fmt.Errorf("query current version: %w", err)
	}

	// Read embedded migration files.
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		var version int
		if _, err := fmt.Sscanf(entry.Name(), "%d_", &version); err != nil {
			continue
		}

		if version <= currentVersion {
			continue
		}

		content, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin transaction for migration %d: %w", version, err)
		}

		if _, err := tx.Exec(string(content)); err != nil {
			tx.Rollback()
			return fmt.Errorf("execute migration %s: %w", entry.Name(), err)
		}

		if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", version); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}

	return nil
}

// Now returns the current UTC time formatted for SQLite.
func Now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// NullTime handles nullable time columns.
type NullTime struct {
	Time  time.Time
	Valid bool
}

// Scan implements sql.Scanner for NullTime.
func (nt *NullTime) Scan(value interface{}) error {
	if value == nil {
		nt.Valid = false
		return nil
	}
	switch v := value.(type) {
	case string:
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return fmt.Errorf("parse time %q: %w", v, err)
		}
		nt.Time = t
		nt.Valid = true
	case time.Time:
		nt.Time = v
		nt.Valid = true
	default:
		return fmt.Errorf("unsupported time type: %T", value)
	}
	return nil
}
