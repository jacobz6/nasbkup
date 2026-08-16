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
// Aliyun OSS Archive tier typically completes within 1 minute; ColdArchive Standard
// tier takes 2-5 hours; Deep Cold Archive can take 48h. A generous upper bound
// avoids failing a large bulk restore just because one object is slow. Note that
// the per-file thaw loop exits as soon as CheckRestored reports completion, so
// most files will not wait anywhere near this long.
const maxThawWait = 6 * time.Hour

// thawPollInterval is how often to poll the restore status of an archived object.
// 30s keeps HEAD request volume low while still providing reasonable latency
// once the thaw completes.
const thawPollInterval = 30 * time.Second

// projectRestoresSentinel is the value the frontend sends in output_dir to
// request "方案2": restore each file into a fixed folder under the project
// data directory (<data_dir>/restores) preserving the full directory depth of
// the original path. It is resolved to a concrete path at normalization time.
const projectRestoresSentinel = "__project_restores__"

// projectRestoresDir returns the fixed project-level restore directory under the
// backend's data directory (<data_dir>/restores). Used for "方案2": all restored
// files are placed here, preserving the full directory depth of each original
// path. Shares the same location as the cross-platform fallback so the user
// always knows where to look regardless of how a restore was targeted.
func projectRestoresDir(cfg *config.AppConfig) string {
	dataDir := filepath.Join(".", "data")
	if cfg != nil && cfg.Database.Path != "" {
		dataDir = filepath.Dir(cfg.Database.Path)
	}
	if absDir, err := filepath.Abs(dataDir); err == nil {
		dataDir = absDir
	}
	return filepath.Join(dataDir, "restores")
}

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

	// NORMALIZE INPUT: ensure the two forms of "restore to DB original path"
	// always behave identically regardless of which field the caller sets.
	// Semantically:
	//   req.RestoreToOriginal == true        → restore each file to files.Path (from DB)
	//   req.OutputDir      == "__original__" → same meaning (legacy sentinel)
	//   req.OutputDir      == "" + RestoreToOriginal not set → treat as restore-to-original
	//                        because the new UI only offers two restore modes.
	//   req.OutputDir      == "__project_restores__" → 方案2: restore into a fixed
	//                        folder under the project data directory, preserving the
	//                        full directory depth of each original path.
	// Downstream code (MkdirAll, restoreFile target-path selection) keys off
	// OutputDir == "__original__" for the original-path mode, so canonicalize here once.
	if req.RestoreToOriginal || req.OutputDir == "" {
		req.OutputDir = "__original__"
		req.RestoreToOriginal = true
	} else if req.OutputDir == "__original__" {
		req.RestoreToOriginal = true
	} else if req.OutputDir == projectRestoresSentinel {
		req.OutputDir = projectRestoresDir(r.config)
	}

	// 1. Query file records matching the request.
	files, err := r.resolveFiles(req)
	if err != nil {
		return nil, fmt.Errorf("resolve files: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no files found matching the request")
	}

	// Determine if this is a restore-to-original-paths operation.
	restoreToOriginal := req.RestoreToOriginal || req.OutputDir == "__original__"

	// CROSS-PLATFORM FALLBACK: When restoring to original paths, detect whether
	// the target path on the current machine can actually be written. The most
	// common case is a backup created on Linux (paths like /home/user/...)
	// being restored on macOS, where /home is an automount that does not allow
	// user-created subdirectories (mkdir fails with "operation not supported").
	// When detected, redirect all files to a fixed sandboxed directory under
	// the backend's data directory (data/restores/<original_path>), preserving
	// the full directory structure so the user can still find their files.
	// All cross-platform restores land in the same data/restores/ dir, so the
	// user always knows where to look regardless of how many jobs they run.
	if restoreToOriginal && req.FallbackBaseDir == "" && len(files) > 0 {
		if fallbackDir, detected := detectCrossPlatformFallback(files[0].Path, r.config); detected {
			req.FallbackBaseDir = fallbackDir
			slog.Warn("cross-platform restore detected: backup path not writable on current OS, "+
				"redirecting restore to fallback directory",
				"sample_path", files[0].Path,
				"fallback_dir", fallbackDir,
				"file_count", len(files))
		}
	}

	// Ensure output directory exists (skip for original-path restore, which
	// creates per-file directories on the fly).
	if !restoreToOriginal {
		if err := os.MkdirAll(req.OutputDir, 0755); err != nil {
			return nil, fmt.Errorf("create output directory %q: %w", req.OutputDir, err)
		}
	}

	// Create temp directory for intermediate files.
	tmpDir, err := os.MkdirTemp("", "nas-restore-*")
	if err != nil {
		return nil, fmt.Errorf("create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// 2. Process each file.
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
				restoreErr = r.restoreFile(ctx, fileRec, bfRec, req.OutputDir, req.Expedited, tmpDir, conflictStrategy, req.FallbackBaseDir)
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
// If search is non-empty, only files whose path contains the search string
// are returned. Pagination is applied via limit/offset; total is the count
// of all matching rows before pagination.
func (r *Restorer) ListRestorableFiles(dirPath string, backupID *int64, search string, limit, offset int) ([]*models.FileRecord, int64, error) {
	return r.db.FileRepo.SearchActiveFiles(db.SearchActiveFilesParams{
		DirPath:  dirPath,
		BackupID: backupID,
		Search:   search,
		Limit:    limit,
		Offset:   offset,
	})
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
// fallbackBaseDir, when non-empty, is prepended to the original file path
// (for restore-to-original mode only) when the original path cannot be written
// on the current OS — see detectCrossPlatformFallback for details.
func (r *Restorer) restoreFile(
	ctx context.Context,
	fileRec *models.FileRecord,
	bfRec *models.BackupFileRecord,
	outputDir string,
	expedited bool,
	tmpDir string,
	conflictStrategy string,
	fallbackBaseDir string,
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
	// First attempt: download directly (thaw was already handled above).
	downloadErr := r.storage.Download(ctx, bfRec.StorageKey, downloadedPath)
	if downloadErr != nil {
		// If the download failed because the object is in GLACIER and we
		// somehow missed the thaw (e.g. CheckRestored returned a false
		// negative), retry once with a full thaw cycle. This is a safety
		// net for edge cases like OSS metadata caching or race conditions
		// where the storage class changed between CheckRestored and Download.
		errMsg := downloadErr.Error()
		if strings.Contains(errMsg, "GLACIER") || strings.Contains(errMsg, "restore first") {
			slog.Warn("download failed with GLACIER error despite thaw check, retrying with full thaw",
				"storage_key", bfRec.StorageKey, "error", downloadErr)

			if thawErr := r.storage.RestoreObject(bfRec.StorageKey, expedited); thawErr != nil {
				return fmt.Errorf("download %q (GLACIER, thaw failed): %w", bfRec.StorageKey, thawErr)
			}

			deadline := time.Now().Add(maxThawWait)
			for time.Now().Before(deadline) {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(thawPollInterval):
				}

				var checkErr error
				restored, checkErr = r.storage.CheckRestored(bfRec.StorageKey)
				if checkErr != nil {
					slog.Warn("check restore status failed during retry",
						"storage_key", bfRec.StorageKey, "error", checkErr)
					continue
				}
				if restored {
					break
				}
			}

			if !restored {
				return fmt.Errorf("object %q not restored after %v (retry)", bfRec.StorageKey, maxThawWait)
			}

			// Second attempt: download after thaw.
			if err := r.storage.Download(ctx, bfRec.StorageKey, downloadedPath); err != nil {
				return fmt.Errorf("download %q (after thaw retry): %w", bfRec.StorageKey, err)
			}
		} else {
			return fmt.Errorf("download %q: %w", bfRec.StorageKey, downloadErr)
		}
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
	var outputPath string
	if outputDir == "__original__" {
		// Restore to the file's original absolute path. When the cross-platform
		// fallback is active, prepend the fallback base so the file lands under
		// <fallback>/home/user/file.jpg instead of failing on /home/user.
		if fallbackBaseDir != "" {
			outputPath = filepath.Join(fallbackBaseDir, fileRec.Path)
		} else {
			outputPath = fileRec.Path
		}
	} else {
		// 方案2 (project restores) or legacy custom dir: preserve the full
		// directory depth of the original path under outputDir, so a file
		// backed up at /home/user/docs/a.txt lands at
		// <outputDir>/home/user/docs/a.txt. Leading path separators are
		// stripped so the original absolute path becomes a relative path.
		relPath := strings.TrimLeft(fileRec.Path, `/\`)
		outputPath = filepath.Join(outputDir, relPath)
	}

	// Ensure the parent directory exists (needed for both original-path and
	// custom-dir restore, since the target dir may not exist yet).
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("create output parent directory for %q: %w", outputPath, err)
	}

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

// detectCrossPlatformFallback tests whether the parent directory of the given
// original backup path can be created on the current machine. When a backup is
// restored on a different OS (e.g. Linux backups → macOS), certain mount points
// (like /home on macOS automount) silently reject mkdir with "operation not
// supported". When that happens this function returns a fixed fallback directory
// under the backend's data directory so the restore can still succeed.
//
// All cross-platform restores land in the SAME directory:
//   <backend_data_dir>/restores/<original_path>
// This keeps the UI/UX simple — the user always looks in one place for their
// restored files, regardless of how many restore jobs they've triggered.
//
// Parameters:
//   - samplePath: one of the original backup paths (e.g. "/home/user/photos/IMG.jpg")
//   - cfg:        app config (used to derive the data directory from the DB path)
//
// Returns:
//   - fallbackDir: sandboxed directory where files should be restored instead
//   - detected:    true when a writable-path issue was found and fallback was set
func detectCrossPlatformFallback(samplePath string, cfg *config.AppConfig) (string, bool) {
	// Try to create the parent directory of the original path. If it fails
	// with a permission/operation error, we know we need the fallback.
	parentDir := filepath.Dir(samplePath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		// Determine the backend data directory from the SQLite DB path.
		dataDir := filepath.Join(".", "data")
		if cfg != nil && cfg.Database.Path != "" {
			dataDir = filepath.Dir(cfg.Database.Path)
		}
		// Resolve to absolute path so fallback works regardless of CWD.
		if absDir, err := filepath.Abs(dataDir); err == nil {
			dataDir = absDir
		}
		// Fixed fallback directory — all cross-platform restores land here.
		fallbackDir := filepath.Join(dataDir, "restores")
		if err := os.MkdirAll(fallbackDir, 0755); err != nil {
			slog.Error("failed to create cross-platform fallback directory",
				"fallback_dir", fallbackDir, "error", err)
			return "", false
		}
		return fallbackDir, true
	}
	// Directory creation succeeded — no cross-platform issue. Clean up the
	// empty directory we just created (best-effort; non-fatal on failure).
	os.Remove(parentDir)
	return "", false
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
