# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- `find --all-sites` and `search --all-sites` search every indexed session at
  once and print `session:path` matches. `download --all-sites --ext zip` and
  `download --all-sites --include '*secrets*'` download matching files from all
  sessions into per-session subdirectories.
- New `stats` command reports total indexed sessions, files, directories,
  storage, soft-deleted files, entry types, and file-extension counts/sizes.
- `scan --retry-failed` rescans every session that previously failed, was
  cancelled, or never finished, pulled straight from the index, so a large run
  can be resumed without keeping the original list or `--failed-file`.
- `scan` shows live preflight progress (`Preflight: N/M checked`) for large
  lists and can write every unscannable target to a retry file with
  `--failed-file PATH`. The file lists private, missing, errored, and
  scan-failed targets with a reason comment and is re-readable by `scan -f`.
- Bucket sessions are now named by the bucket (or Azure container) name alone,
  e.g. `wustl` instead of `wustl-s3-us-west-2-amazonaws-com`. Non-bucket sites
  keep host-based names. `session normalize-names` renames existing sessions to
  the new scheme, keeping names unique.
- `scan --preflight` runs a fast, concurrent accessibility check (one minimal
  listing request per recognized bucket, or HEAD/tiny GET for ordinary HTTP
  listings) and scans only targets that are reachable and publicly listable
  right now, skipping private, deleted, dead-host, and erroring targets. On a
  15-dead-host sample it finished and skipped all of them in under 2 seconds.
- Multi-target `scan` runs preflight automatically and offers `--no-preflight`
  when every target should be attempted regardless of the cheap check.
- Re-running `scan` on a list skips buckets that already completed successfully
  (no re-crawl); `--rescan` forces a fresh crawl. Failed, cancelled, and
  never-finished sessions are retried, so a large list can be re-run to pick up
  only the targets that still need work.
- `sessions` now shows a STATUS column and accepts `--status complete|failed|
  pending|running|cancelled`, so failed buckets are easy to find and re-run.
- New `changes` command lists files that disappeared since the last successful
  scan (soft-deleted, kept with their last-seen size and timestamp rather than
  purged), so deletions between scans are detectable without hashing.
- `scan --parallel/-p N` indexes many sites concurrently. For large bucket
  lists this is dramatically faster (most of each bucket's time is network
  latency); a 24-host sample ran ~28x faster than the serial default. Per-URL
  results stay in input order. `--parallel 0` auto-scales the worker count.
- `scan`, `-f FILE` lines, and the root `dirhop <host>` argument now accept a
  bare host with no scheme (for example `amazetest.storage.googleapis.com` or
  `host.blob.core.windows.net/container`) and index it over `https`. Bucket
  lists can be fed in as-is.
- GrayHatWarfare public-bucket discovery via the `ghw` command, including
  `--files`, `--scan`, and `--urls`.
- Azure Blob Storage and DigitalOcean Spaces bucket support.

### Changed
- SQLite now uses one connection per process and a longer default busy timeout
  so two `dirhop` instances writing the same database wait for each other
  instead of failing quickly with `SQLITE_BUSY`; scan entry writes also retry
  busy transactions with backoff.
- Scans now show live crawl location in terminals: single-target crawls render
  `Crawling: bucket:/path`, and multi-bucket scans also keep a live
  "crawling ..." footer naming buckets currently in flight. Pipes, files, and
  `--json` stay clean.
- Preflight is much faster and no longer floods the network: it uses a dedicated
  client with a short per-probe timeout (default 8s, `preflight_timeout` in
  config) and zero retries, so each target is checked with exactly one request
  and dead or stalled hosts fail fast instead of blocking a worker for the full
  30s crawl timeout times the retry count.
- SQLite write transactions are serialized in-process so concurrent crawls
  (parallel multi-bucket scans) never surface `SQLITE_BUSY`; reads stay
  concurrent under WAL.
- Bumped `golang.org/x/net` to v0.55.0 to clear known advisories.

### Security
- `govulncheck` reports no vulnerabilities in called code.

[Unreleased]: https://github.com/m1r3dk/dirhop/commits/main
