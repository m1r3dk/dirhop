# dirhop architecture

## Scope and command routing

`dirhop` treats an authorized HTTP directory listing as a persistent, read-only remote filesystem. Crawling retrieves directory pages and listing metadata only. File bodies are requested only by `download`.

The root command handles three entry forms:

- `dirhop <URL>` opens the URL's session, creates and crawls it if absent, makes it active, then starts the shell.
- `dirhop --url <URL> <command>` selects or creates that URL's session and runs one command without starting the shell.
- `dirhop <command>` uses `--session/-s` when supplied, otherwise the active session.

A URL maps to a stored session by its canonical URL, not its generated name. Canonicalization lowercases scheme and host, strips fragments and default ports, cleans the escaped path, ensures a directory trailing slash, and removes directory-list sorting queries. Opening the same canonical URL therefore reuses the session without crawling again.

## Package boundaries

- `internal/config`: application-data paths and simple configuration loading.
- `internal/model`: shared persistent and transport types.
- `internal/database`: SQLite lifecycle, schema, transactions, and persistence queries.
- `internal/session`: session naming, lookup, activation, and lifecycle.
- `internal/httpclient`: bounded, retrying HTTP transport with safe logging.
- `internal/parser`: parser interface, detector, and format-specific/generic HTML parsers.
- `internal/crawler`: recursive same-host crawl orchestration and batched index writes.
- `internal/filesystem`: the SQLite-backed virtual filesystem used by every interface.
- `internal/downloader`: explicit bounded downloads, resume, range, hierarchy preservation.
- `internal/output`: human/JSON rendering helpers.
- `internal/shell`: terminal loop, history, tokenization, and index-only completion.
- `internal/cli`: Cobra commands and exit-code mapping.
- `internal/app`: shared service composition and operations invoked by CLI and shell.

## Session model

A site stores identity, generated or user-supplied name, original and canonical base URLs, hostname, parser type, timestamps, independent current working directory, aggregate counts, scan status, and crawl configuration. A singleton `app_state` row stores the globally active site. `--session` resolves without changing that row. Shell `use` and `session use` update it.

Each site has a synthetic root entry at normalized path `/`. Entries are unique by `(site_id, normalized_path)`. Parent IDs are stored for fast directory listing, while normalized paths support direct resolution and prefix scans. Paths are decoded filesystem names; URLs retain valid escaped HTTP paths.

## SQLite and multi-process behavior

One database is stored in the OS user data directory. Connections enable WAL, foreign keys, a busy timeout, and normal synchronous mode. Reads use short implicit transactions. Crawls parse concurrently but send mutations through one batched writer transaction at a time. No process-wide lock is used, so read-only commands and downloads can run while another process crawls.

Core tables are `sites`, `entries`, `crawl_runs`, `crawl_errors`, `downloads`, and `app_state`. Indexes cover site/parent, site/path, name, extension, type, size, and modified time.

## Parser contract

`DirectoryParser` exposes `Name`, `Score(document)`, and `Parse(document, pageURL)`. The detector selects the highest-scoring parser. Apache, nginx/Python-style preformatted listings, and a conservative generic anchor parser share HTML traversal helpers but keep detection rules separate. Parsed links are hints. The crawler independently enforces URL and hierarchy safety.

## URL and listing safety

The crawler only follows links that resolve under the canonical base path and, by default, on the same scheme/host. It rejects parent links, fragments, known sort queries, non-HTTP schemes, paths outside the base, duplicate canonical directory URLs, and file-looking links as directories unless listing structure marks them as such. It never guesses unlinked paths.

Encoded path segments are preserved in request URLs and decoded once for display. Query-only sort links, malformed links, duplicate aliases, redirects outside the allowed root, loops, empty names, and conflicting file/directory entries are ignored or recorded as crawl errors.

## Crawler concurrency

A bounded work queue feeds a fixed directory-worker pool. A synchronized visited set prevents loops before enqueue. Workers use one pooled `http.Client`, parse directory pages, and send entry batches to a single database writer. Retries use capped exponential backoff and honor `Retry-After`. Failure of one directory is recorded and does not cancel unrelated work. Context cancellation stops enqueueing and lets transactions finish cleanly.

Normal metadata mode trusts listing metadata. Full mode may make bounded conditional `HEAD` requests for files. Crawling never issues file `GET` requests.

## Filesystem API

`filesystem.FS` resolves absolute and relative paths against the site's persisted CWD and implements `List`, `ChangeDirectory`, `Stat`, `Walk`, `Find`, `Search`, `DiskUsage`, and `URLs`. All are SQLite queries or streaming row scans. The shell and Cobra commands call the same application methods over this API.

## Refresh model

Each run has a unique timestamp. Upserts set `last_seen_at` to that run. A successful full refresh marks entries not seen in the run as removed. Incremental refresh uses cached directory ETag/Last-Modified values where available and preserves children for `304 Not Modified` directories. `--full` bypasses validators and reconciles the complete tree.

## Download architecture

The filesystem expands a requested file or directory to indexed file entries. A bounded worker pool writes `.part` files beneath the destination while preserving relative hierarchy. Resume sends `Range` from the partial size and only appends after a valid `206`; otherwise it safely restarts. Completed size is checked when known, then the file is atomically renamed. Segmented downloads first probe range support, write non-overlapping pieces, validate every range, merge once, and fall back to one stream if the server ignores ranges. Download state is persisted for restart visibility.

## Interactive terminal

The shell uses `golang.org/x/term` for raw mode, editable history, arrows, Ctrl+C/Ctrl+D, and tab callback support. Quoted arguments use a small shell tokenizer. Completion reads children from SQLite only and never accesses the network. Every session restores its own persisted CWD.

## Exit codes

- `0`: success
- `2`: invalid arguments
- `3`: no active/selected session
- `4`: indexed path not found
- `5`: network or crawl failure
- `6`: download failure
- `7`: unsupported directory listing
- `1`: other internal failure
