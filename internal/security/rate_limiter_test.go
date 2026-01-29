package security

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func rateLimiterForTest(t *testing.T, cfg *RateLimiterConfig) *RateLimiter {
	t.Helper()
	if cfg == nil {
		cfg = DefaultRateLimiterConfig()
		cfg.WhitelistedIPs = nil
	}
	logger := &logging.Logger{Logger: zaptest.NewLogger(t)}
	rl, err := NewRateLimiter(cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { rl.Stop() })
	return rl
}

// --- 12.3 Rate Limiting ---

// Test_RateLimit_UnderLimit_Allows 제한 이하 요청 허용
func Test_RateLimit_UnderLimit_Allows(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.PerIPRPS = 10
	cfg.PerIPBurstSize = 5
	cfg.WhitelistedIPs = nil
	rl := rateLimiterForTest(t, cfg)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(rl.RateLimitMiddleware())
	r.GET("/ping", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

// Test_RateLimit_OverLimit_Returns429 제한 초과 시 429
func Test_RateLimit_OverLimit_Returns429(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.PerIPRPS = 100
	cfg.PerIPBurstSize = 2
	cfg.WhitelistedIPs = nil
	rl := rateLimiterForTest(t, cfg)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(rl.RateLimitMiddleware())
	r.GET("/ping", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.RemoteAddr = "192.0.2.2:12345"
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if i < 2 {
			require.Equal(t, http.StatusOK, rec.Code, "request %d", i)
		} else {
			require.Equal(t, http.StatusTooManyRequests, rec.Code)
		}
	}
}

// --- 13.1C 503/429/499 (test_cases.md) ---

// Test_RateLimit_Returns429_RATE_LIMITED_WithRetryAfter rate limit 시 429, Retry-After 헤더
func Test_RateLimit_Returns429_RATE_LIMITED_WithRetryAfter(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.PerIPRPS = 100
	cfg.PerIPBurstSize = 2
	cfg.WhitelistedIPs = nil
	rl := rateLimiterForTest(t, cfg)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(rl.RateLimitMiddleware())
	r.GET("/ping", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.RemoteAddr = "192.0.2.3:12345"
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if i >= 2 {
			require.Equal(t, http.StatusTooManyRequests, rec.Code)
			require.NotEmpty(t, rec.Header().Get("Retry-After"), "Retry-After header required for 429")
			return
		}
	}
}
