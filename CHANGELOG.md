# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
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
- SQLite write transactions are serialized in-process so concurrent crawls
  (parallel multi-bucket scans) never surface `SQLITE_BUSY`; reads stay
  concurrent under WAL.
- Bumped `golang.org/x/net` to v0.55.0 to clear known advisories.

### Security
- `govulncheck` reports no vulnerabilities in called code.

[Unreleased]: https://github.com/m1r3dk/dirhop/commits/main
