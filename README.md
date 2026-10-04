# dirclone

`dirclone` turns an authorized HTTP/HTTPS directory-listing website into a persistent local metadata index that behaves like a remote filesystem. Crawls fetch directory pages only. File content is transferred only after an explicit `download` command.

> Use `dirclone` only with public directory listings or systems you are explicitly authorized to access. It does not discover unlinked paths or bypass access controls.

## Install

```sh
go install github.com/m1r3dk/dirclone/cmd/dirclone@latest
```

Or build locally:

```sh
make build
./bin/dirclone --help
```

## Quick start

Open a listing for the first time, crawl it, and enter the interactive shell:

```sh
dirclone https://example.com/
```

Later, the same command reopens the persisted index without crawling again.

```sh
dirclone sessions
dirclone -s example-com ls
dirclone -s example-com find "*.zip"
dirclone -s example-com stat /releases/file.zip
dirclone -s example-com download /releases/file.zip
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

```sh
dirclone -s mirror find --ext iso --json
dirclone -s mirror urls --files-only | grep ubuntu
dirclone -s backups du /database
```

## Storage and configuration

The SQLite index is stored in the OS application-data directory:

- Linux: `${XDG_DATA_HOME:-~/.local/share}/dirclone/dirclone.db`
- macOS: `~/Library/Application Support/dirclone/dirclone.db`
- Windows: `%LocalAppData%\\dirclone\\dirclone.db`

Copy `config.example.toml` to the displayed config path from `dirclone config path`. Command flags override configuration.

## Crawling behavior

Public Amazon S3 and Google Cloud Storage buckets are indexed through the documented anonymous ListObjectsV2 API (`?list-type=2`, paginated by continuation token), not HTML scraping. Recognized URLs: `https://<bucket>.s3[.<region>].amazonaws.com/`, `https://s3.amazonaws.com/<bucket>/`, `https://storage.googleapis.com/<bucket>/`, `https://<bucket>.storage.googleapis.com/`, each optionally followed by a key prefix. Key prefixes become directories; object size, ETag, and LastModified are stored. A bucket that refuses anonymous listing exits with code 7.

The crawler uses bounded concurrency, HTTP keep-alive, retries, redirect checks, same-host and base-path restrictions, duplicate prevention, and parser detection for common Apache, nginx, Python, and generic HTML listings. It follows only links present in listing pages. Normal browsing and searching query SQLite and do not make network requests.

Refresh explicitly with:

```sh
dirclone -s example-com refresh
dirclone -s example-com refresh --full
dirclone -s example-com refresh --metadata full   # adds one HEAD per file: exact size, MIME, ETag
```

Metadata levels: `minimal`/`normal` use only what the listing shows (default); `full` additionally issues bounded HEAD requests, never GETs. Supported listing formats: Apache, nginx, Python `http.server`, IIS, lighttpd, Caddy, and generic anchor pages. Entry names come from link targets, so truncated Apache labels (`long-na..>`) are handled.

`--verbose` prints one line per HTTP request (method, redacted URL, status, time). Headers, cookies, credentials, and query values are never logged.

The interactive shell runs the exact same commands as the CLI, so every flag works in both (`find --size '>1GB'`, `ls -lh --sort size`). History persists across sessions; Tab completes commands, remote paths, and session names. `dirclone` with no URL reopens the active session, or offers a picker when none is active (scripts get exit code 3 instead).

## Download behavior

Downloads are always explicit. Files are written through `.part` files, can resume with validated range responses, verify known lengths, and atomically rename on success. Directories are expanded from the local index and downloaded with bounded workers while preserving hierarchy.

```sh
dirclone -s example-com download /software/ --output ./downloads --workers 6
dirclone -s example-com download /huge.iso --segments 8 --resume
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
