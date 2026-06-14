package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/nas-backup/internal/models"
)

// LogRepository manages backup log entries in the backup_logs table.
type LogRepository struct {
	db *sql.DB
}

// NewLogRepository creates a new LogRepository with the given database connection.
func NewLogRepository(db *sql.DB) *LogRepository {
	return &LogRepository{db: db}
}

const logColumns = `id, backup_id, level, message, detail, created_at`

// parseLogTime parses a timestamp string from SQLite into a time.Time.
// It tries multiple common formats so that the log query never fails due to
// an unexpected datetime representation in the database.
func parseLogTime(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02T15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unable to parse time %q", s)
}

// scanLogRecord scans a single backup_logs row from a scanner into a LogRecord.
func scanLogRecord(s scanner) (*models.LogRecord, error) {
	var (
		rec       models.LogRecord
		backupID  sql.NullInt64
		createdAt string
	)
	if err := s.Scan(
		&rec.ID, &backupID, &rec.Level, &rec.Message, &rec.Detail, &createdAt,
	); err != nil {
		return nil, err
	}

	if backupID.Valid {
		rec.BackupID = &backupID.Int64
	}

	t, err := parseLogTime(createdAt)
	if err != nil {
		// If we cannot parse the time, fall back to current UTC time
		// instead of failing the entire query.
		t = time.Now().UTC()
	}
	rec.CreatedAt = t

	return &rec, nil
}

// Insert adds a new log entry. If backupID is nil, the log is not associated
// with any specific backup session.
func (r *LogRepository) Insert(backupID *int64, level models.LogLevel, message, detail string) error {
	now := Now()
	_, err := r.db.Exec(`
		INSERT INTO backup_logs (backup_id, level, message, detail, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, backupID, level, message, detail, now)
	if err != nil {
		return fmt.Errorf("insert log (level=%s): %w", level, err)
	}
	return nil
}

// buildLogWhere constructs a WHERE clause and argument list from a LogFilter.
// Returns the WHERE clause (including "WHERE"), the arguments, and any error.
func buildLogWhere(f *models.LogFilter) (string, []interface{}, error) {
	var conditions []string
	var args []interface{}

	if f.BackupID != nil {
		conditions = append(conditions, "backup_id = ?")
		args = append(args, *f.BackupID)
	}

	if f.Level != nil {
		conditions = append(conditions, "level = ?")
		args = append(args, *f.Level)
	}

	if f.Search != "" {
		conditions = append(conditions, "message LIKE ?")
		args = append(args, "%"+f.Search+"%")
	}

	if f.StartTime != nil {
		conditions = append(conditions, "created_at >= ?")
		args = append(args, f.StartTime.UTC().Format(time.RFC3339))
	}

	if f.EndTime != nil {
		conditions = append(conditions, "created_at <= ?")
		args = append(args, f.EndTime.UTC().Format(time.RFC3339))
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = " WHERE " + strings.Join(conditions, " AND ")
	}

	return whereClause, args, nil
}

// List retrieves log entries with filtering and pagination.
// Returns the matching items, the total count of matching records (ignoring pagination),
// and any error. Default page_size is 50 if not specified.
func (r *LogRepository) List(filter *models.LogFilter) ([]*models.LogRecord, int64, error) {
	if filter == nil {
		filter = &models.LogFilter{}
	}

	// Apply defaults.
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	page := filter.Page
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * pageSize

	whereClause, args, err := buildLogWhere(filter)
	if err != nil {
		return nil, 0, fmt.Errorf("build log filter: %w", err)
	}

	// Count total matching records.
	countSQL := "SELECT COUNT(*) FROM backup_logs" + whereClause
	var totalCount int64
	if err := r.db.QueryRow(countSQL, args...).Scan(&totalCount); err != nil {
		return nil, 0, fmt.Errorf("count logs: %w", err)
	}

	// Fetch the page of records.
	querySQL := "SELECT " + logColumns + " FROM backup_logs" + whereClause +
		" ORDER BY created_at DESC LIMIT ? OFFSET ?"
	queryArgs := append(args, pageSize, offset)

	rows, err := r.db.Query(querySQL, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list logs: %w", err)
	}
	defer rows.Close()

	var records []*models.LogRecord
	for rows.Next() {
		rec, err := scanLogRecord(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan log row: %w", err)
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate logs: %w", err)
	}

	return records, totalCount, nil
}

// GetByBackupID retrieves all log entries for a specific backup session,
// ordered by created_at ascending.
func (r *LogRepository) GetByBackupID(backupID int64) ([]*models.LogRecord, error) {
	rows, err := r.db.Query(`
		SELECT `+logColumns+` FROM backup_logs
		WHERE backup_id = ?
		ORDER BY created_at
	`, backupID)
	if err != nil {
		return nil, fmt.Errorf("get logs for backup %d: %w", backupID, err)
	}
	defer rows.Close()

	var records []*models.LogRecord
	for rows.Next() {
		rec, err := scanLogRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("scan log row for backup %d: %w", backupID, err)
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate logs for backup %d: %w", backupID, err)
	}
	return records, nil
}

// PurgeOlderThan deletes log entries that were created more than the specified
// number of days ago. Returns the count of deleted records.
func (r *LogRepository) PurgeOlderThan(days int) (int64, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
	result, err := r.db.Exec(`DELETE FROM backup_logs WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge logs older than %d days: %w", days, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected after purge logs: %w", err)
	}
	return affected, nil
}

// CountByLevel returns the number of log entries with the given level.
func (r *LogRepository) CountByLevel(level models.LogLevel) (int64, error) {
	var count int64
	err := r.db.QueryRow(`SELECT COUNT(*) FROM backup_logs WHERE level = ?`, level).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count logs by level %q: %w", level, err)
	}
	return count, nil
}
