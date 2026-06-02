# CollabOps Git Executor test

Git protocol server and REST API engine for the CollabOps platform.
Handles Git push/pull/fetch operations and provides repository management APIs.

## Architecture

Feature-based package structure under `internal/`:

```
internal/
  ├── overview/       # Repository overview (tree, commits, branches)
  ├── commits/        # Commit history and multi-file commits
  ├── branches/       # Branch CRUD and validation
  ├── tags/           # Tag listing and creation
  ├── blob/           # File content retrieval
  ├── tree/           # Directory tree listing
  ├── diffs/          # Diff and patch generation
  ├── merge/          # Merge operations and preview
  ├── state/          # Repository state and metadata
  ├── langstats/      # Language distribution statistics
  ├── fork/           # Repository forking and upstream sync
  ├── protocol/       # Git Smart HTTP protocol (upload-pack, receive-pack)
  ├── server/         # HTTP server, routing, middleware
  ├── config/         # YAML configuration loading
  ├── repositories/   # Repository path management and locking
  └── ...
```

Each feature package follows: **Handler** (HTTP) → **Service** (business logic) → **Schema** (request/response types).

## Quick Start

```bash
# Start mock permission service (for development)
cd test/mock-permission-service && node server.js

# Build and run
make build
./bin/git-executor -config config/test-config.yaml

# Health check
curl http://localhost:3000/health
```

## API Overview

### Git Protocol

| Endpoint | Method | Purpose |
|---|---|---|
| `/{owner}/{repo}/info/refs` | GET | Reference advertisement (fetch/push capability discovery) |
| `/{owner}/{repo}/git-upload-pack` | POST | Fetch/clone operations |
| `/{owner}/{repo}/git-receive-pack` | POST | Push operations |
| `/ssh/{owner}/{repo}/upload-pack` | POST | SSH-forwarded fetch |
| `/ssh/{owner}/{repo}/receive-pack` | POST | SSH-forwarded push |

### REST API

All REST endpoints require `X-USER-ID` and `X-Source` headers.

#### Repository

| Endpoint | Method | Purpose |
|---|---|---|
| `/repos/{owner}/{repo}` | POST | Create repository |
| `/repos/{owner}/{repo}/state` | GET | Repository state (HEAD, branches, tags, commit count) |
| `/repos/{owner}/{repo}/overview` | GET | Lightweight overview (tree + latest commit + branches) |
| `/repos/{owner}/{repo}/languages` | GET | Language distribution |

#### Content

| Endpoint | Method | Purpose |
|---|---|---|
| `/repos/{owner}/{repo}/tree` | GET | Directory tree listing |
| `/repos/{owner}/{repo}/blob` | GET | File content |
| `/repos/{owner}/{repo}/commits` | GET | Commit history (paginated) |
| `/repos/{owner}/{repo}/commits` | POST | Multi-file commit |
| `/repos/{owner}/{repo}/commits/{sha}` | GET | Single commit detail |
| `/repos/{owner}/{repo}/tags` | GET | Tag listing |
| `/repos/{owner}/{repo}/tags` | POST | Create tag |

#### Branches

| Endpoint | Method | Purpose |
|---|---|---|
| `/repos/{owner}/{repo}/branches` | GET | List branches |
| `/repos/{owner}/{repo}/branches` | POST | Create branch |
| `/repos/{owner}/{repo}/branches/validate/*name` | GET | Validate branch name |
| `/repos/{owner}/{repo}/branches/resolve/*ref` | GET | Resolve branch reference |

#### Diff & Merge

| Endpoint | Method | Purpose |
|---|---|---|
| `/repos/{owner}/{repo}/diff/commits/{base}/{head}` | GET | Commit diff |
| `/repos/{owner}/{repo}/diff/branches/{base}/{head}` | GET | Branch diff |
| `/repos/{owner}/{repo}/diff/branches` | GET | Branch diff (query params, supports `/` in names) |
| `/repos/{owner}/{repo}/diff/file` | GET | Single file diff |
| `/repos/{owner}/{repo}/diff/commit/{sha}` | GET | Single commit diff |
| `/repos/{owner}/{repo}/diff/patch/{base}/{head}` | GET | Git patch format |
| `/repos/{owner}/{repo}/merges` | POST | Merge branches |
| `/repos/{owner}/{repo}/merges/preview` | GET | Merge preview (dry run) |
| `/repos/{owner}/{repo}/merges/diff` | GET | Merge diff |

#### Overview (Composite)

| Endpoint | Method | Purpose |
|---|---|---|
| `/repos/{owner}/{repo}/overview` | GET | Tree + latest commit + branches |
| `/repos/{owner}/{repo}/overview/tree` | GET | Tree overview |
| `/repos/{owner}/{repo}/overview/tree/commits` | GET | Tree with commit metadata |
| `/repos/{owner}/{repo}/overview/blob` | GET | Blob overview |
| `/repos/{owner}/{repo}/overview/branches` | GET | Branches overview (grouped by activity) |
| `/repos/{owner}/{repo}/overview/latest-commits` | POST | Batch latest commits for paths |

#### Fork

| Endpoint | Method | Purpose |
|---|---|---|
| `/repos/{owner}/{repo}/forks` | POST | Fork repository |
| `/repos/{owner}/{repo}/fork` | GET | Fork info |
| `/repos/{owner}/{repo}/fork/sync` | POST | Sync with upstream |

### Authentication

| Gateway | Required Headers |
|---|---|
| HTTP | `X-USER-ID`, `X-Source: http-gateway` |
| SSH | `X-USER-ID`, `X-Source: ssh-gateway` |
| REST API | `X-USER-ID`, `X-Source: orchestrator` |

### API Documentation (Swagger)

```bash
# With base path
open http://localhost:8080/repos/api-docs/swagger/index.html

# Without base path (dev)
open http://localhost:3000/api-docs/swagger/index.html
```

## Development

```bash
# Format code
make format

# Lint
go vet ./...

# Run all tests
make test

# Run specific package tests
go test ./internal/merge -v
go test ./internal/overview -v
go test ./internal/state -v
```

## Configuration

```yaml
server:
  port: 3000              # HTTP port
  enable_docs: true       # Swagger UI
  docs_path: "/api-docs"  # Docs endpoint path

git:
  repo_root: "/data/repos" # Repository storage root
```

Dev: `localhost:3000` / Production: `localhost:9091`