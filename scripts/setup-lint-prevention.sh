#!/bin/bash

# Setup Lint Prevention for Go Projects
# This script configures your development environment to prevent lint errors

set -e

echo "🛠️  Setting up lint prevention for Go project..."

# Function to check if command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Function to install golangci-lint
install_golangci_lint() {
    echo "📦 Installing golangci-lint..."
    if command_exists curl; then
        curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin
    else
        go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
    fi
}

# Function to install goimports
install_goimports() {
    echo "📦 Installing goimports..."
    go install golang.org/x/tools/cmd/goimports@latest
}

# 1. Check and install required tools
echo "🔍 Checking required tools..."

if ! command_exists go; then
    echo "❌ Go is not installed. Please install Go first."
    exit 1
fi

# Install golangci-lint if not present
if ! command_exists golangci-lint && [ ! -f "$(go env GOPATH)/bin/golangci-lint" ]; then
    install_golangci_lint
else
    echo "✅ golangci-lint is available"
fi

# Install goimports if not present
if ! command_exists goimports && [ ! -f "$(go env GOPATH)/bin/goimports" ]; then
    install_goimports
else
    echo "✅ goimports is available"
fi

# 2. Set up git hooks
echo "🔗 Setting up git hooks..."
git config core.hooksPath .githooks
echo "✅ Git hooks configured to use .githooks directory"

# 3. Install Go tools for VS Code/Cursor
echo "🛠️  Installing Go development tools..."
go install -a github.com/go-delve/delve/cmd/dlv@latest
go install -a github.com/ramya-rao-a/go-outline@latest
go install -a github.com/cweill/gotests/...@latest
go install -a github.com/fatih/gomodifytags@latest
go install -a github.com/josharian/impl@latest
go install -a honnef.co/go/tools/cmd/staticcheck@latest

# 4. Run initial checks
echo "🧪 Running initial quality checks..."
if make qa-check; then
    echo "✅ All quality checks passed!"
else
    echo "⚠️  Some quality issues found. Run 'make qa-fix' to auto-fix what's possible."
fi

echo ""
echo "🎉 Lint prevention setup complete!"
echo ""
echo "📋 What was configured:"
echo "  ✅ Git pre-commit hooks (.githooks/pre-commit)"
echo "  ✅ VS Code/Cursor settings (.vscode/settings.json)"
echo "  ✅ Cursor AI rules (.cursor/rules/go-lint-prevention.md)"
echo "  ✅ Quality assurance Makefile targets"
echo "  ✅ Go development tools installed"
echo ""
echo "🚀 Usage:"
echo "  • Before coding: Check existing issues with 'make qa-check'"
echo "  • During coding: VS Code/Cursor will show lint errors in real-time"
echo "  • Before commit: Git hook automatically runs checks"
echo "  • Quick fix: Run 'make qa-fix' to auto-fix formatting"
echo "  • Get help: Run 'make lint-errors' for detailed error explanations"
echo ""
echo "📖 For AI assistants (like Cursor):"
echo "  • Rules are in .cursor/rules/go-lint-prevention.md"
echo "  • Always handle error returns with '_ = function()' in tests"
echo "  • Use 'defer func() { _ = resource.Close() }()' for resource cleanup"
echo ""
echo "✨ Happy coding with lint-free Go! ✨"
