# Contributing to dirhop

Thanks for your interest in improving `dirhop`. This document explains how to
build, test, and submit changes.

## Scope and ethics

`dirhop` is a tool for indexing and downloading from **public** directory
listings and **public** object-storage buckets, or systems you are explicitly
authorized to access. It does not discover unlinked paths, guess credentials, or
bypass access controls. Contributions that add such capabilities will not be
accepted. See [`SECURITY.md`](SECURITY.md) for the responsible-use policy.

## Development setup

You need Go (see the version in [`go.mod`](go.mod)) and `make`.

```sh
git clone https://github.com/m1r3dk/dirhop
cd dirhop
make build
./bin/dirhop --help
```

## Everyday commands

```sh
make fmt     # gofmt -w across the tree
make vet     # go vet ./...
make test    # go test ./...
make build   # build bin/dirhop
```

Before opening a pull request, please run the full local gate:

```sh
gofmt -l .                 # must print nothing
go vet ./...
go test -race ./...
go build ./cmd/dirhop
```

Optionally run the vulnerability scanner that CI also runs:

```sh
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

## Code style

- Standard `gofmt` formatting; no unformatted files are accepted.
- Keep functions focused and comment the *why*, not the *what*.
- Prefer table-driven tests. Every behavioral change should come with a test.
- Network-touching tests use `httptest` servers; do not reach the live internet
  in tests.
- Do not commit private data (bucket lists, API keys, local databases). The
  `.gitignore` already excludes `testdata/buckets/`, `*.db`, and `*.part`.

## Commit messages

Use short, scoped, imperative subjects, matching the existing history:

```
area: imperative summary under ~72 chars

Optional body explaining why the change is needed and any trade-offs.
```

Examples: `cli: accept bare hosts in scan`, `bucket: support Azure Blob Storage`.

## Pull requests

1. Fork and branch from `main`.
2. Make focused commits; keep unrelated changes out.
3. Ensure the local gate above passes.
4. Fill in the pull-request template, describing the change and how you tested
   it.
5. CI must be green on Linux, macOS, and Windows before review.

## Reporting bugs and requesting features

Open an issue using the provided templates. For anything security-sensitive, do
**not** open a public issue; follow [`SECURITY.md`](SECURITY.md) instead.
