# Go Lint Prevention Rules

## Error Handling Requirements

### CRITICAL: Always Handle Error Returns
```go
// ✅ CORRECT - Handle errors properly
if err := someFunction(); err != nil {
    return fmt.Errorf("operation failed: %w", err)
}

// ✅ CORRECT - Explicitly ignore errors when appropriate
_ = file.Close()                    // Single return value
_, _ = w.Write([]byte("data"))      // Multiple return values

// ❌ NEVER DO - Ignore error returns without explicit assignment
someFunction()     // errcheck violation
file.Close()       // errcheck violation
```

### Test Files - Use Explicit Ignore Pattern
```go
// ✅ In test files, always use explicit error ignoring:
defer func() { _ = repo.Close() }()
defer func() { _ = os.RemoveAll(tempDir) }()
_ = json.NewEncoder(w).Encode(response)
_, _ = w.Write([]byte("test data"))
```

### Variable Declarations
```go
// ✅ CORRECT - Avoid unnecessary type declarations
var _ = client              // Instead of: var _ SomeInterface = client

// ✅ CORRECT - Use explicit type only when needed
var config *Config         // When nil initialization is important
```

### Import Organization
```go
// ✅ Always use explicit import aliases for clarity
import (
    chi "github.com/go-chi/chi/v5"
    lumberjack "gopkg.in/natefinch/lumberjack.v2"
)
```

### Context Keys
```go
// ✅ Use typed constants for context keys to avoid collisions
type ContextKey string
const SkipLoggingKey ContextKey = "skip_logging"

// ❌ NEVER use raw strings as context keys
ctx.Value("skip_logging") // Potential collision
```

### Resource Cleanup
```go
// ✅ ALWAYS use defer with explicit error handling
defer func() { _ = resource.Close() }()

// ✅ For critical cleanup, handle errors appropriately
defer func() {
    if err := criticalResource.Close(); err != nil {
        log.Printf("Failed to close critical resource: %v", err)
    }
}()
```

### Mandatory Checks Before Code Generation
1. **Check if function returns error** - If yes, handle or explicitly ignore
2. **Check if in test file** - If yes, use `_ =` pattern for non-critical errors
3. **Check for resource management** - Always defer cleanup with error handling
4. **Avoid raw interface assertions** - Use explicit variable declarations

### Pre-Generation Checklist
- [ ] All error returns are handled or explicitly ignored with `_ =`
- [ ] All `defer` statements use proper cleanup patterns
- [ ] No raw string context keys
- [ ] Import aliases are explicit when needed
- [ ] No unnecessary type declarations (use `var _ =` instead of `var _ Type =`)

## Quick Reference Commands
```bash
# 🚀 BEST: Auto-fix formatting AND lint issues (COMPREHENSIVE)
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

## Updated: `make format` Now Includes Lint Fixes! ✅

**GOOD NEWS**: `make format` now handles **BOTH formatting AND linting**!

- ✅ **`make format` NOW fixes**:
  - Auto-fixable lint issues (errcheck, staticcheck, unused)
  - Code formatting (gofmt, goimports)
  - Module cleanup (go mod tidy, go mod verify)
- 🎯 **`make fix`**: Now just an alias for `make format`
- 🔍 **`make lint`**: Check for issues without fixing

**Perfect! Now `make format` is truly comprehensive as expected!**
