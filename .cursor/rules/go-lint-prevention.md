# Go Lint Prevention and Code Quality Rules

This rule guides AI assistants to generate lint-free, test-passing Go code that works both locally and in CI/CD pipelines.

## 🚨 CRITICAL: Pre-Generation Checklist

**BEFORE writing ANY Go code, verify:**
1. ✅ All struct fields in tests match the actual struct definition
2. ✅ All required config fields have default values in `setDefaults()`
3. ✅ Error returns are handled (use `_ =` if intentionally ignoring)
4. ✅ No empty strings for required fields (especially paths in router configurations)
5. ✅ Test configurations are complete and valid

## 🎯 Configuration Management

### Required Config Defaults
**ALWAYS check `internal/config/config.go` setDefaults() when using configs:**
```go
// ❌ BAD: Missing required field in test
cfg := &config.Config{
    Server: config.ServerConfig{
        Host: "localhost",
        Port: 0,
        // Missing HealthCheckPath!
    },
}

// ✅ GOOD: All required fields included
cfg := &config.Config{
    Server: config.ServerConfig{
        Host:            "localhost",
        Port:            0,
        BasePath:        "/api/repos",
        HealthCheckPath: "/health",  // REQUIRED for chi router
        ReadTimeout:     30 * time.Second,
        WriteTimeout:    30 * time.Second,
    },
}
```

### Router Path Requirements
**Chi router REQUIRES all paths to start with '/':**
```go
// ❌ WILL PANIC: empty string
router.Get(s.config.Server.HealthCheckPath, handler) // panic if HealthCheckPath is ""

// ✅ GOOD: Always validate or provide defaults
if s.config.Server.HealthCheckPath == "" {
    s.config.Server.HealthCheckPath = "/health"
}
router.Get(s.config.Server.HealthCheckPath, handler)
```

## 🧪 Test Completeness

### Always Match Struct Definitions
```go
// If ServerConfig has these fields:
type ServerConfig struct {
    Host            string
    Port            int
    BasePath        string
    HealthCheckPath string  // <-- Don't forget this!
    ReadTimeout     time.Duration
    WriteTimeout    time.Duration
}

// Then tests MUST include ALL required fields
```

### Cross-Platform Test Compatibility
```go
// ❌ BAD: Hardcoded branch names
mainTree, err := repo.GetTree(ctx, "main") // Fails on older Git

// ✅ GOOD: Dynamic branch detection
defaultBranch := getCurrentBranch(t, repoPath)
defaultTree, err := repo.GetTree(ctx, defaultBranch)
```

## 🔧 Error Handling Patterns

### Explicit Error Ignoring
```go
// ❌ BAD: Unhandled error
defer resp.Body.Close()
defer gitRepo.Close()

// ✅ GOOD: Explicitly ignore if intentional
defer func() { _ = resp.Body.Close() }()
defer func() { _ = gitRepo.Close() }()
```

### Test Cleanup
```go
// ✅ ALWAYS in tests:
tempDir, err := os.MkdirTemp("", "test-")
require.NoError(t, err)
defer func() { _ = os.RemoveAll(tempDir) }()  // Always ignore cleanup errors
```

## 📦 Import Organization

```go
import (
    // Standard library
    "context"
    "fmt"
    "net/http"

    // External packages with explicit aliases when needed
    chi "github.com/go-chi/chi/v5"
    lumberjack "gopkg.in/natefinch/lumberjack.v2"

    // Internal packages
    "github.com/collabops/git-executor/internal/config"
)
```

## 🚀 CI/CD Awareness

### GitHub Actions Configuration
**NEVER change repository-specific settings without verification:**
```yaml
# ❌ BAD: Changing to generic names
REPOSITORY: web  # DON'T change to generic names

# ✅ GOOD: Keep project-specific names
REPOSITORY: git-executor  # Specific to this project
```

### Environment-Specific Code
```go
// ✅ Handle CI vs local differences
if os.Getenv("CI") == "true" {
    // CI-specific behavior
} else {
    // Local development behavior
}
```

## 🛡️ Defensive Programming

### Validate Before Use
```go
// ❌ BAD: Assume config is valid
router.Mount(s.config.Server.BasePath, routes)

// ✅ GOOD: Validate first
if s.config.Server.BasePath == "" {
    s.config.Server.BasePath = "/"
}
router.Mount(s.config.Server.BasePath, routes)
```

### Struct Initialization
```go
// ❌ BAD: Partial initialization
cfg := &config.Config{
    Server: config.ServerConfig{
        Port: 8080,
    },
}

// ✅ GOOD: Complete initialization or use defaults
cfg := &config.Config{}
config.SetDefaults(cfg)  // If such method exists
cfg.Server.Port = 8080   // Then override specifics
```

## 🔍 Common Pitfalls to Avoid

1. **Missing Config Fields in Tests**
   - Always check the struct definition
   - Include ALL required fields, not just the ones you're testing

2. **Empty String Router Paths**
   - Chi router panics on empty paths
   - Always provide defaults or validate

3. **Hardcoded Git References**
   - Don't assume "main" branch exists
   - Use dynamic detection or HEAD

4. **Unhandled Errors in Defers**
   - Always use `defer func() { _ = ... }()` pattern
   - Especially for Close() operations

5. **CI/CD Config Changes**
   - Don't change project-specific values to generic ones
   - Verify with the team before changing deployment configs

## 📝 Quick Reference Commands

```bash
# 🎯 MAIN COMMAND: Does EVERYTHING (lint fixes + formatting)
make format

# 🎯 Alternative comprehensive fix (same as format)
make fix

# 🔍 Check for issues without fixing
make lint

# 🏥 Fix what's possible, then check remaining issues
make qa-fix

# Check specific files
golangci-lint run ./internal/...
```

## 🤖 AI Assistant Guidelines

**BEFORE generating code:**
1. Check struct definitions for ALL required fields
2. Verify config defaults exist in setDefaults()
3. Ensure router paths are never empty strings
4. Plan error handling strategy (handle or explicitly ignore)

**AFTER generating code:**
1. Would `make lint` pass?
2. Are all config fields properly initialized?
3. Are all errors handled or explicitly ignored?
4. Would this work in both local and CI environments?

**When modifying existing code:**
1. Don't change project-specific configurations without verification
2. Maintain consistency with existing patterns
3. Ensure changes work across different environments
4. Test with `make test-race` before committing

## Updated: `make format` Now Includes Lint Fixes! ✅

**GOOD NEWS**: `make format` now handles **BOTH formatting AND linting**!

- ✅ **`make format` NOW fixes**:
  - Auto-fixable lint issues (errcheck, staticcheck, unused)
  - Code formatting (gofmt, goimports)
  - Module cleanup (go mod tidy, go mod verify)
- 🎯 **`make fix`**: Now just an alias for `make format`
- 🔍 **`make lint`**: Check for issues without fixing

**Perfect! Now `make format` is truly comprehensive as expected!**
