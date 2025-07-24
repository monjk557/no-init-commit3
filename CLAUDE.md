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

## Important Notes

- Always run `make format` before committing to ensure consistent code style
- Use `make setup` on first clone to initialize dependencies
- The service requires proper JWT configuration for authentication
- Permission checks integrate with external API-Service with fallback support
- All HTTP endpoints are mounted under configurable base path (default: /repos)