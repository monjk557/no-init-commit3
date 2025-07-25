# CollabOps Git Infrastructure Summary

This document provides a high-level comparison of the three core components in the CollabOps Git infrastructure.

## Component Overview

| Component | Purpose | Tech Stack | Port | Primary Role |
|-----------|---------|------------|------|--------------|
| **collabops-git-executor** | Git operations worker | Go + go-git | 8080 | Execute Git commands, manage repos |
| **collabops-git-http** | HTTP gateway | Go + net/http + JWT | 8080/443 | Handle Git over HTTPS |
| **collabops-git-ssh** | SSH gateway | Go + SSH server | 22/2222 | Handle Git over SSH |

## Architecture Flow

```
[Git Clients]
     |
     ├─── HTTPS ──> [collabops-git-http] ──┐
     |                                      |
     └─── SSH ────> [collabops-git-ssh] ───┤
                                            |
                                            v
                                [collabops-git-executor]
                                            |
                                            v
                                    [Git Repositories]
                                 /var/lib/git/repos/
```

## Authentication Methods

| Component | Auth Method | User Identification |
|-----------|-------------|---------------------|
| **git-http** | JWT Bearer Token | Extract `sub` claim → X-USER-ID |
| **git-ssh** | SSH Public Key | Key fingerprint → Redis/DB → X-USER-ID |
| **git-executor** | X-USER-ID header | Trusts upstream gateways |

## Supported Git Operations

All gateways support the same core Git operations, just over different protocols:

- **Clone/Fetch**: `git-upload-pack`
- **Push**: `git-receive-pack`

## Technology Stack Comparison

### Common Technologies (All Components)
- **Language**: Go 1.21+
- **Configuration**: Viper (YAML)
- **Logging**: Structured (logrus/slog)
- **Metrics**: Prometheus
- **Testing**: Standard Go testing + testify
- **HTTP Client**: Standard library with pooling

### Component-Specific Technologies

**collabops-git-executor**:
- **Git Library**: go-git
- **Web Framework**: Gin
- **Caching**: sync.Map or groupcache

**collabops-git-http**:
- **JWT Library**: golang-jwt/jwt/v5
- **Web Framework**: net/http or Gin/Echo
- **Rate Limiting**: Built-in or middleware

**collabops-git-ssh**:
- **SSH Server**: gliderlabs/ssh or x/crypto/ssh
- **Key Storage**: Redis or PostgreSQL
- **Audit**: File-based audit logs

## Security Model

### Network Security
- **git-http**: TLS termination, JWT validation
- **git-ssh**: SSH crypto, key validation
- **git-executor**: Internal only, trusts gateways

### Access Control Headers
All gateways must add:
- `X-USER-ID`: Authenticated user identifier
- `X-Source`: Component identifier (http-gateway/ssh-gateway)
- `X-Request-ID`: Trace identifier

### Endpoint Access Matrix
Per the executor PRD, each component has specific access rights:
- **Gateways**: Only Git protocol endpoints
- **Orchestrator**: Only REST API endpoints
- **No cross-access allowed**

## Deployment Architecture

```yaml
# Kubernetes Services
collabops-git-http:
  - Type: ClusterIP (internal)
  - Ingress: For external HTTPS
  - Replicas: 3-10 (horizontal scaling)

collabops-git-ssh:
  - Type: LoadBalancer/NodePort
  - Direct SSH access
  - Replicas: 3-10 (session affinity)

collabops-git-executor:
  - Type: ClusterIP (internal only)
  - No external access
  - Replicas: 5-20 (based on load)
```

## Performance Targets

| Component | Concurrent Connections | Memory | CPU | Special Considerations |
|-----------|------------------------|--------|-----|------------------------|
| **git-http** | 100+ | <100MB | Low | Streaming large packfiles |
| **git-ssh** | 1000+ | <200MB | Moderate | SSH crypto overhead |
| **git-executor** | 500+ | <500MB | High | Git operations, caching |

## Key Design Principles

1. **Separation of Concerns**
   - Gateways handle protocol/auth only
   - Executor handles all Git logic
   - No direct repo access from gateways

2. **Stateless Gateways**
   - No session state
   - Horizontal scaling friendly
   - Fast failover capability

3. **Security First**
   - Authentication at gateway level
   - Authorization at executor level
   - Audit trails throughout

4. **Performance Optimization**
   - Connection pooling
   - Streaming support
   - Caching where appropriate

## Future Roadmap Alignment

All components are designed to support:
- Git LFS (Large File Storage)
- Git Protocol v2
- Advanced caching strategies
- Multi-region deployments
- Enhanced monitoring and observability
