-- Migration: Create commit path index for O(1) commit lookup optimization
-- This replaces O(C×N×F) Git traversal with database index queries
-- Target: 8,450ms → 200ms performance improvement (97% reduction)

-- Create commit_path_index table
CREATE TABLE IF NOT EXISTS commit_path_index (
    id                BIGSERIAL PRIMARY KEY,
    repo_id           VARCHAR(255) NOT NULL,
    file_path         TEXT NOT NULL,
    last_commit_sha   VARCHAR(64) NOT NULL,
    commit_time       TIMESTAMP NOT NULL,
    change_type       VARCHAR(20) NOT NULL CHECK (change_type IN ('added', 'modified', 'deleted', 'renamed')),
    file_size         BIGINT DEFAULT 0,
    created_at        TIMESTAMP DEFAULT NOW(),
    updated_at        TIMESTAMP DEFAULT NOW(),

    -- Unique constraint to prevent duplicate entries
    UNIQUE(repo_id, file_path)
);

-- Primary performance indexes
-- This compound index enables O(1) lookup for (repo_id, file_path) queries
CREATE INDEX IF NOT EXISTS idx_commit_path_repo_path
    ON commit_path_index(repo_id, file_path);

-- Secondary indexes for efficient queries
-- For listing files by commit time (recent files first)
CREATE INDEX IF NOT EXISTS idx_commit_path_repo_time
    ON commit_path_index(repo_id, commit_time DESC);

-- For commit SHA lookups and batch operations
CREATE INDEX IF NOT EXISTS idx_commit_path_commit_sha
    ON commit_path_index(last_commit_sha);

-- For directory-level queries (prefix matching)
CREATE INDEX IF NOT EXISTS idx_commit_path_repo_path_prefix
    ON commit_path_index(repo_id, file_path varchar_pattern_ops);

-- For change type filtering (added/modified/deleted)
CREATE INDEX IF NOT EXISTS idx_commit_path_change_type
    ON commit_path_index(repo_id, change_type, commit_time DESC);

-- For file size analysis and large file detection
CREATE INDEX IF NOT EXISTS idx_commit_path_size
    ON commit_path_index(repo_id, file_size DESC)
    WHERE file_size > 0;

-- Update timestamp trigger function
CREATE OR REPLACE FUNCTION update_commit_path_index_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ language 'plpgsql';

-- Trigger to auto-update updated_at timestamp
CREATE TRIGGER update_commit_path_index_updated_at
    BEFORE UPDATE ON commit_path_index
    FOR EACH ROW
    EXECUTE FUNCTION update_commit_path_index_updated_at();

-- Partial indexes for specific use cases

-- Index for active (non-deleted) files only
CREATE INDEX IF NOT EXISTS idx_commit_path_active_files
    ON commit_path_index(repo_id, commit_time DESC)
    WHERE change_type != 'deleted';

-- Index for recently modified files (last 30 days)
CREATE INDEX IF NOT EXISTS idx_commit_path_recent_changes
    ON commit_path_index(repo_id, file_path, commit_time DESC)
    WHERE commit_time > NOW() - INTERVAL '30 days';

-- Comments for performance optimization context
COMMENT ON TABLE commit_path_index IS
'High-performance index for commit history lookup. Enables O(1) queries instead of O(C×N×F) Git traversal. Target: 97% performance improvement (8,450ms → 200ms).';

COMMENT ON INDEX idx_commit_path_repo_path IS
'Primary performance index. Enables instant lookup of latest commit for any file path.';

COMMENT ON INDEX idx_commit_path_repo_time IS
'Secondary index for chronological file browsing and recent changes.';

COMMENT ON INDEX idx_commit_path_commit_sha IS
'Enables efficient batch operations and commit SHA reverse lookups.';

-- SQLite compatibility (alternative syntax for environments without PostgreSQL)
-- Note: This section would be used in SQLite environments
--
-- CREATE TABLE IF NOT EXISTS commit_path_index (
--     id                INTEGER PRIMARY KEY AUTOINCREMENT,
--     repo_id           TEXT NOT NULL,
--     file_path         TEXT NOT NULL,
--     last_commit_sha   TEXT NOT NULL,
--     commit_time       DATETIME NOT NULL,
--     change_type       TEXT NOT NULL CHECK (change_type IN ('added', 'modified', 'deleted', 'renamed')),
--     file_size         INTEGER DEFAULT 0,
--     created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
--     updated_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
--     UNIQUE(repo_id, file_path)
-- );
--
-- CREATE INDEX IF NOT EXISTS idx_commit_path_repo_path ON commit_path_index(repo_id, file_path);
-- CREATE INDEX IF NOT EXISTS idx_commit_path_repo_time ON commit_path_index(repo_id, commit_time DESC);
-- CREATE INDEX IF NOT EXISTS idx_commit_path_commit_sha ON commit_path_index(last_commit_sha);