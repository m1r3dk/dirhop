# Security & Responsible Use

## Authorized use only / no liability

`dirhop` is intended **solely for authorized use**: public directory listings,
publicly listable buckets, and systems you own or have explicit, documented
permission to access.

The author(s) and contributors provide this software "as is", without warranty
of any kind, and **accept no liability** for any misuse, damage, or unlawful or
malicious activity carried out with it. All responsibility rests solely with the
user.

## What dirhop does not do

- It does not discover unlinked or hidden paths.
- It does not guess, brute-force, or submit credentials.
- It does not bypass authentication or access controls.
- It issues only the same anonymous listing requests a browser or SDK would.

## Secret handling

- The GrayHatWarfare API key can be supplied via `GRAYHATWARFARE_API_KEY` so it
  need never be written to disk; the environment variable wins over config.
- `--verbose` request logging redacts userinfo, query strings, cookies, and
  credentials, so signed URLs and tokens are never printed.
- No credentials, cookies, or request bodies are persisted in the index.

## Reporting a vulnerability

Report security issues privately via GitHub's **Security > Report a
vulnerability**. Do not open a public issue. See the repository `SECURITY.md`
for details.
