package gitutil

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// zeroHash 는 git 이 "ref 가 없었음" 을 나타낼 때 쓰는 all-zero SHA 다.
const zeroHash = "0000000000000000000000000000000000000000"

// commitRangeFormat 은 커밋당 한 줄로 sha / author email / author date 를 뽑는다.
//
// %aI 는 커밋 자체의 타임존 오프셋을 포함한 strict ISO-8601 이다. 기여 집계는 이
// 오프셋 기준으로 날짜를 자르므로(설계 D8) UTC 로 정규화하지 않는다.
const commitRangeFormat = "%H%x09%aE%x09%aI"

// CommitMeta 는 기여 집계에 필요한 최소 커밋 정보다.
//
// 메시지·tree·diff 를 담지 않는다 — 집계에 쓰이지 않을 뿐 아니라, 이벤트 payload
// 크기를 커밋 수에 비례해 부풀리기 때문이다.
type CommitMeta struct {
	SHA         string `json:"sha"`
	AuthorEmail string `json:"author_email"`
	// AuthoredAt 은 원본 오프셋을 보존한 RFC3339 문자열이다 (예: 2026-08-20T08:00:00+09:00).
	// time.Time 으로 파싱하지 않고 문자열로 전달해 직렬화 과정에서 오프셋이
	// 유실되지 않게 한다.
	AuthoredAt string `json:"authored_at"`
}

// DefaultBranchRef 는 저장소의 default branch full ref 를 반환한다 (예: refs/heads/main).
//
// HEAD 의 symbolic-ref 가 곧 default branch 다 — bare 저장소에서 이 값이
// "이 저장소의 본선" 을 가리키는 git 쪽 진실이다.
func DefaultBranchRef(ctx context.Context, repoPath string) (string, error) {
	out, err := runGit(ctx, repoPath, nil, "symbolic-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// IsDefaultBranch 는 refName 이 저장소의 default branch 인지 판정한다.
//
// 기여 집계는 default branch 도달 커밋만 센다 (설계 D9). feature 브랜치 커밋을
// 같이 세면, squash 머지 후 default branch 에 남는 커밋과 중복되고 CR 생성/머지
// 기여와도 겹쳐 같은 작업이 여러 번 계산된다.
//
// 판정 불가(HEAD 없음 등)면 false — 애매하면 세지 않는 쪽이 안전하다.
func IsDefaultBranch(ctx context.Context, repoPath, refName string) bool {
	if strings.TrimSpace(refName) == "" {
		return false
	}
	defaultRef, err := DefaultBranchRef(ctx, repoPath)
	if err != nil {
		return false
	}
	return defaultRef == refName
}

// ListCommitRange 는 이번 ref 갱신으로 새로 도달 가능해진 커밋을 반환한다.
//
// default branch 하나만 대상으로 하므로(D9) 범위는 oldRev..newRev 로 충분하다.
// oldRev 가 비어 있으면(저장소 최초 push 로 default branch 가 생기는 경우) newRev
// 에서 도달 가능한 커밋 전부가 새 기여다 — 그 이전에 feature 브랜치로 들어온
// 커밋은 default 가 아니었으므로 아직 한 번도 집계되지 않았다.
//
// force-push 면 oldRev..newRev 가 new 쪽에만 있는 커밋을 주는데, 그게 정확히
// 원하는 값이다.
func ListCommitRange(ctx context.Context, repoPath, oldRev, newRev string) ([]CommitMeta, error) {
	if strings.TrimSpace(newRev) == "" {
		return nil, fmt.Errorf("newRev is required")
	}

	rev := newRev
	if !isNullRev(oldRev) {
		rev = oldRev + ".." + newRev
	}

	out, err := runGit(ctx, repoPath, nil, "log", "--pretty="+commitRangeFormat, rev)
	if err != nil {
		return nil, err
	}
	return ParseCommitMeta(out), nil
}

// ListCommitRangeBestEffort 는 default branch 판정과 실패 흡수를 함께 처리한다.
//
// 기여 집계는 push 의 부수 기능이다. 커밋 목록을 못 뽑았다고 push 이벤트 발행
// 자체를 실패시키면 CI 트리거·CR 동기화까지 같이 죽으므로 여기서만 흡수한다.
// 발행 지점이 6곳이라 "default branch 인가" 와 "실패를 삼키는 방식" 을 각자
// 구현하면 곧 어긋나므로 한 곳에 둔다.
func ListCommitRangeBestEffort(ctx context.Context, repoPath, oldRev, newRev, refName string) []CommitMeta {
	if !IsDefaultBranch(ctx, repoPath, refName) {
		return nil
	}
	commits, err := ListCommitRange(ctx, repoPath, oldRev, newRev)
	if err != nil {
		return nil
	}
	return commits
}

func runGit(ctx context.Context, repoPath string, stdin *bytes.Buffer, args ...string) ([]byte, error) {
	full := append([]string{"--git-dir", repoPath}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := OutputWithLimit(cmd, DefaultMaxOutput)
	if err != nil {
		return nil, fmt.Errorf("git %s failed: %s", args[0], strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// ParseCommitMeta 는 형식이 깨진 줄을 조용히 버린다. 기여 집계는 best-effort 라
// 한 줄 때문에 push 이벤트 전체를 실패시키지 않는다.
func ParseCommitMeta(out []byte) []CommitMeta {
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return []CommitMeta{}
	}
	lines := strings.Split(raw, "\n")
	commits := make([]CommitMeta, 0, len(lines))
	for _, line := range lines {
		parts := strings.Split(strings.TrimSpace(line), "\t")
		if len(parts) != 3 || parts[0] == "" {
			continue
		}
		commits = append(commits, CommitMeta{
			SHA:         parts[0],
			AuthorEmail: parts[1],
			AuthoredAt:  parts[2],
		})
	}
	return commits
}

func isNullRev(rev string) bool {
	rev = strings.TrimSpace(rev)
	return rev == "" || rev == zeroHash
}
