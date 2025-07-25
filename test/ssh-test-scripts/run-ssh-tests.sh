#!/bin/bash
# Comprehensive SSH protocol testing script
# Simulates SSH gateway behavior and tests all scenarios

set -e

# Configuration
EXECUTOR_URL="${EXECUTOR_URL:-http://localhost:8080}"
OUTPUT_DIR="${TEST_OUTPUT:-/results}"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
REPORT_FILE="${OUTPUT_DIR}/ssh-test-report-${TIMESTAMP}.json"

# Ensure output directory exists
mkdir -p "${OUTPUT_DIR}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Test results
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0
TEST_RESULTS=()

echo -e "${BLUE}=== SSH Protocol Comprehensive Testing ===${NC}"
echo "Executor URL: ${EXECUTOR_URL}"
echo "Report will be saved to: ${REPORT_FILE}"
echo

# Helper functions
log_test() {
    echo -e "\n${YELLOW}[TEST ${TOTAL_TESTS}] $1${NC}"
    ((TOTAL_TESTS++))
}

log_pass() {
    echo -e "${GREEN}✓ PASS: $1${NC}"
    ((PASSED_TESTS++))
    TEST_RESULTS+=("{\"test\": \"$1\", \"status\": \"PASS\", \"details\": \"$2\"}")
}

log_fail() {
    echo -e "${RED}✗ FAIL: $1${NC}"
    echo -e "${RED}  Details: $2${NC}"
    ((FAILED_TESTS++))
    TEST_RESULTS+=("{\"test\": \"$1\", \"status\": \"FAIL\", \"details\": \"$2\"}")
}

# Create test repository
create_test_repo() {
    local repo_name=$1
    local temp_dir=$(mktemp -d)
    
    cd "$temp_dir"
    git init
    echo "# Test Repository" > README.md
    git add README.md
    git commit -m "Initial commit"
    
    # Create some test content
    echo "Test content" > test.txt
    git add test.txt
    git commit -m "Add test file"
    
    # Create a tag
    git tag v1.0.0
    
    cd - > /dev/null
    echo "$temp_dir"
}

# Test 1: Basic SSH upload-pack functionality
test_basic_upload_pack() {
    log_test "Basic SSH upload-pack functionality"
    
    response=$(curl -s -w "\n%{http_code}" \
        -X POST \
        -H "X-USER-ID: test-user-123" \
        -H "X-Source: ssh-gateway" \
        -H "X-CORRELATION-ID: test-001" \
        -H "Content-Type: application/x-git-upload-pack" \
        --data-binary "0014command=ls-refs\n0000" \
        "${EXECUTOR_URL}/ssh/test-owner/test-repo/upload-pack")
    
    http_code=$(echo "$response" | tail -n1)
    body=$(echo "$response" | sed '$d')
    
    if [ "$http_code" = "200" ]; then
        log_pass "Basic upload-pack" "HTTP 200 received"
    else
        log_fail "Basic upload-pack" "Expected HTTP 200, got $http_code"
    fi
}

# Test 2: SSH receive-pack functionality
test_basic_receive_pack() {
    log_test "Basic SSH receive-pack functionality"
    
    response=$(curl -s -w "\n%{http_code}" \
        -X POST \
        -H "X-USER-ID: test-user-123" \
        -H "X-Source: ssh-gateway" \
        -H "X-CORRELATION-ID: test-002" \
        -H "Content-Type: application/x-git-receive-pack" \
        --data-binary "0000" \
        "${EXECUTOR_URL}/ssh/test-owner/test-repo/receive-pack")
    
    http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" = "200" ]; then
        log_pass "Basic receive-pack" "HTTP 200 received"
    else
        log_fail "Basic receive-pack" "Expected HTTP 200, got $http_code"
    fi
}

# Test 3: Permission validation
test_permission_validation() {
    log_test "Permission validation for SSH operations"
    
    # Test read-only user trying to push
    response=$(curl -s -w "\n%{http_code}" \
        -X POST \
        -H "X-USER-ID: readonly-user" \
        -H "X-Source: ssh-gateway" \
        -H "X-CORRELATION-ID: test-003" \
        "${EXECUTOR_URL}/ssh/test-owner/test-repo/receive-pack")
    
    http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" = "403" ]; then
        log_pass "Permission validation" "Read-only user correctly denied write access (403)"
    else
        log_fail "Permission validation" "Expected 403 for read-only user, got $http_code"
    fi
}

# Test 4: Source header validation
test_source_validation() {
    log_test "Source header validation"
    
    # Test with invalid source
    response=$(curl -s -w "\n%{http_code}" \
        -X POST \
        -H "X-USER-ID: test-user-123" \
        -H "X-Source: invalid-source" \
        -H "X-CORRELATION-ID: test-004" \
        "${EXECUTOR_URL}/ssh/test-owner/test-repo/upload-pack")
    
    http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" = "400" ]; then
        log_pass "Source validation" "Invalid source correctly rejected (400)"
    else
        log_fail "Source validation" "Expected 400 for invalid source, got $http_code"
    fi
}

# Test 5: Git protocol headers
test_git_protocol_headers() {
    log_test "Git protocol header handling"
    
    response=$(curl -s -w "\n%{http_code}" \
        -X POST \
        -H "X-USER-ID: test-user-123" \
        -H "X-Source: ssh-gateway" \
        -H "X-CORRELATION-ID: test-005" \
        -H "Git-Protocol: version=2" \
        -H "SSH-Connection: 192.168.1.100 22 192.168.1.200 52000" \
        -H "Git-Capabilities: thin-pack side-band" \
        --data-binary "0014command=ls-refs\n0000" \
        "${EXECUTOR_URL}/ssh/test-owner/test-repo/upload-pack")
    
    http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" = "200" ]; then
        log_pass "Git protocol headers" "Headers processed successfully"
    else
        log_fail "Git protocol headers" "Failed to process Git headers, got $http_code"
    fi
}

# Test 6: Concurrent SSH connections
test_concurrent_connections() {
    log_test "Concurrent SSH connections"
    
    local concurrent_count=20
    local success_count=0
    local temp_file=$(mktemp)
    
    echo "Sending $concurrent_count concurrent requests..."
    
    for i in $(seq 1 $concurrent_count); do
        (
            response=$(curl -s -w "\n%{http_code}" \
                -X POST \
                -H "X-USER-ID: test-user-123" \
                -H "X-Source: ssh-gateway" \
                -H "X-CORRELATION-ID: concurrent-$i" \
                --data-binary "0014command=ls-refs\n0000" \
                "${EXECUTOR_URL}/ssh/test-owner/test-repo/upload-pack")
            
            http_code=$(echo "$response" | tail -n1)
            if [ "$http_code" = "200" ]; then
                echo "1" >> "$temp_file"
            fi
        ) &
    done
    
    wait
    
    success_count=$(wc -l < "$temp_file" 2>/dev/null || echo "0")
    rm -f "$temp_file"
    
    if [ "$success_count" -eq "$concurrent_count" ]; then
        log_pass "Concurrent connections" "All $concurrent_count requests succeeded"
    else
        log_fail "Concurrent connections" "Only $success_count/$concurrent_count requests succeeded"
    fi
}

# Test 7: Large data transfer
test_large_data_transfer() {
    log_test "Large data transfer handling"
    
    # Create a large packfile simulation (5MB)
    local large_data=$(dd if=/dev/zero bs=1024 count=5120 2>/dev/null | base64)
    
    response=$(curl -s -w "\n%{http_code}" \
        -X POST \
        -H "X-USER-ID: test-user-123" \
        -H "X-Source: ssh-gateway" \
        -H "X-CORRELATION-ID: test-007" \
        -H "Content-Type: application/x-git-receive-pack" \
        --data-binary "$large_data" \
        --max-time 30 \
        "${EXECUTOR_URL}/ssh/test-owner/test-repo/receive-pack")
    
    http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" = "200" ] || [ "$http_code" = "413" ]; then
        log_pass "Large data transfer" "Server handled large request appropriately ($http_code)"
    else
        log_fail "Large data transfer" "Unexpected response code: $http_code"
    fi
}

# Test 8: Invalid repository paths
test_invalid_paths() {
    log_test "Invalid repository path handling"
    
    # Test path traversal
    response=$(curl -s -w "\n%{http_code}" \
        -X POST \
        -H "X-USER-ID: test-user-123" \
        -H "X-Source: ssh-gateway" \
        -H "X-CORRELATION-ID: test-008" \
        "${EXECUTOR_URL}/ssh/../../../etc/passwd/upload-pack")
    
    http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" = "400" ] || [ "$http_code" = "404" ]; then
        log_pass "Path traversal protection" "Path traversal blocked ($http_code)"
    else
        log_fail "Path traversal protection" "Path traversal not blocked, got $http_code"
    fi
}

# Test 9: Authentication edge cases
test_auth_edge_cases() {
    log_test "Authentication edge cases"
    
    # Missing user ID
    response=$(curl -s -w "\n%{http_code}" \
        -X POST \
        -H "X-Source: ssh-gateway" \
        -H "X-CORRELATION-ID: test-009" \
        "${EXECUTOR_URL}/ssh/test-owner/test-repo/upload-pack")
    
    http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" = "400" ]; then
        log_pass "Missing auth" "Missing user ID correctly rejected"
    else
        log_fail "Missing auth" "Expected 400 for missing user ID, got $http_code"
    fi
}

# Test 10: Protocol-specific features
test_protocol_features() {
    log_test "SSH protocol-specific features"
    
    # Test with SSH-specific headers and commands
    response=$(curl -s -w "\n%{http_code}" \
        -X POST \
        -H "X-USER-ID: test-user-123" \
        -H "X-Source: ssh-gateway" \
        -H "X-CORRELATION-ID: test-010" \
        -H "SSH-Original-Command: git-upload-pack '/test-owner/test-repo'" \
        -H "SSH-Connection: 10.0.0.1 22 10.0.0.2 50000" \
        --data-binary "0014command=ls-refs\n00000014command=fetch\n0000" \
        "${EXECUTOR_URL}/ssh/test-owner/test-repo/upload-pack")
    
    http_code=$(echo "$response" | tail -n1)
    
    if [ "$http_code" = "200" ]; then
        log_pass "Protocol features" "SSH-specific features handled correctly"
    else
        log_fail "Protocol features" "Failed to handle SSH features, got $http_code"
    fi
}

# Run all tests
run_all_tests() {
    test_basic_upload_pack
    test_basic_receive_pack
    test_permission_validation
    test_source_validation
    test_git_protocol_headers
    test_concurrent_connections
    test_large_data_transfer
    test_invalid_paths
    test_auth_edge_cases
    test_protocol_features
}

# Generate JSON report
generate_report() {
    cat > "$REPORT_FILE" << EOF
{
    "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
    "executor_url": "${EXECUTOR_URL}",
    "summary": {
        "total_tests": ${TOTAL_TESTS},
        "passed": ${PASSED_TESTS},
        "failed": ${FAILED_TESTS},
        "success_rate": $(awk "BEGIN {printf \"%.2f\", ${PASSED_TESTS}/${TOTAL_TESTS}*100}")
    },
    "tests": [
        $(IFS=','; echo "${TEST_RESULTS[*]}")
    ]
}
EOF
}

# Main execution
echo "Starting SSH protocol tests..."
run_all_tests

echo -e "\n${BLUE}=== Test Summary ===${NC}"
echo "Total Tests: ${TOTAL_TESTS}"
echo -e "Passed: ${GREEN}${PASSED_TESTS}${NC}"
echo -e "Failed: ${RED}${FAILED_TESTS}${NC}"
echo -e "Success Rate: $(awk "BEGIN {printf \"%.2f\", ${PASSED_TESTS}/${TOTAL_TESTS}*100}")%"

generate_report
echo -e "\nDetailed report saved to: ${REPORT_FILE}"

# Exit with appropriate code
if [ ${FAILED_TESTS} -gt 0 ]; then
    exit 1
else
    exit 0
fi 