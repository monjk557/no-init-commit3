package tree

import (
	"fmt"
	"sort"
	"strings"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
)

// GetTreeEntries는 저장소의 트리 항목을 가져옵니다.
// 최적화: 캐시된 git.OpenRepositorySimple 사용하여 직접 GetTree 호출
func GetTreeEntries(pathConfig *repositories.PathConfig, logger *logging.Logger, repoPath, ref, path string) ([]TreeEntry, error) {
	// Open repository using the simple cache (same as v1)
	gitRepoWrapper, err := repositories.OpenRepositorySimple(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}
	defer func() { _ = gitRepoWrapper.Close() }()

	gitRepo := gitRepoWrapper.GetRepo()
	if gitRepo == nil {
		return nil, fmt.Errorf("failed to get repository")
	}

	// Get tree entries using go-git directly
	// Resolve reference
	refObj, err := gitRepo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return nil, fmt.Errorf("failed to resolve reference: %w", err)
	}

	// Get commit
	commit, err := gitRepo.CommitObject(*refObj)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit: %w", err)
	}

	// Get tree
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get tree: %w", err)
	}

	// Navigate to the specified path
	targetTree := tree
	if path != "" {
		treeEntry, err := tree.FindEntry(path)
		if err != nil {
			return nil, fmt.Errorf("failed to find path: %w", err)
		}
		if treeEntry.Mode != filemode.Dir {
			return nil, fmt.Errorf("path is not a tree: %s", path)
		}
		targetTree, err = gitRepo.TreeObject(treeEntry.Hash)
		if err != nil {
			return nil, fmt.Errorf("failed to get target tree: %w", err)
		}
	}

	// Convert tree entries to TreeEntry format
	var result []TreeEntry
	for _, entry := range targetTree.Entries {
		var entryType string
		switch entry.Mode {
		case filemode.Dir:
			entryType = "tree"
		case filemode.Submodule:
			entryType = "commit"
		case filemode.Symlink:
			entryType = "symlink"
		default:
			entryType = "blob"
		}

		result = append(result, TreeEntry{
			Path: entry.Name,
			Name: entry.Name,
			Type: entryType,
		})
	}

	// 정렬: 트리(디렉토리)가 먼저 오고, 그 다음 blob(파일), 같은 타입 내에서는 파일명 아스키 정렬
	sort.Slice(result, func(i, j int) bool {
		// 타입별 우선순위 결정
		var typeI, typeJ int
		switch result[i].Type {
		case "tree":
			typeI = 0
		case "blob":
			typeI = 1
		case "commit":
			typeI = 2
		default:
			typeI = 3
		}

		switch result[j].Type {
		case "tree":
			typeJ = 0
		case "blob":
			typeJ = 1
		case "commit":
			typeJ = 2
		default:
			typeJ = 3
		}

		// 타입이 다르면 타입 우선순위로 정렬 (작은 값이 먼저)
		if typeI != typeJ {
			return typeI < typeJ
		}

		// 같은 타입이면 파일명으로 아스키 정렬
		return result[i].Name < result[j].Name
	})

	return result, nil
}

// getTreeStructure retrieves the tree structure using the Git repository interface
func (h *TreeHandler) getTreeStructure(repoPath, ref, path string) (*TreeEntriesResponse, error) {
	// Use GetTreeEntries service function
	treeEntries, err := GetTreeEntries(h.pathConfig, h.logger, repoPath, ref, path)
	if err != nil {
		return nil, err
	}

	// Normalize base path (remove leading/trailing slashes)
	basePath := strings.Trim(path, "/")

	// Convert to response format with full paths
	var responseEntries []TreeEntry
	for _, entry := range treeEntries {
		// Path is the full path from repository root
		fullPath := entry.Name
		if basePath != "" {
			fullPath = basePath + "/" + entry.Name
		}

		// For submodules, ensure type is "tree" (GetTreeEntries returns "commit" for submodules)
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

	// 정렬: 트리(디렉토리)가 먼저 오고, 그 다음 blob(파일), 같은 타입 내에서는 파일명 아스키 정렬
	sort.Slice(responseEntries, func(i, j int) bool {
		// 타입별 우선순위 결정
		var typeI, typeJ int
		switch responseEntries[i].Type {
		case "tree":
			typeI = 0
		case "blob":
			typeI = 1
		case "commit":
			typeI = 2
		default:
			typeI = 3
		}

		switch responseEntries[j].Type {
		case "tree":
			typeJ = 0
		case "blob":
			typeJ = 1
		case "commit":
			typeJ = 2
		default:
			typeJ = 3
		}

		// 타입이 다르면 타입 우선순위로 정렬 (작은 값이 먼저)
		if typeI != typeJ {
			return typeI < typeJ
		}

		// 같은 타입이면 파일명으로 아스키 정렬
		return responseEntries[i].Name < responseEntries[j].Name
	})

	return &TreeEntriesResponse{
		Success: true,
		Entries: responseEntries,
	}, nil
}

// getTreeStructureRecursive retrieves tree structure recursively from root to the specified path
// Returns a map where keys are directory paths and values are tree entries in that directory
func (h *TreeHandler) getTreeStructureRecursive(repoPath, ref, targetPath string) (map[string][]TreeEntry, error) {
	// Normalize target path
	targetPath = strings.Trim(targetPath, "/")

	// Build list of all parent directories from root to target path
	var pathsToExplore []string
	if targetPath == "" {
		// If target is root, only explore root
		pathsToExplore = []string{""}
	} else {
		// Start with root
		pathsToExplore = []string{""}

		// Split path into components and build all parent paths
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

	// Map to store results: path -> []TreeEntry
	result := make(map[string][]TreeEntry)

	// Explore each path from root to target using GetTreeEntries service
	for _, path := range pathsToExplore {
		// Use GetTreeEntries service function
		treeEntries, err := GetTreeEntries(h.pathConfig, h.logger, repoPath, ref, path)
		if err != nil {
			// For target path, return error; for parent paths, skip
			if strings.Trim(path, "/") == targetPath {
				return nil, err
			}
			continue
		}

		// Convert to TreeEntry format with full paths
		basePath := strings.Trim(path, "/")
		var responseEntries []TreeEntry

		for _, entry := range treeEntries {
			// For submodules, ensure type is "tree" (GetTreeEntries returns "commit" for submodules)
			entryType := entry.Type
			if entryType == "commit" {
				entryType = "tree"
			}

			// Path is the full path from repository root
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

		// 정렬: 트리(디렉토리)가 먼저 오고, 그 다음 blob(파일), 같은 타입 내에서는 파일명 아스키 정렬
		sort.Slice(responseEntries, func(i, j int) bool {
			// 타입별 우선순위 결정
			var typeI, typeJ int
			switch responseEntries[i].Type {
			case "tree":
				typeI = 0
			case "blob":
				typeI = 1
			case "commit":
				typeI = 2
			default:
				typeI = 3
			}

			switch responseEntries[j].Type {
			case "tree":
				typeJ = 0
			case "blob":
				typeJ = 1
			case "commit":
				typeJ = 2
			default:
				typeJ = 3
			}

			// 타입이 다르면 타입 우선순위로 정렬 (작은 값이 먼저)
			if typeI != typeJ {
				return typeI < typeJ
			}

			// 같은 타입이면 파일명으로 아스키 정렬
			return responseEntries[i].Name < responseEntries[j].Name
		})

		mapKey := path
		if mapKey == "" {
			mapKey = "/"
		}
		result[mapKey] = responseEntries
	}

	// Ensure the requested path exists in the result
	expectedKey := targetPath
	if expectedKey == "" {
		expectedKey = "/"
	}
	if _, ok := result[expectedKey]; !ok {
		return nil, fmt.Errorf("path not found: %s", targetPath)
	}

	return result, nil
}
