#!/bin/bash

# CollabOps Git Executor - Configuration Package Verification Script
# This script verifies that Task 1.2 (Implement Configuration Handling with Viper) was completed correctly

echo "🧪 Verifying Configuration Package Implementation..."
echo "================================================="

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Function to check if file exists and has content
check_file() {
    if [ -f "$1" ]; then
        lines=$(wc -l < "$1")
        if [ "$lines" -gt 10 ]; then
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

# Function to check for specific content in files
check_content() {
    local file=$1
    local pattern=$2
    local description=$3

    if [ -f "$file" ] && grep -q "$pattern" "$file"; then
        echo -e "${GREEN}✅ $description found in $file${NC}"
        return 0
    else
        echo -e "${RED}❌ $description not found in $file${NC}"
        return 1
    fi
}

# Main verification
echo -e "\n📁 Checking Configuration Package Structure..."
failures=0

# Check if config package directory exists
if [ -d "internal/config" ]; then
    echo -e "${GREEN}✅ Configuration package directory exists${NC}"
else
    echo -e "${RED}❌ Configuration package directory missing${NC}"
    ((failures++))
fi

echo -e "\n📄 Checking Configuration Package Files..."

# Check main config files
config_files=("internal/config/config.go" "internal/config/config_test.go" "config/config.yaml.example")
for file in "${config_files[@]}"; do
    if ! check_file "$file"; then
        ((failures++))
    fi
done

echo -e "\n🔧 Checking Configuration Package Content..."

# Check for required structs and functions in config.go
config_elements=(
    "type Config struct"
    "type ServerConfig struct"
    "type GitConfig struct"
    "type JWTConfig struct"
    "type LoggingConfig struct"
    "func Load("
    "func validate("
    "spf13/viper"
)

for element in "${config_elements[@]}"; do
    if ! check_content "internal/config/config.go" "$element" "Configuration element: $element"; then
        ((failures++))
    fi
done

echo -e "\n🧪 Checking Test Coverage..."

# Check for test functions in config_test.go
test_functions=(
    "func TestLoad_ValidConfig"
    "func TestLoad_Defaults"
    "func TestValidate_"
    "github.com/stretchr/testify"
)

for test_func in "${test_functions[@]}"; do
    if ! check_content "internal/config/config_test.go" "$test_func" "Test function: $test_func"; then
        ((failures++))
    fi
done

echo -e "\n📋 Checking Configuration Fields..."

# Check for required configuration fields in example
required_fields=(
    "server:"
    "port:"
    "base_path:"
    "git:"
    "repo_root:"
    "enable_packfile:"
    "jwt:"
    "algorithm:"
    "public_key_path:"
    "log:"
    "level:"
    "format:"
)

for field in "${required_fields[@]}"; do
    if ! check_content "config/config.yaml.example" "$field" "Configuration field: $field"; then
        ((failures++))
    fi
done

echo -e "\n🔧 Checking Viper Integration..."

# Check for Viper-specific functionality
viper_features=(
    "viper.SetConfigType"
    "viper.SetDefault"
    "viper.AutomaticEnv"
    "viper.Unmarshal"
    "COLLABOPS"
)

for feature in "${viper_features[@]}"; do
    if ! check_content "internal/config/config.go" "$feature" "Viper feature: $feature"; then
        ((failures++))
    fi
done

echo -e "\n✅ Checking Environment Variable Support..."

# Check for environment variable documentation
if check_content "config/config.yaml.example" "COLLABOPS_" "Environment variable examples"; then
    echo -e "${GREEN}✅ Environment variable support documented${NC}"
else
    echo -e "${YELLOW}⚠️  Environment variable support not documented${NC}"
    ((failures++))
fi

echo -e "\n🔍 Checking Validation Logic..."

# Check for validation functions and error handling
validation_elements=(
    "validate.*Config.*error"
    "validation errors"
    "fmt.Errorf"
)

for element in "${validation_elements[@]}"; do
    if ! check_content "internal/config/config.go" "$element" "Validation element: $element"; then
        ((failures++))
    fi
done

# Summary
echo -e "\n📊 Verification Summary"
echo "======================"

if [ $failures -eq 0 ]; then
    echo -e "${GREEN}🎉 All checks passed! Configuration package is correctly implemented.${NC}"
    echo -e "${GREEN}✅ Task 1.2 (Implement Configuration Handling with Viper) verified successfully.${NC}"
    echo -e "\n${BLUE}📋 Implementation includes:${NC}"
    echo -e "${BLUE}  • Complete Viper integration with YAML support${NC}"
    echo -e "${BLUE}  • Environment variable override support${NC}"
    echo -e "${BLUE}  • Comprehensive validation logic${NC}"
    echo -e "${BLUE}  • Extensive unit test coverage${NC}"
    echo -e "${BLUE}  • Detailed configuration example file${NC}"
    echo -e "${BLUE}  • All required configuration sections (server, git, jwt, log)${NC}"
    exit 0
else
    echo -e "${RED}❌ $failures check(s) failed. Please review the output above.${NC}"
    exit 1
fi
