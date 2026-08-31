# ---------------------------------------------------------------------------
# 개발 도구 버전 — 반드시 고정한다.
#
# `@latest` 는 go.mod 의 toolchain 고정(go1.25.13)과 정면충돌한다. 도구가 더 높은
# Go 를 요구하는 버전을 내는 순간, 아무 커밋도 하지 않았는데 CI 가 깨진다.
# 실제로 staticcheck v0.8.0(go>=1.26)이 나오면서 install 단계가 실패했다.
#
# 올릴 때는 Go 요구사항을 먼저 확인한다:
#     go list -m -f '{{.GoVersion}}' honnef.co/go/tools@<version>
# 결과가 go.mod 의 toolchain 보다 높으면 올리지 않는다(또는 toolchain 을 먼저 올린다).
# golangci-lint 버전은 아래 fmt/lint 섹션의 GOLANGCI_LINT_VERSION 하나로 관리한다.
GOIMPORTS_VERSION   ?= v0.49.0
STATICCHECK_VERSION ?= v0.7.0

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
		$(MAKE) --no-print-directory ensure-golangci-lint; \
		go install golang.org/x/tools/cmd/goimports@$(GOIMPORTS_VERSION); \
		go install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION); \
	elif [ "$(filter qa,$(MAKECMDGOALS))" ] || [ "$(filter stage,$(MAKECMDGOALS))" ] || [ "$(filter prod,$(MAKECMDGOALS))" ]; then \
		echo "🏗️  Installing production tools..."; \
		go install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION); \
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

# [GUARD] make guard-specs
# Description: Issue-5806 guard — fail if the committed OpenAPI spec has a
# dangling $ref (schema referenced but not defined in components.schemas). Such a
# spec breaks downstream DTO codegen (orchestrator); catch it here, not there.
.PHONY: guard-specs
guard-specs: ## OpenAPI dangling $ref 가드 (downstream codegen 보호)
	@bash scripts/check-openapi-refs.sh

# [GUARD] make guard-spec-drift
# Description: 핸들러 = OpenAPI 스펙 SoT (HTTP-PROTOCOL §3). 스펙을 재생성(make specs)해
# 커밋본과 diff — 드리프트 시 fail. guard-specs(dangling $ref)와 상보적(핸들러↔스펙 동기).
.PHONY: guard-spec-drift
guard-spec-drift: ## OpenAPI 스펙 드리프트 — 재생성 후 커밋본과 diff (핸들러=SoT)
	@bash scripts/check-spec-drift.sh

# [GUARD] make guard-git-native
# Description: #6272 big-rule-1 — git/ssh substrate 를 native 로. go-git import 래칫
# + os.Chdir 금지(동시 push 안전) + 비-Context exec.Command 래칫(취소가능 git). GO-STANDARD §4/§7.
.PHONY: guard-git-native
guard-git-native: ## git/ssh substrate: native git(go-git 래칫)+os.Chdir 0+CommandContext
	@bash scripts/check-git-native.sh

# [GUARD] make guard-http-catalog
# Description: HTTP-PROTOCOL §1/§2 — http 응답을 에러 카탈로그(api.NewCoded)로.
# ad-hoc gin.H 응답 래칫(신규 유입 차단, legacy 는 NewCoded 로 수렴).
.PHONY: guard-http-catalog
guard-http-catalog: ## http substrate: 응답을 에러 카탈로그로 — ad-hoc gin.H 래칫
	@bash scripts/check-http-catalog.sh

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
# 로그 라인 계약 검증 (Issue-8015) — 실제 로거 출력을 계약 사본으로 대조
.PHONY: guard-logshape
guard-logshape:
	go test ./internal/logging/ -run 'Contract|Probe|Level|Error|Reserved|Masking|Secret' -count=1

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
	@GOTOOLCHAIN=go1.25.13 go test -coverprofile=coverage.out \
		./internal/... ./cmd/git-executor
	@echo "📊 Total coverage:"
	@GOTOOLCHAIN=go1.25.13 go tool cover -func=coverage.out | tail -1

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
