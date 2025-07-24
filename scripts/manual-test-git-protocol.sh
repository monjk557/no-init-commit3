#!/bin/bash

# Manual Testing Guide for Git Smart HTTP Protocol
# =================================================
# This script provides comprehensive manual testing for the collabops-git-executor
# Git Smart HTTP protocol implementation using curl commands.

set -e

echo "🧪 MANUAL TESTING GUIDE: Git Smart HTTP Protocol"
echo "================================================"
echo ""

# Configuration
SERVER_URL="http://localhost:9090"
TEST_USER="bjm"
TEST_OWNER="testowner"
TEST_REPO="testrepo"
CONFIG_FILE="config/test-config.yaml"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Helper functions
print_section() {
    echo -e "\n${BLUE}📋 $1${NC}"
    echo "$(echo "$1" | sed 's/./=/g')"
}

print_test() {
    echo -e "\n${YELLOW}🧪 Test: $1${NC}"
}

print_success() {
    echo -e "${GREEN}✅ $1${NC}"
}

print_error() {
    echo -e "${RED}❌ $1${NC}"
}

print_info() {
    echo -e "${BLUE}ℹ️  $1${NC}"
}

# Check if server is running
check_server() {
    print_section "1. Server Health Check"
    
    print_test "Health endpoint"
    if curl -s -f "$SERVER_URL/health" > /dev/null; then
        print_success "Server is running at $SERVER_URL"
    else
        print_error "Server is not running! Start with: go run cmd/git-executor/main.go --config=$CONFIG_FILE"
        exit 1
    fi
    
    print_test "API root endpoint"
    curl -s "$SERVER_URL/repos/" | head -3
    print_success "API root accessible"
}

# Test 1: Git Smart HTTP info/refs endpoint
test_info_refs() {
    print_section "2. Git Smart HTTP: info/refs Endpoint"
    
    print_test "Valid info/refs request (with authentication)"
    echo "Command:"
    echo "curl -v -H 'X-USER-ID: $TEST_USER' '$SERVER_URL/repos/$TEST_OWNER/$TEST_REPO/info/refs?service=git-upload-pack'"
    echo ""
    
    RESPONSE=$(curl -s -w "HTTPSTATUS:%{http_code}" \
        -H "X-USER-ID: $TEST_USER" \
        "$SERVER_URL/repos/$TEST_OWNER/$TEST_REPO/info/refs?service=git-upload-pack")
    
    HTTP_STATUS=$(echo $RESPONSE | tr -d '\n' | sed -e 's/.*HTTPSTATUS://')
    BODY=$(echo $RESPONSE | sed -e 's/HTTPSTATUS:.*//g')
    
    echo "Response Status: $HTTP_STATUS"
    echo "Response Body (first 200 chars):"
    echo "$BODY" | head -c 200
    echo ""
    
    if [ "$HTTP_STATUS" = "200" ]; then
        print_success "Info/refs endpoint working (status 200)"
        # Check if response contains Git protocol headers
        if echo "$BODY" | grep -q "service=git-upload-pack"; then
            print_success "Git protocol service header present"
        fi
    elif [ "$HTTP_STATUS" = "404" ]; then
        print_info "Repository not found (expected if no test repo exists)"
    else
        print_error "Unexpected status: $HTTP_STATUS"
    fi
    
    print_test "Missing authentication"
    UNAUTH_STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
        "$SERVER_URL/repos/$TEST_OWNER/$TEST_REPO/info/refs?service=git-upload-pack")
    
    if [ "$UNAUTH_STATUS" = "401" ]; then
        print_success "Properly rejects unauthenticated requests (401)"
    else
        print_error "Should return 401 for missing auth, got: $UNAUTH_STATUS"
    fi
    
    print_test "Invalid service parameter"
    INVALID_STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
        -H "X-USER-ID: $TEST_USER" \
        "$SERVER_URL/repos/$TEST_OWNER/$TEST_REPO/info/refs?service=invalid-service")
    
    if [ "$INVALID_STATUS" = "400" ]; then
        print_success "Properly rejects invalid service parameter (400)"
    else
        print_error "Should return 400 for invalid service, got: $INVALID_STATUS"
    fi
}

# Test 2: Git Smart HTTP upload-pack endpoint
test_upload_pack() {
    print_section "3. Git Smart HTTP: git-upload-pack Endpoint"
    
    print_test "Basic upload-pack request (POST)"
    echo "Command:"
    echo "curl -v -X POST -H 'X-USER-ID: $TEST_USER' -H 'Content-Type: application/x-git-upload-pack-request' '$SERVER_URL/repos/$TEST_OWNER/$TEST_REPO/git-upload-pack'"
    echo ""
    
    # Create a simple Git protocol request
    # This simulates what a Git client would send
    GIT_REQUEST="0032want 0000000000000000000000000000000000000000 multi_ack_detailed side-band-64k thin-pack ofs-delta
0000"
    
    RESPONSE=$(curl -s -w "HTTPSTATUS:%{http_code}" \
        -X POST \
        -H "X-USER-ID: $TEST_USER" \
        -H "Content-Type: application/x-git-upload-pack-request" \
        -d "$GIT_REQUEST" \
        "$SERVER_URL/repos/$TEST_OWNER/$TEST_REPO/git-upload-pack")
    
    HTTP_STATUS=$(echo $RESPONSE | tr -d '\n' | sed -e 's/.*HTTPSTATUS://')
    BODY=$(echo $RESPONSE | sed -e 's/HTTPSTATUS:.*//g')
    
    echo "Response Status: $HTTP_STATUS"
    echo "Response Body (first 200 chars):"
    echo "$BODY" | head -c 200
    echo ""
    
    if [ "$HTTP_STATUS" = "200" ]; then
        print_success "Upload-pack endpoint accessible (status 200)"
    elif [ "$HTTP_STATUS" = "404" ]; then
        print_info "Repository not found (expected if no test repo exists)"
    elif [ "$HTTP_STATUS" = "400" ]; then
        print_info "Bad request (expected with dummy Git request)"
    else
        print_error "Unexpected status: $HTTP_STATUS"
    fi
    
    print_test "Missing content type"
    MISSING_CT_STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
        -X POST \
        -H "X-USER-ID: $TEST_USER" \
        -d "$GIT_REQUEST" \
        "$SERVER_URL/repos/$TEST_OWNER/$TEST_REPO/git-upload-pack")
    
    if [ "$MISSING_CT_STATUS" = "400" ]; then
        print_success "Properly validates content type (400)"
    else
        print_error "Should return 400 for missing content type, got: $MISSING_CT_STATUS"
    fi
}

# Test 3: Error handling and protocol compliance
test_error_handling() {
    print_section "4. Error Handling & Protocol Compliance"
    
    print_test "Non-existent repository"
    NONEXIST_STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
        -H "X-USER-ID: $TEST_USER" \
        "$SERVER_URL/repos/nonexistent/repo/info/refs?service=git-upload-pack")
    
    if [ "$NONEXIST_STATUS" = "404" ]; then
        print_success "Properly handles non-existent repository (404)"
    else
        print_error "Should return 404 for non-existent repo, got: $NONEXIST_STATUS"
    fi
    
    print_test "Invalid repository path"
    INVALID_PATH_STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
        -H "X-USER-ID: $TEST_USER" \
        "$SERVER_URL/repos/../etc/passwd/info/refs?service=git-upload-pack")
    
    if [ "$INVALID_PATH_STATUS" = "400" ] || [ "$INVALID_PATH_STATUS" = "404" ]; then
        print_success "Properly handles invalid repository path ($INVALID_PATH_STATUS)"
    else
        print_error "Should return 400/404 for invalid path, got: $INVALID_PATH_STATUS"
    fi
    
    print_test "Content-Type validation"
    echo "Testing application/x-git-upload-pack-advertisement response..."
    CONTENT_TYPE=$(curl -s -I \
        -H "X-USER-ID: $TEST_USER" \
        "$SERVER_URL/repos/$TEST_OWNER/$TEST_REPO/info/refs?service=git-upload-pack" | \
        grep -i "content-type" | tr -d '\r')
    
    if echo "$CONTENT_TYPE" | grep -q "application/x-git-upload-pack-advertisement"; then
        print_success "Correct Content-Type header for info/refs"
    else
        print_info "Content-Type: $CONTENT_TYPE"
    fi
}

# Test 4: Streaming endpoints (if available)
test_streaming() {
    print_section "5. Streaming Endpoints (Optional)"
    
    print_test "Streaming info/refs endpoint"
    STREAMING_STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
        -H "X-USER-ID: $TEST_USER" \
        "$SERVER_URL/streaming/$TEST_OWNER/$TEST_REPO/info/refs?service=git-upload-pack" 2>/dev/null || echo "404")
    
    if [ "$STREAMING_STATUS" = "200" ]; then
        print_success "Streaming endpoints available"
    elif [ "$STREAMING_STATUS" = "404" ]; then
        print_info "Streaming endpoints not mounted (optional feature)"
    else
        print_info "Streaming endpoint status: $STREAMING_STATUS"
    fi
}

# Test 5: What about git diff?
explain_git_diff() {
    print_section "6. Git Diff Operations - What's Available?"
    
    print_info "Understanding Git Diff in Smart HTTP Protocol:"
    echo ""
    echo "❓ QUESTION: Can I call 'git diff' via API?"
    echo "📋 ANSWER: Git diff is NOT a direct Smart HTTP protocol operation."
    echo ""
    echo "🔧 WHAT WE HAVE:"
    echo "   ✅ GET  /repos/{owner}/{repo}/info/refs?service=git-upload-pack"
    echo "   ✅ POST /repos/{owner}/{repo}/git-upload-pack"
    echo "   ✅ These enable: git clone, git fetch, git pull"
    echo ""
    echo "🚧 FOR GIT DIFF, YOU WOULD NEED:"
    echo "   1. Clone/fetch the repository data using upload-pack"
    echo "   2. Perform diff operations locally, OR"
    echo "   3. Add custom API endpoints for diff operations (not part of Git Smart HTTP)"
    echo ""
    echo "💡 CUSTOM DIFF API (NOT YET IMPLEMENTED):"
    echo "   - GET /repos/{owner}/{repo}/diff?from={commit1}&to={commit2}"
    echo "   - GET /repos/{owner}/{repo}/diff?from={branch1}&to={branch2}"
    echo "   - This would be a CollabOps-specific extension"
    echo ""
    echo "🎯 CURRENT WORKFLOW FOR DIFF:"
    echo "   1. Use git-upload-pack to fetch repository data"
    echo "   2. Process diff operations in your application"
    echo "   3. Or implement custom diff endpoints in the gateway"
}

# Repository setup helper
setup_test_repo() {
    print_section "Setup Test Repository (Optional)"
    
    echo "To create a test repository for better testing:"
    echo ""
    echo "mkdir -p test-repos/$TEST_OWNER"
    echo "cd test-repos/$TEST_OWNER"
    echo "git clone --bare https://github.com/git/git.git $TEST_REPO"
    echo "cd ../.."
    echo ""
    echo "Then restart the server with repo_root pointing to ./test-repos"
}

# Main execution
main() {
    case "${1:-all}" in
        "health")
            check_server
            ;;
        "info-refs")
            check_server
            test_info_refs
            ;;
        "upload-pack")
            check_server
            test_upload_pack
            ;;
        "errors")
            check_server
            test_error_handling
            ;;
        "streaming")
            check_server
            test_streaming
            ;;
        "diff")
            explain_git_diff
            ;;
        "setup")
            setup_test_repo
            ;;
        "all")
            check_server
            test_info_refs
            test_upload_pack
            test_error_handling
            test_streaming
            explain_git_diff
            echo ""
            print_section "🎯 Manual Testing Complete!"
            echo "Run './scripts/manual-test-git-protocol.sh setup' for repository setup instructions."
            ;;
        *)
            echo "Usage: $0 [health|info-refs|upload-pack|errors|streaming|diff|setup|all]"
            exit 1
            ;;
    esac
}

# Run the specified test
main "$@" 