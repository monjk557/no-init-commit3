#!/bin/bash

# Production Environment File Protection Test Suite
# This script runs comprehensive tests to validate protection against
# environment file deletion in all known production scenarios

set -e

echo "🔥 PRODUCTION ENVIRONMENT FILE PROTECTION TEST SUITE"
echo "═══════════════════════════════════════════════════"
echo ""
echo "This test suite validates that the environment file protection system"
echo "works across ALL real-world scenarios that have historically caused"
echo "environment file deletion, including:"
echo ""
echo "• GitHub PR squash merges"
echo "• GitLab MR workflows"
echo "• Gitea automated merges"
echo "• Bitbucket complex conflicts"
echo "• CI/CD pipeline integrations"
echo "• Emergency hotfixes"
echo "• Dependency updates"
echo "• Monorepo scenarios"
echo "• Microservice deployments"
echo ""

# Check if Go is available
if ! command -v go &> /dev/null; then
    echo "❌ Go is not installed or not in PATH"
    exit 1
fi

# Create a test directory
TEST_DIR="production_test_$(date +%Y%m%d_%H%M%S)"
mkdir -p "$TEST_DIR"
cd "$TEST_DIR"

echo "📁 Created test directory: $TEST_DIR"
echo ""

# Copy test files
cp ../production_scenario_simulator.go .
cp ../real_world_protection_test.go .
cp ../ci_cd_workflow_simulator.go .
cp ../comprehensive_production_test_runner.go .

# Initialize Go module for testing
cat > go.mod << EOF
module production-test

go 1.19

require (
    github.com/go-git/go-git/v5 v5.8.1
    go.uber.org/zap v1.24.0
)

require (
    github.com/Microsoft/go-winio v0.6.1 // indirect
    github.com/ProtonMail/go-crypto v0.0.0-20230717121422-5aa5874ade95 // indirect
    github.com/acomagu/bufpipe v1.0.4 // indirect
    github.com/cloudflare/circl v1.3.3 // indirect
    github.com/emirpasic/gods v1.18.1 // indirect
    github.com/go-git/gcfg v1.5.1-0.20230307220236-3a3c6141e376 // indirect
    github.com/go-git/go-billy/v5 v5.4.1 // indirect
    github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
    github.com/imdario/mergo v0.3.16 // indirect
    github.com/jbenet/go-context v0.0.0-20150711004518-d14ea06fba99 // indirect
    github.com/kevinburke/ssh_config v1.2.0 // indirect
    github.com/pjbgf/sha1cd v0.3.0 // indirect
    github.com/sergi/go-diff v1.1.0 // indirect
    github.com/skeema/knownhosts v1.2.0 // indirect
    github.com/xanzy/ssh-agent v0.3.3 // indirect
    go.uber.org/atomic v1.11.0 // indirect
    go.uber.org/multierr v1.11.0 // indirect
    golang.org/x/crypto v0.11.0 // indirect
    golang.org/x/mod v0.12.0 // indirect
    golang.org/x/net v0.12.0 // indirect
    golang.org/x/sys v0.10.0 // indirect
    golang.org/x/tools v0.11.0 // indirect
    gopkg.in/warnings.v0 v0.1.2 // indirect
)
EOF

echo "📦 Initializing Go module and downloading dependencies..."
go mod tidy

echo ""
echo "🚀 STARTING COMPREHENSIVE PRODUCTION TESTS"
echo "═══════════════════════════════════════════"
echo ""

# Run the comprehensive test suite
echo "⏳ Executing test suite (this may take several minutes)..."
echo ""

if go run comprehensive_production_test_runner.go; then
    TEST_RESULT="SUCCESS"
    EXIT_CODE=0
else
    TEST_RESULT="FAILED"
    EXIT_CODE=$?
fi

echo ""
echo "📋 TEST EXECUTION SUMMARY"
echo "═════════════════════════"
echo "Test Result: $TEST_RESULT"
echo "Exit Code: $EXIT_CODE"
echo "Test Directory: $(pwd)"
echo ""

# Interpret results
case $EXIT_CODE in
    0)
        echo "✅ ALL TESTS PASSED"
        echo "   The environment file protection system is working correctly"
        echo "   across all tested production scenarios. The system is ready"
        echo "   for production deployment."
        ;;
    1)
        echo "⚠️  PARTIAL SUCCESS"
        echo "   Most protection scenarios work, but some edge cases need"
        echo "   attention. Review the detailed test results above."
        ;;
    2)
        echo "❌ SIGNIFICANT ISSUES"
        echo "   The protection system has critical gaps. Additional"
        echo "   development is required before production deployment."
        ;;
    *)
        echo "❌ TEST EXECUTION FAILED"
        echo "   The test suite encountered execution errors. Check the"
        echo "   error messages above for details."
        ;;
esac

echo ""
echo "💡 NEXT STEPS"
echo "═════════════"
if [ $EXIT_CODE -eq 0 ]; then
    echo "1. Deploy protection system to production"
    echo "2. Set up monitoring and alerting"
    echo "3. Document protection features for development teams"
else
    echo "1. Review failed test scenarios in detail"
    echo "2. Address protection gaps identified in test results"
    echo "3. Re-run tests after implementing fixes"
fi

echo "4. Implement continuous testing of protection effectiveness"
echo "5. Create emergency response procedures for environment file issues"
echo ""

# Clean up
cd ..
if [ "$KEEP_TEST_DIR" != "1" ]; then
    echo "🧹 Cleaning up test directory..."
    rm -rf "$TEST_DIR"
else
    echo "📁 Test directory preserved: $TEST_DIR"
fi

echo ""
echo "🏁 Production Environment File Protection Test Complete"
echo "═════════════════════════════════════════════════════"

exit $EXIT_CODE