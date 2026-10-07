package database

const schema = `
CREATE TABLE IF NOT EXISTS sites (
 id INTEGER PRIMARY KEY,
 name TEXT NOT NULL COLLATE NOCASE UNIQUE,
 original_url TEXT NOT NULL,
 canonical_url TEXT NOT NULL UNIQUE,
 hostname TEXT NOT NULL,
 parser_type TEXT NOT NULL DEFAULT '',
 cwd TEXT NOT NULL DEFAULT '/',
 entry_count INTEGER NOT NULL DEFAULT 0,
 file_count INTEGER NOT NULL DEFAULT 0,
 directory_count INTEGER NOT NULL DEFAULT 1,
 total_size INTEGER NOT NULL DEFAULT 0,
 scan_status TEXT NOT NULL DEFAULT 'pending',
 crawl_concurrency INTEGER NOT NULL DEFAULT 8 CHECK(crawl_concurrency > 0),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 last_crawled_at INTEGER
);
CREATE TABLE IF NOT EXISTS entries (
 id INTEGER PRIMARY KEY,
 site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
 parent_id INTEGER REFERENCES entries(id) ON DELETE CASCADE,
 name TEXT NOT NULL,
 normalized_path TEXT NOT NULL,
 url TEXT NOT NULL,
 type TEXT NOT NULL CHECK(type IN ('directory','file','symlink','unknown')),
 size INTEGER CHECK(size IS NULL OR size >= 0),
 modified_at INTEGER,
 etag TEXT NOT NULL DEFAULT '',
 last_modified TEXT NOT NULL DEFAULT '',
 content_type TEXT NOT NULL DEFAULT '',
 extension TEXT NOT NULL DEFAULT '',
 removed INTEGER NOT NULL DEFAULT 0 CHECK(removed IN (0,1)),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 last_seen_at INTEGER NOT NULL,
 UNIQUE(site_id, normalized_path)
);
CREATE TABLE IF NOT EXISTS crawl_runs (
 id INTEGER PRIMARY KEY,
 site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
 mode TEXT NOT NULL,
 status TEXT NOT NULL,
 started_at INTEGER NOT NULL,
 finished_at INTEGER,
 directories INTEGER NOT NULL DEFAULT 0,
 files INTEGER NOT NULL DEFAULT 0,
 bytes INTEGER NOT NULL DEFAULT 0,
 error_count INTEGER NOT NULL DEFAULT 0,
 failure_reason TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS crawl_errors (
 id INTEGER PRIMARY KEY,
 run_id INTEGER NOT NULL REFERENCES crawl_runs(id) ON DELETE CASCADE,
 site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
 url TEXT NOT NULL DEFAULT '',
 path TEXT NOT NULL DEFAULT '',
 operation TEXT NOT NULL DEFAULT '',
 message TEXT NOT NULL,
 created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS scan_checkpoints (
 site_id INTEGER PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
 cursor TEXT NOT NULL DEFAULT '',
 started_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS downloads (
 id INTEGER PRIMARY KEY,
 site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
 entry_id INTEGER NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
 source_url TEXT NOT NULL,
 destination TEXT NOT NULL,
 status TEXT NOT NULL,
 bytes_done INTEGER NOT NULL DEFAULT 0 CHECK(bytes_done >= 0),
 total_bytes INTEGER CHECK(total_bytes IS NULL OR total_bytes >= 0),
 error TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 completed_at INTEGER,
 UNIQUE(entry_id, destination)
);
CREATE TABLE IF NOT EXISTS app_state (
 singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
 active_site_id INTEGER REFERENCES sites(id) ON DELETE SET NULL
);
INSERT OR IGNORE INTO app_state(singleton, active_site_id) VALUES(1, NULL);
CREATE INDEX IF NOT EXISTS idx_entries_site_parent ON entries(site_id, parent_id, removed, name COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS idx_entries_site_path ON entries(site_id, normalized_path, removed);
CREATE INDEX IF NOT EXISTS idx_entries_name ON entries(site_id, name COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS idx_entries_extension ON entries(site_id, extension COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS idx_entries_type ON entries(site_id, type, removed);
CREATE INDEX IF NOT EXISTS idx_entries_size ON entries(site_id, size);
CREATE INDEX IF NOT EXISTS idx_entries_modified ON entries(site_id, modified_at);
CREATE INDEX IF NOT EXISTS idx_crawl_runs_site ON crawl_runs(site_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_crawl_errors_run ON crawl_errors(run_id, created_at);
CREATE INDEX IF NOT EXISTS idx_scan_checkpoints_updated ON scan_checkpoints(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_downloads_site ON downloads(site_id, updated_at DESC);
`
