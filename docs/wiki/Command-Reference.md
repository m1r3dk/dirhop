# Command Reference

Complete reference for every `dirhop` command. Conventions: `<required>`,
`[optional]`, `|` alternatives. Short and long flags are interchangeable.
Global flags ([[Flags-and-Exit-Codes]]) are omitted from each synopsis.

**Command groups**

- [Navigation](#navigation) - `ls` `cd` `pwd` `tree` `stat` `cat` `du`
- [Search](#search) - `find` `search` `urls`
- [Transfer](#transfer) - `download` `downloads`
- [Indexing](#indexing) - `scan` `refresh` `open` `shell`
- [Inspection](#inspection) - `info` `stats` `changes` `errors`
- [Sessions](#sessions) - `sessions` `session`
- [Discovery](#discovery) - `ghw`
- [Maintenance](#maintenance) - `db`
- [Meta](#meta) - `config` `version`

Read operations query the local SQLite index only; they issue no network
requests. `scan`, `refresh`, `open`, `download`, and `ghw` are the only commands
that hit the network.

---

## Navigation

### `ls`

```
dirhop [-s SESSION] ls [PATH] [-l] [-h] [-a] [-r] [--sort name|size|date]
```

List a directory from the index.

| Flag | Short | Default | Description |
| --- | --- | --- | --- |
| `--long` | `-l` | false | Long format: size, date, type. |
| `--human-readable` | `-h` | false | Human-readable sizes. |
| `--all` | `-a` | false | Include hidden entries. |
| `--reverse` | `-r` | false | Reverse sort. |
| `--sort` | | `name` | Sort key: `name`, `size`, `date`. |

```sh
dirhop -s mirror ls -lah --sort size --reverse /releases
```

### `cd`

```
cd [PATH]
```

Change the persisted working directory. Primarily a shell command; a bare `cd`
resets to `/`. One-shot `dirhop cd PATH` persists the CWD for the session.

### `pwd`

```
dirhop [-s SESSION] pwd
```

Print the persisted working directory.

### `tree`

```
dirhop [-s SESSION] tree [PATH] [-L N] [-d] [-f] [--sizes]
```

Render the hierarchy in `tree(1)` format.

| Flag | Short | Default | Description |
| --- | --- | --- | --- |
| `--depth` | `-L` | 0 (all) | Maximum depth. |
| `--dirs-only` | `-d` | false | Directories only. |
| `--files-only` | `-f` | false | Files only. |
| `--sizes` | | false | Exact byte sizes. |

### `stat`

```
dirhop [-s SESSION] stat <PATH>
```

Print metadata for one entry: path, type, size, modified time, content type,
URL, ETag, Last-Modified. Supports `--json`.

### `cat`

```
dirhop [-s SESSION] cat <FILE> [FILE...]
```

Fetch indexed files and stream exact contents to stdout in argument order. No
local file is written and no separators are added. **Network command** (fetches
content on demand).

### `du`

```
dirhop [-s SESSION] du [PATH] [-h]
```

Summarize indexed disk usage. `--human-readable`/`-h` defaults to true. Supports
`--json`.

---

## Search

### `find`

```
dirhop [-s SESSION] find [PATTERN] [-r REGEX] [-e EXT] [--size EXPR]
                         [--modified-after DATE] [-t file|directory] [--all-sites]
```

Match indexed paths by glob (`PATTERN`), regex, or metadata.

| Flag | Short | Description |
| --- | --- | --- |
| `--regex` | `-r` | Match with a regular expression. |
| `--ext` | `-e` | Extension(s), comma-separated. |
| `--size` | | Size filter: `>1GB`, `<10MB`, `100MB..2GB`. |
| `--modified-after` | | `YYYY-MM-DD`, inclusive. |
| `--type` | `-t` | `file` or `directory`. |
| `--all-sites` | | Search every session; emit `session:path`. |

```sh
dirhop -s mirror find --ext iso --size '>1GB' --json
dirhop find --all-sites --regex 'backup.*\.tar\.gz'
```

### `search`

```
dirhop [-s SESSION] search <TEXT> [--all-sites]
```

Substring search over names and paths. `--all-sites` searches every session.
Supports `--json`.

### `urls`

```
dirhop [-s SESSION] urls [PATH] [-f] [-d] [-e EXT] [-i PATTERN]
```

Print source URLs for indexed entries.

| Flag | Short | Description |
| --- | --- | --- |
| `--files-only` | `-f` | File URLs only. |
| `--dirs-only` | `-d` | Directory URLs only. |
| `--ext` | `-e` | Filter by extension. |
| `--include` | `-i` | Glob or substring filter. |

---

## Transfer

### `download`

```
dirhop [-s SESSION] download <PATH...> | --all | --all-sites
                    [-o DIR] [-w N] [--segments N] [--max-rate RATE]
                    [-i PATTERN] [-e PATTERN] [--ext LIST]
                    [--resume] [--overwrite] [--skip-existing]
```

Download files or directories. Writes through `.part` files with atomic rename;
interrupted transfers resume. The only command that transfers bulk content.

| Flag | Short | Default | Description |
| --- | --- | --- | --- |
| `--output` | `-o` | config | Destination directory. |
| `--workers` | `-w` | config | Concurrent file transfers. |
| `--all` | `-a` | false | Every indexed file in the session. |
| `--all-sites` | | false | Matches from every session into per-session subdirs. |
| `--ext` | | | With `--all-sites`, restrict to these extensions. |
| `--include` | `-i` | | Include paths matching glob/substring. |
| `--exclude` | `-e` | | Exclude paths matching glob/substring. |
| `--segments` | | 1 | Parallel byte-range segments for large files. |
| `--resume` | | true | Resume partial files via validated ranges. |
| `--overwrite` | | false | Replace existing files. |
| `--skip-existing` | | false | Leave existing files untouched. |
| `--max-rate` | | | Approximate B/s ceiling, e.g. `10MB`. |

`--overwrite` and `--skip-existing` are mutually exclusive. Supports `--json`
for the result summary.

```sh
dirhop -s mirror download /big.iso --segments 8 --max-rate 20MB -o ./out
dirhop download --all-sites --include '*secret*' -o ./matches
```

### `downloads`

```
dirhop [-s SESSION] downloads [-l N]
```

Show recorded download state. `--limit`/`-l` defaults to 100. Supports `--json`.

---

## Indexing

### `scan`

```
dirhop scan [URL...] [-f FILE] [-m LEVEL] [-p N]
            [--preflight] [--no-preflight] [--rescan] [--retry-failed]
            [--failed-file PATH] [--preflight-file PATH]
```

Index one or more targets. URLs may be bare hosts (`https://` is inferred when a
dot precedes any `/`, `?`, or `#`). Re-running a list skips completed sessions;
failed/cancelled/unfinished ones are retried.

| Flag | Short | Default | Description |
| --- | --- | --- | --- |
| `--file` | `-f` | | Read targets from FILE, one per line (`-` = stdin). |
| `--metadata` | `-m` | `normal` | `minimal`, `normal`, `full`. |
| `--parallel` | `-p` | config | Index N targets concurrently; `0` = auto. |
| `--preflight` | | false | Reachability check first; skip private/missing/errored. |
| `--no-preflight` | | false | Disable automatic preflight on multi-target scans. |
| `--rescan` | | false | Re-crawl already-completed targets. |
| `--failed-file` | | | Write unreachable targets to PATH for retry. |
| `--preflight-file` | | `preflight.md` | Readable preflight report path. |
| `--retry-failed` | | false | Rescan failed/cancelled/unfinished sessions from the index. |

Target file grammar: one target per line; optional whitespace-separated second
field sets the session name; `#` begins a comment; blank lines ignored.

```sh
dirhop scan -f buckets.txt -p 0 --preflight --failed-file failed.txt
dirhop ghw secrets --urls | dirhop scan -f - -p 32
```

Multi-target runs preflight automatically unless `--no-preflight`. See
[[Buckets]] for provider URL forms.

### `refresh`

```
dirhop [-s SESSION] refresh [-f] [-m LEVEL]
```

Re-index the selected session.

| Flag | Short | Default | Description |
| --- | --- | --- | --- |
| `--full` | `-f` | false | Force complete reconciliation. |
| `--metadata` | `-m` | config | `minimal`, `normal`, `full`. |

### `open`

```
dirhop open <URL> [-n NAME]
```

Create or open a session by URL and enter the interactive shell.

### `shell`

```
dirhop [-s SESSION] shell
```

Open the interactive shell on the selected or active session. Tab completion for
commands, remote paths, and session names; persistent history; `Ctrl+D`/`exit`
to quit.

---

## Inspection

### `info`

```
dirhop [-s SESSION] info
```

Session details: URL, host, parser, CWD, counts, size, status, last crawl.
Supports `--json`.

### `stats`

```
dirhop stats [--top N]
```

Aggregate totals across all sessions: sessions by status, files, directories,
storage, soft-deleted rows, entry types, extension breakdown. `--top N` limits
the extension list. Supports `--json`.

### `changes`

```
dirhop [-s SESSION] changes [-l N]
```

List entries soft-deleted since the last successful scan (kept with last-seen
size and timestamp). `--limit`/`-l` defaults to 0 (all). Supports `--json`.

### `errors`

```
dirhop [-s SESSION] errors [-l N]
```

Crawl errors for the session. `--limit`/`-l` defaults to 100. Supports `--json`.

---

## Sessions

### `sessions`

```
dirhop sessions [--status STATE]
```

List sessions. `--status` filters by `complete|failed|pending|running|cancelled`.
Supports `--json`.

### `session`

```
dirhop session <subcommand>
```

| Subcommand | Synopsis | Description |
| --- | --- | --- |
| `list` | `[--status STATE]` | Same as `sessions`. |
| `use` | `<NAME>` | Set the active session. |
| `info` | `[NAME]` | Session details. |
| `rename` | `<OLD> <NEW>` | Rename a session. |
| `delete` | `<NAME> [-y]` | Delete the local index. `--yes`/`-y` required to execute. |
| `refresh` | `<NAME> [-f]` | Refresh a named session. `--full`/`-f`. |
| `normalize-names` | | Rename sessions to their bucket name. |

In the shell, `use <number|name>` switches session (numbers index the
`sessions` listing).

---

## Discovery

### `ghw`

```
dirhop ghw [KEYWORDS...] [--files] [-t TYPE] [-e EXT] [-l N]
           [--order KEY] [--direction asc|desc] [--full-path] [--scan] [--urls]
```

Search the GrayHatWarfare public-bucket index. Requires an API key
([[GrayHatWarfare]]). **Network command.**

| Flag | Short | Description |
| --- | --- | --- |
| `--files` | | Search files instead of buckets. |
| `--type` | `-t` | `aws`, `azure`, `dos`, `gcp`, `ali` (files accept a comma list). |
| `--ext` | `-e` | Extensions, comma-separated (files only). |
| `--limit` | `-l` | Max results (1-1000). |
| `--order` | | Sort key: buckets `fileCount\|bucketName`, files `size\|last_modified`. |
| `--direction` | | `asc` or `desc`. |
| `--full-path` | | Match keywords against the full file path (files only). |
| `--scan` | | Index each matched bucket into a session. |
| `--urls` | | Print only bucket URLs (for piping into `scan -f -`). |

Supports `--json` for raw results.

---

## Maintenance

### `db`

```
dirhop db <subcommand>
```

Inspect and reclaim space in the local index. Mutating subcommands are dry-run
by default; pass `--apply` to execute.

| Subcommand | Synopsis | Description |
| --- | --- | --- |
| `status` | | Per-session live vs. soft-deleted row counts. Supports `--json`. |
| `duplicates` | | Sessions indexing the same bucket under different URLs. Supports `--json`. |
| `prune` | `[--apply]` | Hard-delete soft-removed rows. |
| `vacuum` | | Checkpoint WAL and `VACUUM` to shrink the file. |
| `clean` | `[--apply]` | Dedupe sessions, prune rows, then vacuum. |

```sh
dirhop db status
dirhop db clean            # dry run
dirhop db clean --apply    # execute
```

`vacuum` rewrites the whole file and temporarily needs free space roughly equal
to the database size.

---

## Meta

### `config path`

```
dirhop config path
```

Print the resolved configuration file path. See [[Configuration]].

### `version`

```
dirhop version [--json]
dirhop --version | -V
```

Print version, commit, build date, Go version, and platform. Runs without
opening the database; safe in CI and scripts. `--json` emits structured build
metadata.

---

See [[Flags-and-Exit-Codes]] for global flags, exit codes, and `--json`
scripting patterns; [[Recipes]] for end-to-end workflows.
