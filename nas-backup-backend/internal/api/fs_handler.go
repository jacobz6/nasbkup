package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nas-backup/internal/models"
)

// handleFSBrowse browses the file system at the given path.
func (r *Router) handleFSBrowse(w http.ResponseWriter, req *http.Request) {
	path := req.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}

	// Clean the path to prevent directory traversal.
	path = filepath.Clean(path)

	// Reject paths containing ".." after cleaning as an extra safety measure.
	if strings.Contains(path, "..") {
		r.jsonError(w, "invalid path: directory traversal not allowed", http.StatusBadRequest)
		return
	}

	// Read the directory.
	entries, err := os.ReadDir(path)
	if err != nil {
		r.jsonError(w, fmt.Sprintf("read directory %q: %v", path, err), http.StatusBadRequest)
		return
	}

	// Get backup directories and exclusion rules for status computation.
	backupDirs, _ := r.db.ConfigRepo.ListDirectories()
	exclusionRules, _ := r.db.ConfigRepo.GetEnabledExclusionRules()

	// Build a set of backup directory paths for quick lookup.
	backupPathSet := make(map[string]bool)
	for _, dir := range backupDirs {
		if dir.Enabled {
			backupPathSet[dir.Path] = true
		}
	}

	result := &models.FSBrowseResult{
		Path:    path,
		Entries: make([]models.FSEntry, 0, len(entries)),
	}

	// Compute parent path.
	if path != "/" {
		result.ParentPath = filepath.Dir(path)
	}

	for _, entry := range entries {
		fullPath := filepath.Join(path, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		fsEntry := models.FSEntry{
			Name:    entry.Name(),
			Path:    fullPath,
			IsDir:   entry.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format(time.RFC3339),
		}

		// Determine backup status.
		fsEntry.InBackup = r.isPathInBackup(fullPath, entry.IsDir(), backupDirs)
		fsEntry.WillBackup = r.willPathBeBackedUp(fullPath, entry.IsDir(), backupDirs, exclusionRules)

		// For files, check if there's an update.
		if !entry.IsDir() {
			fsEntry.HasUpdate = r.fileHasUpdate(fullPath, info)
		}

		result.Entries = append(result.Entries, fsEntry)
	}

	// Sort: directories first, then files, both alphabetically.
	sort.SliceStable(result.Entries, func(i, j int) bool {
		if result.Entries[i].IsDir != result.Entries[j].IsDir {
			return result.Entries[i].IsDir
		}
		return strings.ToLower(result.Entries[i].Name) < strings.ToLower(result.Entries[j].Name)
	})

	r.jsonResponse(w, result, http.StatusOK)
}

// isPathInBackup checks if a path is covered by any enabled backup directory.
func (r *Router) isPathInBackup(path string, isDir bool, backupDirs []*models.BackupDirectory) bool {
	for _, dir := range backupDirs {
		if !dir.Enabled {
			continue
		}
		// Exact match.
		if path == dir.Path {
			return true
		}
		// Path is under a backup directory.
		if strings.HasPrefix(path, dir.Path+"/") {
			return true
		}
		// For directories, check if a backup directory is under this path.
		if isDir && strings.HasPrefix(dir.Path, path+"/") {
			return true
		}
	}
	return false
}

// willPathBeBackedUp checks if a path will actually be included in the next backup,
// considering both backup directories and exclusion rules.
func (r *Router) willPathBeBackedUp(path string, isDir bool, backupDirs []*models.BackupDirectory, exclusions []*models.ExclusionRule) bool {
	// First, check if the path is under any enabled backup directory.
	covered := false
	for _, dir := range backupDirs {
		if !dir.Enabled {
			continue
		}
		if path == dir.Path || strings.HasPrefix(path, dir.Path+"/") {
			covered = true
			break
		}
	}
	if !covered {
		return false
	}

	// For directories, if covered, they will be backed up (unless excluded).
	if isDir {
		return !r.isPathExcluded(path, exclusions)
	}

	// For files, check exclusion rules.
	return !r.isPathExcluded(path, exclusions)
}

// isPathExcluded checks if a path matches any enabled exclusion rule.
func (r *Router) isPathExcluded(path string, exclusions []*models.ExclusionRule) bool {
	for _, rule := range exclusions {
		if !rule.Enabled {
			continue
		}
		switch rule.RuleType {
		case "extension":
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
			for _, component := range strings.Split(filepath.ToSlash(path), "/") {
				matched, _ := filepath.Match(rule.Pattern, component)
				if matched {
					return true
				}
			}
		case "pattern":
			matched, err := filepath.Match(rule.Pattern, filepath.Base(path))
			if err == nil && matched {
				return true
			}
		}
	}
	return false
}

// fileHasUpdate checks if a file has been modified since the last backup.
func (r *Router) fileHasUpdate(path string, info os.FileInfo) bool {
	rec, err := r.db.FileRepo.GetByPath(path)
	if err != nil || rec == nil {
		// File not in DB — it's new, so it has an "update".
		return true
	}
	// File exists in DB but mod time or size differs.
	return !info.ModTime().Equal(rec.ModTime) || info.Size() != rec.Size
}
