#!/bin/bash

# Quick Test: Commit-Graph Performance Impact
# Tests the 60% performance improvement we discovered

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "⚡ Testing Commit-Graph Performance Impact"
echo "========================================"
echo ""

# Test on the current CollabOps repository
echo "📊 Testing on CollabOps repository (current directory)"
echo "Repository path: $PROJECT_ROOT"
echo ""

# Check current state
if [ -f "$PROJECT_ROOT/.git/objects/info/commit-graph" ]; then
    echo "🟡 Commit-graph already exists - removing for clean test"
    rm -f "$PROJECT_ROOT/.git/objects/info/commit-graph"
fi

echo "1️⃣  Testing WITHOUT commit-graph:"
echo "   Running git log traversal (3 samples)..."

# Test without commit-graph (3 samples for average)
total_time=0
for i in {1..3}; do
    start_time=$(perl -MTime::HiRes=time -E 'say time')
    commit_count=$(git log --oneline | wc -l)
    end_time=$(perl -MTime::HiRes=time -E 'say time')

    duration=$(perl -E "say $end_time - $start_time")
    total_time=$(perl -E "say $total_time + $duration")

    echo "   Sample $i: ${duration}s ($commit_count commits)"
done

baseline_avg=$(perl -E "say $total_time / 3")
echo "   📈 Average without commit-graph: ${baseline_avg}s"
echo ""

echo "2️⃣  Generating commit-graph:"
git commit-graph write --reachable
if [ -f "$PROJECT_ROOT/.git/objects/info/commit-graph" ]; then
    commit_graph_size=$(ls -lh "$PROJECT_ROOT/.git/objects/info/commit-graph" | awk '{print $5}')
    echo "   ✅ Commit-graph generated (${commit_graph_size})"
else
    echo "   ❌ Failed to generate commit-graph"
    exit 1
fi
echo ""

echo "3️⃣  Testing WITH commit-graph:"
echo "   Running git log traversal (3 samples)..."

# Test with commit-graph (3 samples for average)
total_time=0
for i in {1..3}; do
    start_time=$(perl -MTime::HiRes=time -E 'say time')
    commit_count=$(git log --oneline | wc -l)
    end_time=$(perl -MTime::HiRes=time -E 'say time')

    duration=$(perl -E "say $end_time - $start_time")
    total_time=$(perl -E "say $total_time + $duration")

    echo "   Sample $i: ${duration}s ($commit_count commits)"
done

optimized_avg=$(perl -E "say $total_time / 3")
echo "   📈 Average with commit-graph: ${optimized_avg}s"
echo ""

# Calculate improvement
if (( $(echo "$baseline_avg > 0" | bc -l) )); then
    improvement=$(perl -E "say sprintf('%.1f', (1 - $optimized_avg / $baseline_avg) * 100)")
    speedup=$(perl -E "say sprintf('%.1f', $baseline_avg / $optimized_avg)")

    echo "🎯 PERFORMANCE IMPACT ANALYSIS"
    echo "=============================="
    echo "Baseline (no commit-graph):  ${baseline_avg}s"
    echo "Optimized (with commit-graph): ${optimized_avg}s"
    echo ""
    echo "📊 Performance Improvement: ${improvement}% faster"
    echo "📊 Speed Multiplier: ${speedup}x"
    echo ""

    # Validate our research finding
    if (( $(echo "$improvement >= 50" | bc -l) )); then
        echo "✅ SUCCESS: Confirmed significant performance improvement!"
        echo "   Research predicted 60% improvement"
        echo "   Actual measurement: ${improvement}% improvement"
    elif (( $(echo "$improvement >= 20" | bc -l) )); then
        echo "⚠️  MODERATE: Decent improvement but less than expected"
        echo "   Research predicted 60% improvement"
        echo "   Actual measurement: ${improvement}% improvement"
        echo "   Note: Improvement varies by repository size and structure"
    else
        echo "❌ LOW IMPACT: Minimal improvement detected"
        echo "   This may indicate:"
        echo "   - Repository is too small to show significant improvement"
        echo "   - Commit-graph optimization is already present in git operations"
        echo "   - Other factors are dominating performance"
    fi
    echo ""

    # Provide recommendation
    echo "💡 RECOMMENDATION"
    echo "================="
    if (( $(echo "$improvement >= 30" | bc -l) )); then
        echo "✅ Enable commit-graph optimization in CollabOps"
        echo "   - Add to repository initialization process"
        echo "   - Auto-generate for existing repositories"
        echo "   - Monitor performance improvement in production"
    else
        echo "🤔 Consider commit-graph optimization with monitoring"
        echo "   - Test on larger/more complex repositories"
        echo "   - Measure impact on actual CollabOps operations"
        echo "   - Compare cost/benefit for storage overhead"
    fi
    echo ""

    # Feature flag configuration
    echo "🔧 FEATURE FLAG CONFIGURATION"
    echo "============================="
    echo "To enable commit-graph optimization:"
    echo ""
    echo "export COLLABOPS_PERFORMANCE_FEATURES_ENABLE_COMMIT_GRAPH_OPTIMIZATION=true"
    echo ""
    echo "Or add to config/performance_features.yml:"
    echo "performance_features:"
    echo "  enable_commit_graph_optimization: true"

else
    echo "❌ ERROR: Unable to calculate performance improvement"
    echo "   Baseline measurement was invalid"
fi

echo ""
echo "🏁 Test completed! Commit-graph remains enabled for ongoing benefit."