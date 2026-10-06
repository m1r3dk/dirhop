# dirhop

`dirhop` turns an HTTP/HTTPS directory-listing website into a persistent local metadata index that behaves like a remote filesystem. Crawls fetch directory pages only. File content is transferred only after an explicit `download` command.

> Use `dirhop` for ethical purposes only.

## Disclaimer

`dirhop` is provided for **ethical purposes only**. You are solely responsible
for how you use it and for complying with all applicable laws and the terms of
service of any target.

The author(s) and contributors provide this software "as is", without warranty
of any kind, and **accept no liability** for any misuse, damage, or unlawful or
malicious activity carried out with it. By using `dirhop` you agree that you
alone bear responsibility for your actions. See [`LICENSE`](LICENSE) and
[`SECURITY.md`](SECURITY.md).

## Install

```sh
go install github.com/m1r3dk/dirhop/cmd/dirhop@latest
```

Or build locally:

```sh
make build
./bin/dirhop --help
```

## Quick start

Open a listing for the first time, crawl it, and enter the interactive shell:

```sh
dirhop https://example.com/
```

Later, the same command reopens the persisted index without crawling again.

Index many sites at once from a file (one URL per line, optional session name, `#` comments). A bare host with no scheme is accepted and indexed over `https`, so bucket lists can be fed in as-is:

```sh
cat > urls.txt <<'EOF'
https://mirror.example.com/pub/      mirror
example-bucket.s3.amazonaws.com
host.blob.core.windows.net/container
# disabled.example.com
EOF
dirhop scan -f urls.txt            # or: cat urls.txt | dirhop scan -f -
dirhop scan https://a.example/ https://b.example/
```

Duplicate URLs are skipped. One failing site does not stop the others; the exit code reflects the first failure. `--json` prints a per-URL result list.

For large lists (thousands of buckets), index many sites concurrently with `--parallel/-p N`. Most of a single bucket's wall-clock time is network latency (and dead hosts time out), so overlapping them is dramatically faster: on one 24-host sample, `--parallel 24` ran ~28x faster than the default serial scan. Use `--parallel 0` for an automatic worker count that scales with the list. Results stay in input order and SQLite writes are serialized internally, so the index never corrupts.

```sh
dirhop scan -f buckets.txt --parallel 32        # index 32 buckets at once
dirhop scan -f buckets.txt --parallel 0         # auto-scale concurrency
dirhop ghw backups --urls | dirhop scan -f - -p 32
```

With a big list, many targets may be private, deleted, or on dead hosts by the time you scan. Multi-target scans now run preflight automatically. `--preflight` issues one cheap accessibility request first (a max-1 listing request for recognized buckets, or HEAD/tiny GET for ordinary HTTP listings), then scans only the targets that are reachable and publicly listable right now, skipping private/missing/dead/error targets. Use `--no-preflight` if you explicitly want to try every target anyway. On a 15-dead-host sample it finished the check and skipped everything in under 2 seconds instead of waiting on 15 full crawls.

```sh
dirhop scan -f buckets.txt --preflight --parallel 0
# Preflight summary: 128 accessible, 54 private, 210 missing, 8 errored -> scanning 128/400
```

Re-running the same list is cheap: buckets that already completed are skipped
(no re-crawl), and only failed, cancelled, or never-finished ones are retried.
Pass `--rescan` to force a fresh crawl of everything. Failed buckets stay in the
index so you can review and re-run them:

```sh
dirhop scan -f buckets.txt            # second run skips completed buckets
dirhop scan -f buckets.txt --rescan   # force re-crawl of everything
dirhop sessions --status failed       # list buckets that could not be scanned
```

Large runs show live preflight progress (`Preflight: N/M checked`) and can save
every target that could not be scanned (private, missing, errored, or failed) to
a file for retry. That file is re-readable by `scan -f`:

```sh
dirhop scan -f buckets.txt -p 10 --failed-file failed.txt
dirhop scan -f failed.txt -p 10       # retry just the failures later
```

You can also retry failures straight from the index, without keeping any list:

```sh
dirhop scan --retry-failed -p 10      # rescan every failed/cancelled/unfinished session
```

Deletions are tracked across scans. When a rescan no longer sees a file that was
there before, it is kept (soft-deleted) with its last-seen size and timestamp
rather than purged, and `changes` lists what disappeared:

```sh
dirhop -s example-com changes
# TYPE  SIZE     LAST SEEN         PATH
# file  4.3 MiB  2026-01-02 15:04  /releases/old.iso
```

```sh
dirhop sessions
dirhop -s example-com ls
dirhop -s example-com find "*.zip"
dirhop -s example-com stat /releases/file.zip
dirhop -s example-com download /releases/file.zip
```

Inside the shell:

```text
example-com:/ > cd releases
example-com:/releases > ls -lh
example-com:/releases > find "*.iso"
example-com:/releases > download server.iso
example-com:/releases > exit
```

## Commands

- Navigation: `ls`, `cd`, `pwd`, `tree`, `stat`, `cat`, `du`
  - `tree` uses standard `tree(1)` branches (`├──`, `└──`, `│`), alphabetical siblings, a visible directory/file summary, `-L/--depth`, `-d/--dirs-only`, `-f/--files-only`, and `--sizes`.
  - `cat <file> [file...]` fetches indexed files and streams their exact contents to stdout in argument order, without creating a local file or adding separators/newlines.
- Search: `find`, `search`, `urls`
- Discovery: `ghw` (search public buckets via GrayHatWarfare)
- Transfer: `download`
- Index: `refresh`, `errors`, `changes`, `info`
- Sessions: `sessions`, `session list|use|info|rename|delete|refresh`, shell `use`

Important commands support `--json`, `--quiet`, and `--no-color`. Use `--session/-s` to select a session for one command without changing the active session.

Flags follow POSIX conventions: every option has a long `--name` form, and common ones also have a single-letter short form. Short and long forms are interchangeable, short booleans can be bundled (`ls -lah`), and `--flag=value` works everywhere. Unknown flags and mutually exclusive combinations exit with code 2.

Global: `--session/-s`, `--config/-c`, `--url/-u`, `--name/-n`, `--json/-j`, `--quiet/-q`, `--verbose/-v`, `--no-color`, `--debug`, `--workers`.

| Command | Short forms |
| --- | --- |
| `ls` | `--long/-l`, `--human-readable/-h`, `--all/-a`, `--reverse/-r`, `--sort` |
| `tree` | `--depth/-L`, `--dirs-only/-d`, `--files-only/-f`, `--sizes` |
| `find` | `--regex/-r`, `--ext/-e`, `--type/-t`, `--size`, `--modified-after` |
| `urls` | `--files-only/-f`, `--dirs-only/-d`, `--ext/-e`, `--include/-i` |
| `download` | `--output/-o`, `--workers/-w`, `--all/-a`, `--include/-i`, `--exclude/-e`, `--segments`, `--resume`, `--overwrite`, `--skip-existing`, `--max-rate` |
| `scan` | `--file/-f`, `--metadata/-m`, `--parallel/-p`, `--preflight`, `--no-preflight`, `--rescan`, `--failed-file`, `--retry-failed` |
| `ghw` | `--files`, `--type/-t`, `--ext/-e`, `--limit/-l`, `--order`, `--direction`, `--full-path`, `--scan`, `--urls` |
| `refresh` | `--full/-f`, `--metadata/-m` |
| `errors`, `downloads` | `--limit/-l` |
| `session delete` | `--yes/-y` |

```sh
dirhop -s mirror find --ext iso --json
dirhop --session mirror find -e iso -j      # identical
dirhop -s mirror urls --files-only | grep ubuntu
dirhop -s backups du /database
```

## Storage and configuration

The SQLite index is stored in the OS application-data directory:

- Linux: `${XDG_DATA_HOME:-~/.local/share}/dirhop/dirhop.db`
- macOS: `~/Library/Application Support/dirhop/dirhop.db`
- Windows: `%LocalAppData%\\dirhop\\dirhop.db`

Copy `config.example.toml` to the displayed config path from `dirhop config path`. Command flags override configuration. The file uses flat TOML: `key = value`, quoted or bare values, `# comments`, and optional `[section]` headers. Nested tables and arrays are not used. Unknown keys are rejected so typos are caught. Aliases `workers`, `timeout`, and `retry_count` are accepted.

## Crawling behavior

Public object-storage buckets are indexed through their documented anonymous listing APIs, not HTML scraping. Amazon S3, Google Cloud Storage, and DigitalOcean Spaces (and any other S3-compatible store) use ListObjectsV2 (`?list-type=2`, paginated by continuation token); Azure Blob Storage uses List Blobs (`?restype=container&comp=list`, paginated by `marker`). Recognized URLs:

- `https://<bucket>.s3[.<region>].amazonaws.com/` and `https://s3.amazonaws.com/<bucket>/`
- `https://storage.googleapis.com/<bucket>/` and `https://<bucket>.storage.googleapis.com/`
- `https://<bucket>.<region>.digitaloceanspaces.com/` (and the `.cdn.` alias) and `https://<region>.digitaloceanspaces.com/<bucket>/`
- `https://<account>.blob.core.windows.net/<container>/`

each optionally followed by a key prefix. Key prefixes become directories; object size, ETag, and LastModified are stored. A bucket or container that refuses anonymous listing exits with code 7.

Custom domains are also supported. A CDN hostname CNAMEd to S3, GCS, R2, or MinIO (for example `https://cdn.example.com/`) matches no hostname rule, so dirhop falls back to asking the origin: if the root is not parseable HTML, it issues one `?list-type=2` request and uses the bucket path when the response is a real `ListBucketResult`. Ordinary websites are unaffected, since the probe runs only after HTML parsing has already failed.

Listing prefers the V2 API. Servers that only implement the older V1 listing (no `KeyCount`, no continuation token) are detected from the first response and paged with `marker` instead, resuming from `NextMarker` or the last key of the page, so they are indexed in full rather than truncated at 1000 objects. A server that claims more results but never advances its cursor stops instead of looping.

Bucket listing is parallel. Folders (`delimiter=/` prefixes) are listed concurrently, and large flat folders are split into `start-after` key ranges so one folder with tens of thousands of objects is not a single sequential page chain. Default concurrency is 4x `crawl_concurrency` (max 32) for buckets. Override per run with `--workers N` (max 64) on `scan`/`refresh`/`open`, for both buckets and HTML listings.

The crawler uses bounded concurrency, HTTP keep-alive, retries, redirect checks, same-host and base-path restrictions, duplicate prevention, and parser detection for common Apache, nginx, Python, and generic HTML listings. It follows only links present in listing pages. Normal browsing and searching query SQLite and do not make network requests.

## Discovering buckets (GrayHatWarfare)

`ghw` searches the [GrayHatWarfare](https://grayhatwarfare.com/) public-bucket index, which covers exactly the bucket types dirhop can browse (AWS S3, Azure Blob, DigitalOcean Spaces, Google Cloud). It needs an API key: set `grayhatwarfare_api_key` in the config, or export `GRAYHATWARFARE_API_KEY` (the environment wins, so the key need not be written to disk).

```sh
dirhop ghw backup                    # buckets whose name matches "backup"
dirhop ghw --type azure company      # Azure containers only
dirhop ghw --files --ext sql,zip dump  # files, filtered by extension
dirhop ghw invoices --scan           # index every matched bucket into dirhop
dirhop ghw secrets --urls | dirhop scan -f -   # pipe matches into a batch scan
```

Bucket search is the default; `--files` searches individual files. `--scan` feeds the matched bucket URLs straight into dirhop's indexer so you can immediately `ls`/`find`/`download` them, and `--urls` prints just the bucket URLs for piping into `scan -f -`. `--json` emits raw results. GrayHatWarfare only indexes buckets that are already publicly listable; dirhop itself only issues listing requests, exactly as with any other bucket. Respect the targets' terms and the law.

Refresh explicitly with:

```sh
dirhop -s example-com refresh
dirhop -s example-com refresh --full
dirhop -s example-com refresh --metadata full   # adds one HEAD per file: exact size, MIME, ETag
```

Metadata levels: `minimal`/`normal` use only what the listing shows (default); `full` additionally issues bounded HEAD requests, never GETs. Supported listing formats: Apache, nginx, Python `http.server`, IIS, lighttpd, Caddy, and generic anchor pages. Entry names come from link targets, so truncated Apache labels (`long-na..>`) are handled.

`--verbose` prints one line per HTTP request (method, redacted URL, status, time). Headers, cookies, credentials, and query values are never logged.

The interactive shell runs the exact same commands as the CLI, so every flag works in both (`find --size '>1GB'`, `ls -lh --sort size`). History persists across sessions; Tab completes commands, remote paths, and session names. `dirhop` with no URL reopens the active session, or offers a picker when none is active (scripts get exit code 3 instead).

## Download behavior

Downloads are always explicit. Files are written through `.part` files, can resume with validated range responses, verify known lengths, and atomically rename on success. Directories are expanded from the local index and downloaded with bounded workers while preserving hierarchy.

```sh
dirhop -s example-com download /software/ --output ./downloads --workers 6
dirhop -s example-com download /huge.iso --segments 8 --resume
```

## Development

```sh
make fmt
make test
make vet
make build
```

Architecture and security decisions are documented in [`docs/architecture.md`](docs/architecture.md). Extended guides (install, commands, buckets, configuration, FAQ) live in [`docs/wiki/`](docs/wiki/) and mirror the GitHub wiki.

Contributions are welcome: see [`CONTRIBUTING.md`](CONTRIBUTING.md), [`SECURITY.md`](SECURITY.md), and [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).

## License

MIT
