# Docker Deployment Guide

This directory contains Docker configuration files for running the CollabOps Git Executor in different environments.

## Files

- `Dockerfile` - Production-ready multi-stage build
- `Dockerfile.dev` - Development image with hot reload and tools
- `docker-compose.yml` - Complete orchestration for local development
- `.dockerignore` - Optimizes build context

## Quick Start

### 1. Production Build & Run

```bash
# Build production image
make docker-build

# Run with make
make docker-run

# Or run with docker directly
docker run --rm -p 9090:9090 \
  -v $(PWD)/config:/config:ro \
  -v git-repos:/repos \
  collabops/git-executor:latest
```

### 2. Docker Compose (Recommended)

```bash
# Start production services
make docker-compose-up

# Start with development tools (includes hot reload)
make docker-compose-up-dev

# Stop services
make docker-compose-down
```

### 3. Development Mode

```bash
# Build development image
make docker-build-dev

# Run development container
make docker-run-dev
```

## Configuration

### Port Configuration
- **Production**: Runs on port 9090 (configurable via COLLABOPS_SERVER_PORT)
- **Development**: Accessible on port 9091 (external) → 9090 (internal)

### Volume Mounts
- **Configuration**: `./config` → `/config` (read-only)
- **Git Repositories**: Named volume `git-repos` → `/repos`
- **Development Source**: `./` → `/app` (dev mode only)

### Environment Variables

All configuration can be overridden using environment variables with the `COLLABOPS_` prefix:

```bash
COLLABOPS_SERVER_HOST=0.0.0.0
COLLABOPS_SERVER_PORT=9090
COLLABOPS_GIT_REPO_ROOT=/repos
COLLABOPS_LOG_LEVEL=debug
COLLABOPS_LOG_FORMAT=json
```

## Image Details

### Production Image (`Dockerfile`)
- **Base**: `scratch` (minimal, secure)
- **Size**: ~12.7MB
- **Security**: Non-root user (65534:65534)
- **Features**: Health check, static binary, CA certificates

### Development Image (`Dockerfile.dev`)
- **Base**: `golang:1.23-alpine`
- **Size**: ~800MB (includes Go toolchain)
- **Tools**: Air (hot reload), golangci-lint, make, curl, jq
- **Features**: Source code mounting, development debugging

## Health Checks

Both images include health checks:
- **Production**: Built-in health check via binary
- **Development**: HTTP health check via curl

## Security Features

### Production Container
- Read-only root filesystem
- No new privileges
- Temporary filesystem for `/tmp`
- Non-root user execution
- Minimal attack surface (scratch base)

### Network
- Custom bridge network (`collabops-network`)
- Isolated from host network by default
- Explicit port mapping required

## Troubleshooting

### Build Issues
```bash
# Clean and rebuild
docker system prune -f
make docker-build

# Check Go version compatibility
docker run --rm golang:1.23-alpine go version
```

### Runtime Issues
```bash
# Check container logs
docker logs collabops-git-executor

# Debug inside container
docker exec -it collabops-git-executor sh

# Check health status
docker ps --filter name=collabops-git-executor
```

### Development Issues
```bash
# Use development profile
docker-compose --profile dev up

# Check mounted volumes
docker inspect collabops-git-executor-dev
```

## Available Make Targets

| Target | Description |
|--------|-------------|
| `docker-build` | Build production image |
| `docker-build-dev` | Build development image |
| `docker-run` | Run production container |
| `docker-run-dev` | Run development container |
| `docker-compose-up` | Start with docker-compose |
| `docker-compose-up-dev` | Start development services |
| `docker-compose-down` | Stop all services |
| `docker-shell` | Get shell access to container |

## Examples

### Custom Configuration
```bash
# Run with custom config file
docker run --rm -p 9090:9090 \
  -v $(PWD)/my-config.yaml:/config/config.yaml:ro \
  collabops/git-executor:latest --config /config/config.yaml
```

### Production Deployment
```bash
# With docker-compose (recommended)
cd deployments
docker-compose up -d

# Check status
docker-compose ps
docker-compose logs -f git-executor
```

### Development Workflow
```bash
# Start development environment
make docker-compose-up-dev

# Make code changes...
# Container automatically reloads via Air

# Run tests inside container
docker exec collabops-git-executor-dev go test ./...
``` 