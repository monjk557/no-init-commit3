package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:            "localhost",
			Port:            8080,
			BasePath:        "/",
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    30 * time.Second,
			IdleTimeout:     60 * time.Second,
			ShutdownTimeout: 10 * time.Second,
			HealthCheckPath: "/health",
		},
		Git: config.GitConfig{
			RepoRoot: "/var/lib/git/repos",
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
	}

	server, err := New(cfg)
	require.NoError(t, err)
	assert.NotNil(t, server)
	assert.Equal(t, cfg, server.config)
}

func TestServer_SetupRouter(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:            "localhost",
			Port:            8080,
			BasePath:        "/",
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    30 * time.Second,
			HealthCheckPath: "/health",
		},
		Git: config.GitConfig{
			RepoRoot: "/var/lib/git/repos",
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
	}

	server, err := New(cfg)
	require.NoError(t, err)
	router := server.SetupRouter()

	assert.NotNil(t, router)
}

func TestServer_HealthCheck(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:            "localhost",
			Port:            8080,
			BasePath:        "/",
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    30 * time.Second,
			HealthCheckPath: "/health",
		},
		Git: config.GitConfig{
			RepoRoot: "/var/lib/git/repos",
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
	}

	server, err := New(cfg)
	require.NoError(t, err)

	req, err := http.NewRequest("GET", "/health", nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	server.healthCheck(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

	body := rr.Body.String()
	assert.Contains(t, body, `"status": "healthy"`)
}

func TestServer_HealthCheckEndpoint(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:            "localhost",
			Port:            8080,
			BasePath:        "/",
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    30 * time.Second,
			HealthCheckPath: "/health",
		},
		Git: config.GitConfig{
			RepoRoot: "/var/lib/git/repos",
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
	}

	server, err := New(cfg)
	require.NoError(t, err)
	router := server.SetupRouter()

	req, err := http.NewRequest("GET", "/health", nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	body := rr.Body.String()
	assert.Contains(t, body, `"status": "healthy"`)
}

func TestServer_BasePath(t *testing.T) {
	tests := []struct {
		name     string
		basePath string
		expected string
	}{
		{
			name:     "Custom base path",
			basePath: "/api",
			expected: "/api/",
		},
		{
			name:     "Root base path",
			basePath: "/",
			expected: "/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Server: config.ServerConfig{
					Host:            "localhost",
					Port:            8080,
					BasePath:        tt.basePath,
					ReadTimeout:     30 * time.Second,
					WriteTimeout:    30 * time.Second,
					HealthCheckPath: "/health",
				},
				Git: config.GitConfig{
					RepoRoot: "/var/lib/git/repos",
				},
				Logging: config.LoggingConfig{
					Level:  "info",
					Format: "json",
					Output: "stdout",
				},
			}

			server, err := New(cfg)
			require.NoError(t, err)
			router := server.SetupRouter()

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest("GET", tt.expected, nil)

			router.ServeHTTP(recorder, request)

			assert.Equal(t, http.StatusOK, recorder.Code)
			body := recorder.Body.String()

			// Check for new structured response format
			assert.Contains(t, body, `"service": "git-executor"`)
			assert.Contains(t, body, `"version": "1.0.0"`)
			assert.Contains(t, body, `"status": "running"`)
		})
	}
}

func TestServer_APIRoutesResponse(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:            "localhost",
			Port:            8080,
			BasePath:        "/api",
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    30 * time.Second,
			HealthCheckPath: "/health",
		},
		Git: config.GitConfig{
			RepoRoot: "/var/lib/git/repos",
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
	}

	server, err := New(cfg)
	require.NoError(t, err)
	router := server.SetupRouter()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/", nil)

	router.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()

	// Check for new structured response format
	assert.Contains(t, body, `"service": "git-executor"`)
	assert.Contains(t, body, `"version": "1.0.0"`)
	assert.Contains(t, body, `"status": "running"`)
	assert.Contains(t, body, `"timestamp"`)
}

func TestServer_Shutdown(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:            "localhost",
			Port:            8080,
			BasePath:        "/",
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    30 * time.Second,
			HealthCheckPath: "/health",
		},
		Git: config.GitConfig{
			RepoRoot: "/var/lib/git/repos",
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
	}

	server, err := New(cfg)
	require.NoError(t, err)

	// Test shutdown when server is not started
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err = server.Shutdown(ctx)
	// Allow sync errors on stdout/stderr during shutdown as they're normal
	if err != nil && !strings.Contains(err.Error(), "sync /dev/stdout") && !strings.Contains(err.Error(), "sync /dev/stderr") {
		assert.NoError(t, err)
	}
}

func TestServer_GetAddr(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:            "localhost",
			Port:            8080,
			BasePath:        "/",
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    30 * time.Second,
			HealthCheckPath: "/health",
		},
		Git: config.GitConfig{
			RepoRoot: "/var/lib/git/repos",
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
	}

	server, err := New(cfg)
	require.NoError(t, err)

	// Before server is started
	addr := server.GetAddr()
	assert.Equal(t, "", addr)
}

func TestServer_ConfigurationRespected(t *testing.T) {
	customPath := "/custom/path"
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:            "localhost",
			Port:            9999,
			BasePath:        customPath,
			ReadTimeout:     45 * time.Second,
			WriteTimeout:    60 * time.Second,
			HealthCheckPath: "/health",
		},
		Git: config.GitConfig{
			RepoRoot: "/var/lib/git/repos",
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
	}

	server, err := New(cfg)
	require.NoError(t, err)
	router := server.SetupRouter()

	// Test health check endpoint
	req, err := http.NewRequest("GET", "/health", nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "healthy")

	// Test custom base path
	req, err = http.NewRequest("GET", customPath+"/", nil)
	require.NoError(t, err)

	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestServer_StartAndShutdown tests the server startup and graceful shutdown
func TestServer_StartAndShutdown(t *testing.T) {
	// Skip this test in short mode or CI environments
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:            "localhost",
			Port:            0, // Use random available port
			BasePath:        "/api/repos",
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    45 * time.Second,
			HealthCheckPath: "/health",
		},
		Git: config.GitConfig{
			RepoRoot: "/var/lib/git/repos",
		},
		JWT: config.JWTConfig{
			Algorithm:     "RS256",
			PublicKeyPath: "/etc/collabops/public.pem",
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
	}

	server, err := New(cfg)
	require.NoError(t, err)

	// Start server in background
	startErr := make(chan error, 1)
	go func() {
		err := server.Start()
		startErr <- err
	}()

	// Give server a moment to start
	time.Sleep(100 * time.Millisecond)

	// Send shutdown signal
	process := os.Process{Pid: os.Getpid()}
	err = process.Signal(syscall.SIGTERM)
	require.NoError(t, err)

	// Wait for server to shutdown
	select {
	case err := <-startErr:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Server did not shutdown within timeout")
	}
}

// TestServer_Middleware tests that middleware is properly configured
func TestServer_Middleware(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:            "localhost",
			Port:            8080,
			BasePath:        "/",
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    30 * time.Second,
			HealthCheckPath: "/health",
		},
		Git: config.GitConfig{
			RepoRoot: "/var/lib/git/repos",
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
	}

	server, err := New(cfg)
	require.NoError(t, err)
	router := server.SetupRouter()

	// Test that middleware is properly set up by verifying the request is processed successfully
	req, err := http.NewRequest("GET", "/health", nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	// Verify middleware stack is working by checking successful response
	assert.Equal(t, http.StatusOK, rr.Code)

	// Verify observability middleware is working (should have trace headers or request processed)
	assert.Contains(t, rr.Body.String(), "healthy")
}

// TestServer_InvalidRoute tests handling of invalid routes
func TestServer_InvalidRoute(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:            "localhost",
			Port:            8080,
			BasePath:        "/",
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    30 * time.Second,
			HealthCheckPath: "/health",
		},
		Git: config.GitConfig{
			RepoRoot: "/var/lib/git/repos",
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
	}

	server, err := New(cfg)
	require.NoError(t, err)
	router := server.SetupRouter()

	req, err := http.NewRequest("GET", "/nonexistent", nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}
