# Troubleshooting

Diagnose by exit code and symptom. Exit-code reference: [[Flags-and-Exit-Codes]].

---

## Diagnose by exit code

| Code | Meaning | First checks |
| --- | --- | --- |
| `2` | Invalid arguments | Flag typo, bad value, or mutually exclusive flags (`--overwrite`+`--skip-existing`). Run `dirhop <cmd> --help`. |
| `3` | No session | No active/selected session. `dirhop sessions`; pass `-s NAME` or `session use NAME`. |
| `4` | Path not found | Path not in the index. Verify with `ls`/`find`; `refresh` if the remote changed. |
| `5` | Network / crawl failure | Host unreachable, TLS error, timeout. Retry with `-v`; check `errors`. |
| `6` | Download failure | Transfer aborted. Re-run to resume; inspect `downloads`. |
| `7` | Unsupported listing | Target refuses anonymous listing or is not a directory listing. Not bypassable. |

---

## `initialize schema: ... database is locked by another process`

Another `dirhop` process holds the write lock, or a stale WAL remains from a
crashed run.

```sh
pgrep -fl dirhop                       # find other instances
ls "$(dirname "$(dirhop config path)")"/*.db-wal  # stale WAL?
```

Close other instances. If none are running, remove the stale `*.db-wal`
alongside the database, then retry. Increase `busy_timeout` in config for
heavily concurrent setups.

## A scan exits 5 immediately on HTTPS buckets

Dotted bucket names or HTTP-only endpoints can fail TLS verification. dirhop
retries over plain HTTP automatically for those; if it still fails, confirm the
host resolves and serves a listing:

```sh
dirhop scan bucket.s3.amazonaws.com -v
dirhop -s bucket errors
```

## Preflight skipped targets I expected to scan

Preflight drops `private`/`missing`/`errored` targets. Inspect the report and
force a full attempt if needed:

```sh
cat preflight.md                       # per-target verdicts
dirhop scan -f targets.txt --no-preflight
```

Tune the probe deadline with `preflight_timeout` in config.

## Re-running a scan does nothing

Completed sessions are skipped by design. Force re-crawl:

```sh
dirhop scan -f targets.txt --rescan        # re-crawl completed targets
dirhop -s mirror refresh --full            # single session, full reconciliation
```

## Downloads keep restarting instead of resuming

Resume requires valid HTTP range support. If the origin ignores ranges, dirhop
restarts the stream. Verify the server returns `206 Partial Content`:

```sh
curl -sI -H 'Range: bytes=0-0' <file-url> | grep -i '206\|content-range'
```

Without range support, `--segments` also falls back to a single stream.

## `go install` build reports version `(devel)`

Expected. Module build info has no tag for untagged installs. Tagged releases and
`make build` embed the real version via ldflags. Confirm:

```sh
dirhop version --json
```

## Stale index after an upgrade

If a schema upgrade is needed, dirhop migrates in place on the next open. If a
command still errors on open, back up and reinspect:

```sh
cp "$(dirhop config path | xargs dirname)/dirhop.db" ./dirhop.db.bak
dirhop db status
```

## Output is noisy in pipes

Progress and logs go to stderr; data to stdout. For clean pipes:

```sh
dirhop -s mirror urls --files-only --quiet | aria2c -i -
dirhop stats --json --quiet | jq .
```

## Getting more detail

```sh
dirhop <command> -v        # one line per HTTP request (redacted URLs)
dirhop <command> --debug   # additional diagnostics
dirhop -s SESSION errors   # recorded crawl errors
```

Still stuck? Open an issue with `dirhop version --json` output and the exact
command and exit code.
