# CollabOps Git Executor Architecture

## Simplified Integration Pattern

This document explains the clean, simplified architecture where the **Orchestrator** is the single entry point for all Git operations.

## Request Flow

```
1. Frontend Request
   ↓
2. Orchestrator (FastAPI)
   ├─ Authenticates user
   ├─ Fetches permissions from API Service
   ├─ Validates repository access
   ├─ Prepares metadata
   └─ Makes request to Git Executor
   ↓
3. Git Executor (Go)
   ├─ Receives request with all metadata
   ├─ Performs Git operation
   └─ Returns result
   ↓
4. Orchestrator
   ├─ Processes Git Executor response
   ├─ Updates API Service if needed
   └─ Returns to Frontend
```

## Benefits of This Pattern

### 🎯 **Single Responsibility**
- **Git Executor**: Pure Git operations, no business logic
- **Orchestrator**: Business logic, authentication, authorization
- **API Service**: Data persistence, user management

### 🔒 **Security**
- Git Executor doesn't need API Service credentials
- All authorization handled in one place (Orchestrator)
- Reduced attack surface

### 🧹 **Clean Dependencies**
- No circular dependencies
- Clear data flow
- Easier to test and debug

### 📈 **Scalability**
- Git Executor can be scaled independently
- No database connections from Git service
- Stateless Git operations

## Example API Calls

### Repository Initialization
```bash
# Frontend → Orchestrator
POST /api/v1/repositories/owner/repo/init
Authorization: Bearer <user_jwt>

# Orchestrator → Git Executor
POST /repos/owner/repo/init
Content-Type: application/json
{
  "user_id": "user123",
  "permissions": ["read", "write"],
  "metadata": {
    "created_by": "user123",
    "created_at": "2024-01-01T00:00:00Z"
  }
}
```

### Git Diff Operation
```bash
# Frontend → Orchestrator
GET /api/v1/repositories/owner/repo/diff?from=abc123&to=def456
Authorization: Bearer <user_jwt>

# Orchestrator → Git Executor
GET /repos/owner/repo/diff?from=abc123&to=def456
X-User-ID: user123
X-Permissions: read,write
X-Repository-Access: granted
```

### Git Clone/Fetch (Smart HTTP)
```bash
# Frontend → Orchestrator
GET /api/v1/repositories/owner/repo/info/refs?service=git-upload-pack
Authorization: Bearer <user_jwt>

# Orchestrator → Git Executor
GET /repos/owner/repo/info/refs?service=git-upload-pack
X-User-ID: user123
X-Repository-Access: granted
X-Operation: fetch
```

## Environment Configuration

### Orchestrator Environment
```bash
# Git Executor Integration
GIT_EXECUTOR_URL=http://collabops-git-executor:9090
GIT_EXECUTOR_BASE_PATH=/repos
GIT_EXECUTOR_TIMEOUT=60
GIT_EXECUTOR_JWT_SECRET=shared_jwt_secret

# API Service Integration (for metadata)
DRF_BASE_URL=http://collabops-api-service:8000
DRF_API_KEY=api_service_key
```

### Git Executor Environment
```bash
# Server Configuration
COLLABOPS_SERVER_PORT=9090
COLLABOPS_SERVER_BASE_PATH=/repos

# JWT Configuration (shared with orchestrator)
COLLABOPS_JWT_SECRET_KEY=shared_jwt_secret

# No API Service configuration needed!
```

## Security Model

### JWT Token Flow
1. **User Authentication**: Frontend authenticates with Orchestrator
2. **Service-to-Service**: Orchestrator signs JWT for Git Executor
3. **Operation Authorization**: Metadata passed in JWT claims or headers

### Permission Model
```json
{
  "user_id": "user123",
  "repository": "owner/repo",
  "permissions": ["read", "write", "admin"],
  "expires_at": "2024-01-01T01:00:00Z",
  "operation": "git-upload-pack"
}
```

## Error Handling

### Git Executor Errors
```json
{
  "error": "repository_not_found",
  "message": "Repository owner/repo does not exist",
  "code": 404
}
```

### Orchestrator Error Translation
```json
{
  "error": "REPOSITORY_NOT_FOUND",
  "message": "The requested repository could not be found",
  "details": {
    "repository": "owner/repo",
    "user_id": "user123"
  },
  "timestamp": "2024-01-01T00:00:00Z"
}
```

## Monitoring and Logging

### Git Executor Logs
```json
{
  "timestamp": "2024-01-01T00:00:00Z",
  "level": "info",
  "operation": "git-upload-pack",
  "repository": "owner/repo",
  "user_id": "user123",
  "duration_ms": 150,
  "status": "success"
}
```

### Orchestrator Logs
```json
{
  "timestamp": "2024-01-01T00:00:00Z",
  "level": "info",
  "endpoint": "/api/v1/repositories/owner/repo/diff",
  "user_id": "user123",
  "git_executor_duration_ms": 150,
  "total_duration_ms": 200,
  "status": "success"
}
```

## Development Workflow

### Local Testing
```bash
# 1. Start all services
docker-compose -f local-it.yml up

# 2. Test orchestrator → git-executor flow
curl -X POST http://localhost:2345/git/repositories/test/repo/init \
     -H "Authorization: Bearer <orchestrator_jwt>" \
     -H "Content-Type: application/json" \
     -d '{"user_id": "test_user", "permissions": ["read", "write"]}'

# 3. Check git-executor directly (health only)
curl http://localhost:9090/health
```

### Adding New Git Operations

1. **Add endpoint to Git Executor** (Go service)
2. **Add orchestrator integration** (FastAPI service)
3. **Handle authentication/authorization** in orchestrator
4. **Pass metadata as parameters** to Git Executor

## Migration from Direct Integration

If you had direct API Service → Git Executor communication before:

### Before (Complex)
```
API Service → Git Executor (with database queries)
Orchestrator → Git Executor (separate auth)
Frontend → Multiple services
```

### After (Simple)
```
Frontend → Orchestrator → Git Executor (with metadata)
                    ↓
               API Service (for data only)
```

This pattern is much cleaner and follows microservices best practices! 