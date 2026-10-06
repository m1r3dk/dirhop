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
- `crawl_concurrency` / `workers` - default crawl/bucket worker base
- `download_workers`
- `http_timeout` / `timeout`
- `retries` / `retry_count`
- `busy_timeout` - SQLite busy timeout
- `metadata` - `minimal`, `normal`, or `full`
- `user_agent`
- `grayhatwarfare_api_key` - see [[GrayHatWarfare]]

## Environment variables

- `GRAYHATWARFARE_API_KEY` - GrayHatWarfare API key; takes precedence over the
  config value so the key need not be written to disk.
