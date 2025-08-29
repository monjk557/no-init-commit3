#!/bin/bash
# Manual testing script for SSH protocol endpoints
# This script provides comprehensive manual testing steps for the SSH functionality

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
EXECUTOR_URL="http://localhost:8080"
TEST_OWNER="test-owner"
TEST_REPO="test-repo"
TEST_USER_ID="test-user-123"
CORRELATION_ID="manual-test-$(date +%s)"

echo -e "${BLUE}=== SSH Protocol Manual Testing Script ===${NC}"
echo -e "This script tests the SSH protocol endpoints implemented in collabops-git-executor"
echo

# Function to print test header
print_test() {
    echo -e "\n${YELLOW}TEST: $1${NC}"
    echo "----------------------------------------"
}

# Function to print success
print_success() {
    echo -e "${GREEN}✓ $1${NC}"
}

# Function to print error
print_error() {
    echo -e "${RED}✗ $1${NC}"
}

# Function to make SSH endpoint request
make_ssh_request() {
    local endpoint=$1
    local data=$2
    local extra_headers=$3
    
    echo -e "${BLUE}Request:${NC}"
    echo "POST ${EXECUTOR_URL}/ssh/${TEST_OWNER}/${TEST_REPO}/${endpoint}"
    echo "Headers:"
    echo "  X-USER-ID: ${TEST_USER_ID}"
    echo "  X-Source: ssh-gateway"
    echo "  X-CORRELATION-ID: ${CORRELATION_ID}"
    echo "  Content-Type: application/x-git-${endpoint}"
    if [ -n "$extra_headers" ]; then
        echo "  $extra_headers"
    fi
    echo
    
    response=$(curl -s -w "\n%{http_code}" \
        -X POST \
        -H "X-USER-ID: ${TEST_USER_ID}" \
        -H "X-Source: ssh-gateway" \
        -H "X-CORRELATION-ID: ${CORRELATION_ID}" \
        -H "Content-Type: application/x-git-${endpoint}" \
        ${extra_headers:+-H "$extra_headers"} \
        --data-binary "$data" \
        "${EXECUTOR_URL}/ssh/${TEST_OWNER}/${TEST_REPO}/${endpoint}")
    
    http_code=$(echo "$response" | tail -n1)
    body=$(echo "$response" | sed '$d')
    
    echo -e "${BLUE}Response:${NC}"
    echo "HTTP Status: $http_code"
    echo "Body (first 200 chars): ${body:0:200}"
    echo
    
    return $([ "$http_code" = "200" ] && echo 0 || echo 1)
}

# Test 1: Basic upload-pack request
print_test "Basic SSH upload-pack request"
if make_ssh_request "upload-pack" "0014command=ls-refs\n0000"; then
    print_success "Basic upload-pack request succeeded"
else
    print_error "Basic upload-pack request failed"
fi

# Test 2: Upload-pack with Git protocol version
print_test "SSH upload-pack with Git protocol v2"
if make_ssh_request "upload-pack" "0014command=ls-refs\n0000" "Git-Protocol: version=2"; then
    print_success "Upload-pack with protocol v2 succeeded"
else
    print_error "Upload-pack with protocol v2 failed"
fi

# Test 3: Invalid source header
print_test "SSH request with invalid source header"
response=$(curl -s -w "\n%{http_code}" \
    -X POST \
    -H "X-USER-ID: ${TEST_USER_ID}" \
    -H "X-Source: invalid-source" \
    -H "X-CORRELATION-ID: ${CORRELATION_ID}" \
    "${EXECUTOR_URL}/ssh/${TEST_OWNER}/${TEST_REPO}/upload-pack")

http_code=$(echo "$response" | tail -n1)
if [ "$http_code" = "400" ]; then
    print_success "Invalid source header correctly rejected with 400"
else
    print_error "Invalid source header not properly rejected (got $http_code)"
fi

# Test 4: Missing user ID
print_test "SSH request without user ID"
response=$(curl -s -w "\n%{http_code}" \
    -X POST \
    -H "X-Source: ssh-gateway" \
    -H "X-CORRELATION-ID: ${CORRELATION_ID}" \
    "${EXECUTOR_URL}/ssh/${TEST_OWNER}/${TEST_REPO}/upload-pack")

http_code=$(echo "$response" | tail -n1)
if [ "$http_code" = "400" ]; then
    print_success "Missing user ID correctly rejected with 400"
else
    print_error "Missing user ID not properly rejected (got $http_code)"
fi

# Test 5: Basic receive-pack request
print_test "Basic SSH receive-pack request"
if make_ssh_request "receive-pack" "0000"; then
    print_success "Basic receive-pack request succeeded"
else
    print_error "Basic receive-pack request failed"
fi

# Test 6: Concurrent requests
print_test "Concurrent SSH requests"
echo "Sending 10 concurrent upload-pack requests..."
success_count=0
for i in {1..10}; do
    (
        response=$(curl -s -w "\n%{http_code}" \
            -X POST \
            -H "X-USER-ID: ${TEST_USER_ID}" \
            -H "X-Source: ssh-gateway" \
            -H "X-CORRELATION-ID: concurrent-${i}" \
            --data-binary "0014command=ls-refs\n0000" \
            "${EXECUTOR_URL}/ssh/${TEST_OWNER}/${TEST_REPO}/upload-pack")
        
        http_code=$(echo "$response" | tail -n1)
        if [ "$http_code" = "200" ]; then
            echo -n "."
        else
            echo -n "F"
        fi
    ) &
done
wait
echo
print_success "Concurrent requests completed"

# Test 7: Large request handling
print_test "Large SSH request"
# Create a large request (1MB of data)
large_data=$(dd if=/dev/zero bs=1024 count=1024 2>/dev/null | tr '\0' 'A')
response=$(curl -s -w "\n%{http_code}" \
    -X POST \
    -H "X-USER-ID: ${TEST_USER_ID}" \
    -H "X-Source: ssh-gateway" \
    -H "X-CORRELATION-ID: large-request" \
    --data-binary "$large_data" \
    "${EXECUTOR_URL}/ssh/${TEST_OWNER}/${TEST_REPO}/upload-pack" \
    --max-time 30)

http_code=$(echo "$response" | tail -n1)
echo "Large request response code: $http_code"

# Test 8: Path traversal attempt
print_test "Path traversal security check"
response=$(curl -s -w "\n%{http_code}" \
    -X POST \
    -H "X-USER-ID: ${TEST_USER_ID}" \
    -H "X-Source: ssh-gateway" \
    -H "X-CORRELATION-ID: ${CORRELATION_ID}" \
    "${EXECUTOR_URL}/ssh/../../../etc/passwd/upload-pack")

http_code=$(echo "$response" | tail -n1)
if [ "$http_code" = "400" ] || [ "$http_code" = "404" ]; then
    print_success "Path traversal correctly blocked"
else
    print_error "Path traversal not properly blocked (got $http_code)"
fi

echo -e "\n${BLUE}=== Advanced Testing with Git Client ===${NC}"
echo "To test with an actual Git client through SSH:"
echo
echo "1. Set up SSH tunnel to executor:"
echo "   ssh -L 5001:localhost:5001 your-server"
echo
echo "2. Configure Git to use custom SSH command:"
echo "   export GIT_SSH_COMMAND='ssh -o SendEnv=X-USER-ID -o SendEnv=X-CORRELATION-ID'"
echo
echo "3. Clone via SSH (simulated):"
echo "   git clone ssh://git@localhost:5001/${TEST_OWNER}/${TEST_REPO}"
echo
echo "4. Test push operations:"
echo "   cd ${TEST_REPO}"
echo "   echo 'test' > newfile.txt"
echo "   git add newfile.txt"
echo "   git commit -m 'Test SSH push'"
echo "   git push origin main"

echo -e "\n${BLUE}=== Performance Testing ===${NC}"
echo "Run performance tests with Apache Bench:"
echo
echo "1. Test upload-pack performance:"
echo "   ab -n 1000 -c 10 -H 'X-USER-ID: ${TEST_USER_ID}' -H 'X-Source: ssh-gateway' \\"
echo "      -H 'X-CORRELATION-ID: perf-test' -p upload-pack-data.bin \\"
echo "      ${EXECUTOR_URL}/ssh/${TEST_OWNER}/${TEST_REPO}/upload-pack"
echo
echo "2. Create upload-pack-data.bin with:"
echo "   echo -ne '0014command=ls-refs\n0000' > upload-pack-data.bin"

echo -e "\n${BLUE}=== Monitoring and Debugging ===${NC}"
echo "1. Check executor logs for SSH requests:"
echo "   grep 'SSH' /var/log/git-executor.log"
echo
echo "2. Monitor concurrent connections:"
echo "   netstat -an | grep :8080 | grep ESTABLISHED | wc -l"
echo
echo "3. Check for errors:"
echo "   grep 'ERROR' /var/log/git-executor.log | grep 'SSH'"

echo -e "\n${GREEN}=== Testing Complete ===${NC}"
echo "Manual testing script finished. Check the results above for any failures." 