# [MANUAL 0] make install base|local|qa|stage|prod
# Description: Install Go dependencies and tools
# Used: go.mod, go.sum
# Options: local (local development), qa (QA), stage (staging), prod (production)
.PHONY: install base local qa stage prod
install:
	@echo "📦 Installing Go dependencies..."
	@go mod download
	@go mod tidy
	@echo "📦 Installing test dependencies..."
	@cd tests && go mod download && go mod tidy
	@if [ "$(filter local,$(MAKECMDGOALS))" ] || [ "$(words $(MAKECMDGOALS))" -eq 1 ]; then \
		echo "🔧 Installing local development tools..."; \
		command -v golangci-lint >/dev/null 2>&1 || { \
			echo "Installing golangci-lint..."; \
			curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin; \
		}; \
		go install golang.org/x/tools/cmd/goimports@latest; \
		go install honnef.co/go/tools/cmd/staticcheck@latest; \
	elif [ "$(filter qa,$(MAKECMDGOALS))" ] || [ "$(filter stage,$(MAKECMDGOALS))" ] || [ "$(filter prod,$(MAKECMDGOALS))" ]; then \
		echo "🏗️  Installing production tools..."; \
		go install honnef.co/go/tools/cmd/staticcheck@latest; \
	fi
	@echo "✅ Dependencies installed!"

# [MANUAL 1] make local [bg]
# Description: Start local development environment and mock services.
# Used: config/docker/compose/local.yml
# Options: bg (run in background)
.PHONY: local
local:
	@make check-docker
	@if [ "$(filter bg,$(MAKECMDGOALS))" ]; then \
		docker-compose -f config/docker/compose/local.yml up --build -d; \
	else \
		docker-compose -f config/docker/compose/local.yml up --build; \
	fi

# [MANUAL 2] make local-it [bg]
# Description: Start local development environment with real services.
# Used: config/docker/compose/local-it.yml
# Options: bg (run in background)
.PHONY: local-it
local-it:
	@if [ "$(filter bg,$(MAKECMDGOALS))" ]; then \
		docker-compose -f config/docker/compose/local-it.yml up --build -d; \
	else \
		docker-compose -f config/docker/compose/local-it.yml up --build; \
	fi

# [MANUAL 3] make build-it [bg]
# Description: Start local dev (builds all images from source)
# Used: config/docker/compose/build-it.yml
# Options: bg (run in background)
.PHONY: build-it
build-it:
	@if [ "$(filter bg,$(MAKECMDGOALS))" ]; then \
		docker-compose -f config/docker/compose/build-it.yml up --build -d; \
	else \
		docker-compose -f config/docker/compose/build-it.yml up --build; \
	fi

# [MANUAL 4] make specs [fetch|generate|fix|mock|mock-stop]
# Description: OpenAPI specs management (delegated to specs/Makefile)
# Used: specs/Makefile
# Options: 
#   - fetch: Fetch external specs (not implemented)
#   - generate: Generate OpenAPI spec from Go schemas and handlers
#   - fix: Fix spec issues (not implemented)
#   - mock: Start Prism mock server for API testing
#   - mock-stop: Stop Prism mock server
#   - help: Show help message
# Examples:
#   - make specs generate  # Generate OpenAPI spec from all package schemas
#   - make specs mock      # Start Prism mock server
#   - make specs           # Run default (generate)
.PHONY: specs fetch generate fix mock mock-stop
specs: ## 📋 OpenAPI specs management (fetch/generate/fix/mock)
	@if [ "$(filter fetch,$(MAKECMDGOALS))" ]; then \
		$(MAKE) -C specs fetch; \
	elif [ "$(filter generate,$(MAKECMDGOALS))" ]; then \
		$(MAKE) -C specs generate; \
	elif [ "$(filter fix,$(MAKECMDGOALS))" ]; then \
		$(MAKE) -C specs fix; \
	elif [ "$(filter mock,$(MAKECMDGOALS))" ]; then \
		$(MAKE) -C specs mock; \
	elif [ "$(filter mock-stop,$(MAKECMDGOALS))" ]; then \
		$(MAKE) -C specs mock-stop; \
	elif [ "$(filter help,$(MAKECMDGOALS))" ]; then \
		$(MAKE) -C specs help; \
	else \
		$(MAKE) -C specs all; \
	fi

# Alias for convenience: make specs-generate
specs-generate: specs generate
	@:

# Alias for convenience: make specs-fetch
specs-fetch: specs fetch
	@:

# Alias for convenience: make specs-fix
specs-fix: specs fix
	@:

# [MANUAL 4] make format
# Description: Auto-format code and check formatting (matches CI behavior)
.PHONY: format
format:
	@echo "🎨 Formatting Go code..."
	@find . -name "*.go" -not -path "./build/*" -not -path "./vendor/*" -exec gofmt -w {} \;
	@echo "📦 Organizing imports..."
	@find . -name "*.go" -not -path "./build/*" -not -path "./vendor/*" -exec goimports -w {} \; 2>/dev/null || echo "⚠️  goimports not found, skipping import organization"
	@echo "🔍 Checking code formatting..."	
	@UNFORMATTED=$$(find . -name "*.go" -not -path "./build/*" -not -path "./vendor/*" -exec gofmt -l {} \;); \
	if [ -n "$$UNFORMATTED" ]; then \
		echo "❌ Code not formatted:"; \
		echo "$$UNFORMATTED"; \
		exit 1; \
	fi
	@echo "🧹 Tidying modules..."
	@TIDY_OUTPUT=$$(go mod tidy 2>&1); TIDY_EXIT=$$?; \
	if [ $$TIDY_EXIT -ne 0 ]; then \
		if echo "$$TIDY_OUTPUT" | grep -q "build/go-mod" && ! echo "$$TIDY_OUTPUT" | grep -v "build/go-mod" | grep -q "go:"; then \
			echo "⚠️  go mod tidy: warnings about build/go-mod (ignored)"; \
		else \
			echo "$$TIDY_OUTPUT"; \
			exit $$TIDY_EXIT; \
		fi; \
	fi
	@echo "✅ Code formatting complete!"

# Build the application
.PHONY: build
build:
	@echo "🔨 Building application..."
	@mkdir -p bin
	@go build -o bin/git-executor ./cmd/git-executor
	@echo "✅ Build complete: bin/git-executor"

# [MANUAL 4.5] make lint
# Description: Run linters and static analysis
.PHONY: lint
lint:
	@echo "🔍 Running Go linters..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "⚠️  golangci-lint not found, running basic checks..."; \
		go vet ./...; \
		if command -v staticcheck >/dev/null 2>&1; then staticcheck ./...; fi; \
	fi
	@echo "✅ Linting complete!"

# [MANUAL 5] make test all|unit|api|integration|e2e [core|config|util|perf]
# Description: Run tests with new organized structure
# Options:
#   - all: Run all tests in Docker
#   - unit: Run unit tests (with optional subcategories: core, api, config, util, perf)
#   - api: Run API endpoint tests
#   - integration: Run integration tests
#   - e2e: Run end-to-end tests
.PHONY: test all unit api integration e2e core config util perf
test:
	@if [ "$(filter all,$(MAKECMDGOALS))" ]; then \
		docker-compose -f config/docker/compose/local-it.yml up --build --abort-on-container-exit --exit-code-from collabops-git-executor; \
	elif [ "$(filter unit,$(MAKECMDGOALS))" ]; then \
		cd tests && \
		if [ "$(filter core,$(MAKECMDGOALS))" ]; then \
			go test -v ./unit/core/...; \
		elif [ "$(filter api,$(MAKECMDGOALS))" ]; then \
			go test -v ./unit/api/...; \
		elif [ "$(filter config,$(MAKECMDGOALS))" ]; then \
			go test -v ./unit/config/...; \
		elif [ "$(filter util,$(MAKECMDGOALS))" ]; then \
			go test -v ./unit/util/...; \
		elif [ "$(filter perf,$(MAKECMDGOALS))" ]; then \
			go test -v ./unit/perf/...; \
		else \
			go test -v ./unit/...; \
		fi; \
	elif [ "$(filter api,$(MAKECMDGOALS))" ]; then \
		cd tests && go test -v ./unit/api/...; \
	elif [ "$(filter integration,$(MAKECMDGOALS))" ]; then \
		cd tests && go test -v ./integration/...; \
		cd .. && go test -v ./test/integration/...; \
	elif [ "$(filter e2e,$(MAKECMDGOALS))" ]; then \
		$(MAKE) local-it bg; \
		sleep 10; \
		cd tests && go test -v -tags=e2e ./e2e/...; \
	else \
		cd tests && go test -race -v ./...; \
		cd .. && go test -v ./test/integration/...; \
	fi

# [MANUAL 7] make clean
# Description: Gracefully stop all services and clean up everything
.PHONY: clean
clean:
	@echo "🛑 Gracefully stopping all Docker environments..."
	@docker-compose -f config/docker/compose/local.yml down 2>/dev/null || true
	@docker-compose -f config/docker/compose/local-it.yml down 2>/dev/null || true
	@docker-compose -f config/docker/compose/build-it.yml down 2>/dev/null || true
	@echo "🧹 Cleaning up unused Docker resources..."
	@docker system prune -f 2>/dev/null || true
	@echo "🔄 Checking for any remaining processes on our ports..."
	@lsof -ti:2345,3000,8000,8001,9091 | xargs kill -15 2>/dev/null || true
	@echo "✅ All cleanup complete!"

# [HELPER] Dummy targets for command arguments
.PHONY: bg all e2e base qa stage prod core config util perf fetch generate fix mock mock-stop
bg all e2e base qa stage prod core config util perf fetch generate fix mock mock-stop:
	@:

# [HELPER] Check Docker status
.PHONY: check-docker
check-docker:
	@if ! docker info >/dev/null 2>&1; then echo "❌ Docker not running"; exit 1; else echo "✅ Docker is running"; fi
