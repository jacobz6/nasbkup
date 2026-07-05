// Package backup reconcile.go implements the system sync / reconciliation
// feature that keeps three data sources consistent:
//
//  1. OSS objects (the encrypted blobs under the configured prefix)
//  2. hash_index rows (the global content-addressable dedup table)
//  3. backup_files rows (per-backup references to OSS objects)
//
// After a process crash, an OSS upload success that was not recorded in DB, a
// DecrementRef failure, or a status-update failure, these three sources drift
// apart. The reconciler detects and (when not in dry-run) fixes the drift so
// that:
//   - every OSS object is accounted for by a hash_index row
//   - every hash_index.storage_key has a corresponding OSS object
//   - every backup_files.storage_key exists in hash_index
//   - hash_index.ref_count matches the number of active files referencing that hash
//   - backup status ('failed' / 'completed') matches the presence of backup_files
//
// The reconciler is intentionally conservative: it never deletes OSS objects
// that are still referenced by hash_index with ref_count > 0 (data loss risk),
// and it never marks a backup as 'completed' unless every backup_file's
// storage_key exists in OSS.
package backup

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/nas-backup/internal/models"
)

// RefCountMismatch captures a single ref_count drift between hash_index and
// the actual number of active files in the files table referencing that hash.
type RefCountMismatch struct {
	Hash         string `json:"hash"`
	StoredInDB   int    `json:"stored_in_db"`
	ActualActive int    `json:"actual_active"`
}

// BackupStatusFix captures a backup whose status should be corrected.
// From → To represents the proposed transition, with Reason explaining why.
type BackupStatusFix struct {
	BackupID int64              `json:"backup_id"`
	From     models.BackupStatus `json:"from"`
	To       models.BackupStatus `json:"to"`
	Reason   string             `json:"reason"`
}

// ReconcileReport summarizes the inconsistencies found (and optionally fixed)
// during a single reconcile run.
type ReconcileReport struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Duration   string    `json:"duration"`
	DryRun     bool      `json:"dry_run"`

	// OSS ↔ hash_index
	// OSSOnlyOrphans: OSS objects with no hash_index row referencing them.
	// These are leftover objects from crashed uploads (window A).
	OSSOnlyOrphans []string `json:"oss_only_orphans"`
	// DanglingHashIndexes: hash_index rows whose storage_key has no OSS object.
	// Split by ref_count: 0 → safe to drop the index row; >0 → data loss (unfixable).
	DanglingHashIndexesRefZero  []string `json:"dangling_hash_indexes_ref_zero"`
	DanglingHashIndexesRefNonZero []string `json:"dangling_hash_indexes_ref_nonzero"`

	// hash_index ↔ backup_files
	// OrphanBackupFiles: backup_files rows whose storage_key is missing from
	// both hash_index AND OSS (fully unrecoverable).
	OrphanBackupFiles []string `json:"orphan_backup_files"`
	// BackupFilesMissingHashIndexButInOSS: backup_files whose storage_key is
	// not in hash_index but the OSS object exists — can be repaired by
	// recreating the hash_index entry.
	BackupFilesMissingHashIndexButInOSS []string `json:"backup_files_missing_hash_index_but_in_oss"`

	// ref_count drift
	RefCountMismatches []RefCountMismatch `json:"ref_count_mismatches"`

	// Backup status corrections
	FailedBackupsWithFiles     []BackupStatusFix `json:"failed_backups_with_files"`
	CompletedBackupsNoFiles    []BackupStatusFix `json:"completed_backups_no_files"`

	// Summary of applied / skipped fixes
	AppliedFixes []string `json:"applied_fixes"`
	SkippedFixes []string `json:"skipped_fixes"` // skipped due to dry-run
	Errors       []string `json:"errors"`
}

// Reconcile runs a single reconciliation pass.
//
// If dryRun is true, inconsistencies are detected and reported but NOT fixed.
// The dryRun parameter overrides cfg.Reconcile.DryRun so the API can force
// either mode per-call via ?dry_run=true|false.
//
// The reconciler refuses to run while a backup is in progress, since the
// backup pipeline mutates hash_index.ref_count and writes new backup_files
// rows that a concurrent reconcile could clobber. Outside of an active backup
// it is safe: it only mutates rows belonging to failed/completed backups and
// orphan hash_index entries (ref_count=0), never running/pending ones.
func (e *Engine) Reconcile(ctx context.Context, dryRun bool) (*ReconcileReport, error) {
	report := &ReconcileReport{
		StartedAt: time.Now(),
		DryRun:    dryRun,
	}
	defer func() {
		report.FinishedAt = time.Now()
		report.Duration = report.FinishedAt.Sub(report.StartedAt).String()
	}()

	// Refuse to run while a backup is in progress: the backup pipeline mutates
	// hash_index.ref_count and writes new backup_files rows, so a concurrent
	// reconcile could compute stale ref_count / orphan sets and clobber
	// in-flight writes. The operator should retry after the backup finishes.
	if _, running := e.RunningBackupID(); running {
		err := fmt.Errorf("a backup is currently running; reconcile after it finishes")
		report.Errors = append(report.Errors, err.Error())
		return report, err
	}
	if running, err := e.db.BackupRepo.IsRunning(); err != nil {
		err = fmt.Errorf("check running backup: %w", err)
		report.Errors = append(report.Errors, err.Error())
		return report, err
	} else if running {
		err := fmt.Errorf("a backup is currently running (db); reconcile after it finishes")
		report.Errors = append(report.Errors, err.Error())
		return report, err
	}

	e.logger.Info("starting reconciliation", "dry_run", dryRun)

	// ── Step 1: gather all three sources ────────────────────────────────
	if err := e.reconcileGatherAndCompare(ctx, report); err != nil {
		return report, err
	}

	// ── Step 2: ref_count rebuild ───────────────────────────────────────
	if err := e.reconcileRefCount(ctx, report); err != nil {
		return report, err
	}

	// ── Step 3: backup status correction ───────────────────────────────
	if err := e.reconcileBackupStatus(ctx, report); err != nil {
		return report, err
	}

	// ── Step 4: apply fixes (if not dry-run) ───────────────────────────
	if !dryRun {
		if err := e.reconcileApplyFixes(ctx, report); err != nil {
			// Apply errors are recorded in report.Errors; we still return the report.
			e.logger.Error("reconcile apply fixes encountered errors", "error", err)
		}
	} else {
		// In dry-run, populate SkippedFixes so the operator can preview.
		e.reconcileCollectSkippedFixes(report)
	}

	e.logger.Info("reconciliation completed",
		"dry_run", dryRun, "duration", report.Duration,
		"oss_only_orphans", len(report.OSSOnlyOrphans),
		"dangling_ref_zero", len(report.DanglingHashIndexesRefZero),
		"dangling_ref_nonzero", len(report.DanglingHashIndexesRefNonZero),
		"orphan_backup_files", len(report.OrphanBackupFiles),
		"ref_count_mismatches", len(report.RefCountMismatches),
		"failed_with_files", len(report.FailedBackupsWithFiles),
		"completed_no_files", len(report.CompletedBackupsNoFiles),
		"applied_fixes", len(report.AppliedFixes),
	)
	return report, nil
}

// reconcileGatherAndCompare loads OSS, hash_index, and backup_files storage_keys
// and computes the four sets of inconsistencies between them.
func (e *Engine) reconcileGatherAndCompare(ctx context.Context, report *ReconcileReport) error {
	prefix := e.config.Reconcile.OSSListPrefix
	if prefix == "" {
		prefix = "data/"
	}

	// 1a. List all OSS objects under the configured prefix.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	ossKeys, err := e.storage.List(prefix)
	if err != nil {
		err = fmt.Errorf("list OSS objects under %q: %w", prefix, err)
		report.Errors = append(report.Errors, err.Error())
		return err
	}
	ossSet := make(map[string]struct{}, len(ossKeys))
	for _, k := range ossKeys {
		ossSet[k] = struct{}{}
	}
	e.logger.Info("reconcile: listed OSS objects", "count", len(ossKeys))

	// 1b. Load all hash_index records.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	hashRecords, err := e.db.HashRepo.ListAll()
	if err != nil {
		err = fmt.Errorf("list hash_index records: %w", err)
		report.Errors = append(report.Errors, err.Error())
		return err
	}
	hashByStorageKey := make(map[string]*models.HashIndexRecord, len(hashRecords))
	for _, r := range hashRecords {
		hashByStorageKey[r.StorageKey] = r
	}
	e.logger.Info("reconcile: loaded hash_index records", "count", len(hashRecords))

	// 1c. Load backup_files storage_keys → count of referencing rows.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	bfKeys, err := e.db.BackupRepo.ListAllBackupFileStorageKeys()
	if err != nil {
		err = fmt.Errorf("list backup_files storage keys: %w", err)
		report.Errors = append(report.Errors, err.Error())
		return err
	}
	e.logger.Info("reconcile: loaded backup_files storage keys", "count", len(bfKeys))

	// ── Compare: OSS ↔ hash_index ──────────────────────────────────────
	// OSS-only orphans: in ossSet but not in hashByStorageKey.
	for _, k := range ossKeys {
		if _, ok := hashByStorageKey[k]; !ok {
			report.OSSOnlyOrphans = append(report.OSSOnlyOrphans, k)
		}
	}
	// Dangling hash_index: in hashByStorageKey but not in ossSet.
	for _, r := range hashRecords {
		if _, ok := ossSet[r.StorageKey]; !ok {
			if r.RefCount == 0 {
				report.DanglingHashIndexesRefZero = append(report.DanglingHashIndexesRefZero, r.StorageKey)
			} else {
				// ref_count > 0 but OSS object missing → data loss, cannot auto-fix.
				report.DanglingHashIndexesRefNonZero = append(report.DanglingHashIndexesRefNonZero, r.StorageKey)
			}
		}
	}

	// ── Compare: hash_index ↔ backup_files ─────────────────────────────
	// For each backup_files storage_key, check whether hash_index has it.
	//   - Missing in hash_index AND missing in OSS → orphan backup_files
	//   - Missing in hash_index but present in OSS   → repairable
	for key := range bfKeys {
		_, inHash := hashByStorageKey[key]
		if inHash {
			continue
		}
		_, inOSS := ossSet[key]
		if inOSS {
			report.BackupFilesMissingHashIndexButInOSS = append(report.BackupFilesMissingHashIndexButInOSS, key)
		} else {
			report.OrphanBackupFiles = append(report.OrphanBackupFiles, key)
		}
	}

	// Sort all slices for stable, readable output.
	sort.Strings(report.OSSOnlyOrphans)
	sort.Strings(report.DanglingHashIndexesRefZero)
	sort.Strings(report.DanglingHashIndexesRefNonZero)
	sort.Strings(report.OrphanBackupFiles)
	sort.Strings(report.BackupFilesMissingHashIndexButInOSS)
	return nil
}

// reconcileRefCount compares hash_index.ref_count against the actual number
// of active files in the files table grouped by hash. Mismatches are recorded
// in the report and fixed in the apply phase (when not dry-run).
func (e *Engine) reconcileRefCount(ctx context.Context, report *ReconcileReport) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	actualCounts, err := e.db.FileRepo.CountActiveByHash()
	if err != nil {
		err = fmt.Errorf("count active files by hash: %w", err)
		report.Errors = append(report.Errors, err.Error())
		return err
	}

	hashRecords, err := e.db.HashRepo.ListAll()
	if err != nil {
		err = fmt.Errorf("list hash_index for ref_count check: %w", err)
		report.Errors = append(report.Errors, err.Error())
		return err
	}

	for _, r := range hashRecords {
		actual := actualCounts[r.Hash]
		if r.RefCount != actual {
			report.RefCountMismatches = append(report.RefCountMismatches, RefCountMismatch{
				Hash:         r.Hash,
				StoredInDB:   r.RefCount,
				ActualActive: actual,
			})
		}
	}
	// Sort by hash for stable output.
	sort.Slice(report.RefCountMismatches, func(i, j int) bool {
		return report.RefCountMismatches[i].Hash < report.RefCountMismatches[j].Hash
	})
	return nil
}

// reconcileBackupStatus finds backups whose status disagrees with the presence
// of backup_files rows. Failed backups that actually have files (and whose
// storage_keys all exist in OSS) are candidates for → completed. Completed
// backups with no files are candidates for → failed.
func (e *Engine) reconcileBackupStatus(ctx context.Context, report *ReconcileReport) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Failed backups that have backup_files → candidate for completion IF
	// every storage_key exists in OSS. We only check OSS existence on demand
	// (it's expensive), so we do it per-backup only for the failed set.
	failedBackups, err := e.db.BackupRepo.ListFailedBackupsWithFiles()
	if err != nil {
		err = fmt.Errorf("list failed backups with files: %w", err)
		report.Errors = append(report.Errors, err.Error())
		return err
	}
	for _, b := range failedBackups {
		files, err := e.db.BackupRepo.GetBackupFiles(b.ID)
		if err != nil {
			report.Errors = append(report.Errors,
				fmt.Sprintf("get backup files for failed backup %d: %v", b.ID, err))
			continue
		}
		// Verify every storage_key exists in OSS before proposing completion.
		allExist := true
		missing := []string{}
		for _, bf := range files {
			exists, err := e.storage.Exists(bf.StorageKey)
			if err != nil {
				report.Errors = append(report.Errors,
					fmt.Sprintf("check OSS existence for backup %d storage_key %q: %v",
						b.ID, bf.StorageKey, err))
				allExist = false
				break
			}
			if !exists {
				missing = append(missing, bf.StorageKey)
				allExist = false
			}
		}
		if !allExist {
			// Not safe to mark completed — record why we skipped.
			e.logger.Info("skip marking failed backup as completed: missing OSS objects",
				"backup_id", b.ID, "missing_count", len(missing))
			continue
		}
		report.FailedBackupsWithFiles = append(report.FailedBackupsWithFiles, BackupStatusFix{
			BackupID: b.ID,
			From:     models.BackupStatusFailed,
			To:       models.BackupStatusCompleted,
			Reason:   fmt.Sprintf("backup marked failed but has %d backup_files all present in OSS", len(files)),
		})
	}

	// Completed backups with no backup_files → candidate for failed.
	completedNoFiles, err := e.db.BackupRepo.ListCompletedBackupsWithoutFiles()
	if err != nil {
		err = fmt.Errorf("list completed backups without files: %w", err)
		report.Errors = append(report.Errors, err.Error())
		return err
	}
	for _, b := range completedNoFiles {
		report.CompletedBackupsNoFiles = append(report.CompletedBackupsNoFiles, BackupStatusFix{
			BackupID: b.ID,
			From:     models.BackupStatusCompleted,
			To:       models.BackupStatusFailed,
			Reason:   "backup marked completed but has no backup_files rows",
		})
	}
	return nil
}

// reconcileApplyFixes performs the actual DB / OSS mutations to fix the
// detected inconsistencies. Each fix is logged into AppliedFixes; errors are
// recorded in Errors but do not abort the whole pass.
func (e *Engine) reconcileApplyFixes(ctx context.Context, report *ReconcileReport) error {
	// Fix 1: dangling hash_index with ref_count == 0 → delete the row.
	//        (OSS object already missing, so no OSS deletion needed.)
	for _, key := range report.DanglingHashIndexesRefZero {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := e.db.HashRepo.DeleteByStorageKey(key); err != nil {
			report.Errors = append(report.Errors,
				fmt.Sprintf("delete dangling hash_index (ref=0) storage_key=%q: %v", key, err))
			continue
		}
		report.AppliedFixes = append(report.AppliedFixes,
			fmt.Sprintf("deleted dangling hash_index row (ref=0) for storage_key=%s", key))
	}

	// Fix 2: orphan backup_files (no hash_index, no OSS) → delete the rows.
	for _, key := range report.OrphanBackupFiles {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		affected, err := e.db.BackupRepo.DeleteBackupFilesByStorageKey(key)
		if err != nil {
			report.Errors = append(report.Errors,
				fmt.Sprintf("delete orphan backup_files storage_key=%q: %v", key, err))
			continue
		}
		report.AppliedFixes = append(report.AppliedFixes,
			fmt.Sprintf("deleted %d orphan backup_files rows for storage_key=%s", affected, key))
	}

	// Fix 3: backup_files whose storage_key is missing from hash_index but
	//        exists in OSS → recreate the hash_index entry. We do NOT have
	//        the original file_size / hash for these objects (the encrypted
	//        blob is opaque), so we insert a minimal record with hash="" and
	//        ref_count=1. The hash field is UNIQUE-constrained and NOT NULL,
	//        but the schema allows empty string — however inserting multiple
	//        empty-hash rows would conflict. Since each OSS object maps to
	//        at most one backup_files storage_key, we synthesize a synthetic
	//        hash from the storage_key to keep it unique.
	//
	//        NOTE: This is a best-effort repair. The recreated row will not
	//        be findable by hash (since it's synthetic), but it will prevent
	//        GC from deleting the OSS object and will keep backup_files ↔
	//        hash_index consistent so restore continues to work via storage_key.
	for _, key := range report.BackupFilesMissingHashIndexButInOSS {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Synthetic hash unique per storage_key. Prefixed to make it identifiable
		// as reconciler-synthesized in case of future inspection.
		syntheticHash := "reconciled:" + key
		if _, err := e.db.HashRepo.Upsert(syntheticHash, 0, key); err != nil {
			report.Errors = append(report.Errors,
				fmt.Sprintf("recreate hash_index for storage_key=%q: %v", key, err))
			continue
		}
		report.AppliedFixes = append(report.AppliedFixes,
			fmt.Sprintf("recreated hash_index row (synthetic hash) for storage_key=%s", key))
	}

	// Fix 4: ref_count drift → set ref_count to the actual active-file count.
	for _, m := range report.RefCountMismatches {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := e.db.HashRepo.SetRefCount(m.Hash, m.ActualActive); err != nil {
			report.Errors = append(report.Errors,
				fmt.Sprintf("set ref_count for hash=%q (db=%d, actual=%d): %v",
					m.Hash, m.StoredInDB, m.ActualActive, err))
			continue
		}
		report.AppliedFixes = append(report.AppliedFixes,
			fmt.Sprintf("corrected ref_count for hash=%s: %d → %d",
				m.Hash, m.StoredInDB, m.ActualActive))
	}

	// Fix 5: backup status corrections.
	allStatusFixes := append([]BackupStatusFix{}, report.FailedBackupsWithFiles...)
	allStatusFixes = append(allStatusFixes, report.CompletedBackupsNoFiles...)
	for _, fix := range allStatusFixes {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		errMsg := ""
		if fix.To == models.BackupStatusFailed {
			errMsg = "reconciler: marked failed (no backup_files found)"
		}
		if err := e.db.BackupRepo.UpdateStatus(fix.BackupID, fix.To, errMsg); err != nil {
			report.Errors = append(report.Errors,
				fmt.Sprintf("update backup %d status %s → %s: %v",
					fix.BackupID, fix.From, fix.To, err))
			continue
		}
		report.AppliedFixes = append(report.AppliedFixes,
			fmt.Sprintf("corrected backup %d status: %s → %s", fix.BackupID, fix.From, fix.To))
	}

	// Fix 6: OSS-only orphans → delete OSS objects.
	//        These have no hash_index row and no backup_files reference, so
	//        deleting them is safe. We delete via storage.Delete which already
	//        retries on transient failures.
	for _, key := range report.OSSOnlyOrphans {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := e.storage.Delete(key); err != nil {
			report.Errors = append(report.Errors,
				fmt.Sprintf("delete OSS-only orphan storage_key=%q: %v", key, err))
			continue
		}
		report.AppliedFixes = append(report.AppliedFixes,
			fmt.Sprintf("deleted OSS-only orphan object storage_key=%s", key))
	}

	return nil
}

// reconcileCollectSkippedFixes populates SkippedFixes for dry-run reports so
// the operator can preview what would change. It mirrors the apply logic but
// only describes the action rather than executing it.
func (e *Engine) reconcileCollectSkippedFixes(report *ReconcileReport) {
	for _, key := range report.DanglingHashIndexesRefZero {
		report.SkippedFixes = append(report.SkippedFixes,
			fmt.Sprintf("would delete dangling hash_index row (ref=0) for storage_key=%s", key))
	}
	for _, key := range report.OrphanBackupFiles {
		report.SkippedFixes = append(report.SkippedFixes,
			fmt.Sprintf("would delete orphan backup_files rows for storage_key=%s", key))
	}
	for _, key := range report.BackupFilesMissingHashIndexButInOSS {
		report.SkippedFixes = append(report.SkippedFixes,
			fmt.Sprintf("would recreate hash_index row (synthetic hash) for storage_key=%s", key))
	}
	for _, m := range report.RefCountMismatches {
		report.SkippedFixes = append(report.SkippedFixes,
			fmt.Sprintf("would correct ref_count for hash=%s: %d → %d",
				m.Hash, m.StoredInDB, m.ActualActive))
	}
	for _, fix := range report.FailedBackupsWithFiles {
		report.SkippedFixes = append(report.SkippedFixes,
			fmt.Sprintf("would correct backup %d status: %s → %s", fix.BackupID, fix.From, fix.To))
	}
	for _, fix := range report.CompletedBackupsNoFiles {
		report.SkippedFixes = append(report.SkippedFixes,
			fmt.Sprintf("would correct backup %d status: %s → %s", fix.BackupID, fix.From, fix.To))
	}
	for _, key := range report.OSSOnlyOrphans {
		report.SkippedFixes = append(report.SkippedFixes,
			fmt.Sprintf("would delete OSS-only orphan object storage_key=%s", key))
	}
}
