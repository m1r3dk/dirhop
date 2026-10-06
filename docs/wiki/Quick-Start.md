# Quick Start

## Index a directory listing

```sh
dirhop https://example.com/pub/
```

The first run crawls the listing and opens the interactive shell. Running the
same command later reopens the persisted index without re-crawling.

## Index a bucket

A bare host with no scheme is accepted and indexed over `https`, so you can pass
bucket hosts directly:

```sh
dirhop scan amazetest.storage.googleapis.com
dirhop scan host.blob.core.windows.net/container
dirhop scan bucket.s3.amazonaws.com
```

## Index many targets from a file

One target per line, optional session name as a second field, `#` for comments.
Bare hosts are fine:

```text
mirror.example.com/pub/            mirror
bucket.s3.amazonaws.com
host.blob.core.windows.net/docs    azureblob
# disabled.example.com
```

```sh
dirhop scan -f targets.txt
cat targets.txt | dirhop scan -f -
```

Duplicate canonical URLs are skipped. One failing target does not stop the rest;
the exit code reflects the first failure. `--json` prints a per-target result
list.

### Go faster on large lists

Index many sites at once with `--parallel/-p N`. Use `--parallel 0` to let
dirhop auto-pick a high-throughput worker count for big bucket lists. Most of a
bucket's time is network latency (dead hosts time out), so overlapping them is
dramatically faster: on one 24-host sample, `--parallel 24` ran ~28x faster than
the serial default. Results stay in input order and the SQLite index stays
consistent because writes are serialized internally.

```sh
dirhop scan -f targets.txt --parallel 32
dirhop scan -f buckets_aws.txt --parallel 0
```

### Skip private or missing buckets first

For huge target lists, dirhop runs a cheap preflight automatically before
indexing. `--preflight` sends one minimal listing request per recognized bucket,
or HEAD/tiny GET for ordinary HTTP listings, skips private/missing/error targets,
and then scans only the accessible targets. Use `--no-preflight` to force trying
every target anyway.

```sh
dirhop scan -f buckets.txt --preflight --parallel 0
# Preflight summary: 128 accessible, 54 private, 210 missing, 8 errored -> scanning 128/400
```

## Browse, search, download

```sh
dirhop sessions
dirhop -s mirror ls -lh
dirhop -s mirror find "*.zip"
dirhop find --all-sites --ext zip
dirhop search --all-sites secrets
dirhop stats
dirhop download --all-sites --include '*secrets*' -o ./matches
dirhop -s mirror stat /releases/file.zip
dirhop -s mirror download /releases/file.zip
```

## Interactive shell

```text
mirror:/ > cd releases
mirror:/releases > ls -lh
mirror:/releases > find "*.iso"
mirror:/releases > download server.iso
mirror:/releases > exit
```

History persists across sessions; Tab completes commands, remote paths, and
session names.
