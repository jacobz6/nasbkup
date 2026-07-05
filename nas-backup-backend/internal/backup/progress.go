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
	mu         sync.Mutex
	clients    map[progressClient]struct{}
	logger     *slog.Logger
	history    []models.ProgressEvent
	historyMax int
}

func NewProgressBroker() *ProgressBroker {
	return &ProgressBroker{
		clients:    make(map[progressClient]struct{}),
		logger:     slog.Default(),
		historyMax: 2000,
	}
}

// Subscribe 注册一个新的 SSE 客户端，返回事件 channel、历史事件快照和取消订阅函数。
// 历史快照允许新客户端恢复之前错过的进度和日志事件（如切换页面后回来）。
func (b *ProgressBroker) Subscribe() (progressClient, []models.ProgressEvent, func()) {
	ch := make(progressClient, 256)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	// 快照历史事件，用于新客户端恢复状态。
	// 快照在锁内取，之后 Publish 的新事件只通过 channel 发送，不会重复。
	snapshot := make([]models.ProgressEvent, len(b.history))
	copy(snapshot, b.history)
	b.mu.Unlock()
	b.logger.Debug("progress client subscribed",
		"total_clients", len(b.clients), "history_events", len(snapshot))

	unsub := func() {
		b.mu.Lock()
		delete(b.clients, ch)
		close(ch)
		b.mu.Unlock()
		b.logger.Debug("progress client unsubscribed", "total_clients", len(b.clients))
	}
	return ch, snapshot, unsub
}

// Publish 广播事件给所有订阅者，并追加到历史缓冲。
func (b *ProgressBroker) Publish(event models.ProgressEvent) {
	event.Timestamp = time.Now()
	b.mu.Lock()
	// 追加到历史缓冲（ring buffer 语义）
	b.history = append(b.history, event)
	if len(b.history) > b.historyMax {
		b.history = b.history[len(b.history)-b.historyMax:]
	}
	// 广播给客户端（channel 发送有 default 分支，不会阻塞）
	for ch := range b.clients {
		select {
		case ch <- event:
		default:
			b.logger.Warn("progress client channel full, dropping event",
				"type", event.Type, "phase", event.Phase)
		}
	}
	b.mu.Unlock()
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

// ClearHistory 清空历史缓冲（备份结束后调用，避免下次连接回放过期事件）。
func (b *ProgressBroker) ClearHistory() {
	b.mu.Lock()
	b.history = b.history[:0]
	b.mu.Unlock()
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
