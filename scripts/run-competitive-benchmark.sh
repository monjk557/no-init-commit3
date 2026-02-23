#!/bin/bash

# CollabOps Competitive Benchmarking Script
# Runs performance comparisons against GitLab, Gitea, and GitHub

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
RESULTS_DIR="$PROJECT_ROOT/benchmark-results"
REPOS_DIR="$PROJECT_ROOT/test-repos"

# Configuration
GITLAB_PORT=${GITLAB_PORT:-8080}
GITEA_PORT=${GITEA_PORT:-3000}
COLLABOPS_PORT=${COLLABOPS_PORT:-8090}

echo "🚀 Starting CollabOps Competitive Benchmarking Suite"
echo "================================================"
echo "Project Root: $PROJECT_ROOT"
echo "Results Dir:  $RESULTS_DIR"
echo "Test Repos:   $REPOS_DIR"
echo ""

# Create directories
mkdir -p "$RESULTS_DIR" "$REPOS_DIR"

# Function to check if service is ready
wait_for_service() {
    local service_name="$1"
    local port="$2"
    local max_attempts=30
    local attempt=1

    echo "⏳ Waiting for $service_name on port $port..."

    while [ $attempt -le $max_attempts ]; do
        if curl -s -f "http://localhost:$port" > /dev/null 2>&1; then
            echo "✅ $service_name is ready"
            return 0
        fi

        echo "   Attempt $attempt/$max_attempts - waiting 5s..."
        sleep 5
        ((attempt++))
    done

    echo "❌ $service_name failed to start within $(($max_attempts * 5)) seconds"
    return 1
}

# Function to generate test repositories
generate_test_repos() {
    echo "🏗️  Generating test repositories..."

    # Small scale repo
    echo "   Creating small-scale repository..."
    go run "$PROJECT_ROOT/cmd/benchmark-repo-generator" \
        --output "$REPOS_DIR/small-repo" \
        --commits 500 \
        --files 100 \
        --changes-per-commit 5

    # Medium scale repo
    echo "   Creating medium-scale repository..."
    go run "$PROJECT_ROOT/cmd/benchmark-repo-generator" \
        --output "$REPOS_DIR/medium-repo" \
        --commits 2000 \
        --files 1000 \
        --changes-per-commit 20

    # Large scale repo
    echo "   Creating large-scale repository..."
    go run "$PROJECT_ROOT/cmd/benchmark-repo-generator" \
        --output "$REPOS_DIR/large-repo" \
        --commits 10000 \
        --files 5000 \
        --changes-per-commit 50

    echo "✅ Test repositories generated"
}

# Function to start services using Docker Compose
start_services() {
    echo "🐳 Starting services with Docker Compose..."

    cd "$PROJECT_ROOT"

    # Create docker-compose.yml if it doesn't exist
    if [ ! -f "docker-compose.benchmark.yml" ]; then
        cat > docker-compose.benchmark.yml << EOF
version: '3.8'
services:
  gitlab:
    image: gitlab/gitlab-ce:latest
    ports:
      - "${GITLAB_PORT}:80"
    environment:
      - GITLAB_OMNIBUS_CONFIG=external_url 'http://localhost:${GITLAB_PORT}'
    volumes:
      - gitlab-data:/var/opt/gitlab
      - ./test-repos:/test-repos:ro
    restart: unless-stopped

  gitea:
    image: gitea/gitea:latest
    ports:
      - "${GITEA_PORT}:3000"
    environment:
      - USER_UID=1000
      - USER_GID=1000
    volumes:
      - gitea-data:/data
      - ./test-repos:/test-repos:ro
    restart: unless-stopped

  collabops:
    build:
      context: .
      dockerfile: Dockerfile
    ports:
      - "${COLLABOPS_PORT}:8090"
    environment:
      - GIT_REPO_ROOT=/repos
    volumes:
      - ./test-repos:/repos:ro
    restart: unless-stopped

volumes:
  gitlab-data:
  gitea-data:
EOF
    fi

    # Start services
    docker-compose -f docker-compose.benchmark.yml up -d

    # Wait for services to be ready
    wait_for_service "CollabOps" "$COLLABOPS_PORT"
    wait_for_service "Gitea" "$GITEA_PORT"
    wait_for_service "GitLab" "$GITLAB_PORT"
}

# Function to run benchmarks
run_benchmarks() {
    echo "📊 Running competitive benchmarks..."

    local timestamp=$(date +%Y%m%d_%H%M%S)
    local result_file="$RESULTS_DIR/benchmark_results_$timestamp.json"

    # Run the benchmark suite
    go run "$PROJECT_ROOT/cmd/competitive-benchmark" \
        --gitlab-url "http://localhost:$GITLAB_PORT" \
        --gitea-url "http://localhost:$GITEA_PORT" \
        --collabops-url "http://localhost:$COLLABOPS_PORT" \
        --repos-dir "$REPOS_DIR" \
        --output "$result_file" \
        --concurrent-users 10 \
        --duration 60s

    echo "✅ Benchmark completed: $result_file"

    # Generate report
    echo "📈 Generating performance report..."
    go run "$PROJECT_ROOT/cmd/benchmark-report" \
        --input "$result_file" \
        --output "$RESULTS_DIR/report_$timestamp.html"

    echo "✅ Report generated: $RESULTS_DIR/report_$timestamp.html"
}

# Function to stop services
stop_services() {
    echo "🛑 Stopping services..."
    cd "$PROJECT_ROOT"
    docker-compose -f docker-compose.benchmark.yml down
}

# Function to show results
show_results() {
    echo ""
    echo "📋 Benchmark Results Summary"
    echo "=========================="

    if [ -d "$RESULTS_DIR" ]; then
        echo "Results directory: $RESULTS_DIR"
        echo "Available reports:"
        ls -la "$RESULTS_DIR"/*.html 2>/dev/null || echo "   No HTML reports found"
        ls -la "$RESULTS_DIR"/*.json 2>/dev/null || echo "   No JSON results found"
    else
        echo "No results directory found"
    fi
}

# Main execution
main() {
    case "${1:-run}" in
        "generate-repos")
            generate_test_repos
            ;;
        "start-services")
            start_services
            ;;
        "run-benchmarks")
            run_benchmarks
            ;;
        "stop-services")
            stop_services
            ;;
        "results")
            show_results
            ;;
        "run"|"")
            echo "🎯 Running full competitive benchmark suite..."
            generate_test_repos
            start_services
            run_benchmarks
            show_results
            echo ""
            echo "✨ Benchmark suite completed!"
            echo "   To stop services: $0 stop-services"
            echo "   To view results:  $0 results"
            ;;
        "help"|"--help"|"-h")
            cat << EOF
CollabOps Competitive Benchmarking Script

Usage: $0 [command]

Commands:
  run              Run full benchmark suite (default)
  generate-repos   Generate test repositories only
  start-services   Start Docker services only
  run-benchmarks   Run benchmarks only (requires services)
  stop-services    Stop Docker services
  results          Show benchmark results
  help            Show this help message

Environment Variables:
  GITLAB_PORT      GitLab CE port (default: 8080)
  GITEA_PORT       Gitea port (default: 3000)
  COLLABOPS_PORT   CollabOps port (default: 8090)

Examples:
  $0                    # Run full benchmark suite
  $0 generate-repos     # Just generate test repositories
  $0 start-services     # Just start services
  $0 results           # Show results summary
EOF
            ;;
        *)
            echo "❌ Unknown command: $1"
            echo "   Use '$0 help' for usage information"
            exit 1
            ;;
    esac
}

# Handle CTRL+C gracefully
trap 'echo -e "\n🛑 Interrupted by user"; stop_services; exit 1' INT TERM

# Run main function
main "$@"