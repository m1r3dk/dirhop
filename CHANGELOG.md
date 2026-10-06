# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- `scan`, `-f FILE` lines, and the root `dirhop <host>` argument now accept a
  bare host with no scheme (for example `amazetest.storage.googleapis.com` or
  `host.blob.core.windows.net/container`) and index it over `https`. Bucket
  lists can be fed in as-is.
- GrayHatWarfare public-bucket discovery via the `ghw` command, including
  `--files`, `--scan`, and `--urls`.
- Azure Blob Storage and DigitalOcean Spaces bucket support.

### Changed
- Bumped `golang.org/x/net` to v0.55.0 to clear known advisories.

### Security
- `govulncheck` reports no vulnerabilities in called code.

[Unreleased]: https://github.com/m1r3dk/dirhop/commits/main
