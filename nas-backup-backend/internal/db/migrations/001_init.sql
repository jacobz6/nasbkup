-- Initial schema: creates all tables for the NAS backup system.

-- File tracking: every file ever seen in the backup directories.
CREATE TABLE IF NOT EXISTS files (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    path       TEXT    NOT NULL UNIQUE,
    size       INTEGER NOT NULL DEFAULT 0,
    mod_time   TEXT    NOT NULL,
    hash       TEXT    NOT NULL DEFAULT '',
    status     TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'deleted')),
    backup_id  INTEGER,
    inode      INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_files_path    ON files (path);
CREATE INDEX IF NOT EXISTS idx_files_hash    ON files (hash);
CREATE INDEX IF NOT EXISTS idx_files_status  ON files (status);
CREATE INDEX IF NOT EXISTS idx_files_inode   ON files (inode);

-- Backup session records.
CREATE TABLE IF NOT EXISTS backups (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    type            TEXT    NOT NULL CHECK (type IN ('full', 'incremental')),
    status          TEXT    NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'failed', 'cancelled')),
    base_backup_id  INTEGER,
    total_files     INTEGER NOT NULL DEFAULT 0,
    total_size      INTEGER NOT NULL DEFAULT 0,
    uploaded_size   INTEGER NOT NULL DEFAULT 0,
    skipped_dedup   INTEGER NOT NULL DEFAULT 0,
    skipped_inc     INTEGER NOT NULL DEFAULT 0,
    compress_saved  INTEGER NOT NULL DEFAULT 0,
    started_at      TEXT,
    completed_at    TEXT,
    error_message   TEXT    NOT NULL DEFAULT '',
    created_at      TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_backups_status    ON backups (status);
CREATE INDEX IF NOT EXISTS idx_backups_type      ON backups (type);
CREATE INDEX IF NOT EXISTS idx_backups_created   ON backups (created_at);

-- Many-to-many: which files were included in which backup session.
CREATE TABLE IF NOT EXISTS backup_files (
    backup_id      INTEGER NOT NULL REFERENCES backups(id) ON DELETE CASCADE,
    file_id        INTEGER NOT NULL REFERENCES files(id)   ON DELETE CASCADE,
    storage_key    TEXT    NOT NULL,
    encrypted_iv   TEXT    NOT NULL DEFAULT '',
    auth_tag       TEXT    NOT NULL DEFAULT '',
    compress_type  TEXT    NOT NULL DEFAULT 'none' CHECK (compress_type IN ('zstd', 'none')),
    original_size  INTEGER NOT NULL DEFAULT 0,
    stored_size    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (backup_id, file_id)
);

CREATE INDEX IF NOT EXISTS idx_backup_files_backup   ON backup_files (backup_id);
CREATE INDEX IF NOT EXISTS idx_backup_files_storage  ON backup_files (storage_key);

-- Global hash index for content-addressable dedup.
CREATE TABLE IF NOT EXISTS hash_index (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    hash       TEXT    NOT NULL UNIQUE,
    file_size  INTEGER NOT NULL DEFAULT 0,
    storage_key TEXT   NOT NULL DEFAULT '',
    ref_count  INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_hash_index_hash ON hash_index (hash);

-- Backup operation logs.
CREATE TABLE IF NOT EXISTS backup_logs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    backup_id  INTEGER REFERENCES backups(id) ON DELETE SET NULL,
    level      TEXT    NOT NULL DEFAULT 'info' CHECK (level IN ('debug', 'info', 'warn', 'error')),
    message    TEXT    NOT NULL,
    detail     TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_backup_logs_backup  ON backup_logs (backup_id);
CREATE INDEX IF NOT EXISTS idx_backup_logs_level   ON backup_logs (level);
CREATE INDEX IF NOT EXISTS idx_backup_logs_created ON backup_logs (created_at);

-- Key-value configuration store for runtime settings.
CREATE TABLE IF NOT EXISTS config_kv (
    key        TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL DEFAULT '',
    updated_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- Backup directories to include.
CREATE TABLE IF NOT EXISTS backup_directories (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    path        TEXT    NOT NULL UNIQUE,
    recursive   INTEGER NOT NULL DEFAULT 1,
    enabled     INTEGER NOT NULL DEFAULT 1,
    description TEXT    NOT NULL DEFAULT ''
);

-- Exclusion rules.
CREATE TABLE IF NOT EXISTS exclusion_rules (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    pattern  TEXT    NOT NULL UNIQUE,
    rule_type TEXT   NOT NULL DEFAULT 'pattern' CHECK (rule_type IN ('extension', 'directory', 'pattern', 'size_exceed')),
    enabled  INTEGER NOT NULL DEFAULT 1
);
