# CollabOps Git Executor - Go Project Makefile

# Build configuration
BINARY_NAME=git-executor
BUILD_DIR=bin
GO_FILES=$(shell find . -name "*.go" -type f)
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS=-ldflags "-X main.version=$(VERSION)"

# Default target
.PHONY: all
all: clean build

# Build the application
.PHONY: build
build:
	@echo "🔨 Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	@go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/git-executor
	@echo "✅ Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

# Run tests
.PHONY: test
test:
	@echo "🧪 Running tests..."
	@go test -race -v ./...
	@echo "✅ All tests passed!"

# Run tests with coverage
.PHONY: test-coverage
test-coverage:
	@echo "🧪 Running tests with coverage..."
	@go test -race -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "✅ Coverage report generated: coverage.html"

# Format code
.PHONY: format
format:
	@echo "🎨 Formatting Go code..."
	@gofmt -w .
	@go mod tidy
	@echo "✅ Code formatted!"

# Lint code
.PHONY: lint
lint:
	@echo "🔍 Running linters..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "⚠️  golangci-lint not found, running basic checks..."; \
		go vet ./...; \
		gofmt -l . | grep . && exit 1 || true; \
	fi
	@echo "✅ Linting complete!"

# Clean build artifacts
.PHONY: clean
clean:
	@echo "🧹 Cleaning build artifacts..."
	@rm -rf $(BUILD_DIR)
	@rm -f coverage.out coverage.html
	@echo "✅ Clean complete!"

# Run the application
.PHONY: run
run: build
	@echo "🚀 Starting $(BINARY_NAME)..."
	@./$(BUILD_DIR)/$(BINARY_NAME) -config config/test-config.yaml

# Development server with auto-reload
.PHONY: dev
dev:
	@echo "🔄 Starting development server..."
	@go run ./cmd/git-executor -config config/test-config.yaml

# Docker build
.PHONY: docker-build
docker-build:
	@echo "🐳 Building Docker image..."
	@docker build -t collabops-git-executor:$(VERSION) -f deployments/Dockerfile .
	@echo "✅ Docker image built: collabops-git-executor:$(VERSION)"

# Docker compose - local development
.PHONY: local-it
local-it:
	@echo "🐳 Starting local integration environment..."
	@docker-compose -f config/docker/compose/local-it.yml up --build

# Docker compose - background
.PHONY: local-it-bg
local-it-bg:
	@echo "🐳 Starting local integration environment (background)..."
	@docker-compose -f config/docker/compose/local-it.yml up --build -d

# Stop Docker compose
.PHONY: stop
stop:
	@echo "🛑 Stopping Docker services..."
	@docker-compose -f config/docker/compose/local-it.yml down

# Install development dependencies
.PHONY: install-deps
install-deps:
	@echo "📦 Installing development dependencies..."
	@go mod download
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "Installing golangci-lint..."; \
		go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest; \
	fi
	@echo "✅ Dependencies installed!"

# Generate mocks (if using mockery)
.PHONY: mocks
mocks:
	@echo "🎭 Generating mocks..."
	@if command -v mockery >/dev/null 2>&1; then \
		mockery --all --output ./internal/mocks; \
	else \
		echo "⚠️  mockery not found, skipping mock generation"; \
	fi

# Integration tests
.PHONY: test-integration
test-integration:
	@echo "🧪 Running integration tests..."
	@go test -tags=integration -v ./test/integration/...

# Benchmark tests
.PHONY: bench
bench:
	@echo "⚡ Running benchmarks..."
	@go test -bench=. -benchmem ./...

# Security scan
.PHONY: security
security:
	@echo "🔒 Running security scan..."
	@if command -v gosec >/dev/null 2>&1; then \
		gosec ./...; \
	else \
		echo "⚠️  gosec not found, install with: go install github.com/securecodewarrior/gosec/v2/cmd/gosec@latest"; \
	fi

# Kill processes on common ports
.PHONY: kill-ports
kill-ports:
	@echo "🔪 Killing processes on ports 3000, 8080, 9091..."
	@lsof -ti:3000,8080,9091 | xargs kill -9 2>/dev/null || true
	@echo "✅ Ports cleared!"

# Help
.PHONY: help
help:
	@echo "CollabOps Git Executor - Available Commands:"
	@echo ""
	@echo "Build & Run:"
	@echo "  build          Build the application"
	@echo "  run            Build and run the application"
	@echo "  dev            Run development server with auto-reload"
	@echo "  clean          Clean build artifacts"
	@echo ""
	@echo "Testing:"
	@echo "  test           Run all tests"
	@echo "  test-coverage  Run tests with coverage report"
	@echo "  test-integration Run integration tests"
	@echo "  bench          Run benchmark tests"
	@echo ""
	@echo "Code Quality:"
	@echo "  format         Format Go code"
	@echo "  lint           Run linters"
	@echo "  security       Run security scan"
	@echo ""
	@echo "Docker:"
	@echo "  docker-build   Build Docker image"
	@echo "  local-it       Start local integration environment"
	@echo "  local-it-bg    Start local integration environment (background)"
	@echo "  stop           Stop Docker services"
	@echo ""
	@echo "Development:"
	@echo "  install-deps   Install development dependencies"
	@echo "  mocks          Generate mocks"
	@echo "  kill-ports     Kill processes on common ports"
	@echo ""