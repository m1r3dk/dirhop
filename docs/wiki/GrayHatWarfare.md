# GrayHatWarfare

`ghw` searches the [GrayHatWarfare](https://grayhatwarfare.com/) public-bucket
index, which covers exactly the bucket types dirhop can browse (AWS S3, Azure
Blob, DigitalOcean Spaces, Google Cloud).

## API key

Set `grayhatwarfare_api_key` in the config, or export `GRAYHATWARFARE_API_KEY`.
The environment variable wins, so the key need not be written to disk.

```sh
export GRAYHATWARFARE_API_KEY=...
```

## Usage

```sh
dirhop ghw backup                      # buckets whose name matches "backup"
dirhop ghw --type azure company        # Azure containers only
dirhop ghw --files --ext sql,zip dump  # files, filtered by extension
dirhop ghw invoices --scan             # index every matched bucket into dirhop
dirhop ghw secrets --urls | dirhop scan -f -   # pipe matches into a batch scan
```

Bucket search is the default; `--files` searches individual files. `--scan`
feeds matched bucket URLs straight into the indexer so you can immediately
`ls`/`find`/`download`. `--urls` prints just the bucket URLs for piping into
`scan -f -`. `--json` emits raw results.

## Flags

`--files`, `--type/-t`, `--ext/-e`, `--limit/-l`, `--order`, `--direction`,
`--full-path`, `--scan`, `--urls`.

## Responsible use

GrayHatWarfare only indexes buckets that are already publicly listable, and
dirhop itself only issues listing requests, exactly as with any other bucket.
Respect the targets' terms and the law. See
[[Security & Responsible Use|Security]].
