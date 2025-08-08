# Integration Testing Framework

This directory contains comprehensive integration tests for the CollabOps Git Executor server-side Git REST APIs.

## Overview

The integration testing framework provides:

- **Full API Coverage**: Tests for all implemented REST endpoints
- **Real Repository Testing**: Uses actual Git repositories, not mocks
- **Error Scenario Testing**: Comprehensive error handling validation
- **Performance Testing**: Load testing and performance benchmarks
- **End-to-End Workflows**: Complete user journey testing

## Test Structure

### Test Organization

```
test/integration/
├── git_api_integration_test.go    # Main integration test suite
├── README.md                      # This documentation
└── helpers/                       # Test helper utilities (future)
```

### Test Categories

1. **File Operations** (`TestFileOperations_Integration`)
   - Single file create, update, delete operations
   - Base64 content encoding validation
   - Commit creation and verification

2. **Multi-file Operations** (`TestMultiFileCommit_Integration`)
   - Atomic multi-file commits
   - Transaction rollback testing
   - Batch operation validation

3. **Branch Operations** (`TestBranchOperations_Integration`)
   - Branch creation, update, deletion
   - Fast-forward and force update testing
   - Protected branch handling

4. **Repository State** (`TestRepositoryState_Integration`)
   - Repository metadata queries
   - HEAD information retrieval
   - Reference listing and validation

5. **Merge Operations** (`TestMergeOperations_Integration`)
   - Git merge execution
   - Conflict detection and handling
   - Fast-forward vs three-way merge testing

6. **Merge Preview** (`TestMergePreview_Integration`)
   - Non-destructive merge analysis
   - Conflict preview and recommendations
   - Performance metrics validation

7. **Error Handling** (`TestErrorHandling_Integration`)
   - Authentication and authorization errors
   - Invalid input validation
   - Resource not found scenarios
   - Network and timeout handling

8. **Performance Testing** (`TestPerformance_Integration`)
   - Concurrent request handling
   - Large file operations
   - Response time validation
   - Resource usage monitoring

9. **API Compliance** (`TestAPICompliance_Integration`)
   - Response format validation
   - Header consistency checking
   - Error response structure

## Running Tests

### Prerequisites

1. **Go Environment**: Ensure Go 1.19+ is installed
2. **Git**: Git must be available in PATH
3. **Permissions**: Write access to temp directories

### Basic Test Execution

```bash
# Run all integration tests
go test ./test/integration -v

# Run specific test category
go test ./test/integration -v -run TestFileOperations

# Run with coverage
go test ./test/integration -v -coverprofile=integration_coverage.out
go tool cover -html=integration_coverage.out -o integration_coverage.html

# Run performance tests (longer duration)
go test ./test/integration -v -run TestPerformance

# Skip performance tests for faster runs
go test ./test/integration -v -short
```

### Environment Configuration

Tests use the following default configuration:

```go
// Test repository root (temporary directory)
tempDir := "/tmp/git-executor-integration-test-*"

// Test repository structure
testRepoPath := "tempDir/testowner/testrepo.git"

// Server configuration
server := httptest.NewServer(...)

// Mock permissions (configurable)
readAccess := true
writeAccess := true
```

### Customizing Test Environment

```go
// Override test repository location
suite := SetupIntegrationTest(t)
suite.tempDir = "/custom/test/path"

// Configure permission scenarios
suite.permClient.readAccess = false  // Test unauthorized scenarios
suite.permClient.writeAccess = true  // Test read-only scenarios
```

## Test Data and Fixtures

### Repository Setup

Each test creates a fresh Git repository with:

- **Initial Commit**: README.md with basic content
- **Bare Repository**: Standard Git bare repository structure
- **Default Branch**: `main` branch with initial commit
- **Clean State**: No uncommitted changes or conflicts

### Test Content

Tests use base64-encoded content for files:

```go
// "Hello World" -> "SGVsbG8gV29ybGQ="
// "console.log('Hello World');" -> "Y29uc29sZS5sb2coJ0hlbGxvIFdvcmxkJyk7"
// "{ \"name\": \"test-project\" }" -> "eyAibmFtZSI6ICJ0ZXN0LXByb2plY3QiIH0="
```

### Standard Test Headers

All requests include required authentication headers:

```go
headers := map[string]string{
    "X-USER-ID": "testuser",
    "X-Source":  "orchestrator",
    "Content-Type": "application/json",
}
```

## Assertion Patterns

### Standard Response Validation

```go
// Status code check
assert.Equal(t, http.StatusOK, resp.StatusCode)

// Response structure validation
var response map[string]interface{}
err = json.NewDecoder(resp.Body).Decode(&response)
require.NoError(t, err)

assert.Equal(t, "success", response["status"])
assert.Contains(t, response, "data")
assert.Contains(t, response, "meta")
```

### API-Specific Assertions

```go
// File operations
assert.Contains(t, response["data"], "commit_sha")
assert.Contains(t, response["data"], "file_path")
assert.Equal(t, "created", response["data"]["operation"])

// Branch operations
assert.Equal(t, "feature/test", response["data"]["name"])
assert.Contains(t, response["data"], "commit")

// Merge operations
assert.Contains(t, response["data"], "merge_commit_sha")
assert.Contains(t, response["data"], "status")
```

### Error Response Validation

```go
// Error structure
assert.Equal(t, "error", response["status"])
assert.Contains(t, response, "error")

errorInfo := response["error"].(map[string]interface{})
assert.Contains(t, errorInfo, "code")
assert.Contains(t, errorInfo, "message")
```

## Performance Testing

### Concurrent Operations

```go
concurrency := 10
requests := 50

// Test concurrent file operations
for i := 0; i < concurrency; i++ {
    go func(workerID int) {
        for j := 0; j < requests/concurrency; j++ {
            // Perform file operation
            resp, err := suite.makeRequest("PUT", path, body)
            // Validate response
        }
    }(i)
}
```

### Load Testing Metrics

- **Response Time**: < 5 seconds for individual operations
- **Throughput**: Handle multiple concurrent requests
- **Memory Usage**: Monitor resource consumption
- **Error Rate**: < 10% errors under load

### Large File Testing

```go
// Test with 1MB file
largeContent := strings.Repeat("A", 1024*1024)
largeContentB64 := base64.StdEncoding.EncodeToString([]byte(largeContent))

// Performance expectations
assert.True(t, duration < 30*time.Second)
assert.Equal(t, http.StatusOK, resp.StatusCode)
```

## Benchmarking

### File Operation Benchmarks

```bash
# Run benchmarks
go test ./test/integration -bench=BenchmarkFileOperations -benchtime=30s

# Example output:
# BenchmarkFileOperations-8     1000    1545 ns/op    312 B/op    7 allocs/op
```

### Repository State Benchmarks

```bash
go test ./test/integration -bench=BenchmarkRepositoryState -benchtime=30s
```

## Troubleshooting

### Common Issues

1. **Permission Denied**
   ```bash
   # Ensure write access to temp directories
   chmod 755 /tmp
   export TMPDIR=/path/to/writable/dir
   ```

2. **Git Not Found**
   ```bash
   # Ensure Git is in PATH
   which git
   export PATH=$PATH:/usr/local/bin
   ```

3. **Port Conflicts**
   ```bash
   # Tests use random ports via httptest.NewServer()
   # No manual port configuration needed
   ```

4. **Test Isolation**
   ```bash
   # Each test creates isolated temp directories
   # Cleanup is automatic via t.Cleanup()
   ```

### Debug Mode

```go
// Enable verbose logging in tests
suite.logger.SetLevel("debug")

// Print request/response details
fmt.Printf("Request: %+v\n", req)
fmt.Printf("Response: %+v\n", resp)
```

### Memory Profiling

```bash
# Run with memory profiling
go test ./test/integration -memprofile=mem.prof -v

# Analyze memory usage
go tool pprof mem.prof
```

## Continuous Integration

### GitHub Actions Integration

```yaml
name: Integration Tests
on: [push, pull_request]

jobs:
  integration:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v3
        with:
          go-version: '1.19'
      - name: Run Integration Tests
        run: |
          go test ./test/integration -v -coverprofile=coverage.out
          go tool cover -html=coverage.out -o coverage.html
      - uses: actions/upload-artifact@v3
        with:
          name: coverage-report
          path: coverage.html
```

### Test Reporting

```bash
# Generate test report in JUnit format
go test ./test/integration -v 2>&1 | go-junit-report > integration-tests.xml

# Generate coverage report
go test ./test/integration -coverprofile=coverage.out
go tool cover -func=coverage.out | tail -1
```

## Best Practices

### Test Design

1. **Isolation**: Each test is completely independent
2. **Cleanup**: Automatic resource cleanup via `t.Cleanup()`
3. **Deterministic**: Tests produce consistent results
4. **Fast**: Individual tests complete within seconds
5. **Comprehensive**: Cover both happy path and error scenarios

### Assertions

1. **Specific**: Test exact expected values, not just presence
2. **Meaningful**: Error messages clearly indicate failures
3. **Complete**: Validate entire response structure
4. **Defensive**: Handle edge cases and boundary conditions

### Performance

1. **Realistic**: Use realistic data sizes and scenarios
2. **Measurable**: Include quantitative performance assertions
3. **Scalable**: Tests work with varying load levels
4. **Monitored**: Track performance trends over time

## Future Enhancements

### Planned Improvements

1. **Helper Libraries**: Common test utilities and fixtures
2. **Data Generators**: Random test data generation
3. **Parallel Execution**: Safe concurrent test execution
4. **Visual Reports**: HTML test result dashboards
5. **Regression Detection**: Automated performance regression detection

### Integration Opportunities

1. **API Documentation**: Auto-generate examples from tests
2. **Client Libraries**: Validate client SDK compatibility
3. **Load Testing**: Extended load and stress testing
4. **Security Testing**: Automated security vulnerability scanning

## Support

For issues with integration tests:

1. **Documentation**: Check this README and inline comments
2. **Debugging**: Use `-v` flag for verbose output
3. **Profiling**: Use Go's built-in profiling tools
4. **Issues**: Report bugs via GitHub issues

## Contributing

When adding new integration tests:

1. **Follow Patterns**: Use existing test structure and assertions
2. **Add Documentation**: Update this README for new test categories
3. **Include Examples**: Provide clear request/response examples
4. **Validate Coverage**: Ensure new functionality is tested
5. **Performance**: Include performance validations for new endpoints
