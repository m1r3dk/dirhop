## Summary

Describe what this change does and why.

## Related issues

Closes #

## How was this tested?

- [ ] `gofmt -l .` prints nothing
- [ ] `go vet ./...` passes
- [ ] `go test -race ./...` passes
- [ ] `go build ./cmd/dirhop` succeeds
- [ ] New or changed behavior has test coverage

Describe any manual testing (commands run, targets used).

## Checklist

- [ ] My change keeps dirhop within its ethical-use scope (no path discovery,
      credential guessing, or access-control bypass).
- [ ] I did not commit secrets or private data.
- [ ] I updated docs/CHANGELOG where relevant.
