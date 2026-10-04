BINARY := bin/dirclone

.PHONY: all build test vet fmt clean

all: test build

build:
	mkdir -p bin
	go build -trimpath -o $(BINARY) ./cmd/dirclone

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './.git/*')

clean:
	rm -rf bin coverage.out
