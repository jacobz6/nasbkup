// Package compress handles zstd compression with file-type-aware decision
// making. It uses the external zstd binary for actual compression and
// decompression, which allows leveraging the multi-threaded zstd implementation
// (zstdmt) for better performance on large files.
package compress

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nas-backup/internal/models"
)

// Compressor manages zstd compression for backup files.
type Compressor struct {
	enabled      bool
	algorithm    string
	level        int
	skipTypes    map[string]bool
	zstdBin      string        // Path to the zstd binary.
	compressTimeout time.Duration // Timeout for compression operations.
}

// NewCompressor creates a Compressor from the given compression configuration.
// It locates the zstd binary on the system at init time.
func NewCompressor(cfg models.CompressionConfig) *Compressor {
	skipMap := make(map[string]bool, len(cfg.SkipTypes))
	for _, ext := range cfg.SkipTypes {
		// Normalize extensions to lowercase with leading dot.
		e := strings.ToLower(strings.TrimSpace(ext))
		if e != "" && e[0] != '.' {
			e = "." + e
		}
		if e != "" {
			skipMap[e] = true
		}
	}

	c := &Compressor{
		enabled:         cfg.Enabled,
		algorithm:       cfg.Algorithm,
		level:           cfg.Level,
		skipTypes:       skipMap,
		compressTimeout: 30 * time.Minute, // Default 30-minute timeout.
	}

	c.zstdBin = c.FindZstdBinary()
	return c
}

// SetTimeout sets the timeout for compression operations.
func (c *Compressor) SetTimeout(timeout time.Duration) {
	if timeout > 0 {
		c.compressTimeout = timeout
	}
}

// ShouldCompress returns true if the file at filePath should be compressed
// based on its extension and the compression configuration.
func (c *Compressor) ShouldCompress(filePath string) bool {
	if !c.enabled {
		return false
	}

	ext := strings.ToLower(filepath.Ext(filePath))
	if ext == "" {
		return true // No extension — compress by default.
	}

	return !c.skipTypes[ext]
}

// Compress compresses the file at inputPath and writes the result to outputPath.
// It returns the original and compressed sizes. If the file type should not be
// compressed, the input file is copied to the output path verbatim.
//
// The zstd command is executed with a 30-minute timeout.
func (c *Compressor) Compress(inputPath, outputPath string) (originalSize, compressedSize int64, err error) {
	// If this file type should not be compressed, just copy it.
	if !c.ShouldCompress(inputPath) {
		origSize, copyErr := copyFile(inputPath, outputPath)
		return origSize, origSize, copyErr
	}

	// Ensure the zstd binary is available.
	if c.zstdBin == "" {
		return 0, 0, fmt.Errorf("zstd binary not found")
	}

	// Get the original file size.
	origInfo, err := os.Stat(inputPath)
	if err != nil {
		return 0, 0, fmt.Errorf("stat input file %q: %w", inputPath, err)
	}
	originalSize = origInfo.Size()

	// Ensure the output directory exists.
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return 0, 0, fmt.Errorf("create output directory %q: %w", filepath.Dir(outputPath), err)
	}

	// Build the zstd command: zstd -{level} -f -o outputPath inputPath
	levelFlag := fmt.Sprintf("-%d", c.level)
	cmd := exec.Command(c.zstdBin, levelFlag, "-f", "-o", outputPath, inputPath)

	// Set a timeout (configurable via SetTimeout, defaults to 30 minutes).
	timeout := c.compressTimeout
	done := make(chan error, 1)

	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return 0, 0, fmt.Errorf("start zstd: %w", err)
	}

	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			// Clean up partial output.
			os.Remove(outputPath)
			return 0, 0, fmt.Errorf("zstd compression failed: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
		}
	case <-time.After(timeout):
		cmd.Process.Kill()
		os.Remove(outputPath)
		return 0, 0, fmt.Errorf("zstd compression timed out after %v", timeout)
	}

	// Get the compressed file size.
	compInfo, err := os.Stat(outputPath)
	if err != nil {
		return 0, 0, fmt.Errorf("stat compressed file %q: %w", outputPath, err)
	}
	compressedSize = compInfo.Size()

	return originalSize, compressedSize, nil
}

// Decompress decompresses a zstd-compressed file. The input is expected to be
// a valid zstd archive; the output is the decompressed content.
func (c *Compressor) Decompress(inputPath, outputPath string) error {
	if c.zstdBin == "" {
		return fmt.Errorf("zstd binary not found")
	}

	// Ensure the output directory exists.
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("create output directory %q: %w", filepath.Dir(outputPath), err)
	}

	cmd := exec.Command(c.zstdBin, "-d", "-f", "-o", outputPath, inputPath)

	out, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := strings.TrimSpace(string(out))
		return fmt.Errorf("zstd decompression failed: %w (output: %s)", err, outputStr)
	}

	return nil
}

// FindZstdBinary searches for the zstd binary in PATH and in common
// installation locations. Returns the first path found, or an empty string.
func (c *Compressor) FindZstdBinary() string {
	// Check PATH first.
	if path, err := exec.LookPath("zstd"); err == nil {
		return path
	}

	// Common installation locations.
	candidates := []string{
		"/usr/bin/zstd",
		"/usr/local/bin/zstd",
		"/opt/homebrew/bin/zstd",
		"/snap/bin/zstd",
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	return ""
}

// copyFile copies a file from src to dst and returns the number of bytes copied.
func copyFile(src, dst string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return 0, fmt.Errorf("create output directory %q: %w", filepath.Dir(dst), err)
	}

	in, err := os.Open(src)
	if err != nil {
		return 0, fmt.Errorf("open source %q: %w", src, err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return 0, fmt.Errorf("create destination %q: %w", dst, err)
	}
	defer out.Close()

	n, err := io.Copy(out, in)
	if err != nil {
		os.Remove(dst)
		return 0, fmt.Errorf("copy %q → %q: %w", src, dst, err)
	}

	if err := out.Sync(); err != nil {
		return 0, fmt.Errorf("sync destination %q: %w", dst, err)
	}

	return n, nil
}
