package ruleguard

import (
	"context"
	"os"
	"os/exec"
	"strings"

	"github.com/collabops/git-executor/internal/gitutil"
)

// signature.go — 커밋 서명 판독 어댑터.
//
// git 은 서명을 **자동으로 검사하지 않는다.** receive-pack 은 gpgsig 헤더를 쳐다보지도
// 않으므로, 판정은 우리가 명시적으로 물어봐야만 존재한다. 그리고 물어볼 때 **신뢰 앵커**
// (allowed_signers)를 같이 주지 않으면 유효한 서명도 "서명 없음(N)" 으로 나온다 —
// 앵커 유무만으로 같은 커밋의 판정이 뒤집힌다.
//
// 여기서는 "이 서명이 이 키로 유효한가"(수학) + "그 키가 앵커에 있는가"까지만 답한다.
// "그 키 주인이 커밋의 committer 와 같은 사람인가"(이메일 대조)는 호출부가 번들의
// signers 로 판정한다 — 그게 도장의 세 번째 조건이다.

// SignatureState 는 git 이 커밋 하나에 내리는 서명 판정(%G?)이다.
type SignatureState string

const (
	SigGood        SignatureState = "G" // 유효 + 앵커에 있는 키
	SigGoodUnknown SignatureState = "U" // 유효하나 신뢰 미지정
	SigBad         SignatureState = "B" // 변조되었거나 서명 불일치
	SigNoKey       SignatureState = "E" // 키를 못 찾아 검증 불가
	SigNone        SignatureState = "N" // 서명 없음
	SigExpiredSig  SignatureState = "X" // 만료된 서명
	SigExpiredKey  SignatureState = "Y" // 만료된 키의 유효 서명
	SigRevokedKey  SignatureState = "R" // 폐기된 키의 유효 서명
)

// 수학적으로 유효한 상태는 G·U·X·Y·R 이다. 유효성과 신뢰(앵커 등재·이메일 일치)는
// 별개이며, 판정을 합치는 곳은 아직 없다 (push 사실 생산 시점에 생긴다).

// CommitSignature 는 커밋 하나의 서명 판독 결과다.
type CommitSignature struct {
	SHA   string
	State SignatureState
	// KeyID 는 서명에 쓰인 키 식별자(%GK) — SSH 는 SHA256:… 지문.
	KeyID string
	// Signer 는 앵커가 그 키에 대해 말하는 principal(%GS) — SSH 는 allowed_signers 의
	// 이메일. 앵커에 없으면 빈 문자열이라, 이 값이 비었다는 것 자체가 신호다.
	Signer string
}

// Signed 는 서명이 아예 없는 경우와 구분하기 위한 것 — 미서명은 위반이 아니라 부재다.
func (c CommitSignature) Signed() bool { return c.State != SigNone }

// Trusted 는 수학적 유효성 + 앵커 등재를 함께 만족하는가.
func (c CommitSignature) Trusted() bool { return c.State == SigGood }

// sigFormat 은 판독 포맷 — 탭 구분이라 파싱이 안전하다(메시지·이름에 공백이 흔하다).
const sigFormat = "%H%x09%G?%x09%GK%x09%GS"

// ReadRangeSignatures 는 old..new 범위의 커밋 서명을 **프로세스 한 번**으로 판독한다.
//
// 커밋마다 `git verify-commit` 을 부르면 범위 크기만큼 프로세스가 뜬다 — push 는
// 초크 포인트라 그 비용을 감당할 수 없다.
//
// allowedSigners 가 비어 있으면 앵커 없이 묻는 것이라 모든 커밋이 N/E 로 떨어진다.
// 그건 오류가 아니라 "판정할 수 없음" 이므로 호출부가 미검증으로 다룬다.
func ReadRangeSignatures(ctx context.Context, repoPath, oldHash, newHash string, allowedSigners string) ([]CommitSignature, error) {
	rangeSpec := oldHash + ".." + newHash
	if oldHash == zeroOID {
		rangeSpec = newHash
	}
	return readSignatures(ctx, repoPath, allowedSigners, rangeSpec)
}

// ReadCommitSignatures 는 임의의 커밋 목록을 한 번에 판독한다 (--no-walk).
//
// 조회 화면은 범위가 아니라 "이 페이지에 보이는 커밋들" 이라 조상까지 따라갈 이유가
// 없다. 그래도 프로세스는 한 번이어야 한다 — 목록 한 화면이 커밋 수만큼 git 을 띄우면
// 그게 곧 조회 지연이 된다.
func ReadCommitSignatures(ctx context.Context, repoPath string, allowedSigners string, shas []string) ([]CommitSignature, error) {
	if len(shas) == 0 {
		return nil, nil
	}
	return readSignatures(ctx, repoPath, allowedSigners, append([]string{"--no-walk"}, shas...)...)
}

// readSignatures 는 앵커 주입과 판독을 한 곳에 모은다 — 앵커를 요청 단위 임시 파일로
// 주는 규칙이 호출 경로마다 갈라지면, 한쪽만 전역 gitconfig 를 건드리는 사고가 난다.
func readSignatures(ctx context.Context, repoPath, allowedSigners string, revArgs ...string) ([]CommitSignature, error) {
	args := []string{}

	if allowedSigners != "" {
		path, cleanup, err := writeAllowedSigners(allowedSigners)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		// 호출 단위 주입 — 저장소/전역 gitconfig 를 건드리지 않는다. 동시 push 가
		// 서로의 앵커를 덮어쓰는 사고를 원천적으로 없앤다.
		args = append(args, "-c", "gpg.ssh.allowedSignersFile="+path)
	}

	args = append(args, "log", "--no-abbrev", "--format="+sigFormat)
	args = append(args, revArgs...)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return nil, err
	}
	return parseSignatureLines(string(out)), nil
}

// parseSignatureLines 는 sigFormat 출력 한 줄을 하나의 판정으로 옮긴다.
func parseSignatureLines(out string) []CommitSignature {
	var sigs []CommitSignature
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 2 {
			continue
		}
		sig := CommitSignature{SHA: parts[0], State: SignatureState(parts[1])}
		if len(parts) > 2 {
			sig.KeyID = parts[2]
		}
		if len(parts) > 3 {
			sig.Signer = parts[3]
		}
		sigs = append(sigs, sig)
	}
	return sigs
}

// writeAllowedSigners 는 앵커를 임시 파일로 만든다. git 이 파일 경로만 받기 때문이고,
// 요청 단위로 만들고 지워 다른 push 와 섞이지 않게 한다.
func writeAllowedSigners(content string) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", "allowed_signers-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup = func() { _ = os.Remove(f.Name()) }
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return f.Name(), cleanup, nil
}
