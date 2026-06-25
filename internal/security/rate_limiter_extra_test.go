package security

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// 기존 rate_limiter_test.go 가 다루지 않는 경로를 채운다.

// --- 생성자 / 설정 ---

// Test_NewRateLimiter_NilConfig_UsesDefaults nil config 시 기본값
func Test_NewRateLimiter_NilConfig_UsesDefaults(t *testing.T) {
	logger := &logging.Logger{Logger: zaptest.NewLogger(t)}
	rl, err := NewRateLimiter(nil, logger)
	require.NoError(t, err)
	require.NotNil(t, rl)
	t.Cleanup(func() { rl.Stop() })
	require.True(t, rl.config.Enabled)
}

// Test_NewRateLimiter_InvalidWhitelistIP_Errors 잘못된 화이트리스트 IP 시 에러
func Test_NewRateLimiter_InvalidWhitelistIP_Errors(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.WhitelistedIPs = []string{"not-an-ip"}
	logger := &logging.Logger{Logger: zaptest.NewLogger(t)}
	rl, err := NewRateLimiter(cfg, logger)
	require.Error(t, err)
	require.Nil(t, rl)
}

// Test_NewRateLimiter_InvalidCIDR_Errors 잘못된 CIDR 시 에러
func Test_NewRateLimiter_InvalidCIDR_Errors(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.WhitelistedIPs = []string{"10.0.0.0/999"}
	logger := &logging.Logger{Logger: zaptest.NewLogger(t)}
	rl, err := NewRateLimiter(cfg, logger)
	require.Error(t, err)
	require.Nil(t, rl)
}

// Test_ParseWhitelistedIPs_CIDRAndIPv6 CIDR/IPv4/IPv6 파싱
func Test_ParseWhitelistedIPs_CIDRAndIPv6(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.WhitelistedIPs = []string{"10.0.0.0/8", "192.168.1.1", "::1", "fe80::/10"}
	rl := rateLimiterForTest(t, cfg)
	require.Len(t, rl.whitelistedNets, 4)
}

// --- burst size 폴백 ---

// Test_BurstSizeFallback_UsesDeprecatedField 신규 필드 0 시 BurstSize 폴백
func Test_BurstSizeFallback_UsesDeprecatedField(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.GlobalBurstSize = 0
	cfg.PerIPBurstSize = 0
	cfg.PerUserBurstSize = 0
	cfg.BurstSize = 42
	rl := rateLimiterForTest(t, cfg)

	require.Equal(t, 42, rl.getGlobalBurstSize())
	require.Equal(t, 42, rl.getIPBurstSize())
	require.Equal(t, 42, rl.getUserBurstSize())
}

// Test_BurstSizeFallback_UsesSpecificField 신규 필드 설정 시 우선
func Test_BurstSizeFallback_UsesSpecificField(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.GlobalBurstSize = 11
	cfg.PerIPBurstSize = 22
	cfg.PerUserBurstSize = 33
	rl := rateLimiterForTest(t, cfg)

	require.Equal(t, 11, rl.getGlobalBurstSize())
	require.Equal(t, 22, rl.getIPBurstSize())
	require.Equal(t, 33, rl.getUserBurstSize())
}

// --- getClientIP ---

func Test_GetClientIP_Sources(t *testing.T) {
	rl := rateLimiterForTest(t, nil)
	gin.SetMode(gin.TestMode)

	newCtx := func(setup func(*http.Request)) *gin.Context {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "203.0.113.5:9999"
		setup(req)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = req
		return c
	}

	// X-Forwarded-For 우선
	c := newCtx(func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "198.51.100.1, 10.0.0.1")
	})
	require.Equal(t, "198.51.100.1", rl.getClientIP(c))

	// X-Real-IP 차선
	c = newCtx(func(r *http.Request) {
		r.Header.Set("X-Real-IP", "198.51.100.2")
	})
	require.Equal(t, "198.51.100.2", rl.getClientIP(c))

	// RemoteAddr 폴백
	c = newCtx(func(r *http.Request) {})
	require.Equal(t, "203.0.113.5", rl.getClientIP(c))
}

// --- getUserID ---

func Test_GetUserID(t *testing.T) {
	rl := rateLimiterForTest(t, nil)
	gin.SetMode(gin.TestMode)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	// user_id 없음
	require.Equal(t, "", rl.getUserID(c))

	// 정상 string
	c.Set("user_id", "user-42")
	require.Equal(t, "user-42", rl.getUserID(c))

	// string 아닌 타입
	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	c2.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c2.Set("user_id", 12345)
	require.Equal(t, "", rl.getUserID(c2))
}

// --- isWhitelisted ---

func Test_IsWhitelisted(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.WhitelistedIPs = []string{"10.0.0.0/8"}
	rl := rateLimiterForTest(t, cfg)

	require.True(t, rl.isWhitelisted("10.1.2.3"))
	require.False(t, rl.isWhitelisted("192.168.0.1"))
	require.False(t, rl.isWhitelisted("not-an-ip"))
}

// Test_Middleware_WhitelistedIP_Bypasses 화이트리스트 IP는 우회
func Test_Middleware_WhitelistedIP_Bypasses(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.PerIPRPS = 1
	cfg.PerIPBurstSize = 1
	cfg.WhitelistedIPs = []string{"192.0.2.50"}
	rl := rateLimiterForTest(t, cfg)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(rl.RateLimitMiddleware())
	r.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })

	// 화이트리스트 IP 는 버스트 넘어서도 항상 통과
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.RemoteAddr = "192.0.2.50:1111"
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, "request %d", i)
	}
}

// Test_Middleware_Disabled_AllRequestsPass Enabled=false 시 전부 통과
func Test_Middleware_Disabled_AllRequestsPass(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.Enabled = false
	cfg.PerIPRPS = 1
	cfg.PerIPBurstSize = 1
	cfg.WhitelistedIPs = nil
	rl := rateLimiterForTest(t, cfg)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(rl.RateLimitMiddleware())
	r.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.RemoteAddr = "192.0.2.60:1111"
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
	}
}

// --- isIPBanned ---

func Test_IsIPBanned_NotBanned(t *testing.T) {
	rl := rateLimiterForTest(t, nil)
	require.False(t, rl.isIPBanned("203.0.113.9"))
}

func Test_IsIPBanned_ActiveAndExpired(t *testing.T) {
	rl := rateLimiterForTest(t, nil)

	// 활성 밴
	rl.mutex.Lock()
	rl.bannedIPs["1.2.3.4"] = time.Now().Add(time.Hour)
	rl.bannedIPs["5.6.7.8"] = time.Now().Add(-time.Hour) // 만료
	rl.mutex.Unlock()

	require.True(t, rl.isIPBanned("1.2.3.4"))
	// 만료된 밴은 false + 자동 삭제
	require.False(t, rl.isIPBanned("5.6.7.8"))
	rl.mutex.RLock()
	_, stillThere := rl.bannedIPs["5.6.7.8"]
	rl.mutex.RUnlock()
	require.False(t, stillThere, "만료된 밴은 삭제되어야 함")
}

// --- banIP + checkIPLimit 밴 임계치 ---

// Test_CheckIPLimit_BanThreshold_BansIP 임계치 초과 시 밴
func Test_CheckIPLimit_BanThreshold_BansIP(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.PerIPRPS = 100000
	cfg.PerIPBurstSize = 100000
	cfg.BanThreshold = 3
	cfg.WindowSize = time.Hour
	cfg.WhitelistedIPs = nil
	rl := rateLimiterForTest(t, cfg)

	ip := "203.0.113.77"
	// 임계치(3) 까지는 허용
	require.True(t, rl.checkIPLimit(ip))
	require.True(t, rl.checkIPLimit(ip))
	require.True(t, rl.checkIPLimit(ip))
	// 4번째 → RequestLog 길이 4 > 3 → 밴 + false
	require.False(t, rl.checkIPLimit(ip))
	require.True(t, rl.isIPBanned(ip))
}

// Test_CheckIPLimit_WindowEviction 윈도우 밖 요청은 정리됨
func Test_CheckIPLimit_WindowEviction(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.PerIPRPS = 100000
	cfg.PerIPBurstSize = 100000
	cfg.BanThreshold = 100
	cfg.WindowSize = time.Hour
	cfg.WhitelistedIPs = nil
	rl := rateLimiterForTest(t, cfg)

	ip := "203.0.113.88"
	require.True(t, rl.checkIPLimit(ip))

	// 오래된 요청을 윈도우 밖으로 직접 주입
	rl.mutex.Lock()
	rl.ipLimiters[ip].RequestLog = []time.Time{time.Now().Add(-2 * time.Hour)}
	rl.mutex.Unlock()

	require.True(t, rl.checkIPLimit(ip))
	// 윈도우 밖 1개는 제거되고 방금 추가한 1개만 남아야 함
	rl.mutex.RLock()
	n := len(rl.ipLimiters[ip].RequestLog)
	rl.mutex.RUnlock()
	require.Equal(t, 1, n)
}

// --- checkUserLimit + 사용자별 격리 ---

func Test_CheckUserLimit_PerUserIsolation(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.PerUserRPS = 100
	cfg.PerUserBurstSize = 1
	cfg.WhitelistedIPs = nil
	rl := rateLimiterForTest(t, cfg)

	// userA 버스트 소진
	require.True(t, rl.checkUserLimit("userA"))
	require.False(t, rl.checkUserLimit("userA"))
	// userB 는 독립적으로 여전히 허용
	require.True(t, rl.checkUserLimit("userB"))
}

// Test_Middleware_UserLimit_Enforced user_id 설정 시 사용자 제한 적용
func Test_Middleware_UserLimit_Enforced(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.PerIPRPS = 100000
	cfg.PerIPBurstSize = 100000
	cfg.PerUserRPS = 100
	cfg.PerUserBurstSize = 1
	cfg.WhitelistedIPs = nil
	rl := rateLimiterForTest(t, cfg)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user_id", "u1") })
	r.Use(rl.RateLimitMiddleware())
	r.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })

	codes := make([]int, 0, 2)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.RemoteAddr = "198.51.100.77:1234"
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	require.Equal(t, http.StatusOK, codes[0])
	require.Equal(t, http.StatusTooManyRequests, codes[1])
}

// --- checkEndpointLimit ---

func Test_CheckEndpointLimit(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.EndpointLimits = map[string]Limit{
		"/api/clone": {RPS: 100, BurstSize: 1},
	}
	cfg.WhitelistedIPs = nil
	rl := rateLimiterForTest(t, cfg)

	// 매칭 엔드포인트: 버스트 1 → 첫 허용, 둘째 거부
	require.True(t, rl.checkEndpointLimit("/api/clone/x"))
	require.False(t, rl.checkEndpointLimit("/api/clone/x"))
	// 비매칭 엔드포인트는 항상 허용
	require.True(t, rl.checkEndpointLimit("/other"))
}

// --- handleRateLimit 모든 limitType 분기 ---

func Test_HandleRateLimit_AllTypes(t *testing.T) {
	rl := rateLimiterForTest(t, nil)
	gin.SetMode(gin.TestMode)

	for _, lt := range []string{"global", "ip", "user", "endpoint", "unknown"} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
		c.Request.RemoteAddr = "203.0.113.1:1"
		rl.handleRateLimit(c, lt)
		require.Equal(t, http.StatusTooManyRequests, rec.Code, "type=%s", lt)
		require.NotEmpty(t, rec.Header().Get("Retry-After"), "type=%s", lt)
		require.Equal(t, "0", rec.Header().Get("X-RateLimit-Remaining"))
	}
}

// --- 동시성 제한 ---

func Test_ConcurrentLimit_Helpers(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.MaxConcurrentRequests = 1
	rl := rateLimiterForTest(t, cfg)

	require.False(t, rl.checkConcurrentLimit()) // 0 < 1
	rl.incrementConcurrentRequests()
	require.True(t, rl.checkConcurrentLimit()) // 1 >= 1
	rl.decrementConcurrentRequests()
	require.False(t, rl.checkConcurrentLimit())
}

// --- cleanup ---

func Test_Cleanup_RemovesStale(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.CleanupInterval = time.Minute
	cfg.Enabled = false // 백그라운드 ticker 비활성, cleanup 직접 호출
	rl := rateLimiterForTest(t, cfg)

	old := time.Now().Add(-10 * time.Minute)
	rl.mutex.Lock()
	rl.userLimiters["staleUser"] = &UserLimiter{LastSeen: old}
	rl.userLimiters["freshUser"] = &UserLimiter{LastSeen: time.Now()}
	rl.ipLimiters["staleIP"] = &IPLimiter{LastSeen: old}
	rl.ipLimiters["freshIP"] = &IPLimiter{LastSeen: time.Now()}
	rl.bannedIPs["expiredBan"] = time.Now().Add(-time.Hour)
	rl.bannedIPs["activeBan"] = time.Now().Add(time.Hour)
	rl.mutex.Unlock()

	rl.cleanup()

	rl.mutex.RLock()
	defer rl.mutex.RUnlock()
	require.NotContains(t, rl.userLimiters, "staleUser")
	require.Contains(t, rl.userLimiters, "freshUser")
	require.NotContains(t, rl.ipLimiters, "staleIP")
	require.Contains(t, rl.ipLimiters, "freshIP")
	require.NotContains(t, rl.bannedIPs, "expiredBan")
	require.Contains(t, rl.bannedIPs, "activeBan")
}

// Test_StartCleanup_FiresTick 백그라운드 cleanup ticker 동작
func Test_StartCleanup_FiresTick(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.CleanupInterval = 10 * time.Millisecond
	cfg.WhitelistedIPs = nil
	rl := rateLimiterForTest(t, cfg) // Enabled=true → startCleanup 호출됨

	old := time.Now().Add(-time.Hour)
	rl.mutex.Lock()
	rl.ipLimiters["x"] = &IPLimiter{LastSeen: old}
	rl.mutex.Unlock()

	require.Eventually(t, func() bool {
		rl.mutex.RLock()
		defer rl.mutex.RUnlock()
		_, ok := rl.ipLimiters["x"]
		return !ok
	}, time.Second, 5*time.Millisecond)
}

// --- GetStats ---

func Test_GetStats(t *testing.T) {
	rl := rateLimiterForTest(t, nil)
	rl.mutex.Lock()
	rl.userLimiters["u"] = &UserLimiter{LastSeen: time.Now()}
	rl.ipLimiters["i"] = &IPLimiter{LastSeen: time.Now()}
	rl.bannedIPs["b"] = time.Now().Add(time.Hour)
	rl.concurrentRequests = 7
	rl.mutex.Unlock()

	stats := rl.GetStats()
	require.Equal(t, true, stats["enabled"])
	require.Equal(t, 1, stats["user_limiters"])
	require.Equal(t, 1, stats["ip_limiters"])
	require.Equal(t, 1, stats["banned_ips"])
	require.Equal(t, 7, stats["concurrent_requests"])
	require.Contains(t, stats, "global_rps")
}

// --- 밴 IP 미들웨어 차단 ---

// Test_Middleware_BannedIP_Returns429 밴된 IP 는 429
func Test_Middleware_BannedIP_Returns429(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.WhitelistedIPs = nil
	rl := rateLimiterForTest(t, cfg)

	rl.mutex.Lock()
	rl.bannedIPs["192.0.2.200"] = time.Now().Add(time.Hour)
	rl.mutex.Unlock()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(rl.RateLimitMiddleware())
	r.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.RemoteAddr = "192.0.2.200:1234"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
}

// --- net 헬퍼 sanity (whitelistedNets Contains) ---

func Test_WhitelistedNets_Contains(t *testing.T) {
	cfg := DefaultRateLimiterConfig()
	cfg.WhitelistedIPs = []string{"172.16.0.0/12"}
	rl := rateLimiterForTest(t, cfg)
	require.Len(t, rl.whitelistedNets, 1)
	require.True(t, rl.whitelistedNets[0].Contains(net.ParseIP("172.16.5.5")))
}
