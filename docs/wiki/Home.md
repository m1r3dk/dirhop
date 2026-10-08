# dirhop

`dirhop` turns an HTTP/HTTPS directory listing or a public cloud
bucket into a persistent local metadata index you can browse, search, and
download from like a local filesystem. Crawls fetch directory pages (or bucket
listings) only; file content is transferred only after an explicit `download`.

> **Ethical use only.** Use `dirhop` for ethical purposes only. The authors
> accept no liability for misuse. See [[Security & Responsible Use|Security]].

## Pages

- [[Installation]] - install options and building from source
- [[Quick Start|Quick-Start]] - index your first site or bucket
- [[Command Reference|Command-Reference]] - every command, flag, and synopsis
- [[Recipes]] - task-oriented workflows
- [[Flags & Exit Codes|Flags-and-Exit-Codes]] - global flags, exit codes, JSON scripting
- [[Buckets]] - S3, GCS, Azure Blob, Spaces, custom domains
- [[GrayHatWarfare|GrayHatWarfare]] - discovering public buckets with `ghw`
- [[Configuration]] - config file, storage locations, env vars
- [[Troubleshooting]] - diagnose by exit code and symptom
- [[Architecture]] - how dirhop is built
- [[Security & Responsible Use|Security]] - scope, ethics, reporting
- [[FAQ]]

## At a glance

```sh
# index a directory listing and open the interactive shell
dirhop https://example.com/pub/

# index a bucket by bare host (https is added automatically)
dirhop scan amazetest.storage.googleapis.com

# index many buckets from a file
dirhop scan -f buckets.txt

# search and download without re-hitting the network
dirhop -s example-com find "*.iso"
dirhop -s example-com download /releases/file.iso
```
