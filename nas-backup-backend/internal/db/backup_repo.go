package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/nas-backup/internal/models"
)

// BackupRepository handles backup session records and backup-file junction data.
type BackupRepository struct {
	db *sql.DB
}

// NewBackupRepository creates a new BackupRepository with the given database connection.
func NewBackupRepository(db *sql.DB) *BackupRepository {
	return &BackupRepository{db: db}
}

// scanBackupRecord scans a single backup row from a scanner into a BackupRecord.
func scanBackupRecord(s scanner) (*models.BackupRecord, error) {
	var (
		rec          models.BackupRecord
		baseBackupID sql.NullInt64
		startedAt    sql.NullString
		completedAt  sql.NullString
		createdAt    string
	)
	if err := s.Scan(
		&rec.ID, &rec.Type, &rec.Status, &baseBackupID,
		&rec.TotalFiles, &rec.TotalSize, &rec.UploadedSize,
		&rec.SkippedByDedup, &rec.SkippedByInc, &rec.CompressSaved,
		&startedAt, &completedAt, &rec.ErrorMessage, &createdAt,
	); err != nil {
		return nil, err
	}

	if baseBackupID.Valid {
		rec.BaseBackupID = &baseBackupID.Int64
	}

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

	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at %q: %w", createdAt, err)
	}
	rec.CreatedAt = t

	return &rec, nil
}

// scanBackupFileRecord scans a single backup_file row from a scanner into a BackupFileRecord.
func scanBackupFileRecord(s scanner) (*models.BackupFileRecord, error) {
	var rec models.BackupFileRecord
	if err := s.Scan(
		&rec.BackupID, &rec.FileID, &rec.StorageKey,
		&rec.EncryptedIV, &rec.AuthTag, &rec.CompressType,
		&rec.OriginalSize, &rec.StoredSize,
	); err != nil {
		return nil, err
	}
	return &rec, nil
}

// Create inserts a new backup record with status "pending" and returns its ID.
// If baseBackupID is not nil, the backup is incremental relative to the given full backup.
func (r *BackupRepository) Create(backupType models.BackupType, baseBackupID *int64) (int64, error) {
	now := Now()
	result, err := r.db.Exec(`
		INSERT INTO backups (type, status, base_backup_id, created_at)
		VALUES (?, 'pending', ?, ?)
	`, backupType, baseBackupID, now)
	if err != nil {
		return 0, fmt.Errorf("create backup (type=%s): %w", backupType, err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id after create backup: %w", err)
	}
	return id, nil
}

// UpdateStatus updates the status of a backup session.
// If the new status is "running", started_at is set to the current time.
// If the new status is "completed", "failed", or "cancelled", completed_at is set to the current time.
func (r *BackupRepository) UpdateStatus(id int64, status models.BackupStatus, errorMsg string) error {
	now := Now()

	switch status {
	case models.BackupStatusRunning:
		_, err := r.db.Exec(`
			UPDATE backups SET status = ?, started_at = ?, error_message = ? WHERE id = ?
		`, status, now, errorMsg, id)
		if err != nil {
			return fmt.Errorf("update backup %d status to %q: %w", id, status, err)
		}

	case models.BackupStatusCompleted, models.BackupStatusFailed, models.BackupStatusCancelled:
		_, err := r.db.Exec(`
			UPDATE backups SET status = ?, completed_at = ?, error_message = ? WHERE id = ?
		`, status, now, errorMsg, id)
		if err != nil {
			return fmt.Errorf("update backup %d status to %q: %w", id, status, err)
		}

	default:
		_, err := r.db.Exec(`
			UPDATE backups SET status = ?, error_message = ? WHERE id = ?
		`, status, errorMsg, id)
		if err != nil {
			return fmt.Errorf("update backup %d status to %q: %w", id, status, err)
		}
	}

	return nil
}

// UpdateStats updates the statistics fields of a backup session.
func (r *BackupRepository) UpdateStats(id int64, totalFiles, totalSize, uploadedSize, skippedDedup, skippedInc int, compressSaved int64) error {
	_, err := r.db.Exec(`
		UPDATE backups SET
			total_files = ?,
			total_size = ?,
			uploaded_size = ?,
			skipped_dedup = ?,
			skipped_inc = ?,
			compress_saved = ?
		WHERE id = ?
	`, totalFiles, totalSize, uploadedSize, skippedDedup, skippedInc, compressSaved, id)
	if err != nil {
		return fmt.Errorf("update stats for backup %d: %w", id, err)
	}
	return nil
}

// GetByID retrieves a backup record by its primary key ID.
// Returns nil without error if no record is found.
func (r *BackupRepository) GetByID(id int64) (*models.BackupRecord, error) {
	row := r.db.QueryRow(`
		SELECT id, type, status, base_backup_id,
		       total_files, total_size, uploaded_size,
		       skipped_dedup, skipped_inc, compress_saved,
		       started_at, completed_at, error_message, created_at
		FROM backups WHERE id = ?
	`, id)
	rec, err := scanBackupRecord(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get backup by id %d: %w", id, err)
	}
	return rec, nil
}

// List retrieves backup records ordered by created_at descending with pagination.
func (r *BackupRepository) List(limit, offset int) ([]*models.BackupRecord, error) {
	rows, err := r.db.Query(`
		SELECT id, type, status, base_backup_id,
		       total_files, total_size, uploaded_size,
		       skipped_dedup, skipped_inc, compress_saved,
		       started_at, completed_at, error_message, created_at
		FROM backups ORDER BY created_at DESC
		LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	defer rows.Close()

	records := make([]*models.BackupRecord, 0)
	for rows.Next() {
		rec, err := scanBackupRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("scan backup row: %w", err)
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate backups: %w", err)
	}
	return records, nil
}

// GetLatestCompleted retrieves the most recent backup with status "completed".
// Returns nil without error if no completed backup exists.
func (r *BackupRepository) GetLatestCompleted() (*models.BackupRecord, error) {
	row := r.db.QueryRow(`
		SELECT id, type, status, base_backup_id,
		       total_files, total_size, uploaded_size,
		       skipped_dedup, skipped_inc, compress_saved,
		       started_at, completed_at, error_message, created_at
		FROM backups WHERE status = 'completed'
		ORDER BY created_at DESC LIMIT 1
	`)
	rec, err := scanBackupRecord(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get latest completed backup: %w", err)
	}
	return rec, nil
}

// GetLatestFull retrieves the most recent completed full backup.
// Returns nil without error if no completed full backup exists.
func (r *BackupRepository) GetLatestFull() (*models.BackupRecord, error) {
	row := r.db.QueryRow(`
		SELECT id, type, status, base_backup_id,
		       total_files, total_size, uploaded_size,
		       skipped_dedup, skipped_inc, compress_saved,
		       started_at, completed_at, error_message, created_at
		FROM backups WHERE status = 'completed' AND type = 'full'
		ORDER BY created_at DESC LIMIT 1
	`)
	rec, err := scanBackupRecord(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get latest full backup: %w", err)
	}
	return rec, nil
}

// GetIncrementalsSinceFull retrieves all completed incremental backups
// that are based on the specified full backup, ordered by created_at.
func (r *BackupRepository) GetIncrementalsSinceFull(fullBackupID int64) ([]*models.BackupRecord, error) {
	rows, err := r.db.Query(`
		SELECT id, type, status, base_backup_id,
		       total_files, total_size, uploaded_size,
		       skipped_dedup, skipped_inc, compress_saved,
		       started_at, completed_at, error_message, created_at
		FROM backups
		WHERE status = 'completed' AND type = 'incremental' AND base_backup_id = ?
		ORDER BY created_at
	`, fullBackupID)
	if err != nil {
		return nil, fmt.Errorf("get incrementals since full %d: %w", fullBackupID, err)
	}
	defer rows.Close()

	records := make([]*models.BackupRecord, 0)
	for rows.Next() {
		rec, err := scanBackupRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("scan incremental backup row: %w", err)
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate incremental backups: %w", err)
	}
	return records, nil
}

// CountByStatus returns the number of backup records with the given status.
func (r *BackupRepository) CountByStatus(status models.BackupStatus) (int64, error) {
	var count int64
	err := r.db.QueryRow(`SELECT COUNT(*) FROM backups WHERE status = ?`, status).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count backups by status %q: %w", status, err)
	}
	return count, nil
}

// AddBackupFile inserts a single backup-file junction record.
func (r *BackupRepository) AddBackupFile(bf *models.BackupFileRecord) error {
	if bf == nil {
		return fmt.Errorf("backup file record must not be nil")
	}
	_, err := r.db.Exec(`
		INSERT INTO backup_files (backup_id, file_id, storage_key, encrypted_iv, auth_tag, compress_type, original_size, stored_size)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, bf.BackupID, bf.FileID, bf.StorageKey, bf.EncryptedIV, bf.AuthTag, bf.CompressType, bf.OriginalSize, bf.StoredSize)
	if err != nil {
		return fmt.Errorf("add backup file (backup=%d, file=%d): %w", bf.BackupID, bf.FileID, err)
	}
	return nil
}

// AddBackupFilesBatch inserts multiple backup-file junction records in a single transaction.
// If any insert fails, the entire batch is rolled back.
func (r *BackupRepository) AddBackupFilesBatch(bfs []*models.BackupFileRecord) error {
	if len(bfs) == 0 {
		return nil
	}

	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction for batch backup files: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO backup_files (backup_id, file_id, storage_key, encrypted_iv, auth_tag, compress_type, original_size, stored_size)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare batch backup file insert: %w", err)
	}
	defer stmt.Close()

	for _, bf := range bfs {
		if bf == nil {
			continue
		}
		if _, err := stmt.Exec(
			bf.BackupID, bf.FileID, bf.StorageKey, bf.EncryptedIV,
			bf.AuthTag, bf.CompressType, bf.OriginalSize, bf.StoredSize,
		); err != nil {
			return fmt.Errorf("insert backup file in batch (backup=%d, file=%d): %w", bf.BackupID, bf.FileID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit batch backup files: %w", err)
	}
	return nil
}

// GetBackupFiles retrieves all backup-file junction records for a given backup session.
func (r *BackupRepository) GetBackupFiles(backupID int64) ([]*models.BackupFileRecord, error) {
	rows, err := r.db.Query(`
		SELECT backup_id, file_id, storage_key, encrypted_iv, auth_tag, compress_type, original_size, stored_size
		FROM backup_files WHERE backup_id = ?
	`, backupID)
	if err != nil {
		return nil, fmt.Errorf("get backup files for backup %d: %w", backupID, err)
	}
	defer rows.Close()

	records := make([]*models.BackupFileRecord, 0)
	for rows.Next() {
		rec, err := scanBackupFileRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("scan backup file row: %w", err)
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate backup files: %w", err)
	}
	return records, nil
}

// GetFileRestoreInfo retrieves the latest backup-file entry for a given file ID,
// which is used to determine how to restore the file.
// Returns nil without error if no record is found.
func (r *BackupRepository) GetFileRestoreInfo(fileID int64) (*models.BackupFileRecord, error) {
	row := r.db.QueryRow(`
		SELECT backup_id, file_id, storage_key, encrypted_iv, auth_tag, compress_type, original_size, stored_size
		FROM backup_files WHERE file_id = ?
		ORDER BY backup_id DESC LIMIT 1
	`, fileID)
	rec, err := scanBackupFileRecord(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get file restore info for file %d: %w", fileID, err)
	}
	return rec, nil
}

// IsRunning checks whether there is a backup currently in "running" status.
func (r *BackupRepository) IsRunning() (bool, error) {
	var count int64
	err := r.db.QueryRow(`SELECT COUNT(*) FROM backups WHERE status = 'running'`).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check running backup: %w", err)
	}
	return count > 0, nil
}
