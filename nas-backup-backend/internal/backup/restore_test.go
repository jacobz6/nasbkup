package backup

import (
	"path/filepath"
	"testing"

	"github.com/nas-backup/internal/models"
)

func TestValidateOutputDir(t *testing.T) {
	tests := []struct {
		name      string
		outputDir string
		allowed   []string
		wantErr   bool
	}{
		{
			name:      "empty allowed dirs - no restriction",
			outputDir: "/any/path",
			allowed:   nil,
			wantErr:   false,
		},
		{
			name:      "exact match on allowed base",
			outputDir: "/data",
			allowed:   []string{"/data"},
			wantErr:   false,
		},
		{
			name:      "subdir under allowed base",
			outputDir: "/data/photos/2024",
			allowed:   []string{"/data"},
			wantErr:   false,
		},
		{
			name:      "multiple allowed bases - first matches",
			outputDir: "/home/user/docs",
			allowed:   []string{"/data", "/home/user"},
			wantErr:   false,
		},
		{
			name:      "outside allowed bases",
			outputDir: "/etc/secrets",
			allowed:   []string{"/data", "/home/user"},
			wantErr:   true,
		},
		{
			name:      "path traversal attempt",
			outputDir: "/data/../etc",
			allowed:   []string{"/data"},
			wantErr:   true, // cleaned becomes /etc
		},
		{
			name:      "nested traversal cleaned correctly",
			outputDir: "/data/./photos/../docs/",
			allowed:   []string{"/data"},
			wantErr:   false, // cleaned becomes /data/docs
		},
		{
			name:      "similar prefix but different root",
			outputDir: "/data2/backup",
			allowed:   []string{"/data"},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// filepath.Clean the input as the function does.
			cleaned := filepath.Clean(tt.outputDir)
			err := ValidateOutputDir(cleaned, tt.allowed)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateOutputDir(%q, %v) error = %v, wantErr %v",
					cleaned, tt.allowed, err, tt.wantErr)
			}
		})
	}
}

func TestRestorePhaseName(t *testing.T) {
	tests := []struct {
		phase models.RestorePhase
		want  string
	}{
		{models.RestorePhasePreparing, "准备恢复"},
		{models.RestorePhaseThawing, "解冻文件"},
		{models.RestorePhaseDownloading, "下载文件"},
		{models.RestorePhaseDecrypting, "解密文件"},
		{models.RestorePhaseDecompressing, "解压文件"},
		{models.RestorePhaseVerifying, "校验文件"},
		{models.RestorePhaseMoving, "写入文件"},
		{models.RestorePhaseCompleted, "恢复完成"},
		{models.RestorePhaseFailed, "恢复失败"},
		{models.RestorePhaseCancelled, "已取消"},
	}
	for _, tt := range tests {
		got := restorePhaseName(tt.phase)
		if got != tt.want {
			t.Errorf("restorePhaseName(%q) = %q, want %q", tt.phase, got, tt.want)
		}
	}
}
