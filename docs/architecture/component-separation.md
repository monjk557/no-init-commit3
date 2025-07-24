# CollabOps Git System Architecture

**Official Architecture Documentation**  
*Last Updated: 2025-01-24*  
*Source: Architecture Team Clarification*

## Overview

The CollabOps Git system consists of two main components with clear separation of responsibilities:

1. **collabops-git-http** - HTTP Gateway (stateless)
2. **collabops-git-executor** - Git Logic Worker (this repository)

## Component Breakdown

### 1. collabops-git-http (HTTP Gateway)

**Primary Role:** Stateless HTTP request router and JWT handler

**Responsibilities:**
- ✅ Accept Git Smart HTTP requests over HTTP (clone, push, fetch)
- ✅ Route requests to collabops-git-executor
- ✅ Extract and optionally decode JWT (`Authorization: Bearer <JWT>`)
- ✅ Forward headers like `X-USER-ID` to executor
- ✅ Maintain stateless gateway layer
- ❌ **Does NOT** handle Git logic or touch .git files

**Supported Endpoints:**
```
GET  /info/refs?service=git-upload-pack
GET  /info/refs?service=git-receive-pack
POST /git-upload-pack
POST /git-receive-pack
```

**Technology Stack:**
- HTTP routing and forwarding
- JWT handling (optional)
- Request/response forwarding

---

### 2. collabops-git-executor (Git Logic Worker)

**Primary Role:** Core Git operations and Smart HTTP protocol implementation

**Responsibilities:**
- ✅ Core Git logic using go-git
- ✅ Smart HTTP protocol implementation:
  - git-upload-pack (clone/fetch)
  - git-receive-pack (push)
- ✅ JWT decoding (primary responsibility)
- ✅ Permission checking via API metadata service
- ✅ Read/write Git objects from/to bare .git repositories
- ✅ Stream Git responses (packfiles, refs)

**Smart HTTP Protocol Behaviors:**
- Ref negotiation
- Capability declaration
- Packfile parsing and generation
- Reference updates
- Object validation

**JWT Handling Strategy:**
- **Primary:** Decode JWT using K8s Secret (public key)
- **Secondary:** If gateway decodes, use forwarded `X-USER-ID`
- **Responsibility:** Validate access and execute Git operations

**Technology Stack:**
- go-git library
- HTTP API endpoints
- Repository caching
- Permission verification client

## Request Flow Diagrams

### Git Clone/Fetch Flow
```
1. Git Client → GET /info/refs?service=git-upload-pack
   ↓
2. Gateway → Forward to Executor
   ↓
3. Executor → Return refs + capabilities
   ↓
4. Gateway → Forward response to Client
   ↓
5. Git Client → POST /git-upload-pack (want/have)
   ↓
6. Gateway → Forward to Executor
   ↓
7. Executor → Generate + stream packfile
   ↓
8. Gateway → Forward packfile to Client
```

### Git Push Flow
```
1. Git Client → GET /info/refs?service=git-receive-pack
   ↓
2. Gateway → Forward to Executor
   ↓
3. Executor → Return refs + capabilities
   ↓
4. Gateway → Forward response to Client
   ↓
5. Git Client → POST /git-receive-pack (packfile)
   ↓
6. Gateway → Forward to Executor
   ↓
7. Executor → Parse packfile + update refs
   ↓
8. Gateway → Forward results to Client
```

## Component Responsibility Matrix

| Responsibility             | Gateway (git-http) | Executor (git-executor) |
|----------------------------|--------------------|-------------------------|
| Accept Git HTTP Requests  | ✅                 | ❌                      |
| Smart HTTP Logic           | ❌                 | ✅                      |
| JWT Decoding               | 🚫 (optional)      | ✅ (primary)            |
| Repository Access Check    | ❌                 | ✅                      |
| .git Object Handling       | ❌                 | ✅                      |
| Stateless Component        | ✅                 | ❌ (stateful)           |
| Packfile Processing        | ❌                 | ✅                      |
| Reference Updates          | ❌                 | ✅                      |
| Repository Caching         | ❌                 | ✅                      |
| Permission Verification    | ❌                 | ✅                      |

## Authentication Flow

### Path A: Orchestrator Flow (Preferred)
```
1. Client logs in → JWT from Orchestrator
2. Web app → Gateway with JWT
3. Gateway → Executor with X-USER-ID header
4. Executor → API-Service for permission check
```

### Path B: Direct Gateway Flow (Fallback)
```
1. Git client → Gateway with JWT
2. Gateway decodes JWT → X-USER-ID header
3. Gateway → Executor with X-USER-ID
4. Executor → API-Service for permission check
```

### Path C: Direct Executor Flow (Debug Only)
```
1. Git client → Executor with JWT
2. Executor decodes JWT directly
3. Executor → API-Service for permission check

⚠️ NOT RECOMMENDED for production
```

## Configuration Guidelines

### Gateway Configuration
- Focus on routing and forwarding
- JWT handling (optional)
- Rate limiting
- Request/response headers

### Executor Configuration
- Git repository root path
- Repository caching settings
- Permission service configuration
- JWT public key configuration
- Smart HTTP protocol settings

## Development Guidelines

### Gateway Development
- Implement stateless request forwarding
- Handle HTTP routing efficiently
- Maintain minimal JWT logic
- Focus on reliability and performance

### Executor Development  
- Implement complete Git Smart HTTP protocol
- Handle all go-git operations
- Implement comprehensive caching
- Focus on Git protocol compliance

## Security Considerations

1. **JWT Handling:** Primary responsibility is in executor
2. **Permission Checking:** Always done in executor
3. **Repository Access:** Controlled by executor
4. **Network Security:** Gateway provides HTTP layer protection
5. **Git Security:** Executor handles Git-specific security

## Testing Strategy

### Gateway Testing
- HTTP request routing
- JWT forwarding
- Header management
- Error handling

### Executor Testing
- Git Smart HTTP protocol compliance
- Repository operations
- Permission integration
- Caching functionality
- Performance benchmarks

## Future Considerations

### Scalability
- Multiple executor instances
- Distributed repository caching
- Load balancing strategies

### Architecture Evolution
- gRPC communication between components
- Event-driven updates
- Microservice decomposition

### Performance
- Repository caching optimization
- Streaming improvements
- Protocol v2 support

---

## Discussion Points for Architecture Team

1. **JWT Decoding Responsibility:** Should we enforce that only executor decodes JWT?
2. **Repository Metadata:** Should gateway serve repository metadata or always 404?
3. **Service Decomposition:** Should we split upload-pack and receive-pack into separate services?
4. **Error Handling:** How should gateway handle executor errors?
5. **Monitoring:** What telemetry should each component provide?

---

*This document represents the official architecture as clarified by the Architecture Team. All development should follow these guidelines to ensure consistency and maintainability.* 