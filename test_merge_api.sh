#!/bin/bash

# Test merge API to see if our protection is working
echo "=== Testing Merge API Protection ==="

# Replace with your actual server URL and repo details
SERVER_URL="http://localhost:8080"  # Adjust this
OWNER="your-owner"                   # Adjust this
REPO="your-repo"                     # Adjust this

curl -X POST "$SERVER_URL/api/v1/$OWNER/$REPO/merges" \
  -H "Content-Type: application/json" \
  -d '{
    "source_ref": "feature-branch",
    "target_ref": "main",
    "method": "squash",
    "strategy": "recursive",
    "force_conflict": true,
    "message": "Test merge with protection"
  }' | jq .

echo "=== Check logs for DEBUG messages ==="