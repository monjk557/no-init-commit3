package blob

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/gitutil"
)

const nativeGitTimeout = 5 * time.Second

// NativeGetBlobContent는 native git으로 blob 내용을 조회합니다.
// go-git의 repo.CommitObject() + commit.Tree() + tree.File() + file.Reader()를 대체합니다.
func NativeGetBlobContent(ctx context.Context, repoPath, ref, path string) ([]byte, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	target := ref
	if target == "" {
		target = "HEAD"
	}

	spec := target + ":" + path

	cmd := exec.CommandContext(cmdCtx, "git", "-c", "core.quotePath=false", "--git-dir", repoPath, "show", spec)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// Blob content can be up to MaxBlobSize (50MB) per blob.service.MaxBlobSize.
	out, err := gitutil.OutputWithLimit(cmd, gitutil.MaxOutputLarge)
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())

		// 경로가 디렉토리인 경우 감지
		if strings.Contains(errMsg, "is a tree") {
			return nil, fmt.Errorf("path is a directory")
		}

		// 파일이 존재하지 않는 경우
		if strings.Contains(errMsg, "does not exist") ||
			strings.Contains(errMsg, "not a tree object") ||
			strings.Contains(errMsg, "fatal: path") {
			return nil, fmt.Errorf("file not found: %s", path)
		}

		// ref 해석 실패
		if strings.Contains(errMsg, "unknown revision") ||
			strings.Contains(errMsg, "bad revision") {
			return nil, fmt.Errorf("failed to resolve reference: %s", ref)
		}

		return nil, fmt.Errorf("git show %s: %s: %w", spec, errMsg, err)
	}

	// git show succeeds for tree objects (directories) too - check object type
	isDir, _ := NativeIsDirectory(ctx, repoPath, ref, path)
	if isDir {
		return nil, fmt.Errorf("path is a directory")
	}

	return out, nil
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
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		// ref가 브랜치 이름일 수 있으므로 refs/heads/ 접두사로 재시도
		cmd2 := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "rev-parse", "refs/heads/"+target)
		out2, err2 := gitutil.OutputWithLimit(cmd2, gitutil.DefaultMaxOutput)
		if err2 == nil {
			return strings.TrimSpace(string(out2)), nil
		}
		return "", fmt.Errorf("failed to resolve reference: %s", ref)
	}

	return strings.TrimSpace(string(out)), nil
}

// NativeIsDirectory는 native git으로 경로가 디렉토리인지 확인합니다.
func NativeIsDirectory(ctx context.Context, repoPath, ref, path string) (bool, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	target := ref
	if target == "" {
		target = "HEAD"
	}

	spec := target + ":" + path
	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "cat-file", "-t", spec)
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return false, err
	}

	objType := strings.TrimSpace(string(out))
	return objType == "tree", nil
}
