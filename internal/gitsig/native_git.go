package gitsig

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/collabops/git-executor/internal/gitutil"
)

// native_git.go — "서명이 달려 있는가" 만 판독하는 어댑터.
//
// %G? 로는 이 질문에 답할 수 없다. 검증 도구(gpg / ssh-keygen)가 없으면 서명이 있는
// 커밋도 N(서명 없음)으로 나오기 때문이다. 실제로 executor 이미지에는 gpg 가 없어,
// GPG 로 서명한 커밋이 미서명으로 표시되는 것을 확인했다. 판정 결과로 서명의 존재를
// 추론하면 도구가 빠진 환경에서 배지가 조용히 거짓말을 한다.
//
// 그래서 커밋 객체의 헤더를 직접 본다. gpgsig 는 서명이 있을 때만 존재하며 검증
// 도구와 무관하다.

// signedSHAs 는 서명 헤더를 가진 커밋의 집합을 돌려준다.
//
// rev-list --header 는 커밋 하나의 원본 헤더를 NUL 로 구분해 출력한다. 프로세스는
// 한 번이다. 판독에 실패하면 빈 집합과 함께 오류를 돌려준다 — 배지를 못 그리는 것과
// 커밋에 서명이 없는 것은 다른 사실이고, 호출부가 그 둘을 구분해 로그로 남긴다.
func signedSHAs(ctx context.Context, repoPath string, shas []string) (map[string]bool, error) {
	signed := make(map[string]bool, len(shas))
	if len(shas) == 0 {
		return signed, nil
	}

	args := append([]string{"rev-list", "--no-walk", "--header"}, shas...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return signed, fmt.Errorf("rev-list --header: %w", err)
	}

	// 레코드 하나 = "<sha>\n<헤더들>\n\n<메시지>" 이고 NUL 로 구분된다.
	for _, record := range strings.Split(string(out), "\x00") {
		record = strings.TrimLeft(record, "\n")
		if record == "" {
			continue
		}
		sha, rest, found := strings.Cut(record, "\n")
		if !found {
			continue
		}
		header, _, _ := strings.Cut(rest, "\n\n")
		if hasSignatureHeader(header) {
			signed[sha] = true
		}
	}
	return signed, nil
}

// hasSignatureHeader 는 헤더 영역에 서명 헤더가 있는지 본다.
//
// gpgsig 는 GPG·SSH·X.509 서명 모두가 쓰는 헤더이고, gpgsig-sha256 은 SHA-256
// 저장소용이다. 메시지 본문에 같은 낱말이 나와도 걸리지 않도록 헤더 영역만 본다.
func hasSignatureHeader(header string) bool {
	for _, line := range strings.Split(header, "\n") {
		if strings.HasPrefix(line, "gpgsig ") || strings.HasPrefix(line, "gpgsig-sha256 ") {
			return true
		}
	}
	return false
}
