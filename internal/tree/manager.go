package tree

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
)

// TreeBuilder helps build hierarchical Git tree structures
type TreeBuilder struct {
	storer        storage.Storer
	logger        *logging.Logger
	files         map[string]TreeFileEntry
	objectManager *repositories.GitObjectManager
	owner         string
	repo          string
	repoPath      string
}

// TreeFileEntry represents a file entry in the tree
type TreeFileEntry struct {
	Hash plumbing.Hash
	Mode filemode.FileMode
}

// NewTreeBuilder creates a new tree builder
func NewTreeBuilder(storer storage.Storer, logger *logging.Logger) *TreeBuilder {
	return &TreeBuilder{
		storer: storer,
		logger: logger,
		files:  make(map[string]TreeFileEntry),
	}
}

// SetRepositoryInfo sets repository information for GetTree operations
func (tb *TreeBuilder) SetRepositoryInfo(objectManager *repositories.GitObjectManager, owner, repo, repoPath string) {
	tb.objectManager = objectManager
	tb.owner = owner
	tb.repo = repo
	tb.repoPath = repoPath
}

// LoadBaseTree loads an existing tree as the base
func (tb *TreeBuilder) LoadBaseTree(baseTree *object.Tree) error {
	return tb.loadTreeRecursive("", baseTree)
}

// loadTreeRecursive loads tree entries recursively
func (tb *TreeBuilder) loadTreeRecursive(prefix string, tree *object.Tree) error {
	for _, entry := range tree.Entries {
		fullPath := entry.Name
		if prefix != "" {
			fullPath = prefix + "/" + entry.Name
		}

		if entry.Mode == filemode.Dir {
			// Load subdirectory
			subTree, err := tree.Tree(entry.Name)
			if err != nil {
				return fmt.Errorf("failed to load subtree %s: %w", entry.Name, err)
			}
			if err := tb.loadTreeRecursive(fullPath, subTree); err != nil {
				return err
			}
		} else {
			// Add file entry
			tb.files[fullPath] = TreeFileEntry{
				Hash: entry.Hash,
				Mode: entry.Mode,
			}
		}
	}
	return nil
}

// HasFile checks if a file exists in the tree
func (tb *TreeBuilder) HasFile(path string) bool {
	_, exists := tb.files[path]
	return exists
}

// AddFile adds a file to the tree
func (tb *TreeBuilder) AddFile(path string, hash plumbing.Hash, mode filemode.FileMode) error {
	tb.files[path] = TreeFileEntry{
		Hash: hash,
		Mode: mode,
	}
	return nil
}

// RemoveFile removes a file from the tree
func (tb *TreeBuilder) RemoveFile(path string) error {
	delete(tb.files, path)
	return nil
}

// BuildTree builds the final tree structure
func (tb *TreeBuilder) BuildTree() (plumbing.Hash, error) {
	return tb.buildTreeRecursive("")
}

// buildTreeRecursive builds a tree for a specific directory path
func (tb *TreeBuilder) buildTreeRecursive(dirPath string) (plumbing.Hash, error) {
	var entries []object.TreeEntry
	subdirs := make(map[string]bool)

	// Find all files and subdirectories in this directory
	for filePath, fileEntry := range tb.files {
		var relativePath string
		if dirPath == "" {
			relativePath = filePath
		} else {
			if !strings.HasPrefix(filePath, dirPath+"/") {
				continue
			}
			relativePath = strings.TrimPrefix(filePath, dirPath+"/")
		}

		// Skip if this file is in a subdirectory
		parts := strings.Split(relativePath, "/")
		if len(parts) > 1 {
			subdirs[parts[0]] = true
			continue
		}

		// Add file entry
		entries = append(entries, object.TreeEntry{
			Name: parts[0],
			Mode: fileEntry.Mode,
			Hash: fileEntry.Hash,
		})
	}

	// Add subdirectory entries
	for subdirName := range subdirs {
		var subdirPath string
		if dirPath == "" {
			subdirPath = subdirName
		} else {
			subdirPath = dirPath + "/" + subdirName
		}

		subdirTreeHash, err := tb.buildTreeRecursive(subdirPath)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("failed to build subtree for %s: %w", subdirPath, err)
		}

		entries = append(entries, object.TreeEntry{
			Name: subdirName,
			Mode: filemode.Dir,
			Hash: subdirTreeHash,
		})
	}

	// Sort entries (Git requirement)
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[i].Name > entries[j].Name {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

	// Create tree object
	tree := &object.Tree{Entries: entries}

	// Encode and store the tree
	encodedTree := tb.storer.NewEncodedObject()
	if err := tree.Encode(encodedTree); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to encode tree: %w", err)
	}

	treeHash, err := tb.storer.SetEncodedObject(encodedTree)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to store tree: %w", err)
	}

	return treeHash, nil
}

// GetTree retrieves the directory structure at a given reference and optional path
// This method uses repositories package to open repository and get tree entries
func (tb *TreeBuilder) GetTree(ref string, path string) ([]TreeEntry, error) {
	if tb.objectManager == nil {
		return nil, fmt.Errorf("object manager not set, call SetRepositoryInfo first")
	}

	// Open repository using repositories package
	gitRepo, err := tb.objectManager.OpenRepository(tb.owner, tb.repo)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Resolve reference to commit hash
	commitHash, err := tb.objectManager.ResolveReference(gitRepo, ref)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve reference %s: %w", ref, err)
	}

	// Get tree from commit
	tree, err := tb.objectManager.GetTreeFromCommit(gitRepo, commitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get tree from commit: %w", err)
	}

	// Navigate to subdirectory if path is provided
	if path != "" && path != "/" {
		// go-git의 tree.Tree()는 상대 경로를 기대하므로 앞의 슬래시 제거
		cleanPath := strings.TrimPrefix(filepath.Clean(path), "/")
		subTree, err := tree.Tree(cleanPath)
		if err != nil {
			// 경로를 찾을 수 없는 에러인지 확인
			if err == object.ErrDirectoryNotFound || err == object.ErrEntryNotFound {
				return nil, fmt.Errorf("path not found: %s", path)
			}
			return nil, fmt.Errorf("failed to get subdirectory tree at path %s: %w", path, err)
		}
		tree = subTree
	}

	// 트리 항목들을 우리 포맷으로 변환
	var entries []TreeEntry
	for _, entry := range tree.Entries {
		// Convert file mode to entry type
		var entryType string
		switch entry.Mode {
		case filemode.Dir:
			entryType = "tree"
		case filemode.Submodule:
			entryType = "commit"
		default:
			entryType = "blob"
		}

		entries = append(entries, TreeEntry{
			Path: entry.Name,
			Name: entry.Name,
			Type: entryType,
		})
	}

	// 트리 항목 정렬: 폴더가 먼저, 파일이 뒤에 오고, 각 그룹 내에서 사전순 정렬
	sort.Slice(entries, func(i, j int) bool {
		// Directories first
		if entries[i].Type == "tree" && entries[j].Type != "tree" {
			return true
		}
		if entries[i].Type != "tree" && entries[j].Type == "tree" {
			return false
		}
		// Then alphabetical
		return entries[i].Name < entries[j].Name
	})

	return entries, nil
}
