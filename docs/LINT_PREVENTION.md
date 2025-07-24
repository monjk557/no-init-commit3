# Lint Prevention Guide

This document explains how to prevent lint errors in the Go codebase, especially when working with AI assistants like Cursor.

## 🛡️ Protection Layers

### 1. **Real-time Protection (IDE)**
- **VS Code/Cursor Settings**: `.vscode/settings.json` configures real-time linting
- **Cursor AI Rules**: `.cursor/rules/go-lint-prevention.md` guides AI code generation
- **Go Language Server**: Shows errors as you type

### 2. **Pre-commit Protection (Git Hooks)**
- **Git Hook**: `.githooks/pre-commit` runs checks before every commit
- **Automatic Blocking**: Prevents commits with lint errors
- **Fast Feedback**: Catches issues before they reach CI/CD

### 3. **Development Workflow (Makefile)**
- **Quality Checks**: `make qa-check` - comprehensive quality verification
- **Auto-fix**: `make qa-fix` - automatically fix what's possible
- **Pre-commit**: `make pre-commit` - run same checks as git hook
- **Error Help**: `make lint-errors` - detailed error explanations

## 🚀 Quick Setup

```bash
# One-time setup (installs tools, configures hooks, etc.)
./scripts/setup-lint-prevention.sh

# Verify everything works
make qa-check
```

## 🔧 Daily Usage

### Before Starting to Code
```bash
# Check current state
make qa-check
```

### During Development
- VS Code/Cursor shows lint errors in real-time
- Red squiggly lines indicate issues
- Auto-fix on save enabled

### Before Committing
```bash
# Auto-fix what's possible
make qa-fix

# Verify everything is clean
make pre-commit
```

### If Git Hook Fails
```bash
# See detailed error explanations
make lint-errors

# Fix formatting issues
make format

# Check specific issues
make lint
```

## 🤖 AI Assistant Guidelines

### For Cursor/Claude/Other AI

When generating Go code, ALWAYS:

#### 1. Handle Error Returns
```go
// ✅ CORRECT
if err := someFunction(); err != nil {
    return fmt.Errorf("operation failed: %w", err)
}

// ✅ CORRECT - Explicit ignore in tests
_ = client.Close()
_, _ = w.Write([]byte("data"))

// ❌ NEVER
someFunction()  // errcheck violation
```

#### 2. Resource Cleanup
```go
// ✅ CORRECT
defer func() { _ = resource.Close() }()

// ✅ CORRECT - Critical resources
defer func() {
    if err := resource.Close(); err != nil {
        log.Printf("Failed to close: %v", err)
    }
}()
```

#### 3. Test Files Pattern
```go
// ✅ In test files, always use explicit error ignoring
defer func() { _ = os.RemoveAll(tempDir) }()
defer func() { _ = repo.Close() }()
_ = json.NewEncoder(w).Encode(response)
```

#### 4. Context Keys
```go
// ✅ CORRECT
type ContextKey string
const SkipLoggingKey ContextKey = "skip_logging"

// ❌ AVOID
ctx.Value("skip_logging") // Raw string collision risk
```

## 🎯 Common Lint Errors & Fixes

### `errcheck` - Unchecked error returns
```go
// Problem: Error return value ignored
file.Close()

// Fix: Explicitly ignore
_ = file.Close()

// Fix: Handle properly
if err := file.Close(); err != nil {
    log.Printf("Failed to close: %v", err)
}
```

### `staticcheck` - Style violations
```go
// Problem: Unnecessary type declaration
var _ SomeInterface = client

// Fix: Let Go infer the type
var _ = client
```

### `unused` - Unused code
```go
// Problem: Function defined but never used
func unusedFunction() {}

// Fix: Remove unused code or add "//nolint:unused" comment
```

## 📋 Makefile Commands Reference

| Command | Purpose |
|---------|---------|
| `make qa-check` | Run all quality checks |
| `make qa-fix` | Auto-fix and verify |
| `make fmt-check` | Check formatting only |
| `make format` | Auto-format code |
| `make lint` | Run linter |
| `make lint-errors` | Detailed error help |
| `make pre-commit` | Same as git hook |
| `make test-short` | Quick test run |

## 🛠️ Tools Used

- **golangci-lint**: Comprehensive Go linter
- **gofmt**: Code formatting
- **goimports**: Import organization
- **go vet**: Static analysis
- **staticcheck**: Advanced static analysis

## 🔍 Troubleshooting

### "golangci-lint not found"
```bash
# Install golangci-lint
./scripts/setup-lint-prevention.sh
```

### "Tests fail in git hook"
```bash
# Run tests manually to see details
make test-verbose
```

### "Too many lint errors"
```bash
# Auto-fix what's possible first
make qa-fix

# Then tackle remaining issues one by one
make lint-errors
```

### "AI keeps generating bad code"
Make sure Cursor can see the rules:
1. Check `.cursor/rules/go-lint-prevention.md` exists
2. Restart Cursor to reload rules
3. Use specific prompts: "Generate Go code following golangci-lint standards"

## 🎉 Success Metrics

- ✅ `make qa-check` passes
- ✅ `make lint` shows "0 issues"
- ✅ All tests pass
- ✅ Git commits succeed without manual intervention
- ✅ CI/CD pipelines pass on first try
