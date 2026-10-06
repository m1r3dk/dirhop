# Installation

## With Go

```sh
go install github.com/m1r3dk/dirhop/cmd/dirhop@latest
```

This puts `dirhop` in `$(go env GOPATH)/bin`. Ensure that is on your `PATH`.

## From source

```sh
git clone https://github.com/m1r3dk/dirhop
cd dirhop
make build
./bin/dirhop --help
```

## Prebuilt binaries

Tagged releases publish archives for Linux, macOS, and Windows (amd64/arm64)
with a `checksums.txt`. Download the archive for your platform from the
**Releases** page, verify the checksum, extract, and place `dirhop` on your
`PATH`.

```sh
sha256sum -c checksums.txt --ignore-missing
```

## Supported platforms

Linux, macOS, and Windows are built and tested in CI. The index is a single
SQLite file written via a pure-Go driver, so no CGO or external SQLite is
required.
