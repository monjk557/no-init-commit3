# CollabOps Git Executor

## 🚀 Current Status & Verification Results

**Last Updated:** July 24, 2025

### ✅ What's Working
- **Build & Compilation:** Full compilation working
- **Unit Tests:** All 8 packages passing (config, git, logging, permissions, protocol, repository, server, streaming)
- **Configuration Loading:** YAML config parsing and validation
- **Path Management & Security:** Full path validation, traversal attack prevention
- **Server Startup:** HTTP server starts successfully on localhost:9090
- **HTTP Endpoints:** Health check (`/health`) and API root (`/repos/`) responding
- **Repository Setup:** 3 repositories configured:
  - `📦 CollabOps/collabops-api-service (1.6M)`
  - `📦 bjm/collabops-api-orchestrator (1.2M)`
  - `📦 testowner/testrepo (132K)`
- **Streaming Package:** Complete io.Pipe() implementation for memory-efficient Git operations
- **Protocol Compliance:** Enhanced error handling and Git Smart HTTP protocol compliance system
- **Task 9.1 ✅:** Git info/refs endpoint implementation
- **Task 9.2 ✅:** Git upload-pack endpoint implementation
- **Task 9.3 ✅:** Streaming response mechanism implementation
- **Task 9.4 ✅:** Authentication and authorization for gateway requests
- **Task 9.5 ✅:** Error handling and Smart HTTP protocol compliance

### ⚠️ Known Issues
- **Git Smart HTTP Endpoints:** Path configuration requires absolute paths (relative paths cause handler initialization failure)
- **Configuration:** Default config uses relative path `test-repos` instead of absolute path

### 🧪 Testing Available
```bash
# Manual component testing
make test-manual-config        # Test configuration loading
make test-manual-paths         # Test path management & security
make test-manual-smart-http    # Test Git Smart HTTP (requires absolute paths fix)
make test-manual-streaming     # Test streaming optimization
make setup-test-repos          # Set up additional test repositories

# Unit testing
go test ./internal/... -short  # All unit tests
```

### 🎯 Next Steps
- **Task 9.5:** Implement error handling and Smart HTTP protocol compliance for executor (next)
- **Fix:** Update config to use absolute paths for full Git Smart HTTP functionality
- **Task 10:** Implement Git Smart HTTP Protocol for Push (git-receive-pack)

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
make format      # Fix all formatting and lint issues (use before every commit)
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

## Support

For support and questions, please open an issue in the GitHub repository.
