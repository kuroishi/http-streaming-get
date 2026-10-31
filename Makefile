# Makefile for hsg (gofmt-read first)

BIN := bin
TARGET := $(BIN)/hsg
PACKAGES := ./...

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the hsg binary into bin/
	go build -o $(TARGET) ./cmd/hsg

.PHONY: install
install: ## Install into $(GOBIN) (default $(GOPATH)/bin)
	go install ./cmd/hsg

.PHONY: test
test: ## Run all unit tests
	go test ./...

.PHONY: race
race: ## Run all unit tests with the race detector
	go test -race ./...

.PHONY: fmt
fmt: ## Format all Go sources
	gofmt -w .

.PHONY: vet
vet: ## Run go vet
	go vet $(PACKAGES)

.PHONY: check
check: fmt vet test ## fmt + vet + test (pre-commit style gate)

.PHONY: run
run: ## Run hsg via go run (pass ARGS="...")
	go run ./cmd/hsg $(ARGS)

.PHONY: smoke
smoke: ## Build + run hsg against the bundled HLS fixture server
	go build -o $(BIN)/hsg ./cmd/hsg
	go build -o $(BIN)/hsg-testserver ./cmd/hsg-testserver
	@$(BIN)/hsg-testserver -addr 127.0.0.1:8899 >/dev/null 2>&1 & SRV=$$!; \
	sleep 1; \
	$(BIN)/hsg --clients 3 --seed 42 http://127.0.0.1:8899/master.m3u8; \
	status=$$?; kill $$SRV 2>/dev/null; exit $$status

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN)
	go clean -testcache
