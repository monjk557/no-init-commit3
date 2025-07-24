# CollabOps Git Executor - Testing Guide

This guide covers all available testing commands and workflows for the CollabOps Git Executor project.

## 🚀 Quick Start

### 1. Initial Setup (First Time Only)
```bash
# Download dependencies and create go.sum
make setup
```

### 2. Run Basic Tests
```bash
# Run all tests
make test

# Run configuration package tests only
make test-config
```

## 🧪 Available Test Commands

### Tests (Require Go)
| Command | Description | Output |
|---------|-------------|---------|
| `make test` | Run all unit tests | Verbose test output |
| `make test-config` | Run config package tests only | Focused config testing |
| `make test-short` | Run tests with `-short` flag | Quick test run |
| `make test-race` | Run with race detection | Race condition testing |
| `make test-verbose` | Run with verbose + race | Detailed test output |
| `make test-coverage` | Generate coverage report | `coverage.html` file |

### Verification (No Go Required)
| Command | Description | Output |
|---------|-------------|---------|
| `make verify-config` | Verify config implementation | 42 verification checks |
| `make verify-structure` | Verify project structure | Directory/file validation |
| `make verify-all` | Run all verification scripts | Complete project validation |
| `make quick-test` | All verifications (no Go) | Fast validation suite |

### Code Quality (Require Go)
| Command | Description | Output |
|---------|-------------|---------|
| `make fmt` | Format Go code | Code formatting |
| `make vet` | Run go vet | Static analysis |
| `make lint` | Run golangci-lint | Comprehensive linting |

### Build Commands (Require Go)
| Command | Description | Output |
|---------|-------------|---------|
| `make build` | Build binary | `bin/git-executor` |
| `make build-linux` | Build for Linux | `bin/git-executor_unix` |
| `make clean` | Clean build files | Removes `bin/` directory |

### Development Workflows
| Command | Description | What It Does |
|---------|-------------|--------------|
| `make dev` | Quick dev workflow | `fmt` + `vet` + `test-short` |
| `make full-test` | Complete test suite | All tests + coverage + lint |
| `make production-build` | Production pipeline | Full build with all checks |

## 📊 Expected Test Results

### Configuration Package Tests
When you run `make test-config`, you should see:

```
🧪 Running configuration package tests...
go test -v ./internal/config/...
=== RUN   TestLoad_ValidConfig
--- PASS: TestLoad_ValidConfig (0.00s)
=== RUN   TestLoad_Defaults
--- PASS: TestLoad_Defaults (0.00s)
=== RUN   TestLoad_InvalidYAML
--- PASS: TestLoad_InvalidYAML (0.00s)
=== RUN   TestValidate_InvalidServerPort
--- PASS: TestValidate_InvalidServerPort (0.00s)
=== RUN   TestValidate_InvalidBasePath
--- PASS: TestValidate_InvalidBasePath (0.00s)
=== RUN   TestValidate_JWTAlgorithms
--- PASS: TestValidate_JWTAlgorithms (0.00s)
=== RUN   TestValidate_LogConfiguration
--- PASS: TestValidate_LogConfiguration (0.00s)
=== RUN   TestValidate_RequiredFields
--- PASS: TestValidate_RequiredFields (0.00s)
=== RUN   TestLoad_EnvironmentVariables
--- PASS: TestLoad_EnvironmentVariables (0.00s)
=== RUN   TestGetConfigPath
--- PASS: TestGetConfigPath (0.00s)
PASS
ok  	github.com/collabops/git-executor/internal/config	0.005s
```

### Coverage Report
When you run `make test-coverage`:

```
🧪 Running tests with coverage...
go test -v -coverprofile=coverage.out ./...
go tool cover -html=coverage.out -o coverage.html
📊 Coverage report generated: coverage.html
```

This creates an HTML coverage report you can open in your browser.

### Verification Scripts
When you run `make verify-all`:

```
✅ All verifications completed!
```

With detailed checks for:
- Project structure (7 directories)
- Essential files (3 files with content)
- Configuration package (42 verification checks)
- Go module dependencies (6 key packages)

## 🔧 Troubleshooting

### Missing Dependencies Error
```
missing go.sum entry for module providing package github.com/spf13/viper
```
**Solution:** Run `make setup` to download dependencies.

### Go Not Found
```
go: command not found
```
**Solution:** Install Go 1.21+ or use verification commands that don't require Go:
- `make verify-all`
- `make quick-test`
- `make status`

### Tests Failing
1. **Check Go version:** `go version` (should be 1.21+)
2. **Update dependencies:** `make deps-tidy`
3. **Clean and rebuild:** `make clean && make setup`

## 📋 Test Coverage Goals

Current implementation covers:
- **Configuration Loading:** ✅ YAML, environment variables, defaults
- **Validation Logic:** ✅ All config sections with proper error handling
- **Error Cases:** ✅ Invalid YAML, missing fields, wrong types
- **Environment Override:** ✅ `COLLABOPS_*` prefix support
- **JWT Algorithms:** ✅ RS256, HS256, ES256 validation

## 🎯 Next Steps

After running tests successfully:
1. **Task 1.3:** Create HTTP Server with Graceful Shutdown
2. **Task 1.4:** Docker and Docker Compose Setup
3. **Task 1.5:** Logging and Error Handling

## 📚 Additional Resources

- **Makefile Help:** `make help` - Shows all available commands
- **Project Status:** `make status` - Current project overview
- **Go Environment:** `make go-env` - Show Go configuration (if installed) 