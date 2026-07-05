-- Migration 002: Add orphaned_at column to hash_index.
-- Tracks when a hash_index entry's ref_count dropped to 0, so garbage
-- collection can enforce the orphan grace period correctly (previously
-- used created_at, which reflected when the row was first inserted, not
-- when it became orphaned — causing newly-orphaned rows to be deleted
-- immediately).
--
-- Existing rows with ref_count > 0: orphaned_at stays NULL (not orphaned).
-- Existing rows with ref_count = 0: set orphaned_at to created_at as a
--   reasonable default (better than treating them as immediately deletable).

ALTER TABLE hash_index ADD COLUMN orphaned_at TEXT;

-- Backfill orphaned_at for existing orphans (ref_count=0): use created_at
-- so the grace period starts from when the row was created, not from now.
UPDATE hash_index SET orphaned_at = created_at WHERE ref_count = 0;

CREATE INDEX IF NOT EXISTS idx_hash_index_orphaned ON hash_index (orphaned_at) WHERE orphaned_at IS NOT NULL;
