# Competitive Performance Benchmarking

## Quick Start

Run competitive benchmarks against GitLab CE, Gitea, and GitHub:

```bash
# Run full benchmark suite
./scripts/run-competitive-benchmark.sh

# Or run individual steps
./scripts/run-competitive-benchmark.sh generate-repos
./scripts/run-competitive-benchmark.sh start-services
./scripts/run-competitive-benchmark.sh run-benchmarks
./scripts/run-competitive-benchmark.sh results
```

## What Gets Measured

### **Directory Listing Performance**
- Root directory with 1K files
- Deep directory with 10K files
- Mixed files/subdirectories
- Response time (p50, p95, p99)

### **Commit History Performance**
- Latest commits for files in large directories
- Commit traversal with merge commits
- Memory allocation patterns
- Cache hit/miss behavior

### **Large Repository Handling**
- Enterprise-scale repositories (50K+ commits)
- Timeout and error handling behavior
- Memory consumption patterns
- Degraded mode activation

### **Concurrent Load Performance**
- 10+ concurrent users
- Cache contention scenarios
- Resource contention patterns
- Connection pooling effectiveness

## Results

After benchmarking, you'll get:

1. **Performance Matrix**: Response times compared across all systems
2. **Resource Usage**: Memory, CPU, network patterns
3. **Architecture Insights**: Inferred implementation patterns
4. **Recommendations**: Evidence-based optimization priorities

## Why This Matters

Without competitive benchmarking, our performance optimizations are **theoretical**:

- ❓ **Problem Validation**: Is O(C×N×F) complexity actually a real-world issue?
- ❓ **Solution Validation**: Are our proposed optimizations better than proven approaches?
- ❓ **Target Validation**: What constitutes acceptable performance in this domain?

## Key Questions Answered

1. **Do competitors experience similar performance degradation on large repos?**
   - If yes → Our analysis is correct, proceed with optimizations
   - If no → Learn from their architectural patterns

2. **What patterns do production systems actually use?**
   - Pagination strategies
   - Caching implementations
   - Graceful degradation modes
   - Rate limiting approaches

3. **Are we over-engineering or under-engineering solutions?**
   - Evidence-based performance targets
   - Industry-standard trade-offs
   - Competitive differentiation opportunities

## Expected Outcomes

### **Architecture Validation Scenarios**

**Scenario A**: We're significantly slower
- **Action**: Adopt proven patterns from faster competitors
- **Focus**: Their architectural advantages

**Scenario B**: We're competitive
- **Action**: Validate current optimization priorities
- **Focus**: Differentiation features

**Scenario C**: We're significantly faster
- **Action**: Investigate if we're sacrificing correctness
- **Focus**: Validate accuracy vs competitors

## Prerequisites

- Docker and Docker Compose
- Go 1.21+
- 8GB+ RAM (for GitLab)
- Network access for Docker images

## Documentation

- [Performance Analysis](./docs/performance-analysis.md) - Current bottleneck analysis
- [Competitive Benchmarking](./docs/competitive-benchmarking.md) - Detailed framework
- [Existing Benchmarks](./internal/overview/performance_bench_test.go) - Internal performance tests