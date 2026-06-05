package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/collabops/git-executor/internal/config"
)

const testSecret = "test-decision-secret-0123456789"

func testConfig(required bool, secret string) config.DecisionJWTConfig {
	return config.DecisionJWTConfig{
		Secret:        secret,
		Required:      required,
		Audience:      "git-executor",
		Issuer:        "collabops-o2",
		LeewaySeconds: 5,
	}
}

// baseClaims returns a valid, gateway-attested claim set mirroring the orchestrator
// issuer payload. Tests mutate individual fields to exercise failure modes.
func baseClaims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":              "collabops-o2",
		"aud":              "git-executor",
		"sub":              uuid.NewString(),
		"obj":              "collabops:repository:R_abc123",
		"act":              "git.receive-pack",
		"dec":              true,
		"iat":              now.Unix(),
		"exp":              now.Add(60 * time.Second).Unix(),
		"jti":              uuid.NewString(),
		"gateway_attested": true,
	}
}

func mintHS256(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("failed to mint token: %v", err)
	}
	return signed
}

func mintNone(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("failed to mint none token: %v", err)
	}
	return signed
}

// --- Verify (unit) -----------------------------------------------------------

func TestVerify_ValidAttestedToken(t *testing.T) {
	v := NewDecisionVerifier(testConfig(false, testSecret), nil)
	claims := baseClaims()
	got, err := v.Verify(mintHS256(t, testSecret, claims))
	if err != nil {
		t.Fatalf("expected valid token, got error: %v", err)
	}
	if !got.GatewayAttested {
		t.Errorf("expected GatewayAttested=true")
	}
	if got.Subject != claims["sub"] {
		t.Errorf("subject mismatch: got %q want %q", got.Subject, claims["sub"])
	}
	if got.Audience != "git-executor" {
		t.Errorf("audience mismatch: got %q", got.Audience)
	}
}

func TestVerify_NonAttestedTokenStillVerifies(t *testing.T) {
	// Absence of gateway_attested is NOT a verification failure — the middleware,
	// not Verify, decides what to do about it.
	v := NewDecisionVerifier(testConfig(false, testSecret), nil)
	claims := baseClaims()
	delete(claims, "gateway_attested")
	got, err := v.Verify(mintHS256(t, testSecret, claims))
	if err != nil {
		t.Fatalf("non-attested token should still verify: %v", err)
	}
	if got.GatewayAttested {
		t.Errorf("expected GatewayAttested=false when claim absent")
	}
}

func TestVerify_RejectsInvalidTokens(t *testing.T) {
	v := NewDecisionVerifier(testConfig(false, testSecret), nil)

	tests := []struct {
		name   string
		token  func(t *testing.T) string
		reason string
	}{
		{
			name:   "wrong signing secret",
			reason: "signature must not validate against a different key",
			token:  func(t *testing.T) string { return mintHS256(t, "another-secret", baseClaims()) },
		},
		{
			name:   "alg none downgrade",
			reason: "alg=none must be rejected by the HS256 pin",
			token:  func(t *testing.T) string { return mintNone(t, baseClaims()) },
		},
		{
			name:   "expired",
			reason: "exp in the past must fail",
			token: func(t *testing.T) string {
				c := baseClaims()
				past := time.Now().Add(-1 * time.Hour)
				c["iat"] = past.Unix()
				c["exp"] = past.Add(60 * time.Second).Unix()
				return mintHS256(t, testSecret, c)
			},
		},
		{
			name:   "wrong audience",
			reason: "aud not git-executor must fail",
			token: func(t *testing.T) string {
				c := baseClaims()
				c["aud"] = "piper"
				return mintHS256(t, testSecret, c)
			},
		},
		{
			name:   "wrong issuer",
			reason: "iss not collabops-o2 must fail",
			token: func(t *testing.T) string {
				c := baseClaims()
				c["iss"] = "evil"
				return mintHS256(t, testSecret, c)
			},
		},
		{
			name:   "missing exp",
			reason: "exp is required",
			token: func(t *testing.T) string {
				c := baseClaims()
				delete(c, "exp")
				return mintHS256(t, testSecret, c)
			},
		},
		{
			name:   "iat in the future",
			reason: "future iat signals clock manipulation",
			token: func(t *testing.T) string {
				c := baseClaims()
				future := time.Now().Add(1 * time.Hour)
				c["iat"] = future.Unix()
				c["exp"] = future.Add(60 * time.Second).Unix()
				return mintHS256(t, testSecret, c)
			},
		},
		{
			name:   "missing sub",
			reason: "sub is required",
			token: func(t *testing.T) string {
				c := baseClaims()
				delete(c, "sub")
				return mintHS256(t, testSecret, c)
			},
		},
		{
			name:   "non-uuid jti",
			reason: "jti must be a UUID",
			token: func(t *testing.T) string {
				c := baseClaims()
				c["jti"] = "not-a-uuid"
				return mintHS256(t, testSecret, c)
			},
		},
		{
			name:   "dec false",
			reason: "dec must grant permission",
			token: func(t *testing.T) string {
				c := baseClaims()
				c["dec"] = false
				return mintHS256(t, testSecret, c)
			},
		},
		{
			name:   "dec string true rejected",
			reason: "string \"true\" must not be accepted",
			token: func(t *testing.T) string {
				c := baseClaims()
				c["dec"] = "true"
				return mintHS256(t, testSecret, c)
			},
		},
		{
			name:   "garbage",
			reason: "non-JWT input must fail",
			token:  func(t *testing.T) string { return "not.a.jwt" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := v.Verify(tt.token(t)); err == nil {
				t.Errorf("expected error (%s), got nil", tt.reason)
			}
		})
	}
}

func TestVerify_AcceptsNumericDecision(t *testing.T) {
	// The issuer's contract accepts dec as boolean true OR numeric 1. JSON numbers
	// decode into any as float64, so verify the end-to-end numeric path.
	v := NewDecisionVerifier(testConfig(false, testSecret), nil)
	c := baseClaims()
	c["dec"] = 1
	if _, err := v.Verify(mintHS256(t, testSecret, c)); err != nil {
		t.Fatalf("numeric dec=1 must be accepted: %v", err)
	}
}

func TestDecisionGranted(t *testing.T) {
	cases := []struct {
		in   any
		want bool
	}{
		{true, true},
		{false, false},
		{float64(1), true},
		{float64(0), false},
		{int(1), true},
		{int64(1), true},
		{"true", false}, // string must never grant
		{nil, false},
	}
	for _, c := range cases {
		if got := decisionGranted(c.in); got != c.want {
			t.Errorf("decisionGranted(%#v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// --- Middleware (integration) ------------------------------------------------

func runRequest(v *DecisionVerifier, header, token, path string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	exempt := func(p string) bool { return p == "/health" }
	r.Use(v.Middleware(exempt))
	handler := func(c *gin.Context) {
		if val, ok := c.Get(DecisionClaimsContextKey); ok {
			if dc, ok := val.(*DecisionClaims); ok {
				c.Header("X-Test-Sub", dc.Subject)
			}
		}
		c.String(http.StatusOK, "ok")
	}
	r.GET("/repos/o/r/info/refs", handler)
	r.GET("/health", handler)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set(header, token)
	}
	r.ServeHTTP(w, req)
	return w
}

func TestMiddleware_DisabledIsNoOp(t *testing.T) {
	// No secret → verification disabled. Even a garbage token passes (cannot verify).
	v := NewDecisionVerifier(testConfig(true, ""), nil) // Required ignored when disabled
	if v.Enabled() {
		t.Fatalf("verifier should be disabled without a secret")
	}
	w := runRequest(v, DecisionTokenHeader, "garbage", "/repos/o/r/info/refs")
	if w.Code != http.StatusOK {
		t.Errorf("disabled verifier must pass through, got %d", w.Code)
	}
}

func TestMiddleware_DualAccept(t *testing.T) {
	v := NewDecisionVerifier(testConfig(false, testSecret), nil)

	t.Run("valid attested token passes and stashes claims", func(t *testing.T) {
		tok := mintHS256(t, testSecret, baseClaims())
		w := runRequest(v, DecisionTokenHeader, tok, "/repos/o/r/info/refs")
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		if w.Header().Get("X-Test-Sub") == "" {
			t.Errorf("expected verified claims in context")
		}
	})

	t.Run("missing token falls back to allow", func(t *testing.T) {
		w := runRequest(v, DecisionTokenHeader, "", "/repos/o/r/info/refs")
		if w.Code != http.StatusOK {
			t.Errorf("dual-accept must allow missing token, got %d", w.Code)
		}
	})

	t.Run("non-attested token falls back to allow", func(t *testing.T) {
		c := baseClaims()
		delete(c, "gateway_attested")
		w := runRequest(v, DecisionTokenHeader, mintHS256(t, testSecret, c), "/repos/o/r/info/refs")
		if w.Code != http.StatusOK {
			t.Errorf("dual-accept must allow non-attested token, got %d", w.Code)
		}
	})

	t.Run("present but invalid token falls back to allow (non-rejecting)", func(t *testing.T) {
		// Dual-accept must NOT reject — upstream legitimately relays tokens this
		// verifier doesn't yet accept (e.g. aud="api-service"). Rejecting here would
		// break live push/pull the moment the secret is set. Logged, but allowed.
		w := runRequest(v, DecisionTokenHeader, mintHS256(t, "wrong", baseClaims()), "/repos/o/r/info/refs")
		if w.Code != http.StatusOK {
			t.Errorf("dual-accept must allow (only log) an invalid token, got %d", w.Code)
		}
	})

	t.Run("wrong-audience token falls back to allow (the real-world case)", func(t *testing.T) {
		// Exactly the git-http Layer-1 path: token minted with aud="api-service".
		c := baseClaims()
		c["aud"] = "api-service"
		w := runRequest(v, DecisionTokenHeader, mintHS256(t, testSecret, c), "/repos/o/r/info/refs")
		if w.Code != http.StatusOK {
			t.Errorf("dual-accept must not break on aud mismatch, got %d", w.Code)
		}
	})

	t.Run("bearer prefix tolerated", func(t *testing.T) {
		tok := "Bearer " + mintHS256(t, testSecret, baseClaims())
		w := runRequest(v, DecisionTokenHeader, tok, "/repos/o/r/info/refs")
		if w.Code != http.StatusOK {
			t.Errorf("expected bearer-prefixed token to pass, got %d", w.Code)
		}
	})
}

func TestMiddleware_Strict(t *testing.T) {
	v := NewDecisionVerifier(testConfig(true, testSecret), nil)

	t.Run("valid attested token passes", func(t *testing.T) {
		w := runRequest(v, DecisionTokenHeader, mintHS256(t, testSecret, baseClaims()), "/repos/o/r/info/refs")
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})

	t.Run("missing token rejected", func(t *testing.T) {
		w := runRequest(v, DecisionTokenHeader, "", "/repos/o/r/info/refs")
		if w.Code != http.StatusForbidden {
			t.Errorf("strict mode must reject missing token, got %d", w.Code)
		}
	})

	t.Run("non-attested token rejected", func(t *testing.T) {
		c := baseClaims()
		delete(c, "gateway_attested")
		w := runRequest(v, DecisionTokenHeader, mintHS256(t, testSecret, c), "/repos/o/r/info/refs")
		if w.Code != http.StatusForbidden {
			t.Errorf("strict mode must reject non-attested token, got %d", w.Code)
		}
	})

	t.Run("invalid token rejected", func(t *testing.T) {
		w := runRequest(v, DecisionTokenHeader, mintHS256(t, "wrong", baseClaims()), "/repos/o/r/info/refs")
		if w.Code != http.StatusForbidden {
			t.Errorf("strict mode must reject invalid token, got %d", w.Code)
		}
	})

	t.Run("wrong-audience token rejected", func(t *testing.T) {
		c := baseClaims()
		c["aud"] = "api-service"
		w := runRequest(v, DecisionTokenHeader, mintHS256(t, testSecret, c), "/repos/o/r/info/refs")
		if w.Code != http.StatusForbidden {
			t.Errorf("strict mode must reject aud mismatch, got %d", w.Code)
		}
	})

	t.Run("exempt path bypasses verification", func(t *testing.T) {
		w := runRequest(v, DecisionTokenHeader, "", "/health")
		if w.Code != http.StatusOK {
			t.Errorf("exempt path must pass without a token even in strict mode, got %d", w.Code)
		}
	})
}
