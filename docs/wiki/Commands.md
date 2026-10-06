# Commands

All commands work both as one-shot CLI subcommands and inside the interactive
shell. Important commands support `--json`, `--quiet`, and `--no-color`. Use
`--session/-s` to target a session for one command without changing the active
one.

## Global flags

`--session/-s`, `--config/-c`, `--url/-u`, `--name/-n`, `--json/-j`,
`--quiet/-q`, `--verbose/-v`, `--no-color`, `--debug`, `--workers`.

Flags follow POSIX conventions: every option has a long `--name`, common ones a
short form; short booleans bundle (`ls -lah`); `--flag=value` works everywhere.
Unknown flags and mutually exclusive combinations exit with code 2.

## Navigation

- `ls [path]` - `--long/-l`, `--human-readable/-h`, `--all/-a`, `--reverse/-r`, `--sort`
- `cd`, `pwd`
- `tree` - `--depth/-L`, `--dirs-only/-d`, `--files-only/-f`, `--sizes`
- `stat <path>`
- `cat <file> [file...]` - streams exact indexed file contents to stdout
- `du [path]`

## Search

- `find <pattern>` - `--regex/-r`, `--ext/-e`, `--type/-t`, `--size`, `--modified-after`
- `search <text>`
- `urls` - `--files-only/-f`, `--dirs-only/-d`, `--ext/-e`, `--include/-i`

## Discovery

- `ghw <query>` - search public buckets via GrayHatWarfare
  (`--files`, `--type/-t`, `--ext/-e`, `--limit/-l`, `--order`, `--direction`,
  `--full-path`, `--scan`, `--urls`). See [[GrayHatWarfare]].

## Transfer

- `download <path...>` - `--output/-o`, `--workers/-w`, `--all/-a`,
  `--include/-i`, `--exclude/-e`, `--segments`, `--resume`, `--overwrite`,
  `--skip-existing`, `--max-rate`

## Index

- `scan [URL...] [-f FILE]` - `--file/-f`, `--metadata/-m`, `--parallel/-p`, `--preflight`, `--no-preflight` (full rescan, multi-target scans preflight automatically, `--parallel 0` auto-scales workers)
- `scan` also takes `--rescan` to re-crawl buckets that already completed (default skips them)
- `refresh` - `--full/-f`, `--metadata/-m`
- `errors`, `downloads` - `--limit/-l`
- `changes` - `--limit/-l` (files removed since the last successful scan)
- `info`

## Sessions

- `sessions`, `session list|use|info|rename|delete|refresh`
- `session normalize-names` renames existing sessions to their bucket name (e.g. `wustl`)
- `sessions --status complete|failed|pending|running|cancelled` filters by scan status
- `session delete` - `--yes/-y`
- shell `use <name|number>` to switch the active session

## Examples

```sh
dirhop scan -f buckets.txt --preflight --parallel 0
dirhop -s mirror find --ext iso --json
dirhop -s mirror urls --files-only | grep ubuntu
dirhop -s backups du /database
dirhop -s mirror find --size '>1GB'
dirhop -s mirror ls -lh --sort size
```
