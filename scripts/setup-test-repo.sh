#!/bin/bash
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}🏗️  Setting up Git Repository for Testing${NC}"
echo "==========================================="

# Get absolute paths
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$PROJECT_ROOT/test-repos"
OWNER="testowner"
REPO="testrepo"

# Function to show repository structure
show_structure() {
    echo -e "\n${YELLOW}📁 Expected Repository Structure:${NC}"
    echo "================================="
    echo "Repository Root: $REPO_ROOT"
    echo "└── {owner}/"
    echo "    └── {repo}.git/     <- Bare repository"
    echo ""
    echo "Example:"
    echo "$REPO_ROOT/"
    echo "├── testowner/"
    echo "│   └── testrepo.git/"
    echo "├── myorg/"
    echo "│   ├── backend.git/"
    echo "│   └── frontend.git/"
    echo "└── alice/"
    echo "    └── personal-project.git/"
}

# Function to create a test repository
create_test_repo() {
    local owner=$1
    local repo=$2
    
    REPO_PATH="$REPO_ROOT/$owner/$repo.git"
    
    echo -e "\n${BLUE}📦 Creating test repository: $owner/$repo${NC}"
    
    # Create directory structure
    mkdir -p "$REPO_PATH"
    
    # Initialize as bare repository
    cd "$REPO_PATH"
    git init --bare
    cd "$PROJECT_ROOT"
    
    echo -e "${GREEN}✅ Created bare repository at: $REPO_PATH${NC}"
    
    # Create a temporary working directory to add some content
    TEMP_DIR=$(mktemp -d)
    cd "$TEMP_DIR"
    
    # Clone the bare repo, add content, and push
    git clone "$REPO_PATH" working-repo
    cd working-repo
    
    # Configure git (needed for commits)
    git config user.name "Test User"
    git config user.email "test@example.com"
    
    # Add some test content
    echo "# $repo" > README.md
    echo "This is a test repository for CollabOps Git Executor" >> README.md
    echo "" >> README.md
    echo "Created: $(date)" >> README.md
    
    mkdir -p src
    echo 'console.log("Hello from CollabOps!");' > src/main.js
    echo '{"name": "test-repo", "version": "1.0.0"}' > package.json
    
    # Add and commit
    git add .
    git commit -m "Initial commit: Add test files"
    
    # Create a few more commits for testing
    echo "## Features" >> README.md
    echo "- Git Smart HTTP Protocol support" >> README.md
    git add README.md
    git commit -m "docs: Add features section"
    
    echo 'export default "CollabOps Git Executor";' > src/index.js
    git add src/index.js
    git commit -m "feat: Add main module export"
    
    # Push to bare repository
    git push origin main
    
    cd "$PROJECT_ROOT"
    rm -rf "$TEMP_DIR"
    
    echo -e "${GREEN}✅ Repository $owner/$repo ready with test commits!${NC}"
}

# Function to clone an existing repository
clone_existing_repo() {
    echo -e "\n${YELLOW}📥 To clone your existing repository (collabops-api-service):${NC}"
    echo "=============================================================="
    echo ""
    echo "1. Clone as bare repository:"
    echo "   mkdir -p $REPO_ROOT/CollabOps"
    echo "   git clone --bare https://github.com/CollabOps/collabops-api-service.git \\"
    echo "     $REPO_ROOT/CollabOps/collabops-api-service.git"
    echo ""
    echo "2. Then it will be accessible via our API:"
    echo "   GET  /repos/CollabOps/collabops-api-service/info/refs?service=git-upload-pack"
    echo "   POST /repos/CollabOps/collabops-api-service/git-upload-pack"
    echo ""
    echo "3. Or using streaming endpoints (better for large repos):"
    echo "   GET  /repos/streaming/CollabOps/collabops-api-service/info/refs?service=git-upload-pack"
    echo "   POST /repos/streaming/CollabOps/collabops-api-service/git-upload-pack"
}

# Function to show configuration options
show_config_options() {
    echo -e "\n${BLUE}⚙️  Configuration Options:${NC}"
    echo "=========================="
    echo ""
    echo "Current location: $REPO_ROOT"
    echo ""
    echo "To change the repository root, edit: config/config.yaml"
    echo "  git:"
    echo "    repo_root: \"/your/custom/path\""
}

# Main execution
main() {
    show_structure
    
    # Create test repository
    create_test_repo "$OWNER" "$REPO"
    
    # Show how to clone existing repos
    clone_existing_repo
    
    # Show configuration options
    show_config_options
    
    echo -e "\n${GREEN}🎉 Repository setup completed!${NC}"
    echo ""
    echo "Next steps:"
    echo "1. Start the server: make run"
    echo "2. Test the repositories: make test-manual-smart-http"
    echo "3. Test streaming: make test-manual-streaming"
    echo ""
    echo "Available repositories:"
    find "$REPO_ROOT" -name "*.git" -type d 2>/dev/null | sed "s|$REPO_ROOT/||" | sed 's|\.git||' | while read repo; do
        echo "  📁 $repo"
    done
}

# Handle command line arguments
case "${1:-}" in
    --owner)
        OWNER="$2"
        REPO="${3:-testrepo}"
        main
        ;;
    --help|-h)
        echo "Usage: $0 [--owner OWNER [REPO]] [--help]"
        echo ""
        echo "  --owner OWNER [REPO]  Create repository for specific owner/repo"
        echo "  --help                Show this help message"
        echo ""
        echo "Examples:"
        echo "  $0                           # Create testowner/testrepo"
        echo "  $0 --owner alice myproject   # Create alice/myproject"
        ;;
    *)
        main
        ;;
esac
