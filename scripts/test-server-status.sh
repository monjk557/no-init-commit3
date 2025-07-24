#!/bin/bash

# Quick server status test script
echo "🧪 Testing Git Executor Server Status"
echo "===================================="

SERVER_URL="http://localhost:9091"

echo ""
echo "1. Testing Health Endpoint..."
response=$(curl -s -w "HTTP_STATUS:%{http_code}" "$SERVER_URL/health")
http_status=$(echo "$response" | grep -o "HTTP_STATUS:[0-9]*" | cut -d: -f2)
body=$(echo "$response" | sed 's/HTTP_STATUS:[0-9]*$//')

if [ "$http_status" = "200" ]; then
    echo "✅ Health check passed: $body"
else
    echo "❌ Health check failed (HTTP $http_status)"
    exit 1
fi

echo ""
echo "2. Testing API Root..."
response=$(curl -s -w "HTTP_STATUS:%{http_code}" "$SERVER_URL/")
http_status=$(echo "$response" | grep -o "HTTP_STATUS:[0-9]*" | cut -d: -f2)
body=$(echo "$response" | sed 's/HTTP_STATUS:[0-9]*$//')

if [ "$http_status" = "200" ]; then
    echo "✅ API root passed: $body"
else
    echo "❌ API root failed (HTTP $http_status)"
    exit 1
fi

echo ""
echo "3. Testing Git Smart HTTP info/refs..."
response=$(curl -s -w "HTTP_STATUS:%{http_code}" "$SERVER_URL/testowner/testrepo/info/refs?service=git-upload-pack" -H "X-USER-ID: test-user-123")
http_status=$(echo "$response" | grep -o "HTTP_STATUS:[0-9]*" | cut -d: -f2)

if [ "$http_status" = "200" ]; then
    echo "✅ Git Smart HTTP info/refs working"
else
    echo "⚠️  Git Smart HTTP info/refs returned HTTP $http_status (expected - may need test repo setup)"
fi

echo ""
echo "🎉 Server is operational on $SERVER_URL!"
echo ""
echo "Manual testing commands:"
echo "curl $SERVER_URL/health"
echo "curl $SERVER_URL/"
echo "curl \"$SERVER_URL/testowner/testrepo/info/refs?service=git-upload-pack\" -H \"X-USER-ID: test-user-123\"" 