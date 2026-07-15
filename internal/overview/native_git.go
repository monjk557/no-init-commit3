package overview

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/gitutil"
	"github.com/collabops/git-executor/internal/repositories"
)

// ---------------------------------------------------------------------------
// Native git: latest commit per path
// ---------------------------------------------------------------------------
//
// go-git의 object.DiffTree는 매 커밋마다 전체 트리를 Go로 재귀 비교하므로
// 파일이 많은 레포에서 극도로 느립니다 (973파일×340커밋 → 9초).
//
// native git은 C 최적화 + bloom filter + commit-graph + path-limited traversal로
// 동일 작업을 6ms 이내에 완료합니다 (1,500배 차이).
//
// 이 파일은 `git log` CLI를 사용하여 경로별 최신 커밋을 조회합니다.
// ---------------------------------------------------------------------------

const (
	// nativeGitConcurrency는 동시에 실행할 git log 프로세스 수.
	nativeGitConcurrency = 8
	// nativeGitTimeout는 단일 git log 호출의 최대 실행 시간.
	nativeGitTimeout = 5 * time.Second
)

// gitLogFormat은 git log --format에 사용하는 포맷 문자열.
// 필드 구분자로 %x00 (null byte)을 사용.
// 순서: SHA, TreeHash, ParentHashes, AuthorName, AuthorEmail, AuthorDate,
//
//	CommitterName, CommitterEmail, CommitterDate, Subject
const gitLogFormat = "%H%x00%T%x00%P%x00%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI%x00%s"

// GetLatestCommitsNative는 native git CLI를 사용하여 각 경로의 최신 커밋을 조회합니다.
// go-git 대비 ~1,500배 빠릅니다.
func GetLatestCommitsNative(
	ctx context.Context,
	repoPath string,
	ref string,
	paths []string,
) (map[string]*GitCommit, LatestCommitsMeta) {
	result := make(map[string]*GitCommit, len(paths))
	meta := LatestCommitsMeta{}

	if len(paths) == 0 {
		meta.CutReason = "complete"
		return result, meta
	}

	// Resolve ref hash first.
	refHash, err := nativeResolveRef(ctx, repoPath, ref)
	if err != nil {
		meta.Incomplete = true
		meta.CutReason = "error"
		return result, meta
	}
	meta.ResolvedRefHash = refHash

	start := time.Now()

	// Concurrent git log per path.
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, nativeGitConcurrency)

	for _, p := range paths {
		wg.Add(1)
		sem <- struct{}{} // acquire
		go func(path string) {
			defer wg.Done()
			defer func() { <-sem }() // release

			commit, err := nativeGitLogForPath(ctx, repoPath, ref, path)
			if err != nil {
				return
			}
			mu.Lock()
			result[path] = commit
			mu.Unlock()
		}(p)
	}
	wg.Wait()

	meta.ScannedCommits = len(result)
	meta.TimeBudgetMs = int(time.Since(start).Milliseconds())
	if len(result) < len(paths) {
		meta.Incomplete = true
		meta.CutReason = "partial"
	} else {
		meta.CutReason = "complete"
	}

	return result, meta
}

// nativeCheckRepoExists는 repoPath가 유효한 git 저장소인지 확인합니다.
// git rev-parse --git-dir를 사용하여 커밋이 없는 빈 저장소도 올바르게 검증합니다.
func nativeCheckRepoExists(ctx context.Context, repoPath string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "rev-parse", "--git-dir")
	_, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return fmt.Errorf("git rev-parse --git-dir failed: %w", err)
	}
	return nil
}

// nativeResolveRef는 ref를 커밋 해시로 resolve합니다.
func nativeResolveRef(ctx context.Context, repoPath, ref string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	target := ref
	if target == "" {
		target = "HEAD"
	}

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "rev-parse", target)
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("git rev-parse failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// NativeGetLatestCommitForPath는 특정 경로의 최신 커밋을 native git으로 조회합니다.
// tree/blob overview 핸들러에서 go-git GetLatestCommitForPath를 대체합니다.
func NativeGetLatestCommitForPath(ctx context.Context, repoPath, ref, path string) (*GitCommit, error) {
	return nativeGitLogForPath(ctx, repoPath, ref, path)
}

// nativeGitLogForPath는 단일 경로의 최신 커밋을 git log로 조회합니다.
func nativeGitLogForPath(ctx context.Context, repoPath, ref, path string) (*GitCommit, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	target := ref
	if target == "" {
		target = "HEAD"
	}

	args := []string{
		"--git-dir", repoPath,
		"log", "-1",
		"--format=" + gitLogFormat,
		target,
		"--",
		path,
	}

	cmd := exec.CommandContext(cmdCtx, "git", args...)
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return nil, fmt.Errorf("git log for %s: %w", path, err)
	}

	line := strings.TrimSpace(string(out))
	if line == "" {
		// Path never appeared in history (e.g., submodule edge case).
		return nil, nil
	}

	return parseGitLogLine(line)
}

// parseGitLogLine은 null-separated git log 출력을 GitCommit으로 파싱합니다.
func parseGitLogLine(line string) (*GitCommit, error) {
	parts := bytes.SplitN([]byte(line), []byte{0x00}, 10)
	if len(parts) < 10 {
		return nil, fmt.Errorf("unexpected git log format: got %d fields", len(parts))
	}

	sha := string(parts[0])
	treeHash := string(parts[1])
	parentHashesRaw := string(parts[2])
	authorName := string(parts[3])
	authorEmail := string(parts[4])
	authorDateStr := string(parts[5])
	committerName := string(parts[6])
	committerEmail := string(parts[7])
	committerDateStr := string(parts[8])
	subject := string(parts[9])

	authorDate, _ := time.Parse(time.RFC3339, authorDateStr)
	committerDate, _ := time.Parse(time.RFC3339, committerDateStr)

	var parentHashes []string
	if parentHashesRaw != "" {
		parentHashes = strings.Split(parentHashesRaw, " ")
	}

	return &GitCommit{
		SHA:     sha,
		Message: subject,
		Author: GitAuthor{
			Name:  authorName,
			Email: authorEmail,
			Date:  authorDate,
		},
		Committer: GitAuthor{
			Name:  committerName,
			Email: committerEmail,
			Date:  committerDate,
		},
		ParentHashes: parentHashes,
		TreeHash:     treeHash,
	}, nil
}

// NativeGetCommits는 native git으로 커밋 목록을 조회합니다.
// tree/blob overview의 include_all_commits 옵션에서 go-git commits.GetCommits를 대체합니다.
func NativeGetCommits(ctx context.Context, repoPath, ref string, limit int) ([]GitCommit, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	target := ref
	if target == "" {
		target = "HEAD"
	}

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath,
		"log", fmt.Sprintf("-%d", limit), "--format="+gitLogFormat, target)
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return nil, fmt.Errorf("git log commits: %w", err)
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return []GitCommit{}, nil
	}

	lines := strings.Split(raw, "\n")
	result := make([]GitCommit, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		commit, err := parseGitLogLine(line)
		if err != nil {
			continue
		}
		result = append(result, *commit)
	}
	return result, nil
}

// NativeGetTreeEntries는 native git으로 트리 엔트리를 조회합니다.
// go-git의 GetTreeOnly를 대체할 수 있으나, GetTreeOnly는 충분히 빠르므로
// 필요시에만 사용합니다.
func NativeGetTreeEntries(ctx context.Context, repoPath, ref, path string) ([]GitTreeEntryWithCommit, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	target := ref
	if target == "" {
		target = "HEAD"
	}

	treeSpec := target
	if path != "" {
		treeSpec = target + ":" + path
	}

	cmd := exec.CommandContext(cmdCtx, "git", "-c", "core.quotePath=false", "--git-dir", repoPath, "ls-tree", "-l", treeSpec)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		// Include stderr for proper error classification (e.g., "not a tree object", "does not exist").
		return nil, fmt.Errorf("git ls-tree failed: %s: %w", strings.TrimSpace(stderr.String()), err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	entries := make([]GitTreeEntryWithCommit, 0, len(lines))

	basePath := strings.Trim(path, "/")

	for _, line := range lines {
		if line == "" {
			continue
		}
		entry, err := parseLsTreeLine(line, basePath)
		if err != nil {
			continue
		}
		entries = append(entries, entry)
	}

	// submodule 엔트리가 하나라도 있으면 ref 의 .gitmodules 를 한 번만 읽어
	// URL/branch/parsed 메타를 채운다. .gitmodules 가 없거나 path 가 누락된
	// 경우 commit 만 채워진 SubmoduleInfo 가 결과로 나간다 — service.go
	// (go-git 경로) 의 동작과 동등.
	hasSubmodule := false
	for _, e := range entries {
		if e.Type == "submodule" {
			hasSubmodule = true
			break
		}
	}
	if hasSubmodule {
		gitmods := nativeLoadGitmodules(ctx, repoPath, ref)
		for i := range entries {
			if entries[i].Type != "submodule" {
				continue
			}
			si := &SubmoduleInfo{}
			if entries[i].Hash != nil {
				si.Commit = *entries[i].Hash
			}
			if cfg := gitmods.LookupByPath(entries[i].Path); cfg != nil {
				si.URL = cfg.URL
				si.Branch = cfg.Branch
			}
			if si.URL != "" {
				si.Parsed = repositories.ParseRepoURL(si.URL)
			}
			entries[i].Submodule = si
		}
	}

	// Sort: trees (directories) first, then submodules, then blobs (files).
	// service.go 의 entryTypeSortKey 와 동일 우선순위 (0=tree, 1=blob, 2=submodule)
	// 를 유지하기 위해 정렬 key 를 일관되게 매핑.
	sortKey := func(t string) int {
		switch t {
		case "tree":
			return 0
		case "blob":
			return 1
		case "submodule", "commit":
			return 2
		default:
			return 3
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		ki, kj := sortKey(entries[i].Type), sortKey(entries[j].Type)
		if ki != kj {
			return ki < kj
		}
		return entries[i].Name < entries[j].Name
	})

	return entries, nil
}

// ---------------------------------------------------------------------------
// Native git: overview helper functions
// ---------------------------------------------------------------------------
//
// /overview 핸들러의 6개 goroutine을 native git exec으로 대체하기 위한 함수들.
// go-git repo를 열 필요 없이 bare repo 경로만으로 동작합니다.
// ---------------------------------------------------------------------------

// NativeGetLatestCommit은 native git으로 ref의 최신 커밋 1개를 조회합니다.
// nativeGitLogForPath와 동일하지만 경로 인자 없이 전체 ref의 HEAD 커밋을 반환합니다.
func NativeGetLatestCommit(ctx context.Context, repoPath, ref string) (*GitCommit, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	target := ref
	if target == "" {
		target = "HEAD"
	}

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath,
		"log", "-1", "--format="+gitLogFormat, target)
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return nil, fmt.Errorf("git log latest commit: %w", err)
	}

	line := strings.TrimSpace(string(out))
	if line == "" {
		return nil, nil
	}
	return parseGitLogLine(line)
}

// NativeCountCommits는 native git으로 ref의 커밋 수를 카운트합니다.
// overviewCommitCountCap 초과 시 approximate=true를 반환합니다.
func NativeCountCommits(ctx context.Context, repoPath, ref string) (count int, approximate bool, err error) {
	return nativeCountCommitsPathspec(ctx, repoPath, ref, "")
}

// NativeCountCommitsForPath는 native git으로 ref에서 특정 path(pathspec)를 건드린
// 커밋 수를 카운트합니다: `git rev-list --count <ref> -- <path>`. path가 빈 문자열이면
// NativeCountCommits와 동일하게 ref 전체를 셉니다. overviewCommitCountCap 초과 시
// approximate=true.
//
// 주의: pathspec 카운트는 history simplification 때문에 ref 전체 카운트보다 무거울 수
// 있습니다(경로를 건드린 커밋을 찾기 위해 그래프를 walk). 호출부는 3초 타임아웃에 의존해
// best-effort로 다뤄야 하며, 실패해도 latest-commit 응답 본체(커밋)는 유지해야 합니다.
func NativeCountCommitsForPath(ctx context.Context, repoPath, ref, path string) (count int, approximate bool, err error) {
	return nativeCountCommitsPathspec(ctx, repoPath, ref, path)
}

// nativeCountCommitsPathspec는 NativeCountCommits / NativeCountCommitsForPath의 공통 구현.
// path != "" 이면 `-- <path>` pathspec을 붙여 경로별로 카운트합니다.
func nativeCountCommitsPathspec(ctx context.Context, repoPath, ref, path string) (count int, approximate bool, err error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	target := ref
	if target == "" {
		target = "HEAD"
	}

	args := []string{"--git-dir", repoPath, "rev-list", "--count", target}
	if path != "" {
		args = append(args, "--", path)
	}

	cmd := exec.CommandContext(cmdCtx, "git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		// Include stderr so genuine failures (bad revision, corrupt object,
		// maintenance dead-zone) are distinguishable from a bare exit code.
		return 0, false, fmt.Errorf("git rev-list --count: %s: %w",
			strings.TrimSpace(stderr.String()), err)
	}

	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, false, fmt.Errorf("parse commit count: %w", err)
	}

	if n > overviewCommitCountCap {
		return n, true, nil
	}
	return n, false, nil
}

// NativeGetTags는 native git으로 태그 목록을 조회합니다.
// creatordate 내림차순으로 정렬하여 최신 태그부터 반환합니다.
func NativeGetTags(ctx context.Context, repoPath string, limit int) ([]GitTag, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// %(refname:short) = tag name, %(objectname:short) = short hash, %(objecttype) = "commit" or "tag"
	// NOTE: Using tab separator instead of %x00 because Apple Git's for-each-ref/tag -l
	// does not interpret %x00 as null byte (unlike git log --format).
	format := "%(refname:short)\t%(objectname:short)\t%(objecttype)"
	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath,
		"tag", "-l", "--sort=-creatordate", "--format="+format)
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return nil, fmt.Errorf("git tag -l: %w", err)
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return []GitTag{}, nil
	}

	lines := strings.Split(raw, "\n")
	if limit > 0 && len(lines) > limit {
		lines = lines[:limit]
	}

	result := make([]GitTag, 0, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		result = append(result, GitTag{
			Name:        parts[0],
			Hash:        parts[1],
			IsAnnotated: parts[2] == "tag",
		})
	}
	return result, nil
}

// NativeGetRemotes는 native git으로 리모트 목록을 조회합니다.
// fetch URL만 수집하며, 첫 번째 리모트를 IsDefault=true로 표시합니다.
func NativeGetRemotes(ctx context.Context, repoPath string) ([]RemoteInfo, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "remote", "-v")
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return nil, fmt.Errorf("git remote -v: %w", err)
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return []RemoteInfo{}, nil
	}

	// git remote -v 출력: "origin\thttps://... (fetch)\norigin\thttps://... (push)\n..."
	// fetch URL만 수집하고 중복 제거
	seen := make(map[string]struct{})
	var result []RemoteInfo
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasSuffix(line, "(fetch)") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		url := fields[1]
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, RemoteInfo{
			Name:      name,
			URL:       url,
			IsDefault: len(result) == 0, // 첫 번째 리모트가 default
		})
	}

	if result == nil {
		return []RemoteInfo{}, nil
	}
	return result, nil
}

// NativeCountBranches returns the number of local branches (refs/heads/*).
// Uses `git for-each-ref --format=1` so output is one byte per ref and the
// count is a byte scan with no ref-name parsing.
func NativeCountBranches(ctx context.Context, repoPath string) (int, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath,
		"for-each-ref", "--format=1", "refs/heads/")
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return 0, fmt.Errorf("git for-each-ref refs/heads/: %w", err)
	}
	return bytes.Count(out, []byte{'\n'}), nil
}

// NativeGetBlobContent는 native git으로 특정 ref:path의 blob 내용을 조회합니다.
// README 파일 등 단일 blob을 빠르게 읽을 때 사용합니다.
func NativeGetBlobContent(ctx context.Context, repoPath, ref, path string) ([]byte, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
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
		return nil, fmt.Errorf("git show %s: %s: %w", spec, strings.TrimSpace(stderr.String()), err)
	}
	return out, nil
}

// parseLsTreeLine은 `git ls-tree -l` 출력 한 줄을 파싱합니다.
// 형식: <mode> <type> <hash> <size>\t<name>
func parseLsTreeLine(line, basePath string) (GitTreeEntryWithCommit, error) {
	// Split on tab first to get name
	tabParts := strings.SplitN(line, "\t", 2)
	if len(tabParts) != 2 {
		return GitTreeEntryWithCommit{}, fmt.Errorf("invalid ls-tree line")
	}
	name := gitutil.UnquoteGitPath(tabParts[1])

	// Parse metadata: "<mode> <type> <hash> <size>"
	// size can be "-" for trees
	metaParts := strings.Fields(tabParts[0])
	if len(metaParts) < 4 {
		return GitTreeEntryWithCommit{}, fmt.Errorf("invalid ls-tree metadata")
	}

	mode := metaParts[0]
	entryType := metaParts[1]
	hash := metaParts[2]
	sizeStr := metaParts[3]

	// gitlink (mode 160000) 은 git ls-tree 가 raw type 으로 "commit" 을 노출.
	// service.go 의 go-git 경로 (filemode.Submodule → "submodule") 와 일관되게
	// "submodule" 로 정규화 — 두 코드 경로가 같은 wire response 를 내도록.
	if entryType == "commit" {
		entryType = "submodule"
	}

	fullPath := name
	if basePath != "" {
		fullPath = basePath + "/" + name
	}

	entry := GitTreeEntryWithCommit{
		Type: entryType,
		Name: name,
		Path: fullPath,
		Mode: &mode,
		Hash: &hash,
	}

	if entryType == "blob" && sizeStr != "-" {
		var size int64
		fmt.Sscanf(sizeStr, "%d", &size)
		entry.Size = &size
	}

	return entry, nil
}

// nativeLoadGitmodules 는 ref 의 .gitmodules blob 을 native git 으로 읽어
// 파싱한다. 파일이 없거나 파싱 실패 시 nil 반환 — 호출자는 "submodule 부가
// 정보가 비어 있는 정상 케이스" 로 처리한다 (service.go 의
// loadGitmodulesFromTree 와 동일 contract).
func nativeLoadGitmodules(ctx context.Context, repoPath, ref string) *repositories.GitmodulesIndex {
	cmdCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	target := ref
	if target == "" {
		target = "HEAD"
	}

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "show", target+":.gitmodules")
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		// .gitmodules 부재 (대부분의 repo) — 정상.
		return nil
	}
	return repositories.ParseGitmodules(out)
}
