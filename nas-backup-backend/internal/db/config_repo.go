package db

import (
        "database/sql"
        "fmt"

        "github.com/nas-backup/internal/models"
)

// ConfigRepository manages key-value configuration, backup directories,
// and exclusion rules in the database.
type ConfigRepository struct {
        db *sql.DB
}

// NewConfigRepository creates a new ConfigRepository with the given database connection.
func NewConfigRepository(db *sql.DB) *ConfigRepository {
        return &ConfigRepository{db: db}
}

// boolToInt converts a Go bool to an SQLite-compatible integer (1 or 0).
func boolToInt(b bool) int {
        if b {
                return 1
        }
        return 0
}

// intToBool converts an SQLite integer (0 or 1) to a Go bool.
func intToBool(i int) bool {
        return i != 0
}

// ---------------------------------------------------------------------------
// config_kv table operations
// ---------------------------------------------------------------------------

// Get retrieves the value for a configuration key.
// Returns ("", nil) if the key does not exist.
func (r *ConfigRepository) Get(key string) (string, error) {
        var value string
        err := r.db.QueryRow(`SELECT value FROM config_kv WHERE key = ?`, key).Scan(&value)
        if err != nil {
                if err == sql.ErrNoRows {
                        return "", nil
                }
                return "", fmt.Errorf("get config %q: %w", key, err)
        }
        return value, nil
}

// Set upserts a key-value configuration pair. If the key already exists,
// the value and updated_at timestamp are updated.
func (r *ConfigRepository) Set(key, value string) error {
        now := Now()
        _, err := r.db.Exec(`
                INSERT INTO config_kv (key, value, updated_at) VALUES (?, ?, ?)
                ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
        `, key, value, now)
        if err != nil {
                return fmt.Errorf("set config %q: %w", key, err)
        }
        return nil
}

// GetAll retrieves all configuration entries as a map from key to value.
func (r *ConfigRepository) GetAll() (map[string]string, error) {
        rows, err := r.db.Query(`SELECT key, value FROM config_kv ORDER BY key`)
        if err != nil {
                return nil, fmt.Errorf("get all config: %w", err)
        }
        defer rows.Close()

        result := make(map[string]string)
        for rows.Next() {
                var k, v string
                if err := rows.Scan(&k, &v); err != nil {
                        return nil, fmt.Errorf("scan config row: %w", err)
                }
                result[k] = v
        }
        if err := rows.Err(); err != nil {
                return nil, fmt.Errorf("iterate config entries: %w", err)
        }
        return result, nil
}

// ---------------------------------------------------------------------------
// backup_directories table operations
// ---------------------------------------------------------------------------

// scanBackupDirectory scans a single backup_directories row from a scanner.
func scanBackupDirectory(s scanner) (*models.BackupDirectory, error) {
        var (
                dir         models.BackupDirectory
                recursive   int
                enabled     int
                description sql.NullString
        )
        if err := s.Scan(&dir.ID, &dir.Path, &recursive, &enabled, &description); err != nil {
                return nil, err
        }
        dir.Recursive = intToBool(recursive)
        dir.Enabled = intToBool(enabled)
        if description.Valid {
                dir.Description = description.String
        }
        return &dir, nil
}

// ListDirectories retrieves all backup directory entries.
func (r *ConfigRepository) ListDirectories() ([]*models.BackupDirectory, error) {
        rows, err := r.db.Query(`
                SELECT id, path, recursive, enabled, description
                FROM backup_directories ORDER BY path
        `)
        if err != nil {
                return nil, fmt.Errorf("list backup directories: %w", err)
        }
        defer rows.Close()

        var dirs []*models.BackupDirectory
        for rows.Next() {
                d, err := scanBackupDirectory(rows)
                if err != nil {
                        return nil, fmt.Errorf("scan backup directory row: %w", err)
                }
                dirs = append(dirs, d)
        }
        if err := rows.Err(); err != nil {
                return nil, fmt.Errorf("iterate backup directories: %w", err)
        }
        return dirs, nil
}

// AddDirectory inserts a new backup directory entry and returns its ID.
func (r *ConfigRepository) AddDirectory(path string, recursive, enabled bool, description string) (int64, error) {
        result, err := r.db.Exec(`
                INSERT INTO backup_directories (path, recursive, enabled, description)
                VALUES (?, ?, ?, ?)
        `, path, boolToInt(recursive), boolToInt(enabled), description)
        if err != nil {
                return 0, fmt.Errorf("add backup directory %q: %w", path, err)
        }

        id, err := result.LastInsertId()
        if err != nil {
                return 0, fmt.Errorf("last insert id after add directory %q: %w", path, err)
        }
        return id, nil
}

// UpdateDirectory updates an existing backup directory entry identified by ID.
func (r *ConfigRepository) UpdateDirectory(id int64, path string, recursive, enabled bool, description string) error {
        result, err := r.db.Exec(`
                UPDATE backup_directories
                SET path = ?, recursive = ?, enabled = ?, description = ?
                WHERE id = ?
        `, path, boolToInt(recursive), boolToInt(enabled), description, id)
        if err != nil {
                return fmt.Errorf("update backup directory %d: %w", id, err)
        }
        affected, err := result.RowsAffected()
        if err != nil {
                return fmt.Errorf("rows affected after update directory %d: %w", id, err)
        }
        if affected == 0 {
                return fmt.Errorf("backup directory not found for update: %d", id)
        }
        return nil
}

// DeleteDirectory removes a backup directory entry by ID.
func (r *ConfigRepository) DeleteDirectory(id int64) error {
        result, err := r.db.Exec(`DELETE FROM backup_directories WHERE id = ?`, id)
        if err != nil {
                return fmt.Errorf("delete backup directory %d: %w", id, err)
        }
        affected, err := result.RowsAffected()
        if err != nil {
                return fmt.Errorf("rows affected after delete directory %d: %w", id, err)
        }
        if affected == 0 {
                return fmt.Errorf("backup directory not found for delete: %d", id)
        }
        return nil
}

// GetEnabledDirectories retrieves only backup directories that are enabled.
func (r *ConfigRepository) GetEnabledDirectories() ([]*models.BackupDirectory, error) {
        rows, err := r.db.Query(`
                SELECT id, path, recursive, enabled, description
                FROM backup_directories WHERE enabled = 1
                ORDER BY path
        `)
        if err != nil {
                return nil, fmt.Errorf("get enabled backup directories: %w", err)
        }
        defer rows.Close()

        var dirs []*models.BackupDirectory
        for rows.Next() {
                d, err := scanBackupDirectory(rows)
                if err != nil {
                        return nil, fmt.Errorf("scan enabled directory row: %w", err)
                }
                dirs = append(dirs, d)
        }
        if err := rows.Err(); err != nil {
                return nil, fmt.Errorf("iterate enabled directories: %w", err)
        }
        return dirs, nil
}

// ---------------------------------------------------------------------------
// exclusion_rules table operations
// ---------------------------------------------------------------------------

// scanExclusionRule scans a single exclusion_rules row from a scanner.
func scanExclusionRule(s scanner) (*models.ExclusionRule, error) {
        var (
                rule    models.ExclusionRule
                enabled int
        )
        if err := s.Scan(&rule.ID, &rule.Pattern, &rule.RuleType, &enabled); err != nil {
                return nil, err
        }
        rule.Enabled = intToBool(enabled)
        return &rule, nil
}

// ListExclusionRules retrieves all exclusion rule entries.
func (r *ConfigRepository) ListExclusionRules() ([]*models.ExclusionRule, error) {
        rows, err := r.db.Query(`
                SELECT id, pattern, rule_type, enabled
                FROM exclusion_rules ORDER BY id
        `)
        if err != nil {
                return nil, fmt.Errorf("list exclusion rules: %w", err)
        }
        defer rows.Close()

        var rules []*models.ExclusionRule
        for rows.Next() {
                rule, err := scanExclusionRule(rows)
                if err != nil {
                        return nil, fmt.Errorf("scan exclusion rule row: %w", err)
                }
                rules = append(rules, rule)
        }
        if err := rows.Err(); err != nil {
                return nil, fmt.Errorf("iterate exclusion rules: %w", err)
        }
        return rules, nil
}

// AddExclusionRule inserts a new exclusion rule and returns its ID.
func (r *ConfigRepository) AddExclusionRule(pattern, ruleType string, enabled bool) (int64, error) {
        result, err := r.db.Exec(`
                INSERT INTO exclusion_rules (pattern, rule_type, enabled)
                VALUES (?, ?, ?)
        `, pattern, ruleType, boolToInt(enabled))
        if err != nil {
                return 0, fmt.Errorf("add exclusion rule %q: %w", pattern, err)
        }

        id, err := result.LastInsertId()
        if err != nil {
                return 0, fmt.Errorf("last insert id after add exclusion rule %q: %w", pattern, err)
        }
        return id, nil
}

// UpdateExclusionRule updates an existing exclusion rule identified by ID.
func (r *ConfigRepository) UpdateExclusionRule(id int64, pattern, ruleType string, enabled bool) error {
        result, err := r.db.Exec(`
                UPDATE exclusion_rules
                SET pattern = ?, rule_type = ?, enabled = ?
                WHERE id = ?
        `, pattern, ruleType, boolToInt(enabled), id)
        if err != nil {
                return fmt.Errorf("update exclusion rule %d: %w", id, err)
        }
        affected, err := result.RowsAffected()
        if err != nil {
                return fmt.Errorf("rows affected after update exclusion rule %d: %w", id, err)
        }
        if affected == 0 {
                return fmt.Errorf("exclusion rule not found for update: %d", id)
        }
        return nil
}

// DeleteExclusionRule removes an exclusion rule by ID.
func (r *ConfigRepository) DeleteExclusionRule(id int64) error {
        result, err := r.db.Exec(`DELETE FROM exclusion_rules WHERE id = ?`, id)
        if err != nil {
                return fmt.Errorf("delete exclusion rule %d: %w", id, err)
        }
        affected, err := result.RowsAffected()
        if err != nil {
                return fmt.Errorf("rows affected after delete exclusion rule %d: %w", id, err)
        }
        if affected == 0 {
                return fmt.Errorf("exclusion rule not found for delete: %d", id)
        }
        return nil
}

// GetEnabledExclusionRules retrieves only exclusion rules that are enabled.
func (r *ConfigRepository) GetEnabledExclusionRules() ([]*models.ExclusionRule, error) {
        rows, err := r.db.Query(`
                SELECT id, pattern, rule_type, enabled
                FROM exclusion_rules WHERE enabled = 1
                ORDER BY id
        `)
        if err != nil {
                return nil, fmt.Errorf("get enabled exclusion rules: %w", err)
        }
        defer rows.Close()

        var rules []*models.ExclusionRule
        for rows.Next() {
                rule, err := scanExclusionRule(rows)
                if err != nil {
                        return nil, fmt.Errorf("scan enabled exclusion rule row: %w", err)
                }
                rules = append(rules, rule)
        }
        if err := rows.Err(); err != nil {
                return nil, fmt.Errorf("iterate enabled exclusion rules: %w", err)
        }
        return rules, nil
}
