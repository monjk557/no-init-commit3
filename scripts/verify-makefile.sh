#!/bin/bash

# CollabOps Git Executor - Makefile Verification Script
# This script verifies that the Makefile is correctly implemented and functional

echo "🧪 Verifying Makefile Implementation..."
echo "======================================"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Function to run a make command and check exit status
test_make_command() {
    local command=$1
    local description=$2
    local should_fail=${3:-false}
    
    echo -e "\n${BLUE}Testing: make $command${NC}"
    
    if [ "$should_fail" = "true" ]; then
        # Command is expected to fail (like check-go without Go)
        if make $command >/dev/null 2>&1; then
            echo -e "${YELLOW}⚠️  Expected failure but command succeeded: make $command${NC}"
            return 1
        else
            echo -e "${GREEN}✅ $description (expected failure)${NC}"
            return 0
        fi
    else
        # Command should succeed
        if make $command >/dev/null 2>&1; then
            echo -e "${GREEN}✅ $description${NC}"
            return 0
        else
            echo -e "${RED}❌ Failed: $description${NC}"
            return 1
        fi
    fi
}

# Function to check if Makefile contains specific targets
check_target() {
    local target=$1
    local description=$2
    
    if grep -q "^$target:" Makefile; then
        echo -e "${GREEN}✅ Target exists: $target - $description${NC}"
        return 0
    else
        echo -e "${RED}❌ Target missing: $target${NC}"
        return 1
    fi
}

# Main verification
failures=0

echo -e "\n📄 Checking Makefile Exists..."
if [ ! -f "Makefile" ]; then
    echo -e "${RED}❌ Makefile not found${NC}"
    exit 1
else
    lines=$(wc -l < "Makefile")
    echo -e "${GREEN}✅ Makefile exists ($lines lines)${NC}"
fi

echo -e "\n🎯 Checking Required Targets..."

# List of critical targets that should exist
targets=(
    "help:Show help message"
    "build:Build binary"
    "test:Run tests"
    "clean:Clean files"
    "fmt:Format code"
    "vet:Run go vet"
    "lint:Run linter"
    "deps:Download dependencies"
    "verify-structure:Verify structure"
    "verify-config:Verify config"
    "verify-all:Run all verifications"
    "quick-test:Quick tests without Go"
    "status:Show project status"
    "check-go:Check Go installation"
)

for target_desc in "${targets[@]}"; do
    IFS=':' read -r target desc <<< "$target_desc"
    if ! check_target "$target" "$desc"; then
        ((failures++))
    fi
done

echo -e "\n🧪 Testing Non-Go Commands..."

# Test commands that don't require Go
non_go_commands=(
    "help:Display help message"
    "status:Show project status"
    "verify-structure:Verify project structure"
    "verify-config:Verify configuration"
    "verify-all:Run all verifications"
    "quick-test:Run quick tests"
)

for cmd_desc in "${non_go_commands[@]}"; do
    IFS=':' read -r cmd desc <<< "$cmd_desc"
    if ! test_make_command "$cmd" "$desc"; then
        ((failures++))
    fi
done

echo -e "\n🔧 Testing Go-Required Commands (Expected to Fail)..."

# Test commands that require Go (should fail gracefully)
go_commands=(
    "check-go:Check Go installation"
)

for cmd_desc in "${go_commands[@]}"; do
    IFS=':' read -r cmd desc <<< "$cmd_desc"
    if ! test_make_command "$cmd" "$desc" "true"; then
        ((failures++))
    fi
done

echo -e "\n📋 Checking Makefile Content..."

# Check for important content in Makefile
content_checks=(
    "GOCMD=go:Go command variable"
    "BINARY_NAME=git-executor:Binary name variable"
    "BIN_DIR=./bin:Binary directory variable"
    ".PHONY:PHONY targets declared"
    "## :Help documentation format"
    "GREEN=:Color definitions"
    "BLUE=:Color usage in targets"
)

for check_desc in "${content_checks[@]}"; do
    IFS=':' read -r pattern desc <<< "$check_desc"
    if grep -q "$pattern" Makefile; then
        echo -e "${GREEN}✅ $desc found${NC}"
    else
        echo -e "${RED}❌ $desc not found${NC}"
        ((failures++))
    fi
done

echo -e "\n🔍 Checking Help Output..."

# Test that help command produces expected output
if make help | grep -q "CollabOps Git Executor"; then
    echo -e "${GREEN}✅ Help output contains project name${NC}"
else
    echo -e "${RED}❌ Help output missing project name${NC}"
    ((failures++))
fi

if make help | grep -q "Build Commands"; then
    echo -e "${GREEN}✅ Help output shows build commands section${NC}"
else
    echo -e "${RED}❌ Help output missing build commands section${NC}"
    ((failures++))
fi

if make help | grep -q "Test Commands"; then
    echo -e "${GREEN}✅ Help output shows test commands section${NC}"
else
    echo -e "${RED}❌ Help output missing test commands section${NC}"
    ((failures++))
fi

echo -e "\n🚀 Testing Clean Operation..."

# Test clean command
if test_make_command "clean" "Clean operation"; then
    echo -e "${GREEN}✅ Clean command works${NC}"
else
    echo -e "${RED}❌ Clean command failed${NC}"
    ((failures++))
fi

echo -e "\n📊 Verification Summary"
echo "======================"

if [ $failures -eq 0 ]; then
    echo -e "${GREEN}🎉 All Makefile checks passed!${NC}"
    echo -e "${GREEN}✅ Makefile is correctly implemented and functional.${NC}"
    echo -e "\n${BLUE}📋 Makefile features verified:${NC}"
    echo -e "${BLUE}  • Comprehensive target structure${NC}"
    echo -e "${BLUE}  • Color-coded output${NC}"
    echo -e "${BLUE}  • Help documentation${NC}"
    echo -e "${BLUE}  • Non-Go commands work without Go${NC}"
    echo -e "${BLUE}  • Go-required commands fail gracefully${NC}"
    echo -e "${BLUE}  • Verification script integration${NC}"
    echo -e "${BLUE}  • Project status reporting${NC}"
    echo -e "\n${GREEN}🎯 Ready for development workflow!${NC}"
    exit 0
else
    echo -e "${RED}❌ $failures check(s) failed. Please review the output above.${NC}"
    exit 1
fi 