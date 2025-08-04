# CollabOps Local Integration Testing

This directory contains Docker Compose configurations for local integration testing of the complete CollabOps stack, including the new **Git Executor** service.

## Architecture Overview

```
┌─────────────────┐    ┌─────────────────────┐    ┌──────────────────┐
│   Web Frontend  │───▶│  API Orchestrator   │───▶│   API Service    │
│    (Next.js)    │    │     (FastAPI)       │    │    (Django)      │
│   Port: 3000    │    │    Port: 2345       │    │   Port: 8000     │
└─────────────────┘    └─────────────────────┘    └──────────────────┘
                                │
                                ▼
                       ┌─────────────────────┐
                       │   Git Executor      │
                       │     (Go HTTP)       │
                       │    Port: 9090       │
                       │                     │
                       │  ⭐ SIMPLIFIED:     │
                       │  Only Orchestrator  │
                       │  talks to this      │
                       │  service            │
                       └─────────────────────┘
```

### 🎯 **Key Architectural Decision**

**Single Entry Point**: Only the **Orchestrator** communicates with the **Git Executor**. All metadata (user permissions, repository access, etc.) is passed as parameters from the orchestrator. This eliminates the need for the Git Executor to directly communicate with the API Service.

## Services

### Core Application Services

1. **collabops-web** (Port 3000)
   - Next.js frontend application
   - Hot reloading enabled for development
   - Connects to orchestrator at port 2345

2. **collabops-api-orchestrator** (Port 2345)
   - FastAPI orchestrator service
   - Coordinates between frontend, API service, and Git executor
   - Handles business logic and service coordination

3. **collabops-api-service** (Port 8000)
   - Django REST API service
   - Core business logic and data management
   - Database and user management

4. **collabops-git-executor** (Port 9090) ⭐ **NEW**
   - Go-based Git operations service
   - Handles git init, diff, clone, push, pull operations
   - Smart HTTP protocol support
   - Repository management and streaming

### Supporting Services

5. **api-service-db** (Port 5432)
   - PostgreSQL database for API service
   - Contains main application data

6. **orchestrator-db** (Port 5433)
   - PostgreSQL database for orchestrator
   - Separate database for orchestrator state

7. **api-service-redis** (Port 6380)
   - Redis cache for API service
   - Session and cache storage

8. **orchestrator-redis** (Port 6379)
   - Redis cache for orchestrator
   - Service coordination and caching

## Quick Start

1. **Prerequisites**
   ```bash
   # Ensure you have Docker and Docker Compose installed
   docker --version
   docker-compose --version
   ```

2. **Environment Setup**
   ```bash
   # Create environment file for local integration testing
   cat > .env.local-it << 'EOF'
# CollabOps Local Integration Testing Environment Configuration

# Service Ports Configuration
WEB_PORT=3000
ORCHESTRATOR_PORT=2345
API_SERVICE_PORT=8000
GIT_EXECUTOR_PORT=9090

# Database Ports
DB_PORT=5432
ORCHESTRATOR_DB_PORT=5433

# Redis Ports
REDIS_ORCHESTRATOR_PORT=6379
REDIS_API_SERVICE_PORT=6380

# Application Environment
APP_ENV=local
APP_DEBUG=true
APP_LOG_LEVEL=DEBUG

# Database Configuration
DB_NAME=collabops
DB_USER=postgres
DB_PASSWORD=postgres
DB_CONN_MAX_AGE=0
DB_SSLMODE=disable

# API Keys and Secrets
DRF_API_KEY=api_service_key_local_it
SECRET_KEY=dev-secret-key-for-local-integration-testing
GIT_EXECUTOR_JWT_SECRET=git_executor_jwt_secret_local

# Git Executor Configuration
GIT_EXECUTOR_LOG_LEVEL=info

# Superuser Configuration
DJANGO_SUPERUSER_USERNAME=admin
DJANGO_SUPERUSER_EMAIL=admin@collabops.ai
DJANGO_SUPERUSER_PASSWORD=admin123
DJANGO_SUPERUSER_FIRST_NAME=Admin
DJANGO_SUPERUSER_LAST_NAME=User

# Feature Flags
ENABLE_METRICS=true
ENABLE_JWT_MIDDLEWARE=false
USE_SQLITE=0
USE_DUMMY_CACHE=0
CELERY_EAGER=1

# Migration and Deployment
MIGRATION_STRATEGY=auto

# Logging Configuration
GUNICORN_LOG_LEVEL=info
GUNICORN_WORKERS=2

# Network Configuration
HOST=0.0.0.0

# JWT Configuration
ACCESS_TOKEN_EXPIRE_MINUTES=30
EOF
   
   # Edit the environment file if needed
   nano .env.local-it
   ```

3. **Start Services**
   ```bash
   # Start all services
   docker-compose -f local-it.yml --env-file .env.local-it up --build

   # Or start in background
   docker-compose -f local-it.yml --env-file .env.local-it up -d --build
   ```

4. **Verify Services**
   ```bash
   # Check all services are running
   docker-compose -f local-it.yml ps

   # Test endpoints
   curl http://localhost:3000/api/health          # Web Frontend
   curl http://localhost:2345/health              # Orchestrator
   curl http://localhost:8000/health/             # API Service
   curl http://localhost:9090/health              # Git Executor ⭐
   ```

## Git Executor Integration

### Service Communication

The **Git Executor** follows a **simplified integration pattern**:

1. **Orchestrator → Git Executor** (ONLY communication path)
   - URL: `http://collabops-git-executor:9090`
   - Base Path: `/repos`
   - Authentication: JWT tokens
   - **All metadata passed as parameters**: User permissions, repository access rights, etc.

2. **API Service**: **NO direct communication** with Git Executor
   - Orchestrator handles all API Service interactions
   - Orchestrator fetches metadata and passes to Git Executor
   - Clean separation of concerns

### Available Git Operations

The Git Executor provides REST endpoints for:

- `GET /repos/{owner}/{repo}/info/refs` - Git Smart HTTP info/refs
- `POST /repos/{owner}/{repo}/git-upload-pack` - Git fetch operations
- `GET /repos/{owner}/{repo}/status` - Repository status
- `POST /repos/{owner}/{repo}/init` - Initialize repository
- `GET /repos/{owner}/{repo}/diff` - Get repository diffs

### Environment Variables

Key environment variables for Git Executor:

```bash
# Service Configuration
GIT_EXECUTOR_PORT=9090
GIT_EXECUTOR_LOG_LEVEL=info

# Authentication (JWT only - no API service integration needed)
GIT_EXECUTOR_JWT_SECRET=git_executor_jwt_secret_local

# Integration URLs (in orchestrator only)
GIT_EXECUTOR_URL=http://collabops-git-executor:9090
```

## Development Workflow

### Local Development

1. **Code Changes**
   - Web: Changes to source files trigger hot reload
   - Git Executor: Rebuild container for Go changes
   - API Service: Pull latest image or rebuild if modified

2. **Testing Git Operations**
   ```bash
   # Test Git Executor directly (health check only)
   curl http://localhost:9090/health

   # Test through orchestrator (all Git operations go through here)
   curl http://localhost:2345/git/repositories
   
   # Git operations with metadata passed from orchestrator
   curl -X POST http://localhost:2345/git/repositories/owner/repo/init \
        -H "Content-Type: application/json" \
        -d '{"user_id": "123", "permissions": ["read", "write"]}'
   ```

3. **Logs and Debugging**
   ```bash
   # Follow logs for all services
   docker-compose -f local-it.yml logs -f

   # Follow logs for specific service
   docker-compose -f local-it.yml logs -f collabops-git-executor
   ```

### Stopping Services

```bash
# Stop all services
docker-compose -f local-it.yml down

# Stop and remove volumes (caution: destroys data)
docker-compose -f local-it.yml down -v
```

## Configuration

### Git Executor Configuration

The Git Executor service can be configured via environment variables:

- **Server Settings**: Host, port, timeouts, base path
- **Git Settings**: Repository root, cache settings, packfile options
- **Security**: JWT algorithm, secret keys, CORS settings
- **Integration**: API service URL, timeout settings
- **Logging**: Level, format, output destination

### Volume Mounts

- `git_executor_repos:/repos` - Git repositories storage
- `git_executor_logs:/var/log/collabops` - Service logs
- Configuration files mounted from local filesystem

### Networking

All services communicate through the `collabops-network` bridge network:
- Internal DNS resolution (e.g., `collabops-git-executor:9090`)
- External access via localhost ports
- CORS configured for cross-service communication

## Troubleshooting

### Common Issues

1. **Port Conflicts**
   ```bash
   # Check if ports are in use
   netstat -tulpn | grep :9090
   
   # Change ports in .env.local-it file
   GIT_EXECUTOR_PORT=9091
   ```

2. **Service Not Ready**
   ```bash
   # Check service health
   docker-compose -f local-it.yml ps
   
   # View service logs
   docker-compose -f local-it.yml logs collabops-git-executor
   ```

3. **Git Executor Build Issues**
   ```bash
   # Rebuild Git Executor
   docker-compose -f local-it.yml build --no-cache collabops-git-executor
   ```

### Health Checks

All services include health checks:
- **Web**: `GET /api/health`
- **Orchestrator**: `GET /health`
- **API Service**: `GET /health/`
- **Git Executor**: `GET /health`
- **Databases**: `pg_isready` checks
- **Redis**: `redis-cli ping`

## Production Deployment

For production deployment:

1. **Use Registry Images**
   ```yaml
   collabops-git-executor:
     image: asia-northeast3-docker.pkg.dev/collabops-shared/git-executor/collabops-git-executor:latest
     # Remove build context
   ```

2. **Security Hardening**
   - Enable read-only filesystems
   - Use proper secrets management
   - Configure TLS/SSL
   - Set up proper authentication

3. **Resource Limits**
   ```yaml
   deploy:
     resources:
       limits:
         memory: 512M
         cpus: '0.5'
   ```

## API Documentation

- **Web Frontend**: `http://localhost:3000`
- **Orchestrator API**: `http://localhost:2345/docs`
- **API Service**: `http://localhost:8000/api/docs/`
- **Git Executor**: `http://localhost:9090/health` (health endpoint)

---

**Note**: This setup is optimized for local development and integration testing. For production use, ensure proper security configurations, secrets management, and monitoring. 