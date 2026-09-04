package gitutil

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// BranchRefPrefix 는 브랜치 ref 의 고정 prefix 다.
const BranchRefPrefix = "refs/heads/"

// HeadRefName 은 HEAD 가 가리키는 브랜치 ref 를 반환한다 (예: refs/heads/main).
//
// bare 저장소에서 이 값이 곧 default branch 다. HEAD 가 detached (SHA 직접
// 참조) 면 빈 문자열을 error 없이 반환한다 — 정상 상태이지 실패가 아니다.
func HeadRefName(ctx context.Context, repoPath string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "symbolic-ref", "-q", "HEAD")
	out, err := OutputWithLimit(cmd, MaxOutputSmall)
	if err != nil {
		// exit 1 = detached HEAD. 그 외에는 진짜 실패.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("git symbolic-ref HEAD failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// RefExists 는 refName 이 실재하는 ref 인지 확인한다.
//
// HEAD 는 존재하지 않는 브랜치를 가리킬 수 있다 (unborn). git init 직후가
// 정확히 그 상태이고, 그 저장소를 clone 하면 git 이
// "remote HEAD refers to nonexistent ref" 로 경고한다. 그래서 HEAD 를 다룰
// 때는 "가리키는 대상이 실재하는가" 를 항상 따로 확인해야 한다.
func RefExists(ctx context.Context, repoPath, refName string) bool {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "show-ref", "--verify", "--quiet", refName)
	return cmd.Run() == nil
}

// SetHeadRef 는 HEAD 를 refName 브랜치로 재지정한다 (symbolic-ref).
//
// bare 저장소에는 워킹트리도 인덱스도 없으므로 이 호출은 checkout 이 아니라
// 파일 한 줄 교체다 — 객체/ref 는 전혀 건드리지 않는다.
func SetHeadRef(ctx context.Context, repoPath, refName string) error {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "symbolic-ref", "HEAD", refName)
	if out, err := CombinedOutputWithLimit(cmd, MaxOutputSmall); err != nil {
		return fmt.Errorf("git symbolic-ref HEAD %s failed: %s: %w", refName, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// AdoptHeadIfUnborn 은 HEAD 가 실재하지 않는 브랜치를 가리키고 있을 때
// candidates 중 하나를 default branch 로 승격시킨다. 승격한 ref 를 반환하고,
// 할 일이 없었으면 빈 문자열을 반환한다.
//
// 왜 필요한가: git receive-pack 은 HEAD 를 절대 자동으로 옮기지 않는다.
// `git init --bare` 로 만든 저장소의 HEAD 는 init.defaultBranch (보통 main) 를
// 가리키는 unborn 상태이고, 첫 push 가 develop 하나만 올려도 HEAD 는 계속
// 존재하지도 않는 main 을 가리킨다. 그 저장소를 clone 하면 아무것도 체크아웃
// 되지 않는다. 그래서 push 경로가 이 승격을 대신 해준다.
//
// HEAD 가 실재하는 브랜치를 가리키고 있으면(=정상) 아무것도 하지 않는다 —
// 기본 브랜치를 push 가 마음대로 바꾸는 일은 없다. detached HEAD 도 건드리지
// 않는다 (의도적으로 그렇게 둔 저장소를 push 가 되돌리면 안 된다).
//
// 후보가 여럿이면 (`git push --all` 로 첫 push) main → master → 사전순 첫
// 브랜치 순으로 고른다. 임의로 고르더라도 같은 입력이면 항상 같은 결과여야
// 재현/디버깅이 가능하다.
func AdoptHeadIfUnborn(ctx context.Context, repoPath string, candidates []string) (string, error) {
	if len(candidates) == 0 {
		return "", nil
	}

	head, err := HeadRefName(ctx, repoPath)
	if err != nil {
		return "", err
	}
	if head == "" {
		// detached — 의도된 상태로 보고 그대로 둔다.
		return "", nil
	}
	if RefExists(ctx, repoPath, head) {
		return "", nil
	}

	chosen := PreferAdoptionRef(existingBranchRefs(ctx, repoPath, candidates))
	if chosen == "" {
		return "", nil
	}
	if err := SetHeadRef(ctx, repoPath, chosen); err != nil {
		return "", err
	}
	return chosen, nil
}

// existingBranchRefs 는 후보 중 실재하는 브랜치 ref 만 중복 없이 남긴다.
func existingBranchRefs(ctx context.Context, repoPath string, candidates []string) []string {
	valid := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, ref := range candidates {
		if !strings.HasPrefix(ref, BranchRefPrefix) {
			continue
		}
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		if RefExists(ctx, repoPath, ref) {
			valid = append(valid, ref)
		}
	}
	return valid
}

// PreferAdoptionRef 는 default branch 후보 중 하나를 결정론적으로 고른다:
// main → master → 사전순 첫 브랜치. 후보가 없으면 빈 문자열.
//
// 임의로 고르더라도 같은 입력이면 항상 같은 결과여야 재현/디버깅이 가능하고,
// HTTP·SSH 두 push 경로가 같은 브랜치를 고르는 것도 이 함수 하나에 달려 있다.
func PreferAdoptionRef(candidates []string) string {
	valid := make([]string, 0, len(candidates))
	for _, ref := range candidates {
		if strings.HasPrefix(ref, BranchRefPrefix) {
			valid = append(valid, ref)
		}
	}
	if len(valid) == 0 {
		return ""
	}
	sort.Strings(valid)
	for _, preferred := range []string{BranchRefPrefix + "main", BranchRefPrefix + "master"} {
		for _, ref := range valid {
			if ref == preferred {
				return ref
			}
		}
	}
	return valid[0]
}
