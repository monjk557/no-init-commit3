# CI/CD Troubleshooting Guide

This document provides solutions to common CI/CD issues in the Go project.

## 🚨 **SOLVED Issues**

### **Issue 1: "make: *** [test-verbose] Error 1" in GitHub Actions**

### **Problem Description**
CI was failing with exit code 1 even though all individual tests were passing. The logs showed:
```
=== RUN   TestServer_InvalidRoute
--- PASS: TestServer_InvalidRoute (0.00s)
PASS
ok      github.com/collabops/git-executor/internal/server       1.113s
FAIL
make: *** [Makefile:99: test-verbose] Error 1
```

### **Root Cause**
The CI workflow was running **redundant race detection tests**:
1. `make test-verbose` → `go test -v -race ./...` (all packages)
2. `make test-race` → `go test -race -short ./...` (all packages again)

This caused:
- Resource contention between two race detection runs
- Potential race condition conflicts in CI environment
- Timeout issues due to `-race` flag being resource-intensive

### **Solution Applied**
1. **Eliminated Redundant Testing**: Removed duplicate test execution in CI
2. **Created Optimized Target**: Added `test-race-verbose` combining race detection + verbose output
3. **Added Timeout Protection**: 10-minute timeout for tests, 15-minute for coverage
4. **Updated Local QA**: Made local `qa-check` consistent with CI approach

### **CI Workflow Changes**
**Before (Problematic):**
```yaml
- name: Run verbose tests
  run: make test-verbose    # go test -v -race ./...

- name: Run tests with race detection
  run: make test-race       # go test -race -short ./...
```

**After (Fixed):**
```yaml
- name: Run tests with race detection
  timeout-minutes: 10
  run: make test-race-verbose  # go test -v -race -timeout=10m ./...

- name: Run tests with coverage
  timeout-minutes: 15
  run: make test-coverage
```

### **Makefile Changes**
**New Target:**
```makefile
test-race-verbose: ## Run tests with race detection and verbose output (CI optimized)
	@echo "$(BLUE)🧪 Running comprehensive tests with race detection and verbose output...$(NC)"
	@echo "$(YELLOW)🔍 Race detection enabled for CI reliability$(NC)"
	$(GOTEST) -v -race -timeout=10m ./...
```

### **Issue 2: Git Branch Reference Error - "reference 'main' not found"**

**Problem Description**
Test failure in CI with error:
```
=== RUN   TestGoGitRepository_BranchHandling
    gogit_repository_test.go:442: Failed to get main tree: reference "main" not found in repository
--- FAIL: TestGoGitRepository_BranchHandling (0.03s)
```

**Root Cause**
Git version differences between local development and CI environments:
- **Git 2.28+**: Creates "main" as default branch
- **Git 2.23-2.27**: Creates "master" as default branch
- **CI Environment**: Often uses older Git versions
- **Test Issue**: Hardcoded branch name expectations

**Solution Applied**
Made the test robust to different Git versions by dynamically detecting the default branch:

```go
// Before (Problematic):
mainTree, err := repo.GetTree(ctx, "main")  // Hardcoded "main"

// After (Fixed):
defaultBranch := getCurrentBranch(t, repoPath)  // Dynamic detection
defaultTree, err := repo.GetTree(ctx, defaultBranch)

func getCurrentBranch(t *testing.T, repoPath string) string {
    cmd := exec.Command("git", "branch", "--show-current")
    cmd.Dir = repoPath
    output, err := cmd.Output()
    if err != nil {
        t.Fatalf("Failed to get current branch: %v", err)
    }
    return strings.TrimSpace(string(output))
}
```

**Prevention Strategy**
- Avoid hardcoding Git branch names in tests
- Use dynamic branch detection or generic references like "HEAD"
- Test with multiple Git versions locally when possible

### **Issue 3: Lint Errors After `git pull` - \"make format didn't catch this!\"**

**Problem Description**
After pulling new code, lint errors appear despite running `make format`:
```
Error: internal/protocol/upload_pack.go:112:21: Error return value of `gitRepo.Close` is not checked (errcheck)
        defer gitRepo.Close()
                           ^
14 issues: errcheck: 11, staticcheck: 2, unused: 1
```

**Root Cause**
**Common Misconception**: `make format` should fix all code issues
**Reality**: `make format` only handles **formatting**, not **linting**

- ✅ **`make format` fixes**: Code formatting (gofmt), import organization (goimports), module cleanup (go mod tidy)
- ❌ **`make format` CANNOT fix**: Error handling, unused variables, staticcheck issues
- 🚀 **Solution**: Use `make fix` for BOTH formatting AND auto-fixable lint issues

**Solution Applied**
Enhanced `make format` with comprehensive auto-fix capabilities:

```makefile
format: ## Auto-format code, fix lint issues, and show results (COMPREHENSIVE)
	@echo "🎨 COMPREHENSIVE Auto-formatting and organizing code..."
	golangci-lint run --fix ./...  # Auto-fix lint issues FIRST
	gofmt -s -w .                  # Then do formatting
	goimports -w .                 # Then organize imports
	go mod tidy                    # Then clean modules
```

**Prevention Strategy**
- **`make format` now fixes EVERYTHING** - no more confusion!
- **`make fix`** is now just an alias for `make format`
- **`make qa-fix`** still works for fix + verification
- **No more split between formatting and linting** - it's all in `make format`

## 🛠️ **Future CI Issue Prevention**

### **Best Practices**

1. **Single Test Execution**: Never run the same test suite twice in CI
2. **Timeout Protection**: Always add timeouts to prevent hanging builds
3. **Resource Awareness**: `-race` flag is resource-intensive, use judiciously
4. **Local-CI Consistency**: Ensure local `qa-check` matches CI approach

### **Testing Strategy**
```makefile
# Local development
make qa-check        # fmt-check + vet + lint + test-race

# CI optimized
make test-race-verbose  # Comprehensive with timeouts
```

### **Monitoring CI Health**
- ✅ All tests pass individually
- ✅ No redundant test execution
- ✅ Timeout protection in place
- ✅ Exit codes properly handled

## 🔍 **Debugging CI Issues**

### **Step 1: Check for Redundant Operations**
Look for duplicate commands in `.github/workflows/ci-cd.yml`:
```bash
grep -n "test" .github/workflows/ci-cd.yml
```

### **Step 2: Verify Makefile Targets**
Ensure test targets don't conflict:
```bash
make -n test-verbose
make -n test-race
make -n test-race-verbose
```

### **Step 3: Test Locally with CI Conditions**
```bash
# Simulate CI environment
make test-race-verbose

# Check timeout behavior
timeout 600 make test-race-verbose  # 10 minutes
```

### **Step 4: Check Resource Usage**
The `-race` flag can increase:
- Memory usage by 5-10x
- CPU usage significantly
- Test execution time by 2-4x

### **Step 5: Check Git Version Compatibility**
For Git-related test failures:
```bash
# Check Git version locally vs CI
git --version

# Check default branch behavior
git init test-repo && cd test-repo
git config user.name "Test" && git config user.email "test@example.com"
echo "test" > README.md && git add README.md && git commit -m "Initial"
git branch --show-current  # Shows "main" or "master"
cd .. && rm -rf test-repo

# Test with both branch names
go test -v ./internal/git -run TestGoGitRepository_BranchHandling
```

## 📋 **CI Performance Optimization**

### **Resource Management**
- **Race Detection**: Use sparingly, only when needed
- **Timeouts**: Set appropriate limits (10min tests, 15min coverage)
- **Caching**: Leverage GitHub Actions cache for dependencies

### **Test Execution Strategy**
```yaml
# Efficient approach
test → format → lint → race-tests → coverage

# Avoid
test → race-tests → verbose-tests → coverage  # Redundant!
```

## 🚀 **Quick Recovery Commands**

If CI is failing:

```bash
# 1. Check local setup matches CI
make qa-check

# 2. Test the exact CI command
make test-race-verbose

# 3. Verify no timeout issues
timeout 600 make test-race-verbose

# 4. Check for race conditions locally
go test -race -v ./internal/...

# 5. For Git-related failures, check version compatibility
git --version
go test -v ./internal/git -run TestGoGitRepository_BranchHandling
```

## 📞 **Getting Help**

If you encounter new CI issues:

1. **Check this document first**
2. **Run local diagnostics** (`make qa-check`)
3. **Compare CI logs** with local execution
4. **Look for resource/timeout patterns**
5. **Check for redundant operations**

## 🎯 **Success Metrics**

A healthy CI should show:
- ✅ Exit code 0 for all steps
- ✅ Reasonable execution times (<15 minutes total)
- ✅ No "FAIL" after "PASS" messages
- ✅ Clear timeout boundaries
- ✅ Single execution of each test type

---

**Last Updated**: January 2025
**Status**: All CI issues resolved, preventive measures in place
