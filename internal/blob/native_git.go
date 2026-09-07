package blob

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/gitutil"
)

const nativeGitTimeout = 5 * time.Second

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

// BlobStat 은 blob 을 읽지 않고 얻는 메타데이터다 (object id / type / size).
type BlobStat struct {
	OID  string
	Type string
	Size int64
}

// NativeStatBlob 은 <ref>:<path> 의 object type/size 를 내용을 읽지 않고
// 조회한다.
//
// `git cat-file --batch-check` 는 object 헤더만 읽으므로 blob 크기와 무관하게
// 상수 시간/메모리다. "이 파일을 미리보기할 것인가" 를 내용을 메모리에 올리기
// **전에** 결정할 수 있게 하는 진입점 — 60MB 바이너리를 판정하려고 60MB 를
// 읽다가 출력 한도에 걸려 "파일 없음" 으로 오분류되던 문제의 근본 해소.
func NativeStatBlob(ctx context.Context, repoPath, ref, path string) (BlobStat, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	target := ref
	if target == "" {
		target = "HEAD"
	}
	spec := target + ":" + path

	cmd := exec.CommandContext(cmdCtx, "git", "-c", "core.quotePath=false", "--git-dir", repoPath, "cat-file", "--batch-check")
	cmd.Stdin = strings.NewReader(spec + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		if cmdCtx.Err() != nil {
			return BlobStat{}, fmt.Errorf("stat blob %s: %w", spec, cmdCtx.Err())
		}
		errMsg := strings.TrimSpace(stderr.String())
		return BlobStat{}, fmt.Errorf("%w: git cat-file %s: %s", ErrBlobNotFound, spec, errMsg)
	}

	line := strings.TrimSpace(string(out))
	// 존재하지 않는 ref/path 는 "<spec> missing" (또는 ambiguous/dangling).
	// spec 에 공백이 있을 수 있으므로 필드 개수가 아니라 접미사로 판정한다.
	if line == "" || strings.HasSuffix(line, " missing") || strings.HasSuffix(line, " ambiguous") {
		return BlobStat{}, fmt.Errorf("%w: file not found: %s", ErrBlobNotFound, path)
	}

	fields := strings.Fields(line)
	if len(fields) != 3 {
		return BlobStat{}, fmt.Errorf("%w: unexpected cat-file output: %q", ErrBlobNotFound, line)
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return BlobStat{}, fmt.Errorf("%w: unexpected cat-file size: %q", ErrBlobNotFound, fields[2])
	}

	return BlobStat{OID: fields[0], Type: fields[1], Size: size}, nil
}

// NativeOpenBlob 은 blob 내용을 스트림으로 연다 — 전체를 메모리에 올리지
// 않으므로 blob 크기와 무관하게 상수 메모리다. 호출자는 반드시 Close 해야
// git 프로세스가 회수된다.
//
// ctx 는 요청 컨텍스트를 그대로 넘긴다 (고정 timeout 금지) — 200MB 다운로드가
// 느린 회선에서 중간에 잘리면 안 되고, 클라이언트가 끊으면 ctx 취소로 git 도
// 함께 종료된다.
func NativeOpenBlob(ctx context.Context, repoPath, oid string) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "cat-file", "blob", oid)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open blob %s: %w", oid, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("open blob %s: %w", oid, err)
	}
	return &blobStream{rc: stdout, cmd: cmd}, nil
}

// blobStream 은 git cat-file 프로세스의 stdout 을 감싸 Close 시 프로세스를
// 회수한다.
type blobStream struct {
	rc  io.ReadCloser
	cmd *exec.Cmd
}

func (s *blobStream) Read(p []byte) (int, error) { return s.rc.Read(p) }

func (s *blobStream) Close() error {
	// 파이프를 먼저 닫아 git 이 SIGPIPE 로 끝나게 한다 — 클라이언트가 중간에
	// 끊었을 때 git 이 남아 파이프를 채우려 블록하는 것을 막는다.
	_ = s.rc.Close()
	return s.cmd.Wait()
}
