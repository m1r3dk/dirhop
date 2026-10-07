# Configuration

## Storage location

The SQLite index lives in the OS application-data directory:

- Linux: `${XDG_DATA_HOME:-~/.local/share}/dirhop/dirhop.db`
- macOS: `~/Library/Application Support/dirhop/dirhop.db`
- Windows: `%LocalAppData%\dirhop\dirhop.db`

Show the resolved config path with:

```sh
dirhop config path
```

## Config file

Copy `config.example.toml` to the displayed config path. Command flags override
configuration. The file uses flat TOML: `key = value`, quoted or bare values,
`# comments`, and optional `[section]` headers. Nested tables and arrays are not
used. Unknown keys are rejected so typos are caught. Aliases `workers`,
`timeout`, and `retry_count` are accepted.

Common keys:

- `database` - path to the SQLite index
- `shard_dir` - directory holding entry shards (default: `shards/` beside the database)
- `shard_count` - number of entry-storage shards, 1-32, default 16 (see below)
- `crawl_concurrency` / `workers` - default crawl/bucket worker base
- `download_workers`
- `http_timeout` / `timeout`
- `retries` / `retry_count`
- `busy_timeout` - SQLite busy timeout
- `metadata` - `minimal`, `normal`, or `full`
- `user_agent`
- `grayhatwarfare_api_key` - see [[GrayHatWarfare]]

## Shard count

To index very large numbers of buckets without every scan serializing on
SQLite's single writer, entries are distributed across a fixed set of shard
databases. `shard_count` sets how many (1-32, default 16). Pick a value near your
scan parallelism (16-20 is typical, up to 32).

It is fixed for a dataset once chosen: each bucket's shard assignment is stored,
so changing `shard_count` later does not silently remap existing data (a future
re-shard tool will handle that explicitly). See
`docs/adr/0001-sharded-storage.md` for the full design.

## Environment variables

- `GRAYHATWARFARE_API_KEY` - GrayHatWarfare API key; takes precedence over the
  config value so the key need not be written to disk.
