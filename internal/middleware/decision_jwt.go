// decision_jwt.go implements verification of authorization "decision" JWTs minted
// by the orchestrator (collabops-o2) and a gin middleware that enforces them across
// every executor API.
//
// Why this exists: the executor performs the actual git work. Until now its only
// caller-trust signal was the plaintext X-Source header, which any direct caller can
// spoof — bypassing the gateway's authorization entirely. A request that legitimately
// flows through a trusted gateway (git-http / git-ssh → orchestrator) now carries a
// short-lived HS256 decision token in the X-Decision-JWT header whose
// `gateway_attested` claim proves the orchestrator verified the gateway's origin
// signature. The executor verifies that token here.
//
// The verification contract MUST stay in lock-step with the issuer:
// collabops-api-orchestrator app/services/authz/decision_jwt.py (create/verify).
//
// Rollout is dual-accept — see config.DecisionJWTConfig for the three states.
package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
)

// DecisionTokenHeader carries the orchestrator-minted decision JWT. git-http already
// relays the token on this header today (collabops-git-http internal/server/server.go
// → r.Header.Set("X-Decision-JWT", ...)); issue-3383 (CR-4747) adds the gateway HMAC
// signing so the relayed token becomes gateway_attested. The name MUST match the relay.
const DecisionTokenHeader = "X-Decision-JWT"

// DecisionClaimsContextKey is the gin context key under which verified claims are
// stashed for downstream handlers and audit logging.
const DecisionClaimsContextKey = "decision_claims"

// signingAlgorithm pins the accepted JWT alg. Pinning to HS256 prevents algorithm
// confusion / alg=none downgrade attacks. Mirrors DECISION_JWT_ALGORITHM.
const signingAlgorithm = "HS256"

// Sentinel errors for the absence / attestation cases. Signature/exp/iss/aud failures
// surface as wrapped jwt errors from Verify.
var (
	// ErrNoDecisionToken: no token on the request (only rejected in strict mode).
	ErrNoDecisionToken = errors.New("decision token missing")
	// ErrNotAttested: token valid but lacks gateway_attested=true.
	ErrNotAttested = errors.New("decision token is not gateway-attested")
)

// DecisionClaims is the verified, decoded decision token. Field set mirrors the
// orchestrator's DecisionClaims.to_dict() payload.
type DecisionClaims struct {
	Subject         string // sub — authorized principal (user UUID)
	Object          string // obj — resource identifier
	Action          string // act — authorized action
	Audience        string // aud — target service
	Issuer          string // iss
	JTI             string // jti — unique token id (UUID)
	GatewayAttested bool   // gateway_attested — proves trusted-gateway origin
	IssuedAt        time.Time
	ExpiresAt       time.Time
}

// decisionTokenClaims is the wire shape parsed by golang-jwt. RegisteredClaims
// supplies iss/aud/sub/exp/iat/jti; the rest are custom.
type decisionTokenClaims struct {
	Object          string `json:"obj"`
	Action          string `json:"act"`
	Decision        any    `json:"dec"`                        // bool true or number 1
	GatewayAttested bool   `json:"gateway_attested,omitempty"` // absent ⇒ false
	jwt.RegisteredClaims
}

// fallbackLogInterval bounds how often the dual-accept fallback is logged per
// reason. The fallback fires on every tokenless request during rollout, so
// per-request logging floods the log volume (and storage). One line per reason
// per interval keeps the migration-readiness signal without the spam.
const fallbackLogInterval = 60 * time.Second

// DecisionVerifier verifies decision tokens against the shared HS256 secret.
type DecisionVerifier struct {
	cfg    config.DecisionJWTConfig
	logger *logging.Logger
	key    []byte
	parser *jwt.Parser

	// Rate-limit state for dual-accept fallback logging (per reason).
	fbMu         sync.Mutex
	fbLastLogged map[string]time.Time
	fbSuppressed map[string]int
}

// NewDecisionVerifier builds a verifier from config. The parser options enforce the
// orchestrator contract: HS256-only, exp required, issuer + audience checked, and a
// clock-skew leeway applied to exp/iat.
func NewDecisionVerifier(cfg config.DecisionJWTConfig, logger *logging.Logger) *DecisionVerifier {
	leeway := time.Duration(cfg.LeewaySeconds) * time.Second

	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{signingAlgorithm}),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(leeway),
		jwt.WithIssuer(cfg.Issuer),
		jwt.WithAudience(cfg.Audience),
		jwt.WithIssuedAt(), // reject tokens issued in the future (clock manipulation)
	)

	return &DecisionVerifier{
		cfg:          cfg,
		logger:       logger,
		key:          []byte(cfg.Secret),
		parser:       parser,
		fbLastLogged: make(map[string]time.Time),
		fbSuppressed: make(map[string]int),
	}
}

// Enabled reports whether verification is active. Without a shared secret nothing can
// be verified, so the middleware degrades to a complete no-op (safe migration default).
func (v *DecisionVerifier) Enabled() bool {
	return v != nil && len(v.key) > 0
}

// Verify validates a raw token string and returns its claims. It enforces every check
// the orchestrator's verify_decision_jwt performs: signature (HS256), exp, iss, aud,
// iat presence + not-in-future, required sub/obj/act/jti, jti UUID format, and dec=true.
func (v *DecisionVerifier) Verify(tokenStr string) (*DecisionClaims, error) {
	parsed := &decisionTokenClaims{}
	if _, err := v.parser.ParseWithClaims(tokenStr, parsed, func(*jwt.Token) (any, error) {
		return v.key, nil
	}); err != nil {
		return nil, fmt.Errorf("decision token verification failed: %w", err)
	}

	// iat is required by the issuer; the parser validates its value but does not
	// require its presence, so assert it explicitly.
	if parsed.IssuedAt == nil {
		return nil, errors.New("decision token missing iat")
	}

	if strings.TrimSpace(parsed.Subject) == "" {
		return nil, errors.New("decision token missing sub")
	}
	if strings.TrimSpace(parsed.Object) == "" {
		return nil, errors.New("decision token missing obj")
	}
	if strings.TrimSpace(parsed.Action) == "" {
		return nil, errors.New("decision token missing act")
	}

	// jti must be a UUID (matches issuer; blocks injection / replay-id forgery).
	if _, err := uuid.Parse(parsed.ID); err != nil {
		return nil, errors.New("decision token has invalid jti (must be UUID)")
	}

	// dec must explicitly grant. Only boolean true or numeric 1 — never the string
	// "true" — mirroring the issuer's deliberately strict check.
	if !decisionGranted(parsed.Decision) {
		return nil, errors.New("decision token does not grant permission (dec must be true)")
	}

	aud := ""
	if len(parsed.Audience) > 0 {
		aud = parsed.Audience[0]
	}

	return &DecisionClaims{
		Subject:         parsed.Subject,
		Object:          parsed.Object,
		Action:          parsed.Action,
		Audience:        aud,
		Issuer:          parsed.Issuer,
		JTI:             parsed.ID,
		GatewayAttested: parsed.GatewayAttested,
		IssuedAt:        parsed.IssuedAt.Time,
		ExpiresAt:       expiryTime(parsed.ExpiresAt),
	}, nil
}

// Middleware returns the gin handler that enforces decision tokens on every request,
// implementing the dual-accept policy. isExempt (optional) skips infra/probe paths
// that never carry a token (health, ready, metrics, swagger).
func (v *DecisionVerifier) Middleware(isExempt func(path string) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		// No shared secret yet → cannot verify anything, so never reject. This keeps
		// existing traffic flowing until the secret is provisioned everywhere.
		if !v.Enabled() {
			c.Next()
			return
		}

		if isExempt != nil && isExempt(c.Request.URL.Path) {
			c.Next()
			return
		}

		raw := extractDecisionToken(c.Request)
		if raw == "" {
			if v.cfg.Required {
				v.reject(c, "missing decision token", ErrNoDecisionToken)
				return
			}
			v.logFallback(c, "missing_token")
			c.Next()
			return
		}

		claims, err := v.Verify(raw)
		if err != nil {
			// Dual-accept (non-strict) NEVER rejects — it only observes. Rejecting a
			// present-but-invalid token here would break live traffic the moment the
			// secret is provisioned, because upstream legitimately relays tokens this
			// verifier does not yet accept (e.g. git-http's Layer-1 resolve path mints
			// aud="api-service", not "git-executor"). Non-strict logs the failure for
			// rollout-readiness measurement and falls back to legacy X-Source trust.
			// Only strict mode enforces — flip it once these fallback logs reach zero.
			if v.cfg.Required {
				v.reject(c, "invalid decision token", err)
				return
			}
			v.logFallback(c, "invalid_token")
			c.Next()
			return
		}

		if !claims.GatewayAttested {
			if v.cfg.Required {
				v.reject(c, "decision token not gateway-attested", ErrNotAttested)
				return
			}
			v.logFallback(c, "not_attested")
		}

		// Stash verified claims for downstream handlers / audit trail.
		c.Set(DecisionClaimsContextKey, claims)
		c.Next()
	}
}

// reject aborts the request with a 403 and logs the security event.
func (v *DecisionVerifier) reject(c *gin.Context, reason string, err error) {
	if v.logger != nil {
		v.logger.LogError(c.Request.Context(), err, "decision token rejected",
			zap.String("path", c.Request.URL.Path),
			zap.String("method", c.Request.Method),
			zap.String("remote_addr", c.Request.RemoteAddr),
			zap.String("reason", reason),
			zap.Bool("strict", v.cfg.Required),
			zap.String("request_id", c.Request.Header.Get("X-Request-ID")),
		)
	}
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"error": fmt.Sprintf("Access denied: %s", reason),
		"code":  http.StatusForbidden,
	})
}

// logFallback records a dual-accept fallback (token absent or non-attested while
// running in non-strict mode). These logs measure migration readiness: when they
// drop to zero, strict mode can be enabled safely.
//
// Rate-limited to one line per reason per fallbackLogInterval — the fallback
// fires on nearly every read request during rollout, so per-request logging
// dominated log volume/storage (Issue-5071). Each emitted line carries how many
// occurrences were suppressed since the previous one, so the signal is intact.
func (v *DecisionVerifier) logFallback(c *gin.Context, reason string) {
	if v.logger == nil {
		return
	}
	emit, suppressed := v.fallbackShouldLog(reason)
	if !emit {
		return
	}
	v.logger.LogWarning(c.Request.Context(), "decision token dual-accept fallback",
		zap.String("path", c.Request.URL.Path),
		zap.String("method", c.Request.Method),
		zap.String("reason", reason),
		zap.Int("suppressed_since_last", suppressed),
		zap.String("request_id", c.Request.Header.Get("X-Request-ID")),
	)
}

// fallbackShouldLog reports whether a fallback line should be emitted now for
// reason, rate-limited to once per fallbackLogInterval per reason, and returns
// how many occurrences were suppressed since the last emitted line.
func (v *DecisionVerifier) fallbackShouldLog(reason string) (emit bool, suppressed int) {
	v.fbMu.Lock()
	defer v.fbMu.Unlock()
	now := time.Now()
	if last, seen := v.fbLastLogged[reason]; seen && now.Sub(last) < fallbackLogInterval {
		v.fbSuppressed[reason]++
		return false, 0
	}
	suppressed = v.fbSuppressed[reason]
	v.fbSuppressed[reason] = 0
	v.fbLastLogged[reason] = now
	return true, suppressed
}

// extractDecisionToken pulls the token from the X-Decision-JWT header,
// tolerating an optional "Bearer " prefix.
func extractDecisionToken(r *http.Request) string {
	raw := strings.TrimSpace(r.Header.Get(DecisionTokenHeader))
	raw = strings.TrimPrefix(raw, "Bearer ")
	return strings.TrimSpace(raw)
}

// decisionGranted accepts only boolean true or numeric 1. JSON numbers decode into
// any as float64; json.Number is handled for completeness. The string "true" is
// intentionally NOT accepted (matches issuer's strict semantics).
func decisionGranted(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t == 1
	case int:
		return t == 1
	case int64:
		return t == 1
	default:
		return false
	}
}

// expiryTime safely dereferences an optional NumericDate.
func expiryTime(d *jwt.NumericDate) time.Time {
	if d == nil {
		return time.Time{}
	}
	return d.Time
}
