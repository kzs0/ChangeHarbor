GO := $(if $(wildcard /workspace/toolchains/go/bin/go),/workspace/toolchains/go/bin/go,go)
export GOCACHE ?= $(CURDIR)/.cache/go-build
.PHONY: test check build package
test:
	$(GO) test ./...
check:
	$(GO) vet ./...
	$(GO) test -race ./...
build:
	$(GO) build -o bin/reviewd ./cmd/reviewd
package:
	GO=$(GO) scripts/package.sh
