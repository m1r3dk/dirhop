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

## Browse, search, download

```sh
dirhop sessions
dirhop -s mirror ls -lh
dirhop -s mirror find "*.zip"
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
