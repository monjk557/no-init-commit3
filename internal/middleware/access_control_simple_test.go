package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestAccessControlMiddleware_CoreFunctionality(t *testing.T) {
	// Create simple logger for testing
	zapLogger := zaptest.NewLogger(t)
	logger := &logging.Logger{Logger: zapLogger}

	middleware, err := NewAccessControlMiddleware(nil, logger)
	require.NoError(t, err)

	// Create a simple test handler
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("success"))
	})

	// Wrap with middleware
	handler := middleware.Middleware(testHandler)

	tests := []struct {
		name           string
		path           string
		sourceHeader   string
		expectedStatus int
		shouldSucceed  bool
	}{
		{
			name:           "Valid HTTP Gateway to Git Protocol",
			path:           "/owner/repo/info/refs",
			sourceHeader:   "http-gateway",
			expectedStatus: http.StatusOK,
			shouldSucceed:  true,
		},
		{
			name:           "Valid Orchestrator to REST API",
			path:           "/repos/owner/repo/tree",
			sourceHeader:   "orchestrator",
			expectedStatus: http.StatusOK,
			shouldSucceed:  true,
		},
		{
			name:           "Valid SSH Gateway to SSH Protocol",
			path:           "/ssh/upload-pack",
			sourceHeader:   "ssh-gateway",
			expectedStatus: http.StatusOK,
			shouldSucceed:  true,
		},
		{
			name:           "Missing X-Source header",
			path:           "/health",
			sourceHeader:   "",
			expectedStatus: http.StatusForbidden,
			shouldSucceed:  false,
		},
		{
			name:           "Invalid X-Source header",
			path:           "/health",
			sourceHeader:   "invalid-source",
			expectedStatus: http.StatusForbidden,
			shouldSucceed:  false,
		},
		{
			name:           "HTTP Gateway accessing REST API (denied)",
			path:           "/repos/owner/repo/tree",
			sourceHeader:   "http-gateway",
			expectedStatus: http.StatusForbidden,
			shouldSucceed:  false,
		},
		{
			name:           "Orchestrator accessing Git Protocol (denied)",
			path:           "/owner/repo/git-upload-pack",
			sourceHeader:   "orchestrator",
			expectedStatus: http.StatusForbidden,
			shouldSucceed:  false,
		},
		{
			name:           "SSH Gateway accessing Git Protocol (denied)",
			path:           "/owner/repo/info/refs",
			sourceHeader:   "ssh-gateway",
			expectedStatus: http.StatusForbidden,
			shouldSucceed:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create request
			req := httptest.NewRequest("GET", tt.path, nil)
			if tt.sourceHeader != "" {
				req.Header.Set("X-Source", tt.sourceHeader)
			}

			// Create response recorder
			rr := httptest.NewRecorder()

			// Execute request
			handler.ServeHTTP(rr, req)

			// Check status code
			assert.Equal(t, tt.expectedStatus, rr.Code)

			// Check response content
			if tt.shouldSucceed {
				assert.Contains(t, rr.Body.String(), "success")
			} else {
				assert.Contains(t, rr.Body.String(), "Access denied")
			}
		})
	}
}

func TestDefaultAccessControlMatrix(t *testing.T) {
	config := DefaultAccessControlConfig()
	assert.NotNil(t, config)
	assert.True(t, config.DenyByDefault)
	assert.True(t, config.LogSecurityEvents)

	// Verify specific rules exist
	ruleMap := make(map[string]bool)
	for _, rule := range config.Rules {
		key := string(rule.Source) + ":" + string(rule.Endpoint)
		ruleMap[key] = rule.Allowed
	}

	// HTTP Gateway rules
	assert.True(t, ruleMap["http-gateway:git-protocol"])
	assert.False(t, ruleMap["http-gateway:rest-api"])
	assert.True(t, ruleMap["http-gateway:common"])

	// SSH Gateway rules
	assert.True(t, ruleMap["ssh-gateway:ssh-protocol"])
	assert.False(t, ruleMap["ssh-gateway:git-protocol"])
	assert.True(t, ruleMap["ssh-gateway:common"])

	// Orchestrator rules
	assert.True(t, ruleMap["orchestrator:rest-api"])
	assert.False(t, ruleMap["orchestrator:git-protocol"])
	assert.True(t, ruleMap["orchestrator:common"])
}

func TestEndpointClassification(t *testing.T) {
	zapLogger := zaptest.NewLogger(t)
	logger := &logging.Logger{Logger: zapLogger}
	middleware, err := NewAccessControlMiddleware(nil, logger)
	require.NoError(t, err)

	tests := []struct {
		path     string
		expected EndpointType
	}{
		// Git Protocol endpoints
		{"/owner/repo/info/refs", EndpointGitProtocol},
		{"/testowner/testrepo/git-upload-pack", EndpointGitProtocol},
		{"/user/project/git-receive-pack", EndpointGitProtocol},

		// SSH Protocol endpoints
		{"/ssh/upload-pack", EndpointSSHProtocol},
		{"/ssh/receive-pack", EndpointSSHProtocol},

		// REST API endpoints
		{"/repos/owner/repo/tree", EndpointRESTAPI},
		{"/repos/owner/repo/blob", EndpointRESTAPI},
		{"/repos/owner/repo/commits", EndpointRESTAPI},
		{"/repos/owner/repo/diff", EndpointRESTAPI},

		// Common endpoints
		{"/health", EndpointCommon},
		{"/metrics", EndpointCommon},
		{"/", EndpointCommon},

		// Unknown endpoints
		{"/unknown/path", "unknown"},
	}

	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.path, "/", "_"), func(t *testing.T) {
			result := middleware.classifyEndpoint(tt.path)
			assert.Equal(t, tt.expected, result)
		})
	}
}
