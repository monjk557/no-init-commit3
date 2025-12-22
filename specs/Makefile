.PHONY: all generate fetch fix help mock mock-stop

# Default target
all: generate

# Generate OpenAPI spec from Go code
generate:
	@echo "🔍 Generating OpenAPI spec from Go schemas and handlers..."
	@if [ -f "go.mod" ]; then \
		go run specs/generate.go; \
	elif [ -f "../go.mod" ]; then \
		cd .. && go run specs/generate.go; \
	else \
		echo "Error: Could not find go.mod. Please run from project root."; \
		exit 1; \
	fi
	@echo "✅ OpenAPI spec generated successfully!"

# Fetch (placeholder for future use)
fetch:
	@echo "📥 Fetch operation (not implemented)"

# Fix (placeholder for future use)
fix:
	@echo "🔧 Fix operation (not implemented)"

# Mock server using Prism
mock:
	@echo "🚀 Starting Prism mock server..."
	@echo "📄 Using OpenAPI spec: generated/collabops-git-executor.yaml"
	@echo "🌐 Mock server will be available at: http://localhost:4010"
	@echo "📖 API docs: http://localhost:4010/api-docs/swagger/index.html"
	@echo ""
	@echo "Press Ctrl+C to stop the mock server"
	@if ! command -v npx >/dev/null 2>&1; then \
		echo "❌ Error: npx (Node.js) is required but not found."; \
		echo "   Please install Node.js: https://nodejs.org/"; \
		exit 1; \
	fi
	@npx --yes @stoplight/prism@latest mock generated/collabops-git-executor.yaml --port 4010 --host 0.0.0.0

# Stop mock server (helper command)
mock-stop:
	@echo "🛑 Stopping Prism mock server..."
	@pkill -f "prism mock" || echo "No mock server running"

# Help
help:
	@echo "OpenAPI Specs Management"
	@echo ""
	@echo "Available targets:"
	@echo "  make generate  - Generate OpenAPI spec from Go code"
	@echo "  make fetch     - Fetch external specs (not implemented)"
	@echo "  make fix       - Fix spec issues (not implemented)"
	@echo "  make mock      - Start Prism mock server for API testing"
	@echo "  make mock-stop - Stop Prism mock server"
	@echo "  make help      - Show this help message"
