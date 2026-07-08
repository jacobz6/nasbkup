// restore-cli is a standalone command-line tool for testing the backup→restore
// closed-loop: it pulls encrypted objects back from Alibaba Cloud OSS, decrypts,
// decompresses, and verifies the SHA-256 hash — without depending on the HTTP
// service.
//
// It reuses the same Restorer used by the API layer, so any verification here
// exercises the exact same code path as a production restore.
//
// Usage:
//
//	./restore-cli -config config.yaml <command> [flags]
//
// Commands:
//
//	backups                         List recent backup sessions
//	list [dir-path]                 List restorable files (optionally under a directory)
//	info <path>                     Show file record + backup metadata for a path
//	verify <path>                   Verify one file: download → decrypt → decompress → hash check (temp dir, auto-cleaned)
//	verify-dir <dir> [--limit N]    Verify all (or N sampled) files under a directory
//	restore <path> -o <outdir>      Restore a single file to outdir
//	restore-dir <dir> -o <outdir>   Restore all files under a directory to outdir
//	bootstrap [-o <db-path>]       Download latest encrypted DB from OSS, decrypt, and save locally
//	db-backup                       Manually trigger encrypted DB upload to OSS
//
// Common flags:
//
//	--backup-id N   Target a specific backup session (default: latest)
//	--expedited     Use expedited thaw for ColdArchive objects
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nas-backup/internal/backup"
	"github.com/nas-backup/internal/compress"
	"github.com/nas-backup/internal/config"
	"github.com/nas-backup/internal/crypto"
	"github.com/nas-backup/internal/db"
	"github.com/nas-backup/internal/logger"
	"github.com/nas-backup/internal/models"
	"github.com/nas-backup/internal/storage"
)

func main() {
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	backupID := flag.Int64("backup-id", 0, "Target backup session ID (0 = latest completed)")
	expedited := flag.Bool("expedited", false, "Use expedited thaw for ColdArchive objects")
	outDir := flag.String("o", "", "Output directory for restore/restore-dir commands")
	limit := flag.Int("limit", 0, "Max files to verify/restore (0 = all)")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}
	command := args[0]
	rest := args[1:]

	// Load configuration.
	cfg, err := config.Load(*configPath)
	if err != nil {
		fail("load config: %v", err)
	}
	if err := cfg.EnsureDataDirs(); err != nil {
		fail("ensure data dirs: %v", err)
	}
	if err := logger.Init(cfg.Logging.Level, cfg.Logging.FilePath, cfg.Logging.MaxSize, cfg.Logging.MaxFiles); err != nil {
		fail("init logger: %v", err)
	}
	defer logger.Close()

	// The "bootstrap" command recovers the database itself from OSS, so it
	// must be handled before trying to open the local database file.
	if command == "bootstrap" {
		runBootstrap(cfg, *outDir)
		return
	}

	// Open database.
	database, err := db.Open(cfg.Database.Path)
	if err != nil {
		fail("open db: %v", err)
	}
	defer database.Close()

	// Initialize components.
	comp := compress.NewCompressor(cfg.ToModelsCompressionConfig())
	enc, err := crypto.NewEncryptor(cfg.Backup.Encryption.KeyFilePath)
	if err != nil {
		fail("init encryptor: %v", err)
	}
	stor, err := storage.NewStorageManager(cfg)
	if err != nil {
		fail("init storage: %v", err)
	}
	if err := stor.EnsureRcloneConfig(); err != nil {
		fail("ensure rclone config: %v", err)
	}

	restorer := backup.NewRestorer(database, enc, comp, stor, cfg)

	// Resolve effective backup ID.
	var targetBackupID *int64
	if *backupID > 0 {
		targetBackupID = backupID
	}

	switch command {
	case "backups":
		runBackups(database)
	case "list":
		runList(restorer, targetBackupID, rest)
	case "info":
		runInfo(restorer, rest)
	case "verify":
		runVerify(restorer, targetBackupID, *expedited, rest)
	case "verify-dir":
		runVerifyDir(restorer, targetBackupID, *expedited, *limit, rest)
	case "restore":
		runRestore(restorer, targetBackupID, *expedited, *outDir, rest)
	case "restore-dir":
		runRestoreDir(restorer, targetBackupID, *expedited, *outDir, *limit, rest)
	case "bootstrap":
		// Already handled above (before DB open). This case is unreachable.
	case "db-backup":
		runDBBackup(stor, enc, cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprint(os.Stderr, `restore-cli — NAS backup cloud recovery & verification tool

Usage:
  restore-cli -config <config.yaml> <command> [flags]

Commands:
  backups                       List recent backup sessions
  list [dir-path]               List restorable files (optionally under a directory)
  info <path>                   Show file record + backup metadata for a path
  verify <path>                 Verify one file (download→decrypt→decompress→hash check)
  verify-dir <dir> [--limit N]  Verify all (or N sampled) files under a directory
  restore <path> -o <outdir>    Restore a single file to outdir
  restore-dir <dir> -o <outdir> Restore all files under a directory to outdir
  bootstrap [-o <db-path>]       Download latest encrypted DB from OSS and save locally
  db-backup                       Manually trigger encrypted DB upload to OSS

Flags:
  --config       Path to config.yaml (default: config.yaml)
  --backup-id N  Target a specific backup session (default: latest completed)
  --expedited    Use expedited thaw for ColdArchive objects
  -o <dir>       Output directory (for restore/restore-dir)
  --limit N      Max files to process (for verify-dir/restore-dir, 0=all)
`)
}

// ─── commands ───────────────────────────────────────────────────────────────

func runBackups(database *db.Database) {
	backups, _, err := database.BackupRepo.List(20, 0)
	if err != nil {
		fail("list backups: %v", err)
	}
	if len(backups) == 0 {
		fmt.Println("(no backup sessions found)")
		return
	}
	fmt.Printf("%-6s %-12s %-12s %-10s %-12s %-20s\n", "ID", "TYPE", "STATUS", "FILES", "SIZE", "COMPLETED_AT")
	fmt.Println(strings.Repeat("-", 80))
	for _, b := range backups {
		completed := "-"
		if b.CompletedAt != nil {
			completed = b.CompletedAt.Local().Format("2006-01-02 15:04:05")
		}
		fmt.Printf("%-6d %-12s %-12s %-10d %-12s %-20s\n",
			b.ID, b.Type, b.Status, b.TotalFiles, humanSize(b.TotalSize), completed)
	}
}

func runList(restorer *backup.Restorer, backupID *int64, args []string) {
	dirPath := ""
	if len(args) > 0 {
		dirPath = args[0]
	}
	files, _, err := restorer.ListRestorableFiles(dirPath, backupID, "", 0, 0)
	if err != nil {
		fail("list restorable files: %v", err)
	}
	if len(files) == 0 {
		fmt.Println("(no restorable files found)")
		return
	}
	fmt.Printf("%-60s %-12s %-12s %-20s\n", "PATH", "SIZE", "HASH(8)", "MOD_TIME")
	fmt.Println(strings.Repeat("-", 110))
	for _, f := range files {
		hashShort := "-"
		if f.Hash != "" {
			hashShort = f.Hash[:8]
		}
		fmt.Printf("%-60s %-12s %-12s %-20s\n",
			truncate(f.Path, 60), humanSize(f.Size), hashShort,
			f.ModTime.Local().Format("2006-01-02 15:04:05"))
	}
	fmt.Printf("\nTotal: %d files, %s\n", len(files), humanSize(totalSize(files)))
}

func runInfo(restorer *backup.Restorer, args []string) {
	if len(args) == 0 {
		fail("info requires a file path argument")
	}
	path := args[0]
	fileRec, bfRec, err := restorer.GetFileInfo(path)
	if err != nil {
		fail("get file info: %v", err)
	}
	if fileRec == nil {
		fail("file not found: %s", path)
	}
	fmt.Println("── File Record ──────────────────────────────")
	fmt.Printf("  ID:       %d\n", fileRec.ID)
	fmt.Printf("  Path:     %s\n", fileRec.Path)
	fmt.Printf("  Size:     %d bytes (%s)\n", fileRec.Size, humanSize(fileRec.Size))
	fmt.Printf("  Hash:     %s\n", fileRec.Hash)
	fmt.Printf("  Status:   %s\n", fileRec.Status)
	fmt.Printf("  ModTime:  %s\n", fileRec.ModTime.Local().Format(time.RFC3339))

	if bfRec == nil {
		fmt.Println("\n(no backup_file record — file has never been backed up)")
		return
	}
	fmt.Println("\n── Backup File Record ───────────────────────")
	fmt.Printf("  BackupID:     %d\n", bfRec.BackupID)
	fmt.Printf("  StorageKey:   %s\n", bfRec.StorageKey)
	fmt.Printf("  CompressType: %s\n", bfRec.CompressType)
	fmt.Printf("  OriginalSize: %d (%s)\n", bfRec.OriginalSize, humanSize(bfRec.OriginalSize))
	fmt.Printf("  StoredSize:   %d (%s)\n", bfRec.StoredSize, humanSize(bfRec.StoredSize))
	fmt.Printf("  EncryptedIV:  %s\n", bfRec.EncryptedIV)

	fmt.Println("\n── Closed-loop check ────────────────────────")
	fmt.Printf("  Lossless:     %v\n", bfRec.OriginalSize == fileRec.Size)
	ratio := 0.0
	if bfRec.OriginalSize > 0 {
		ratio = float64(bfRec.StoredSize) / float64(bfRec.OriginalSize) * 100
	}
	fmt.Printf("  StorageRatio: %.1f%% of original\n", ratio)
}

func runVerify(restorer *backup.Restorer, backupID *int64, expedited bool, args []string) {
	if len(args) == 0 {
		fail("verify requires a file path argument")
	}
	path := args[0]
	tmpDir, err := os.MkdirTemp("", "restore-cli-verify-*")
	if err != nil {
		fail("create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	fmt.Printf("Verifying %q ...\n", path)
	start := time.Now()
	req := restoreRequest(path, backupID, expedited, tmpDir)
	result, err := restorer.Restore(context.Background(), req)
	elapsed := time.Since(start)

	if err != nil {
		fmt.Printf("  ✗ FAILED: %v\n", err)
		fmt.Printf("  Elapsed: %s\n", elapsed)
		os.Exit(1)
	}
	if len(result.FailedFiles) > 0 {
		fmt.Printf("  ✗ FAILED files: %v\n", result.FailedFiles)
		fmt.Printf("  Restored: %d/%d, Elapsed: %s\n", result.RestoredFiles, result.TotalFiles, elapsed)
		os.Exit(1)
	}
	// Confirm the restored file exists and matches expected size.
	// Restore preserves the immediate parent dir name for a single file
	// (strips the grandparent), so /data/docs/report.pdf lands at
	// tmpDir/docs/report.pdf.
	restoredPath := filepath.Join(tmpDir, filepath.Base(filepath.Dir(path)), filepath.Base(path))
	info, err := os.Stat(restoredPath)
	if err != nil {
		fmt.Printf("  ✗ restored file missing after restore: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  ✓ VERIFIED — hash matched, size=%d bytes (%s)\n", info.Size(), humanSize(info.Size()))
	fmt.Printf("  Elapsed: %s\n", elapsed)
}

func runVerifyDir(restorer *backup.Restorer, backupID *int64, expedited bool, limit int, args []string) {
	if len(args) == 0 {
		fail("verify-dir requires a directory path argument")
	}
	dir := args[0]
	files, _, err := restorer.ListRestorableFiles(dir, backupID, "", 0, 0)
	if err != nil {
		fail("list files: %v", err)
	}
	if len(files) == 0 {
		fmt.Println("(no restorable files found under this directory)")
		return
	}
	if limit > 0 && len(files) > limit {
		fmt.Printf("Sampling %d of %d files (use --limit 0 to verify all)\n", limit, len(files))
		files = files[:limit]
	} else {
		fmt.Printf("Verifying %d files under %q ...\n\n", len(files), dir)
	}

	tmpDir, err := os.MkdirTemp("", "restore-cli-verifydir-*")
	if err != nil {
		fail("create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}

	start := time.Now()
	req := &models.RestoreRequest{
		Paths:     paths,
		BackupID:  backupID,
		OutputDir: tmpDir,
		Expedited: expedited,
	}
	result, err := restorer.Restore(context.Background(), req)
	elapsed := time.Since(start)

	fmt.Println("── Verify Summary ───────────────────────────")
	fmt.Printf("  Total:    %d\n", result.TotalFiles)
	fmt.Printf("  Verified: %d  ✓\n", result.RestoredFiles)
	fmt.Printf("  Failed:   %d  ✗\n", len(result.FailedFiles))
	fmt.Printf("  Size:     %s\n", humanSize(result.TotalSize))
	fmt.Printf("  Elapsed:  %s\n", elapsed)
	if err != nil {
		fmt.Printf("  Error:    %v\n", err)
	}
	if len(result.FailedFiles) > 0 {
		fmt.Println("\n  Failed files:")
		for _, p := range result.FailedFiles {
			fmt.Printf("    ✗ %s\n", p)
		}
		os.Exit(1)
	}
}

func runRestore(restorer *backup.Restorer, backupID *int64, expedited bool, outDir string, args []string) {
	if len(args) == 0 {
		fail("restore requires a file path argument")
	}
	if outDir == "" {
		fail("restore requires -o <output directory>")
	}
	path := args[0]
	fmt.Printf("Restoring %q → %s ...\n", path, outDir)
	start := time.Now()
	req := &models.RestoreRequest{
		Paths:     []string{path},
		BackupID:  backupID,
		OutputDir: outDir,
		Expedited: expedited,
	}
	result, err := restorer.Restore(context.Background(), req)
	elapsed := time.Since(start)
	if err != nil {
		fail("restore: %v", err)
	}
	fmt.Printf("  ✓ Restored %d/%d files (%s) in %s\n",
		result.RestoredFiles, result.TotalFiles, humanSize(result.TotalSize), elapsed)
	if len(result.FailedFiles) > 0 {
		fmt.Printf("  ✗ Failed: %v\n", result.FailedFiles)
		os.Exit(1)
	}
}

func runRestoreDir(restorer *backup.Restorer, backupID *int64, expedited bool, outDir string, limit int, args []string) {
	if len(args) == 0 {
		fail("restore-dir requires a directory path argument")
	}
	if outDir == "" {
		fail("restore-dir requires -o <output directory>")
	}
	dir := args[0]
	files, _, err := restorer.ListRestorableFiles(dir, backupID, "", 0, 0)
	if err != nil {
		fail("list files: %v", err)
	}
	if len(files) == 0 {
		fmt.Println("(no restorable files found under this directory)")
		return
	}
	if limit > 0 && len(files) > limit {
		fmt.Printf("Restoring first %d of %d files (use --limit 0 for all)\n", limit, len(files))
		files = files[:limit]
	} else {
		fmt.Printf("Restoring %d files under %q → %s ...\n", len(files), dir, outDir)
	}

	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}

	start := time.Now()
	req := &models.RestoreRequest{
		Paths:     paths,
		BackupID:  backupID,
		OutputDir: outDir,
		Expedited: expedited,
	}
	result, err := restorer.Restore(context.Background(), req)
	elapsed := time.Since(start)
	if err != nil {
		fail("restore: %v", err)
	}
	fmt.Println("── Restore Summary ──────────────────────────")
	fmt.Printf("  Total:    %d\n", result.TotalFiles)
	fmt.Printf("  Restored: %d  ✓\n", result.RestoredFiles)
	fmt.Printf("  Failed:   %d  ✗\n", len(result.FailedFiles))
	fmt.Printf("  Size:     %s\n", humanSize(result.TotalSize))
	fmt.Printf("  Elapsed:  %s\n", elapsed)
	if len(result.FailedFiles) > 0 {
		fmt.Println("\n  Failed files:")
		for _, p := range result.FailedFiles {
			fmt.Printf("    ✗ %s\n", p)
		}
		os.Exit(1)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// restoreRequest is a small helper to build a single-file RestoreRequest.
func restoreRequest(path string, backupID *int64, expedited bool, outDir string) *models.RestoreRequest {
	return &models.RestoreRequest{
		Paths:     []string{path},
		BackupID:  backupID,
		OutputDir: outDir,
		Expedited: expedited,
	}
}

func fail(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "restore-cli: "+format+"\n", args...)
	os.Exit(1)
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for d := n / unit; d >= unit; d /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func totalSize(files []*models.FileRecord) int64 {
	var total int64
	for _, f := range files {
		total += f.Size
	}
	return total
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// ─── bootstrap & db-backup ────────────────────────────────────────────────

// runBootstrap downloads the latest encrypted database from OSS, decrypts it,
// and saves it to the configured database path (or -o path).
// This is the first step of disaster recovery on a fresh NAS.
func runBootstrap(cfg *config.AppConfig, targetPath string) {
	enc, err := crypto.NewEncryptor(cfg.Backup.Encryption.KeyFilePath)
	if err != nil {
		fail("init encryptor: %v", err)
	}
	stor, err := storage.NewStorageManager(cfg)
	if err != nil {
		fail("init storage: %v", err)
	}
	if err := stor.EnsureRcloneConfig(); err != nil {
		fail("ensure rclone config: %v", err)
	}

	if targetPath == "" {
		targetPath = cfg.Database.Path
	}

	svc := backup.NewDBBackupService(enc, stor, cfg, nil)
	ctx := context.Background()

	// List available versions.
	versions, err := svc.ListVersions(ctx)
	if err != nil {
		fail("list database backup versions: %v", err)
	}
	if len(versions) == 0 {
		fail("no database backups found in OSS")
	}

	fmt.Printf("Available database backup versions:\n")
	for i, v := range versions {
		fmt.Printf("  [%d] %s\n", i+1, filepath.Base(v))
	}

	latest := versions[0]
	fmt.Printf("\nBootstrapping latest version: %s\n", filepath.Base(latest))

	if err := svc.Bootstrap(ctx, latest, targetPath); err != nil {
		fail("bootstrap database: %v", err)
	}

	fmt.Printf("Database restored to: %s\n", targetPath)
	fmt.Println("You can now start the nas-backup service normally.")
}

// runDBBackup manually triggers an encrypted database backup upload to OSS.
func runDBBackup(stor *storage.StorageManager, enc *crypto.Encryptor, cfg *config.AppConfig) {
	svc := backup.NewDBBackupService(enc, stor, cfg, nil)
	ctx := context.Background()

	fmt.Println("Starting database backup to OSS...")
	if err := svc.BackupDatabase(ctx); err != nil {
		fail("database backup: %v", err)
	}

	fmt.Println("Database backup completed successfully.")
}
