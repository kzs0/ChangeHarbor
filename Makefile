export GOCACHE := /workspace/.cache/go-build
export GOPATH := /workspace/.cache/go
GO := /workspace/toolchains/go/bin/go
.PHONY: test check build
test:
	$(GO) test ./...
check:
	$(GO) vet ./...
	$(GO) test -race ./...
build:
	$(GO) build -o bin/reviewd ./cmd/reviewd
