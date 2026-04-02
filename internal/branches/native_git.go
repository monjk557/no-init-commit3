package branches

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const nativeGitTimeout = 5 * time.Second

// BranchSortField는 브랜치 정렬 기준을 나타냅니다.
type BranchSortField string

const (
	// BranchSortNewest는 최근 커밋 순 정렬입니다 (기본값).
	BranchSortNewest BranchSortField = "-committerdate"
	// BranchSortOldest는 오래된 커밋 순 정렬입니다.
	BranchSortOldest BranchSortField = "committerdate"
	// BranchSortName은 브랜치명 ABC 순 정렬입니다.
	BranchSortName BranchSortField = "refname"
)

// ParseBranchSort는 쿼리 파라미터 문자열을 BranchSortField로 변환합니다.
// 지원: "newest" (기본), "oldest", "name"
func ParseBranchSort(s string) BranchSortField {
	switch s {
	case "oldest":
		return BranchSortOldest
	case "name":
		return BranchSortName
	default:
		return BranchSortNewest
	}
}

// NativeGetBranches는 native git으로 모든 브랜치를 조회합니다.
// go-git의 repo.References() + ref.IsBranch() 조합을 대체합니다.
func NativeGetBranches(ctx context.Context, repoPath string, sort BranchSortField) ([]BranchInfo, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	// format: refname:short\tsha\tdate\trefname\tcommitteremail
	// NOTE: Using tab separator instead of %x00 because Apple Git's for-each-ref
	// does not interpret %x00 as null byte (unlike git log --format).
	format := "%(refname:short)\t%(objectname)\t%(committerdate:iso8601-strict)\t%(refname)\t%(committeremail)"
	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath,
		"for-each-ref", "refs/heads/",
		"--format="+format,
		"--sort="+string(sort))

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git for-each-ref failed: %s: %w", strings.TrimSpace(stderr.String()), err)
	}

	// HEAD를 조회하여 현재 브랜치 판별
	headRef := nativeGetHead(ctx, repoPath)

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return []BranchInfo{}, nil
	}

	lines := strings.Split(raw, "\n")
	branches := make([]BranchInfo, 0, len(lines))

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) < 5 {
			continue
		}

		name := parts[0]
		commitSHA := parts[1]
		dateStr := parts[2]
		refPath := parts[3]
		committerEmail := strings.Trim(parts[4], "<>")

		date, _ := time.Parse(time.RFC3339, dateStr)

		branch := BranchInfo{
			Name:           name,
			CommitSHA:      commitSHA,
			RefPath:        refPath,
			IsHead:         headRef == refPath || headRef == "refs/heads/"+name,
			Date:           date,
			CommitterEmail: committerEmail,
		}

		branches = append(branches, branch)
	}

	return branches, nil
}

// NativeGetSimpleBranches는 native git으로 간단한 브랜치 목록을 조회합니다.
// go-git의 GetBranches (package-level) 함수를 대체합니다.
func NativeGetSimpleBranches(ctx context.Context, repoPath string) ([]SimpleBranchInfo, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	format := "%(refname:short)\t%(objectname)"
	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath,
		"for-each-ref", "refs/heads/",
		"--format="+format,
		"--sort=refname")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git for-each-ref failed: %s: %w", strings.TrimSpace(stderr.String()), err)
	}

	headRef := nativeGetHead(ctx, repoPath)

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return []SimpleBranchInfo{}, nil
	}

	lines := strings.Split(raw, "\n")
	branches := make([]SimpleBranchInfo, 0, len(lines))

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			continue
		}

		name := parts[0]
		hash := parts[1]

		branch := SimpleBranchInfo{
			Name:      name,
			Hash:      hash,
			IsRemote:  false,
			IsCurrent: headRef == "refs/heads/"+name,
		}

		branches = append(branches, branch)
	}

	return branches, nil
}

// NativeCheckBranchExists는 native git으로 브랜치 존재 여부를 확인합니다.
func NativeCheckBranchExists(ctx context.Context, repoPath, branchName string) (bool, string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	refName := "refs/heads/" + branchName
	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "rev-parse", "--verify", refName)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		// 브랜치가 없는 경우
		return false, "", nil
	}

	hash := strings.TrimSpace(string(out))
	return true, hash, nil
}

// NativeCalculateAheadBehind는 native git으로 두 브랜치의 ahead/behind를 계산합니다.
// go-git의 commit.MergeBase() + countCommitsBetween()을 대체합니다.
func NativeCalculateAheadBehind(ctx context.Context, repoPath, branchRef, baseRef string) (*BranchAheadBehind, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	// git rev-list --count --left-right base...branch
	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath,
		"rev-list", "--count", "--left-right",
		baseRef+"..."+branchRef)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git rev-list failed: %s: %w", strings.TrimSpace(stderr.String()), err)
	}

	// 출력 형식: "behind\tahead\n"
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) != 2 {
		return &BranchAheadBehind{Ahead: 0, Behind: 0}, nil
	}

	behind, _ := strconv.Atoi(parts[0])
	ahead, _ := strconv.Atoi(parts[1])

	return &BranchAheadBehind{
		Ahead:  ahead,
		Behind: behind,
	}, nil
}

// NativeResolveRef는 native git으로 ref를 커밋 해시로 resolve합니다.
func NativeResolveRef(ctx context.Context, repoPath, ref string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	target := ref
	if target == "" {
		target = "HEAD"
	}

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "rev-parse", target)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to resolve reference: %s", ref)
	}

	return strings.TrimSpace(string(out)), nil
}

// NativeMergeBase finds the merge base of two commits.
func NativeMergeBase(ctx context.Context, repoPath, hash1, hash2 string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "merge-base", hash1, hash2)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("no common ancestor found between commits")
	}
	return strings.TrimSpace(string(out)), nil
}

// NativeUpdateRef sets a ref to point to a given hash.
// Equivalent to: git update-ref <refName> <hash>
func NativeUpdateRef(ctx context.Context, repoPath, refName, hash string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "update-ref", refName, hash)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git update-ref failed: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

// NativeDeleteRef deletes a ref.
// Equivalent to: git update-ref -d <refName>
func NativeDeleteRef(ctx context.Context, repoPath, refName string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "update-ref", "-d", refName)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git update-ref -d failed: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

// NativeSetSymbolicRef sets HEAD to point to a branch ref.
// Equivalent to: git symbolic-ref HEAD refs/heads/<name>
func NativeSetSymbolicRef(ctx context.Context, repoPath, target string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "symbolic-ref", "HEAD", target)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git symbolic-ref failed: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

// NativeIsFastForward checks if oldHash is an ancestor of newHash.
// Equivalent to: git merge-base --is-ancestor <old> <new>
func NativeIsFastForward(ctx context.Context, repoPath, oldHash, newHash string) (bool, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "merge-base", "--is-ancestor", oldHash, newHash)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	// Exit code 1 = not ancestor, exit code != 0 = error
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor failed: %w", err)
}

// nativeGetHead는 HEAD가 가리키는 브랜치 ref를 반환합니다.
func nativeGetHead(ctx context.Context, repoPath string) string {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "symbolic-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
