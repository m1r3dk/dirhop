# FAQ

## Do I have to type `http://` or `https://`?

No. A bare host is accepted and indexed over `https`:

```sh
dirhop scan amazetest.storage.googleapis.com
```

The scheme is added when the input looks like a host (a dot appears before any
`/`, `?`, or `#`). Typos without a dot still fail as unknown commands.

## Does dirhop download everything when it indexes?

No. Indexing fetches directory pages or bucket listings only. File content is
transferred solely by an explicit `download`.

## Which buckets are supported?

Amazon S3, Google Cloud Storage, Azure Blob Storage, DigitalOcean Spaces, other
S3-compatible stores, and custom/CDN domains fronting them. See [[Buckets]].

## A bucket exited with code 7. Why?

The bucket or container exists but refuses anonymous listing, or the target is
not a recognizable directory listing. dirhop never tries to bypass this.

## Where is my data stored?

In a single SQLite file in your OS application-data directory. See
[[Configuration]].

## How do I make indexing faster?

Use `--workers N` (max 64) on `scan`/`refresh`. Buckets default to 4x
`crawl_concurrency` (max 32). See [[Buckets]].

## What do the exit codes mean?

`0` success, `2` invalid arguments, `3` no session, `4` path not found, `5`
network/crawl failure, `6` download failure, `7` unsupported listing, `1` other.
