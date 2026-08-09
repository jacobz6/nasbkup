-- Migration 004: Add per-file backup status tracking.
--
-- Adds last_backup_status / last_backup_error / last_backup_at / last_backup_id
-- columns to the files table so the UI can show whether each file's last
-- backup succeeded or failed, and why.
--
-- Note: The backups table status CHECK (including 'completed_with_errors')
-- and the failed_files column are already defined in 001_init.sql, so this
-- migration no longer rebuilds the backups table.

-- ── files: new columns ────────────────────────────────────────────────────

ALTER TABLE files ADD COLUMN last_backup_status TEXT NOT NULL DEFAULT '';
ALTER TABLE files ADD COLUMN last_backup_error  TEXT NOT NULL DEFAULT '';
ALTER TABLE files ADD COLUMN last_backup_at     TEXT;
ALTER TABLE files ADD COLUMN last_backup_id     INTEGER;

-- Backfill: files that have at least one backup_files row get status 'success'.
-- We cannot easily determine the exact backup_id/timestamp from existing data
-- without complex joins, so leave those NULL — the next successful backup will
-- populate them correctly.
UPDATE files SET last_backup_status = 'success'
WHERE id IN (SELECT DISTINCT file_id FROM backup_files);

CREATE INDEX IF NOT EXISTS idx_files_last_backup_status ON files (last_backup_status) WHERE last_backup_status <> '';
