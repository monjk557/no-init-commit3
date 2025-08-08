package security

import (
	"crypto/rsa"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/collabops/git-executor/internal/logging"
)

// JWTAuthMiddleware provides JWT-based authentication and authorization
type JWTAuthMiddleware struct {
	config      *JWTConfig
	logger      *logging.Logger
	publicKey   *rsa.PublicKey
	trustedKeys map[string]*rsa.PublicKey // Multiple trusted keys for key rotation
}

// JWTConfig holds configuration for JWT authentication
type JWTConfig struct {
	Enabled               bool          `yaml:"enabled" json:"enabled"`
	PublicKeyPath         string        `yaml:"public_key_path" json:"public_key_path"`
	TrustedIssuers        []string      `yaml:"trusted_issuers" json:"trusted_issuers"`
	RequiredAudience      string        `yaml:"required_audience" json:"required_audience"`
	TokenExpiration       time.Duration `yaml:"token_expiration" json:"token_expiration"`
	ClockSkewTolerance    time.Duration `yaml:"clock_skew_tolerance" json:"clock_skew_tolerance"`
	AllowInsecureSkip     bool          `yaml:"allow_insecure_skip" json:"allow_insecure_skip"` // For development only
	RequiredScopes        []string      `yaml:"required_scopes" json:"required_scopes"`
	RateLimitPerUser      int           `yaml:"rate_limit_per_user" json:"rate_limit_per_user"`
	SessionTimeout        time.Duration `yaml:"session_timeout" json:"session_timeout"`
	EnableRefreshTokens   bool          `yaml:"enable_refresh_tokens" json:"enable_refresh_tokens"`
	MaxConcurrentSessions int           `yaml:"max_concurrent_sessions" json:"max_concurrent_sessions"`
}

// DefaultJWTConfig returns secure default configuration
func DefaultJWTConfig() *JWTConfig {
	return &JWTConfig{
		Enabled:               true,
		PublicKeyPath:         "/etc/git-executor/jwt-public.pem",
		TrustedIssuers:        []string{"https://auth.collabops.com"},
		RequiredAudience:      "git-executor-api",
		TokenExpiration:       15 * time.Minute,
		ClockSkewTolerance:    5 * time.Minute,
		AllowInsecureSkip:     false,
		RequiredScopes:        []string{"git:read", "git:write"},
		RateLimitPerUser:      1000, // requests per hour
		SessionTimeout:        8 * time.Hour,
		EnableRefreshTokens:   true,
		MaxConcurrentSessions: 5,
	}
}

// JWTClaims represents the claims in a JWT token
type JWTClaims struct {
	UserID      string   `json:"sub"`
	Username    string   `json:"preferred_username"`
	Email       string   `json:"email"`
	Roles       []string `json:"roles"`
	Scopes      []string `json:"scope"`
	Permissions []string `json:"permissions"`
	SessionID   string   `json:"session_id"`
	IPAddress   string   `json:"ip_address"`
	UserAgent   string   `json:"user_agent"`
	jwt.RegisteredClaims
}

// AuthContext contains authenticated user information
type AuthContext struct {
	UserID      string
	Username    string
	Email       string
	Roles       []string
	Scopes      []string
	Permissions []string
	SessionID   string
	IPAddress   string
	TokenIssuer string
	AuthTime    time.Time
}

// NewJWTAuthMiddleware creates a new JWT authentication middleware
func NewJWTAuthMiddleware(config *JWTConfig, logger *logging.Logger) (*JWTAuthMiddleware, error) {
	if config == nil {
		config = DefaultJWTConfig()
	}

	if logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}

	middleware := &JWTAuthMiddleware{
		config:      config,
		logger:      logger,
		trustedKeys: make(map[string]*rsa.PublicKey),
	}

	// Load public key for token verification
	if config.Enabled && !config.AllowInsecureSkip {
		if err := middleware.loadPublicKey(); err != nil {
			return nil, fmt.Errorf("failed to load JWT public key: %w", err)
		}
	}

	return middleware, nil
}

// loadPublicKey loads the RSA public key for JWT verification
func (j *JWTAuthMiddleware) loadPublicKey() error {
	if j.config.PublicKeyPath == "" {
		j.logger.Info("No public key path configured, skipping JWT key loading")
		return nil
	}

	// In a real implementation, this would read from file system
	// For now, we'll use a placeholder that would be replaced with actual key loading
	j.logger.Info("Loading JWT public key", zap.String("path", j.config.PublicKeyPath))

	// Placeholder: In production, implement actual key loading
	// publicKeyPEM, err := os.ReadFile(j.config.PublicKeyPath)
	// if err != nil {
	//     return fmt.Errorf("failed to read public key file: %w", err)
	// }
	//
	// publicKey, err := j.parseRSAPublicKey(publicKeyPEM)
	// if err != nil {
	//     return fmt.Errorf("failed to parse public key: %w", err)
	// }
	//
	// j.publicKey = publicKey

	return nil
}

// AuthenticationMiddleware returns a Gin middleware for JWT authentication
func (j *JWTAuthMiddleware) AuthenticationMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Skip authentication if disabled
		if !j.config.Enabled {
			j.logger.Debug("JWT authentication disabled, skipping")
			c.Next()
			return
		}

		// Allow insecure skip for development
		if j.config.AllowInsecureSkip {
			j.logger.Warn("JWT authentication bypassed (insecure skip enabled)")
			j.setDevelopmentAuthContext(c)
			c.Next()
			return
		}

		// Extract token from request
		token, err := j.extractToken(c)
		if err != nil {
			j.logger.Warn("Failed to extract JWT token", zap.Error(err))
			j.sendAuthError(c, "Missing or invalid authorization header", http.StatusUnauthorized)
			return
		}

		// Validate and parse token
		claims, err := j.validateToken(token)
		if err != nil {
			j.logger.Warn("JWT token validation failed", zap.Error(err), zap.String("remote_addr", c.ClientIP()))
			j.sendAuthError(c, "Invalid or expired token", http.StatusUnauthorized)
			return
		}

		// Additional security checks
		if err := j.performSecurityChecks(c, claims); err != nil {
			j.logger.Warn("JWT security checks failed", zap.Error(err), zap.String("user_id", claims.UserID))
			j.sendAuthError(c, "Security validation failed", http.StatusForbidden)
			return
		}

		// Set authentication context
		authCtx := j.createAuthContext(claims)
		j.setAuthContext(c, authCtx)

		// Log successful authentication
		j.logger.Info("JWT authentication successful",
			zap.String("user_id", claims.UserID),
			zap.String("username", claims.Username),
			zap.Strings("roles", claims.Roles),
			zap.String("session_id", claims.SessionID),
			zap.String("remote_addr", c.ClientIP()))

		c.Next()
	}
}

// extractToken extracts JWT token from Authorization header
func (j *JWTAuthMiddleware) extractToken(c *gin.Context) (string, error) {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		return "", fmt.Errorf("authorization header is missing")
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		return "", fmt.Errorf("authorization header format must be Bearer {token}")
	}

	return parts[1], nil
}

// validateToken validates and parses a JWT token
func (j *JWTAuthMiddleware) validateToken(tokenString string) (*JWTClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &JWTClaims{}, func(token *jwt.Token) (interface{}, error) {
		// Verify signing method
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}

		// Return the public key for verification
		// In production, this would use the loaded public key
		if j.publicKey != nil {
			return j.publicKey, nil
		}

		// Placeholder for development/testing
		return []byte("placeholder-key"), nil
	})

	if err != nil {
		return nil, fmt.Errorf("token parsing failed: %w", err)
	}

	claims, ok := token.Claims.(*JWTClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	// Validate required claims
	if err := j.validateClaims(claims); err != nil {
		return nil, fmt.Errorf("claims validation failed: %w", err)
	}

	return claims, nil
}

// validateClaims validates JWT claims according to security requirements
func (j *JWTAuthMiddleware) validateClaims(claims *JWTClaims) error {
	now := time.Now()

	// Check expiration with clock skew tolerance
	if claims.ExpiresAt != nil {
		if now.After(claims.ExpiresAt.Add(j.config.ClockSkewTolerance)) {
			return fmt.Errorf("token has expired")
		}
	}

	// Check not before with clock skew tolerance
	if claims.NotBefore != nil {
		if now.Before(claims.NotBefore.Add(-j.config.ClockSkewTolerance)) {
			return fmt.Errorf("token not yet valid")
		}
	}

	// Validate issuer
	if len(j.config.TrustedIssuers) > 0 {
		validIssuer := false
		for _, trustedIssuer := range j.config.TrustedIssuers {
			if claims.Issuer == trustedIssuer {
				validIssuer = true
				break
			}
		}
		if !validIssuer {
			return fmt.Errorf("token issuer not trusted: %s", claims.Issuer)
		}
	}

	// Validate audience
	if j.config.RequiredAudience != "" {
		validAudience := false
		for _, aud := range claims.Audience {
			if aud == j.config.RequiredAudience {
				validAudience = true
				break
			}
		}
		if !validAudience {
			return fmt.Errorf("token audience invalid")
		}
	}

	// Validate required scopes
	if len(j.config.RequiredScopes) > 0 {
		for _, requiredScope := range j.config.RequiredScopes {
			found := false
			for _, scope := range claims.Scopes {
				if scope == requiredScope {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("missing required scope: %s", requiredScope)
			}
		}
	}

	// Validate required fields
	if claims.UserID == "" {
		return fmt.Errorf("user ID is required")
	}

	if claims.SessionID == "" {
		return fmt.Errorf("session ID is required")
	}

	return nil
}

// performSecurityChecks performs additional security validations
func (j *JWTAuthMiddleware) performSecurityChecks(c *gin.Context, claims *JWTClaims) error {
	// IP address validation (if token contains IP binding)
	if claims.IPAddress != "" && claims.IPAddress != c.ClientIP() {
		return fmt.Errorf("token IP address mismatch: expected %s, got %s", claims.IPAddress, c.ClientIP())
	}

	// User agent validation (if token contains user agent binding)
	if claims.UserAgent != "" && claims.UserAgent != c.GetHeader("User-Agent") {
		return fmt.Errorf("token user agent mismatch")
	}

	// Session timeout check
	if claims.IssuedAt != nil {
		if time.Since(claims.IssuedAt.Time) > j.config.SessionTimeout {
			return fmt.Errorf("session has timed out")
		}
	}

	// TODO: Add additional checks:
	// - Rate limiting per user
	// - Concurrent session limits
	// - Account lockout checks
	// - Suspicious activity detection

	return nil
}

// createAuthContext creates an authentication context from JWT claims
func (j *JWTAuthMiddleware) createAuthContext(claims *JWTClaims) *AuthContext {
	return &AuthContext{
		UserID:      claims.UserID,
		Username:    claims.Username,
		Email:       claims.Email,
		Roles:       claims.Roles,
		Scopes:      claims.Scopes,
		Permissions: claims.Permissions,
		SessionID:   claims.SessionID,
		IPAddress:   claims.IPAddress,
		TokenIssuer: claims.Issuer,
		AuthTime:    time.Now(),
	}
}

// setAuthContext sets the authentication context in the Gin context
func (j *JWTAuthMiddleware) setAuthContext(c *gin.Context, authCtx *AuthContext) {
	c.Set("auth_context", authCtx)
	c.Set("user_id", authCtx.UserID)
	c.Set("username", authCtx.Username)
	c.Set("user_roles", authCtx.Roles)
	c.Set("user_permissions", authCtx.Permissions)
	c.Set("session_id", authCtx.SessionID)
}

// setDevelopmentAuthContext sets a development authentication context (insecure)
func (j *JWTAuthMiddleware) setDevelopmentAuthContext(c *gin.Context) {
	devAuthCtx := &AuthContext{
		UserID:      "dev-user",
		Username:    "developer",
		Email:       "dev@collabops.com",
		Roles:       []string{"admin", "developer"},
		Scopes:      []string{"git:read", "git:write", "admin:all"},
		Permissions: []string{"*"},
		SessionID:   "dev-session",
		IPAddress:   c.ClientIP(),
		TokenIssuer: "development",
		AuthTime:    time.Now(),
	}

	j.setAuthContext(c, devAuthCtx)
}

// sendAuthError sends a standardized authentication error response
func (j *JWTAuthMiddleware) sendAuthError(c *gin.Context, message string, statusCode int) {
	c.JSON(statusCode, gin.H{
		"status": "error",
		"error": gin.H{
			"code":    "authentication_failed",
			"message": message,
		},
		"meta": gin.H{
			"timestamp":  time.Now().Format(time.RFC3339),
			"request_id": c.GetString("request_id"),
		},
	})
	c.Abort()
}

// RequireScopes middleware to require specific scopes
func (j *JWTAuthMiddleware) RequireScopes(requiredScopes ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		authCtx := GetAuthContext(c)
		if authCtx == nil {
			j.sendAuthError(c, "Authentication required", http.StatusUnauthorized)
			return
		}

		for _, requiredScope := range requiredScopes {
			found := false
			for _, userScope := range authCtx.Scopes {
				if userScope == requiredScope || userScope == "*" {
					found = true
					break
				}
			}
			if !found {
				j.logger.Warn("Insufficient scopes",
					zap.String("user_id", authCtx.UserID),
					zap.String("required_scope", requiredScope),
					zap.Strings("user_scopes", authCtx.Scopes))
				j.sendAuthError(c, "Insufficient permissions", http.StatusForbidden)
				return
			}
		}

		c.Next()
	}
}

// RequireRoles middleware to require specific roles
func (j *JWTAuthMiddleware) RequireRoles(requiredRoles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		authCtx := GetAuthContext(c)
		if authCtx == nil {
			j.sendAuthError(c, "Authentication required", http.StatusUnauthorized)
			return
		}

		for _, requiredRole := range requiredRoles {
			found := false
			for _, userRole := range authCtx.Roles {
				if userRole == requiredRole || userRole == "admin" {
					found = true
					break
				}
			}
			if !found {
				j.logger.Warn("Insufficient roles",
					zap.String("user_id", authCtx.UserID),
					zap.String("required_role", requiredRole),
					zap.Strings("user_roles", authCtx.Roles))
				j.sendAuthError(c, "Insufficient permissions", http.StatusForbidden)
				return
			}
		}

		c.Next()
	}
}

// GetAuthContext retrieves the authentication context from Gin context
func GetAuthContext(c *gin.Context) *AuthContext {
	authCtx, exists := c.Get("auth_context")
	if !exists {
		return nil
	}

	ctx, ok := authCtx.(*AuthContext)
	if !ok {
		return nil
	}

	return ctx
}

// GetUserID retrieves the user ID from the authentication context
func GetUserID(c *gin.Context) string {
	authCtx := GetAuthContext(c)
	if authCtx == nil {
		return ""
	}
	return authCtx.UserID
}

// HasScope checks if the authenticated user has a specific scope
func HasScope(c *gin.Context, scope string) bool {
	authCtx := GetAuthContext(c)
	if authCtx == nil {
		return false
	}

	for _, userScope := range authCtx.Scopes {
		if userScope == scope || userScope == "*" {
			return true
		}
	}
	return false
}

// HasRole checks if the authenticated user has a specific role
func HasRole(c *gin.Context, role string) bool {
	authCtx := GetAuthContext(c)
	if authCtx == nil {
		return false
	}

	for _, userRole := range authCtx.Roles {
		if userRole == role || userRole == "admin" {
			return true
		}
	}
	return false
}

// GetSessionID retrieves the session ID from the authentication context
func GetSessionID(c *gin.Context) string {
	authCtx := GetAuthContext(c)
	if authCtx == nil {
		return ""
	}
	return authCtx.SessionID
}
