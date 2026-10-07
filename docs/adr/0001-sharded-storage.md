# ADR 0001: Sharded storage for scalable bucket indexing

- Status: Accepted
- Date: 2026-10-07
- Deciders: dirhop maintainers
- Supersedes: none

## Context

dirhop indexes public object-storage buckets (S3, GCS, Azure Blob,
DigitalOcean Spaces) and directory listings into a local SQLite database so the
results can be browsed, searched, and downloaded offline.

The current design stores everything in a single SQLite file (`dirhop.db`):
all sessions and every indexed entry across every bucket. This worked well for
tens of buckets but breaks down at the scale we now target.

### Forces

1. **Scan concurrency.** SQLite permits exactly one writer per database across
   all processes and connections. With one shared database, every concurrent
   bucket scan serializes on that single writer. Users run 16-20 scan threads
   and want to grow to ~32; today those threads mostly wait on each other, and
   one slow or stuck scan stalls every other write, including unrelated
   commands.
2. **Blast radius.** A single file holding every bucket means one corrupted or
   pathologically large bucket degrades the whole tool. There is no way to drop
   or repair one bucket's data without touching the shared file.
3. **Scale target.** Up to ~100,000 buckets, some with millions of objects,
   i.e. potentially billions of entry rows.
4. **Global reads must stay feasible.** The CLI needs global `stats` and global
   `search` across all buckets. These cannot require opening 100,000 files or
   scanning a billion-row table interactively.
5. **CLI-only today, GUI later.** There is no GUI yet. A future GUI
   (grayhatwarfare-style, with interactive substring filename search) is
   anticipated but explicitly out of scope for this decision. The design must
   not preclude it.

### Non-goals (for this ADR)

- Migrating the existing single-file database. The current `dirhop.db` is kept
  as-is; migration is deferred to a later ADR.
- Full-text / substring search engine (FTS5, Tantivy, Meilisearch) and
  columnar analytics (DuckDB/Parquet). These belong to the GUI milestone.
- Reducing total on-disk size. Sharding does not change total bytes; per-row
  size reduction (dropping the stored `url`, trimming indexes) is a separate,
  orthogonal change tracked elsewhere.

## Decision

Adopt a **two-tier local storage model**: a small shared **catalog** plus a
**fixed set of entry shards**. Route each bucket deterministically to exactly
one shard.

### Tier 1: Catalog (`catalog.db`, one small SQLite file)

Holds only small, global-by-nature data:

- the `sites` (sessions) table: one row per bucket/listing, with its URL,
  provider, status, assigned shard, and **per-bucket aggregate rollups**
  (file_count, directory_count, total_size, and optionally per-extension
  counts);
- app state (active session), crawl-run history, scan checkpoints.

Because the catalog stores per-bucket aggregates, **global `stats` is a simple
aggregation over ~100,000 small catalog rows** and never touches the entry
shards. This keeps global tracking instant regardless of entry volume.

### Tier 2: Entry shards (`shards/shard-NN.db`, fixed count)

Each shard is an independent SQLite database holding the `entries` (indexed
files/directories) for the buckets routed to it. A bucket's entries live in
exactly one shard.

- **Routing**: `shard = stable_hash(site_id) % shard_count`. The mapping is
  stored on the catalog `sites` row at creation time and never recomputed, so
  changing `shard_count` later never silently misroutes existing buckets (a
  future re-shard is an explicit, catalog-driven migration).
- **Write concurrency**: a scan only locks its shard's writer, so up to
  `shard_count` buckets can be indexed fully in parallel. This directly removes
  the single-writer bottleneck.
- **Blast radius**: dropping a bucket is a `DELETE ... WHERE site_id=?` within
  one shard; a damaged shard isolates damage to the buckets it holds.

### Shard count

- Configurable via `shard_count`, range **1-32**, default **16**.
- Chosen to match scan parallelism (typical 16-20 threads, up to 32). A small
  fixed N keeps global reads cheap: a global search fans out across N shard
  files in parallel and merges, which is practical for N<=32 but would be
  infeasible for 100,000 per-bucket files.
- `shard_count` is fixed for the life of a dataset. Changing it requires an
  explicit re-shard migration (future ADR), never an implicit remap.

### Global operations

- **stats**: aggregate the catalog rollups. O(buckets), not O(entries).
- **search / find across all buckets**: fan out the query to each shard
  (parallel), merge and rank results. Per-shard queries use existing
  `LIKE`/`GLOB` filters; no search engine is introduced now.
- **single-session operations** (ls, tree, cat, download): resolve the
  session's shard from the catalog, open just that shard.

## Alternatives considered

1. **Status quo: one shared database.** Rejected: the single-writer bottleneck
   is the primary pain and does not improve with tuning.
2. **One database per bucket (~100,000 files).** Rejected: file-descriptor and
   filesystem limits, and interactive global search would have to open tens of
   thousands of files. Maximum write isolation but infeasible reads and
   management.
3. **A client/server database (PostgreSQL).** Rejected for a single-user local
   CLI: heavy operational burden, and it does not reduce total size or change
   the fundamental row volume. Reconsidered only if multi-machine concurrent
   writers become a requirement.
4. **Keep one DB, add external search/analytics tiers now.** Deferred: correct
   long-term shape for the GUI, but premature for CLI-only use.

Fixed-shard storage captures most of the concurrency and blast-radius benefit
of per-bucket databases while keeping global reads and management tractable,
and it is a clean on-ramp to the GUI (bolt a search index onto the shards
without changing the catalog or routing).

## Consequences

### Positive

- Up to `shard_count`-way concurrent scanning; a stuck scan no longer blocks
  unrelated writes.
- Instant global stats via catalog rollups.
- Per-bucket drop/repair without touching unrelated data.
- Forward-compatible with a future GUI/search tier.

### Negative / costs

- More moving parts: a catalog plus N shard files, a router, and a connection
  manager. Higher code complexity than one file.
- Global search must fan out and merge across N shards (more code, bounded
  cost for N<=32).
- Cross-shard transactional guarantees are weaker; writes that span the catalog
  and a shard are coordinated in application code, not one SQL transaction.
- Does not reduce total disk size; a single enormous bucket is still large and
  slow to scan (sharding parallelizes across buckets, not within one).

### Neutral / deferred

- Existing `dirhop.db` remains the current store until a migration ADR lands.
- Re-sharding (changing `shard_count` on existing data) is an explicit future
  migration, never an implicit remap.

## Rollout plan (incremental, test-driven)

1. **Foundations (no behavior change):** ADR (this document); `shard_count`
   config with validation; a pure, unit-tested **shard router**
   (`site_id -> shard`), stable and independent of storage.
2. **Storage layer:** a shard-aware store that owns the catalog and opens shard
   connections on demand, mirroring the current `database.DB` surface.
3. **Write path:** route scans to the per-bucket shard; maintain catalog
   rollups on completion.
4. **Read path:** single-session reads open one shard; global `stats` uses
   rollups; global `search` fans out across shards.
5. **Migration (separate ADR):** optional tool to split the legacy single file
   into catalog + shards.

Each step is additive and independently tested; the legacy single-file path
remains usable until migration lands.
