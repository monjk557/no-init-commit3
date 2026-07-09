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
# ---------------------------------------------------------------------
# CollabOps 사내 Go 공통 포맷/린트 표준 타겟 (fmt / fmt-check / lint / vet)
# 포맷터는 golangci-lint v2 (gofmt + goimports) 로 통합 관리한다.
# ---------------------------------------------------------------------
GOLANGCI_LINT_VERSION ?= v2.12.2
GOLANGCI_LINT ?= $(shell go env GOPATH)/bin/golangci-lint
GOLANGCI := $(shell command -v golangci-lint 2>/dev/null || echo $(GOLANGCI_LINT))

.PHONY: ensure-golangci-lint
ensure-golangci-lint:
	@command -v golangci-lint >/dev/null 2>&1 || test -x "$(GOLANGCI_LINT)" || \
		go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: fmt
fmt: ensure-golangci-lint ## 포맷 자동 적용 (gofmt + goimports)
	$(GOLANGCI) fmt

.PHONY: fmt-check
fmt-check: ensure-golangci-lint ## 포맷 검사만 (CI 게이트, 어긋나면 실패)
	$(GOLANGCI) fmt --diff

.PHONY: vet
vet: ## go vet 정적 검사
	go vet ./...

# [GUARD] make guard-errcodes
# Description: Issue-5577 ratchet — fail if handlers emit bare typed.NewHTTPError
# (no error_code) or HTTP 422 in production code. Keeps the central error catalog
# the single source of truth for error responses.
.PHONY: guard-errcodes
guard-errcodes: ## errcode ratchet (bare NewHTTPError / HTTP 422 금지)
	@bash scripts/check-error-codes.sh

# 하위호환: 기존 `make format` 호출은 표준 `fmt` 로 위임
.PHONY: format
format: fmt

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
lint: ensure-golangci-lint ## golangci-lint (.golangci.yml 사용)
	$(GOLANGCI) run ./...

# [PERF] make bench-overview-update|bench-overview-check
# Description: Generate/check benchmark baselines for overview performance (regression gate).
.PHONY: bench-overview-update bench-overview-check
bench-overview-update:
	@go run ./cmd/benchgate \
		-pkg ./internal/overview \
		-bench 'Benchmark_(GetLatestCommitsOnly|BlobSizeFillOnly|GetTreeWithCommit_Full|LatestCommitsCache_.*WarmHit)' \
		-benchtime 1s \
		-baseline .benchmarks/overview_baseline.json \
		-update

bench-overview-check:
	@go run ./cmd/benchgate \
		-pkg ./internal/overview \
		-bench 'Benchmark_(GetLatestCommitsOnly|BlobSizeFillOnly|GetTreeWithCommit_Full|LatestCommitsCache_.*WarmHit)' \
		-benchtime 1s \
		-baseline .benchmarks/overview_baseline.json \
		-max-regress 0.15

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

# [MANUAL 6] make test-cover
# Description: Run Go coverage over ./internal/... + the real cmd/git-executor and
#              print the total. Throwaway cmd/test-* programs are intentionally
#              excluded. Uses the repo-pinned Go toolchain (GOTOOLCHAIN).
# Used: internal/..., cmd/git-executor
.PHONY: test-cover
test-cover:
	@echo "🧪 Running coverage over ./internal/... + cmd/git-executor..."
	@GOTOOLCHAIN=go1.25.10 go test -coverprofile=coverage.out \
		./internal/... ./cmd/git-executor
	@echo "📊 Total coverage:"
	@GOTOOLCHAIN=go1.25.10 go tool cover -func=coverage.out | tail -1

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
