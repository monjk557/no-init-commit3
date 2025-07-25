# CollabOps Git Executor

## 🚀 Current Status & Verification Results

**Last Updated:** July 24, 2025

### ✅ What's Working
- **Build & Compilation:** Full compilation working
- **Unit Tests:** All 8 packages passing (config, git, logging, permissions, protocol, repository, server, streaming)
- **Configuration Loading:** YAML config parsing and validation
- **Path Management & Security:** Full path validation, traversal attack prevention
- **Server Startup:** HTTP server starts successfully on localhost:9091 (config fixed!)
- **HTTP Endpoints:** Health check (`/health`) and API root (`/repos/`) responding
- **Repository Setup:** 3 repositories configured:
  - `📦 CollabOps/collabops-api-service (1.6M)`
  - `📦 bjm/collabops-api-orchestrator (1.2M)`
  - `📦 testowner/testrepo (132K)`
- **Streaming Package:** Complete io.Pipe() implementation for memory-efficient Git operations
- **Protocol Compliance:** Enhanced error handling and Git Smart HTTP protocol compliance system
- **🔥 COMPLETE: Git Push Support (Receive-Pack):**
  - **Reference Advertisement:** Full implementation for git push capability discovery
  - **Packfile Processing:** Complete POST endpoint for git push operations
  - **Reference Updates:** Create, update, delete branches/tags with validation
  - **Concurrent Safety:** Per-repository locking mechanism for safe parallel pushes
  - **Authentication & Authorization:** Write permission enforcement for push operations
  - **Gateway Integration:** Processes push requests forwarded from collabops-git-http gateway
  - **Protocol Compliance:** Complete git-receive-pack protocol with correct content types & status reporting
  - **Push Capabilities:** report-status, delete-refs, ofs-delta, side-band-64k, atomic, push-options
  - **Pkt-Line Parsing:** Full Git protocol command parsing with capability negotiation
- **Task 9.1 ✅:** Git info/refs endpoint implementation
- **Task 9.2 ✅:** Git upload-pack endpoint implementation
- **Task 9.3 ✅:** Streaming response mechanism implementation
- **Task 9.4 ✅:** Authentication and authorization for gateway requests
- **Task 9.5 ✅:** Error handling and Smart HTTP protocol compliance
- **Task 10.1 ✅:** **Git receive-pack reference advertisement** (Push operations foundation!)
- **Task 10.2 ✅:** **Git receive-pack POST endpoint** (Complete push operations with packfile processing!)
- **Task 15 ✅:** Endpoint Access Control Middleware (CRITICAL SECURITY)
- **TEST COVERAGE:** **100% SUCCESS** - Enhanced security logging and protocol compliance test suites

### ⚠️ Known Issues & Missing Features
- **✅ FIXED:** Git Smart HTTP Endpoints now working with absolute paths
- **✅ FIXED:** Configuration updated to use absolute paths
- **✅ CRITICAL SECURITY MILESTONE:** **Endpoint Access Control Middleware IMPLEMENTED!**
  - ✅ Complete X-Source header validation system
  - ✅ Full access control matrix enforcement per PRD
  - ✅ Security foundation ready for all future endpoint implementations
  - ✅ HTTP/SSH gateways restricted to appropriate protocol endpoints
  - ✅ Orchestrator restricted to REST API endpoints only
  - ✅ Comprehensive security logging and audit trail
- **Missing Diff API:** REST API endpoints for diff operations (needed by orchestrator) not yet implemented:
  - `GET /repos/:owner/:repo/diff?from=<ref>&to=<ref>` - Compare branches/commits
  - `GET /repos/:owner/:repo/commits/:sha/diff` - Get commit diff
  - `GET /repos/:owner/:repo/file-diff?path=<path>&from=<ref>&to=<ref>` - File-level diff
- **Missing SSH Protocol Support:** SSH-specific endpoints not yet implemented:
  - `POST /ssh/upload-pack` - SSH fetch/clone operations from SSH gateway
  - `POST /ssh/receive-pack` - SSH push operations from SSH gateway

### 🧪 **MANUAL TESTING & VERIFICATION**

#### **🚀 Quick Start - Server Health Check**
```bash
# Start the server (now working with fixed config!)
go run ./cmd/git-executor/main.go --config=config/test-config.yaml
# Server will start on localhost:9091

# Test server is working
curl http://localhost:9091/health
# Expected: {"status":"ok","message":"Git Executor is running"}

curl http://localhost:9091/
# Expected: {"message":"CollabOps Git Executor","version":"1.0.0","status":"running"}
```

#### **📋 GIT SMART HTTP PROTOCOL TESTING (Push/Pull)**

##### **🔄 FETCH OPERATIONS (Upload-Pack) - ✅ WORKING**
```bash
# Test Git fetch reference advertisement
curl -H "X-USER-ID: testuser123" \
     -H "X-Source: http-gateway" \
     "http://localhost:9091/testowner/testrepo/info/refs?service=git-upload-pack"

# Expected Response:
# - Content-Type: application/x-git-upload-pack-advertisement  
# - Body contains: "# service=git-upload-pack"
# - Git capabilities: multi_ack, thin-pack, side-band-64k, ofs-delta, etc.
```

##### **⬆️ PUSH OPERATIONS (Receive-Pack) - ✅ COMPLETE!**
```bash
# Test Git push reference advertisement
curl -H "X-USER-ID: testuser123" \
     -H "X-Source: http-gateway" \
     "http://localhost:9091/testowner/testrepo/info/refs?service=git-receive-pack"

# Expected Response:
# - Content-Type: application/x-git-receive-pack-advertisement
# - Body contains: "# service=git-receive-pack" 
# - Push capabilities: report-status, delete-refs, ofs-delta, side-band-64k, atomic, push-options

# Test Git push POST endpoint (packfile processing)
curl -X POST \
     -H "X-USER-ID: testuser123" \
     -H "X-Source: http-gateway" \
     -H "Content-Type: application/x-git-receive-pack-request" \
     -d "0074a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2 b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3 refs/heads/main\n0000" \
     "http://localhost:9091/testowner/testrepo/git-receive-pack"

# Expected Response:
# - Content-Type: application/x-git-receive-pack-result
# - Body contains: "unpack ok" and reference status
```

##### **🔐 AUTHENTICATION & AUTHORIZATION TESTING**
```bash
# Test missing authentication (should fail)
curl "http://localhost:9091/testowner/testrepo/info/refs?service=git-receive-pack"
# Expected: 401 Unauthorized

# Test invalid source header (should fail)  
curl -H "X-USER-ID: testuser123" \
     -H "X-Source: invalid-source" \
     "http://localhost:9091/testowner/testrepo/info/refs?service=git-receive-pack"
# Expected: 403 Forbidden

# Test invalid service parameter
curl -H "X-USER-ID: testuser123" \
     -H "X-Source: http-gateway" \
     "http://localhost:9091/testowner/testrepo/info/refs?service=invalid-service"
# Expected: 400 Bad Request
```

##### **🛡️ SECURITY TESTING**
```bash
# Test path traversal prevention
curl -H "X-USER-ID: testuser123" \
     -H "X-Source: http-gateway" \
     "http://localhost:9091/../../../etc/passwd/info/refs?service=git-upload-pack"
# Expected: 400 Bad Request or 403 Forbidden

# Test repository access control
curl -H "X-USER-ID: testuser123" \
     -H "X-Source: http-gateway" \
     "http://localhost:9091/nonexistent/repo/info/refs?service=git-upload-pack"
# Expected: 404 Not Found
```

#### **🔧 UNIT & INTEGRATION TESTING**
```bash
# Run all tests with enhanced coverage
make test-enhanced                 # Security + Protocol compliance tests
make test-security                 # Security logging tests only
make test-compliance              # Protocol compliance tests only

# Component-specific testing
go test ./internal/protocol -v    # Protocol handlers (upload-pack + receive-pack)
go test ./internal/server -v      # Server routing and middleware
go test ./internal/permissions -v # Permission system

# Manual component testing
make test-manual-config        # Test configuration loading
make test-manual-paths         # Test path management & security
make test-manual-smart-http    # Test Git Smart HTTP (pre-made script)
make test-manual-streaming     # Test streaming optimization
make setup-test-repos          # Set up additional test repositories

# NEW: Comprehensive Receive-Pack Testing
make test-manual-receive-pack      # Complete manual test suite for Git push functionality
./scripts/test-receive-pack.sh     # Alternative: Direct script execution
                                   # (Both require server to be running first)
```

#### **📊 VERIFICATION CHECKLIST**

**🚀 Quick Manual Test Setup:**
```bash
# Terminal 1: Start the server
go run ./cmd/git-executor/main.go --config=config/test-config.yaml

# Terminal 2: Run comprehensive tests
make test-manual-receive-pack
```

**✅ Core Functionality:**
- [ ] Server starts without errors on localhost:9091
- [ ] Health endpoint responds correctly
- [ ] Git upload-pack (fetch) working with proper authentication
- [ ] Git receive-pack (push) working with proper authentication  
- [ ] Proper content types returned for each service
- [ ] All required headers validated

**✅ Security & Access Control:**
- [ ] X-USER-ID header required for Git operations
- [ ] X-Source header properly validated 
- [ ] Path traversal attacks blocked
- [ ] Non-existent repositories return 404
- [ ] Permission system integrated

**✅ Protocol Compliance:**
- [ ] Git Smart HTTP protocol headers correct
- [ ] Proper capability advertisement for both upload/receive-pack
- [ ] Error responses follow Git protocol standards
- [ ] Content-Type headers match Git expectations

### 🎯 Next Steps
- **✅ COMPLETED:** Configuration updated with absolute paths - Git Smart HTTP now functional
- **✅ COMPLETED:** **Task 15 (CRITICAL):** Endpoint Access Control Middleware - **SECURITY FOUNDATION ESTABLISHED**
- **NEXT:** **Task 10:** Implement Git Smart HTTP Protocol for Push (git-receive-pack)
- **Task 14 (NEW):** Implement Diff API endpoints for orchestrator integration  
- **Task 16 (NEW):** Implement SSH Protocol endpoints for SSH gateway

---

# CollabOps Git Executor

The `collabops-git-executor` is a backend component in the CollabOps platform responsible for executing actual Git operations. It serves as the **Git Logic Worker** in the CollabOps architecture, receiving requests from the `collabops-git-http` gateway and implementing the core Git Smart HTTP protocol logic.

> 📋 **Architecture Documentation:** See [docs/architecture/component-separation.md](docs/architecture/component-separation.md) for the complete architectural overview and component responsibilities.

## Architecture

```
Git Client (SSH/HTTP)
    ↓
Gateway Layer: collabops-git-ssh / collabops-git-http
    ↓ (REST / RPC)
collabops-git-executor  ← This repository (Git Logic Worker)
    ↓ (go-git)
Filesystem (e.g., /var/lib/git/repos/{user}/{repo}.git)
```

### Component Role

**collabops-git-executor** serves as the **Git Logic Worker** that:
- ✅ Implements Git Smart HTTP protocol (executor side)
- ✅ Handles all go-git operations and .git object processing
- ✅ Performs JWT decoding and permission verification
- ✅ Manages repository caching and streaming
- ✅ Processes packfiles and reference updates
- ❌ Does NOT directly accept Git client requests (gateway handles routing)

For complete architectural details and component separation, see the [Component Separation Documentation](docs/architecture/component-separation.md).

## Features

- **Git Operations**: Tree and blob queries, push/pull operations, commit log retrieval
- **Authentication**: JWT validation via header processing (X-USER-ID)
- **Permission Management**: Integration with API-Service for access control
- **Git Smart HTTP Protocol**: Full support for git fetch/push operations
- **Performance**: Repository caching and memory-efficient streaming
- **Observability**: Comprehensive metrics, logging, and health checks

## Quick Start

### Prerequisites

- Go 1.21+
- Docker (optional)

### Installation

```bash
git clone https://github.com/collabops/git-executor.git
cd git-executor
go mod download

# 🚨 MANDATORY: Set up lint prevention system for ALL developers
make setup-lint-prevention
```

### 🚨 Code Quality Enforcement

This project uses **MANDATORY** lint prevention to ensure code quality. All developers MUST run the setup:

```bash
# Force setup quality control system (REQUIRED for all team members)
make setup-lint-prevention
# or shorter alias:
make enforce-quality
```

**What this does:**
- ✅ Installs golangci-lint automatically
- ✅ Sets up pre-commit hooks that **BLOCK** commits with lint errors
- ✅ Configures IDE settings for real-time lint checking
- ✅ Runs initial code cleanup
- ✅ Tests the entire setup

**After setup, these commands are available:**
```bash
make format      # Fix all formatting, lint issues, AND run tests (use before every commit)
make qa-check    # Check code quality without fixing
make pre-commit  # Test what the commit hook will do
```

⚠️ **WARNING:** After running `setup-lint-prevention`, commits with lint/format issues will be automatically REJECTED by git hooks.

### Configuration

Copy the sample configuration:

```bash
cp config/config.yaml.example config/config.yaml
```

Edit the configuration file to match your environment.

### Running

```bash
go run cmd/git-executor/main.go
```

### Docker

```bash
docker-compose up --build
```

## API Endpoints

### Repository Operations
- `GET /repos/:owner/:repo/tree?ref=:ref` - Get repository tree structure
- `GET /repos/:owner/:repo/blob?path=:path&ref=:ref` - Get blob content
- `GET /repos/:owner/:repo/commits?ref=:ref&limit=:limit&skip=:skip` - Get commit history

### Git Smart HTTP Protocol
- `GET /repos/:owner/:repo/info/refs?service=git-upload-pack` - Reference advertisement for fetch
- `POST /repos/:owner/:repo/git-upload-pack` - Git fetch packfile endpoint
- `GET /repos/:owner/:repo/info/refs?service=git-receive-pack` - Reference advertisement for push
- `POST /repos/:owner/:repo/git-receive-pack` - Git push packfile endpoint

### Health & Monitoring
- `GET /health` - Health check endpoint
- `GET /metrics` - Prometheus metrics

## Development

### Project Structure

```
├── cmd/                    # Application entry points
├── internal/               # Internal packages
│   ├── config/            # Configuration handling
│   ├── server/            # HTTP server implementation
│   ├── git/               # Git repository interface
│   ├── auth/              # Authentication middleware
│   ├── permissions/       # Permission verification
│   └── logging/           # Structured logging
├── pkg/                   # Reusable packages
├── config/                # Configuration files
├── docs/                  # Documentation
└── deployments/           # Docker and deployment configs
```

### Running Tests

```bash
go test ./...
```

### Building

```bash
go build -o bin/git-executor cmd/git-executor/main.go
```

## Configuration

The application uses YAML configuration with the following structure:

```yaml
server:
  port: 8080
  base_path: /repos

git:
  repo_root: /var/lib/git/repos
  enable_packfile: true

jwt:
  algorithm: RS256
  public_key_path: /etc/collabops/public.pem
```

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 🔒 Endpoint Access Control Architecture

The git-executor implements strict endpoint access control to ensure each component can only access its authorized endpoints:

```mermaid
graph TB
    subgraph "Client Layer"
        GitCLI[Git CLI]
        WebUI[Web UI]
    end

    subgraph "Gateway Layer"
        HTTPGateway[git-http<br/>Gateway]
        SSHGateway[git-ssh<br/>Gateway]
        Orchestrator[Orchestrator<br/>FastAPI]
    end

    subgraph "Git Executor"
        subgraph "Access Control"
            Middleware[Source Validation<br/>Middleware]
        end

        subgraph "Git Protocol Endpoints"
            InfoRefs[GET /info/refs]
            UploadPack[POST /upload-pack]
            ReceivePack[POST /receive-pack]
            SSHUpload[POST /ssh/upload-pack]
            SSHReceive[POST /ssh/receive-pack]
        end

        subgraph "REST API Endpoints"
            Tree[GET /repos/.../tree]
            Blob[GET /repos/.../blob]
            Commits[GET /repos/.../commits]
            Diff[GET /repos/.../diff]
            CommitDiff[GET /repos/.../commits/:sha/diff]
            FileDiff[GET /repos/.../file-diff]
        end
    end

    GitCLI -->|HTTPS| HTTPGateway
    GitCLI -->|SSH| SSHGateway
    WebUI --> Orchestrator

    HTTPGateway -->|X-Source: http-gateway| Middleware
    SSHGateway -->|X-Source: ssh-gateway| Middleware
    Orchestrator -->|X-Source: orchestrator| Middleware

    Middleware -->|✅ Allowed| InfoRefs
    Middleware -->|✅ Allowed| UploadPack
    Middleware -->|✅ Allowed| ReceivePack
    Middleware -->|✅ Allowed| SSHUpload
    Middleware -->|✅ Allowed| SSHReceive

    Middleware -->|✅ Allowed| Tree
    Middleware -->|✅ Allowed| Blob
    Middleware -->|✅ Allowed| Commits
    Middleware -->|✅ Allowed| Diff
    Middleware -->|✅ Allowed| CommitDiff
    Middleware -->|✅ Allowed| FileDiff

    style HTTPGateway fill:#e1f5e1
    style SSHGateway fill:#e1f5e1
    style Orchestrator fill:#e1e5f5
    style Middleware fill:#f5e1e1

    classDef gitProtocol fill:#f9f,stroke:#333,stroke-width:2px
    classDef restApi fill:#bbf,stroke:#333,stroke-width:2px

    class InfoRefs,UploadPack,ReceivePack,SSHUpload,SSHReceive gitProtocol
    class Tree,Blob,Commits,Diff,CommitDiff,FileDiff restApi
```

**Key Security Rules:**
- **HTTP/SSH Gateways** → Can ONLY access Git Protocol endpoints (pink)
- **Orchestrator** → Can ONLY access REST API endpoints (blue)
- **All requests** → Must include valid `X-Source` header
- **Unauthorized access** → Returns 403 Forbidden + security log

## Support

For support and questions, please open an issue in the GitHub repository.
