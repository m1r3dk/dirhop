# Flags & Exit Codes

One place for the cross-cutting details: global flags, how to script dirhop with
`--json`, and what every exit code means.

---

## Global flags

These apply to (almost) every command. Put them anywhere on the line; short and
long forms are interchangeable; `--flag=value` works everywhere.

| Flag | Short | Type | Meaning |
| --- | --- | --- | --- |
| `--session` | `-s` | string | Act on this session for one command without switching the active one. |
| `--config` | `-c` | string | Use a specific config file. |
| `--url` | `-u` | string | Select/create a session by URL for this command only. |
| `--name` | `-n` | string | Custom name when creating a session by URL. |
| `--json` | `-j` | bool | Emit JSON instead of a table. |
| `--quiet` | `-q` | bool | Suppress non-essential output. |
| `--verbose` | `-v` | bool | One line per HTTP request (redacted URLs). |
| `--version` | `-V` | bool | Print version and exit. |
| `--no-color` | | bool | Disable ANSI colors. |
| `--debug` | | bool | Extra diagnostics. |
| `--workers` | | int | Crawl/listing concurrency for this run (max 64). |

POSIX conventions:

- Every option has a long `--name`. Common ones also have a one-letter short.
- Short booleans can bundle: `ls -lah` == `ls -l -a -h`.
- `--flag=value` and `--flag value` are both accepted.
- Unknown flags or mutually exclusive combinations exit with code `2`.

---

## Exit codes

dirhop returns a specific exit code so scripts can branch on the outcome.

| Code | Name | When |
| --- | --- | --- |
| `0` | success | Everything worked. |
| `1` | generic error | An error with no more specific code. |
| `2` | invalid arguments | Bad flag, bad value, or conflicting options. |
| `3` | no session | No active/selected session (e.g. nothing to open). |
| `4` | path not found | The indexed path does not exist. |
| `5` | network / crawl failure | A target could not be crawled (first failure wins). |
| `6` | download failure | A download could not complete. |
| `7` | unsupported listing | Target exists but refuses anonymous listing or is not a listing. |

Check the code in a shell:

```sh
dirhop -s mirror ls /nope
echo $?        # prints 4
```

Branch on it:

```sh
if dirhop -s mirror find --ext iso --quiet >/dev/null; then
  echo "found ISOs"
else
  code=$?
  echo "no match or error (exit $code)"
fi
```

---

## Scripting with `--json`

Most read commands emit structured JSON with `--json`, ideal for `jq` and
automation.

```sh
# List of entries -> just the paths:
dirhop -s mirror find --ext iso --json | jq -r '.[].path'

# Total file count from stats:
dirhop stats --json | jq '.Files'

# Per-session row counts:
dirhop db status --json | jq -r '.[] | "\(.Site.Name) \(.LiveEntries)"'

# Build metadata (great for bug reports):
dirhop version --json
```

Tips:

- Combine `--json` with `--quiet` to keep stdout clean for piping.
- `version`, `stats`, `find`, `search`, `urls`, `ls`, `info`, `sessions`,
  `download`, `downloads`, `changes`, `errors`, and `db status` all support
  `--json`.
- JSON goes to stdout; progress/log noise goes to stderr, so pipes stay clean.

---

## Selecting a session

Three ways, in order of precedence for a single command:

```sh
dirhop -s mirror ls              # by name/ID (most common)
dirhop -u https://x.example/ ls  # by URL (selects/creates for this command)
dirhop ls                        # the active session (set with `session use`)
```

Set a persistent active session:

```sh
dirhop session use mirror
dirhop ls                        # now implicitly uses "mirror"
```

---

## Metadata levels (`--metadata`)

Used by `scan` and `refresh`:

| Level | What it fetches | Cost |
| --- | --- | --- |
| `minimal` | Only what the listing shows. | Cheapest. |
| `normal` | Listing metadata (default). | Cheap. |
| `full` | Adds one HEAD per file for exact size, MIME, ETag. | One extra request per file. |

`full` never issues GETs during indexing - file content is only transferred by
`download`.

---

## See also

- [[Command Reference|Command-Reference]] - per-command flags.
- [[Configuration]] - config-file keys and defaults.
- [[Troubleshooting]] - diagnosing failures by exit code.
