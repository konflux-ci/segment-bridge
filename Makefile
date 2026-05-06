# segment-bridge developer entry points.
#
# Thin wrapper over `mise` so that `mise.toml` remains the single source of
# truth for toolchain versions. See CONTRIBUTING.md for the full setup story.

.PHONY: setup test lint pre-commit help

help: ## Show this help.
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

setup: ## One-command development environment setup.
	@command -v mise >/dev/null || { echo "Install mise first: https://mise.jdx.dev/"; exit 1; }
	mise install
	mise run pre-commit
	@echo "Setup complete. Run 'make test' to verify."

test: ## Run all Go unit tests with the pinned toolchain.
	mise exec -- go test ./...

lint: ## Run golangci-lint with the pinned toolchain.
	mise exec -- golangci-lint run

pre-commit: ## Run all pre-commit hooks against all files.
	mise run pre-commit
