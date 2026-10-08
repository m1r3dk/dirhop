# Recipes

Task-oriented workflows with exact commands. See [[Command-Reference]] for flag
semantics and [[Flags-and-Exit-Codes]] for scripting and exit codes.

---

## Bulk-index a large target list

Index thousands of targets, skip unreachable ones before crawling, and capture a
retry list.

```sh
dirhop scan -f buckets.txt -p 0 --preflight --failed-file failed.txt
dirhop sessions --status complete
dirhop scan -f failed.txt -p 10            # retry later
```

`-p 0` auto-scales concurrency. `--preflight` issues one reachability probe per
target and drops private/missing/errored before any full crawl. The report is
written to `preflight.md` beside the database.

## Resume an interrupted bulk scan

```sh
dirhop scan -f buckets.txt -p 0            # re-run skips completed sessions
dirhop scan --retry-failed -p 10           # or target only failed/cancelled/unfinished
```

## Cross-session search and extraction

```sh
dirhop find --all-sites --ext sql,dump --json | jq -r '.[]'
dirhop search --all-sites credentials
dirhop download --all-sites --include '*backup*' -o ./backups
```

`--all-sites` matches emit `session:path`. `download --all-sites` writes into
per-session subdirectories under `-o`.

## Targeted download from one session

```sh
dirhop -s reports find --ext pdf --json | jq -r '.[].path'
dirhop -s reports download --all --include '*.pdf' -o ./pdfs -w 6
```

## Large-file transfer: segmented, rate-limited, resumable

```sh
dirhop -s mirror download /datasets/full.tar \
  --segments 8 --max-rate 20MB --resume -o ./data
```

`--segments N` uses parallel validated byte-ranges when the origin supports
them, falling back to a single stream otherwise. Re-running resumes from the
`.part` file.

## Pipe into external tooling

```sh
dirhop -s mirror urls --ext iso | aria2c -i -
dirhop -s mirror urls --files-only > urls.txt
dirhop -s mirror find --ext iso --json | jq -r '.[].path'
```

JSON goes to stdout; progress/log output goes to stderr, so pipes stay clean.
Add `--quiet` to suppress non-essential stdout.

## Discover then index (GrayHatWarfare)

```sh
dirhop ghw invoices --type aws --json          # inspect matches
dirhop ghw invoices --scan                      # index matches directly
dirhop ghw invoices --urls | dirhop scan -f - -p 0 --preflight
```

Requires an API key; see [[GrayHatWarfare]].

## Detect changes between scans

```sh
dirhop -s reports refresh
dirhop -s reports changes          # entries soft-deleted since last successful scan
dirhop -s reports info             # counts and last-crawl timestamp
```

Removed entries are retained with last-seen size/timestamp rather than purged.

## Reclaim database space

```sh
dirhop db status                   # live vs. soft-deleted rows per session
dirhop db duplicates               # sessions indexing the same bucket
dirhop db clean                    # dry run
dirhop db clean --apply            # dedupe + prune + vacuum
```

All mutating `db` subcommands are dry-run until `--apply`.

## Scheduled refresh (cron)

```sh
#!/usr/bin/env bash
set -euo pipefail
for s in mirror reports datasets; do
  dirhop -s "$s" refresh --quiet
done
dirhop stats --json > "$HOME/dirhop-stats-$(date +%F).json"
```

```
0 2 * * * /path/to/refresh-nightly.sh >> /var/log/dirhop.log 2>&1
```

Branch automation on exit codes ([[Flags-and-Exit-Codes]]).

## Pin to a specific index location

```sh
dirhop -c ./project.toml scan -f targets.txt
dirhop -c ./project.toml -s mirror ls
```

A per-project config isolates the database and shard directory; see
[[Configuration]].
