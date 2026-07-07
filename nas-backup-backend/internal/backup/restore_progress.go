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

type restoreProgressClient chan models.RestoreProgressEvent

// RestoreProgressBroker manages SSE subscriptions and event broadcasting for
// restore operations. It is fully independent from the backup ProgressBroker
// to avoid event confusion between backup and restore streams.
type RestoreProgressBroker struct {
	mu         sync.Mutex
	clients    map[restoreProgressClient]struct{}
	logger     *slog.Logger
	history    []models.RestoreProgressEvent
	historyMax int
}

// NewRestoreProgressBroker creates a new RestoreProgressBroker.
func NewRestoreProgressBroker() *RestoreProgressBroker {
	return &RestoreProgressBroker{
		clients:    make(map[restoreProgressClient]struct{}),
		logger:     slog.Default(),
		historyMax: 2000,
	}
}

// Subscribe registers a new SSE client and returns the event channel,
// a snapshot of historical events, and an unsubscribe function.
func (b *RestoreProgressBroker) Subscribe() (restoreProgressClient, []models.RestoreProgressEvent, func()) {
	ch := make(restoreProgressClient, 256)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	snapshot := make([]models.RestoreProgressEvent, len(b.history))
	copy(snapshot, b.history)
	b.mu.Unlock()

	b.logger.Debug("restore progress client subscribed",
		"total_clients", len(b.clients), "history_events", len(snapshot))

	unsub := func() {
		b.mu.Lock()
		delete(b.clients, ch)
		close(ch)
		b.mu.Unlock()
		b.logger.Debug("restore progress client unsubscribed",
			"total_clients", len(b.clients))
	}
	return ch, snapshot, unsub
}

// Publish broadcasts an event to all subscribers and appends it to history.
func (b *RestoreProgressBroker) Publish(event models.RestoreProgressEvent) {
	event.Timestamp = time.Now()
	b.mu.Lock()
	b.history = append(b.history, event)
	if len(b.history) > b.historyMax {
		b.history = b.history[len(b.history)-b.historyMax:]
	}
	for ch := range b.clients {
		select {
		case ch <- event:
		default:
			b.logger.Warn("restore progress client channel full, dropping event",
				"type", event.Type, "phase", event.Phase)
		}
	}
	b.mu.Unlock()
}

// PublishPhase broadcasts a phase transition event.
func (b *RestoreProgressBroker) PublishPhase(jobID int64, phase models.RestorePhase, message string) {
	b.Publish(models.RestoreProgressEvent{
		Type:      "phase",
		JobID:     jobID,
		Phase:     phase,
		PhaseName: restorePhaseName(phase),
		Message:   message,
	})
}

// PublishProgress broadcasts a progress update event.
func (b *RestoreProgressBroker) PublishProgress(jobID int64, current, total int, percent float64, restoredSize, totalSize int64) {
	b.Publish(models.RestoreProgressEvent{
		Type:         "progress",
		JobID:        jobID,
		Current:      current,
		Total:        total,
		Percent:      percent,
		RestoredSize: restoredSize,
		TotalSize:    totalSize,
	})
}

// PublishFile broadcasts a per-file event during restore.
func (b *RestoreProgressBroker) PublishFile(jobID int64, filePath string, fileSize int64, message string) {
	b.Publish(models.RestoreProgressEvent{
		Type:     "file",
		JobID:    jobID,
		FilePath: filePath,
		FileSize: fileSize,
		Message:  message,
	})
}

// PublishLog broadcasts a log message event.
func (b *RestoreProgressBroker) PublishLog(jobID int64, level, message, detail string) {
	b.Publish(models.RestoreProgressEvent{
		Type:   "log",
		JobID:  jobID,
		Level:  level,
		Message: message,
		Detail: detail,
	})
}

// ClearHistory empties the historical event buffer.
func (b *RestoreProgressBroker) ClearHistory() {
	b.mu.Lock()
	b.history = b.history[:0]
	b.mu.Unlock()
}

// restorePhaseName returns a Chinese human-readable name for a restore phase.
func restorePhaseName(phase models.RestorePhase) string {
	switch phase {
	case models.RestorePhasePreparing:
		return "准备恢复"
	case models.RestorePhaseThawing:
		return "解冻文件"
	case models.RestorePhaseDownloading:
		return "下载文件"
	case models.RestorePhaseDecrypting:
		return "解密文件"
	case models.RestorePhaseDecompressing:
		return "解压文件"
	case models.RestorePhaseVerifying:
		return "校验文件"
	case models.RestorePhaseMoving:
		return "写入文件"
	case models.RestorePhaseCompleted:
		return "恢复完成"
	case models.RestorePhaseFailed:
		return "恢复失败"
	case models.RestorePhaseCancelled:
		return "已取消"
	default:
		return string(phase)
	}
}

// WriteRestoreSSEEvent writes a single SSE event to the writer.
func WriteRestoreSSEEvent(w io.Writer, event models.RestoreProgressEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal restore progress event: %w", err)
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", event.Type); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	return nil
}
