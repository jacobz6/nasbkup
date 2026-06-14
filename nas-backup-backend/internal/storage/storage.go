// Package storage handles cloud storage operations for backup data via rclone
// and the Alibaba Cloud OSS SDK. It provides upload, download, delete, and
// existence-check operations backed by rclone, and archive restore (thaw)
// operations backed by the OSS SDK.
package storage

import (
        "context"
        "fmt"
        "os"
        "os/exec"
        "path/filepath"
        "regexp"
        "strconv"
        "strings"
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

// NewStorageManager creates a StorageManager from the application configuration.
// It locates the rclone binary at init time and verifies it is runnable.
func NewStorageManager(cfg *config.AppConfig) (*StorageManager, error) {
        sm := &StorageManager{
                rcloneBinCfg: cfg.Rclone.BinaryPath,
                rcloneConf:   cfg.Rclone.ConfigPath,
                remoteName:   cfg.Rclone.RemoteName,
                storageClass: cfg.OSS.StorageClass,
                ossEndpoint:  cfg.OSS.Endpoint,
                ossBucket:    cfg.OSS.Bucket,
                ossAKID:      cfg.OSS.AccessKeyID,
                ossAKSecret:  cfg.OSS.AccessKeySecret,
        }

        sm.rcloneBin = sm.FindRcloneBinary()
        if sm.rcloneBin == "" {
                return nil, fmt.Errorf("rclone binary not found in PATH or common installation locations")
        }

        // Verify rclone is runnable and log its version for diagnostics.
        if err := sm.checkRcloneVersion(); err != nil {
                return nil, fmt.Errorf("rclone version check failed: %w", err)
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
func (sm *StorageManager) EnsureRcloneConfig() error {
        if _, err := os.Stat(sm.rcloneConf); err == nil {
                // Config already exists.
                return nil
        }

        // Ensure the config directory exists.
        if err := os.MkdirAll(filepath.Dir(sm.rcloneConf), 0700); err != nil {
                return fmt.Errorf("create rclone config directory: %w", err)
        }

        // Build the rclone config content.
        // The raw OSS remote uses the S3 provider with Alibaba compatibility.
        rawRemoteName := "oss"
        storageClass := sm.storageClass
        if storageClass == "" {
                storageClass = "ColdArchive"
        }

        var sb strings.Builder
        sb.WriteString(fmt.Sprintf("[%s]\n", rawRemoteName))
        sb.WriteString("type = s3\n")
        sb.WriteString("provider = Alibaba\n")
        sb.WriteString("env_auth = false\n")
        sb.WriteString(fmt.Sprintf("access_key_id = %s\n", sm.ossAKID))
        sb.WriteString(fmt.Sprintf("secret_access_key = %s\n", sm.ossAKSecret))
        sb.WriteString(fmt.Sprintf("endpoint = %s\n", sm.ossEndpoint))
        sb.WriteString("acl = private\n")
        sb.WriteString(fmt.Sprintf("storage_class = %s\n", storageClass))
        sb.WriteString(fmt.Sprintf("bucket = %s\n", sm.ossBucket))
        sb.WriteString("\n")

	// If the configured remote name differs from the raw remote, add a crypt
	// remote wrapping the raw OSS remote.
	if sm.remoteName != rawRemoteName {
		// Generate encryption passwords from the OSS secret key for simplicity.
		// In a production setup these should be stored in a secrets manager.
		password, err := obscurePassword(sm.ossAKSecret)
		if err != nil {
			return fmt.Errorf("obscure crypt password: %w", err)
		}
		password2, err := obscurePassword(sm.ossAKSecret + "-content-key")
		if err != nil {
			return fmt.Errorf("obscure crypt content-key password: %w", err)
		}

		sb.WriteString(fmt.Sprintf("[%s]\n", sm.remoteName))
		sb.WriteString("type = crypt\n")
		sb.WriteString(fmt.Sprintf("remote = %s:%s\n", rawRemoteName, sm.ossBucket))
		sb.WriteString("filename_encryption = off\n")
		sb.WriteString("directory_name_encryption = false\n")
		sb.WriteString(fmt.Sprintf("password = %s\n", password))
		sb.WriteString(fmt.Sprintf("password2 = %s\n", password2))
		sb.WriteString("\n")
	}

        if err := os.WriteFile(sm.rcloneConf, []byte(sb.String()), 0600); err != nil {
                return fmt.Errorf("write rclone config to %q: %w", sm.rcloneConf, err)
        }

        return nil
}

// obscurePassword generates an obscured representation of a password for rclone.
// It uses the rclone obscure command to generate the obscured password.
// If the rclone binary is not available or the command fails, an error is returned
// to prevent accidentally storing plaintext passwords in the config file.
func obscurePassword(plain string) (string, error) {
	if path, err := exec.LookPath("rclone"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, "obscure", plain)
		cmd.Stdin = nil // Ensure rclone doesn't try to read from stdin.
		if output, err := cmd.Output(); err == nil {
			return strings.TrimSpace(string(output)), nil
		}
	}
	// Refuse to fall back to plaintext — this would expose credentials.
	// The caller should propagate this error and abort config generation.
	return "", fmt.Errorf("failed to obscure password: rclone binary not found or obscure command failed; " +
		"manually run 'rclone obscure' and use the output in the config")
}

// Upload uploads a local file to OSS via rclone. It retries up to 3 times with
// exponential backoff on failure.
func (sm *StorageManager) Upload(localPath, remoteKey string) error {
        if sm.rcloneBin == "" {
                return fmt.Errorf("rclone binary not found")
        }

        remoteSpec := fmt.Sprintf("%s:%s", sm.remoteName, remoteKey)

        return sm.withRetry(defaultRetryCount, func() error {
                var args []string
                args = append(args, "copyto", localPath, remoteSpec)
                if sm.storageClass != "" {
                        args = append(args, fmt.Sprintf("--s3-storage-class=%s", sm.storageClass))
                }
                args = append(args, "--config", sm.rcloneConf)

                cmd := exec.Command(sm.rcloneBin, args...)
                var stderr strings.Builder
                cmd.Stderr = &stderr

                if err := cmd.Run(); err != nil {
                        return fmt.Errorf("rclone copyto %q → %q: %w (stderr: %s)",
                                localPath, remoteSpec, err, strings.TrimSpace(stderr.String()))
                }
                return nil
        })
}

// Download downloads a file from OSS to a local path via rclone. It retries up
// to 3 times on failure.
func (sm *StorageManager) Download(remoteKey, localPath string) error {
        if sm.rcloneBin == "" {
                return fmt.Errorf("rclone binary not found")
        }

        remoteSpec := fmt.Sprintf("%s:%s", sm.remoteName, remoteKey)

        // Ensure the output directory exists.
        if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
                return fmt.Errorf("create download directory: %w", err)
        }

        return sm.withRetry(defaultRetryCount, func() error {
                cmd := exec.Command(sm.rcloneBin,
                        "copyto", remoteSpec, localPath,
                        "--config", sm.rcloneConf,
                )
                var stderr strings.Builder
                cmd.Stderr = &stderr

                if err := cmd.Run(); err != nil {
                        return fmt.Errorf("rclone copyto %q → %q: %w (stderr: %s)",
                                remoteSpec, localPath, err, strings.TrimSpace(stderr.String()))
                }
                return nil
        })
}

// Delete removes a single file from OSS with retry logic for transient failures.
func (sm *StorageManager) Delete(remoteKey string) error {
        if sm.rcloneBin == "" {
                return fmt.Errorf("rclone binary not found")
        }

        remoteSpec := fmt.Sprintf("%s:%s", sm.remoteName, remoteKey)
        return sm.withRetry(defaultRetryCount, func() error {
                cmd := exec.Command(sm.rcloneBin,
                        "delete", remoteSpec,
                        "--config", sm.rcloneConf,
                )
                var stderr strings.Builder
                cmd.Stderr = &stderr

                if err := cmd.Run(); err != nil {
                        return fmt.Errorf("rclone delete %q: %w (stderr: %s)",
                                remoteSpec, err, strings.TrimSpace(stderr.String()))
                }
                return nil
        })
}

// DeleteBatch removes multiple files from OSS. It executes delete operations
// sequentially to avoid rate-limiting issues.
func (sm *StorageManager) DeleteBatch(remoteKeys []string) error {
        var errs []string
        for _, key := range remoteKeys {
                if err := sm.Delete(key); err != nil {
                        errs = append(errs, fmt.Sprintf("%q: %v", key, err))
                }
        }
        if len(errs) > 0 {
                return fmt.Errorf("batch delete had %d errors: %s", len(errs), strings.Join(errs, "; "))
        }
        return nil
}

// Exists checks whether a file exists in OSS by running `rclone lsl`.
func (sm *StorageManager) Exists(remoteKey string) (bool, error) {
        if sm.rcloneBin == "" {
                return false, fmt.Errorf("rclone binary not found")
        }

        remoteSpec := fmt.Sprintf("%s:%s", sm.remoteName, remoteKey)
        cmd := exec.Command(sm.rcloneBin,
                "lsl", remoteSpec,
                "--config", sm.rcloneConf,
        )

        var stderr strings.Builder
        cmd.Stderr = &stderr

        if err := cmd.Run(); err != nil {
                // rclone lsl returns a non-zero exit code when the file doesn't exist.
                stderrStr := strings.TrimSpace(stderr.String())
                if strings.Contains(stderrStr, "not found") ||
                        strings.Contains(stderrStr, "object not found") ||
                        strings.Contains(stderrStr, "no such file") ||
                        strings.Contains(stderrStr, "NoSuchKey") {
                        return false, nil
                }
                // For other errors (network, auth, etc.), return the real error
                // so the caller can distinguish between "file not found" and
                // "something went wrong checking the file".
                return false, fmt.Errorf("rclone lsl %q: %w (stderr: %s)",
                        remoteSpec, err, stderrStr)
        }
        return true, nil
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

// GetStorageUsage returns the total storage used in the bucket in bytes.
// It runs `rclone size remoteName:` and parses the output.
func (sm *StorageManager) GetStorageUsage() (int64, error) {
        if sm.rcloneBin == "" {
                return 0, fmt.Errorf("rclone binary not found")
        }

        remoteSpec := fmt.Sprintf("%s:", sm.remoteName)
        cmd := exec.Command(sm.rcloneBin,
                "size", remoteSpec,
                "--config", sm.rcloneConf,
        )

        output, err := cmd.Output()
        if err != nil {
                return 0, fmt.Errorf("rclone size: %w", err)
        }

        // Parse the output. rclone size outputs lines like:
        // Total objects: 1234
        // Total size: 5.678 GiB (6102728960 Byte)
        return parseRcloneSize(string(output))
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
func (sm *StorageManager) withRetry(maxRetries int, fn func() error) error {
        var lastErr error
        for attempt := 0; attempt < maxRetries; attempt++ {
                if attempt > 0 {
                        delay := defaultRetryBaseDelay * time.Duration(1<<uint(attempt-1)) // Exponential backoff.
                        time.Sleep(delay)
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
