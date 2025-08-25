# Git Executor - Complete API Endpoints Reference

This document lists ALL available endpoints in the CollabOps Git Executor service.

## Base Configuration
- **Default Port**: 8080
- **Base Path**: Configurable (default: `/`)
- **Content-Type**: `application/json` (REST APIs)

## Core Service Endpoints

### Health & Status
| Method | Endpoint | Description | Response |
|--------|----------|-------------|----------|
| `GET` | `/health` | Simple health check (backward compatibility) | `{"status": "healthy", "timestamp": "..."}` |
| `GET` | `/health/liveness` | Kubernetes liveness probe | Health status |
| `GET` | `/health/readiness` | Kubernetes readiness probe | Service readiness |
| `GET` | `/metrics` | Prometheus metrics endpoint | Metrics in Prometheus format |
| `GET` | `/` | Service info (when using base path) | `{"service": "git-executor", "version": "1.0.0", "status": "running"}` |

## REST API Endpoints

All REST API endpoints are under `/repos/:owner/:repo` pattern.

### Repository Management
| Method | Endpoint | Description | Request Body |
|--------|----------|-------------|--------------|
| `POST` | `/repos/:owner/:repo` | Create new repository | `{"description": "...", "initial_commit": true, "default_branch": "main", "author": {...}}` |
| `GET` | `/repos/:owner/:repo/state` | Get repository state and status | - |
| `GET` | `/repos/:owner/:repo/head` | Get repository HEAD reference | - |
| `GET` | `/repos/:owner/:repo/references` | Get all repository references (branches, tags) | - |

### Git Object Access
| Method | Endpoint | Description | Query Params |
|--------|----------|-------------|--------------|
| `GET` | `/repos/:owner/:repo/tree` | Get tree objects | `sha`, `path`, `recursive` |
| `GET` | `/repos/:owner/:repo/blob` | Get blob (file) contents | `sha`, `path` |
| `GET` | `/repos/:owner/:repo/commits` | Get commit history | `ref`, `limit`, `skip` |

### Diff & Comparison
| Method | Endpoint | Description | Query Params |
|--------|----------|-------------|--------------|
| `GET` | `/repos/:owner/:repo/diff/commits/:base/:head` | Compare two commits | `context_lines`, `ignore_whitespace` |
| `GET` | `/repos/:owner/:repo/diff/branches/:base/:head` | Compare two branches | `context_lines`, `ignore_whitespace` |
| `GET` | `/repos/:owner/:repo/diff/file` | Compare file between refs | `base`, `head`, `path`, `context_lines` |
| `GET` | `/repos/:owner/:repo/patch/:base/:head` | Generate Git patch format | Same as diff endpoints |

### File Operations (Server-side Git)
| Method | Endpoint | Description | Request Body |
|--------|----------|-------------|--------------|
| `PUT` | `/repos/:owner/:repo/files/*path` | Create/update file | `{"content": "...", "message": "...", "branch": "...", "author": {...}}` |
| `DELETE` | `/repos/:owner/:repo/files/*path` | Delete file | `{"message": "...", "branch": "...", "author": {...}}` |
| `POST` | `/repos/:owner/:repo/commits` | Multi-file commit | `{"files": [...], "message": "...", "branch": "...", "author": {...}}` |

### Branch Operations
| Method | Endpoint | Description | Request Body |
|--------|----------|-------------|--------------|
| `POST` | `/repos/:owner/:repo/branches` | Create branch | `{"name": "...", "source": "...", "message": "..."}` |
| `PATCH` | `/repos/:owner/:repo/branches/*branch` | Update branch (fast-forward) | `{"target": "...", "message": "..."}` |
| `DELETE` | `/repos/:owner/:repo/branches/*branch` | Delete branch | `{"message": "..."}` |

## Git Smart HTTP Protocol Endpoints

These endpoints implement the Git Smart HTTP protocol for `git clone`, `git fetch`, and `git push` operations.

### Info/Refs (Discovery)
| Method | Endpoint | Description | Query Params |
|--------|----------|-------------|--------------|
| `GET` | `/:owner/:repo/info/refs` | Git capability advertisement | `service=git-upload-pack` or `service=git-receive-pack` |

### Upload Pack (Fetch Operations)
| Method | Endpoint | Description | Usage |
|--------|----------|-------------|-------|
| `POST` | `/:owner/:repo/git-upload-pack` | Git fetch/clone operations | Used by `git clone`, `git fetch` |

### Receive Pack (Push Operations)
| Method | Endpoint | Description | Usage |
|--------|----------|-------------|-------|
| `POST` | `/:owner/:repo/git-receive-pack` | Git push operations | Used by `git push` |

## SSH Protocol Endpoints

These endpoints handle SSH-forwarded Git operations.

### SSH Git Operations
| Method | Endpoint | Description | Usage |
|--------|----------|-------------|-------|
| `POST` | `/ssh/:owner/:repo/upload-pack` | SSH-forwarded fetch | SSH gateway → executor |
| `POST` | `/ssh/:owner/:repo/receive-pack` | SSH-forwarded push | SSH gateway → executor |

## Authentication & Authorization

All endpoints require proper authentication:

### Headers
- **Primary**: `X-USER-ID` (user identifier from JWT decoded by gateway)
- **Fallback**: `Authorization: Bearer <jwt>` (direct JWT in executor)

### Permission Model
- **Read operations**: Require `read` permission on repository
- **Write operations**: Require `write` permission on repository
- **Branch operations**: May require additional `admin` permission

## Request/Response Format

### Standard Error Response
```json
{
  "success": false,
  "error": {
    "code": "error_code",
    "message": "Human readable error message",
    "details": "Additional context"
  },
  "request_id": "req_12345..."
}
```

### Standard Success Response
```json
{
  "success": true,
  "data": {
    // Response payload
  },
  "request_id": "req_12345..."
}
```

## Missing Endpoints (Identified for Future Implementation)

Based on analysis, these endpoints are commonly needed but not yet implemented:

### File Content Reading
- `GET /repos/:owner/:repo/files/:path` - Read file content at specific ref
- `GET /repos/:owner/:repo/files/:path/lines` - Read specific line ranges

### Enhanced Git Operations
- `GET /repos/:owner/:repo/commits/:sha/diff` - Single commit diff
- `GET /repos/:owner/:repo/files/:path/blame` - Git blame information
- `GET /repos/:owner/:repo/commits/common-ancestor` - Merge-base calculation
- `POST /repos/:owner/:repo/line-mapping` - Comment position mapping

### Directory Browsing
- `GET /repos/:owner/:repo/contents` - GitHub-style directory contents
- `GET /repos/:owner/:repo/contents/:path` - Path-specific contents

## Rate Limiting & Performance

- **Rate Limits**: Applied via middleware (configurable)
- **Request Size Limits**:
  - File operations: 100MB per file
  - Multi-file commits: 500MB total
  - Git operations: No explicit limit (streaming)
- **Timeouts**:
  - Read operations: 30s
  - Write operations: 45s
  - Git protocol: 5m

## Development & Testing

### Local Testing
```bash
# Health check
curl http://localhost:8080/health

# Service info
curl http://localhost:8080/

# Repository state
curl -H "X-USER-ID: test-user" http://localhost:8080/repos/testowner/testrepo/state

# Get commits
curl -H "X-USER-ID: test-user" http://localhost:8080/repos/testowner/testrepo/commits

# Compare branches
curl -H "X-USER-ID: test-user" http://localhost:8080/repos/testowner/testrepo/diff/branches/main/feature-branch
```

### Mock Permission Service
For development, a mock permission service runs on port 8082:
```bash
cd test/mock-permission-service && node server.js
```

---

**Note**: This reference is based on the current codebase. For the most up-to-date information, refer to the source code in `internal/server/server.go`.
