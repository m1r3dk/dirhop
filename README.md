# dirhop

`dirhop` turns an authorized HTTP/HTTPS directory-listing website into a persistent local metadata index that behaves like a remote filesystem. Crawls fetch directory pages only. File content is transferred only after an explicit `download` command.

> Use `dirhop` only with public directory listings or systems you are explicitly authorized to access. It does not discover unlinked paths or bypass access controls.

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

Index many sites at once from a file (one URL per line, optional session name, `#` comments):

```sh
cat > urls.txt <<'EOF'
https://mirror.example.com/pub/      mirror
https://example-bucket.s3.amazonaws.com/
# https://disabled.example.com/
EOF
dirhop scan -f urls.txt            # or: cat urls.txt | dirhop scan -f -
dirhop scan https://a.example/ https://b.example/
```

Duplicate URLs are skipped. One failing site does not stop the others; the exit code reflects the first failure. `--json` prints a per-URL result list.

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

- Navigation: `ls`, `cd`, `pwd`, `tree`, `stat`, `du`
- Search: `find`, `search`, `urls`
- Transfer: `download`
- Index: `refresh`, `errors`, `info`
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
| `scan` | `--file/-f`, `--metadata/-m` |
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

Public Amazon S3 and Google Cloud Storage buckets are indexed through the documented anonymous ListObjectsV2 API (`?list-type=2`, paginated by continuation token), not HTML scraping. Recognized URLs: `https://<bucket>.s3[.<region>].amazonaws.com/`, `https://s3.amazonaws.com/<bucket>/`, `https://storage.googleapis.com/<bucket>/`, `https://<bucket>.storage.googleapis.com/`, each optionally followed by a key prefix. Key prefixes become directories; object size, ETag, and LastModified are stored. A bucket that refuses anonymous listing exits with code 7.

Bucket listing is parallel. Folders (`delimiter=/` prefixes) are listed concurrently, and large flat folders are split into `start-after` key ranges so one folder with tens of thousands of objects is not a single sequential page chain. Default concurrency is 4x `crawl_concurrency` (max 32) for buckets. Override per run with `--workers N` (max 64) on `scan`/`refresh`/`open`, for both buckets and HTML listings.

The crawler uses bounded concurrency, HTTP keep-alive, retries, redirect checks, same-host and base-path restrictions, duplicate prevention, and parser detection for common Apache, nginx, Python, and generic HTML listings. It follows only links present in listing pages. Normal browsing and searching query SQLite and do not make network requests.

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

Architecture and security decisions are documented in [`docs/architecture.md`](docs/architecture.md).

## License

MIT
