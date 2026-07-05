package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/nas-backup/internal/models"
)

// HashRepository manages the global hash dedup index, mapping content hashes
// to their single physical storage location and tracking reference counts.
type HashRepository struct {
	db *sql.DB
}

// NewHashRepository creates a new HashRepository with the given database connection.
func NewHashRepository(db *sql.DB) *HashRepository {
	return &HashRepository{db: db}
}

// scanHashIndexRecord scans a single hash_index row from a scanner into a HashIndexRecord.
func scanHashIndexRecord(s scanner) (*models.HashIndexRecord, error) {
	var (
		rec       models.HashIndexRecord
		createdAt string
	)
	if err := s.Scan(
		&rec.ID, &rec.Hash, &rec.FileSize, &rec.StorageKey, &rec.RefCount, &createdAt,
	); err != nil {
		return nil, err
	}

	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at %q: %w", createdAt, err)
	}
	rec.CreatedAt = t

	return &rec, nil
}

// GetByHash retrieves a hash index record by its hash value.
// Returns nil without error if no record is found.
func (r *HashRepository) GetByHash(hash string) (*models.HashIndexRecord, error) {
	row := r.db.QueryRow(`
		SELECT id, hash, file_size, storage_key, ref_count, created_at
		FROM hash_index WHERE hash = ?
	`, hash)
	rec, err := scanHashIndexRecord(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get hash index by hash %q: %w", hash, err)
	}
	return rec, nil
}

// Upsert inserts a new hash index record or increments the ref_count if the hash already exists.
// When the hash already exists, only the ref_count is incremented; other fields are not updated.
// Returns the hash record ID.
func (r *HashRepository) Upsert(hash string, fileSize int64, storageKey string) (int64, error) {
	now := Now()
	var id int64
	err := r.db.QueryRow(`
		INSERT INTO hash_index (hash, file_size, storage_key, ref_count, created_at)
		VALUES (?, ?, ?, 1, ?)
		ON CONFLICT(hash) DO UPDATE SET ref_count = ref_count + 1
		RETURNING id
	`, hash, fileSize, storageKey, now).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert hash index %q: %w", hash, err)
	}

	return id, nil
}

// IncrementRef increments the ref_count for an existing hash record.
// Returns an error if the hash does not exist.
func (r *HashRepository) IncrementRef(hash string) error {
	result, err := r.db.Exec(`
		UPDATE hash_index SET ref_count = ref_count + 1 WHERE hash = ?
	`, hash)
	if err != nil {
		return fmt.Errorf("increment ref for hash %q: %w", hash, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected after increment ref for hash %q: %w", hash, err)
	}
	if affected == 0 {
		return fmt.Errorf("hash not found for increment ref: %q", hash)
	}
	return nil
}

// DecrementRef decrements the ref_count for an existing hash record.
// The ref_count will never go below 0. Returns the new ref_count value.
func (r *HashRepository) DecrementRef(hash string) (int, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin transaction for decrement ref %q: %w", hash, err)
	}
	defer tx.Rollback()

	result, err := tx.Exec(`
		UPDATE hash_index SET ref_count = ref_count - 1 WHERE hash = ? AND ref_count > 0
	`, hash)
	if err != nil {
		return 0, fmt.Errorf("decrement ref for hash %q: %w", hash, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected after decrement ref for hash %q: %w", hash, err)
	}
	if affected == 0 {
		var count int
		err := tx.QueryRow(`SELECT ref_count FROM hash_index WHERE hash = ?`, hash).Scan(&count)
		if err != nil {
			if err == sql.ErrNoRows {
				return 0, fmt.Errorf("hash not found for decrement ref: %q", hash)
			}
			return 0, fmt.Errorf("check ref_count for hash %q: %w", hash, err)
		}
		tx.Commit()
		return 0, nil
	}

	var newCount int
	err = tx.QueryRow(`SELECT ref_count FROM hash_index WHERE hash = ?`, hash).Scan(&newCount)
	if err != nil {
		return 0, fmt.Errorf("get ref_count after decrement for hash %q: %w", hash, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit decrement ref for hash %q: %w", hash, err)
	}
	return newCount, nil
}

// GetOrphans retrieves all hash index records with ref_count equal to 0.
// These represent content that is no longer referenced by any file.
func (r *HashRepository) GetOrphans() ([]*models.HashIndexRecord, error) {
	rows, err := r.db.Query(`
		SELECT id, hash, file_size, storage_key, ref_count, created_at
		FROM hash_index WHERE ref_count = 0
		ORDER BY created_at
	`)
	if err != nil {
		return nil, fmt.Errorf("get orphan hash records: %w", err)
	}
	defer rows.Close()

	records := make([]*models.HashIndexRecord, 0)
	for rows.Next() {
		rec, err := scanHashIndexRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("scan orphan hash row: %w", err)
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate orphan hash records: %w", err)
	}
	return records, nil
}

// GetOrphansOlderThan retrieves orphaned hash index records (ref_count=0)
// that were created more than the specified number of days ago.
func (r *HashRepository) GetOrphansOlderThan(days int) ([]*models.HashIndexRecord, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
	rows, err := r.db.Query(`
		SELECT id, hash, file_size, storage_key, ref_count, created_at
		FROM hash_index WHERE ref_count = 0 AND created_at < ?
		ORDER BY created_at
	`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("get orphan hash records older than %d days: %w", days, err)
	}
	defer rows.Close()

	records := make([]*models.HashIndexRecord, 0)
	for rows.Next() {
		rec, err := scanHashIndexRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("scan old orphan hash row: %w", err)
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate old orphan hash records: %w", err)
	}
	return records, nil
}

// DeleteByHash removes a hash index record by its hash value.
func (r *HashRepository) DeleteByHash(hash string) error {
	result, err := r.db.Exec(`DELETE FROM hash_index WHERE hash = ?`, hash)
	if err != nil {
		return fmt.Errorf("delete hash index %q: %w", hash, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected after delete hash %q: %w", hash, err)
	}
	if affected == 0 {
		return fmt.Errorf("hash not found for delete: %q", hash)
	}
	return nil
}

// DeleteBatch removes multiple hash index records in a single transaction.
// If any delete fails, the entire batch is rolled back.
func (r *HashRepository) DeleteBatch(hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}

	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction for batch delete hashes: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`DELETE FROM hash_index WHERE hash = ?`)
	if err != nil {
		return fmt.Errorf("prepare batch delete hash: %w", err)
	}
	defer stmt.Close()

	for _, h := range hashes {
		if _, err := stmt.Exec(h); err != nil {
			return fmt.Errorf("delete hash %q in batch: %w", h, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit batch delete hashes: %w", err)
	}
	return nil
}

// TotalDedupSaved returns the total bytes saved by deduplication.
// It sums (file_size * (ref_count - 1)) for all hash records with ref_count > 1,
// representing the storage that would have been consumed without dedup.
func (r *HashRepository) TotalDedupSaved() (int64, error) {
	var total sql.NullInt64
	err := r.db.QueryRow(`
		SELECT SUM(file_size * (ref_count - 1)) FROM hash_index WHERE ref_count > 1
	`).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("calculate total dedup saved: %w", err)
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

// OSSStorageUsed returns the total bytes actually stored in OSS (compressed +
// encrypted). Deduplicates by storage_key so the same object referenced by
// multiple files is counted only once.
func (r *HashRepository) OSSStorageUsed() (int64, error) {
	var total sql.NullInt64
	err := r.db.QueryRow(`
		SELECT COALESCE(SUM(stored_size), 0) FROM (
			SELECT DISTINCT storage_key, stored_size FROM backup_files
			WHERE storage_key <> '' AND stored_size > 0
		)
	`).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("calculate OSS storage used: %w", err)
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

// CountActiveHashes returns the number of hash_index records with ref_count > 0,
// i.e. unique content hashes currently referenced by at least one active file.
func (r *HashRepository) CountActiveHashes() (int64, error) {
	var count int64
	err := r.db.QueryRow(`SELECT COUNT(*) FROM hash_index WHERE ref_count > 0`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active hash records: %w", err)
	}
	return count, nil
}

// HasRefCountMismatches returns true if any hash_index.ref_count disagrees with
// the actual number of active files referencing that hash. This is a lightweight
// single-query check used by the dashboard's "needs reconcile" indicator.
func (r *HashRepository) HasRefCountMismatches() (bool, error) {
	var exists int
	err := r.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM hash_index hi
			LEFT JOIN (
				SELECT hash, COUNT(*) AS cnt
				FROM files
				WHERE status = 'active' AND hash <> ''
				GROUP BY hash
			) fc ON hi.hash = fc.hash
			WHERE COALESCE(fc.cnt, 0) <> hi.ref_count
		)
	`).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check ref_count mismatches: %w", err)
	}
	return exists == 1, nil
}

// GetAllStorageKeys retrieves all storage_key values from the hash index.
// This is used for garbage collection to compare against objects in OSS storage.
func (r *HashRepository) GetAllStorageKeys() ([]string, error) {
	rows, err := r.db.Query(`SELECT storage_key FROM hash_index ORDER BY storage_key`)
	if err != nil {
		return nil, fmt.Errorf("get all storage keys: %w", err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("scan storage key row: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate storage keys: %w", err)
	}
	return keys, nil
}

// ListAll retrieves every hash index record. Used by the reconciler to compare
// the DB index against OSS contents and against actual file references.
func (r *HashRepository) ListAll() ([]*models.HashIndexRecord, error) {
	rows, err := r.db.Query(`
		SELECT id, hash, file_size, storage_key, ref_count, created_at
		FROM hash_index ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("list all hash index records: %w", err)
	}
	defer rows.Close()

	records := make([]*models.HashIndexRecord, 0)
	for rows.Next() {
		rec, err := scanHashIndexRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("scan hash index row: %w", err)
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate all hash index records: %w", err)
	}
	return records, nil
}

// SetRefCount sets the ref_count of a hash record to the specified value.
// Used by the reconciler to correct ref_count drift caused by missed
// DecrementRef / IncrementRef calls during crashes.
func (r *HashRepository) SetRefCount(hash string, count int) error {
	if count < 0 {
		count = 0
	}
	result, err := r.db.Exec(`UPDATE hash_index SET ref_count = ? WHERE hash = ?`, count, hash)
	if err != nil {
		return fmt.Errorf("set ref_count for hash %q: %w", hash, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected after set ref_count %q: %w", hash, err)
	}
	if affected == 0 {
		return fmt.Errorf("hash not found for set ref_count: %q", hash)
	}
	return nil
}

// DeleteByStorageKey removes a hash index record by its storage_key.
// Used by the reconciler when an OSS object is missing AND ref_count is 0
// (safe to drop the index entry). Returns nil without error if no row matched.
func (r *HashRepository) DeleteByStorageKey(storageKey string) error {
	_, err := r.db.Exec(`DELETE FROM hash_index WHERE storage_key = ?`, storageKey)
	if err != nil {
		return fmt.Errorf("delete hash index by storage_key %q: %w", storageKey, err)
	}
	return nil
}
