package security

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/time/rate"

	"github.com/collabops/git-executor/internal/logging"
)

// RateLimiterConfig holds configuration for rate limiting
type RateLimiterConfig struct {
	Enabled                bool             `yaml:"enabled" json:"enabled"`
	GlobalRPS              float64          `yaml:"global_rps" json:"global_rps"`                     // Global requests per second
	GlobalBurstSize        int              `yaml:"global_burst_size" json:"global_burst_size"`       // Global burst allowance
	PerUserRPS             float64          `yaml:"per_user_rps" json:"per_user_rps"`                 // Per user requests per second
	PerUserBurstSize       int              `yaml:"per_user_burst_size" json:"per_user_burst_size"`   // Per user burst allowance
	PerIPRPS               float64          `yaml:"per_ip_rps" json:"per_ip_rps"`                     // Per IP requests per second
	PerIPBurstSize         int              `yaml:"per_ip_burst_size" json:"per_ip_burst_size"`       // Per IP burst allowance
	BurstSize              int              `yaml:"burst_size" json:"burst_size"`                     // Deprecated: Use specific burst sizes
	CleanupInterval        time.Duration    `yaml:"cleanup_interval" json:"cleanup_interval"`         // Limiter cleanup interval
	BanDuration            time.Duration    `yaml:"ban_duration" json:"ban_duration"`                 // IP ban duration
	BanThreshold           int              `yaml:"ban_threshold" json:"ban_threshold"`               // Requests to trigger ban
	WindowSize             time.Duration    `yaml:"window_size" json:"window_size"`                   // Window for ban threshold
	WhitelistedIPs         []string         `yaml:"whitelisted_ips" json:"whitelisted_ips"`           // IPs exempt from rate limiting
	EndpointLimits         map[string]Limit `yaml:"endpoint_limits" json:"endpoint_limits"`           // Per-endpoint limits
	SlowlorisProtection    bool             `yaml:"slowloris_protection" json:"slowloris_protection"` // Enable slowloris protection
	MaxConcurrentRequests  int              `yaml:"max_concurrent_requests" json:"max_concurrent_requests"`
	RequestTimeoutDuration time.Duration    `yaml:"request_timeout_duration" json:"request_timeout_duration"`
}

// Limit defines rate limiting parameters for specific endpoints
type Limit struct {
	RPS       float64 `yaml:"rps" json:"rps"`
	BurstSize int     `yaml:"burst_size" json:"burst_size"`
}

// DefaultRateLimiterConfig returns sensible defaults
func DefaultRateLimiterConfig() *RateLimiterConfig {
	return &RateLimiterConfig{
		Enabled:                true,
		GlobalRPS:              1000.0, // 1000 requests per second globally
		GlobalBurstSize:        50,     // Allow bursts up to 50 requests globally
		PerUserRPS:             10.0,   // 10 requests per second per user
		PerUserBurstSize:       20,     // Allow bursts up to 20 requests per user
		PerIPRPS:               50.0,   // 50 requests per second per IP
		PerIPBurstSize:         30,     // Allow bursts up to 30 requests per IP
		BurstSize:              20,     // Deprecated: Use specific burst sizes (kept for backward compatibility)
		CleanupInterval:        5 * time.Minute,
		BanDuration:            10 * time.Minute,
		BanThreshold:           100, // 100 requests in window triggers ban
		WindowSize:             1 * time.Minute,
		WhitelistedIPs:         []string{"127.0.0.1", "::1"},
		MaxConcurrentRequests:  500,
		RequestTimeoutDuration: 30 * time.Second,
		SlowlorisProtection:    true,
		EndpointLimits: map[string]Limit{
			"/api/auth":   {RPS: 5.0, BurstSize: 10},  // Authentication endpoints
			"/api/upload": {RPS: 2.0, BurstSize: 5},   // File upload endpoints
			"/api/clone":  {RPS: 1.0, BurstSize: 2},   // Clone operations
			"/api/push":   {RPS: 5.0, BurstSize: 10},  // Push operations
			"/api/pull":   {RPS: 10.0, BurstSize: 20}, // Pull operations
		},
	}
}

// getGlobalBurstSize returns the global burst size with backward compatibility
func (rl *RateLimiter) getGlobalBurstSize() int {
	if rl.config.GlobalBurstSize > 0 {
		return rl.config.GlobalBurstSize
	}
	return rl.config.BurstSize // Fallback to deprecated field
}

// getIPBurstSize returns the IP burst size with backward compatibility
func (rl *RateLimiter) getIPBurstSize() int {
	if rl.config.PerIPBurstSize > 0 {
		return rl.config.PerIPBurstSize
	}
	return rl.config.BurstSize // Fallback to deprecated field
}

// getUserBurstSize returns the user burst size with backward compatibility
func (rl *RateLimiter) getUserBurstSize() int {
	if rl.config.PerUserBurstSize > 0 {
		return rl.config.PerUserBurstSize
	}
	return rl.config.BurstSize // Fallback to deprecated field
}

// RateLimiter provides comprehensive rate limiting and DDoS protection
type RateLimiter struct {
	config             *RateLimiterConfig
	logger             *logging.Logger
	globalLimiter      *rate.Limiter
	userLimiters       map[string]*UserLimiter
	ipLimiters         map[string]*IPLimiter
	bannedIPs          map[string]time.Time
	whitelistedNets    []*net.IPNet
	endpointLimiters   map[string]*rate.Limiter
	concurrentRequests int
	mutex              sync.RWMutex
	cleanupTicker      *time.Ticker
	stopCleanup        chan struct{}
}

// UserLimiter tracks rate limiting for a specific user
type UserLimiter struct {
	Limiter  *rate.Limiter
	LastSeen time.Time
}

// IPLimiter tracks rate limiting and abuse detection for an IP
type IPLimiter struct {
	Limiter    *rate.Limiter
	RequestLog []time.Time // Rolling window of requests
	LastSeen   time.Time
	Violations int
}

// NewRateLimiter creates a new rate limiter instance
func NewRateLimiter(config *RateLimiterConfig, logger *logging.Logger) (*RateLimiter, error) {
	if config == nil {
		config = DefaultRateLimiterConfig()
	}

	rl := &RateLimiter{
		config:           config,
		logger:           logger,
		userLimiters:     make(map[string]*UserLimiter),
		ipLimiters:       make(map[string]*IPLimiter),
		bannedIPs:        make(map[string]time.Time),
		endpointLimiters: make(map[string]*rate.Limiter),
		stopCleanup:      make(chan struct{}),
	}

	// Initialize global limiter with appropriate burst size
	rl.globalLimiter = rate.NewLimiter(rate.Limit(config.GlobalRPS), rl.getGlobalBurstSize())

	// Parse whitelisted IP ranges
	if err := rl.parseWhitelistedIPs(); err != nil {
		return nil, fmt.Errorf("failed to parse whitelisted IPs: %w", err)
	}

	// Setup endpoint-specific limiters
	rl.setupEndpointLimiters()

	// Start background cleanup
	if config.Enabled {
		rl.startCleanup()
	}

	return rl, nil
}

// parseWhitelistedIPs parses CIDR ranges for whitelisted IPs
func (rl *RateLimiter) parseWhitelistedIPs() error {
	for _, ip := range rl.config.WhitelistedIPs {
		// Check if it's a CIDR range
		if strings.Contains(ip, "/") {
			_, ipNet, err := net.ParseCIDR(ip)
			if err != nil {
				return fmt.Errorf("invalid CIDR range %s: %w", ip, err)
			}
			rl.whitelistedNets = append(rl.whitelistedNets, ipNet)
		} else {
			// Single IP - convert to /32 or /128
			parsedIP := net.ParseIP(ip)
			if parsedIP == nil {
				return fmt.Errorf("invalid IP address: %s", ip)
			}

			var ipNet *net.IPNet
			if parsedIP.To4() != nil {
				_, ipNet, _ = net.ParseCIDR(ip + "/32")
			} else {
				_, ipNet, _ = net.ParseCIDR(ip + "/128")
			}
			rl.whitelistedNets = append(rl.whitelistedNets, ipNet)
		}
	}
	return nil
}

// setupEndpointLimiters creates rate limiters for specific endpoints
func (rl *RateLimiter) setupEndpointLimiters() {
	for endpoint, limit := range rl.config.EndpointLimits {
		rl.endpointLimiters[endpoint] = rate.NewLimiter(rate.Limit(limit.RPS), limit.BurstSize)
	}
}

// RateLimitMiddleware returns a Gin middleware for rate limiting
func (rl *RateLimiter) RateLimitMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !rl.config.Enabled {
			c.Next()
			return
		}

		clientIP := rl.getClientIP(c)
		userID := rl.getUserID(c)

		// Check if IP is whitelisted
		if rl.isWhitelisted(clientIP) {
			c.Next()
			return
		}

		// Check if IP is banned
		if rl.isIPBanned(clientIP) {
			rl.logger.Warn("Blocked request from banned IP",
				zap.String("ip", clientIP),
				zap.String("path", c.Request.URL.Path))
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":       "IP temporarily banned due to abuse",
				"code":        "IP_BANNED",
				"retry_after": int(rl.config.BanDuration.Seconds()),
			})
			c.Abort()
			return
		}

		// Check concurrent request limit
		if rl.checkConcurrentLimit() {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "Server overloaded",
				"code":  "CONCURRENT_LIMIT_EXCEEDED",
			})
			c.Abort()
			return
		}

		// Increment concurrent requests
		rl.incrementConcurrentRequests()
		defer rl.decrementConcurrentRequests()

		// Apply rate limiting checks
		if !rl.checkGlobalLimit() {
			rl.handleRateLimit(c, "global")
			return
		}

		if !rl.checkIPLimit(clientIP) {
			rl.handleRateLimit(c, "ip")
			return
		}

		if userID != "" && !rl.checkUserLimit(userID) {
			rl.handleRateLimit(c, "user")
			return
		}

		if !rl.checkEndpointLimit(c.Request.URL.Path) {
			rl.handleRateLimit(c, "endpoint")
			return
		}

		// Setup request timeout if slowloris protection is enabled
		if rl.config.SlowlorisProtection {
			ctx, cancel := context.WithTimeout(c.Request.Context(), rl.config.RequestTimeoutDuration)
			defer cancel()
			c.Request = c.Request.WithContext(ctx)
		}

		c.Next()
	}
}

// getClientIP extracts the real client IP from the request
func (rl *RateLimiter) getClientIP(c *gin.Context) string {
	// Check X-Forwarded-For header first
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}

	// Check X-Real-IP header
	if xri := c.GetHeader("X-Real-IP"); xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	ip, _, _ := net.SplitHostPort(c.Request.RemoteAddr)
	return ip
}

// getUserID extracts user ID from authentication context
func (rl *RateLimiter) getUserID(c *gin.Context) string {
	if userID, exists := c.Get("user_id"); exists {
		if id, ok := userID.(string); ok {
			return id
		}
	}
	return ""
}

// isWhitelisted checks if an IP is in the whitelist
func (rl *RateLimiter) isWhitelisted(ip string) bool {
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false
	}

	for _, ipNet := range rl.whitelistedNets {
		if ipNet.Contains(parsedIP) {
			return true
		}
	}
	return false
}

// isIPBanned checks if an IP is currently banned
func (rl *RateLimiter) isIPBanned(ip string) bool {
	rl.mutex.RLock()
	bannedUntil, banned := rl.bannedIPs[ip]
	rl.mutex.RUnlock()

	if !banned {
		return false
	}

	if time.Now().After(bannedUntil) {
		// Ban expired, remove it
		rl.mutex.Lock()
		delete(rl.bannedIPs, ip)
		rl.mutex.Unlock()
		return false
	}

	return true
}

// checkConcurrentLimit verifies concurrent request limits
func (rl *RateLimiter) checkConcurrentLimit() bool {
	rl.mutex.RLock()
	defer rl.mutex.RUnlock()
	return rl.concurrentRequests >= rl.config.MaxConcurrentRequests
}

// incrementConcurrentRequests safely increments the concurrent request counter
func (rl *RateLimiter) incrementConcurrentRequests() {
	rl.mutex.Lock()
	rl.concurrentRequests++
	rl.mutex.Unlock()
}

// decrementConcurrentRequests safely decrements the concurrent request counter
func (rl *RateLimiter) decrementConcurrentRequests() {
	rl.mutex.Lock()
	rl.concurrentRequests--
	rl.mutex.Unlock()
}

// checkGlobalLimit checks against global rate limit
func (rl *RateLimiter) checkGlobalLimit() bool {
	return rl.globalLimiter.Allow()
}

// checkIPLimit checks and tracks IP-based rate limiting
func (rl *RateLimiter) checkIPLimit(ip string) bool {
	rl.mutex.Lock()
	defer rl.mutex.Unlock()

	ipLimiter, exists := rl.ipLimiters[ip]
	if !exists {
		ipLimiter = &IPLimiter{
			Limiter:    rate.NewLimiter(rate.Limit(rl.config.PerIPRPS), rl.getIPBurstSize()),
			RequestLog: make([]time.Time, 0),
			LastSeen:   time.Now(),
		}
		rl.ipLimiters[ip] = ipLimiter
	}

	ipLimiter.LastSeen = time.Now()

	// Track request for ban threshold monitoring
	now := time.Now()
	ipLimiter.RequestLog = append(ipLimiter.RequestLog, now)

	// Clean old requests outside the window
	cutoff := now.Add(-rl.config.WindowSize)
	validRequests := make([]time.Time, 0)
	for _, reqTime := range ipLimiter.RequestLog {
		if reqTime.After(cutoff) {
			validRequests = append(validRequests, reqTime)
		}
	}
	ipLimiter.RequestLog = validRequests

	// Check for ban threshold
	if len(ipLimiter.RequestLog) > rl.config.BanThreshold {
		rl.banIP(ip)
		rl.logger.Warn("IP banned for exceeding request threshold",
			zap.String("ip", ip),
			zap.Int("requests_in_window", len(ipLimiter.RequestLog)),
			zap.Int("threshold", rl.config.BanThreshold))
		return false
	}

	return ipLimiter.Limiter.Allow()
}

// checkUserLimit checks user-based rate limiting
func (rl *RateLimiter) checkUserLimit(userID string) bool {
	rl.mutex.Lock()
	defer rl.mutex.Unlock()

	userLimiter, exists := rl.userLimiters[userID]
	if !exists {
		userLimiter = &UserLimiter{
			Limiter:  rate.NewLimiter(rate.Limit(rl.config.PerUserRPS), rl.getUserBurstSize()),
			LastSeen: time.Now(),
		}
		rl.userLimiters[userID] = userLimiter
	}

	userLimiter.LastSeen = time.Now()
	return userLimiter.Limiter.Allow()
}

// checkEndpointLimit checks endpoint-specific rate limiting
func (rl *RateLimiter) checkEndpointLimit(path string) bool {
	// Find matching endpoint pattern
	for endpoint, limiter := range rl.endpointLimiters {
		if strings.HasPrefix(path, endpoint) {
			return limiter.Allow()
		}
	}
	return true // No specific limit for this endpoint
}

// banIP adds an IP to the ban list
func (rl *RateLimiter) banIP(ip string) {
	rl.bannedIPs[ip] = time.Now().Add(rl.config.BanDuration)
}

// handleRateLimit sends appropriate rate limit response
func (rl *RateLimiter) handleRateLimit(c *gin.Context, limitType string) {
	var retryAfter int
	var message string

	switch limitType {
	case "global":
		retryAfter = int(1.0 / rl.config.GlobalRPS)
		message = "Global rate limit exceeded"
	case "ip":
		retryAfter = int(1.0 / rl.config.PerIPRPS)
		message = "IP rate limit exceeded"
	case "user":
		retryAfter = int(1.0 / rl.config.PerUserRPS)
		message = "User rate limit exceeded"
	case "endpoint":
		retryAfter = 1
		message = "Endpoint rate limit exceeded"
	default:
		retryAfter = 1
		message = "Rate limit exceeded"
	}

	rl.logger.Info("Rate limit exceeded",
		zap.String("limit_type", limitType),
		zap.String("ip", rl.getClientIP(c)),
		zap.String("path", c.Request.URL.Path),
		zap.String("user_agent", c.GetHeader("User-Agent")))

	c.Header("Retry-After", strconv.Itoa(retryAfter))
	c.Header("X-RateLimit-Limit", fmt.Sprintf("%.0f", rl.config.PerIPRPS))
	c.Header("X-RateLimit-Remaining", "0")
	c.Header("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Duration(retryAfter)*time.Second).Unix(), 10))

	c.JSON(http.StatusTooManyRequests, gin.H{
		"error":       message,
		"code":        "RATE_LIMIT_EXCEEDED",
		"limit_type":  limitType,
		"retry_after": retryAfter,
	})
	c.Abort()
}

// startCleanup starts the background cleanup routine
func (rl *RateLimiter) startCleanup() {
	rl.cleanupTicker = time.NewTicker(rl.config.CleanupInterval)
	go func() {
		for {
			select {
			case <-rl.cleanupTicker.C:
				rl.cleanup()
			case <-rl.stopCleanup:
				return
			}
		}
	}()
}

// cleanup removes stale limiters and expired bans
func (rl *RateLimiter) cleanup() {
	rl.mutex.Lock()
	defer rl.mutex.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.config.CleanupInterval * 2) // Keep for 2 intervals

	// Clean up user limiters
	for userID, limiter := range rl.userLimiters {
		if limiter.LastSeen.Before(cutoff) {
			delete(rl.userLimiters, userID)
		}
	}

	// Clean up IP limiters
	for ip, limiter := range rl.ipLimiters {
		if limiter.LastSeen.Before(cutoff) {
			delete(rl.ipLimiters, ip)
		}
	}

	// Clean up expired bans
	for ip, bannedUntil := range rl.bannedIPs {
		if now.After(bannedUntil) {
			delete(rl.bannedIPs, ip)
		}
	}

	rl.logger.Debug("Rate limiter cleanup completed",
		zap.Int("user_limiters", len(rl.userLimiters)),
		zap.Int("ip_limiters", len(rl.ipLimiters)),
		zap.Int("banned_ips", len(rl.bannedIPs)))
}

// Stop gracefully shuts down the rate limiter
func (rl *RateLimiter) Stop() {
	if rl.cleanupTicker != nil {
		rl.cleanupTicker.Stop()
		close(rl.stopCleanup)
	}
}

// GetStats returns current rate limiter statistics
func (rl *RateLimiter) GetStats() map[string]interface{} {
	rl.mutex.RLock()
	defer rl.mutex.RUnlock()

	return map[string]interface{}{
		"enabled":             rl.config.Enabled,
		"user_limiters":       len(rl.userLimiters),
		"ip_limiters":         len(rl.ipLimiters),
		"banned_ips":          len(rl.bannedIPs),
		"concurrent_requests": rl.concurrentRequests,
		"global_rps":          rl.config.GlobalRPS,
		"per_ip_rps":          rl.config.PerIPRPS,
		"per_user_rps":        rl.config.PerUserRPS,
	}
}
