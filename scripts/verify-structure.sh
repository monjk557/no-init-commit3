#!/bin/bash

# CollabOps Git Executor - Project Structure Verification Script
# This script verifies that Task 1.1 (Create Project Directory Structure) was completed correctly

echo "🧪 Verifying CollabOps Git Executor Project Structure..."
echo "=================================================="

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Function to check if directory exists
check_directory() {
    if [ -d "$1" ]; then
        echo -e "${GREEN}✅ Directory exists: $1${NC}"
        return 0
    else
        echo -e "${RED}❌ Directory missing: $1${NC}"
        return 1
    fi
}

# Function to check if file exists and has content
check_file() {
    if [ -f "$1" ]; then
        lines=$(wc -l < "$1")
        if [ "$lines" -gt 5 ]; then
            echo -e "${GREEN}✅ File exists with content: $1 ($lines lines)${NC}"
            return 0
        else
            echo -e "${YELLOW}⚠️  File exists but may be empty: $1 ($lines lines)${NC}"
            return 1
        fi
    else
        echo -e "${RED}❌ File missing: $1${NC}"
        return 1
    fi
}

# Function to check go.mod content
check_go_mod() {
    echo -e "\n📦 Checking Go Module Configuration..."
    
    if ! check_file "go.mod"; then
        return 1
    fi
    
    # Check Go version (1.21 or higher)
    if grep -E "go 1\.(2[1-9]|[3-9][0-9])" go.mod; then
        echo -e "${GREEN}✅ Go version 1.21+ specified${NC}"
    else
        echo -e "${RED}❌ Go version 1.21+ not found${NC}"
        return 1
    fi
    
    # Check key dependencies (currently needed)
    dependencies=("spf13/viper" "testify")
    
    for dep in "${dependencies[@]}"; do
        if grep -q "$dep" go.mod; then
            echo -e "${GREEN}✅ Dependency found: $dep${NC}"
        else
            echo -e "${RED}❌ Dependency missing: $dep${NC}"
            return 1
        fi
    done
    
    # Check planned dependencies (will be added in future tasks)
    planned_deps=("go-git/go-git" "go-chi/chi" "uber.org/zap" "prometheus")
    echo -e "${BLUE}📋 Planned dependencies (for future tasks):${NC}"
    for dep in "${planned_deps[@]}"; do
        if grep -q "$dep" go.mod; then
            echo -e "${GREEN}✅ Already added: $dep${NC}"
        else
            echo -e "${YELLOW}⏳ Will be added later: $dep${NC}"
        fi
    done
    
    return 0
}

# Main verification
echo -e "\n📁 Checking Directory Structure..."
failures=0

# Check required directories
directories=("cmd" "internal" "pkg" "config" "docs" "deployments" "bin")
for dir in "${directories[@]}"; do
    if ! check_directory "$dir"; then
        ((failures++))
    fi
done

echo -e "\n📄 Checking Essential Files..."

# Check required files
files=("go.mod" "README.md" ".gitignore")
for file in "${files[@]}"; do
    if ! check_file "$file"; then
        ((failures++))
    fi
done

# Check go.mod content
if ! check_go_mod; then
    ((failures++))
fi

# Summary
echo -e "\n📊 Verification Summary"
echo "======================"

if [ $failures -eq 0 ]; then
    echo -e "${GREEN}🎉 All checks passed! Project structure is correctly set up.${NC}"
    echo -e "${GREEN}✅ Task 1.1 (Create Project Directory Structure) verified successfully.${NC}"
    exit 0
else
    echo -e "${RED}❌ $failures check(s) failed. Please review the output above.${NC}"
    exit 1
fi 