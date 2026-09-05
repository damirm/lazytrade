SHELL := /bin/sh

.DEFAULT_GOAL := help

GO ?= go
BIN_DIR ?= bin
BINARY ?= $(BIN_DIR)/lazytrade
CONFIG ?= configs/example.yaml
PKG ?= ./...

.PHONY: help build fmt vet test test-race check config-validate version

help: ## Show available targets.
	@printf '%s\n' \
		'make build            Build bin/lazytrade (override BINARY=...).' \
		'make fmt              Format all Go source files.' \
		'make vet              Run go vet for all packages.' \
		'make test             Run tests (override PKG=./internal/agent).' \
		'make test-race        Run all tests with the race detector.' \
		'make check            Run vet and tests.' \
		'make config-validate  Build and validate CONFIG (default: configs/example.yaml).' \
		'make version          Build and print the application version.'

build: ## Build the CLI binary.
	mkdir -p $(BIN_DIR)
	$(GO) build -o $(BINARY) ./cmd/lazytrade

fmt: ## Format Go source files.
	gofmt -w $$(rg --files -g '*.go')

vet: ## Run static checks provided by the Go toolchain.
	$(GO) vet ./...

test: ## Run Go tests; use PKG to select packages.
	$(GO) test $(PKG)

test-race: ## Run the full test suite with the race detector.
	$(GO) test -race ./...

check: vet test ## Run the non-mutating local verification suite.

config-validate: build ## Strictly validate a configuration file.
	$(BINARY) config validate --config $(CONFIG)

version: build ## Print build version information.
	$(BINARY) version
