package gitsig

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/gitutil"
	"github.com/collabops/git-executor/internal/ruleguard"
)

// verdict.go — push 범위의 서명 판정을 만든다.
//
// 배지가 붙으려면 세 가지가 함께 성립해야 한다. 서명이 유효한가(git 이 계산), 그 키가
// 등록된 것인가(signers 목록), 그리고 커밋의 committer 이메일이 키 주인의 것인가.
// 세 번째를 빼면 자기 키로 서명하면서 committer 만 남의 이메일로 적어도 통과하므로,
// 사칭을 막으려고 만든 표시가 사칭 수단이 된다.

// CommitVerdict 는 커밋 하나의 판정이다.
type CommitVerdict struct {
	SHA          string
	Verification Verification
}

// Verified 는 배지를 붙일 수 있는가.
func (v CommitVerdict) Verified() bool {
	return v.Verification.State == StateVerified
}

// EvaluateRangeSignatures 는 old..new 범위의 커밋마다 판정을 만든다.
//
// signers 가 비어 있으면 nil 을 돌려준다. 앵커가 없으면 유효한 서명도 "서명 없음" 으로
// 나오므로, 그 상태로 만든 판정은 사실이 아니라 착시다. 호출부는 사실을 생산하지 않고
// 미검증으로 두어야 한다 — 계산하지 못한 것으로 push 를 거부하지 않는다.
func EvaluateRangeSignatures(
	ctx context.Context,
	repoPath, oldHash, newHash string,
	signers []ruleguard.BundleSigner,
) ([]CommitVerdict, error) {
	anchor, err := ruleguard.BuildAnchor(ctx, signers)
	if err != nil {
		return nil, err
	}
	if anchor == nil {
		return nil, nil
	}
	defer anchor.Cleanup()

	sigs, err := ruleguard.ReadRangeSignatures(ctx, repoPath, oldHash, newHash, anchor)
	if err != nil {
		return nil, err
	}
	if len(sigs) == 0 {
		return nil, nil
	}

	// 서명 시각은 커밋의 committer 시각으로 본다. 폐기 판정이 "언제 서명했나" 를
	// 필요로 하는데, 서명 자체에는 우리가 신뢰할 시각이 없다.
	meta := readCommitMeta(ctx, repoPath, sigs)

	byPrincipal := indexSigners(signers)
	verdicts := make([]CommitVerdict, 0, len(sigs))
	for _, sig := range sigs {
		verdicts = append(verdicts, CommitVerdict{
			SHA:          sig.SHA,
			Verification: judge(sig, meta[sig.SHA], byPrincipal),
		})
	}
	return verdicts, nil
}

// judge 는 커밋 하나에 세 조건과 폐기·만료를 적용한다.
func judge(sig ruleguard.CommitSignature, m commitMeta, byPrincipal map[string]ruleguard.BundleSigner) Verification {
	now := time.Now().UTC()
	base := Verification{
		SignerType: SignerUser,
		VerifiedAt: &now,
	}

	if !sig.Signed() {
		base.State = StateUnsigned
		base.Reason = ReasonUnsigned
		return base
	}

	base.Signer = &Signer{KeyID: sig.KeyID, Fingerprint: sig.KeyID}

	// git 이 앵커를 보고도 신뢰하지 못했다면 그 이유가 판정이다.
	switch sig.State {
	case ruleguard.SigBad:
		base.State = StateUnverified
		base.Reason = ReasonBadSignature
		return base
	case ruleguard.SigNoKey, ruleguard.SigGoodUnknown:
		// 앵커에 없는 키다. 서명은 유효할 수 있으나 우리가 아는 키가 아니다.
		base.State = StateUnverified
		base.Reason = ReasonUnknownKey
		return base
	}

	// 앵커가 principal 로 돌려준 이메일이 곧 키 주인이다.
	signer, known := byPrincipal[strings.ToLower(principalEmail(sig.Signer))]
	if !known {
		base.State = StateUnverified
		base.Reason = ReasonUnknownKey
		return base
	}
	base.Signer.Email = principalEmail(sig.Signer)
	base.Signer.Fingerprint = signer.Fingerprint

	if !ownsEmail(signer, m.CommitterEmail) {
		base.State = StateUnverified
		base.Reason = ReasonEmailMismatch
		return base
	}

	if signer.RevokedAt != nil && !m.SignedAt.IsZero() && m.SignedAt.After(*signer.RevokedAt) {
		base.State = StateUnverified
		base.Reason = ReasonRevokedKey
		return base
	}
	if signer.ExpiresAt != nil && !m.SignedAt.IsZero() && m.SignedAt.After(*signer.ExpiresAt) {
		base.State = StateUnverified
		base.Reason = ReasonExpiredKey
		return base
	}

	base.State = StateVerified
	base.Reason = ReasonVerified
	return base
}

// indexSigners 는 이메일로 서명자를 찾을 수 있게 한다. 한 사람이 이메일을 여럿 가질 수
// 있고, 같은 이메일에 키가 여럿일 수도 있다 — 앵커가 그 이메일을 principal 로 돌려준
// 이상 어느 키든 그 사람의 것이다.
func indexSigners(signers []ruleguard.BundleSigner) map[string]ruleguard.BundleSigner {
	byPrincipal := make(map[string]ruleguard.BundleSigner, len(signers))
	for _, s := range signers {
		for _, email := range s.Emails {
			byPrincipal[strings.ToLower(email)] = s
		}
	}
	return byPrincipal
}

// ownsEmail 은 committer 이메일이 그 키 주인의 것인지 본다.
func ownsEmail(signer ruleguard.BundleSigner, committerEmail string) bool {
	if committerEmail == "" {
		return false
	}
	for _, email := range signer.Emails {
		if strings.EqualFold(email, committerEmail) {
			return true
		}
	}
	return false
}

// principalEmail 은 %GS 가 돌려준 principal 에서 이메일만 꺼낸다. ssh 는 이메일을
// 그대로, gpg 는 "이름 <이메일>" 형태로 돌려준다.
func principalEmail(principal string) string {
	if i := strings.LastIndex(principal, "<"); i >= 0 {
		if j := strings.Index(principal[i:], ">"); j > 0 {
			return principal[i+1 : i+j]
		}
	}
	return strings.TrimSpace(principal)
}

// commitMeta 는 판정에 필요한 커밋 사실이다. 서명 자체에서는 얻을 수 없다.
type commitMeta struct {
	CommitterEmail string
	SignedAt       time.Time
}

// readCommitMeta 는 판정 대상 커밋들의 committer 이메일과 시각을 한 번에 읽는다.
//
// 커밋마다 부르면 범위 크기만큼 프로세스가 뜬다. push 는 초크 포인트라 그 비용을
// 감당할 수 없다. 읽지 못하면 빈 값이고, 그때 이메일 대조가 실패해 미검증이 된다 —
// 확인하지 못한 것을 통과시키지 않는다.
func readCommitMeta(ctx context.Context, repoPath string, sigs []ruleguard.CommitSignature) map[string]commitMeta {
	meta := make(map[string]commitMeta, len(sigs))
	if len(sigs) == 0 {
		return meta
	}

	shas := make([]string, 0, len(sigs))
	for _, sig := range sigs {
		shas = append(shas, sig.SHA)
	}

	args := append([]string{"log", "--no-walk", "--no-abbrev", "--format=%H%x09%ce%x09%cI"}, shas...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return meta
	}

	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		m := commitMeta{CommitterEmail: parts[1]}
		if ts, err := time.Parse(time.RFC3339, parts[2]); err == nil {
			m.SignedAt = ts.UTC()
		}
		meta[parts[0]] = m
	}
	return meta
}
