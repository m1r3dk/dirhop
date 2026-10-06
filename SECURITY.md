# Security Policy

## Responsible use

`dirhop` indexes and downloads from **publicly accessible** HTTP/HTTPS directory
listings and **publicly listable** object-storage buckets (Amazon S3, Google
Cloud Storage, Azure Blob Storage, DigitalOcean Spaces, and other
S3-compatible stores), or systems you are explicitly authorized to access.

`dirhop` does **not**:

- discover unlinked or hidden paths,
- guess, brute-force, or submit credentials,
- bypass authentication or access controls, or
- exploit misconfigurations beyond issuing the same anonymous listing requests
  a browser or SDK would.

You are responsible for complying with the law and with the terms of service of
any system you point it at. Only use it where you have permission.

### Authorized use only / no liability

`dirhop` is provided **exclusively for authorized use** against buckets and
systems you own or are explicitly permitted to access. The author(s) and
contributors are **not responsible for any misuse, damage, or malicious or
unlawful activity** performed with this tool. All responsibility and liability
rests solely with the user. The software is provided "as is" without warranty of
any kind, as stated in [`LICENSE`](LICENSE).

## Reporting a vulnerability

If you discover a security vulnerability in `dirhop` itself (for example, a way
it could be made to leak credentials, write outside its download directory, or
execute untrusted input), please report it privately.

- Use GitHub's **private vulnerability reporting**: open the repository's
  **Security** tab and choose **Report a vulnerability**.
- Do **not** open a public issue for security reports.

Please include:

- a description of the issue and its impact,
- steps to reproduce (a minimal proof of concept if possible),
- the `dirhop` version or commit, and your OS/architecture.

You can expect an acknowledgement within a few days. Once a fix is available,
we will credit reporters who wish to be named.

## Supported versions

This project is pre-1.0. Security fixes are applied to the latest `main` and the
most recent tagged release.

## Handling of secrets

- The GrayHatWarfare API key can be supplied via the `GRAYHATWARFARE_API_KEY`
  environment variable so it need never be written to disk. When set in config,
  the environment variable takes precedence.
- Request logging (`--verbose`) redacts userinfo, query strings, cookies, and
  credentials, so signed URLs and tokens are never printed.
- No credentials, cookies, or request bodies are persisted in the index.
