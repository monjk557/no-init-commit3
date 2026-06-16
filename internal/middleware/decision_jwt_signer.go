// decision_jwt_signer.go mints short-lived HS256 "decision" JWTs using the same
// shared secret the DecisionVerifier checks — the opposite direction of the usual
// orchestrator→executor flow.
//
// Why git-executor mints: SSH `git lfs` has no HTTP credentials. When a client runs
// `git-lfs-authenticate`, git-executor (which has already authenticated the SSH user)
// must hand back a token the orchestrator's LFS HTTP API will accept. The orchestrator
// verifies decision JWTs (iss/aud/sub/exp + HS256 over DECISION_JWT_SECRET) and accepts
// any audience in its ALLOWED_AUDIENCES set — including "git-executor". So a token minted
// here with sub=<user uuid> authenticates the subsequent batch/verify/lock calls.
//
// Trust model: this is the SAME symmetric secret used for verification. Anyone holding it
// can mint a token for any subject, so the secret remains a trusted-infra credential and
// these tokens are deliberately short-lived. The orchestrator counterpart is
// collabops-api-orchestrator app/services/authz/decision_jwt.py (create/verify) and
// app/shared/dependencies/auth.py (get_current_user accepts decision JWTs).
package middleware

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/collabops/git-executor/internal/config"
)

// DecisionSigner mints decision tokens against the shared HS256 secret. It is the
// minting counterpart to DecisionVerifier and honours the same iss/aud contract.
type DecisionSigner struct {
	cfg config.DecisionJWTConfig
	key []byte
}

// NewDecisionSigner builds a signer from the same DecisionJWTConfig the verifier uses.
func NewDecisionSigner(cfg config.DecisionJWTConfig) *DecisionSigner {
	return &DecisionSigner{cfg: cfg, key: []byte(cfg.Secret)}
}

// Enabled reports whether signing is possible. Without the shared secret minting is a
// no-op so the caller degrades gracefully (omit the token rather than fail the request).
func (s *DecisionSigner) Enabled() bool {
	return s != nil && len(s.key) > 0
}

// Mint returns a signed, short-lived decision token authorizing `subject` (a user UUID)
// to perform `action` on `object`, valid for `ttl` from `now`. `now` is injected for
// deterministic tests. The claim set mirrors what DecisionVerifier / the orchestrator's
// verify_decision_jwt require: iss, aud, sub(UUID), obj, act, dec=true, iat, exp, jti.
func (s *DecisionSigner) Mint(subject, object, action string, ttl time.Duration, now time.Time) (string, error) {
	if !s.Enabled() {
		return "", errors.New("decision signer disabled: no shared secret configured")
	}
	if _, err := uuid.Parse(strings.TrimSpace(subject)); err != nil {
		// The orchestrator rejects a non-UUID sub; fail here with a clear cause.
		return "", fmt.Errorf("decision token subject must be a UUID, got %q: %w", subject, err)
	}

	claims := &decisionTokenClaims{
		Object:   object,
		Action:   action,
		Decision: true,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.cfg.Issuer,
			Audience:  jwt.ClaimStrings{s.cfg.Audience},
			Subject:   subject,
			ID:        uuid.NewString(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.key)
	if err != nil {
		return "", fmt.Errorf("sign decision token: %w", err)
	}
	return signed, nil
}
