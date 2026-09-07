package gitsig

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/ruleguard"
)

// signedRepo 는 SSH 로 서명한 커밋 하나를 가진 저장소와 그 키를 준다.
func signedRepo(t *testing.T, email string) (repoPath, base, head, pubLine string) {
	t.Helper()

	keyDir := t.TempDir()
	keyPath := filepath.Join(keyDir, "id")
	if out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-f", keyPath, "-N", "", "-q", "-C", email).CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen 사용 불가: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatalf("공개키 읽기: %v", err)
	}
	pubLine = strings.TrimSpace(string(raw))

	repoPath = t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoPath
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", email)
	run("config", "user.name", "signer")
	run("config", "gpg.format", "ssh")
	run("config", "user.signingkey", keyPath+".pub")
	run("commit", "-q", "--allow-empty", "-m", "base")
	base = run("rev-parse", "HEAD")
	run("commit", "-q", "--allow-empty", "-S", "-m", "signed")
	head = run("rev-parse", "HEAD")
	return repoPath, base, head, pubLine
}

func signer(email, pubLine string) ruleguard.BundleSigner {
	return ruleguard.BundleSigner{
		UserID:      "u1",
		Fingerprint: "SHA256:test",
		KeyFormat:   "ssh",
		PublicKey:   pubLine,
		Emails:      []string{email},
	}
}

// 세 조건을 모두 만족하면 verified 다.
func TestEvaluateRange_VerifiedWhenAllThreeHold(t *testing.T) {
	email := "signer@example.com"
	repo, base, head, pub := signedRepo(t, email)

	verdicts, err := EvaluateRangeSignatures(context.Background(), repo, base, head,
		[]ruleguard.BundleSigner{signer(email, pub)})
	if err != nil {
		t.Fatalf("판정 실패: %v", err)
	}
	if len(verdicts) != 1 {
		t.Fatalf("판정 수 = %d, want 1", len(verdicts))
	}
	v := verdicts[0].Verification
	if v.State != StateVerified || v.Reason != ReasonVerified {
		t.Fatalf("state=%q reason=%q, want verified", v.State, v.Reason)
	}
	if v.Signer == nil || v.Signer.Email != email {
		t.Fatalf("서명자 이메일이 비었다: %+v", v.Signer)
	}
}

// committer 가 키 주인이 아니면 서명이 유효해도 verified 가 아니다.
//
// 이 대조를 빼면 남의 이메일로 커밋한 뒤 자기 키로 서명해서 배지를 얻을 수 있다.
func TestEvaluateRange_EmailMismatchIsNotVerified(t *testing.T) {
	repo, base, head, pub := signedRepo(t, "signer@example.com")

	// 키 주인의 이메일을 다른 사람 것으로 둔다 — 앵커 principal 과 committer 가 갈린다.
	verdicts, err := EvaluateRangeSignatures(context.Background(), repo, base, head,
		[]ruleguard.BundleSigner{signer("someone-else@example.com", pub)})
	if err != nil {
		t.Fatalf("판정 실패: %v", err)
	}
	if len(verdicts) != 1 {
		t.Fatalf("판정 수 = %d, want 1", len(verdicts))
	}
	if verdicts[0].Verified() {
		t.Fatal("committer 가 키 주인이 아닌데 verified 로 판정했다")
	}
}

// 폐기 이후의 서명만 무효다. 그 이전 서명은 유효하게 남는다.
func TestEvaluateRange_RevokedOnlyAfterTheRevocationTime(t *testing.T) {
	email := "signer@example.com"
	repo, base, head, pub := signedRepo(t, email)

	past := time.Now().UTC().Add(-24 * time.Hour)
	s := signer(email, pub)
	s.RevokedAt = &past

	verdicts, err := EvaluateRangeSignatures(context.Background(), repo, base, head,
		[]ruleguard.BundleSigner{s})
	if err != nil {
		t.Fatalf("판정 실패: %v", err)
	}
	if verdicts[0].Verification.Reason != ReasonRevokedKey {
		t.Fatalf("reason=%q, want %q", verdicts[0].Verification.Reason, ReasonRevokedKey)
	}

	future := time.Now().UTC().Add(24 * time.Hour)
	s.RevokedAt = &future
	verdicts, err = EvaluateRangeSignatures(context.Background(), repo, base, head,
		[]ruleguard.BundleSigner{s})
	if err != nil {
		t.Fatalf("판정 실패: %v", err)
	}
	if !verdicts[0].Verified() {
		t.Fatalf("폐기 이전 서명인데 무효로 판정했다: %+v", verdicts[0].Verification)
	}
}

// signers 가 없으면 판정을 만들지 않는다. 앵커 없이 물으면 유효한 서명도 N 으로
// 나오므로, 그 상태로 만든 판정은 사실이 아니라 착시다.
func TestEvaluateRange_NoSignersProducesNoVerdict(t *testing.T) {
	repo, base, head, _ := signedRepo(t, "signer@example.com")

	verdicts, err := EvaluateRangeSignatures(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatalf("판정 실패: %v", err)
	}
	if verdicts != nil {
		t.Fatalf("앵커가 없는데 판정을 만들었다: %+v", verdicts)
	}
}
