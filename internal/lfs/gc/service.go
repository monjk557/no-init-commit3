package gc

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/lfs"
)

// Default scan limits. orchestrator 가 override 하고 싶으면 query param 으로
// 받도록 추후 확장 (현재는 hard-coded — 대부분의 monorepo 에 충분).
const (
	defaultScanTimeout = 10 * time.Minute
	defaultMaxOIDs     = 1_000_000 // 1M OID = ~80MB JSON, 대부분의 repo 에 충분한 상한
)

// ScanReachableOIDs 는 repoPath 의 git object DB 를 스캔해서 ref 에서 도달
// 가능한 LFS pointer 의 OID 집합을 반환한다.
//
// 알고리즘:
//  1. `git rev-list --objects --all` 로 reachable object enumerate
//     - commit / tree / blob 모두 포함, deduped
//     - --all 은 모든 ref (branches + tags) 의 union
//  2. `git cat-file --batch-check='%(objectname) %(objecttype) %(objectsize)'` 로
//     blob 인 것 + size 가 LFS pointer 크기 범위 (50~300 byte) 인 것만 candidate
//  3. candidate blob 의 내용을 `git cat-file --batch` 로 일괄 read
//  4. 각 content 가 LFS pointer 정규식 매치 → OID 추출
//  5. set 에 누적, max_oids cap 도달 시 truncated=true 마킹
//
// reachability 정의: --all 은 모든 local ref (branches + tags + special refs)
// 의 합집합. orchestrator 의 GC 는 이 결과를 보고 "MinIO 에 있지만 reachable
// 안 한" object 를 orphan 으로 판정.
func ScanReachableOIDs(ctx context.Context, repoPath string) (*ScanReachableOIDsResponse, error) {
	start := time.Now()

	ctx, cancel := context.WithTimeout(ctx, defaultScanTimeout)
	defer cancel()

	refCount, err := countRefs(ctx, repoPath)
	if err != nil {
		return nil, fmt.Errorf("count refs: %w", err)
	}

	// blob OID + size 후보 수집
	candidates, scannedBlobs, err := candidateBlobs(ctx, repoPath)
	if err != nil {
		return nil, fmt.Errorf("enumerate blobs: %w", err)
	}

	// content 일괄 read 후 LFS pointer OID 추출
	oids, lfsPointerCount, truncated, err := extractLFSOIDs(ctx, repoPath, candidates, defaultMaxOIDs)
	if err != nil {
		return nil, fmt.Errorf("extract LFS OIDs: %w", err)
	}

	return &ScanReachableOIDsResponse{
		Success:         true,
		ReachableOIDs:   oids,
		ScannedRefs:     refCount,
		ScannedBlobs:    scannedBlobs,
		LFSPointerBlobs: lfsPointerCount,
		Truncated:       truncated,
		DurationMs:      time.Since(start).Milliseconds(),
	}, nil
}

// countRefs 는 repo 의 ref 수 (branches + tags) 를 반환.
// `git for-each-ref --format=%(refname)` 의 line count.
func countRefs(ctx context.Context, repoPath string) (int, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "for-each-ref", "--format=%(refname)")
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, line := range bytes.Split(out, []byte("\n")) {
		if len(line) > 0 {
			count++
		}
	}
	return count, nil
}

// blobCandidate 는 LFS pointer 후보 blob — size 가 pointer 크기 범위 내인 것.
type blobCandidate struct {
	OID  string
	Size int64
}

// candidateBlobs 는 reachable blob 중 LFS pointer 가 될 수 있는 크기 범위
// (50~300 byte) 의 것만 추린다. LFS pointer 표준은 ~130 byte 라 200 byte 도
// 안전한 상한이지만 future-proof 위해 300 까지 잡는다.
//
// 알고리즘 detail:
//  1. `git rev-list --objects --all` 는 라인마다 "<sha> [path]" 형태 — blob 의
//     path 가 trailing 으로 붙는다. `git cat-file --batch-check` 는 라인을
//     단일 rev 로 파싱하므로 path 가 붙으면 "missing" 으로 잘못 응답.
//  2. → rev-list 출력에서 첫 column (SHA) 만 추출해서 cat-file 로 pipe.
//
// 반환값: (candidates, total_scanned_blobs)
func candidateBlobs(ctx context.Context, repoPath string) ([]blobCandidate, int, error) {
	revList := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-list", "--objects", "--all")
	revListOut, err := revList.StdoutPipe()
	if err != nil {
		return nil, 0, err
	}
	if err := revList.Start(); err != nil {
		return nil, 0, err
	}
	defer func() { _ = revList.Wait() }()

	catFile := exec.CommandContext(ctx, "git", "-C", repoPath,
		"cat-file", "--batch-check=%(objectname) %(objecttype) %(objectsize)")
	catFileStdin, err := catFile.StdinPipe()
	if err != nil {
		return nil, 0, err
	}
	catFileStdout, err := catFile.StdoutPipe()
	if err != nil {
		_ = catFileStdin.Close()
		return nil, 0, err
	}
	if err := catFile.Start(); err != nil {
		_ = catFileStdin.Close()
		return nil, 0, err
	}

	// rev-list stdout → SHA 첫 column 만 추출 → cat-file stdin
	go func() {
		defer func() { _ = catFileStdin.Close() }()
		scanner := bufio.NewScanner(revListOut)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			// "<sha>" 또는 "<sha> <path>" — 공백으로 분리해서 첫 token 만
			sha := line
			if idx := strings.IndexByte(line, ' '); idx >= 0 {
				sha = line[:idx]
			}
			if sha == "" {
				continue
			}
			_, _ = fmt.Fprintln(catFileStdin, sha)
		}
	}()

	out, err := io.ReadAll(catFileStdout)
	if err != nil {
		return nil, 0, err
	}
	if err := catFile.Wait(); err != nil {
		return nil, 0, err
	}

	const (
		minPointerSize = 50
		maxPointerSize = 300
	)

	candidates := make([]blobCandidate, 0, 256)
	scannedBlobs := 0
	scanner := bufio.NewScanner(bytes.NewReader(out))
	// 큰 repo 의 cat-file 출력 라인은 길어질 수 있다 (objectname=64hex + type + size 정도 라 충분하지만 안전마진).
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		oid, objType, sizeStr := fields[0], fields[1], fields[2]
		if objType != "blob" {
			continue
		}
		scannedBlobs++
		size, err := strconv.ParseInt(sizeStr, 10, 64)
		if err != nil {
			continue
		}
		if size < minPointerSize || size > maxPointerSize {
			continue
		}
		candidates = append(candidates, blobCandidate{OID: oid, Size: size})
	}
	if err := scanner.Err(); err != nil {
		return nil, scannedBlobs, err
	}
	return candidates, scannedBlobs, nil
}

// extractLFSOIDs 는 candidate blob 각각의 content 를 read 해서 LFS pointer
// 인지 검사한다. 매치되면 pointer 안의 sha256 OID 를 추출한다.
//
// 반환값: (deduped oids, total LFS pointer blob count, truncated flag, error)
//
// 같은 OID 가 여러 path / commit 에 등장할 수 있으므로 set 으로 dedup. cap 도달
// 시 truncated=true 로 반환 — orchestrator 가 partial 로 안전 처리.
func extractLFSOIDs(
	ctx context.Context,
	repoPath string,
	candidates []blobCandidate,
	maxOIDs int,
) ([]string, int, bool, error) {
	if len(candidates) == 0 {
		return []string{}, 0, false, nil
	}

	catFile := exec.CommandContext(ctx, "git", "-C", repoPath, "cat-file", "--batch")
	stdin, err := catFile.StdinPipe()
	if err != nil {
		return nil, 0, false, err
	}
	stdout, err := catFile.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, 0, false, err
	}
	if err := catFile.Start(); err != nil {
		_ = stdin.Close()
		return nil, 0, false, err
	}

	// goroutine 으로 stdin write — stdout read 와 데드락 회피
	go func() {
		defer func() { _ = stdin.Close() }()
		for _, c := range candidates {
			_, _ = fmt.Fprintln(stdin, c.OID)
		}
	}()

	seen := make(map[string]struct{}, len(candidates))
	oids := make([]string, 0, 256)
	pointerCount := 0
	truncated := false

	reader := bufio.NewReader(stdout)
	for {
		header, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, pointerCount, truncated, err
		}
		header = strings.TrimRight(header, "\n")
		fields := strings.Fields(header)
		if len(fields) < 3 {
			continue
		}
		objType, sizeStr := fields[1], fields[2]
		size, err := strconv.Atoi(sizeStr)
		if err != nil {
			continue
		}

		// 정확히 size byte + trailing newline 1 byte 읽기
		content := make([]byte, size)
		if _, err := io.ReadFull(reader, content); err != nil {
			return nil, pointerCount, truncated, err
		}
		_, _ = reader.ReadByte() // trailing \n

		if objType != "blob" {
			continue
		}

		if !lfs.IsLFSPointer(content) {
			continue
		}
		oid, _, ok := lfs.ParseLFSPointer(content)
		if !ok {
			continue
		}
		pointerCount++
		if _, dup := seen[oid]; dup {
			continue
		}
		if len(oids) >= maxOIDs {
			truncated = true
			continue
		}
		seen[oid] = struct{}{}
		oids = append(oids, oid)
	}

	if err := catFile.Wait(); err != nil {
		return oids, pointerCount, truncated, err
	}

	return oids, pointerCount, truncated, nil
}
