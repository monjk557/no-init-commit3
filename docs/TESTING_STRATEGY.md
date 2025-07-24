# Testing Strategy for CollabOps Git Executor

## 🎯 Testing Philosophy

**"Fix something? Write a test first!"** - TDD 원칙을 프로젝트에 적용

### Core Principles
1. **Test-Driven Development (TDD)**: 코드 수정 전에 항상 테스트 작성
2. **Behavior-Driven Development (BDD)**: 사용자 관점에서 동작 정의
3. **Regression Prevention**: 기존 버그 재발 방지를 위한 테스트
4. **Contract Testing**: API/인터페이스 계약 검증

## 📊 Current Test Coverage Status

| Package | Coverage | Status | Priority |
|---------|----------|---------|----------|
| permissions | 94.9% | ✅ Excellent | Maintain |
| git | 91.5% | ✅ Excellent | Maintain |
| server | 88.6% | ✅ Good | Minor improvements |
| repository | 83.3% | ✅ Good | Minor improvements |
| config | 70.6% | ⚠️ Needs work | Medium priority |
| protocol | 39.7% | ❌ Poor | High priority |
| streaming | 13.9% | ❌ Critical | Critical priority |
| logging | 18.4% | ❌ Critical | Critical priority |

## 🚀 Testing Workflow

### 1. Before Fixing Any Bug
```bash
# 1. Write a failing test that reproduces the bug
go test ./path/to/package -run TestBugName -v

# 2. Ensure it fails (Red)
# 3. Fix the bug (Green)
# 4. Refactor if needed (Refactor)
# 5. Ensure all tests pass
make format  # Includes linting + testing
```

### 2. Before Adding New Features
```bash
# 1. Write tests for expected behavior first
# 2. Watch them fail
# 3. Implement the feature
# 4. Watch tests pass
```

### 3. After Any Code Change
```bash
# Always run comprehensive validation
make format
```

## 🧪 Test Categories & Guidelines

### Unit Tests
- **Purpose**: Test individual functions/methods in isolation
- **Naming**: `TestFunctionName_Scenario`
- **Location**: Same package as code (`*_test.go`)
- **Coverage Goal**: 80%+ for all packages

### Integration Tests
- **Purpose**: Test component interactions
- **Naming**: `TestIntegration_Feature`
- **Focus**: Real Git operations, HTTP endpoints, database interactions
- **Examples**: Git protocol compliance, streaming efficiency

### Contract Tests
- **Purpose**: Verify API contracts and interfaces
- **Naming**: `TestContract_Interface`
- **Focus**: Input/output validation, error responses

### Regression Tests
- **Purpose**: Prevent previous bugs from returning
- **Naming**: `TestRegression_IssueNumber` or `TestRegression_BugDescription`
- **Required**: For every bug fix

## 🎨 Test Patterns & Best Practices

### Table-Driven Tests
```go
func TestValidation(t *testing.T) {
    tests := []struct {
        name        string
        input       string
        expected    bool
        expectError bool
    }{
        {"valid input", "test", true, false},
        {"invalid input", "", false, true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result, err := Validate(tt.input)
            if tt.expectError && err == nil {
                t.Errorf("expected error, got none")
            }
            if result != tt.expected {
                t.Errorf("expected %v, got %v", tt.expected, result)
            }
        })
    }
}
```

### Mock/Stub Usage
- Use `testify/mock` for complex dependencies
- Create test doubles for external services
- Mock time, HTTP clients, file system when needed

### Context Testing
```go
func TestWithContext(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()

    // Test that function respects context cancellation
}
```

## 📈 Priority Test Implementation Plan

### Phase 1: Critical Coverage (Immediate)

#### 1. Streaming Package Tests (`internal/streaming`)
**Current: 13.9% → Target: 80%**

Create comprehensive tests for:
```go
// Test streaming performance and memory efficiency
func TestStreaming_MemoryUsage(t *testing.T)
func TestStreaming_LargeDataHandling(t *testing.T)
func TestStreaming_ConcurrentStreams(t *testing.T)
func TestStreaming_ErrorHandling(t *testing.T)
```

#### 2. Logging Package Tests (`internal/logging`)
**Current: 18.4% → Target: 80%**

Focus on:
```go
// Test all logging levels and outputs
func TestLogger_AllLevels(t *testing.T)
func TestLogger_FileRotation(t *testing.T)
func TestLogger_StructuredLogging(t *testing.T)
func TestLogger_ErrorRecovery(t *testing.T)
```

### Phase 2: Protocol Enhancement (High Priority)

#### Protocol Package Tests (`internal/protocol`)
**Current: 39.7% → Target: 85%**

Git protocol compliance tests:
```go
func TestGitProtocol_UploadPackCompliance(t *testing.T)
func TestGitProtocol_ReceivePackCompliance(t *testing.T)
func TestGitProtocol_ErrorHandling(t *testing.T)
func TestGitProtocol_SecurityValidation(t *testing.T)
```

### Phase 3: Configuration Hardening (Medium Priority)

#### Config Package Tests (`internal/config`)
**Current: 70.6% → Target: 90%**

Edge cases and validation:
```go
func TestConfig_EnvironmentOverrides(t *testing.T)
func TestConfig_ValidationEdgeCases(t *testing.T)
func TestConfig_DefaultValues(t *testing.T)
func TestConfig_InvalidFormats(t *testing.T)
```

## 🔧 Testing Tools & Automation

### Enhanced Make Targets
```makefile
test-unit:          # Unit tests only
test-integration:   # Integration tests only
test-regression:    # Regression tests only
test-coverage-detailed:  # Detailed coverage analysis
test-benchmark:     # Performance benchmarks
test-security:      # Security-focused tests
```

### Test Data Management
- Use `testdata/` directories for test fixtures
- Create helper functions for common test setup
- Use temporary directories for file operations

### CI/CD Integration
- Fail builds on coverage decrease
- Require tests for all new code
- Run different test suites in parallel

## 🛡️ Regression Prevention Strategy

### Mandatory Test Requirements
1. **Every Bug Fix** → Write regression test
2. **Every New Feature** → Write comprehensive tests
3. **Every Refactor** → Maintain/improve test coverage
4. **Every Performance Fix** → Add benchmark tests

### Git Hooks Enhancement
Update `.githooks/pre-commit` to:
- Run affected tests based on changed files
- Check coverage doesn't decrease
- Validate new tests are added for changes

### Documentation Requirements
- Document test scenarios in code comments
- Maintain test plan documentation
- Update testing strategy as project evolves

## 📝 Test Writing Guidelines

### Test Naming Convention
```
Test[Function/Feature]_[Scenario]_[ExpectedResult]

Examples:
- TestValidateRepoPath_ValidPath_ReturnsNoError
- TestUploadPack_InvalidRequest_ReturnsHTTP400
- TestStreaming_LargeFile_CompletesWithinTimeout
```

### Test Structure (AAA Pattern)
```go
func TestExample(t *testing.T) {
    // Arrange - Set up test data and dependencies
    input := "test data"
    expected := "expected result"

    // Act - Execute the function under test
    result, err := FunctionUnderTest(input)

    // Assert - Verify the results
    require.NoError(t, err)
    assert.Equal(t, expected, result)
}
```

### Error Testing
```go
func TestFunction_ErrorCases(t *testing.T) {
    // Test all error paths
    // Verify error messages are helpful
    // Check error types are correct
}
```

## 🎯 Success Metrics

### Coverage Goals
- **Overall Project**: 85%+
- **Critical Packages** (git, protocol, repository): 90%+
- **Security-Related Code**: 95%+

### Quality Metrics
- Zero failing tests in main branch
- All new code covered by tests
- Regression tests for all bug fixes
- Performance benchmarks for critical paths

### Process Metrics
- Tests written before code changes: 100%
- Code review includes test review
- CI/CD pipeline runs all test suites
- Test execution time < 2 minutes for full suite
