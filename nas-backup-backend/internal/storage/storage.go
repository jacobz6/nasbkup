// Package storage handles cloud storage operations for backup data via rclone
// and the Alibaba Cloud OSS SDK. It provides upload, download, delete, and
// existence-check operations backed by rclone, and archive restore (thaw)
// operations backed by the OSS SDK.
package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/nas-backup/internal/config"
)

// defaultRetryCount is the default number of retries for rclone operations.
const defaultRetryCount = 3

// defaultRetryBaseDelay is the base delay for exponential backoff between retries.
const defaultRetryBaseDelay = 2 * time.Second

// StorageManager manages all interactions with the cloud backup store.
type StorageManager struct {
	rcloneBin    string // Resolved path to the rclone binary.
	rcloneBinCfg string // User-configured binary path (may be empty or "rclone").
	rcloneConf   string
	remoteName   string
	storageClass string
	ossEndpoint  string
	ossBucket    string
	ossAKID      string
	ossAKSecret  string
}

// validS3StorageClasses lists the storage class values accepted by rclone's
// S3 backend. OSS-native names (ColdArchive, Archive, IA, DeepColdArchive) are
// NOT accepted by the S3-compatible endpoint and trigger InvalidStorageClass.
var validS3StorageClasses = map[string]bool{
	"":                    true, // default — use bucket's storage class
	"STANDARD":            true,
	"REDUCED_REDUNDANCY":  true,
	"STANDARD_IA":         true,
	"ONEZONE_IA":          true,
	"GLACIER":             true,
	"DEEP_ARCHIVE":        true,
	"INTELLIGENT_TIERING": true,
	"GLACIER_IR":          true,
}

// NewStorageManager creates a StorageManager from the application configuration.
// It locates the rclone binary at init time but does not fail if rclone is not found.
// Operations requiring rclone will fail gracefully when invoked.
func NewStorageManager(cfg *config.AppConfig) (*StorageManager, error) {
	storageClass := cfg.OSS.StorageClass
	if storageClass != "" && !validS3StorageClasses[storageClass] {
		fmt.Printf("WARNING: storage_class %q is not a valid S3 storage class and will be ignored. "+
			"Set the storage class on the OSS bucket instead. Valid values: STANDARD, STANDARD_IA, "+
			"GLACIER, DEEP_ARCHIVE, etc.\n", storageClass)
		storageClass = ""
	}

	sm := &StorageManager{
		rcloneBinCfg: cfg.Rclone.BinaryPath,
		rcloneConf:   cfg.Rclone.ConfigPath,
		remoteName:   cfg.Rclone.RemoteName,
		storageClass: storageClass,
		ossEndpoint:  cfg.OSS.Endpoint,
		ossBucket:    cfg.OSS.Bucket,
		ossAKID:      cfg.OSS.AccessKeyID,
		ossAKSecret:  cfg.OSS.AccessKeySecret,
	}

	// Try to find rclone binary, but don't fail if not found.
	sm.rcloneBin = sm.FindRcloneBinary()
	if sm.rcloneBin == "" {
		// Log warning but allow service to start.
		// Operations requiring rclone will fail gracefully.
		fmt.Printf("WARNING: rclone binary not found. Storage operations will be unavailable.\n")
	} else {
		// Verify rclone is runnable and log its version for diagnostics.
		if err := sm.checkRcloneVersion(); err != nil {
			fmt.Printf("WARNING: rclone version check failed: %v. Storage operations may be unavailable.\n", err)
			sm.rcloneBin = ""
		}
	}

	return sm, nil
}

// checkRcloneVersion verifies that the rclone binary is runnable by executing
// `rclone version`. It returns an error if the command fails.
func (sm *StorageManager) checkRcloneVersion() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sm.rcloneBin, "version")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to execute rclone version: %w", err)
	}
	// Parse the first line to verify it looks like a valid version string.
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "rclone ") {
		return fmt.Errorf("unexpected rclone version output: %q", lines[0])
	}
	return nil
}

// EnsureRcloneConfig checks if the rclone configuration file exists; if not, it
// generates one from the application configuration. The generated config contains
// two remotes:
//
//   - [oss]: a raw S3-compatible remote pointing at Alibaba Cloud OSS.
//   - [oss-crypt]: a crypt remote wrapping the raw OSS remote for at-rest
//     encryption. The RemoteName from config determines which remote is used
//     for actual operations.
//
// If the config file already exists, it is validated: when a crypt remote is
// configured, its [oss-crypt] section is checked for the required password /
// password2 fields. If either is missing or empty, the section is patched
// in place without clobbering other sections the user may have added.
func (sm *StorageManager) EnsureRcloneConfig() error {
	if _, err := os.Stat(sm.rcloneConf); err == nil {
		// Config already exists — validate and repair if needed.
		return sm.repairRcloneConfig()
	}
	return sm.generateRcloneConfig()
}

// generateRcloneConfig writes a fresh rclone.conf from the application config.
//
// NOTE: storage_class is intentionally NOT written to the [oss] section here.
// It is passed via the --s3-storage-class command-line flag in Upload() so
// that storage class is controlled solely by config.yaml and changes take
// effect without rewriting rclone.conf. Writing storage_class in both places
// can conflict (rclone.conf value vs. CLI flag) and trigger OSS 400 errors.
func (sm *StorageManager) generateRcloneConfig() error {
	// Ensure the config directory exists.
	if err := os.MkdirAll(filepath.Dir(sm.rcloneConf), 0700); err != nil {
		return fmt.Errorf("create rclone config directory: %w", err)
	}

	rawRemoteName := "oss"

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[%s]\n", rawRemoteName))
	sb.WriteString("type = s3\n")
	sb.WriteString("provider = Alibaba\n")
	sb.WriteString("env_auth = false\n")
	sb.WriteString(fmt.Sprintf("access_key_id = %s\n", sm.ossAKID))
	sb.WriteString(fmt.Sprintf("secret_access_key = %s\n", sm.ossAKSecret))
	sb.WriteString(fmt.Sprintf("endpoint = %s\n", sm.ossEndpoint))
	sb.WriteString("acl = private\n")
	sb.WriteString(fmt.Sprintf("bucket = %s\n", sm.ossBucket))
	sb.WriteString("\n")

	if sm.remoteName != rawRemoteName {
		password, err := sm.obscurePassword(sm.ossAKSecret)
		if err != nil {
			return fmt.Errorf("obscure crypt password: %w", err)
		}
		password2, err := sm.obscurePassword(sm.ossAKSecret + "-content-key")
		if err != nil {
			return fmt.Errorf("obscure crypt content-key password: %w", err)
		}
		sb.WriteString(buildCryptSection(sm.remoteName, rawRemoteName, sm.ossBucket, password, password2))
	}

	if err := os.WriteFile(sm.rcloneConf, []byte(sb.String()), 0600); err != nil {
		return fmt.Errorf("write rclone config to %q: %w", sm.rcloneConf, err)
	}
	return nil
}

// repairRcloneConfig validates an existing rclone.conf and patches the crypt
// remote section if its password / password2 fields are missing or empty.
// This handles the case where a config was generated without passwords
// (or with stale credentials) without clobbering unrelated sections.
// If the configured remote is the raw "oss" remote (no crypt), this is a no-op.
func (sm *StorageManager) repairRcloneConfig() error {
	rawRemoteName := "oss"

	content, err := os.ReadFile(sm.rcloneConf)
	if err != nil {
		return fmt.Errorf("read rclone config: %w", err)
	}
	text := string(content)

	// Always strip any storage_class line from the [oss] section: storage class
	// is now controlled solely via the --s3-storage-class CLI flag in Upload(),
	// and leaving a stale value here can conflict with the flag and trigger
	// OSS 400 BadRequest errors.
	text = stripStorageClass(text)

	// Normalize the crypt section's "remote" field by stripping any quotes
	// around the bucket name. A value like remote = oss:"bucket" causes rclone
	// to send literal quote characters as part of the bucket name, which OSS
	// rejects with 400 BadRequest.
	text = normalizeRemoteField(text, sm.remoteName)

	if sm.remoteName == rawRemoteName {
		// Raw remote only — just persist the cleanups above.
		return os.WriteFile(sm.rcloneConf, []byte(text), 0600)
	}

	sectionStart, sectionEnd, ok := locateSection(text, sm.remoteName)
	if !ok {
		// Crypt section missing entirely — regenerate the whole file.
		return sm.generateRcloneConfig()
	}

	section := text[sectionStart:sectionEnd]
	if fieldValue(section, "password") != "" && fieldValue(section, "password2") != "" {
		// Passwords already present — just persist the cleanups above.
		return os.WriteFile(sm.rcloneConf, []byte(text), 0600)
	}

	password, err := sm.obscurePassword(sm.ossAKSecret)
	if err != nil {
		return fmt.Errorf("obscure crypt password: %w", err)
	}
	password2, err := sm.obscurePassword(sm.ossAKSecret + "-content-key")
	if err != nil {
		return fmt.Errorf("obscure crypt content-key password: %w", err)
	}

	newSection := buildCryptSection(sm.remoteName, rawRemoteName, sm.ossBucket, password, password2)
	newText := text[:sectionStart] + newSection + text[sectionEnd:]

	if err := os.WriteFile(sm.rcloneConf, []byte(newText), 0600); err != nil {
		return fmt.Errorf("write repaired rclone config: %w", err)
	}
	return nil
}

// stripStorageClass removes any "storage_class = ..." line from the [oss]
// section of the rclone config text. Only the first [oss] section is touched.
func stripStorageClass(text string) string {
	start, end, ok := locateSection(text, "oss")
	if !ok {
		return text
	}
	section := text[start:end]
	re := regexp.MustCompile(`(?m)^\s*storage_class\s*=\s*.*$\n?`)
	newSection := re.ReplaceAllString(section, "")
	if newSection == section {
		return text // nothing to strip
	}
	return text[:start] + newSection + text[end:]
}

// normalizeRemoteField strips quotes from the bucket name in the "remote"
// line of the named crypt section. For example:
//
//	remote = oss:"mybucket"   →   remote = oss:mybucket
//	remote = oss:'mybucket'   →   remote = oss:mybucket
//
// Quotes around the bucket name cause rclone to send literal quote characters
// as part of the bucket name to OSS, which rejects them with 400 BadRequest.
// If the section or remote field is absent, text is returned unchanged.
func normalizeRemoteField(text, sectionName string) string {
	start, end, ok := locateSection(text, sectionName)
	if !ok {
		return text
	}
	section := text[start:end]
	// Match: remote = <prefix>:"<bucket>"  or  remote = <prefix>:'<bucket>'
	// Go's regexp (RE2) doesn't support backreferences, so we accept either
	// quote at start and end without requiring them to match.
	re := regexp.MustCompile(`(?m)^(\s*remote\s*=\s*\w+:)["']([^"']+)["'](\s*)$`)
	if !re.MatchString(section) {
		return text
	}
	newSection := re.ReplaceAllString(section, "$1$2$3")
	if newSection == section {
		return text
	}
	return text[:start] + newSection + text[end:]
}

// buildCryptSection returns the text of a [crypt] remote section.
func buildCryptSection(remoteName, rawRemoteName, bucket, password, password2 string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[%s]\n", remoteName))
	sb.WriteString("type = crypt\n")
	sb.WriteString(fmt.Sprintf("remote = %s:%s\n", rawRemoteName, bucket))
	sb.WriteString("filename_encryption = off\n")
	sb.WriteString("directory_name_encryption = false\n")
	sb.WriteString(fmt.Sprintf("password = %s\n", password))
	sb.WriteString(fmt.Sprintf("password2 = %s\n", password2))
	sb.WriteString("\n")
	return sb.String()
}

// locateSection returns the byte offsets of the [name] section in text.
// sectionEnd points to the start of the next [section] header or len(text)
// if this is the last section. Returns ok=false if the section is absent.
func locateSection(text, name string) (sectionStart, sectionEnd int, ok bool) {
	headerRe := regexp.MustCompile(`(?m)^\[` + regexp.QuoteMeta(name) + `\]\s*$`)
	loc := headerRe.FindStringIndex(text)
	if loc == nil {
		return 0, 0, false
	}
	start := loc[0]

	// Find the next section header after the matched header.
	nextRe := regexp.MustCompile(`(?m)^\[[^\]]+\]\s*$`)
	rest := text[loc[1]:]
	nextLoc := nextRe.FindStringIndex(rest)
	if nextLoc == nil {
		return start, len(text), true
	}
	return start, loc[1] + nextLoc[0], true
}

// fieldValue extracts the value of the first "key = value" line in section.
// Returns empty string if not found or value is empty.
func fieldValue(section, key string) string {
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `\s*=\s*(.*?)\s*$`)
	m := re.FindStringSubmatch(section)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// obscurePassword generates an obscured representation of a password for rclone.
// It uses the configured rclone binary to run the obscure command.
// If the rclone binary is not available or the command fails, an error is returned
// to prevent accidentally storing plaintext passwords in the config file.
func (sm *StorageManager) obscurePassword(plain string) (string, error) {
	rcloneBin := sm.rcloneBin
	if rcloneBin == "" {
		rcloneBin = "rclone"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, rcloneBin, "obscure", plain)
	cmd.Stdin = nil
	if output, err := cmd.Output(); err == nil {
		return strings.TrimSpace(string(output)), nil
	}
	// Refuse to fall back to plaintext — this would expose credentials.
	// The caller should propagate this error and abort config generation.
	return "", fmt.Errorf("failed to obscure password: rclone obscure command failed; " +
		"manually run 'rclone obscure' and use the output in the config")
}

// Upload uploads a local file to OSS via rclone. It retries up to 3 times with
// exponential backoff on failure. The context allows cancellation of in-flight
// uploads (e.g. when the backup is cancelled).
//
// On failure the captured stderr includes rclone's verbose log (run with -v)
// so that the underlying OSS error code (e.g. InvalidArgument /
// EntityTooLarge) is preserved in the returned error message instead of just
// "BadRequest: Bad Request".
func (sm *StorageManager) Upload(ctx context.Context, localPath, remoteKey string) error {
	if sm.rcloneBin == "" {
		return fmt.Errorf("rclone binary not found")
	}

	// Capture file size for diagnostics: large-file failures often relate to
	// OSS upload limits / multipart thresholds, and having the size in the
	// error makes triage much faster.
	var fileSize int64 = -1
	if info, err := os.Stat(localPath); err == nil {
		fileSize = info.Size()
	}

	remoteSpec := fmt.Sprintf("%s:%s", sm.remoteName, remoteKey)

	return sm.withRetry(ctx, defaultRetryCount, func() error {
		var args []string
		args = append(args, "copyto", localPath, remoteSpec)
		if sm.storageClass != "" {
			args = append(args, fmt.Sprintf("--s3-storage-class=%s", sm.storageClass))
		}
		args = append(args, "--config", sm.rcloneConf)
		args = append(args, "-v")

		cmd := exec.CommandContext(ctx, sm.rcloneBin, args...)
		var stderr strings.Builder
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("rclone copyto %q → %q (size=%d): %w (stderr: %s)",
				localPath, remoteSpec, fileSize, err,
				strings.TrimSpace(stderr.String()))
		}
		return nil
	})
}

// Download downloads a file from OSS to a local path via rclone. It retries up
// to 3 times on failure. The context allows cancellation of in-flight downloads.
func (sm *StorageManager) Download(ctx context.Context, remoteKey, localPath string) error {
	if sm.rcloneBin == "" {
		return fmt.Errorf("rclone binary not found")
	}

	remoteSpec := fmt.Sprintf("%s:%s", sm.remoteName, remoteKey)

	// Ensure the output directory exists.
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return fmt.Errorf("create download directory: %w", err)
	}

	return sm.withRetry(ctx, defaultRetryCount, func() error {
		cmd := exec.CommandContext(ctx, sm.rcloneBin,
			"copyto", remoteSpec, localPath,
			"--config", sm.rcloneConf,
		)
		var stderr strings.Builder
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("rclone copyto %q → %q: %w (stderr: %s)",
				remoteSpec, localPath, err, strings.TrimSpace(stderr.String()))
		}
		return nil
	})
}

// Delete removes a single file from OSS with retry logic for transient failures.
// The context allows cancellation of in-flight delete operations.
func (sm *StorageManager) Delete(ctx context.Context, remoteKey string) error {
	if sm.rcloneBin == "" {
		return fmt.Errorf("rclone binary not found")
	}
	// Reject directory paths: rclone delete on a directory path would
	// recursively delete the entire directory tree, causing data loss.
	if strings.HasSuffix(remoteKey, "/") {
		return fmt.Errorf("delete rejected: storage_key %q looks like a directory (trailing slash)", remoteKey)
	}

	remoteSpec := fmt.Sprintf("%s:%s", sm.remoteName, remoteKey)
	return sm.withRetry(ctx, defaultRetryCount, func() error {
		cmd := exec.CommandContext(ctx, sm.rcloneBin,
			"delete", remoteSpec,
			"--config", sm.rcloneConf,
		)
		var stderr strings.Builder
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("rclone delete %q: %w (stderr: %s)",
				remoteSpec, err, strings.TrimSpace(stderr.String()))
		}
		return nil
	})
}

// DeleteBatch removes multiple files from OSS. It executes delete operations
// sequentially to avoid rate-limiting issues.
func (sm *StorageManager) DeleteBatch(ctx context.Context, remoteKeys []string) error {
	var errs []string
	for _, key := range remoteKeys {
		if err := sm.Delete(ctx, key); err != nil {
			errs = append(errs, fmt.Sprintf("%q: %v", key, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("batch delete had %d errors: %s", len(errs), strings.Join(errs, "; "))
	}
	return nil
}

// Exists checks whether a file exists in OSS by running `rclone lsjson` with
// --files-only on the exact remote path. This is more reliable than `rclone lsl`
// for existence checks because:
//   - lsjson returns a structured JSON array; an empty array reliably means
//     "not found" regardless of backend type (S3, crypt-wrapped S3, etc.)
//   - It does not rely on matching error message substrings, which vary across
//     backends and rclone versions.
//
// Returns (true, nil) if the object exists, (false, nil) if it doesn't,
// or (false, error) on transport/rclone failures.
func (sm *StorageManager) Exists(ctx context.Context, remoteKey string) (bool, error) {
	if sm.rcloneBin == "" {
		return false, fmt.Errorf("rclone binary not found")
	}

	remoteSpec := fmt.Sprintf("%s:%s", sm.remoteName, remoteKey)
	cmd := exec.CommandContext(ctx, sm.rcloneBin,
		"lsjson", remoteSpec,
		"--config", sm.rcloneConf,
		"--files-only",
	)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		stderrStr := strings.TrimSpace(stderr.String())
		// rclone lsjson returns exit code 3 if the directory/object doesn't exist.
		// Also check common error substrings as a fallback for older rclone versions.
		exitErr, ok := err.(*exec.ExitError)
		if ok && exitErr.ExitCode() == 3 {
			return false, nil
		}
		lowerStderr := strings.ToLower(stderrStr)
		// Use space/quote-bounded " 404" to avoid false matches on substrings
		// like "404 retries exhausted" or "404-policy".
		if strings.Contains(lowerStderr, "not found") ||
			strings.Contains(lowerStderr, "no such file") ||
			strings.Contains(lowerStderr, "doesn't exist") ||
			strings.Contains(lowerStderr, "does not exist") ||
			strings.Contains(lowerStderr, "directory not found") ||
			strings.Contains(lowerStderr, "nosuchkey") ||
			strings.Contains(lowerStderr, "notfound") ||
			strings.Contains(lowerStderr, " 404") ||
			strings.Contains(lowerStderr, " 404\"") {
			return false, nil
		}
		return false, fmt.Errorf("rclone lsjson %q: %w (stderr: %s)",
			remoteSpec, err, stderrStr)
	}

	// Command succeeded — parse JSON to confirm we got an object entry.
	// An empty stdout (or empty JSON array "[]") means the object doesn't exist.
	out := strings.TrimSpace(stdout.String())
	if out == "" || out == "[]" || out == "null" {
		return false, nil
	}

	// Try to parse as a JSON array to confirm there's at least one entry.
	var entries []json.RawMessage
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		// Unparseable output but exit code 0 — treat as exists to be safe
		// (avoid false "missing" when rclone changes output format).
		return true, nil
	}
	return len(entries) > 0, nil
}

// RestoreObject initiates a restore (thaw) request for an archived object.
// For ColdArchive objects, the expedited flag controls the restore speed:
//   - Standard: restore completes in 1-10 hours.
//   - Expedited: restore completes in 1-10 minutes (requires whitelist).
//
// The restore window is set to 7 days.
func (sm *StorageManager) RestoreObject(remoteKey string, expedited bool) error {
	client, err := oss.New(sm.ossEndpoint, sm.ossAKID, sm.ossAKSecret)
	if err != nil {
		return fmt.Errorf("create OSS client: %w", err)
	}

	bucket, err := client.Bucket(sm.ossBucket)
	if err != nil {
		return fmt.Errorf("get OSS bucket %q: %w", sm.ossBucket, err)
	}

	restoreConfig := oss.RestoreConfiguration{
		Days: 7,
	}
	if expedited {
		restoreConfig.Tier = "Expedited"
	} else {
		restoreConfig.Tier = "Standard"
	}

	err = bucket.RestoreObjectDetail(remoteKey, restoreConfig)
	if err != nil {
		// OSS returns a specific error when the object is already restored
		// or a restore is already in progress. This is not a fatal error.
		errMsg := err.Error()
		if strings.Contains(errMsg, "RestoreAlreadyInProgress") {
			return nil
		}
		return fmt.Errorf("restore object %q: %w", remoteKey, err)
	}

	return nil
}

// CheckRestored checks whether an archived object has been restored and is
// ready for download. It returns true if the object is in a restorable state.
func (sm *StorageManager) CheckRestored(remoteKey string) (bool, error) {
	client, err := oss.New(sm.ossEndpoint, sm.ossAKID, sm.ossAKSecret)
	if err != nil {
		return false, fmt.Errorf("create OSS client: %w", err)
	}

	bucket, err := client.Bucket(sm.ossBucket)
	if err != nil {
		return false, fmt.Errorf("get OSS bucket %q: %w", sm.ossBucket, err)
	}

	// GetObjectDetailedMeta returns full metadata including the X-Oss-Restore header.
	meta, err := bucket.GetObjectDetailedMeta(remoteKey)
	if err != nil {
		errMsg := err.Error()
		// Distinguish "object not found" (404 NoSuchKey) from other HEAD errors.
		// A 404 means the object was never uploaded or has been deleted (e.g.
		// by GC based on an inconsistent hash_index). This is a data-integrity
		// problem, NOT a thaw issue — returning the generic "head object" error
		// here caused restore to fail with the misleading message "check
		// restore status failed" instead of pointing at the real problem.
		if strings.Contains(errMsg, "404") || strings.Contains(errMsg, "NoSuchKey") {
			return false, fmt.Errorf("object %q does not exist in OSS (404 NoSuchKey) — it may have been deleted by GC or never uploaded; verify hash_index/backup_files consistency (files.hash vs storage_key)", remoteKey)
		}
		return false, fmt.Errorf("head object %q: %w", remoteKey, err)
	}

	// Check the X-Oss-Restore header.
	restoreHeader := meta.Get("X-Oss-Restore")
	if restoreHeader == "" {
		// No restore header means the object is not archived or not in a
		// storage class that requires restoration.
		return true, nil
	}

	// The header value is like "ongoing-request=true" or "ongoing-request=false, expiry-date=..."
	if strings.Contains(restoreHeader, "ongoing-request=false") {
		return true, nil
	}

	return false, nil
}

// List returns all object keys under the given prefix (e.g. "data/").
// Keys are returned as full paths relative to the bucket root, matching the
// storage_key values stored in hash_index / backup_files.
// Uses `rclone lsf <remote>:<prefix> --recursive` which lists objects only
// (no directory markers). The returned keys are prefixed with prefix so they
// compare directly to DB storage_key values.
// The context allows cancellation of long-running list operations.
func (sm *StorageManager) List(ctx context.Context, prefix string) ([]string, error) {
	if sm.rcloneBin == "" {
		return nil, fmt.Errorf("rclone binary not found")
	}

	// Normalize prefix: ensure trailing slash so path concatenation produces
	// correct keys (e.g. prefix "data" + line "20260705/file.enc" becomes
	// "data/20260705/file.enc" not "data20260705/file.enc").
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	remoteSpec := fmt.Sprintf("%s:%s", sm.remoteName, prefix)
	cmd := exec.CommandContext(ctx, sm.rcloneBin,
		"lsf", remoteSpec,
		"--config", sm.rcloneConf,
		"--recursive",
		"--files-only",
	)

	var (
		stdout strings.Builder
		stderr strings.Builder
	)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("rclone lsf %q: %w (stderr: %s)",
			remoteSpec, err, strings.TrimSpace(stderr.String()))
	}

	var keys []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// rclone lsf returns paths relative to the prefix; prepend the prefix
		// so the keys match storage_key values stored in the database.
		keys = append(keys, prefix+line)
	}
	return keys, nil
}

// GetStorageUsage returns the total storage used under the given prefix in bytes.
// Pass "" to query the entire remote, or a prefix like "data/" to limit to
// backup objects. It runs `rclone size remote:prefix` and parses the output.
// The context allows cancellation of the size query.
func (sm *StorageManager) GetStorageUsage(ctx context.Context, prefix string) (int64, error) {
	if sm.rcloneBin == "" {
		return 0, fmt.Errorf("rclone binary not found")
	}

	// Normalize prefix: ensure trailing slash when non-empty.
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	remoteSpec := fmt.Sprintf("%s:%s", sm.remoteName, prefix)
	cmd := exec.CommandContext(ctx, sm.rcloneBin,
		"size", remoteSpec,
		"--config", sm.rcloneConf,
	)

	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("rclone size: %w", err)
	}

	// Parse the output. rclone size outputs lines like:
	// Total objects: 1234
	// Total size: 5.678 GiB (6102728960 Byte)
	return parseRcloneSize(string(output))
}

// ─── Batch / Metadata Operations ────────────────────────────────────────────
//
// The methods below provide batch and metadata operations on OSS objects.
// They use a worker pool to parallelize rclone invocations, dramatically
// reducing wall-clock time when checking or downloading many objects
// (e.g. during dedup verification or restore of multiple files).
//
// Default concurrency is 8, matching the recommended worker count for
// Alibaba Cloud OSS to stay well under rate-limit thresholds.

// DefaultBatchConcurrency is the default number of parallel rclone workers
// used by ExistsBatch / DownloadBatch. Override per-call with the
// concurrency parameter (must be > 0).
const DefaultBatchConcurrency = 8

// ObjectMeta holds metadata about a single OSS object, returned by HeadObject.
// Used by the reconciler to validate that DB-stored size matches OSS reality.
type ObjectMeta struct {
	Key      string    // Full storage key (e.g. "data/20260705-full/a3/a3f5..e1.enc")
	Size     int64     // Object size in bytes
	ModTime  time.Time // Object last-modified time
	IsDir    bool      // True if the key is a directory marker (rarely true for backup data)
	MimeType string    // MIME type as reported by rclone
}

// ExistsBatch checks the existence of multiple OSS objects concurrently.
// Returns a map of key → exists (true/false) and a slice of per-key errors
// for keys whose check failed (transient rclone errors, network issues, etc.).
//
// When concurrency ≤ 0, DefaultBatchConcurrency is used. The caller should
// pass the configured storage.concurrency value.
//
// The function does NOT short-circuit on individual key errors: it keeps
// checking the remaining keys so a single rclone failure doesn't abort the
// whole batch. Failed keys are absent from the result map and listed in
// errors. The context allows cancellation of the entire batch operation.
func (sm *StorageManager) ExistsBatch(ctx context.Context, keys []string, concurrency int) (map[string]bool, []KeyError, error) {
	if sm.rcloneBin == "" {
		return nil, nil, fmt.Errorf("rclone binary not found")
	}
	if len(keys) == 0 {
		return map[string]bool{}, nil, nil
	}
	if concurrency <= 0 {
		concurrency = DefaultBatchConcurrency
	}

	type result struct {
		key     string
		exists  bool
		err     error
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Buffered channel for results so workers never block.
	results := make(chan result, len(keys))
	// Worker pool: limit concurrent rclone processes to avoid overwhelming
	// the local process table and OSS rate limits.
	jobs := make(chan string, len(keys))

	// Track keys still pending so we can report them as failed on cancellation.
	// Without this, callers would treat missing keys as "exists" — a fail-open
	// window that reintroduces the data-loss bug we are fixing.
	pending := make(map[string]struct{}, len(keys))
	var pendingMu sync.Mutex
	for _, k := range keys {
		pending[k] = struct{}{}
	}

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range jobs {
				// Skip work if context is already cancelled — pending will be
				// reported as failures by the collector.
				if ctx.Err() != nil {
					continue
				}
				exists, err := sm.Exists(ctx, key)

				pendingMu.Lock()
				delete(pending, key)
				pendingMu.Unlock()

				select {
				case results <- result{key: key, exists: exists, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Feed all keys to the worker pool.
	for _, k := range keys {
		jobs <- k
	}
	close(jobs)

	// Wait for all workers to finish, then close results so the collector
	// can terminate.
	go func() {
		wg.Wait()
		close(results)
	}()

	out := make(map[string]bool, len(keys))
	var errs []KeyError
	for r := range results {
		if r.err != nil {
			errs = append(errs, KeyError{Key: r.key, Err: r.err, Message: r.err.Error()})
			continue
		}
		out[r.key] = r.exists
	}

	// Any keys still pending (never processed, or processed after ctx cancel)
	// must be reported as failures. Otherwise dedup would treat them as
	// "OSS exists" and skip the upload.
	pendingMu.Lock()
	for k := range pending {
		errs = append(errs, KeyError{
			Key:     k,
			Err:     context.Canceled,
			Message: "exists check did not complete before context cancellation",
		})
	}
	pendingMu.Unlock()

	return out, errs, nil
}

// KeyError pairs a storage key with the error encountered while operating on
// it. Returned by batch operations so callers can identify which keys failed
// without losing the error context.
type KeyError struct {
	Key string `json:"key"`
	Err error  `json:"-"`
	// Message is Err.Error() captured at construction time, since errors
	// don't serialize to JSON.
	Message string `json:"message"`
}

// HeadObject fetches metadata for a single OSS object. Returns (nil, nil) if
// the object does not exist. Used by the reconciler to validate that the
// stored_size recorded in backup_files matches the actual OSS object size
// (a mismatch indicates silent corruption or partial upload).
//
// Uses `rclone lsjson <remote>:<key> --files-only` which returns a JSON array
// with a single entry when the object exists, or an empty array otherwise.
// The context allows cancellation of the metadata lookup.
func (sm *StorageManager) HeadObject(ctx context.Context, remoteKey string) (*ObjectMeta, error) {
	if sm.rcloneBin == "" {
		return nil, fmt.Errorf("rclone binary not found")
	}

	remoteSpec := fmt.Sprintf("%s:%s", sm.remoteName, remoteKey)
	cmd := exec.CommandContext(ctx, sm.rcloneBin,
		"lsjson", remoteSpec,
		"--config", sm.rcloneConf,
		"--files-only",
	)

	var stdout strings.Builder
	var stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		stderrStr := strings.TrimSpace(stderr.String())
		// Treat "not found" as a non-error nil result.
		if strings.Contains(stderrStr, "not found") ||
			strings.Contains(stderrStr, "object not found") ||
			strings.Contains(stderrStr, "no such file") ||
			strings.Contains(stderrStr, "NoSuchKey") {
			return nil, nil
		}
		return nil, fmt.Errorf("rclone lsjson %q: %w (stderr: %s)",
			remoteSpec, err, stderrStr)
	}

	var entries []struct {
		Path     string `json:"Path"`
		Name     string `json:"Name"`
		Size     int64  `json:"Size"`
		ModTime  string `json:"ModTime"`
		IsDir    bool   `json:"IsDir"`
		MimeType string `json:"MimeType"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &entries); err != nil {
		return nil, fmt.Errorf("parse lsjson output for %q: %w (output: %s)",
			remoteSpec, err, stdout.String())
	}
	if len(entries) == 0 {
		return nil, nil
	}
	e := entries[0]
	mt, _ := time.Parse(time.RFC3339, e.ModTime)
	return &ObjectMeta{
		Key:      remoteKey,
		Size:     e.Size,
		ModTime:  mt,
		IsDir:    e.IsDir,
		MimeType: e.MimeType,
	}, nil
}

// DownloadItem pairs a remote key with its local destination path.
type DownloadItem struct {
	RemoteKey  string
	LocalPath  string
}

// DownloadResult captures the outcome of a single download within a batch.
type DownloadResult struct {
	Item DownloadItem
	Err  error
}

// DownloadBatch downloads multiple objects concurrently using a worker pool.
// Returns a slice of per-item results so the caller can identify which
// downloads failed. Items are NOT retried here — retry is the caller's
// responsibility (the existing Download method already retries each file
// internally, so a failure here means the retries were exhausted).
//
// concurrency ≤ 0 → DefaultBatchConcurrency.
// The context allows cancellation of in-flight downloads.
func (sm *StorageManager) DownloadBatch(ctx context.Context, items []DownloadItem, concurrency int) []DownloadResult {
	results := make([]DownloadResult, len(items))
	if len(items) == 0 {
		return results
	}
	if concurrency <= 0 {
		concurrency = DefaultBatchConcurrency
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)

	for i, item := range items {
		wg.Add(1)
		go func(idx int, it DownloadItem) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[idx] = DownloadResult{Item: it, Err: ctx.Err()}
				return
			}
			// Download already retries internally; we don't add another layer.
			err := sm.Download(ctx, it.RemoteKey, it.LocalPath)
			results[idx] = DownloadResult{Item: it, Err: err}
		}(i, item)
	}
	wg.Wait()
	return results
}

// Ping verifies OSS connectivity by listing the root of the configured remote.
// Returns nil if the remote responds (even if empty); returns an error with
// diagnostic context otherwise. Used by the /api/storage/health endpoint.
//
// This is intentionally cheap: `rclone lsf remote:` with no --recursive flag
// only lists the immediate children, which for OSS is a single bucket listing
// request. The context allows cancellation of the health check.
func (sm *StorageManager) Ping(ctx context.Context) error {
	if sm.rcloneBin == "" {
		return fmt.Errorf("rclone binary not found")
	}
	remoteSpec := fmt.Sprintf("%s:", sm.remoteName)
	cmd := exec.CommandContext(ctx, sm.rcloneBin,
		"lsf", remoteSpec,
		"--config", sm.rcloneConf,
		"--max-depth", "1",
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("rclone lsf %q: %w (stderr: %s)",
			remoteSpec, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// FindRcloneBinary searches for the rclone binary using the configured path,
// then PATH, then common installation locations. Returns the first path found,
// or an empty string.
func (sm *StorageManager) FindRcloneBinary() string {
	// If the config specifies an explicit binary path, try it first.
	if sm.rcloneBinCfg != "" && sm.rcloneBinCfg != "rclone" {
		if _, err := os.Stat(sm.rcloneBinCfg); err == nil {
			return sm.rcloneBinCfg
		}
	}

	// Check PATH.
	if path, err := exec.LookPath("rclone"); err == nil {
		return path
	}

	// Common installation locations.
	candidates := []string{
		"/usr/bin/rclone",
		"/usr/local/bin/rclone",
		"/opt/homebrew/bin/rclone",
		"/snap/bin/rclone",
		"/usr/local/sbin/rclone",
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	return ""
}

// withRetry executes fn up to maxRetries times with exponential backoff.
// It checks ctx.Done() between retries so that cancellation is observed
// promptly instead of sleeping through the full backoff delay.
func (sm *StorageManager) withRetry(ctx context.Context, maxRetries int, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			delay := defaultRetryBaseDelay * time.Duration(1<<uint(attempt-1))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		if err := fn(); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}

// parseRcloneSize extracts the total byte count from rclone size output.
// Expected format: "Total size: X.XXX Unit (NNN Byte)"
var rcloneSizeRe = regexp.MustCompile(`\((\d+)\s+Byte\)`)

func parseRcloneSize(output string) (int64, error) {
	matches := rcloneSizeRe.FindStringSubmatch(output)
	if len(matches) < 2 {
		return 0, fmt.Errorf("could not parse rclone size output: %q", output)
	}
	size, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse size %q: %w", matches[1], err)
	}
	return size, nil
}
