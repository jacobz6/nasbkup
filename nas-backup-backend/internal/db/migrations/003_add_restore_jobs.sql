-- 003_add_restore_jobs.sql
-- Restore jobs tracking table for async restore operations.
-- Each row represents a single user-initiated restore task with its
-- parameters, progress counters, and lifecycle status.

CREATE TABLE IF NOT EXISTS restore_jobs (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    status            TEXT NOT NULL CHECK (status IN ('pending', 'running', 'completed', 'failed', 'cancelled')),
    paths             TEXT NOT NULL,                -- JSON array of file/directory paths to restore
    pattern           TEXT,                         -- glob pattern if used instead of paths
    backup_id         INTEGER,                      -- specific backup version to restore from
    output_dir        TEXT NOT NULL,                -- target directory for restored files
    expedited         BOOLEAN NOT NULL DEFAULT 0,   -- use expedited OSS thaw (ColdArchive)
    conflict_strategy TEXT NOT NULL DEFAULT 'skip', -- 'overwrite' | 'skip' | 'rename'
    total_files       INTEGER NOT NULL DEFAULT 0,    -- total files to restore
    restored_files    INTEGER NOT NULL DEFAULT 0,    -- files successfully restored so far
    failed_files      TEXT,                         -- JSON array of paths that failed to restore
    total_size        INTEGER NOT NULL DEFAULT 0,    -- total bytes to restore
    restored_size     INTEGER NOT NULL DEFAULT 0,    -- bytes successfully restored so far
    elapsed_ms        INTEGER,                       -- total elapsed time in milliseconds (set on completion)
    error_message     TEXT,                          -- error details if status is 'failed'
    created_at        TEXT NOT NULL DEFAULT (datetime('now')),
    started_at        TEXT,
    completed_at      TEXT
);

-- Index for quickly finding running/pending jobs and listing history.
CREATE INDEX IF NOT EXISTS idx_restore_jobs_status ON restore_jobs(status);
CREATE INDEX IF NOT EXISTS idx_restore_jobs_created_at ON restore_jobs(created_at DESC);
