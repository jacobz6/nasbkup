package db

import (
        "database/sql"
        "fmt"
        "time"

        "github.com/nas-backup/internal/models"
)

// scanner is a common interface for *sql.Row and *sql.Rows Scan method.
type scanner interface {
        Scan(dest ...interface{}) error
}

// FileRepository handles all CRUD operations for the files table.
type FileRepository struct {
        db *sql.DB
}

// NewFileRepository creates a new FileRepository with the given database connection.
func NewFileRepository(db *sql.DB) *FileRepository {
        return &FileRepository{db: db}
}

// scanFileRecord scans a single file row from a scanner into a FileRecord.
// The inode column is read from the database but discarded since the model
// does not expose it as a field.
func scanFileRecord(s scanner) (*models.FileRecord, error) {
        var (
                rec       models.FileRecord
                modTime   string
                createdAt string
                updatedAt string
                backupID  sql.NullInt64
                _inode    int64 // read but not exposed on model
        )
        if err := s.Scan(
                &rec.ID, &rec.Path, &rec.Size, &modTime,
                &rec.Hash, &rec.Status, &backupID, &_inode,
                &createdAt, &updatedAt,
        ); err != nil {
                return nil, err
        }

        t, err := time.Parse(time.RFC3339, modTime)
        if err != nil {
                return nil, fmt.Errorf("parse mod_time %q: %w", modTime, err)
        }
        rec.ModTime = t

        t, err = time.Parse(time.RFC3339, createdAt)
        if err != nil {
                return nil, fmt.Errorf("parse created_at %q: %w", createdAt, err)
        }
        rec.CreatedAt = t

        t, err = time.Parse(time.RFC3339, updatedAt)
        if err != nil {
                return nil, fmt.Errorf("parse updated_at %q: %w", updatedAt, err)
        }
        rec.UpdatedAt = t

        if backupID.Valid {
                rec.BackupID = backupID.Int64
        }

        return &rec, nil
}

// Upsert inserts a new file record or updates an existing one if the path already exists.
// When the path exists, it updates size, mod_time, hash, inode, status to active, and updated_at.
// Returns the file ID of the inserted or updated record.
func (r *FileRepository) Upsert(path string, size int64, modTime time.Time, hash string, inode uint64) (int64, error) {
        now := Now()
        modTimeStr := modTime.UTC().Format(time.RFC3339)

        result, err := r.db.Exec(`
                INSERT INTO files (path, size, mod_time, hash, inode, status, created_at, updated_at)
                VALUES (?, ?, ?, ?, ?, 'active', ?, ?)
                ON CONFLICT(path) DO UPDATE SET
                        size = excluded.size,
                        mod_time = excluded.mod_time,
                        hash = excluded.hash,
                        inode = excluded.inode,
                        status = 'active',
                        updated_at = excluded.updated_at
        `, path, size, modTimeStr, hash, inode, now, now)
        if err != nil {
                return 0, fmt.Errorf("upsert file %q: %w", path, err)
        }

        id, err := result.LastInsertId()
        if err != nil {
                return 0, fmt.Errorf("last insert id after upsert file %q: %w", path, err)
        }
        return id, nil
}

// GetByPath retrieves a file record by its path.
// Returns nil without error if no record is found.
func (r *FileRepository) GetByPath(path string) (*models.FileRecord, error) {
        row := r.db.QueryRow(`
                SELECT id, path, size, mod_time, hash, status, backup_id, inode, created_at, updated_at
                FROM files WHERE path = ?
        `, path)
        rec, err := scanFileRecord(row)
        if err != nil {
                if err == sql.ErrNoRows {
                        return nil, nil
                }
                return nil, fmt.Errorf("get file by path %q: %w", path, err)
        }
        return rec, nil
}

// GetByHash retrieves all active file records matching the given hash.
func (r *FileRepository) GetByHash(hash string) ([]*models.FileRecord, error) {
        rows, err := r.db.Query(`
                SELECT id, path, size, mod_time, hash, status, backup_id, inode, created_at, updated_at
                FROM files WHERE hash = ? AND status = 'active'
        `, hash)
        if err != nil {
                return nil, fmt.Errorf("get files by hash %q: %w", hash, err)
        }
        defer rows.Close()

        records := make([]*models.FileRecord, 0)
        for rows.Next() {
                rec, err := scanFileRecord(rows)
                if err != nil {
                        return nil, fmt.Errorf("scan file row by hash: %w", err)
                }
                records = append(records, rec)
        }
        if err := rows.Err(); err != nil {
                return nil, fmt.Errorf("iterate files by hash: %w", err)
        }
        return records, nil
}

// ListByStatus retrieves file records filtered by status with pagination.
// Use limit and offset to control pagination; pass 0 for limit to retrieve all records.
func (r *FileRepository) ListByStatus(status models.FileStatus, limit, offset int) ([]*models.FileRecord, error) {
	query := `SELECT id, path, size, mod_time, hash, status, backup_id, inode, created_at, updated_at
		FROM files WHERE status = ?
		ORDER BY path`
	var args []interface{}
	args = append(args, status)
	if limit > 0 {
		query += " LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
	}

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list files by status %q: %w", status, err)
	}
	defer rows.Close()

	records := make([]*models.FileRecord, 0)
	for rows.Next() {
		rec, err := scanFileRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("scan file row by status: %w", err)
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate files by status: %w", err)
	}
	return records, nil
}

// MarkDeleted sets the status of a file to "deleted" and updates the updated_at timestamp.
func (r *FileRepository) MarkDeleted(path string) error {
        now := Now()
        result, err := r.db.Exec(`
                UPDATE files SET status = 'deleted', updated_at = ? WHERE path = ?
        `, now, path)
        if err != nil {
                return fmt.Errorf("mark file deleted %q: %w", path, err)
        }
        affected, err := result.RowsAffected()
        if err != nil {
                return fmt.Errorf("rows affected after mark deleted %q: %w", path, err)
        }
        if affected == 0 {
                return fmt.Errorf("file not found for mark deleted: %q", path)
        }
        return nil
}

// MarkDeletedBatch marks multiple files as deleted in a single transaction.
// Returns an error if any path fails to be updated; the entire batch is rolled back.
func (r *FileRepository) MarkDeletedBatch(paths []string) error {
        if len(paths) == 0 {
                return nil
        }

        tx, err := r.db.Begin()
        if err != nil {
                return fmt.Errorf("begin transaction for batch mark deleted: %w", err)
        }
        defer tx.Rollback()

        now := Now()
        stmt, err := tx.Prepare(`
                UPDATE files SET status = 'deleted', updated_at = ? WHERE path = ?
        `)
        if err != nil {
                return fmt.Errorf("prepare batch mark deleted: %w", err)
        }
        defer stmt.Close()

        for _, p := range paths {
                if _, err := stmt.Exec(now, p); err != nil {
                        return fmt.Errorf("mark deleted %q in batch: %w", p, err)
                }
        }

        if err := tx.Commit(); err != nil {
                return fmt.Errorf("commit batch mark deleted: %w", err)
        }
        return nil
}

// UpdateHash updates the hash value and updated_at timestamp for a file identified by ID.
func (r *FileRepository) UpdateHash(id int64, hash string) error {
        now := Now()
        result, err := r.db.Exec(`
                UPDATE files SET hash = ?, updated_at = ? WHERE id = ?
        `, hash, now, id)
        if err != nil {
                return fmt.Errorf("update hash for file %d: %w", id, err)
        }
        affected, err := result.RowsAffected()
        if err != nil {
                return fmt.Errorf("rows affected after update hash for file %d: %w", id, err)
        }
        if affected == 0 {
                return fmt.Errorf("file not found for update hash: %d", id)
        }
        return nil
}

// CountByStatus returns the number of file records with the given status.
func (r *FileRepository) CountByStatus(status models.FileStatus) (int64, error) {
        var count int64
        err := r.db.QueryRow(`SELECT COUNT(*) FROM files WHERE status = ?`, status).Scan(&count)
        if err != nil {
                return 0, fmt.Errorf("count files by status %q: %w", status, err)
        }
        return count, nil
}

// TotalSizeByStatus returns the sum of file sizes for all records with the given status.
// Returns 0 if no records match.
func (r *FileRepository) TotalSizeByStatus(status models.FileStatus) (int64, error) {
        var total sql.NullInt64
        err := r.db.QueryRow(`SELECT SUM(size) FROM files WHERE status = ?`, status).Scan(&total)
        if err != nil {
                return 0, fmt.Errorf("total size by status %q: %w", status, err)
        }
        if !total.Valid {
                return 0, nil
        }
        return total.Int64, nil
}

// ListActiveByDirectory retrieves all active file records whose path starts with
// the given directory path (i.e., files under that directory).
func (r *FileRepository) ListActiveByDirectory(dirPath string) ([]*models.FileRecord, error) {
        pattern := dirPath + "/%"
        rows, err := r.db.Query(`
                SELECT id, path, size, mod_time, hash, status, backup_id, inode, created_at, updated_at
                FROM files WHERE path LIKE ? AND status = 'active'
                ORDER BY path
        `, pattern)
        if err != nil {
                return nil, fmt.Errorf("list active files by directory %q: %w", dirPath, err)
        }
        defer rows.Close()

        records := make([]*models.FileRecord, 0)
        for rows.Next() {
                rec, err := scanFileRecord(rows)
                if err != nil {
                        return nil, fmt.Errorf("scan file row by directory: %w", err)
                }
                records = append(records, rec)
        }
        if err := rows.Err(); err != nil {
                return nil, fmt.Errorf("iterate active files by directory: %w", err)
        }
        return records, nil
}

// ListAllPaths returns all file paths in the database, used for comparing
// against scan results to detect deleted files.
func (r *FileRepository) ListAllPaths() ([]string, error) {
        rows, err := r.db.Query(`SELECT path FROM files ORDER BY path`)
        if err != nil {
                return nil, fmt.Errorf("list all paths: %w", err)
        }
        defer rows.Close()

        var paths []string
        for rows.Next() {
                var p string
                if err := rows.Scan(&p); err != nil {
                        return nil, fmt.Errorf("scan path row: %w", err)
                }
                paths = append(paths, p)
        }
        if err := rows.Err(); err != nil {
                return nil, fmt.Errorf("iterate all paths: %w", err)
        }
        return paths, nil
}

// GetByID retrieves a file record by its primary key ID.
// Returns nil without error if no record is found.
func (r *FileRepository) GetByID(id int64) (*models.FileRecord, error) {
        row := r.db.QueryRow(`
                SELECT id, path, size, mod_time, hash, status, backup_id, inode, created_at, updated_at
                FROM files WHERE id = ?
        `, id)
        rec, err := scanFileRecord(row)
        if err != nil {
                if err == sql.ErrNoRows {
                        return nil, nil
                }
                return nil, fmt.Errorf("get file by id %d: %w", id, err)
        }
        return rec, nil
}
