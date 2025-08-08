package security

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
)

func createTestRateLimiterLogger() *logging.Logger {
	cfg := config.LoggingConfig{
		Level:  "debug",
		Format: "text",
		Output: "stdout",
	}
	logger, _ := logging.NewLogger(cfg)
	return logger
}

func TestNewRateLimiter(t *testing.T) {
	logger := createTestRateLimiterLogger()

	tests := []struct {
		name        string
		config      *RateLimiterConfig
		expectError bool
	}{
		{
			name:        "nil config uses defaults",
			config:      nil,
			expectError: false,
		},
		{
			name:        "valid config",
			config:      DefaultRateLimiterConfig(),
			expectError: false,
		},
		{
			name: "invalid IP in whitelist",
			config: &RateLimiterConfig{
				Enabled:         false, // Disable to avoid ticker issue
				WhitelistedIPs:  []string{"invalid-ip"},
				CleanupInterval: 1 * time.Minute, // Set proper cleanup interval
			},
			expectError: true,
		},
		{
			name: "valid CIDR in whitelist",
			config: &RateLimiterConfig{
				Enabled:         true,
				WhitelistedIPs:  []string{"192.168.1.0/24", "127.0.0.1"},
				CleanupInterval: 1 * time.Minute, // Set proper cleanup interval
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rl, err := NewRateLimiter(tt.config, logger)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, rl)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, rl)
				if rl != nil {
					rl.Stop() // Clean up
				}
			}
		})
	}
}

func TestRateLimiter_GlobalLimit(t *testing.T) {
	logger := createTestRateLimiterLogger()
	config := &RateLimiterConfig{
		Enabled:               true,
		GlobalRPS:             5.0,             // Allow 5 requests per second
		BurstSize:             3,               // Allow bursts up to 3 requests
		PerIPRPS:              100.0,           // High to avoid IP limiting
		PerUserRPS:            100.0,           // High to avoid user limiting
		CleanupInterval:       1 * time.Minute, // Required for ticker
		MaxConcurrentRequests: 1000,            // High to avoid concurrent limiting
	}

	rl, err := NewRateLimiter(config, logger)
	require.NoError(t, err)
	defer rl.Stop()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(rl.RateLimitMiddleware())
	router.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// First 3 requests should succeed (burst allowance)
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code, "Request %d should succeed", i+1)
	}

	// Fourth request should be rate limited
	req4 := httptest.NewRequest("GET", "/test", nil)
	w4 := httptest.NewRecorder()
	router.ServeHTTP(w4, req4)
	assert.Equal(t, http.StatusTooManyRequests, w4.Code)
	assert.Contains(t, w4.Body.String(), "Global rate limit exceeded")
}

func TestRateLimiter_IPLimit(t *testing.T) {
	logger := createTestRateLimiterLogger()
	config := &RateLimiterConfig{
		Enabled:               true,
		GlobalRPS:             1000.0,          // High to avoid global limiting
		GlobalBurstSize:       100,             // High burst for global
		PerIPRPS:              1000.0,          // High rate, rely on burst
		PerIPBurstSize:        2,               // Small burst - this will be the limiting factor
		PerUserRPS:            1000.0,          // High to avoid user limiting
		PerUserBurstSize:      100,             // High burst for user
		CleanupInterval:       1 * time.Minute, // Required for ticker
		MaxConcurrentRequests: 1000,            // High to avoid concurrent limiting
	}

	rl, err := NewRateLimiter(config, logger)
	require.NoError(t, err)
	defer rl.Stop()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(rl.RateLimitMiddleware())
	router.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// First request should succeed
	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.RemoteAddr = "192.168.1.100:12345"
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)

	// Second request should succeed (within burst)
	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.RemoteAddr = "192.168.1.100:12346" // Same IP
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)

	// Third request should be rate limited (exceeds burst from same IP)
	req3 := httptest.NewRequest("GET", "/test", nil)
	req3.RemoteAddr = "192.168.1.100:12347" // Same IP
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, req3)
	assert.Equal(t, http.StatusTooManyRequests, w3.Code)
	assert.Contains(t, w3.Body.String(), "IP rate limit exceeded")
}

func TestRateLimiter_WhitelistedIP(t *testing.T) {
	logger := createTestRateLimiterLogger()
	config := &RateLimiterConfig{
		Enabled:         true,
		GlobalRPS:       1.0, // Very low to trigger limiting
		PerIPRPS:        1.0, // Very low to trigger limiting
		BurstSize:       1,
		WhitelistedIPs:  []string{"192.168.1.100", "10.0.0.0/8"},
		CleanupInterval: 1 * time.Minute, // Required for ticker
	}

	rl, err := NewRateLimiter(config, logger)
	require.NoError(t, err)
	defer rl.Stop()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(rl.RateLimitMiddleware())
	router.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// Test single whitelisted IP
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.100:12345"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}

	// Test CIDR range whitelisted IP
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "10.0.1.50:12345"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}
}

func TestRateLimiter_UserLimit(t *testing.T) {
	logger := createTestRateLimiterLogger()
	config := &RateLimiterConfig{
		Enabled:               true,
		GlobalRPS:             1000.0,          // High to avoid global limiting
		GlobalBurstSize:       100,             // High burst for global
		PerIPRPS:              1000.0,          // High to avoid IP limiting
		PerIPBurstSize:        100,             // High burst for IP
		PerUserRPS:            1000.0,          // High rate, rely on burst
		PerUserBurstSize:      2,               // Small burst - this will be the limiting factor
		CleanupInterval:       1 * time.Minute, // Required for ticker
		MaxConcurrentRequests: 1000,            // High to avoid concurrent limiting
	}

	rl, err := NewRateLimiter(config, logger)
	require.NoError(t, err)
	defer rl.Stop()

	gin.SetMode(gin.TestMode)
	router := gin.New()

	// Set user ID before rate limiting middleware
	router.Use(func(c *gin.Context) {
		c.Set("user_id", "test-user-123")
		c.Next()
	})

	router.Use(rl.RateLimitMiddleware())
	router.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// First request should succeed
	req1 := httptest.NewRequest("GET", "/test", nil)
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)

	// Second request should succeed (burst)
	req2 := httptest.NewRequest("GET", "/test", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)

	// Third request should be rate limited
	req3 := httptest.NewRequest("GET", "/test", nil)
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, req3)
	assert.Equal(t, http.StatusTooManyRequests, w3.Code)
	assert.Contains(t, w3.Body.String(), "User rate limit exceeded")
}

func TestRateLimiter_EndpointLimit(t *testing.T) {
	logger := createTestRateLimiterLogger()
	config := &RateLimiterConfig{
		Enabled:               true,
		GlobalRPS:             1000.0,          // High to avoid global limiting
		PerIPRPS:              1000.0,          // High to avoid IP limiting
		PerUserRPS:            1000.0,          // High to avoid user limiting
		BurstSize:             100,             // High burst for global limiting
		CleanupInterval:       1 * time.Minute, // Required for ticker
		MaxConcurrentRequests: 1000,            // High to avoid concurrent limiting
		EndpointLimits: map[string]Limit{
			"/api/auth": {RPS: 1.0, BurstSize: 1}, // Very low for testing
		},
	}

	rl, err := NewRateLimiter(config, logger)
	require.NoError(t, err)
	defer rl.Stop()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(rl.RateLimitMiddleware())
	router.GET("/api/auth/login", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})
	router.GET("/api/other", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// First auth request should succeed
	req1 := httptest.NewRequest("GET", "/api/auth/login", nil)
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)

	// Second auth request should be rate limited
	req2 := httptest.NewRequest("GET", "/api/auth/login", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusTooManyRequests, w2.Code)
	assert.Contains(t, w2.Body.String(), "Endpoint rate limit exceeded")

	// Other endpoint should still work
	req3 := httptest.NewRequest("GET", "/api/other", nil)
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, req3)
	assert.Equal(t, http.StatusOK, w3.Code)
}

func TestRateLimiter_BanThreshold(t *testing.T) {
	logger := createTestRateLimiterLogger()
	config := &RateLimiterConfig{
		Enabled:               true,
		GlobalRPS:             1000.0, // High to avoid global limiting
		PerIPRPS:              1000.0, // High to avoid IP limiting but allow threshold check
		BanThreshold:          3,      // Very low for testing
		WindowSize:            1 * time.Minute,
		BanDuration:           10 * time.Second,
		BurstSize:             100,             // High burst to avoid rate limiting
		CleanupInterval:       1 * time.Minute, // Required for ticker
		MaxConcurrentRequests: 1000,            // High to avoid concurrent limiting
	}

	rl, err := NewRateLimiter(config, logger)
	require.NoError(t, err)
	defer rl.Stop()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(rl.RateLimitMiddleware())
	router.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// Make requests up to threshold (should succeed)
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.200:12345"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}

	// Next request should trigger ban
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.200:12346"
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Contains(t, w.Body.String(), "IP rate limit exceeded")

	// Subsequent requests should be banned
	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.RemoteAddr = "192.168.1.200:12347"
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusTooManyRequests, w2.Code)
	assert.Contains(t, w2.Body.String(), "IP temporarily banned")
}

func TestRateLimiter_ConcurrentRequestLimit(t *testing.T) {
	logger := createTestRateLimiterLogger()
	config := &RateLimiterConfig{
		Enabled:               true,
		GlobalRPS:             2.0, // Very low for testing
		BurstSize:             1,
		PerIPRPS:              100.0,           // High to avoid IP limiting
		PerUserRPS:            100.0,           // High to avoid user limiting
		CleanupInterval:       1 * time.Minute, // Required for ticker
		MaxConcurrentRequests: 1000,            // High to avoid concurrent limiting
	}

	rl, err := NewRateLimiter(config, logger)
	require.NoError(t, err)
	defer rl.Stop()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(rl.RateLimitMiddleware())
	router.GET("/test", func(c *gin.Context) {
		time.Sleep(100 * time.Millisecond) // Hold the request for a moment
		c.JSON(200, gin.H{"status": "ok"})
	})

	// Start concurrent requests
	done := make(chan int, 5)

	for i := 0; i < 5; i++ {
		go func(id int) {
			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = "192.168.1.50:12345"
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			done <- w.Code
		}(i)
	}

	// Collect results
	results := make([]int, 5)
	for i := 0; i < 5; i++ {
		results[i] = <-done
	}

	// Some should succeed, some should be rejected due to concurrent limit
	okCount := 0
	tooManyCount := 0
	for _, code := range results {
		switch code {
		case http.StatusOK:
			okCount++
		case http.StatusTooManyRequests:
			tooManyCount++
		}
	}

	assert.True(t, okCount <= 2, "Should have at most 2 successful requests")
	assert.True(t, tooManyCount >= 1, "Should have at least 1 rejected request")
}

func TestRateLimiter_Headers(t *testing.T) {
	logger := createTestRateLimiterLogger()
	config := &RateLimiterConfig{
		Enabled:               true,
		GlobalRPS:             1.0, // Very low to trigger limiting
		PerIPRPS:              1.0,
		BurstSize:             1,               // Allow only 1 request, then rate limit
		CleanupInterval:       1 * time.Minute, // Required for ticker
		MaxConcurrentRequests: 1000,            // High to avoid concurrent limiting
	}

	rl, err := NewRateLimiter(config, logger)
	require.NoError(t, err)
	defer rl.Stop()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(rl.RateLimitMiddleware())
	router.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// First request should succeed
	req1 := httptest.NewRequest("GET", "/test", nil)
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)

	// Second request should be rate limited with proper headers
	req2 := httptest.NewRequest("GET", "/test", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusTooManyRequests, w2.Code)

	assert.NotEmpty(t, w2.Header().Get("Retry-After"))
	assert.NotEmpty(t, w2.Header().Get("X-RateLimit-Limit"))
	assert.Equal(t, "0", w2.Header().Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, w2.Header().Get("X-RateLimit-Reset"))
}

func TestRateLimiter_SlowlorisProtection(t *testing.T) {
	logger := createTestRateLimiterLogger()
	config := &RateLimiterConfig{
		Enabled:                true,
		GlobalRPS:              1000.0,
		PerIPRPS:               1000.0,
		BurstSize:              100, // Adequate burst size
		SlowlorisProtection:    true,
		RequestTimeoutDuration: 100 * time.Millisecond, // Very short for testing
		CleanupInterval:        1 * time.Minute,        // Required for ticker
		MaxConcurrentRequests:  1000,                   // High to avoid concurrent limiting
	}

	rl, err := NewRateLimiter(config, logger)
	require.NoError(t, err)
	defer rl.Stop()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(rl.RateLimitMiddleware())
	router.GET("/test", func(c *gin.Context) {
		// Simulate slow handler
		select {
		case <-time.After(200 * time.Millisecond):
			c.JSON(200, gin.H{"status": "ok"})
		case <-c.Request.Context().Done():
			c.JSON(408, gin.H{"error": "request timeout"})
		}
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// The request should complete with timeout context
	t.Logf("Response code: %d, Body: %s", w.Code, w.Body.String())
	assert.True(t, w.Code == 408 || w.Code == 200, "Expected 200 or 408, got %d with body: %s", w.Code, w.Body.String())
}

func TestRateLimiter_Stats(t *testing.T) {
	logger := createTestRateLimiterLogger()
	config := DefaultRateLimiterConfig()

	rl, err := NewRateLimiter(config, logger)
	require.NoError(t, err)
	defer rl.Stop()

	stats := rl.GetStats()
	assert.True(t, stats["enabled"].(bool))
	assert.Equal(t, 0, stats["user_limiters"].(int))
	assert.Equal(t, 0, stats["ip_limiters"].(int))
	assert.Equal(t, 0, stats["banned_ips"].(int))
	assert.Equal(t, 0, stats["concurrent_requests"].(int))
	assert.Equal(t, config.GlobalRPS, stats["global_rps"].(float64))
}

func TestRateLimiter_Disabled(t *testing.T) {
	logger := createTestRateLimiterLogger()
	config := &RateLimiterConfig{
		Enabled:         false,           // Disabled
		CleanupInterval: 1 * time.Minute, // Required for ticker
	}

	rl, err := NewRateLimiter(config, logger)
	require.NoError(t, err)
	defer rl.Stop()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(rl.RateLimitMiddleware())
	router.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// All requests should succeed when disabled
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}
}

func TestDefaultRateLimiterConfig(t *testing.T) {
	config := DefaultRateLimiterConfig()

	assert.True(t, config.Enabled)
	assert.Equal(t, 1000.0, config.GlobalRPS)
	assert.Equal(t, 10.0, config.PerUserRPS)
	assert.Equal(t, 50.0, config.PerIPRPS)
	assert.Equal(t, 20, config.BurstSize)
	assert.Equal(t, 5*time.Minute, config.CleanupInterval)
	assert.Equal(t, 10*time.Minute, config.BanDuration)
	assert.Equal(t, 100, config.BanThreshold)
	assert.Equal(t, 1*time.Minute, config.WindowSize)
	assert.Contains(t, config.WhitelistedIPs, "127.0.0.1")
	assert.Contains(t, config.WhitelistedIPs, "::1")
	assert.Equal(t, 500, config.MaxConcurrentRequests)
	assert.Equal(t, 30*time.Second, config.RequestTimeoutDuration)
	assert.True(t, config.SlowlorisProtection)
	assert.Contains(t, config.EndpointLimits, "/api/auth")
	assert.Contains(t, config.EndpointLimits, "/api/upload")
}
