package tree

import (
	"fmt"
	"strings"

	"github.com/collabops/git-executor/internal/repositories"
)

// getTreeStructure retrieves the tree structure using the Git repository interface
func (h *TreeHandler) getTreeStructure(repoPath, ref, path string) (*TreeEntriesResponse, error) {
	// Extract owner and repo from repoPath
	// repoPath format: /path/to/repos/owner/repo.git
	parts := strings.Split(repoPath, "/")
	var owner, repo string
	if len(parts) >= 2 {
		repo = strings.TrimSuffix(parts[len(parts)-1], ".git")
		if len(parts) >= 3 {
			owner = parts[len(parts)-2]
		}
	}

	// Create object manager using pathConfig and logger
	objectManager, err := repositories.NewGitObjectManager(h.pathConfig, h.logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create object manager: %w", err)
	}

	// Use TreeBuilder with repositories package
	treeBuilder := NewTreeBuilder(nil, nil) // storer and logger not needed for GetTree

	// Set repository info
	treeBuilder.SetRepositoryInfo(objectManager, owner, repo, repoPath)

	// Get tree entries
	entries, err := treeBuilder.GetTree(ref, path)
	if err != nil {
		return nil, fmt.Errorf("failed to get tree: %w", err)
	}

	// Convert to response format
	var responseEntries []TreeEntry
	// Normalize base path (remove leading/trailing slashes)
	basePath := strings.Trim(path, "/")

	for _, entry := range entries {
		// Convert type from git.EntryType to string expected by API
		entryType := ""
		switch entry.Type {
		case string(EntryTypeTree):
			entryType = "tree"
		case string(EntryTypeBlob):
			entryType = "blob"
		case string(EntryTypeCommit):
			// For submodules, we'll treat them as trees for the API response
			entryType = "tree"
		default:
			// Default to blob for unknown types
			entryType = "blob"
		}

		// Name is just the entry name (file or directory name)
		name := entry.Path

		// Path is the full path from repository root
		fullPath := entry.Path
		if basePath != "" {
			fullPath = basePath + "/" + entry.Path
		}

		responseEntries = append(responseEntries, TreeEntry{
			Type: entryType,
			Name: name,
			Path: fullPath,
		})
	}

	return &TreeEntriesResponse{
		Success: true,
		Data:    responseEntries,
	}, nil
}

// getTreeStructureRecursive retrieves tree structure recursively from root to the specified path
// Returns a map where keys are directory paths and values are tree entries in that directory
func (h *TreeHandler) getTreeStructureRecursive(repoPath, ref, targetPath string) (map[string][]TreeEntry, error) {
	// Open repository using the simple cache
	gitRepo, err := repositories.OpenRepositorySimple(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}
	defer func() { _ = gitRepo.Close() }()

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

	// Extract owner and repo from repoPath
	parts := strings.Split(repoPath, "/")
	var owner, repo string
	if len(parts) >= 2 {
		repo = strings.TrimSuffix(parts[len(parts)-1], ".git")
		if len(parts) >= 3 {
			owner = parts[len(parts)-2]
		}
	}

	// Create object manager using pathConfig and logger
	objectManager, err := repositories.NewGitObjectManager(h.pathConfig, h.logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create object manager: %w", err)
	}

	// Use TreeBuilder with repositories package
	treeBuilder := NewTreeBuilder(nil, nil)
	treeBuilder.SetRepositoryInfo(objectManager, owner, repo, repoPath)

	// Explore each path from root to target
	for _, path := range pathsToExplore {
		// Get tree entries for this path
		entries, err := treeBuilder.GetTree(ref, path)
		if err != nil {
			// If the requested path (or one of its parents) doesn't exist,
			// propagate the error so the handler can return 404.
			if strings.Trim(path, "/") == targetPath {
				return nil, fmt.Errorf("path not found: %w", err)
			}
			continue
		}

		// Convert to responses.TreeEntry format (same as getTreeStructure)
		basePath := strings.Trim(path, "/")
		var responseEntries []TreeEntry

		for _, entry := range entries {
			// Convert type from git.EntryType to string expected by API
			entryType := ""
			switch entry.Type {
			case string(EntryTypeTree):
				entryType = "tree"
			case string(EntryTypeBlob):
				entryType = "blob"
			case string(EntryTypeCommit):
				// For submodules, we'll treat them as trees for the API response
				entryType = "tree"
			default:
				// Default to blob for unknown types
				entryType = "blob"
			}

			// Name is just the entry name (file or directory name)
			name := entry.Path

			// Path is the full path from repository root
			fullPath := entry.Path
			if basePath != "" {
				fullPath = basePath + "/" + entry.Path
			}

			responseEntries = append(responseEntries, TreeEntry{
				Type: entryType,
				Name: name,
				Path: fullPath,
			})
		}

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
