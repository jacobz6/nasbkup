package backup

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/nas-backup/internal/models"
)

type progressClient chan models.ProgressEvent

type ProgressBroker struct {
	mu      sync.RWMutex
	clients map[progressClient]struct{}
	logger  *slog.Logger
}

func NewProgressBroker() *ProgressBroker {
	return &ProgressBroker{
		clients: make(map[progressClient]struct{}),
		logger:  slog.Default(),
	}
}

func (b *ProgressBroker) Subscribe() (progressClient, func()) {
	ch := make(progressClient, 256)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	b.logger.Debug("progress client subscribed", "total_clients", len(b.clients))

	unsub := func() {
		b.mu.Lock()
		delete(b.clients, ch)
		close(ch)
		b.mu.Unlock()
		b.logger.Debug("progress client unsubscribed", "total_clients", len(b.clients))
	}
	return ch, unsub
}

func (b *ProgressBroker) Publish(event models.ProgressEvent) {
	event.Timestamp = time.Now()
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.clients {
		select {
		case ch <- event:
		default:
			b.logger.Warn("progress client channel full, dropping event",
				"type", event.Type, "phase", event.Phase)
		}
	}
}

func (b *ProgressBroker) PublishPhase(backupID int64, phase models.BackupPhase, message string) {
	b.Publish(models.ProgressEvent{
		Type:      "phase",
		BackupID:  backupID,
		Phase:     phase,
		PhaseName: phaseName(phase),
		Message:   message,
	})
}

func (b *ProgressBroker) PublishProgress(backupID int64, phase models.BackupPhase, current, total int, percent float64) {
	b.Publish(models.ProgressEvent{
		Type:     "progress",
		BackupID: backupID,
		Phase:    phase,
		Current:  current,
		Total:    total,
		Percent:  percent,
	})
}

func (b *ProgressBroker) PublishFile(backupID int64, phase models.BackupPhase, filePath string, fileSize int64) {
	b.Publish(models.ProgressEvent{
		Type:     "file",
		BackupID: backupID,
		Phase:    phase,
		FilePath: filePath,
		FileSize: fileSize,
	})
}

func (b *ProgressBroker) PublishLog(backupID int64, level, message, detail string) {
	b.Publish(models.ProgressEvent{
		Type:     "log",
		BackupID: backupID,
		Level:    level,
		Message:  message,
		Detail:   detail,
	})
}

func phaseName(phase models.BackupPhase) string {
	switch phase {
	case models.PhaseScanning:
		return "扫描文件"
	case models.PhaseHashing:
		return "计算哈希"
	case models.PhaseDeduplicating:
		return "去重分析"
	case models.PhaseUploading:
		return "上传文件"
	case models.PhaseFinalizing:
		return "完成收尾"
	case models.PhaseCompleted:
		return "备份完成"
	case models.PhaseFailed:
		return "备份失败"
	case models.PhaseCancelled:
		return "已取消"
	default:
		return string(phase)
	}
}

func WriteSSEEvent(w io.Writer, event models.ProgressEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal progress event: %w", err)
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", event.Type); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	return nil
}
