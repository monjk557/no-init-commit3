# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Common Development Commands

### Build and Test
- `make test` - Run all tests with verbose output
- `make test-config` - Run configuration package tests specifically
- `make test-coverage` - Run tests with coverage report (generates coverage.html)
- `make build` - Build the git-executor binary to bin/
- `make clean` - Clean build artifacts

### Code Quality
- `make format` - Auto-format code, organize imports, run go mod tidy, and show lint results
- `make lint` - Run golangci-lint (requires installation via make install-tools)
- `make vet` - Run go vet static analysis

### Development Workflows
- `make local` - Quick local workflow: format + vet + short tests
- `make full-test` - Complete test suite with coverage and linting
- `make setup` - Initial project setup (download deps, create go.sum)

### Manual Testing
- `make test-manual-config` - Test configuration loading manually
- `make test-manual-paths` - Test repository path management
- `make test-manual-server` - Start server for manual testing (localhost:9090)

### Docker
- `make docker-build` - Build production Docker image
- `make docker-compose-up` - Start services with docker-compose

## Architecture Overview

This is a Git repository executor service that handles Git operations for the CollabOps platform. It sits between Git client gateways (SSH/HTTP) and the filesystem, providing secure repository access with permission verification.

### Core Components

**Configuration System** (`internal/config/`)
- YAML-based configuration with environment variable overrides (COLLABOPS_ prefix)
- Validates server, Git, JWT, and logging settings
- Supports multiple config locations: ./config, ., /etc/collabops

**HTTP Server** (`internal/server/`)
- Chi router with graceful shutdown support
- Health check endpoint at /health
- Configurable base path (default: /repos)
- Structured middleware: RequestID, RealIP, Logger, Recoverer, Timeout

**Permission System** (`internal/permissions/`)
- Client interface for repository access verification
- Caching layer with TTL support
- Circuit breaker pattern for resilience
- Fallback policies when API-Service is unavailable
- Comprehensive retry logic with exponential backoff

**Repository Management** (`internal/repository/`)
- Path resolution and validation for Git repositories
- Security checks to prevent path traversal

**Logging** (`internal/logging/`)
- Structured logging with Zap
- Configurable levels and formats (JSON/text)
- Error handling utilities and middleware

### Key Configuration Sections

- **Server**: Port (8080), host, timeouts, base path
- **Git**: Repository root (/var/lib/git/repos), packfile support, caching
- **JWT**: Algorithm validation (RS256/HS256/ES256), key paths
- **Logging**: Level, format, output destination

## API Endpoints Overview

**📚 Complete Reference**: See `docs/api/ENDPOINTS_REFERENCE.md` for comprehensive list of ALL available endpoints.

### ✅ Currently Available (25 endpoints)
- **Health & Monitoring**: `/health`, `/health/liveness`, `/health/readiness`, `/metrics`
- **Git Objects**: `/repos/:owner/:repo/tree`, `/repos/:owner/:repo/blob`, `/repos/:owner/:repo/commits`
- **Diff & Comparison**: `/repos/:owner/:repo/diff/commits/:base/:head`, `/repos/:owner/:repo/diff/branches/:base/:head`
- **File Operations**: `/repos/:owner/:repo/files/*path` (PUT/DELETE), `/repos/:owner/:repo/commits` (POST)
- **Branch Management**: `/repos/:owner/:repo/branches` (POST/PATCH/DELETE)
- **Repository State**: `/repos/:owner/:repo/state`, `/repos/:owner/:repo/head`, `/repos/:owner/:repo/references`
- **Git Smart HTTP**: `/:owner/:repo/info/refs`, `/:owner/:repo/git-upload-pack`, `/:owner/:repo/git-receive-pack`
- **SSH Protocol**: `/ssh/:owner/:repo/upload-pack`, `/ssh/:owner/:repo/receive-pack`

### ❌ Critical Missing Endpoints (HIGH PRIORITY)

#### 1. Single Commit Diff (PRIORITY 1 - Essential for Code Review)
**Missing**: `GET /repos/:owner/:repo/commits/:sha/diff`
**Problem**: No way to see "what changed in this specific commit"
**Impact**: Blocks code review workflows, security auditing, commit analysis
**Implementation Status**: 90% ready - can reuse existing diff logic in `internal/protocol/diff.go`

#### 2. RESTful File Content Reading (PRIORITY 2 - API Consistency)
**Missing**: `GET /repos/:owner/:repo/files/:path?ref=:ref&lines=:range`
**Problem**: Current blob endpoint uses awkward query params, no line ranges
**Impact**: Poor API UX, missing functionality for documentation systems
**Implementation Status**: 90% ready - can enhance existing blob logic in `internal/protocol/blob.go`

## Critical Missing Endpoints - Implementation Guide

### 🎯 IMMEDIATE PRIORITY: Single Commit Diff

**Why Critical**: Every Git hosting platform has this. Essential for code review UX.

**Technical Approach**:
```go
// Add to internal/server/server.go
repoGroup.GET("/commits/:sha/diff", gin.WrapF(diffHandler.HandleSingleCommitDiff))

// Add to internal/protocol/diff.go
func (h *DiffHandler) HandleSingleCommitDiff(w http.ResponseWriter, r *http.Request) {
    sha := URLParam(r, "sha")

    // 1. Resolve commit and find parent
    commit, parentSHA, err := h.resolveCommitAndParent(repo, sha)

    // 2. Reuse existing calculateCommitDiff logic
    diffResp, err := h.calculateCommitDiff(ctx, repoPath, parentSHA, sha, contextLines)

    // 3. Enhanced response with commit metadata
    response := &SingleCommitDiffResponse{
        Commit: extractCommitMetadata(commit),
        Changes: diffResp,
    }
}
```

**Edge Cases to Handle**:
- Initial commits (no parent) - compare against empty tree
- Merge commits (multiple parents) - use first parent
- Invalid SHA - return 404 with clear error

**Files to Modify**:
- `internal/server/server.go` - Add route registration
- `internal/protocol/diff.go` - Add handler function
- `internal/protocol/diff_test.go` - Add comprehensive tests

### 🔧 SECONDARY PRIORITY: RESTful File Content

**Why Important**: Better API design, adds line range functionality.

**Technical Approach**:
```go
// Add to internal/server/server.go
repoGroup.GET("/files/*path", fileContentHandler.HandleFileContent)

// Create new internal/protocol/file_content.go
func (h *FileContentHandler) HandleFileContent(c *gin.Context) {
    path := c.Param("path")[1:]  // Remove leading slash
    ref := c.Query("ref")        // Default to main
    lines := c.Query("lines")    // Optional: "10-20"

    // Reuse existing blob handler logic
    content, err := h.blobHandler.getBlobContent(ctx, repoPath, path, ref)

    // NEW: Add line range filtering
    if lines != "" {
        content = h.filterContentByLines(content, lines)
    }
}
```

**Files to Create/Modify**:
- `internal/protocol/file_content.go` - New handler (wraps existing blob logic)
- `internal/server/server.go` - Add route registration
- Tests for line range parsing and filtering

### 🛠 Implementation Infrastructure Already Available

**Security & Permissions** (✅ Ready):
- Path traversal protection in `internal/repository/path.go`
- Permission checking via `X-USER-ID` header validation
- Authentication middleware in place

**Git Operations** (✅ Ready):
- Blob reading: `internal/git/gogit_repository.go` - `GetBlob()` method
- Diff calculation: `internal/protocol/diff.go` - `calculateCommitDiff()` method
- Commit resolution: Existing reference resolution logic

**Response Handling** (✅ Ready):
- JSON response formatting in existing handlers
- Error handling patterns established
- Structured API responses with request IDs

## Test Structure

All packages have comprehensive test coverage:
- Configuration: Load, validate, environment overrides, error cases
- Server: HTTP handlers, graceful shutdown, middleware
- Permissions: API integration, caching, circuit breaker, fallback policies
- Repository: Path resolution, security validation

Use `make verify-structure` and `make verify-config` for project validation without requiring Go installation.

## Entry Points

- `cmd/git-executor/main.go` - Main HTTP server
- `cmd/test-config/main.go` - Configuration testing utility
- `cmd/test-repo-paths/main.go` - Repository path testing utility

## Implementation Success Criteria

### Single Commit Diff Endpoint
- ✅ Handles normal commits (with single parent)
- ✅ Handles initial commits (no parent)
- ✅ Handles merge commits (multiple parents)
- ✅ Returns commit metadata + diff in single response
- ✅ Performance: <500ms for typical commits
- ✅ Error handling: 404 for invalid SHA, proper error messages

### RESTful File Content Endpoint
- ✅ RESTful path-based access pattern
- ✅ Line range support (e.g., `?lines=10-20`)
- ✅ Backward compatibility with existing blob endpoint
- ✅ Performance: <200ms for files under 1MB
- ✅ Proper content-type detection and caching headers

## Important Notes

- Always run `make format` before committing to ensure consistent code style
- Use `make setup` on first clone to initialize dependencies
- The service requires proper JWT configuration for authentication
- Permission checks integrate with external API-Service with fallback support
- All HTTP endpoints are mounted under configurable base path (default: /repos)
- **90% of implementation is already done** - these missing endpoints mainly need REST wrappers around existing Git operations
- Both missing endpoints are essential for Git hosting platform feature parity
- Implementation can reuse existing security, validation, and Git operation infrastructure

## Quick Start for Missing Endpoints Implementation

1. **Review Current Endpoints**: Check `docs/api/ENDPOINTS_REFERENCE.md`
2. **Understand Existing Patterns**: Look at `internal/protocol/diff.go` and `internal/protocol/blob.go`
3. **Start with Single Commit Diff**: Highest impact, lowest effort
4. **Test Thoroughly**: All commit types, error cases, performance
5. **Add RESTful File Content**: Enhanced API consistency and functionality
