// Package dedup performs content-hash based deduplication. It checks each
// changed file's hash against the global hash index; if the content already
// exists in the backup store the file is skipped (only a reference count is
// incremented), otherwise it is queued for upload.
package dedup

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nas-backup/internal/db"
	"github.com/nas-backup/internal/models"
	"github.com/nas-backup/internal/scanner"
	"github.com/nas-backup/internal/storage"
)

// DedupFileEntry represents a file that needs to be uploaded (or has been
// matched to an existing storage key after upload).
type DedupFileEntry struct {
	scanner.FileChange
	StorageKey string // Empty until set after upload.
	IsNew      bool   // True if no existing hash index entry was found.
}

// DedupSkippedEntry represents a file whose content was already present in
// the backup store, so the upload was skipped.
type DedupSkippedEntry struct {
	Path              string
	Hash              string
	ExistingStorageKey string
	Reason            string
}

// DedupResult aggregates the outcome of a deduplication pass.
type DedupResult struct {
	ToUpload   []DedupFileEntry    // Files that need to be uploaded.
	Skipped    []DedupSkippedEntry // Files whose content already exists.
	TotalSaved int64               // Bytes saved by skipping duplicates.
}

// truncateHash returns the first n characters of hash, or the full hash if
// it is shorter than n, avoiding a slice-bounds panic.
func truncateHash(hash string, n int) string {
	if len(hash) < n {
		return hash
	}
	return hash[:n]
}

// Deduplicator checks file changes against the global hash index.
type Deduplicator struct {
	hashRepo    *db.HashRepository
	storage     *storage.StorageManager
	concurrency int // worker count for batch OSS existence checks
}

// NewDeduplicator creates a Deduplicator backed by the given hash repository.
// The storage manager is used to verify that the OSS object referenced by a
// hash_index row actually exists — if it has been lost (e.g. by a crash in a
// previous backup window), the file is re-queued for upload instead of being
// silently skipped.
//
// concurrency controls the worker pool for batch OSS existence checks.
// When ≤ 0, storage.DefaultBatchConcurrency is used.
func NewDeduplicator(hashRepo *db.HashRepository, stor *storage.StorageManager, concurrency int) *Deduplicator {
	if concurrency <= 0 {
		concurrency = storage.DefaultBatchConcurrency
	}
	return &Deduplicator{
		hashRepo:    hashRepo,
		storage:     stor,
		concurrency: concurrency,
	}
}

// Deduplicate processes all Added and Modified changes from a scan result,
// checking each file's hash against the global hash index. Files whose
// content already exists are skipped (with a ref-count increment); new
// content is added to the upload list.
//
// Implementation note: this function performs OSS existence checks in batch
// (via ExistsBatch) rather than one-by-one. This is critical for performance
// when scanning thousands of unchanged files during a full backup: a serial
// rclone lsl per file would take minutes, while the batched version takes
// seconds at concurrency=8.
func (d *Deduplicator) Deduplicate(ctx context.Context, changes []scanner.FileChange) (*DedupResult, error) {
	result := &DedupResult{}

	// Pass 1: classify changes and look up hash_index entries.
	// We collect (change, existingRecord) pairs for files that need an OSS
	// existence check; everything else is handled inline.
	type pending struct {
		change   scanner.FileChange
		existing *models.HashIndexRecord
	}
	var (
		pendingChecks []pending
	)
	for _, change := range changes {
		switch change.ChangeType {
		case scanner.Added, scanner.Modified:
			// Files with no hash are always uploaded.
			if change.NewHash == "" {
				result.ToUpload = append(result.ToUpload, DedupFileEntry{
					FileChange: change,
					IsNew:      true,
				})
				continue
			}
			existing, err := d.hashRepo.GetByHash(change.NewHash)
			if err != nil {
				return nil, fmt.Errorf("query hash index for %q: %w", change.NewHash, err)
			}
			if existing == nil {
				// New content — queue for upload.
				result.ToUpload = append(result.ToUpload, DedupFileEntry{
					FileChange: change,
					IsNew:      true,
				})
				continue
			}
			// Hash exists in DB — defer OSS existence check to the batch pass.
			pendingChecks = append(pendingChecks, pending{change: change, existing: existing})

		case scanner.Deleted, scanner.Unchanged, scanner.Renamed:
			// Deleted files are handled separately by the backup engine.
			// Unchanged and Renamed entries are not part of the dedup pipeline.
			continue
		}
	}

	// Pass 2: batch OSS existence check for all hashes that exist in DB.
	// Use a set to deduplicate storage_keys (multiple files may share the
	// same hash, hence the same storage_key).
	ossExists := map[string]bool{}
	ossCheckFailed := map[string]bool{}
	ossCheckPerformed := false
	if len(pendingChecks) > 0 && d.storage != nil {
		ossCheckPerformed = true
		seen := map[string]struct{}{}
		keys := make([]string, 0, len(pendingChecks))
		for _, p := range pendingChecks {
			if _, ok := seen[p.existing.StorageKey]; ok {
				continue
			}
			seen[p.existing.StorageKey] = struct{}{}
			keys = append(keys, p.existing.StorageKey)
		}
		existsMap, errs, err := d.storage.ExistsBatch(ctx, keys, d.concurrency)
		if err != nil {
			return nil, fmt.Errorf("batch OSS existence check: %w", err)
		}
		ossExists = existsMap
		for _, ke := range errs {
			slog.Warn("dedup: OSS existence check failed for key, will re-upload to be safe",
				"storage_key", ke.Key, "error", ke.Message)
			ossCheckFailed[ke.Key] = true
		}
	}

	// Pass 3: apply the OSS check results and finalize each pending change.
	for _, p := range pendingChecks {
		exists, checked := ossExists[p.existing.StorageKey]
		checkFailed := ossCheckFailed[p.existing.StorageKey]

		// Determine if the OSS object is missing or unverifiable.
		// Fail-close policy for backup safety:
		//   - If OSS was NOT checked (storage is nil, e.g. tests), assume exists (legacy behavior).
		//   - If OSS was checked AND (object not found OR check errored), re-upload.
		//     This prevents silent data loss when rclone returns an unexpected error
		//     (e.g. crypt remote not-found messages that don't match our keyword list).
		var ossMissing bool
		if !ossCheckPerformed {
			ossMissing = false
		} else {
			ossMissing = checkFailed || (checked && !exists)
		}

		if ossMissing {
			// OSS object missing or unverifiable — re-upload to restore data redundancy.
			// Do NOT increment ref_count; the existing hash_index row will
			// be reused (processAndUploadFile keeps the same storage_key).
			slog.Info("dedup: OSS object missing or unverifiable, re-queuing for upload",
				"hash", truncateHash(p.change.NewHash, 16),
				"storage_key", p.existing.StorageKey,
				"check_failed", checkFailed)
			result.ToUpload = append(result.ToUpload, DedupFileEntry{
				FileChange: p.change,
				StorageKey: p.existing.StorageKey,
				IsNew:      false,
			})
			continue
		}

		// OSS object exists (or OSS check was not performed because storage
		// is nil, e.g. in tests). Skip upload and increment ref count.
		if err := d.hashRepo.IncrementRef(p.change.NewHash); err != nil {
			return nil, fmt.Errorf("increment ref for hash %q: %w", p.change.NewHash, err)
		}

		result.Skipped = append(result.Skipped, DedupSkippedEntry{
			Path:               p.change.Path,
			Hash:               p.change.NewHash,
			ExistingStorageKey: p.existing.StorageKey,
			Reason:             fmt.Sprintf("content already stored (hash=%s, ref_count=%d)", truncateHash(p.change.NewHash, 16), p.existing.RefCount+1),
		})
		result.TotalSaved += p.change.Size
	}

	return result, nil
}
