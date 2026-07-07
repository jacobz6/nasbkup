package backup

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nas-backup/internal/compress"
	"github.com/nas-backup/internal/config"
	"github.com/nas-backup/internal/crypto"
	"github.com/nas-backup/internal/db"
	"github.com/nas-backup/internal/models"
	"github.com/nas-backup/internal/storage"
)

// maxThawWait is the maximum time to wait for an archived object to be restored.
const maxThawWait = 30 * time.Minute

// thawPollInterval is how often to poll the restore status of an archived object.
const thawPollInterval = 30 * time.Second

// Restorer handles file restoration from backup storage.
type Restorer struct {
	db          *db.Database
	encryptor   *crypto.Encryptor
	compressor  *compress.Compressor
	storage     *storage.StorageManager
	config      *config.AppConfig
	concurrency int // worker count for concurrent restore
}

// RestoreOptions configures a single restore operation.
type RestoreOptions struct {
	// ConflictStrategy controls behavior when the target file already exists.
	// "skip" (default): skip the file, record as failed.
	// "overwrite": delete the existing file and write the restored version.
	// "rename": append a timestamp suffix to avoid collision.
	ConflictStrategy string

	// OnFileProgress is called after each file is processed (success or failure).
	// Enables the caller to relay per-file progress to an SSE broker.
	OnFileProgress models.FileProgressCallback
}

// NewRestorer creates a new Restorer with all required dependencies.
// concurrency controls the worker pool for concurrent file restore; ≤ 0 falls
// back to storage.DefaultBatchConcurrency.
func NewRestorer(database *db.Database, enc *crypto.Encryptor, comp *compress.Compressor,
	stor *storage.StorageManager, cfg *config.AppConfig) *Restorer {
	concurrency := 0
	if cfg != nil {
		concurrency = cfg.Storage.Concurrency
	}
	return &Restorer{
		db:          database,
		encryptor:   enc,
		compressor:  comp,
		storage:     stor,
		config:      cfg,
		concurrency: concurrency,
	}
}

// Restore restores files according to the given request. It downloads, decrypts,
// decompresses (if needed), verifies the hash, and moves each file to the output
// directory.
func (r *Restorer) Restore(ctx context.Context, req *models.RestoreRequest) (*models.RestoreResult, error) {
	return r.RestoreWithOptions(ctx, req, nil)
}

// RestoreWithOptions restores files according to the given request with
// additional options for conflict handling and progress reporting. When opts is
// nil, the behaviour is identical to Restore.
func (r *Restorer) RestoreWithOptions(ctx context.Context, req *models.RestoreRequest, opts *RestoreOptions) (*models.RestoreResult, error) {
	start := time.Now()

	// 1. Query file records matching the request.
	files, err := r.resolveFiles(req)
	if err != nil {
		return nil, fmt.Errorf("resolve files: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no files found matching the request")
	}

	// Ensure output directory exists.
	if err := os.MkdirAll(req.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("create output directory %q: %w", req.OutputDir, err)
	}

	// Create temp directory for intermediate files.
	tmpDir, err := os.MkdirTemp("", "nas-restore-*")
	if err != nil {
		return nil, fmt.Errorf("create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// 2. Compute the common base directory to strip from output paths
	// so that directory structure is preserved under outputDir.
	//
	// For a single file we strip the grandparent directory so the immediate
	// parent dir name is preserved: restoring /data/docs/report.pdf lands at
	// outputDir/docs/report.pdf. Previously filepath.Base() was used which
	// flattened the path to outputDir/report.pdf, losing all directory
	// structure — inconsistent with multi-file restore which preserves the
	// relative structure under the common prefix.
	stripPrefix := ""
	switch len(files) {
	case 1:
		stripPrefix = filepath.Dir(filepath.Dir(files[0].Path))
	default:
		if len(files) > 1 {
			stripPrefix = longestCommonDirPrefix(files)
		}
	}

	// 3. Process each file.
	result := &models.RestoreResult{
		TotalFiles: len(files),
	}

	// Determine conflict strategy from options.
	conflictStrategy := ""
	if opts != nil {
		conflictStrategy = opts.ConflictStrategy
	}

	// onProgress is a convenience closure that calls the callback when set.
	var onProgress models.FileProgressCallback
	if opts != nil && opts.OnFileProgress != nil {
		onProgress = opts.OnFileProgress
	}

	// Process files concurrently using a worker pool. restoreFile's pipeline
	// (thaw → download → decrypt → decompress → verify → move) is self-contained
	// per file, so parallelizing at the file level is safe. Thaw wait times
	// don't consume OSS bandwidth, allowing workers to overlap wait latency
	// with another file's actual download.
	concurrency := r.concurrency
	if concurrency <= 0 {
		concurrency = storage.DefaultBatchConcurrency
	}
	if concurrency > len(files) {
		concurrency = len(files)
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	jobs := make(chan *models.FileRecord, len(files))

	// Worker function: pulls files from the queue and restores them,
	// updating shared result under mutex.
	worker := func() {
		defer wg.Done()
		for fileRec := range jobs {
			if ctx.Err() != nil {
				return
			}

			var restoreErr error

			bfRec, err := r.resolveBackupFile(fileRec.ID, req.BackupID)
			if err != nil {
				slog.Error("resolve backup file record",
					"path", fileRec.Path, "error", err)
				restoreErr = fmt.Errorf("resolve backup file: %w", err)
			} else if bfRec == nil {
				slog.Error("no backup file record found",
					"path", fileRec.Path, "file_id", fileRec.ID)
				restoreErr = fmt.Errorf("no backup file record for file_id %d", fileRec.ID)
			} else {
				restoreErr = r.restoreFile(ctx, fileRec, bfRec, req.OutputDir, stripPrefix, req.Expedited, tmpDir, conflictStrategy)
			}

			if onProgress != nil {
				onProgress(fileRec.Path, fileRec.Size, restoreErr == nil, restoreErr)
			}

			if restoreErr != nil {
				slog.Error("restore file failed",
					"path", fileRec.Path, "error", restoreErr)
				mu.Lock()
				result.FailedFiles = append(result.FailedFiles, fileRec.Path)
				mu.Unlock()
				continue
			}

			mu.Lock()
			result.RestoredFiles++
			result.TotalSize += fileRec.Size
			mu.Unlock()
		}
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go worker()
	}
	for _, fileRec := range files {
		jobs <- fileRec
	}
	close(jobs)
	wg.Wait()

	result.ElapsedMs = time.Since(start).Milliseconds()
	return result, nil
}

// PingStorage verifies OSS connectivity by listing the root of the remote.
// Exposed via GET /api/storage/health for operator diagnostics.
func (r *Restorer) PingStorage(ctx context.Context) error {
	return r.storage.Ping(ctx)
}

// ListRestorableFiles returns file records that can be restored under a given
// directory path. If backupID is provided, only files contained in that backup
// session are returned (joined via backup_files); otherwise all active files
// are considered. If dirPath is empty, all matching files are returned.
func (r *Restorer) ListRestorableFiles(dirPath string, backupID *int64) ([]*models.FileRecord, error) {
	if backupID != nil {
		return r.db.FileRepo.ListActiveByBackup(*backupID, dirPath)
	}
	if dirPath != "" {
		return r.db.FileRepo.ListActiveByDirectory(dirPath)
	}
	return r.db.FileRepo.ListByStatus(models.FileStatusActive, 0, 0)
}

// GetFileInfo returns the file record and backup file record for a specific path.
func (r *Restorer) GetFileInfo(path string) (*models.FileRecord, *models.BackupFileRecord, error) {
	fileRec, err := r.db.FileRepo.GetByPath(path)
	if err != nil {
		return nil, nil, fmt.Errorf("get file record: %w", err)
	}
	if fileRec == nil {
		return nil, nil, fmt.Errorf("file not found: %s", path)
	}

	bfRec, err := r.db.BackupRepo.GetFileRestoreInfo(fileRec.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("get backup file info: %w", err)
	}

	return fileRec, bfRec, nil
}

// ResolveFilesForPreview queries the database for file records matching the
// restore request. It is the public-exported version of resolveFiles, intended
// for use by RestoreJobManager.CreateJob to preview total count/size before
// persisting the job record.
func (r *Restorer) ResolveFilesForPreview(req *models.RestoreRequest) ([]*models.FileRecord, error) {
	return r.resolveFiles(req)
}

// resolveFiles queries the database for file records matching the restore request.
func (r *Restorer) resolveFiles(req *models.RestoreRequest) ([]*models.FileRecord, error) {
	var files []*models.FileRecord

	if len(req.Paths) > 0 {
		for _, path := range req.Paths {
			fileRec, err := r.db.FileRepo.GetByPath(path)
			if err != nil {
				return nil, fmt.Errorf("get file record for %q: %w", path, err)
			}
			if fileRec != nil && fileRec.Status == models.FileStatusActive {
				files = append(files, fileRec)
				continue
			}
			// GetByPath returned nil — the path may be a directory prefix.
			// Try to list all active files under that directory.
			dirFiles, err := r.db.FileRepo.ListActiveByDirectory(path)
			if err != nil {
				return nil, fmt.Errorf("list files under directory %q: %w", path, err)
			}
			files = append(files, dirFiles...)
		}
		return files, nil
	}

	if req.Pattern != "" {
		// List all active files and filter by glob pattern.
		activeFiles, err := r.db.FileRepo.ListByStatus(models.FileStatusActive, 0, 0)
		if err != nil {
			return nil, fmt.Errorf("list active files: %w", err)
		}
		for _, f := range activeFiles {
			matched, matchErr := filepath.Match(req.Pattern, filepath.Base(f.Path))
			if matchErr == nil && matched {
				files = append(files, f)
			}
		}
		return files, nil
	}

	return nil, fmt.Errorf("request must specify paths or pattern")
}

// resolveBackupFile returns the BackupFileRecord for a given file ID, optionally
// filtered by a specific backup ID. When backupID is provided this is a single
// indexed lookup on the (backup_id, file_id) primary key; previously it loaded
// every backup_file row of the session and scanned linearly, which was
// O(N×M) for large restores.
func (r *Restorer) resolveBackupFile(fileID int64, backupID *int64) (*models.BackupFileRecord, error) {
	if backupID != nil {
		return r.db.BackupRepo.GetBackupFileByFileID(*backupID, fileID)
	}
	// Default: get the latest backup file record for this file.
	return r.db.BackupRepo.GetFileRestoreInfo(fileID)
}

// restoreFile handles the complete restore pipeline for a single file:
// thaw (if needed) → download → decrypt → decompress → verify → conflict check → move.
func (r *Restorer) restoreFile(
	ctx context.Context,
	fileRec *models.FileRecord,
	bfRec *models.BackupFileRecord,
	outputDir string,
	stripPrefix string,
	expedited bool,
	tmpDir string,
	conflictStrategy string,
) error {
	downloadedPath := filepath.Join(tmpDir, fmt.Sprintf("%d_download.enc", fileRec.ID))
	decryptedPath := filepath.Join(tmpDir, fmt.Sprintf("%d_decrypted", fileRec.ID))
	decompressedPath := filepath.Join(tmpDir, fmt.Sprintf("%d_final", fileRec.ID))

	defer func() {
		os.Remove(downloadedPath)
		os.Remove(decryptedPath)
		os.Remove(decompressedPath)
	}()

	// Step 0: Validate hash consistency between files.hash and storage_key.
	// The storage_key is built from the content hash (see generateStorageKey),
	// so it MUST contain files.hash. A mismatch indicates the backup record
	// was created by a buggy older version (double-hashing bug) where engine
	// recomputed the hash over compressed data while files.hash stored the
	// scanner's hash of the original file. In that case the hash_index is also
	// inconsistent, and GC may have deleted the OSS object based on the
	// hash_index ref_count while backup_files still references the old key.
	// Failing early with a clear message is far more actionable than letting
	// the downstream CheckRestored/Download return a cryptic 404.
	if fileRec.Hash != "" && bfRec.StorageKey != "" {
		if !strings.Contains(bfRec.StorageKey, fileRec.Hash) {
			return fmt.Errorf("hash inconsistency detected for %q: files.hash=%s but storage_key=%s — "+
				"this backup record was likely created by a buggy older version (double-hashing bug); "+
				"the OSS object may have been deleted by GC. Re-run the backup with the current code to fix",
				fileRec.Path, fileRec.Hash, bfRec.StorageKey)
		}
	}

	// Step 1: Check if object needs thawing (ColdArchive objects).
	restored, err := r.storage.CheckRestored(bfRec.StorageKey)
	if err != nil {
		return fmt.Errorf("check restore status for %q: %w", bfRec.StorageKey, err)
	}
	if !restored {
		// Initiate thaw.
		if err := r.storage.RestoreObject(bfRec.StorageKey, expedited); err != nil {
			return fmt.Errorf("initiate object restore for %q: %w", bfRec.StorageKey, err)
		}
		slog.Info("object thaw initiated, waiting...", "storage_key", bfRec.StorageKey)

		// Wait for thaw with timeout. Use select on ctx.Done() + a timer
		// instead of time.Sleep so that context cancellation is observed
		// promptly (within one poll tick at most). Previously time.Sleep
		// blocked for the full thawPollInterval, delaying cancellation by up
		// to 30s per iteration.
		deadline := time.Now().Add(maxThawWait)
		for time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(thawPollInterval):
			}

			restored, err = r.storage.CheckRestored(bfRec.StorageKey)
			if err != nil {
				slog.Warn("check restore status failed, retrying",
					"storage_key", bfRec.StorageKey, "error", err)
				continue
			}
			if restored {
				break
			}
		}
		if !restored {
			return fmt.Errorf("object %q not restored after %v", bfRec.StorageKey, maxThawWait)
		}
		slog.Info("object thaw completed", "storage_key", bfRec.StorageKey)
	}

	// Step 2: Download encrypted file.
	if err := r.storage.Download(ctx, bfRec.StorageKey, downloadedPath); err != nil {
		return fmt.Errorf("download %q: %w", bfRec.StorageKey, err)
	}

	// Step 3: Decrypt.
	if err := r.encryptor.DecryptFile(downloadedPath, decryptedPath, bfRec.EncryptedIV); err != nil {
		return fmt.Errorf("decrypt file: %w", err)
	}

	// Step 4: Decompress if needed.
	workingPath := decryptedPath
	if bfRec.CompressType == "zstd" {
		if err := r.compressor.Decompress(decryptedPath, decompressedPath); err != nil {
			return fmt.Errorf("decompress file: %w", err)
		}
		workingPath = decompressedPath
	}

	// Step 5: Verify hash.
	if fileRec.Hash != "" {
		actualHash, hashErr := sha256File(workingPath)
		if hashErr != nil {
			slog.Warn("hash verification skipped (could not compute hash)",
				"path", fileRec.Path, "error", hashErr)
		} else if actualHash != fileRec.Hash {
			return fmt.Errorf("hash verification failed for %q: expected %s, got %s",
				fileRec.Path, fileRec.Hash, actualHash)
		}
	}

	// Step 6: Move to output directory, preserving relative directory structure.
	relPath := fileRec.Path
	if stripPrefix != "" {
		relPath = strings.TrimPrefix(fileRec.Path, stripPrefix)
		relPath = strings.TrimPrefix(relPath, string(filepath.Separator))
	} else {
		relPath = filepath.Base(fileRec.Path)
	}
	outputPath := filepath.Join(outputDir, relPath)

	// Step 6a: Handle existing file conflict according to conflictStrategy.
	if conflictStrategy != "" {
		if _, statErr := os.Stat(outputPath); statErr == nil {
			// File already exists at the destination.
			switch conflictStrategy {
			case "skip":
				slog.Info("conflict: skipping existing file",
					"path", fileRec.Path, "output", outputPath)
				return fmt.Errorf("conflict: file already exists and strategy is skip: %s", outputPath)
			case "overwrite":
				if err := os.Remove(outputPath); err != nil {
					return fmt.Errorf("conflict: remove existing file %q: %w", outputPath, err)
				}
				slog.Info("conflict: removed existing file for overwrite",
					"path", fileRec.Path, "output", outputPath)
			case "rename":
				outputPath = fmt.Sprintf("%s.restored_%d", outputPath, time.Now().UnixMilli())
				slog.Info("conflict: renamed to avoid collision",
					"path", fileRec.Path, "new_output", outputPath)
			}
		}
	}

	if err := moveFile(workingPath, outputPath); err != nil {
		return fmt.Errorf("move file to output directory: %w", err)
	}

	slog.Info("file restored", "path", fileRec.Path, "output", outputPath)
	return nil
}

// sha256File computes the SHA-256 hash of a file.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open for hashing: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("read for hashing: %w", err)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// moveFile moves a file from src to dst. It only falls back to copy+delete
// when os.Rename fails with a cross-device link error (EXDEV); other rename
// errors (permission denied, dst already exists, etc.) are returned directly
// so that an existing destination is NOT silently overwritten.
//
// In the copy+delete path, out.Close() errors are checked explicitly (a
// deferred Close would silently drop write-back failures such as disk-full or
// NFS commit errors, leaving a truncated file on disk while the function
// reported success).
func moveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	renameErr := os.Rename(src, dst)
	if renameErr == nil {
		return nil
	}
	// Only fall back to copy+delete for cross-device link errors. Previously
	// ANY rename error triggered the copy fallback, and os.Create(dst) would
	// truncate an already-existing destination file.
	if !errors.Is(renameErr, syscall.EXDEV) {
		return fmt.Errorf("rename %q → %q: %w", src, dst, renameErr)
	}

	// Cross-device rename: copy then delete.
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}

	copyErr := func() error {
		if _, err := io.Copy(out, in); err != nil {
			return fmt.Errorf("copy content: %w", err)
		}
		if err := out.Sync(); err != nil {
			return fmt.Errorf("sync destination: %w", err)
		}
		return nil
	}()

	// Always close the destination; if close fails after a successful copy,
	// treat the whole move as failed and clean up the partial file.
	if cerr := out.Close(); cerr != nil {
		os.Remove(dst)
		if copyErr == nil {
			return fmt.Errorf("close destination: %w", cerr)
		}
	}

	if copyErr != nil {
		os.Remove(dst)
		return copyErr
	}

	os.Remove(src)
	return nil
}

func longestCommonDirPrefix(files []*models.FileRecord) string {
	if len(files) == 0 {
		return ""
	}
	prefix := filepath.Dir(files[0].Path)
	for _, f := range files[1:] {
		dir := filepath.Dir(f.Path)
		for !strings.HasPrefix(dir, prefix+string(filepath.Separator)) && prefix != dir {
			parent := filepath.Dir(prefix)
			if parent == prefix {
				return ""
			}
			prefix = parent
		}
	}
	return prefix
}

// ValidateOutputDir checks that outputDir is under one of the allowed base
// directories. If allowedDirs is empty, no restriction is applied (for backward
// compatibility). Both outputDir and each entry in allowedDirs are cleaned via
// filepath.Clean before comparison.
func ValidateOutputDir(outputDir string, allowedDirs []string) error {
	if len(allowedDirs) == 0 {
		return nil
	}
	cleaned := filepath.Clean(outputDir)
	for _, base := range allowedDirs {
		cleanedBase := filepath.Clean(base)
		if strings.HasPrefix(cleaned, cleanedBase+string(filepath.Separator)) || cleaned == cleanedBase {
			return nil
		}
	}
	return fmt.Errorf("output directory %q is not under any allowed base directories %v", outputDir, allowedDirs)
}
