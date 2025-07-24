# CollabOps Git Executor - Makefile
# Comprehensive build, test, and development automation

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GOFMT=gofmt
GOLINT=$$(go env GOPATH)/bin/golangci-lint

# Project parameters
BINARY_NAME=git-executor
BINARY_UNIX=$(BINARY_NAME)_unix
MAIN_PATH=./cmd/git-executor
PKG_LIST=$$(go list ./... | grep -v /vendor/)

# Docker parameters
DOCKER_IMAGE=collabops/git-executor
DOCKER_TAG=latest

# Directories
BIN_DIR=./bin
CONFIG_DIR=./config
SCRIPTS_DIR=./scripts

# Colors for output
RED=\033[0;31m
GREEN=\033[0;32m
YELLOW=\033[1;33m
BLUE=\033[0;34m
NC=\033[0m # No Color

.PHONY: all build clean test test-coverage test-race test-verbose test-race-verbose test-logging lint fmt format fix vet verify help local
.PHONY: deps deps-update deps-tidy docker docker-build docker-build-dev docker-run docker-run-dev docker-compose-up docker-compose-up-dev docker-compose-down
.PHONY: verify-structure verify-config verify-all local-setup install-tools qa-check qa-fix fmt-check pre-commit lint-errors

# Default target
all: clean deps fmt vet lint test build

# Help target - shows available commands
help: ## Show this help message
	@echo "$(BLUE)CollabOps Git Executor - Available Make Targets:$(NC)"
	@echo ""
	@echo "$(GREEN)🔨 Build Commands:$(NC)"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  $(YELLOW)%-15s$(NC) %s\n", $$1, $$2}' $(MAKEFILE_LIST) | grep -E "(build|clean|install)"
	@echo ""
	@echo "$(GREEN)🧪 Test Commands:$(NC)"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  $(YELLOW)%-15s$(NC) %s\n", $$1, $$2}' $(MAKEFILE_LIST) | grep -E "(test|verify)"
	@echo ""
	@echo "$(GREEN)🔧 Development Commands:$(NC)"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  $(YELLOW)%-15s$(NC) %s\n", $$1, $$2}' $(MAKEFILE_LIST) | grep -E "(deps|fmt|format|lint|vet|local)"
	@echo ""
	@echo "$(GREEN)🐳 Docker Commands:$(NC)"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  $(YELLOW)%-15s$(NC) %s\n", $$1, $$2}' $(MAKEFILE_LIST) | grep -E "(docker)"
	@echo ""

# Build commands
build: ## Build the binary
	@echo "$(BLUE)🔨 Building $(BINARY_NAME)...$(NC)"
	@mkdir -p $(BIN_DIR)
	$(GOBUILD) -o $(BIN_DIR)/$(BINARY_NAME) -v $(MAIN_PATH)

build-linux: ## Build the binary for Linux
	@echo "$(BLUE)🔨 Building $(BINARY_NAME) for Linux...$(NC)"
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GOBUILD) -o $(BIN_DIR)/$(BINARY_UNIX) -v $(MAIN_PATH)

clean: ## Clean build files
	@echo "$(BLUE)🧹 Cleaning...$(NC)"
	@if command -v $(GOCMD) >/dev/null 2>&1; then \
		$(GOCLEAN); \
	else \
		echo "$(YELLOW)⚠️  Go not installed, skipping 'go clean'$(NC)"; \
	fi
	rm -rf $(BIN_DIR)

install: build ## Install the binary to $GOPATH/bin
	@echo "$(BLUE)📦 Installing $(BINARY_NAME)...$(NC)"
	$(GOCMD) install $(MAIN_PATH)

# Test commands
test: ## Run tests
	@echo "$(BLUE)🧪 Running tests...$(NC)"
	$(GOTEST) -v ./...

test-short: ## Run tests with short flag
	@echo "$(BLUE)🧪 Running short tests...$(NC)"
	$(GOTEST) -short ./...

test-race: ## Run tests with race detection
	@echo "$(BLUE)🧪 Running tests with race detection...$(NC)"
	$(GOTEST) -race -short ./...

test-verbose: ## Run tests with verbose output
	@echo "$(BLUE)🧪 Running verbose tests...$(NC)"
	$(GOTEST) -v -race ./...

test-race-verbose: ## Run tests with race detection and verbose output (CI optimized)
	@echo "$(BLUE)🧪 Running comprehensive tests with race detection and verbose output...$(NC)"
	@echo "$(YELLOW)🔍 Race detection enabled for CI reliability$(NC)"
	$(GOTEST) -v -race -timeout=10m ./...

test-coverage: ## Run tests with coverage
	@echo "$(BLUE)🧪 Running tests with coverage...$(NC)"
	$(GOTEST) -v -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html
	@echo "$(GREEN)📊 Coverage report generated: coverage.html$(NC)"

test-config: ## Run configuration package tests specifically
	@echo "$(BLUE)🧪 Running configuration package tests...$(NC)"
	$(GOTEST) -v ./internal/config/...

test-server: ## Run server package tests specifically
	@echo "$(BLUE)🧪 Running server package tests...$(NC)"
	$(GOTEST) -v ./internal/server/...

test-logging: ## Run logging package tests specifically
	@echo "$(BLUE)🧪 Running logging package tests...$(NC)"
	$(GOTEST) -v ./internal/logging/...

test-repository: ## Run repository package tests specifically
	@echo "$(BLUE)🧪 Running repository package tests...$(NC)"
	$(GOTEST) -v ./internal/repository/...

test-manual-paths: ## Run manual repository path management test
	@echo "$(BLUE)🧪 Running manual repository path management test...$(NC)"
	$(GOCMD) run ./cmd/test-repo-paths/main.go

test-manual-config: ## Run manual configuration loading test
	@echo "$(BLUE)🧪 Running manual configuration loading test...$(NC)"
	$(GOCMD) run ./cmd/test-config/main.go

test-manual-server: ## Run manual server test (requires separate terminal for testing)
	@echo "$(BLUE)🧪 Starting server for manual testing...$(NC)"
	@echo "$(YELLOW)💡 Use another terminal to test with: curl http://localhost:9090/health$(NC)"
	@echo "$(YELLOW)💡 Press Ctrl+C to stop the server$(NC)"
	$(GOCMD) run ./cmd/git-executor/main.go

test-manual-smart-http: ## Run Smart HTTP Protocol tests (requires server to be running)
	@echo "$(BLUE)🧪 Running Smart HTTP Protocol tests...$(NC)"
	@./scripts/test-smart-http.sh

test-manual-smart-http-setup: ## Create test repositories for Smart HTTP testing
	@echo "$(BLUE)🧪 Setting up test repositories for Smart HTTP...$(NC)"
	@./scripts/test-smart-http.sh --create-repo

test-manual-all: ## Run all available manual tests
	@echo "$(BLUE)🧪 Running all manual tests...$(NC)"
	@echo ""
	@$(MAKE) test-manual-config
	@echo ""
	@$(MAKE) test-manual-paths
	@echo ""
	@echo "$(GREEN)🎉 All manual component tests completed!$(NC)"
	@echo "$(YELLOW)💡 To test the HTTP server, run: make test-manual-server$(NC)"
	@echo "$(YELLOW)💡 To test Smart HTTP protocol, run: make test-manual-smart-http$(NC)"

# Code quality commands
fmt: ## Format code (basic - use 'make format' for comprehensive)
	@echo "$(BLUE)🎨 Basic formatting (gofmt only)...$(NC)"
	@echo "$(YELLOW)💡 For comprehensive formatting including go.mod cleanup, use 'make format'$(NC)"
	$(GOFMT) -s -w .

fix: ## COMPREHENSIVE: Auto-format, fix auto-fixable lint issues, and show remaining issues (alias for 'format')
	@echo "$(BLUE)🎯 Running comprehensive fix (same as 'make format')...$(NC)"
	@$(MAKE) format

format: ## Auto-format code, fix lint issues, and show results (COMPREHENSIVE)
	@echo "$(BLUE)🎨 COMPREHENSIVE Auto-formatting and organizing code...$(NC)"
	@echo ""
	@echo "$(YELLOW)1️⃣  Running auto-fixable lint fixes...$(NC)"
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run --fix ./...; \
		echo "$(GREEN)   ✅ Auto-fixable lint issues resolved$(NC)"; \
	elif command -v $$(go env GOPATH)/bin/golangci-lint >/dev/null 2>&1; then \
		$$(go env GOPATH)/bin/golangci-lint run --fix ./...; \
		echo "$(GREEN)   ✅ Auto-fixable lint issues resolved$(NC)"; \
	else \
		echo "$(YELLOW)   📦 Installing golangci-lint for auto-fixes...$(NC)"; \
		go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest; \
		$$(go env GOPATH)/bin/golangci-lint run --fix ./...; \
		echo "$(GREEN)   ✅ golangci-lint installed and auto-fixes applied$(NC)"; \
	fi
	@echo ""
	@echo "$(YELLOW)2️⃣  Running gofmt (format Go source code)...$(NC)"
	$(GOFMT) -s -w .
	@echo "$(GREEN)   ✅ gofmt completed$(NC)"
	@echo ""
	@echo "$(YELLOW)3️⃣  Organizing imports with goimports...$(NC)"
	@if command -v goimports >/dev/null 2>&1; then \
		goimports -w .; \
		echo "$(GREEN)   ✅ goimports completed$(NC)"; \
	elif command -v $$(go env GOPATH)/bin/goimports >/dev/null 2>&1; then \
		$$(go env GOPATH)/bin/goimports -w .; \
		echo "$(GREEN)   ✅ goimports completed$(NC)"; \
	else \
		echo "$(YELLOW)   📦 Installing goimports...$(NC)"; \
		go install golang.org/x/tools/cmd/goimports@latest; \
		$$(go env GOPATH)/bin/goimports -w .; \
		echo "$(GREEN)   ✅ goimports installed and completed$(NC)"; \
	fi
	@echo ""
	@echo "$(YELLOW)4️⃣  Cleaning up go.mod with go mod tidy...$(NC)"
	go mod tidy
	@echo "$(GREEN)   ✅ go mod tidy completed$(NC)"
	@echo ""
	@echo "$(YELLOW)5️⃣  Verifying go.mod integrity...$(NC)"
	go mod verify
	@echo "$(GREEN)   ✅ go mod verify completed$(NC)"
	@echo ""
	@echo "$(YELLOW)6️⃣  Running final formatting check...$(NC)"
	@if [ "$$($(GOFMT) -l . | wc -l)" -gt 0 ]; then \
		echo "$(RED)   ❌ Files still need formatting:$(NC)"; \
		$(GOFMT) -l .; \
		echo "$(RED)   This should not happen - please report this issue$(NC)"; \
		exit 1; \
	else \
		echo "$(GREEN)   ✅ All files properly formatted$(NC)"; \
	fi
	@echo ""
	@echo "$(GREEN)✨ COMPREHENSIVE formatting complete! Running lint check...$(NC)"
	@echo "$(BLUE)═══════════════════════════════════════════════════$(NC)"
	@$(MAKE) lint

vet: ## Run go vet
	@echo "$(BLUE)🔍 Running go vet...$(NC)"
	$(GOCMD) vet ./...

lint: ## Run golangci-lint
	@echo "$(BLUE)🔍 Running golangci-lint...$(NC)"
	@if command -v $(GOLINT) >/dev/null 2>&1; then \
		$(GOLINT) run ./...; \
	else \
		echo "$(YELLOW)⚠️  golangci-lint not installed. Run 'make install-tools' to install.$(NC)"; \
	fi

# Dependency management
deps: ## Download dependencies
	@echo "$(BLUE)📦 Downloading dependencies...$(NC)"
	$(GOMOD) download

deps-update: ## Update dependencies
	@echo "$(BLUE)📦 Updating dependencies...$(NC)"
	$(GOMOD) get -u ./...

deps-tidy: ## Tidy dependencies
	@echo "$(BLUE)📦 Tidying dependencies...$(NC)"
	$(GOMOD) tidy

setup: ## Initial project setup (download deps, create go.sum)
	@echo "$(BLUE)🚀 Setting up project dependencies...$(NC)"
	$(GOMOD) tidy
	$(GOMOD) download
	@echo "$(GREEN)✅ Project setup complete! Ready to run tests.$(NC)"

deps-verify: ## Verify dependencies
	@echo "$(BLUE)📦 Verifying dependencies...$(NC)"
	$(GOMOD) verify

# Verification commands (our custom scripts)
verify-structure: ## Verify project structure (Task 1.1)
	@echo "$(BLUE)🔍 Verifying project structure...$(NC)"
	@if [ -f $(SCRIPTS_DIR)/verify-structure.sh ]; then \
		$(SCRIPTS_DIR)/verify-structure.sh; \
	else \
		echo "$(RED)❌ verify-structure.sh not found$(NC)"; \
		exit 1; \
	fi

verify-config: ## Verify configuration implementation (Task 1.2)
	@echo "$(BLUE)🔍 Verifying configuration implementation...$(NC)"
	@if [ -f $(SCRIPTS_DIR)/verify-config.sh ]; then \
		$(SCRIPTS_DIR)/verify-config.sh; \
	else \
		echo "$(RED)❌ verify-config.sh not found$(NC)"; \
		exit 1; \
	fi

verify-all: verify-structure verify-config ## Run all verification scripts
	@echo "$(GREEN)✅ All verifications completed!$(NC)"

# Docker commands
docker-build: ## Build Docker image
	@echo "$(BLUE)🐳 Building Docker image...$(NC)"
	docker build -f deployments/Dockerfile -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

docker-build-dev: ## Build development Docker image
	@echo "$(BLUE)🐳 Building development Docker image...$(NC)"
	docker build -f deployments/Dockerfile.dev -t $(DOCKER_IMAGE):dev .

docker-run: ## Run Docker container
	@echo "$(BLUE)🐳 Running Docker container...$(NC)"
	docker run --rm -p 9090:9090 -v $(PWD)/config:/config:ro -v git-repos:/repos $(DOCKER_IMAGE):$(DOCKER_TAG)

docker-run-dev: ## Run development Docker container
	@echo "$(BLUE)🐳 Running development Docker container...$(NC)"
	docker run --rm -p 9091:9090 -v $(PWD):/app -v git-repos:/repos $(DOCKER_IMAGE):dev

docker-compose-up: ## Start services with docker-compose
	@echo "$(BLUE)🐳 Starting services with docker-compose...$(NC)"
	cd deployments && docker-compose up --build

docker-compose-up-dev: ## Start development services with docker-compose
	@echo "$(BLUE)🐳 Starting development services with docker-compose...$(NC)"
	cd deployments && docker-compose --profile dev up --build

docker-compose-down: ## Stop services with docker-compose
	@echo "$(BLUE)🐳 Stopping services with docker-compose...$(NC)"
	cd deployments && docker-compose down

docker-shell: ## Get shell access to Docker container
	@echo "$(BLUE)🐳 Opening shell in Docker container...$(NC)"
	docker run --rm -it --entrypoint=/bin/sh $(DOCKER_IMAGE):$(DOCKER_TAG)

# Local setup
local-setup: install-tools deps ## Set up local environment
	@echo "$(GREEN)🎉 Local environment setup complete!$(NC)"

install-tools: ## Install local tools
	@echo "$(BLUE)🛠️  Installing local tools...$(NC)"
	@command -v $(GOLINT) >/dev/null 2>&1 || { \
		echo "Installing golangci-lint..."; \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin; \
	}
	@echo "$(GREEN)✅ Local tools installed!$(NC)"

# Check if Go is installed
check-go: ## Check if Go is installed
	@echo "$(BLUE)🔍 Checking Go installation...$(NC)"
	@if command -v $(GOCMD) >/dev/null 2>&1; then \
		echo "$(GREEN)✅ Go is installed: $$(go version)$(NC)"; \
	else \
		echo "$(RED)❌ Go is not installed. Please install Go 1.21+ to run build and test commands.$(NC)"; \
		echo "$(YELLOW)💡 You can still run verification scripts without Go.$(NC)"; \
		exit 1; \
	fi

# Status check
status: ## Show project status
	@echo "$(BLUE)📊 Project Status:$(NC)"
	@echo ""
	@echo "$(GREEN)📁 Project Structure:$(NC)"
	@find . -type d -name ".*" -prune -o -type d -print | head -10 | sed 's/^/  /'
	@echo ""
	@echo "$(GREEN)📄 Key Files:$(NC)"
	@ls -la go.mod README.md Makefile 2>/dev/null | sed 's/^/  /' || echo "  Some files missing"
	@echo ""
	@echo "$(GREEN)🔧 Configuration:$(NC)"
	@ls -la internal/config/ 2>/dev/null | sed 's/^/  /' || echo "  Configuration package not found"
	@echo ""
	@echo "$(GREEN)📝 Scripts:$(NC)"
	@ls -la scripts/ 2>/dev/null | sed 's/^/  /' || echo "  Scripts directory not found"

# Quick test runner (works without Go if verification scripts exist)
quick-test: ## Run quick tests (verification scripts only, no Go required)
	@echo "$(BLUE)⚡ Running quick tests (no Go required)...$(NC)"
	@$(MAKE) verify-all

# Full test suite (requires Go)
full-test: check-go test test-coverage lint vet ## Run full test suite (requires Go)
	@echo "$(GREEN)🎉 Full test suite completed!$(NC)"

# Production build
production-build: clean deps fmt vet lint test build-linux ## Full production build pipeline
	@echo "$(GREEN)🚀 Production build completed!$(NC)"

# Local workflow
local: fmt vet test-short ## Quick local workflow
	@echo "$(GREEN)⚡ Local workflow completed!$(NC)"

# Quality Assurance targets to prevent lint errors
qa-check: ## Run comprehensive quality checks (lint, vet, fmt, test)
	@echo "$(BLUE)🔍 Running comprehensive quality checks...$(NC)"
	@$(MAKE) fmt-check
	@$(MAKE) vet
	@$(MAKE) lint
	@$(MAKE) test-race
	@echo "$(GREEN)✅ All quality checks passed!$(NC)"

qa-fix: ## Auto-fix formatting, linting, and show remaining issues (COMPREHENSIVE)
	@echo "$(BLUE)🔧 COMPREHENSIVE Auto-fixing code quality issues...$(NC)"
	@echo "$(BLUE)═══════════════════════════════════════════════════$(NC)"
	@$(MAKE) format
	@echo "$(BLUE)═══════════════════════════════════════════════════$(NC)"
	@echo "$(GREEN)✨ All auto-fixes applied! Running final verification...$(NC)"
	@echo "$(BLUE)═══════════════════════════════════════════════════$(NC)"
	@$(MAKE) qa-check

fmt-check: ## Check if code is formatted (without fixing)
	@echo "$(BLUE)📝 Checking Go formatting...$(NC)"
	@if [ "$$($(GOFMT) -l . | wc -l)" -gt 0 ]; then \
		echo "$(RED)❌ Code is not formatted. Files that need formatting:$(NC)"; \
		$(GOFMT) -l .; \
		echo "$(YELLOW)💡 Run 'make format' to fix formatting issues$(NC)"; \
		exit 1; \
	else \
		echo "$(GREEN)✅ All files are properly formatted$(NC)"; \
	fi

pre-commit: ## Run all pre-commit checks (like git hook)
	@echo "$(BLUE)🔒 Running pre-commit checks...$(NC)"
	@$(MAKE) qa-check
	@echo "$(GREEN)✅ Pre-commit checks passed! Safe to commit.$(NC)"

lint-errors: ## Show detailed lint error explanations
	@echo "$(BLUE)🔍 Running detailed lint analysis...$(NC)"
	@echo "$(YELLOW)📋 Common Go lint errors and fixes:$(NC)"
	@echo "$(NC)  • errcheck: Handle or explicitly ignore error returns with '_ = function()'"
	@echo "$(NC)  • staticcheck: Follow Go conventions (e.g., avoid unnecessary type declarations)"
	@echo "$(NC)  • unused: Remove unused variables, functions, and imports"
	@echo "$(NC)  • See .cursor/rules/go-lint-prevention.md for complete guide"
	@echo ""
	@$(MAKE) lint

# Show current Go environment (if Go is installed)
go-env: ## Show Go environment
	@if command -v $(GOCMD) >/dev/null 2>&1; then \
		echo "$(BLUE)🔧 Go Environment:$(NC)"; \
		$(GOCMD) env; \
	else \
		echo "$(RED)❌ Go is not installed$(NC)"; \
	fi
