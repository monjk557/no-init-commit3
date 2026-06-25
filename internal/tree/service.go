package tree

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/gitutil"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
)

const nativeGitTimeout = 5 * time.Second

// refResolves reports whether ref resolves to a valid object in repoPath.
// Used to distinguish "ref not found" from "path not found" when the combined
// "<ref>:<path>" spec fails (git returns an identical error for both cases).
func refResolves(ctx context.Context, repoPath, ref string) bool {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	_, err := gitutil.OutputWithLimit(cmd, gitutil.MaxOutputSmall)
	return err == nil
}

// GetTreeEntries는 저장소의 트리 항목을 가져옵니다.
// native git ls-tree를 사용합니다.
func GetTreeEntries(pathConfig *repositories.PathConfig, logger *logging.Logger, repoPath, ref, path string) ([]TreeEntry, error) {
	ctx := context.Background()
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	// Build the ls-tree target: ref:path or ref
	target := ref
	if path != "" {
		target = ref + ":" + path
	}

	cmd := exec.CommandContext(cmdCtx, "git", "-c", "core.quotePath=false", "--git-dir", repoPath, "ls-tree", target)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if strings.Contains(errMsg, "not a tree") {
			return nil, fmt.Errorf("path is not a tree: %s", path)
		}
		if strings.Contains(errMsg, "Not a valid object name") || strings.Contains(errMsg, "fatal") {
			// git uses the same "Not a valid object name <ref>:<path>" message for
			// both an unresolvable ref AND a path that doesn't exist under a valid
			// ref. When a path was supplied, verify the ref independently so a
			// missing path yields "path not found" (404) instead of being
			// misclassified as a ref resolution failure.
			if path != "" && refResolves(cmdCtx, repoPath, ref) {
				return nil, fmt.Errorf("path not found: %s", path)
			}
			return nil, fmt.Errorf("failed to resolve reference: %s", ref)
		}
		return nil, fmt.Errorf("git ls-tree failed: %s: %w", errMsg, err)
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return []TreeEntry{}, nil
	}

	var result []TreeEntry
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		// format: <mode> <type> <hash>\t<name>
		tabParts := strings.SplitN(line, "\t", 2)
		if len(tabParts) != 2 {
			continue
		}
		metaParts := strings.Fields(tabParts[0])
		if len(metaParts) != 3 {
			continue
		}

		mode := metaParts[0]      // e.g. "100644", "040000", "120000", "160000"
		entryType := metaParts[1] // "blob", "tree", "commit"
		hash := metaParts[2]
		name := gitutil.UnquoteGitPath(tabParts[1])

		entry := TreeEntry{
			Path: name,
			Name: name,
			Type: entryType,
		}
		switch {
		// gitlink (mode 160000): expose pinned commit immediately so the
		// handler can decorate with .gitmodules-derived URL/branch later.
		case entryType == string(EntryTypeCommit):
			entry.Type = string(EntryTypeSubmodule)
			entry.Submodule = &SubmoduleInfo{Commit: hash}
		// symlink (mode 120000): git stores it as a blob, but we surface a
		// distinct "symlink" type so callers don't treat the link target path
		// (the blob content) as regular file content.
		case mode == symlinkMode:
			entry.Type = string(EntryTypeSymlink)
		}
		result = append(result, entry)
	}

	// Sort: trees first, then blobs, then submodules; alphabetical within type
	sortTreeEntries(result)

	return result, nil
}

// SearchFiles는 저장소의 트리에서 파일명을 검색합니다.
// git ls-tree -r --name-only로 전체 파일 목록을 가져온 뒤 대소문자 무관 부분 일치로 필터링합니다.
func SearchFiles(repoPath, ref, query string, limit int) ([]TreeEntry, int, error) {
	ctx := context.Background()
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "-c", "core.quotePath=false", "--git-dir", repoPath, "ls-tree", "-r", "--name-only", ref)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if strings.Contains(errMsg, "Not a valid object name") || strings.Contains(errMsg, "fatal") {
			return nil, 0, fmt.Errorf("failed to resolve reference: %s", ref)
		}
		return nil, 0, fmt.Errorf("git ls-tree failed: %s: %w", errMsg, err)
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return []TreeEntry{}, 0, nil
	}

	queryLower := strings.ToLower(query)
	lines := strings.Split(raw, "\n")
	var results []TreeEntry
	totalMatches := 0

	for _, line := range lines {
		if line == "" {
			continue
		}
		path := gitutil.UnquoteGitPath(line)
		if !strings.Contains(strings.ToLower(path), queryLower) {
			continue
		}
		totalMatches++
		if len(results) < limit {
			// 파일명만 추출
			name := path
			if idx := strings.LastIndex(path, "/"); idx >= 0 {
				name = path[idx+1:]
			}
			results = append(results, TreeEntry{
				Type: "blob",
				Name: name,
				Path: path,
			})
		}
	}

	return results, totalMatches, nil
}

// sortTreeEntries sorts entries: trees first, then blobs, then submodules; alphabetical within type.
func sortTreeEntries(entries []TreeEntry) {
	sort.Slice(entries, func(i, j int) bool {
		typeOrder := func(t string) int {
			switch t {
			case string(EntryTypeTree):
				return 0
			case string(EntryTypeBlob):
				return 1
			case string(EntryTypeSubmodule), string(EntryTypeCommit):
				return 2
			default:
				return 3
			}
		}
		ti, tj := typeOrder(entries[i].Type), typeOrder(entries[j].Type)
		if ti != tj {
			return ti < tj
		}
		return entries[i].Name < entries[j].Name
	})
}

// decorateEntries 는 GetTreeEntries 가 반환한 엔트리를 응답 형태로 변환합니다.
// 절대 경로를 채우고, gitlink 엔트리에는 .gitmodules 정보를 덧붙입니다.
// gitmods 가 nil 이거나 매칭되는 항목이 없으면 commit hash 만 노출됩니다.
func decorateEntries(treeEntries []TreeEntry, basePath string, gitmods *repositories.GitmodulesIndex) []TreeEntry {
	out := make([]TreeEntry, 0, len(treeEntries))
	for _, entry := range treeEntries {
		fullPath := entry.Name
		if basePath != "" {
			fullPath = basePath + "/" + entry.Name
		}

		decorated := TreeEntry{
			Type: entry.Type,
			Name: entry.Name,
			Path: fullPath,
		}
		if entry.Submodule != nil {
			info := *entry.Submodule
			if cfg := gitmods.LookupByPath(fullPath); cfg != nil {
				info.URL = cfg.URL
				info.Branch = cfg.Branch
			}
			if info.URL != "" {
				info.Parsed = repositories.ParseRepoURL(info.URL)
			}
			decorated.Submodule = &info
		}
		out = append(out, decorated)
	}
	return out
}

// loadGitmodules 는 ref 의 .gitmodules 를 읽어 인덱스를 반환합니다.
// 파일이 없거나 읽기 실패 시 nil 을 반환하며, 호출자는 이를 "submodule 부가
// 정보가 비어 있는" 정상 케이스로 처리합니다.
func (h *TreeHandler) loadGitmodules(repoPath, ref string) *repositories.GitmodulesIndex {
	idx, err := repositories.LoadGitmodulesAtRef(context.Background(), repoPath, ref)
	if err != nil {
		return nil
	}
	return idx
}

// getTreeStructure retrieves the tree structure using the Git repository interface
func (h *TreeHandler) getTreeStructure(repoPath, ref, path string) (*TreeEntriesResponse, error) {
	treeEntries, err := GetTreeEntries(h.pathConfig, h.logger, repoPath, ref, path)
	if err != nil {
		return nil, err
	}

	basePath := strings.Trim(path, "/")
	gitmods := h.loadGitmodules(repoPath, ref)
	responseEntries := decorateEntries(treeEntries, basePath, gitmods)
	sortTreeEntries(responseEntries)

	return &TreeEntriesResponse{
		Success: true,
		Entries: responseEntries,
	}, nil
}

// getTreeStructureRecursive retrieves tree structure recursively from root to the specified path
func (h *TreeHandler) getTreeStructureRecursive(repoPath, ref, targetPath string) (map[string][]TreeEntry, error) {
	targetPath = strings.Trim(targetPath, "/")

	var pathsToExplore []string
	if targetPath == "" {
		pathsToExplore = []string{""}
	} else {
		pathsToExplore = []string{""}
		parts := strings.Split(targetPath, "/")
		currentPath := ""
		for _, part := range parts {
			if currentPath == "" {
				currentPath = part
			} else {
				currentPath = currentPath + "/" + part
			}
			pathsToExplore = append(pathsToExplore, currentPath)
		}
	}

	result := make(map[string][]TreeEntry)
	gitmods := h.loadGitmodules(repoPath, ref)

	for _, path := range pathsToExplore {
		treeEntries, err := GetTreeEntries(h.pathConfig, h.logger, repoPath, ref, path)
		if err != nil {
			if strings.Trim(path, "/") == targetPath {
				return nil, err
			}
			continue
		}

		basePath := strings.Trim(path, "/")
		responseEntries := decorateEntries(treeEntries, basePath, gitmods)
		sortTreeEntries(responseEntries)

		mapKey := path
		if mapKey == "" {
			mapKey = "/"
		}
		result[mapKey] = responseEntries
	}

	expectedKey := targetPath
	if expectedKey == "" {
		expectedKey = "/"
	}
	if _, ok := result[expectedKey]; !ok {
		return nil, fmt.Errorf("path not found: %s", targetPath)
	}

	return result, nil
}
