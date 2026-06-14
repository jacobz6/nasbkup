package backup

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
	db         *db.Database
	encryptor  *crypto.Encryptor
	compressor *compress.Compressor
	storage    *storage.StorageManager
	config     *config.AppConfig
}

// NewRestorer creates a new Restorer with all required dependencies.
func NewRestorer(database *db.Database, enc *crypto.Encryptor, comp *compress.Compressor,
	stor *storage.StorageManager, cfg *config.AppConfig) *Restorer {
	return &Restorer{
		db:         database,
		encryptor:  enc,
		compressor: comp,
		storage:    stor,
		config:     cfg,
	}
}

// Restore restores files according to the given request. It downloads, decrypts,
// decompresses (if needed), verifies the hash, and moves each file to the output
// directory.
func (r *Restorer) Restore(ctx context.Context, req *models.RestoreRequest) (*models.RestoreResult, error) {
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

	// 2. Process each file.
	result := &models.RestoreResult{
		TotalFiles: len(files),
	}

	for _, fileRec := range files {
		if ctx.Err() != nil {
			break
		}

		// Get the backup_file record for this file.
		bfRec, err := r.resolveBackupFile(fileRec.ID, req.BackupID)
		if err != nil {
			slog.Error("resolve backup file record",
				"path", fileRec.Path, "error", err)
			result.FailedFiles = append(result.FailedFiles, fileRec.Path)
			continue
		}
		if bfRec == nil {
			slog.Error("no backup file record found",
				"path", fileRec.Path, "file_id", fileRec.ID)
			result.FailedFiles = append(result.FailedFiles, fileRec.Path)
			continue
		}

		if err := r.restoreFile(ctx, fileRec, bfRec, req.OutputDir, req.Expedited, tmpDir); err != nil {
			slog.Error("restore file failed",
				"path", fileRec.Path, "error", err)
			result.FailedFiles = append(result.FailedFiles, fileRec.Path)
			continue
		}

		result.RestoredFiles++
		result.TotalSize += fileRec.Size
	}

	result.ElapsedMs = time.Since(start).Milliseconds()
	return result, nil
}

// ListRestorableFiles returns file records that can be restored under a given
// directory path. If dirPath is empty, all active files are returned.
func (r *Restorer) ListRestorableFiles(dirPath string, backupID *int64) ([]*models.FileRecord, error) {
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
			}
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
// filtered by a specific backup ID.
func (r *Restorer) resolveBackupFile(fileID int64, backupID *int64) (*models.BackupFileRecord, error) {
	if backupID != nil {
		// Get the backup files for the specific backup and find our file.
		bfRecords, err := r.db.BackupRepo.GetBackupFiles(*backupID)
		if err != nil {
			return nil, fmt.Errorf("get backup files for backup %d: %w", *backupID, err)
		}
		for _, bf := range bfRecords {
			if bf.FileID == fileID {
				return bf, nil
			}
		}
		return nil, nil
	}
	// Default: get the latest backup file record for this file.
	return r.db.BackupRepo.GetFileRestoreInfo(fileID)
}

// restoreFile handles the complete restore pipeline for a single file:
// thaw (if needed) → download → decrypt → decompress → verify → move.
func (r *Restorer) restoreFile(
	ctx context.Context,
	fileRec *models.FileRecord,
	bfRec *models.BackupFileRecord,
	outputDir string,
	expedited bool,
	tmpDir string,
) error {
	downloadedPath := filepath.Join(tmpDir, fmt.Sprintf("%d_download.enc", fileRec.ID))
	decryptedPath := filepath.Join(tmpDir, fmt.Sprintf("%d_decrypted", fileRec.ID))
	decompressedPath := filepath.Join(tmpDir, fmt.Sprintf("%d_final", fileRec.ID))

	defer func() {
		os.Remove(downloadedPath)
		os.Remove(decryptedPath)
		os.Remove(decompressedPath)
	}()

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

		// Wait for thaw with timeout.
		deadline := time.Now().Add(maxThawWait)
		for time.Now().Before(deadline) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			time.Sleep(thawPollInterval)

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
	if err := r.storage.Download(bfRec.StorageKey, downloadedPath); err != nil {
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

	// Step 6: Move to output directory.
	outputPath := filepath.Join(outputDir, filepath.Base(fileRec.Path))
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

// moveFile moves a file from src to dst. Falls back to copy+delete
// if the rename fails (e.g. cross-device).
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	// Cross-device rename: copy then delete.
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		os.Remove(dst)
		return fmt.Errorf("copy content: %w", err)
	}

	if err := out.Sync(); err != nil {
		return fmt.Errorf("sync destination: %w", err)
	}

	os.Remove(src)
	return nil
}
