# Sebab build helpers. Everything stays inside the repo; nothing is installed system-wide.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/yoockh/sebab/internal/version.Version=$(VERSION)
GO      ?= go

.PHONY: all build test race vet fmt fmt-check check lab clean plugin-lint

all: check build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/sebab ./cmd/sebab

lab:
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/sebab-lab ./lab/cmd/sebab-lab

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

check: fmt-check vet race

plugin-lint:
	noctalia plugins lint frontends/noctalia/sebab

clean:
	rm -rf bin dist coverage.out
