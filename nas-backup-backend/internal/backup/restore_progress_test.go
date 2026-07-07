package backup

import (
	"testing"
	"time"

	"github.com/nas-backup/internal/models"
)

func TestRestoreProgressBroker_SubscribePublish(t *testing.T) {
	b := NewRestoreProgressBroker()

	ch, history, unsub := b.Subscribe()
	defer unsub()

	// Initially no history.
	if len(history) != 0 {
		t.Fatalf("expected empty history, got %d events", len(history))
	}

	// Publish a phase event.
	b.PublishPhase(1, models.RestorePhaseDownloading, "test phase")

	select {
	case event := <-ch:
		if event.Type != "phase" {
			t.Errorf("expected phase event, got %q", event.Type)
		}
		if event.JobID != 1 {
			t.Errorf("expected job_id=1, got %d", event.JobID)
		}
		if event.Phase != models.RestorePhaseDownloading {
			t.Errorf("expected phase=downloading, got %q", event.Phase)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for phase event")
	}

	// Publish a progress event.
	b.PublishProgress(1, 5, 10, 50.0, 1024, 2048)
	select {
	case event := <-ch:
		if event.Type != "progress" {
			t.Errorf("expected progress event, got %q", event.Type)
		}
		if event.Current != 5 || event.Total != 10 {
			t.Errorf("expected 5/10, got %d/%d", event.Current, event.Total)
		}
		if event.Percent != 50.0 {
			t.Errorf("expected 50%%, got %f", event.Percent)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for progress event")
	}

	// Publish a file event.
	b.PublishFile(1, "/data/test.txt", 100, "已恢复")
	select {
	case event := <-ch:
		if event.FilePath != "/data/test.txt" {
			t.Errorf("expected /data/test.txt, got %q", event.FilePath)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for file event")
	}

	// Publish a log event.
	b.PublishLog(1, "error", "something failed", "detail")
	select {
	case event := <-ch:
		if event.Level != "error" {
			t.Errorf("expected error level, got %q", event.Level)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for log event")
	}
}

func TestRestoreProgressBroker_HistoryReplay(t *testing.T) {
	b := NewRestoreProgressBroker()

	// Publish several events before subscribing.
	b.PublishPhase(1, models.RestorePhasePreparing, "preparing")
	b.PublishProgress(1, 0, 3, 0, 0, 300)
	b.PublishFile(1, "/a.txt", 100, "已恢复")

	// New subscriber should get history replay.
	_, history, _ := b.Subscribe()
	if len(history) != 3 {
		t.Fatalf("expected 3 history events, got %d", len(history))
	}
}

func TestRestoreProgressBroker_ClearHistory(t *testing.T) {
	b := NewRestoreProgressBroker()
	b.PublishPhase(1, models.RestorePhasePreparing, "preparing")
	b.ClearHistory()

	_, history, _ := b.Subscribe()
	if len(history) != 0 {
		t.Fatalf("expected 0 history events after clear, got %d", len(history))
	}
}

func TestRestoreProgressBroker_Unsubscribe(t *testing.T) {
	b := NewRestoreProgressBroker()
	ch, _, unsub := b.Subscribe()

	unsub()

	// Publishing after unsubscribe should not panic or block.
	b.PublishPhase(1, models.RestorePhaseCompleted, "done")

	// Channel should be closed.
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after unsubscribe")
	}
}

func TestRestoreProgressBroker_HistoryMaxSize(t *testing.T) {
	b := NewRestoreProgressBroker()
	b.historyMax = 5

	// Publish more events than historyMax.
	for i := 0; i < 10; i++ {
		b.PublishLog(1, "info", "event", "")
	}

	_, history, _ := b.Subscribe()
	if len(history) != 5 {
		t.Fatalf("expected history capped at 5, got %d", len(history))
	}
}
