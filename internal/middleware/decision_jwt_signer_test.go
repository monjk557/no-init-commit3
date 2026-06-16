package middleware

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/collabops/git-executor/internal/config"
)

func signerTestConfig() config.DecisionJWTConfig {
	return config.DecisionJWTConfig{
		Secret:        "shared-test-secret",
		Audience:      "git-executor",
		Issuer:        "collabops-o2",
		LeewaySeconds: 5,
	}
}

// The minted token must pass the DecisionVerifier — which enforces every check the
// orchestrator's verify_decision_jwt performs — so a successful round-trip is the
// correctness proof that the orchestrator will accept it.
func TestDecisionSigner_MintRoundTripsThroughVerifier(t *testing.T) {
	cfg := signerTestConfig()
	signer := NewDecisionSigner(cfg)
	verifier := NewDecisionVerifier(cfg, nil)

	sub := uuid.NewString()
	token, err := signer.Mint(sub, "repository:acme/web", "git.lfs", lfsTTL(), time.Now())
	if err != nil {
		t.Fatalf("mint failed: %v", err)
	}

	claims, err := verifier.Verify(token)
	if err != nil {
		t.Fatalf("verifier rejected minted token: %v", err)
	}
	if claims.Subject != sub {
		t.Errorf("sub mismatch: got %q want %q", claims.Subject, sub)
	}
	if claims.Audience != "git-executor" {
		t.Errorf("aud mismatch: got %q", claims.Audience)
	}
	if claims.Issuer != "collabops-o2" {
		t.Errorf("iss mismatch: got %q", claims.Issuer)
	}
}

func TestDecisionSigner_RejectsNonUUIDSubject(t *testing.T) {
	signer := NewDecisionSigner(signerTestConfig())
	if _, err := signer.Mint("not-a-uuid", "repository:a/b", "git.lfs", lfsTTL(), time.Now()); err == nil {
		t.Fatal("expected error for non-UUID subject, got nil")
	}
}

func TestDecisionSigner_DisabledWithoutSecret(t *testing.T) {
	cfg := signerTestConfig()
	cfg.Secret = ""
	signer := NewDecisionSigner(cfg)
	if signer.Enabled() {
		t.Fatal("signer should be disabled without a secret")
	}
	if _, err := signer.Mint(uuid.NewString(), "repository:a/b", "git.lfs", lfsTTL(), time.Now()); err == nil {
		t.Fatal("expected error when signing disabled, got nil")
	}
}

func TestDecisionSigner_WrongSecretRejected(t *testing.T) {
	signer := NewDecisionSigner(signerTestConfig())
	token, err := signer.Mint(uuid.NewString(), "repository:a/b", "git.lfs", lfsTTL(), time.Now())
	if err != nil {
		t.Fatalf("mint failed: %v", err)
	}

	otherCfg := signerTestConfig()
	otherCfg.Secret = "a-different-secret"
	if _, err := NewDecisionVerifier(otherCfg, nil).Verify(token); err == nil {
		t.Fatal("verifier with a different secret must reject the token")
	}
	if strings.TrimSpace(token) == "" {
		t.Fatal("token unexpectedly empty")
	}
}

func lfsTTL() time.Duration { return 5 * time.Minute }
