BINARY_NAME  := linkclient
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS      := -s -w -X main.version=$(VERSION)
DIST_DIR     := dist
PLATFORMS    := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.DEFAULT_GOAL := help

.PHONY: all build build-all test vet fmt check release run clean help

all: build

build: ## Build the client for the current platform
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY_NAME) ./cmd/linkclient

build-all: ## Cross-compile the client for all supported platforms into dist/
	@mkdir -p $(DIST_DIR)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; \
		arch=$${platform#*/}; \
		ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		out="$(DIST_DIR)/$(BINARY_NAME)-$(VERSION)-$${os}-$${arch}$${ext}"; \
		echo "  GOOS=$${os} GOARCH=$${arch} -> $${out}"; \
		CGO_ENABLED=0 GOOS=$${os} GOARCH=$${arch} \
			go build -trimpath -ldflags '$(LDFLAGS)' -o "$${out}" ./cmd/linkclient || exit 1; \
	done
	@cd $(DIST_DIR) && (shasum -a 256 -- * 2>/dev/null || sha256sum -- *) > checksums.txt
	@echo "artifacts in $(DIST_DIR):"; ls -1 $(DIST_DIR)

test: ## Run all tests
	go test ./... -count=1

vet: ## Run go vet
	go vet ./...

fmt: ## Verify gofmt formatting
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then \
		echo "files not gofmt'd:"; echo "$$out"; exit 1; \
	fi

check: fmt vet test ## fmt + vet + test

release: check build-all ## Run checks and build all release artifacts

run: ## Run the client locally
	go run ./cmd/linkclient

clean: ## Remove build artifacts
	rm -rf $(DIST_DIR) $(BINARY_NAME)

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
