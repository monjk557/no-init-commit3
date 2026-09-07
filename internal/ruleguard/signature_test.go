package ruleguard

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 실제 ed25519 키페어를 만들어 진짜 서명 커밋을 찍는다. 픽스처를 박아두면 git 버전이
// 올라갔을 때 판독이 깨져도 테스트가 알려주지 못한다.
func signingKey(t *testing.T) (pubPath, pubLine string) {
	t.Helper()
	dir := t.TempDir()
	pubPath = filepath.Join(dir, "id.pub")
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-f", filepath.Join(dir, "id"), "-N", "", "-q", "-C", "signer@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen 사용 불가: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatalf("read pubkey: %v", err)
	}
	return pubPath, strings.TrimSpace(string(raw))
}

// signedRepo 는 초기 커밋 + 서명 커밋 1개를 가진 저장소와, 그 키로 만든 앵커를 준다.
func signedRepo(t *testing.T) (repo, base, signed string, anchor *Anchor) {
	t.Helper()
	pubPath, pubLine := signingKey(t)
	repo, base = initRepo(t)
	gitInRepo(t, repo, "config", "gpg.format", "ssh")
	gitInRepo(t, repo, "config", "user.signingkey", pubPath)

	writeFile(t, repo, "a.txt", "a\n")
	gitInRepo(t, repo, "add", "a.txt")
	gitInRepo(t, repo, "commit", "-q", "-S", "-m", "signed")
	signed = gitInRepo(t, repo, "rev-parse", "HEAD")

	anchor, err := BuildAnchor(context.Background(), []BundleSigner{{
		Fingerprint: "SHA256:test",
		KeyFormat:   "ssh",
		PublicKey:   pubLine,
		Emails:      []string{"signer@example.com"},
	}})
	if err != nil {
		t.Fatalf("BuildAnchor: %v", err)
	}
	t.Cleanup(anchor.Cleanup)

	return repo, base, signed, anchor
}

func sigBySHA(sigs []CommitSignature, sha string) (CommitSignature, bool) {
	for _, s := range sigs {
		if s.SHA == sha {
			return s, true
		}
	}
	return CommitSignature{}, false
}

// 앵커(allowed_signers)가 판정을 바꾼다 — 같은 커밋, 같은 저장소, 앵커 유무만 다르다.
// 이게 "git 은 물어봐야만 알고, 물어볼 때 앵커를 줘야 한다"의 실증이다.
func TestReadRangeSignatures_AnchorDecidesTrust(t *testing.T) {
	repo, base, signed, anchor := signedRepo(t)
	ctx := context.Background()

	withoutAnchor, err := ReadRangeSignatures(ctx, repo, base, signed, nil)
	if err != nil {
		t.Fatalf("anchor 없이 판독 실패: %v", err)
	}
	got, ok := sigBySHA(withoutAnchor, signed)
	if !ok {
		t.Fatalf("서명 커밋이 결과에 없다: %+v", withoutAnchor)
	}
	if got.Trusted() {
		t.Errorf("앵커 없이 신뢰로 판정됐다: %+v", got)
	}

	withAnchor, err := ReadRangeSignatures(ctx, repo, base, signed, anchor)
	if err != nil {
		t.Fatalf("anchor 판독 실패: %v", err)
	}
	got, _ = sigBySHA(withAnchor, signed)
	if !got.Trusted() {
		t.Fatalf("앵커를 줬는데 신뢰가 아니다: %+v", got)
	}
	if got.Signer != "signer@example.com" {
		t.Errorf("서명자 principal 이 안 나온다: %q", got.Signer)
	}
	if got.KeyID == "" {
		t.Error("키 식별자가 비었다")
	}
}

func TestReadRangeSignatures_UnsignedIsAbsenceNotViolation(t *testing.T) {
	repo, base, signed, anchor := signedRepo(t)
	writeFile(t, repo, "b.txt", "b\n")
	gitInRepo(t, repo, "add", "b.txt")
	gitInRepo(t, repo, "commit", "-q", "-m", "unsigned")
	unsigned := gitInRepo(t, repo, "rev-parse", "HEAD")

	sigs, err := ReadRangeSignatures(context.Background(), repo, base, unsigned, anchor)
	if err != nil {
		t.Fatalf("판독 실패: %v", err)
	}
	if len(sigs) != 2 {
		t.Fatalf("범위의 커밋 수가 다르다: %d (%+v)", len(sigs), sigs)
	}

	u, _ := sigBySHA(sigs, unsigned)
	if u.State != SigNone {
		t.Errorf("미서명은 N 이어야 한다: %+v", u)
	}
	if u.Signed() {
		t.Error("미서명인데 Signed() 가 true")
	}

	s, _ := sigBySHA(sigs, signed)
	if !s.Trusted() {
		t.Errorf("같은 범위의 서명 커밋은 신뢰여야 한다: %+v", s)
	}
}

// payload 를 한 바이트라도 고치면 서명이 깨진다 — 서명은 커밋 오브젝트에 묶여 있고
// 다른 커밋으로 옮겨 붙일 수 없다.
func TestReadRangeSignatures_TamperedPayloadIsBad(t *testing.T) {
	repo, base, signed, anchor := signedRepo(t)

	raw := gitInRepo(t, repo, "cat-file", "commit", signed)
	tampered := strings.Replace(raw, "committer tester", "committer forged", 1)
	if tampered == raw {
		t.Fatal("변조 대상 문자열을 못 찾았다 — 픽스처가 바뀌었다")
	}

	cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(tampered + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("위조 오브젝트 기록 실패: %v\n%s", err, out)
	}
	forged := strings.TrimSpace(string(out))

	sigs, err := ReadRangeSignatures(context.Background(), repo, base, forged, anchor)
	if err != nil {
		t.Fatalf("판독 실패: %v", err)
	}
	got, ok := sigBySHA(sigs, forged)
	if !ok {
		t.Fatalf("위조 커밋이 결과에 없다: %+v", sigs)
	}
	if got.State != SigBad {
		t.Errorf("변조된 payload 는 B 여야 한다: %+v", got)
	}
	if got.Trusted() {
		t.Error("변조됐는데 신뢰로 판정됐다")
	}
}

// 범위 전체를 프로세스 한 번으로 읽는다는 계약 — 커밋마다 verify-commit 을 부르면
// push 초크 포인트에서 프로세스가 범위 크기만큼 뜬다.
func TestReadRangeSignatures_ReadsWholeRangeAtOnce(t *testing.T) {
	repo, base, _, anchor := signedRepo(t)
	for _, name := range []string{"c1", "c2", "c3"} {
		writeFile(t, repo, name+".txt", name+"\n")
		gitInRepo(t, repo, "add", name+".txt")
		gitInRepo(t, repo, "commit", "-q", "-m", name)
	}
	head := gitInRepo(t, repo, "rev-parse", "HEAD")

	sigs, err := ReadRangeSignatures(context.Background(), repo, base, head, anchor)
	if err != nil {
		t.Fatalf("판독 실패: %v", err)
	}
	if len(sigs) != 4 { // signed + c1..c3
		t.Fatalf("범위 커밋 수가 다르다: %d", len(sigs))
	}
}

func TestParseSignatureLines_MalformedIsSkipped(t *testing.T) {
	out := "abc\tG\tSHA256:k\tsigner@example.com\n" +
		"\n" + // 빈 줄
		"broken-line-without-tab\n" +
		"def\tN\n" // 서명 없음 — 키/서명자 칼럼이 아예 없다
	sigs := parseSignatureLines(out)
	if len(sigs) != 2 {
		t.Fatalf("파싱 결과가 다르다: %+v", sigs)
	}
	if sigs[0].Signer != "signer@example.com" || sigs[1].State != SigNone {
		t.Errorf("파싱 값이 틀리다: %+v", sigs)
	}
}
