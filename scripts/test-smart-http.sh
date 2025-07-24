#!/bin/bash
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}🧪 Smart HTTP Protocol Manual Test Suite${NC}"
echo "========================================="

# Configuration
SERVER_URL="http://localhost:9090"
TEST_OWNER="testowner"
TEST_REPO="testrepo"
TEST_USER_ID="test-user-123"

# Function to check if server is running
check_server() {
    echo -e "${YELLOW}🔍 Checking if server is running...${NC}"
    if curl -s "$SERVER_URL/health" > /dev/null 2>&1; then
        echo -e "${GREEN}✅ Server is running at $SERVER_URL${NC}"
        return 0
    else
        echo -e "${RED}❌ Server is not running. Please start it first with:${NC}"
        echo "   go run ./cmd/git-executor/"
        return 1
    fi
}

# Function to test API status
test_api_status() {
    echo -e "\n${YELLOW}📡 Testing API Status...${NC}"
    echo "curl -s $SERVER_URL/"
    
    response=$(curl -s "$SERVER_URL/" | jq . 2>/dev/null || curl -s "$SERVER_URL/")
    echo -e "${GREEN}Response:${NC} $response"
}

# Function to test info/refs endpoint (without authentication - should fail)
test_info_refs_no_auth() {
    echo -e "\n${YELLOW}🔒 Testing info/refs without authentication (should fail)...${NC}"
    echo "curl -i -X GET '$SERVER_URL/$TEST_OWNER/$TEST_REPO/info/refs?service=git-upload-pack'"
    
    echo -e "${BLUE}Response:${NC}"
    curl -i -X GET "$SERVER_URL/$TEST_OWNER/$TEST_REPO/info/refs?service=git-upload-pack" 2>/dev/null || true
    echo ""
}

# Function to test info/refs endpoint (with mock authentication)
test_info_refs_with_auth() {
    echo -e "\n${YELLOW}🔑 Testing info/refs with mock authentication...${NC}"
    echo "curl -i -X GET -H 'X-USER-ID: $TEST_USER_ID' '$SERVER_URL/$TEST_OWNER/$TEST_REPO/info/refs?service=git-upload-pack'"
    
    echo -e "${BLUE}Response:${NC}"
    curl -i -X GET \
        -H "X-USER-ID: $TEST_USER_ID" \
        "$SERVER_URL/$TEST_OWNER/$TEST_REPO/info/refs?service=git-upload-pack" 2>/dev/null || true
    echo ""
}

# Function to test invalid service parameter
test_invalid_service() {
    echo -e "\n${YELLOW}❌ Testing invalid service parameter...${NC}"
    echo "curl -i -X GET -H 'X-USER-ID: $TEST_USER_ID' '$SERVER_URL/$TEST_OWNER/$TEST_REPO/info/refs?service=invalid-service'"
    
    echo -e "${BLUE}Response:${NC}"
    curl -i -X GET \
        -H "X-USER-ID: $TEST_USER_ID" \
        "$SERVER_URL/$TEST_OWNER/$TEST_REPO/info/refs?service=invalid-service" 2>/dev/null || true
    echo ""
}

# Function to test upload-pack endpoint (not yet implemented)
test_upload_pack() {
    echo -e "\n${YELLOW}📦 Testing git-upload-pack endpoint (should return not implemented)...${NC}"
    echo "curl -i -X POST -H 'X-USER-ID: $TEST_USER_ID' '$SERVER_URL/$TEST_OWNER/$TEST_REPO/git-upload-pack'"
    
    echo -e "${BLUE}Response:${NC}"
    curl -i -X POST \
        -H "X-USER-ID: $TEST_USER_ID" \
        -H "Content-Type: application/x-git-upload-pack-request" \
        "$SERVER_URL/$TEST_OWNER/$TEST_REPO/git-upload-pack" 2>/dev/null || true
    echo ""
}

# Function to create a test repository
create_test_repo() {
    echo -e "\n${YELLOW}📁 Creating test repository structure...${NC}"
    
    # Create repository directory structure
    REPO_ROOT="./test-repos"
    REPO_PATH="$REPO_ROOT/$TEST_OWNER/$TEST_REPO.git"
    
    echo "Creating: $REPO_PATH"
    mkdir -p "$REPO_PATH"
    
    # Create basic Git repository structure
    mkdir -p "$REPO_PATH/objects"
    mkdir -p "$REPO_PATH/refs/heads"
    mkdir -p "$REPO_PATH/refs/tags"
    
    # Create HEAD file
    echo "ref: refs/heads/main" > "$REPO_PATH/HEAD"
    
    # Create basic config
    cat > "$REPO_PATH/config" << EOF
[core]
	repositoryformatversion = 0
	filemode = true
	bare = true
EOF
    
    echo -e "${GREEN}✅ Test repository created at: $REPO_PATH${NC}"
    echo "   You can now set GIT_REPO_ROOT=$REPO_ROOT in your server config"
}

# Function to simulate gateway request
test_gateway_simulation() {
    echo -e "\n${YELLOW}🌐 Simulating Gateway-Forwarded Request...${NC}"
    echo "This simulates how collabops-git-http gateway would forward requests"
    
    # Headers that the gateway would typically forward
    echo "curl -i -X GET \\"
    echo "  -H 'X-USER-ID: $TEST_USER_ID' \\"
    echo "  -H 'X-Forwarded-For: 192.168.1.100' \\"
    echo "  -H 'X-Request-ID: req-12345' \\"
    echo "  '$SERVER_URL/$TEST_OWNER/$TEST_REPO/info/refs?service=git-upload-pack'"
    
    echo -e "${BLUE}Response:${NC}"
    curl -i -X GET \
        -H "X-USER-ID: $TEST_USER_ID" \
        -H "X-Forwarded-For: 192.168.1.100" \
        -H "X-Request-ID: req-12345" \
        "$SERVER_URL/$TEST_OWNER/$TEST_REPO/info/refs?service=git-upload-pack" 2>/dev/null || true
    echo ""
}

# Function to show protocol analysis
analyze_protocol() {
    echo -e "\n${YELLOW}🔬 Git Smart HTTP Protocol Analysis...${NC}"
    echo "The response should contain:"
    echo "  1. Content-Type: application/x-git-upload-pack-advertisement"
    echo "  2. Service advertisement: # service=git-upload-pack"
    echo "  3. Flush packet: 0000"
    echo "  4. References with capabilities"
    echo "  5. Final null packet: 0000"
    echo ""
    echo "Git protocol packet format:"
    echo "  - 4-byte hex length + data"
    echo "  - 0000 = flush/end packet"
    echo "  - Capabilities separated by \\x00"
}

# Main execution
main() {
    echo "Starting Smart HTTP Protocol tests..."
    echo ""
    
    # Check if jq is available for JSON formatting
    if ! command -v jq &> /dev/null; then
        echo -e "${YELLOW}⚠️  Note: 'jq' not found. JSON responses won't be formatted.${NC}"
    fi
    
    # Run tests
    if check_server; then
        test_api_status
        test_info_refs_no_auth
        test_info_refs_with_auth
        test_invalid_service
        test_upload_pack
        test_gateway_simulation
        analyze_protocol
        
        echo -e "\n${GREEN}🎉 Manual testing complete!${NC}"
        echo ""
        echo -e "${BLUE}Next steps:${NC}"
        echo "1. Create test repositories with: $0 --create-repo"
        echo "2. Configure server with GIT_REPO_ROOT=./test-repos"
        echo "3. Start server: go run ./cmd/git-executor/"
        echo "4. Re-run tests: $0"
    fi
}

# Handle command line arguments
case "${1:-}" in
    --create-repo)
        create_test_repo
        ;;
    --help|-h)
        echo "Usage: $0 [--create-repo] [--help]"
        echo ""
        echo "  --create-repo  Create test repository structure"
        echo "  --help         Show this help message"
        ;;
    *)
        main
        ;;
esac 