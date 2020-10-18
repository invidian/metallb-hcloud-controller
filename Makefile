# Packages which Go tooling will operate on.
GO_PACKAGES=./...

# Tests which make run by default.
GO_TESTS=^.*$

# Go parameters.
CGO_ENABLED=0
GOCMD=env GO111MODULE=on CGO_ENABLED=$(CGO_ENABLED) go
GOTEST=$(GOCMD) test -covermode=atomic -buildmode=exe

all: test lint ## Runs unit tests and linter.

lint: ## Run linter. Set GO_PACKAGES to select which packages to lint. By defaults lints all packages in module.
	golangci-lint run --enable-all --max-same-issues=0 --max-issues-per-linter=0 --timeout 10m --exclude-use-default=false $(GO_PACKAGES)

build-test: ## Compile unit tests. Set GO_PACKAGES to select which packages to compile. By defaults compiles all packages in module.
	$(GOTEST) -run=nope -tags e2e $(GO_PACKAGES)

test: build-test ## Run unit tests. Set GO_PACKAGES and GO_TESTS to select which tests to run. By defaults run all unit tests in module.
	$(GOTEST) -run $(GO_TESTS) $(GO_PACKAGES)

test-e2e: ## Run e2e tests. May require extra environment variables to pass.
	$(GOTEST) -run $(GO_TESTS) -tags e2e $(GO_PACKAGES)

help: ## Prints help message.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

# Following tasks should not be run in parallel.
.PHONY: lint help build-test test test-e2e all
