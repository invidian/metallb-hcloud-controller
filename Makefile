# Packages which Go tooling will operate on.
GO_PACKAGES=./...

lint: ## Run linter. Set GO_PACKAGES to select which packages to lint. By defaults lints all packages in module.
	golangci-lint run --enable-all --max-same-issues=0 --max-issues-per-linter=0 --timeout 10m --exclude-use-default=false $(GO_PACKAGES)

help: ## Prints help message.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

.PHONY: lint help
