package context

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	nativeGitTimeout = 10 * time.Second
	nullSHA          = "0000000000000000000000000000000000000000"
)

// isNullSHA는 null SHA(최초 push)인지 확인합니다.
func isNullSHA(sha string) bool {
	return sha == nullSHA
}

// nativeDiffFiles는 두 커밋 간 변경된 파일 경로 목록을 반환합니다.
func nativeDiffFiles(ctx context.Context, repoPath, oldRev, newRev string) ([]string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if isNullSHA(oldRev) {
		// 최초 push: newRev의 전체 파일 목록
		cmd = exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "diff-tree", "-r", "--no-commit-id", "--name-only", "--root", newRev)
	} else {
		cmd = exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "diff-tree", "-r", "--no-commit-id", "--name-only", oldRev, newRev)
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff-tree failed: %s: %w", strings.TrimSpace(stderr.String()), err)
	}

	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}

	return files, nil
}

// nativeListTreeFiles는 특정 커밋의 지정 경로 하위 파일 목록을 반환합니다.
func nativeListTreeFiles(ctx context.Context, repoPath, sha, pathPrefix string) ([]string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "ls-tree", "-r", "--name-only", sha, pathPrefix)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		// 경로가 존재하지 않으면 빈 목록 반환
		return nil, nil
	}

	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}

	return files, nil
}

// nativeShowFile는 특정 커밋의 파일 내용을 반환합니다.
func nativeShowFile(ctx context.Context, repoPath, sha, filePath string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	spec := sha + ":" + filePath
	cmd := exec.CommandContext(cmdCtx, "git", "-c", "core.quotePath=false", "--git-dir", repoPath, "show", spec)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git show %s failed: %s: %w", spec, strings.TrimSpace(stderr.String()), err)
	}

	return string(out), nil
}

// nativeValidateSHA는 SHA가 레포에 존재하는지 확인합니다.
func nativeValidateSHA(ctx context.Context, repoPath, sha string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "cat-file", "-e", sha)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("SHA not found: %s", sha)
	}

	return nil
}
