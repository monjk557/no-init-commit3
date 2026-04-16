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

		entryType := metaParts[1] // "blob", "tree", "commit"
		name := gitutil.UnquoteGitPath(tabParts[1])

		// Map commit (submodule) to its type
		displayType := entryType
		if displayType == "commit" {
			displayType = "commit" // kept as-is, caller may remap to "tree"
		}

		result = append(result, TreeEntry{
			Path: name,
			Name: name,
			Type: displayType,
		})
	}

	// Sort: trees first, then blobs, then commits; alphabetical within type
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

// sortTreeEntries sorts entries: trees first, then blobs, alphabetical within type.
func sortTreeEntries(entries []TreeEntry) {
	sort.Slice(entries, func(i, j int) bool {
		typeOrder := func(t string) int {
			switch t {
			case "tree":
				return 0
			case "blob":
				return 1
			case "commit":
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

// getTreeStructure retrieves the tree structure using the Git repository interface
func (h *TreeHandler) getTreeStructure(repoPath, ref, path string) (*TreeEntriesResponse, error) {
	treeEntries, err := GetTreeEntries(h.pathConfig, h.logger, repoPath, ref, path)
	if err != nil {
		return nil, err
	}

	basePath := strings.Trim(path, "/")

	var responseEntries []TreeEntry
	for _, entry := range treeEntries {
		fullPath := entry.Name
		if basePath != "" {
			fullPath = basePath + "/" + entry.Name
		}

		entryType := entry.Type
		if entryType == "commit" {
			entryType = "tree"
		}

		responseEntries = append(responseEntries, TreeEntry{
			Type: entryType,
			Name: entry.Name,
			Path: fullPath,
		})
	}

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

	for _, path := range pathsToExplore {
		treeEntries, err := GetTreeEntries(h.pathConfig, h.logger, repoPath, ref, path)
		if err != nil {
			if strings.Trim(path, "/") == targetPath {
				return nil, err
			}
			continue
		}

		basePath := strings.Trim(path, "/")
		var responseEntries []TreeEntry

		for _, entry := range treeEntries {
			entryType := entry.Type
			if entryType == "commit" {
				entryType = "tree"
			}

			fullPath := entry.Name
			if basePath != "" {
				fullPath = basePath + "/" + entry.Name
			}

			responseEntries = append(responseEntries, TreeEntry{
				Type: entryType,
				Name: entry.Name,
				Path: fullPath,
			})
		}

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
