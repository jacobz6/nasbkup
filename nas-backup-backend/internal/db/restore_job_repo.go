package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nas-backup/internal/models"
)

// RestoreJobRepository handles CRUD operations for the restore_jobs table.
type RestoreJobRepository struct {
	db *sql.DB
}

// NewRestoreJobRepository creates a new RestoreJobRepository.
func NewRestoreJobRepository(db *sql.DB) *RestoreJobRepository {
	return &RestoreJobRepository{db: db}
}

// scanRestoreJob scans a single restore_jobs row into a RestoreJobRecord.
func scanRestoreJob(s scanner) (*models.RestoreJobRecord, error) {
	var rec models.RestoreJobRecord
	var (
		pathsJSON      sql.NullString
		pattern        sql.NullString
		backupID       sql.NullInt64
		failedJSON     sql.NullString
		errorMsg       sql.NullString
		startedAt      sql.NullString
		completedAt    sql.NullString
		createdAt      string
	)

	if err := s.Scan(
		&rec.ID, &rec.Status, &pathsJSON, &pattern, &backupID,
		&rec.OutputDir, &rec.Expedited, &rec.ConflictStrategy,
		&rec.TotalFiles, &rec.RestoredFiles, &failedJSON,
		&rec.TotalSize, &rec.RestoredSize, &rec.ElapsedMs,
		&errorMsg, &createdAt, &startedAt, &completedAt,
	); err != nil {
		return nil, err
	}

	if pathsJSON.Valid && pathsJSON.String != "" {
		if err := json.Unmarshal([]byte(pathsJSON.String), &rec.Paths); err != nil {
			return nil, fmt.Errorf("unmarshal paths JSON: %w", err)
		}
	}
	if pattern.Valid {
		rec.Pattern = pattern.String
	}
	if backupID.Valid {
		rec.BackupID = &backupID.Int64
	}
	if failedJSON.Valid && failedJSON.String != "" {
		if err := json.Unmarshal([]byte(failedJSON.String), &rec.FailedFiles); err != nil {
			return nil, fmt.Errorf("unmarshal failed_files JSON: %w", err)
		}
	}
	if errorMsg.Valid {
		rec.ErrorMessage = errorMsg.String
	}

	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at %q: %w", createdAt, err)
	}
	rec.CreatedAt = t

	if startedAt.Valid {
		t, err := time.Parse(time.RFC3339, startedAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse started_at %q: %w", startedAt.String, err)
		}
		rec.StartedAt = &t
	}
	if completedAt.Valid {
		t, err := time.Parse(time.RFC3339, completedAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse completed_at %q: %w", completedAt.String, err)
		}
		rec.CompletedAt = &t
	}

	return &rec, nil
}

// Create inserts a new restore job record and returns its ID.
func (r *RestoreJobRepository) Create(rec *models.RestoreJobRecord) (int64, error) {
	now := Now()
	pathsJSON, err := json.Marshal(rec.Paths)
	if err != nil {
		return 0, fmt.Errorf("marshal paths: %w", err)
	}

	result, err := r.db.Exec(`
		INSERT INTO restore_jobs (status, paths, pattern, backup_id, output_dir, expedited, conflict_strategy, total_files, total_size, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, rec.Status, string(pathsJSON), rec.Pattern, rec.BackupID, rec.OutputDir, rec.Expedited, rec.ConflictStrategy, rec.TotalFiles, rec.TotalSize, now)
	if err != nil {
		return 0, fmt.Errorf("create restore job: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id after create restore job: %w", err)
	}
	return id, nil
}

// GetByID retrieves a restore job by its primary key ID.
func (r *RestoreJobRepository) GetByID(id int64) (*models.RestoreJobRecord, error) {
	row := r.db.QueryRow(`
		SELECT id, status, paths, pattern, backup_id, output_dir, expedited, conflict_strategy,
		       total_files, restored_files, failed_files, total_size, restored_size, elapsed_ms,
		       error_message, created_at, started_at, completed_at
		FROM restore_jobs WHERE id = ?
	`, id)
	rec, err := scanRestoreJob(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get restore job %d: %w", id, err)
	}
	return rec, nil
}

// UpdateStatus updates the status (and optionally error message) of a restore job.
// When transitioning to 'running', started_at is set; when transitioning to a
// terminal status, completed_at is set.
func (r *RestoreJobRepository) UpdateStatus(id int64, status models.RestoreJobStatus, errorMsg string) error {
	now := Now()

	switch status {
	case models.RestoreJobStatusRunning:
		_, err := r.db.Exec(`
			UPDATE restore_jobs SET status = ?, started_at = ?, error_message = ? WHERE id = ?
		`, status, now, errorMsg, id)
		if err != nil {
			return fmt.Errorf("update restore job %d status to %q: %w", id, status, err)
		}

	case models.RestoreJobStatusCompleted, models.RestoreJobStatusFailed, models.RestoreJobStatusCancelled:
		_, err := r.db.Exec(`
			UPDATE restore_jobs SET status = ?, completed_at = ?, error_message = ? WHERE id = ?
		`, status, now, errorMsg, id)
		if err != nil {
			return fmt.Errorf("update restore job %d status to %q: %w", id, status, err)
		}

	default:
		_, err := r.db.Exec(`
			UPDATE restore_jobs SET status = ?, error_message = ? WHERE id = ?
		`, status, errorMsg, id)
		if err != nil {
			return fmt.Errorf("update restore job %d status to %q: %w", id, status, err)
		}
	}

	return nil
}

// UpdateProgress updates the in-flight progress counters for a restore job.
func (r *RestoreJobRepository) UpdateProgress(id int64, restoredFiles int, restoredSize int64, failedFiles []string) error {
	failedJSON, err := json.Marshal(failedFiles)
	if err != nil {
		return fmt.Errorf("marshal failed_files: %w", err)
	}

	_, err = r.db.Exec(`
		UPDATE restore_jobs SET restored_files = ?, restored_size = ?, failed_files = ? WHERE id = ?
	`, restoredFiles, restoredSize, string(failedJSON), id)
	if err != nil {
		return fmt.Errorf("update progress for restore job %d: %w", id, err)
	}
	return nil
}

// UpdateCompleted marks a restore job as completed or failed with final counters.
func (r *RestoreJobRepository) UpdateCompleted(id int64, restoredFiles int, restoredSize int64, elapsedMs int64, failedFiles []string) error {
	failedJSON, err := json.Marshal(failedFiles)
	if err != nil {
		return fmt.Errorf("marshal failed_files: %w", err)
	}
	now := Now()

	_, err = r.db.Exec(`
		UPDATE restore_jobs
		SET restored_files = ?, restored_size = ?, elapsed_ms = ?, failed_files = ?, completed_at = ?
		WHERE id = ?
	`, restoredFiles, restoredSize, elapsedMs, string(failedJSON), now, id)
	if err != nil {
		return fmt.Errorf("update completed for restore job %d: %w", id, err)
	}
	return nil
}

// List returns restore jobs ordered by created_at DESC with pagination.
// If status is non-empty, results are filtered to that status.
func (r *RestoreJobRepository) List(limit, offset int, status string) ([]*models.RestoreJobRecord, int64, error) {
	var total int64
	countQuery := "SELECT COUNT(*) FROM restore_jobs"
	listQuery := `
		SELECT id, status, paths, pattern, backup_id, output_dir, expedited, conflict_strategy,
		       total_files, restored_files, failed_files, total_size, restored_size, elapsed_ms,
		       error_message, created_at, started_at, completed_at
		FROM restore_jobs`
	var args []interface{}

	if status != "" {
		where := " WHERE status = ?"
		countQuery += where
		listQuery += where
		args = append(args, status)
	}

	if err := r.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count restore jobs: %w", err)
	}

	listQuery += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	listArgs := append(args, limit, offset)

	rows, err := r.db.Query(listQuery, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list restore jobs: %w", err)
	}
	defer rows.Close()

	records := make([]*models.RestoreJobRecord, 0)
	for rows.Next() {
		rec, err := scanRestoreJob(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan restore job row: %w", err)
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate restore jobs: %w", err)
	}
	return records, total, nil
}

// GetRunning returns the currently running restore job, or nil if none.
func (r *RestoreJobRepository) GetRunning() (*models.RestoreJobRecord, error) {
	row := r.db.QueryRow(`
		SELECT id, status, paths, pattern, backup_id, output_dir, expedited, conflict_strategy,
		       total_files, restored_files, failed_files, total_size, restored_size, elapsed_ms,
		       error_message, created_at, started_at, completed_at
		FROM restore_jobs WHERE status = 'running' LIMIT 1
	`)
	rec, err := scanRestoreJob(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get running restore job: %w", err)
	}
	return rec, nil
}

// IsRunning checks whether any restore job is currently running.
func (r *RestoreJobRepository) IsRunning() (bool, error) {
	var count int64
	err := r.db.QueryRow(`SELECT COUNT(*) FROM restore_jobs WHERE status = 'running'`).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check running restore job: %w", err)
	}
	return count > 0, nil
}

// CleanupStaleRunning marks any "running" or "pending" restore jobs as failed.
// Called at startup to recover from a previous process crash.
func (r *RestoreJobRepository) CleanupStaleRunning() (int64, error) {
	now := Now()
	res, err := r.db.Exec(`
		UPDATE restore_jobs SET status = 'failed', completed_at = ?, error_message = 'process interrupted'
		WHERE status IN ('running', 'pending')
	`, now)
	if err != nil {
		return 0, fmt.Errorf("cleanup stale restore jobs: %w", err)
	}
	return res.RowsAffected()
}
