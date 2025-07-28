#!/bin/bash
# Performance testing script for SSH protocol endpoints

set -e

EXECUTOR_URL="${EXECUTOR_URL:-http://localhost:8080}"
RESULTS_DIR="${RESULTS_DIR:-/results}"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)

mkdir -p "${RESULTS_DIR}"

echo "=== SSH Protocol Performance Testing ==="
echo "Executor URL: ${EXECUTOR_URL}"
echo "Results directory: ${RESULTS_DIR}"
echo

# Create test data files
echo -ne '0014command=ls-refs\n0000' > /tmp/upload-pack-small.bin
dd if=/dev/zero bs=1024 count=10 2>/dev/null | base64 > /tmp/upload-pack-medium.bin
dd if=/dev/zero bs=1024 count=100 2>/dev/null | base64 > /tmp/upload-pack-large.bin

# Test 1: Small request performance
echo "Test 1: Small request performance (1000 requests, 10 concurrent)"
ab -n 1000 -c 10 \
  -H 'X-USER-ID: test-user-123' \
  -H 'X-Source: ssh-gateway' \
  -H 'X-CORRELATION-ID: perf-small' \
  -p /tmp/upload-pack-small.bin \
  -T 'application/x-git-upload-pack' \
  "${EXECUTOR_URL}/ssh/test-owner/test-repo/upload-pack" \
  > "${RESULTS_DIR}/perf-small-${TIMESTAMP}.txt"

# Test 2: Concurrent connection stress test
echo "Test 2: Concurrent connection stress (5000 requests, 50 concurrent)"
ab -n 5000 -c 50 \
  -H 'X-USER-ID: test-user-123' \
  -H 'X-Source: ssh-gateway' \
  -H 'X-CORRELATION-ID: perf-concurrent' \
  -p /tmp/upload-pack-small.bin \
  -T 'application/x-git-upload-pack' \
  "${EXECUTOR_URL}/ssh/test-owner/test-repo/upload-pack" \
  > "${RESULTS_DIR}/perf-concurrent-${TIMESTAMP}.txt"

# Test 3: Medium payload performance
echo "Test 3: Medium payload performance (500 requests, 5 concurrent)"
ab -n 500 -c 5 \
  -H 'X-USER-ID: test-user-123' \
  -H 'X-Source: ssh-gateway' \
  -H 'X-CORRELATION-ID: perf-medium' \
  -p /tmp/upload-pack-medium.bin \
  -T 'application/x-git-upload-pack' \
  "${EXECUTOR_URL}/ssh/test-owner/test-repo/upload-pack" \
  > "${RESULTS_DIR}/perf-medium-${TIMESTAMP}.txt"

# Test 4: Large payload performance
echo "Test 4: Large payload performance (100 requests, 2 concurrent)"
ab -n 100 -c 2 \
  -H 'X-USER-ID: test-user-123' \
  -H 'X-Source: ssh-gateway' \
  -H 'X-CORRELATION-ID: perf-large' \
  -p /tmp/upload-pack-large.bin \
  -T 'application/x-git-upload-pack' \
  "${EXECUTOR_URL}/ssh/test-owner/test-repo/upload-pack" \
  > "${RESULTS_DIR}/perf-large-${TIMESTAMP}.txt"

# Test 5: Receive-pack performance
echo "Test 5: Receive-pack performance (1000 requests, 10 concurrent)"
echo -ne '0000' > /tmp/receive-pack-empty.bin
ab -n 1000 -c 10 \
  -H 'X-USER-ID: test-user-123' \
  -H 'X-Source: ssh-gateway' \
  -H 'X-CORRELATION-ID: perf-receive' \
  -p /tmp/receive-pack-empty.bin \
  -T 'application/x-git-receive-pack' \
  "${EXECUTOR_URL}/ssh/test-owner/test-repo/receive-pack" \
  > "${RESULTS_DIR}/perf-receive-${TIMESTAMP}.txt"

# Generate summary report
echo "Generating performance summary..."
cat > "${RESULTS_DIR}/performance-summary-${TIMESTAMP}.json" << EOF
{
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "executor_url": "${EXECUTOR_URL}",
  "tests": {
    "small_requests": {
      "file": "perf-small-${TIMESTAMP}.txt",
      "requests": 1000,
      "concurrency": 10
    },
    "concurrent_stress": {
      "file": "perf-concurrent-${TIMESTAMP}.txt",
      "requests": 5000,
      "concurrency": 50
    },
    "medium_payload": {
      "file": "perf-medium-${TIMESTAMP}.txt",
      "requests": 500,
      "concurrency": 5
    },
    "large_payload": {
      "file": "perf-large-${TIMESTAMP}.txt",
      "requests": 100,
      "concurrency": 2
    },
    "receive_pack": {
      "file": "perf-receive-${TIMESTAMP}.txt",
      "requests": 1000,
      "concurrency": 10
    }
  }
}
EOF

# Extract key metrics
echo
echo "=== Performance Summary ==="
for file in "${RESULTS_DIR}"/perf-*-${TIMESTAMP}.txt; do
  if [ -f "$file" ]; then
    echo
    echo "File: $(basename $file)"
    grep "Requests per second:" "$file" || true
    grep "Time per request:" "$file" | head -n1 || true
    grep "Transfer rate:" "$file" || true
  fi
done

echo
echo "Performance testing complete. Results saved to ${RESULTS_DIR}" 