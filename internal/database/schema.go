package database

// The store is split into two kinds of SQLite file, per
// docs/adr/0001-sharded-storage.md:
//
//   - the catalog: one small database holding global, small-cardinality data
//     (sites, app_state, crawl_runs, crawl_errors, scan_checkpoints) plus the
//     per-bucket aggregate rollups that make global stats O(buckets);
//   - entry shards: a fixed set of databases holding the large `entries` and
//     `downloads` tables for the buckets routed to each shard.
//
// Entries carry site_id as a plain column (no cross-file foreign key to sites,
// which SQLite cannot enforce across databases); referential integrity between a
// site and its entries is maintained in application code by the Store.

// catalogSchema defines the catalog database. `sites.shard` records which entry
// shard owns a site's entries, assigned once at creation so routing never has to
// be recomputed from a possibly-changed shard count.
const catalogSchema = `
CREATE TABLE IF NOT EXISTS sites (
 id INTEGER PRIMARY KEY,
 name TEXT NOT NULL COLLATE NOCASE UNIQUE,
 original_url TEXT NOT NULL,
 canonical_url TEXT NOT NULL UNIQUE,
 hostname TEXT NOT NULL,
 parser_type TEXT NOT NULL DEFAULT '',
 cwd TEXT NOT NULL DEFAULT '/',
 shard INTEGER NOT NULL DEFAULT 0,
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
CREATE TABLE IF NOT EXISTS app_state (
 singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
 active_site_id INTEGER REFERENCES sites(id) ON DELETE SET NULL
);
INSERT OR IGNORE INTO app_state(singleton, active_site_id) VALUES(1, NULL);
CREATE INDEX IF NOT EXISTS idx_sites_shard ON sites(shard);
CREATE INDEX IF NOT EXISTS idx_crawl_runs_site ON crawl_runs(site_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_crawl_errors_run ON crawl_errors(run_id, created_at);
CREATE INDEX IF NOT EXISTS idx_scan_checkpoints_updated ON scan_checkpoints(updated_at DESC);
`

// shardSchema defines one entry shard. site_id is a plain column: the catalog is
// the authority on sites, and the Store enforces the relationship.
const shardSchema = `
CREATE TABLE IF NOT EXISTS entries (
 id INTEGER PRIMARY KEY,
 site_id INTEGER NOT NULL,
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
CREATE TABLE IF NOT EXISTS downloads (
 id INTEGER PRIMARY KEY,
 site_id INTEGER NOT NULL,
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
CREATE INDEX IF NOT EXISTS idx_entries_site_parent ON entries(site_id, parent_id, removed, name COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS idx_entries_site_path ON entries(site_id, normalized_path, removed);
CREATE INDEX IF NOT EXISTS idx_entries_name ON entries(site_id, name COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS idx_entries_extension ON entries(site_id, extension COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS idx_entries_type ON entries(site_id, type, removed);
CREATE INDEX IF NOT EXISTS idx_entries_size ON entries(site_id, size);
CREATE INDEX IF NOT EXISTS idx_entries_modified ON entries(site_id, modified_at);
CREATE INDEX IF NOT EXISTS idx_downloads_site ON downloads(site_id, updated_at DESC);
`

// combinedSchema is catalog + shard tables in one file. It is used only by the
// per-file DB method tests, which validate the shared SQL the Store reuses; the
// cross-file entries->sites foreign key is intentionally absent so the same
// method bodies work whether they run against the catalog, a shard, or a
// combined test file.
const combinedSchema = catalogSchema + shardSchema
