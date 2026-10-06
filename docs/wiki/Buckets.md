# Buckets

Public object-storage buckets are indexed through their documented **anonymous
listing APIs**, not HTML scraping. Amazon S3, Google Cloud Storage, and
DigitalOcean Spaces (and other S3-compatible stores) use ListObjectsV2
(`?list-type=2`, paginated by continuation token). Azure Blob Storage uses List
Blobs (`?restype=container&comp=list`, paginated by `marker`).

## Recognized URL forms

- `https://<bucket>.s3[.<region>].amazonaws.com/` and `https://s3.amazonaws.com/<bucket>/`
- `https://storage.googleapis.com/<bucket>/` and `https://<bucket>.storage.googleapis.com/`
- `https://<bucket>.<region>.digitaloceanspaces.com/` (and the `.cdn.` alias) and `https://<region>.digitaloceanspaces.com/<bucket>/`
- `https://<account>.blob.core.windows.net/<container>/`

Each optionally followed by a key prefix. Key prefixes become directories;
object size, ETag, and LastModified are stored. A bucket or container that
refuses anonymous listing exits with code 7.

## Bare hosts

You can omit the scheme. `scan`, `-f FILE` lines, and the root `dirhop <host>`
argument prepend `https://` when the input looks like a host (a dot before any
`/`, `?`, or `#`):

```sh
dirhop scan amazetest.storage.googleapis.com
dirhop scan host.blob.core.windows.net/container
```

Typos with no dot are left unchanged and still fail as unknown commands.

## Custom / CDN domains

A CDN hostname CNAMEd to S3, GCS, R2, or MinIO (e.g. `https://cdn.example.com/`)
matches no hostname rule, so dirhop falls back to asking the origin: if the root
is not parseable HTML, it issues one `?list-type=2` request and uses the bucket
path when the response is a real `ListBucketResult`. Ordinary websites are
unaffected because the probe runs only after HTML parsing fails.

## V1 vs V2 listing

Listing prefers V2. Servers that only implement the older V1 listing (no
`KeyCount`, no continuation token) are detected from the first response and
paged with `marker`, resuming from `NextMarker` or the last key, so they are
indexed in full rather than truncated at 1000 objects.

## Concurrency

Bucket listing is parallel: `delimiter=/` prefixes are listed concurrently, and
large flat folders are split into `start-after` key ranges. Default bucket
concurrency is 4x `crawl_concurrency` (max 32). Override per run with
`--workers N` (max 64) on `scan`/`refresh`/`open`.

## Metadata levels

`minimal`/`normal` use only what the listing shows (default). `full`
additionally issues bounded HEAD requests for exact size, MIME, and ETag; it
never issues GETs during indexing.
