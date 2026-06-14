// Package dedup performs content-hash based deduplication. It checks each
// changed file's hash against the global hash index; if the content already
// exists in the backup store the file is skipped (only a reference count is
// incremented), otherwise it is queued for upload.
package dedup

import (
	"fmt"

	"github.com/nas-backup/internal/db"
	"github.com/nas-backup/internal/scanner"
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

// Deduplicator checks file changes against the global hash index.
type Deduplicator struct {
	hashRepo *db.HashRepository
}

// NewDeduplicator creates a Deduplicator backed by the given hash repository.
func NewDeduplicator(hashRepo *db.HashRepository) *Deduplicator {
	return &Deduplicator{
		hashRepo: hashRepo,
	}
}

// Deduplicate processes all Added and Modified changes from a scan result,
// checking each file's hash against the global hash index. Files whose
// content already exists are skipped (with a ref-count increment); new
// content is added to the upload list.
func (d *Deduplicator) Deduplicate(changes []scanner.FileChange) (*DedupResult, error) {
	result := &DedupResult{}

	for _, change := range changes {
		switch change.ChangeType {
		case scanner.Added, scanner.Modified:
			if err := d.processChange(change, result); err != nil {
				return nil, fmt.Errorf("deduplicate %q: %w", change.Path, err)
			}

		case scanner.Deleted, scanner.Unchanged, scanner.Renamed:
			// Deleted files are handled separately by the backup engine.
			// Unchanged and Renamed entries are not part of the dedup pipeline.
			continue
		}
	}

	return result, nil
}

// processChange handles a single Added or Modified file change.
func (d *Deduplicator) processChange(change scanner.FileChange, result *DedupResult) error {
	// A file with no hash cannot be deduplicated — it must be uploaded.
	if change.NewHash == "" {
		result.ToUpload = append(result.ToUpload, DedupFileEntry{
			FileChange: change,
			IsNew:      true,
		})
		return nil
	}

	// Look up the hash in the global index.
	existing, err := d.hashRepo.GetByHash(change.NewHash)
	if err != nil {
		return fmt.Errorf("query hash index for %q: %w", change.NewHash, err)
	}

	if existing != nil {
		// Content already exists — skip upload, increment ref count.
		if err := d.hashRepo.IncrementRef(change.NewHash); err != nil {
			return fmt.Errorf("increment ref for hash %q: %w", change.NewHash, err)
		}

		result.Skipped = append(result.Skipped, DedupSkippedEntry{
			Path:               change.Path,
			Hash:               change.NewHash,
			ExistingStorageKey: existing.StorageKey,
			Reason:             fmt.Sprintf("content already stored (hash=%s, ref_count=%d)", change.NewHash[:16], existing.RefCount+1),
		})
		result.TotalSaved += change.Size
	} else {
		// New content — queue for upload.
		result.ToUpload = append(result.ToUpload, DedupFileEntry{
			FileChange: change,
			IsNew:      true,
		})
	}

	return nil
}
