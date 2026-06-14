package compress

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nas-backup/internal/models"
)

// TestNewCompressor 测试压缩器创建和默认配置
func TestNewCompressor(t *testing.T) {
	cfg := testConfig(true, 3, []string{".zip", ".gz"})
	c := NewCompressor(cfg)

	if !c.enabled {
		t.Fatal("compressor should be enabled")
	}
	if c.algorithm != "zstd" {
		t.Errorf("expected algorithm 'zstd', got %q", c.algorithm)
	}
	if c.level != 3 {
		t.Errorf("expected level 3, got %d", c.level)
	}
	if !c.skipTypes[".zip"] {
		t.Fatal(".zip should be in skip types")
	}
	if !c.skipTypes[".gz"] {
		t.Fatal(".gz should be in skip types")
	}
}

// TestNewCompressorDisabled 测试禁用压缩的配置
func TestNewCompressorDisabled(t *testing.T) {
	cfg := testConfig(false, 1, nil)
	c := NewCompressor(cfg)

	if c.enabled {
		t.Fatal("compressor should be disabled")
	}
}

// TestNewCompressorSkipTypeNormalization 测试跳过类型的规范化处理
func TestNewCompressorSkipTypeNormalization(t *testing.T) {
	cfg := testConfig(true, 1, []string{"mp3", ".FLAC", "zip"})
	c := NewCompressor(cfg)

	// 所有跳过类型应该被规范化为小写且以 . 开头
	expected := map[string]bool{
		".mp3":  true,
		".flac": true,
		".zip":  true,
	}
	for ext := range expected {
		if !c.skipTypes[ext] {
			t.Errorf("skip type %q not found in normalized map", ext)
		}
	}
}

// TestShouldCompress 测试压缩决策逻辑
func TestShouldCompress(t *testing.T) {
	skipTypes := []string{".mp4", ".jpg", ".zip", ".gz"}
	cfg := testConfig(true, 3, skipTypes)
	c := NewCompressor(cfg)

	tests := []struct {
		path     string
		expected bool
	}{
		{"file.txt", true},          // 可压缩类型
		{"file.mp4", false},         // 已压缩
		{"file.jpg", false},         // 已压缩（小写）
		{"file.png", false},         // 已压缩（小写）
		{"noextension", true},       // 无扩展名默认压缩
		{"file.doc", true},          // 可压缩
		{"file.zip", false},         // 已压缩
		{"file.tar.gz", false},      // .gz 结尾
	}

	for _, tt := range tests {
		got := c.ShouldCompress(tt.path)
		if got != tt.expected {
			t.Errorf("ShouldCompress(%q) = %v, want %v", tt.path, got, tt.expected)
		}
	}
}

// TestShouldCompressDisabled 测试压缩禁用时永远返回 false
func TestShouldCompressDisabled(t *testing.T) {
	cfg := testConfig(false, 3, []string{".zip"})
	c := NewCompressor(cfg)

	if c.ShouldCompress("file.txt") {
		t.Fatal("ShouldCompress should return false when compressor is disabled")
	}
}

// TestCompressAndDecompress 测试完整的压缩和解压缩流程
func TestCompressAndDecompress(t *testing.T) {
	skipTypes := []string{}
	cfg := testConfig(true, 3, skipTypes)
	c := NewCompressor(cfg)

	if c.zstdBin == "" {
		t.Skip("zstd binary not found, skipping compression test")
	}

	tmpDir := t.TempDir()

	// 创建可压缩的测试数据（大量重复内容）
	original := make([]byte, 10000)
	for i := range original {
		original[i] = byte(i % 10) // 高度可压缩
	}
	plainPath := filepath.Join(tmpDir, "original.txt")
	if err := os.WriteFile(plainPath, original, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// 压缩
	compressedPath := filepath.Join(tmpDir, "compressed.zst")
	origSize, compSize, err := c.Compress(plainPath, compressedPath)
	if err != nil {
		t.Fatalf("Compress failed: %v", err)
	}
	if origSize != int64(len(original)) {
		t.Errorf("origSize = %d, want %d", origSize, len(original))
	}
	if compSize == 0 {
		t.Fatal("compressed size is 0")
	}
	if compSize >= origSize {
		t.Logf("warning: compressed size %d >= original %d (data is not very compressible)", compSize, origSize)
	}

	// 解压
	decompressedPath := filepath.Join(tmpDir, "decompressed.txt")
	if err := c.Decompress(compressedPath, decompressedPath); err != nil {
		t.Fatalf("Decompress failed: %v", err)
	}

	// 验证内容一致
	decompressed, err := os.ReadFile(decompressedPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(original) != string(decompressed) {
		t.Fatal("decompressed content does not match original")
	}
}

// TestCompressSkippedType 测试已压缩类型的跳过逻辑
func TestCompressSkippedType(t *testing.T) {
	cfg := testConfig(true, 3, []string{".mp4"})
	c := NewCompressor(cfg)

	tmpDir := t.TempDir()

	original := []byte("fake mp4 data")
	plainPath := filepath.Join(tmpDir, "video.mp4")
	if err := os.WriteFile(plainPath, original, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	outputPath := filepath.Join(tmpDir, "output.zst")
	origSize, compSize, err := c.Compress(plainPath, outputPath)
	if err != nil {
		t.Fatalf("Compress failed: %v", err)
	}
	if origSize != compSize {
		t.Errorf("for skipped type, origSize %d should equal compSize %d", origSize, compSize)
	}

	// 输出文件应该是原始内容的副本
	output, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(output) != string(original) {
		t.Fatal("output should be identical copy for skipped types")
	}
}

// TestCompressNotFound 测试压缩不存在的文件
func TestCompressNotFound(t *testing.T) {
	cfg := testConfig(true, 3, nil)
	c := NewCompressor(cfg)

	_, _, err := c.Compress("/nonexistent/file.txt", "/tmp/output.zst")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

// TestDecompressNotFound 测试解压不存在的文件
func TestDecompressNotFound(t *testing.T) {
	cfg := testConfig(true, 3, nil)
	c := NewCompressor(cfg)

	err := c.Decompress("/nonexistent/file.zst", "/tmp/output.txt")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

// TestCompressLargeFile 测试大文件压缩
func TestCompressLargeFile(t *testing.T) {
	cfg := testConfig(true, 3, nil)
	c := NewCompressor(cfg)

	if c.zstdBin == "" {
		t.Skip("zstd binary not found, skipping large file test")
	}

	tmpDir := t.TempDir()

	// 创建 1MB 高度可压缩的数据
	original := make([]byte, 1024*1024)
	for i := range original {
		original[i] = 'A'
	}
	plainPath := filepath.Join(tmpDir, "large.txt")
	if err := os.WriteFile(plainPath, original, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	compressedPath := filepath.Join(tmpDir, "large.zst")
	origSize, compSize, err := c.Compress(plainPath, compressedPath)
	if err != nil {
		t.Fatalf("Compress failed: %v", err)
	}

	// 全 A 数据应该能被大幅压缩
	if compSize >= origSize/2 {
		t.Errorf("compression ratio is poor: original %d, compressed %d", origSize, compSize)
	}

	// 解压并验证
	decompressedPath := filepath.Join(tmpDir, "large_decompressed.txt")
	if err := c.Decompress(compressedPath, decompressedPath); err != nil {
		t.Fatalf("Decompress failed: %v", err)
	}

	decompressed, err := os.ReadFile(decompressedPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(original) != string(decompressed) {
		t.Fatal("decompressed large file does not match original")
	}
}

// TestCompressBinaryData 测试二进制数据压缩
func TestCompressBinaryData(t *testing.T) {
	cfg := testConfig(true, 3, nil)
	c := NewCompressor(cfg)

	if c.zstdBin == "" {
		t.Skip("zstd binary not found, skipping binary data test")
	}

	tmpDir := t.TempDir()

	// 创建包含各种字节值的二进制数据
	original := make([]byte, 5000)
	for i := range original {
		original[i] = byte(i % 256)
	}
	plainPath := filepath.Join(tmpDir, "binary.bin")
	if err := os.WriteFile(plainPath, original, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	compressedPath := filepath.Join(tmpDir, "binary.zst")
	_, _, err := c.Compress(plainPath, compressedPath)
	if err != nil {
		t.Fatalf("Compress failed: %v", err)
	}

	decompressedPath := filepath.Join(tmpDir, "binary_decompressed.bin")
	if err := c.Decompress(compressedPath, decompressedPath); err != nil {
		t.Fatalf("Decompress failed: %v", err)
	}

	decompressed, err := os.ReadFile(decompressedPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(original) != string(decompressed) {
		t.Fatal("decompressed binary data does not match original")
	}
}

// TestCompressMultipleLevels 测试不同压缩级别
func TestCompressMultipleLevels(t *testing.T) {
	if _, err := os.Stat("/usr/local/bin/zstd"); os.IsNotExist(err) {
		t.Skip("zstd not found at /usr/local/bin/zstd")
	}

	original := []byte("repeat repeat repeat repeat repeat repeat")
	levels := []int{1, 3, 10, 19}

	for _, level := range levels {
		t.Run("", func(t *testing.T) {
			cfg := testConfig(true, level, nil)
			c := NewCompressor(cfg)

			if c.zstdBin == "" {
				t.Skip("zstd binary not found")
			}

			tmpDir := t.TempDir()
			plainPath := filepath.Join(tmpDir, "plain.txt")
			if err := os.WriteFile(plainPath, original, 0644); err != nil {
				t.Fatalf("WriteFile failed: %v", err)
			}

			compressedPath := filepath.Join(tmpDir, "compressed.zst")
			_, compSize, err := c.Compress(plainPath, compressedPath)
			if err != nil {
				t.Fatalf("Compress at level %d failed: %v", level, err)
			}

			decompressedPath := filepath.Join(tmpDir, "decompressed.txt")
			if err := c.Decompress(compressedPath, decompressedPath); err != nil {
				t.Fatalf("Decompress at level %d failed: %v", level, err)
			}

			decompressed, _ := os.ReadFile(decompressedPath)
			if string(original) != string(decompressed) {
				t.Fatalf("decompressed content mismatch at level %d", level)
			}
			_ = compSize
		})
	}
}

// TestCompressEmptyFile 测试空文件压缩
func TestCompressEmptyFile(t *testing.T) {
	cfg := testConfig(true, 3, nil)
	c := NewCompressor(cfg)

	if c.zstdBin == "" {
		t.Skip("zstd binary not found")
	}

	tmpDir := t.TempDir()

	plainPath := filepath.Join(tmpDir, "empty.txt")
	if err := os.WriteFile(plainPath, []byte{}, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	compressedPath := filepath.Join(tmpDir, "empty.zst")
	_, _, err := c.Compress(plainPath, compressedPath)
	if err != nil {
		t.Fatalf("Compress empty file failed: %v", err)
	}

	decompressedPath := filepath.Join(tmpDir, "empty_decompressed.txt")
	if err := c.Decompress(compressedPath, decompressedPath); err != nil {
		t.Fatalf("Decompress empty file failed: %v", err)
	}

	decompressed, err := os.ReadFile(decompressedPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if len(decompressed) != 0 {
		t.Fatalf("expected empty file, got %d bytes", len(decompressed))
	}
}

// testConfig 创建测试用的配置
func testConfig(enabled bool, level int, skipTypes []string) models.CompressionConfig {
	return models.CompressionConfig{
		Enabled:   enabled,
		Algorithm: "zstd",
		Level:     level,
		SkipTypes: skipTypes,
	}
}
