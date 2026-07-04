// Package scanner performs directory scanning and change detection against the
// file index. It walks enabled backup directories, compares each file against
// the database, and produces a ScanResult containing Added, Modified, Deleted,
// and Unchanged entries.
package scanner

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nas-backup/internal/db"
	"github.com/nas-backup/internal/models"
)

// ChangeType enumerates the kinds of changes a scanner can detect.
type ChangeType int

const (
	Added     ChangeType = iota // File exists on disk but not in the index.
	Modified                    // File exists in the index but mtime or size differs.
	Deleted                     // File exists in the index (active) but not on disk.
	Unchanged                   // File matches the index record exactly.
	Renamed                     // Placeholder for future rename detection.
)

// String returns a human-readable label for the ChangeType.
func (ct ChangeType) String() string {
	switch ct {
	case Added:
		return "added"
	case Modified:
		return "modified"
	case Deleted:
		return "deleted"
	case Unchanged:
		return "unchanged"
	case Renamed:
		return "renamed"
	default:
		return "unknown"
	}
}

// FileChange describes a single detected change for a file path.
type FileChange struct {
	Path       string
	ChangeType ChangeType
	Size       int64
	ModTime    time.Time
	OldHash    string // Hash from the DB (empty for Added).
	NewHash    string // Hash computed by computeHashes (empty until hashed).
	Inode      uint64
}

// ScanResult aggregates all changes discovered during a scan.
type ScanResult struct {
	Changes      []FileChange
	TotalScanned int      // Total files examined on disk.
	TotalActive  int      // Total active files in the DB under enabled directories.
	Errors       []string // Non-fatal errors encountered during scan.
}

// Scanner walks configured directories and detects changes against the file index.
type Scanner struct {
	fileRepo   *db.FileRepository
	configRepo *db.ConfigRepository
}

// NewScanner creates a Scanner backed by the given repositories.
func NewScanner(fileRepo *db.FileRepository, configRepo *db.ConfigRepository) *Scanner {
	return &Scanner{
		fileRepo:   fileRepo,
		configRepo: configRepo,
	}
}

// Scan performs a full scan of all enabled backup directories and returns
// a ScanResult describing every detected change. Hashes for Added/Modified
// files are computed before returning.
func (s *Scanner) Scan() (*ScanResult, error) {
	result, err := s.ScanWithProgress(nil)
	if err != nil {
		return nil, err
	}
	if err := s.ComputeHashes(result, nil); err != nil {
		return nil, fmt.Errorf("compute hashes: %w", err)
	}
	return result, nil
}

// ScanWithProgress performs a full scan with a progress callback that is
// invoked with the total count of files scanned so far. The callback may be
// nil. Note: hash computation is NOT performed here — the caller must call
// ComputeHashes separately (useful when you want progress on hashing too).
func (s *Scanner) ScanWithProgress(progress func(scanned int)) (*ScanResult, error) {
	// 1. Get enabled directories.
	dirs, err := s.configRepo.GetEnabledDirectories()
	if err != nil {
		return nil, fmt.Errorf("get enabled directories: %w", err)
	}
	if len(dirs) == 0 {
		return &ScanResult{}, nil
	}

	// 2. Get enabled exclusion rules.
	exclusions, err := s.configRepo.GetEnabledExclusionRules()
	if err != nil {
		return nil, fmt.Errorf("get enabled exclusion rules: %w", err)
	}

	// 3. Get size limits from config_kv.
	maxFileSize, minFileSize, err := s.getSizeLimits()
	if err != nil {
		return nil, fmt.Errorf("get size limits: %w", err)
	}

	// 4. Pre-load all active file records for enabled directories.
	activeDBFiles := make(map[string]*models.FileRecord)
	for _, dir := range dirs {
		records, err := s.fileRepo.ListActiveByDirectory(dir.Path)
		if err != nil {
			return nil, fmt.Errorf("list active files for directory %q: %w", dir.Path, err)
		}
		for _, rec := range records {
			activeDBFiles[rec.Path] = rec
		}
	}

	// 5. Walk each directory.
	result := &ScanResult{
		TotalActive: len(activeDBFiles),
	}
	scannedPaths := make(map[string]bool)

	for _, dir := range dirs {
		if err := s.walkDirectory(dir.Path, dir.Recursive, exclusions, maxFileSize, minFileSize, activeDBFiles, scannedPaths, result, progress); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("walk directory %q: %v", dir.Path, err))
		}
	}

	// 6. Detect deletions: active DB paths not found during the scan.
	// Note: hash computation is NOT done here; the caller is responsible for
	// calling ComputeHashes separately (with optional progress callback).
	for path, rec := range activeDBFiles {
		if !scannedPaths[path] {
			result.Changes = append(result.Changes, FileChange{
				Path:       path,
				ChangeType: Deleted,
				Size:       rec.Size,
				ModTime:    rec.ModTime,
				OldHash:    rec.Hash,
			})
		}
	}

	return result, nil
}

// getSizeLimits reads size constraints from the config_kv table.
func (s *Scanner) getSizeLimits() (maxFileSize, minFileSize int64, err error) {
	maxStr, err := s.configRepo.Get("scanner.max_file_size")
	if err != nil {
		return 0, 0, fmt.Errorf("get max_file_size: %w", err)
	}
	minStr, err := s.configRepo.Get("scanner.min_file_size")
	if err != nil {
		return 0, 0, fmt.Errorf("get min_file_size: %w", err)
	}

	if maxStr != "" {
		maxFileSize, err = strconv.ParseInt(maxStr, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("parse max_file_size %q: %w", maxStr, err)
		}
	}
	if minStr != "" {
		minFileSize, err = strconv.ParseInt(minStr, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("parse min_file_size %q: %w", minStr, err)
		}
	}
	return maxFileSize, minFileSize, nil
}

// walkDirectory recursively walks a single backup directory, following symlinks,
// and populates the ScanResult with detected changes.
func (s *Scanner) walkDirectory(
	root string,
	recursive bool,
	exclusions []*models.ExclusionRule,
	maxFileSize, minFileSize int64,
	activeDBFiles map[string]*models.FileRecord,
	scannedPaths map[string]bool,
	result *ScanResult,
	progress func(scanned int),
) error {
	// Resolve the root path to follow any top-level symlink.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve symlink for %q: %w", root, err)
	}

	visited := make(map[devIno]bool)
	return s.walkRecursive(resolved, root, recursive, exclusions, maxFileSize, minFileSize, activeDBFiles, scannedPaths, result, visited, progress)
}

// devIno uniquely identifies a filesystem entry by device and inode.
type devIno struct {
	dev uint64
	ino uint64
}

// walkRecursive is the core recursive walker that follows symlinks with
// cycle detection using device+inode pairs.
func (s *Scanner) walkRecursive(
	dir string, // resolved (physical) path
	logicalRoot string, // original configured path (used for constructing display paths)
	recursive bool,
	exclusions []*models.ExclusionRule,
	maxFileSize, minFileSize int64,
	activeDBFiles map[string]*models.FileRecord,
	scannedPaths map[string]bool,
	result *ScanResult,
	visited map[devIno]bool,
	progress func(scanned int),
) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read directory %q: %w", dir, err)
	}

	for _, entry := range entries {
		fullPath := filepath.Join(dir, entry.Name())

		// Check if this entry is a symlink.
		lstat, lerr := os.Lstat(fullPath)
		if lerr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("lstat %q: %v", fullPath, lerr))
			continue
		}

		// Skip symlink files — only follow symlink directories.
		if lstat.Mode()&os.ModeSymlink != 0 {
			if !lstat.IsDir() {
				continue
			}
		}

		// Follow symlinks by using os.Stat instead of os.Lstat.
		info, err := os.Stat(fullPath)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("stat %q: %v", fullPath, err))
			continue
		}

		// Compute display path relative to logical root.
		displayPath := fullPath
		if dir != logicalRoot {
			resolvedRoot, _ := filepath.EvalSymlinks(logicalRoot)
			if resolvedRoot != "" && strings.HasPrefix(fullPath, resolvedRoot) {
				displayPath = filepath.Join(logicalRoot, strings.TrimPrefix(fullPath, resolvedRoot))
			}
		}

		if info.IsDir() {
			if !recursive {
				continue
			}

			stat, ok := info.Sys().(*syscall.Stat_t)
			if ok {
				di := devIno{dev: uint64(stat.Dev), ino: stat.Ino}
				if visited[di] {
					continue
				}
				visited[di] = true
			}

			if err := s.walkRecursive(fullPath, logicalRoot, recursive, exclusions, maxFileSize, minFileSize, activeDBFiles, scannedPaths, result, visited, progress); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("walk subdir %q: %v", fullPath, err))
			}

			if ok {
				stat2, _ := info.Sys().(*syscall.Stat_t)
				if stat2 != nil {
					delete(visited, devIno{dev: uint64(stat2.Dev), ino: stat2.Ino})
				}
			}
			continue
		}

		s.processFile(displayPath, info, exclusions, maxFileSize, minFileSize, activeDBFiles, scannedPaths, result, progress)
	}

	return nil
}

// processFile evaluates a single file against exclusions, size limits, and
// the DB index, appending an appropriate FileChange to the result.
func (s *Scanner) processFile(
	path string,
	info fs.FileInfo,
	exclusions []*models.ExclusionRule,
	maxFileSize, minFileSize int64,
	activeDBFiles map[string]*models.FileRecord,
	scannedPaths map[string]bool,
	result *ScanResult,
	progress func(scanned int),
) {
	result.TotalScanned++
	scannedPaths[path] = true

	if progress != nil {
		progress(result.TotalScanned)
	}

	// Check exclusion rules.
	if s.shouldExclude(path, exclusions) {
		return
	}

	// Check size limits.
	size := info.Size()
	if maxFileSize > 0 && size > maxFileSize {
		return
	}
	if minFileSize > 0 && size < minFileSize {
		return
	}

	// Get inode for the file.
	var inode uint64
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		inode = stat.Ino
	}

	// Compare against DB.
	dbRec, inDB := activeDBFiles[path]
	if !inDB {
		result.Changes = append(result.Changes, FileChange{
			Path:       path,
			ChangeType: Added,
			Size:       size,
			ModTime:    info.ModTime(),
			Inode:      inode,
		})
		return
	}

	// Path exists in DB — check if mtime or size changed.
	mtimeChanged := !info.ModTime().Equal(dbRec.ModTime)
	sizeChanged := size != dbRec.Size

	if mtimeChanged || sizeChanged {
		result.Changes = append(result.Changes, FileChange{
			Path:       path,
			ChangeType: Modified,
			Size:       size,
			ModTime:    info.ModTime(),
			OldHash:    dbRec.Hash,
			Inode:      inode,
		})
		return
	}

	// Unchanged — include in result for bookkeeping but pipeline will skip.
	result.Changes = append(result.Changes, FileChange{
		Path:       path,
		ChangeType: Unchanged,
		Size:       size,
		ModTime:    info.ModTime(),
		OldHash:    dbRec.Hash,
		NewHash:    dbRec.Hash,
		Inode:      inode,
	})
}

// shouldExclude checks whether a file path matches any of the enabled exclusion
// rules. The rule_type determines how the pattern is interpreted:
//
//   - "extension":  matches the file extension (e.g. "*.tmp" matches ".tmp")
//   - "directory":  matches any path component (e.g. "node_modules" matches
//     any directory named "node_modules")
//   - "pattern":    uses filepath.Match against the base filename
//   - "size_exceed": handled separately during size checks; ignored here
func (s *Scanner) shouldExclude(path string, exclusions []*models.ExclusionRule) bool {
	for _, rule := range exclusions {
		if !rule.Enabled {
			continue
		}

		switch rule.RuleType {
		case "extension":
			// Normalize pattern: strip leading "*." if present.
			pat := strings.TrimPrefix(rule.Pattern, "*.")
			pat = strings.TrimPrefix(pat, ".")
			if pat == "" {
				continue
			}
			ext := strings.TrimPrefix(filepath.Ext(path), ".")
			if strings.EqualFold(ext, pat) {
				return true
			}

		case "directory":
			// Check if any path component matches the pattern.
			pattern := rule.Pattern
			for _, component := range strings.Split(filepath.ToSlash(path), "/") {
				matched, _ := filepath.Match(pattern, component)
				if matched {
					return true
				}
			}

		case "pattern":
			matched, err := filepath.Match(rule.Pattern, filepath.Base(path))
			if err == nil && matched {
				return true
			}

		case "size_exceed":
			// Handled in the size-check step; skip here.
		}
	}
	return false
}

// computeHashes computes SHA-256 hashes for all Added and Modified files in
// the scan result. After hashing, if the new hash matches the old hash, the
// change is downgraded to Unchanged (false positive from mtime-only change).
// The progress callback is invoked with the number of files hashed so far.
func (s *Scanner) ComputeHashes(result *ScanResult, progress func(int)) error {
	hashed := 0
	for i := range result.Changes {
		change := &result.Changes[i]

		switch change.ChangeType {
		case Added, Modified:
			if change.NewHash != "" {
				continue
			}
			hash, err := sha256File(change.Path)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("hash %q: %v", change.Path, err))
				continue
			}
			change.NewHash = hash

			if change.ChangeType == Modified && change.OldHash == hash {
				change.ChangeType = Unchanged
			}

		case Deleted, Unchanged, Renamed:
			continue
		}

		hashed++
		if progress != nil {
			progress(hashed)
		}
	}
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
