BINARY  := locallm
PKG     := ./cmd/$(BINARY)
BIN_DIR := bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

GOLANGCI_LINT       ?= golangci-lint
GOVULNCHECK_VERSION ?= latest

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build the binary into bin/
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) $(PKG)

.PHONY: crossbuild
crossbuild: ## Check cross-compilation for the target platforms
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -o /dev/null $(PKG)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /dev/null $(PKG)
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -o /dev/null $(PKG)

.PHONY: run
run: ## Run the app, pass flags via ARGS="--model qwen"
	go run $(PKG) $(ARGS)

.PHONY: test
test: ## Run unit tests with the race detector
	go test -race -count=1 ./...

.PHONY: test-integration
test-integration: ## Run tests that require a running LM Studio
	go test -tags=integration -count=1 -timeout=10m ./...

.PHONY: cover
cover: ## Run tests and print total coverage
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -n 1

.PHONY: fmt
fmt: ## Format code (gofumpt + goimports)
	$(GOLANGCI_LINT) fmt

.PHONY: lint
lint: ## Run linters (includes go vet and format checks)
	$(GOLANGCI_LINT) run

.PHONY: tidy
tidy: ## Tidy go.mod and go.sum
	go mod tidy

.PHONY: tidy-check
tidy-check: ## Fail if go.mod or go.sum are not tidy
	go mod tidy -diff

.PHONY: vuln
vuln: ## Check dependencies for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

.PHONY: check
check: tidy-check lint test ## Run all checks; must pass before a change is done

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out
