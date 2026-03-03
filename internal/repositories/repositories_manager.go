package repositories

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/logging"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// RepositoryWrapper wraps go-git repository for backward compatibility with overview/merge packages.
// New code should use repoPath string + native git commands instead.
type RepositoryWrapper struct {
	repo *gogit.Repository
	path string
	mu   sync.RWMutex
}

// Path returns the filesystem path of the repository
func (rw *RepositoryWrapper) Path() string {
	return rw.path
}

// Close releases any resources held by the repository
func (rw *RepositoryWrapper) Close() error {
	return nil
}

// GetRepo returns the underlying go-git repository (thread-safe).
// Deprecated: used by overview/merge only. New code should use native git.
func (rw *RepositoryWrapper) GetRepo() *gogit.Repository {
	rw.mu.RLock()
	defer rw.mu.RUnlock()
	return rw.repo
}

// ResolveReference resolves a Git reference to a commit hash (thread-safe).
// Deprecated: used by overview only. New code should use native git.
func (rw *RepositoryWrapper) ResolveReference(ref string) (plumbing.Hash, error) {
	rw.mu.RLock()
	defer rw.mu.RUnlock()

	if rw.repo == nil {
		return plumbing.ZeroHash, fmt.Errorf("repository is closed")
	}

	if ref == "" || ref == "HEAD" {
		head, err := rw.repo.Head()
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("failed to get HEAD: %w", err)
		}
		return head.Hash(), nil
	}

	// Try to resolve as a reference first
	refObj, err := rw.repo.Reference(plumbing.ReferenceName(ref), false)
	if err == nil {
		if strings.HasPrefix(ref, "refs/tags/") {
			tagHash := refObj.Hash()
			tagObj, err := rw.repo.TagObject(tagHash)
			if err == nil {
				return tagObj.Target, nil
			}
			return tagHash, nil
		}
		return refObj.Hash(), nil
	}

	// Try with refs/heads/ prefix (branch)
	refObj, err = rw.repo.Reference(plumbing.ReferenceName("refs/heads/"+ref), false)
	if err == nil {
		return refObj.Hash(), nil
	}

	// Try with refs/tags/ prefix (tag)
	refObj, err = rw.repo.Reference(plumbing.ReferenceName("refs/tags/"+ref), false)
	if err == nil {
		tagHash := refObj.Hash()
		tagObj, err := rw.repo.TagObject(tagHash)
		if err == nil {
			return tagObj.Target, nil
		}
		return tagHash, nil
	}

	// If not a reference, try as a hash
	hash := plumbing.NewHash(ref)
	if hash != plumbing.ZeroHash {
		return hash, nil
	}

	return plumbing.ZeroHash, fmt.Errorf("failed to resolve reference: %s", ref)
}

// CommitObject returns a commit object from the repository (thread-safe).
// Deprecated: used by overview/merge only. New code should use native git.
func (rw *RepositoryWrapper) CommitObject(hash plumbing.Hash) (*object.Commit, error) {
	rw.mu.RLock()
	defer rw.mu.RUnlock()

	if rw.repo == nil {
		return nil, fmt.Errorf("repository is closed")
	}

	return rw.repo.CommitObject(hash)
}

// SetReference sets a reference in the repository (thread-safe).
// Deprecated: used by overview/merge only. New code should use native git.
func (rw *RepositoryWrapper) SetReference(ref *plumbing.Reference) error {
	rw.mu.Lock()
	defer rw.mu.Unlock()

	if rw.repo == nil {
		return fmt.Errorf("repository is closed")
	}

	return rw.repo.Storer.SetReference(ref)
}

// DefaultRepositoryManager implements the RepositoryManager interface
type DefaultRepositoryManager struct {
	pathConfig PathConfig
	logger     *logging.Logger
}

// NewRepositoryManager creates a new repository manager
func NewRepositoryManager(pathConfig *PathConfig, logger *logging.Logger) (*DefaultRepositoryManager, error) {
	return &DefaultRepositoryManager{
		pathConfig: *pathConfig,
		logger:     logger,
	}, nil
}

// CreateRepository creates a new Git repository at the specified path
func (rm *DefaultRepositoryManager) CreateRepository(options *CreateRepositoryOptions) (*RepositoryInfo, error) {
	if options == nil {
		return nil, fmt.Errorf("options cannot be nil")
	}

	if options.DefaultBranch == "" {
		options.DefaultBranch = "main"
	}

	rm.logger.Info("Creating new repository",
		zap.String("path", options.Path),
		zap.Bool("bare", options.Bare),
		zap.String("default_branch", options.DefaultBranch),
		zap.Bool("initial_commit", options.InitialCommit))

	// Check if repository already exists
	exists, err := rm.RepositoryExists(options.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to check repository existence: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("repository already exists at path: %s", options.Path)
	}

	// Validate path
	if err := ValidateRepoPath(&rm.pathConfig, options.Path); err != nil {
		return nil, fmt.Errorf("invalid repository path: %w", err)
	}

	// Create directory structure
	if err := os.MkdirAll(filepath.Dir(options.Path), 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory structure: %w", err)
	}

	// Initialize Git repository using native git
	ctx := context.Background()
	if err := nativeInitBare(ctx, options.Path); err != nil {
		return nil, fmt.Errorf("failed to initialize git repository: %w", err)
	}

	// Set HEAD to default branch
	if err := nativeSetSymbolicRef(ctx, options.Path, "refs/heads/"+options.DefaultBranch); err != nil {
		rm.logger.Warn("Failed to set HEAD to default branch",
			zap.String("path", options.Path),
			zap.String("default_branch", options.DefaultBranch),
			zap.Error(err))
	}

	// Create repository info
	repoInfo := &RepositoryInfo{
		Path:          options.Path,
		Name:          rm.extractRepositoryName(options.Path),
		DefaultBranch: options.DefaultBranch,
		IsBare:        options.Bare,
		CreatedAt:     time.Now(),
		LastModified:  time.Now(),
	}

	// Create initial commit if requested
	if options.InitialCommit {
		commitHash, err := rm.createInitialCommit(ctx, options)
		if err != nil {
			rm.logger.Warn("Failed to create initial commit",
				zap.String("path", options.Path),
				zap.Error(err))
		} else {
			rm.logger.Info("Created initial commit",
				zap.String("path", options.Path),
				zap.String("commit_hash", commitHash))
		}
	}

	// Update repository statistics
	if err := rm.updateRepositoryStats(ctx, repoInfo); err != nil {
		rm.logger.Warn("Failed to update repository statistics",
			zap.String("path", options.Path),
			zap.Error(err))
	}

	rm.logger.Info("Repository created successfully",
		zap.String("path", options.Path),
		zap.String("name", repoInfo.Name))

	return repoInfo, nil
}

// OpenRepository opens an existing Git repository.
// Returns a RepositoryWrapper for backward compatibility with overview/merge.
func (rm *DefaultRepositoryManager) OpenRepository(path string) (*RepositoryWrapper, error) {
	return OpenRepositorySimple(path)
}

// RepositoryExists checks if a repository exists at the given path
func (rm *DefaultRepositoryManager) RepositoryExists(path string) (bool, error) {
	if path == "" {
		return false, fmt.Errorf("path cannot be empty")
	}

	// Check if directory exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("failed to check path: %w", err)
	}

	// Check for HEAD file (both bare and non-bare repos have it)
	headPath := filepath.Join(path, "HEAD")
	if _, err := os.Stat(headPath); os.IsNotExist(err) {
		// Also check .git/HEAD for non-bare repos
		dotGitHead := filepath.Join(path, ".git", "HEAD")
		if _, err := os.Stat(dotGitHead); os.IsNotExist(err) {
			return false, nil
		}
	}

	return true, nil
}

// DeleteRepository removes a repository from the filesystem
func (rm *DefaultRepositoryManager) DeleteRepository(path string) error {
	if path == "" {
		return fmt.Errorf("path cannot be empty")
	}

	if err := ValidateRepoPath(&rm.pathConfig, path); err != nil {
		return fmt.Errorf("invalid repository path: %w", err)
	}

	rm.logger.Info("Deleting repository", zap.String("path", path))

	exists, err := rm.RepositoryExists(path)
	if err != nil {
		return fmt.Errorf("failed to check repository existence: %w", err)
	}
	if !exists {
		return fmt.Errorf("repository does not exist at path: %s", path)
	}

	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("failed to remove repository directory: %w", err)
	}

	// Invalidate cache
	GetSimpleCache().Invalidate(path)

	rm.logger.Info("Repository deleted successfully", zap.String("path", path))
	return nil
}

// ListRepositories returns a list of repositories in a directory
func (rm *DefaultRepositoryManager) ListRepositories(rootPath string) ([]RepositoryInfo, error) {
	if rootPath == "" {
		rootPath = rm.pathConfig.GetRepoRoot()
	}

	var repositories []RepositoryInfo

	err := filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			return nil
		}

		exists, err := rm.RepositoryExists(path)
		if err != nil {
			return nil
		}

		if exists {
			repoInfo := &RepositoryInfo{
				Path:         path,
				Name:         rm.extractRepositoryName(path),
				IsBare:       rm.isBareRepository(path),
				CreatedAt:    info.ModTime(),
				LastModified: info.ModTime(),
			}

			ctx := context.Background()
			if err := rm.updateRepositoryStats(ctx, repoInfo); err != nil {
				rm.logger.Debug("Failed to update repository statistics",
					zap.String("path", path),
					zap.Error(err))
			}

			repositories = append(repositories, *repoInfo)
			return filepath.SkipDir
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk repository directory: %w", err)
	}

	return repositories, nil
}

// createInitialCommit creates an initial commit using native git plumbing.
func (rm *DefaultRepositoryManager) createInitialCommit(ctx context.Context, options *CreateRepositoryOptions) (string, error) {
	repoPath := options.Path

	// Prepare initial files
	initialFiles := options.InitialFiles
	if len(initialFiles) == 0 {
		initialFiles = map[string]string{
			"README.md": fmt.Sprintf("# Repository\n\nCreated: %s\n",
				time.Now().Format(time.RFC3339)),
		}
	}

	// Hash all blobs first
	blobHashes := make(map[string]string, len(initialFiles))
	for filename, content := range initialFiles {
		blobHash, err := nativeHashObject(ctx, repoPath, []byte(content))
		if err != nil {
			return "", fmt.Errorf("failed to hash blob for %s: %w", filename, err)
		}
		blobHashes[filename] = blobHash
	}

	// Build recursive tree structure for nested paths
	treeHash, err := rm.buildTreeFromFiles(ctx, repoPath, blobHashes)
	if err != nil {
		return "", fmt.Errorf("failed to create tree: %w", err)
	}

	// Set default author if not provided
	author := options.Author
	if author == nil {
		author = &CommitAuthor{
			Name:  "CollabOps Git Executor",
			Email: "git-executor@collabops.dev",
			Date:  time.Now(),
		}
	}

	sig := &nativeSignature{
		Name:  author.Name,
		Email: author.Email,
		When:  author.Date,
	}

	// Create commit
	commitHash, err := nativeCommitTree(ctx, repoPath, treeHash, nil, "Initial commit", sig, sig)
	if err != nil {
		return "", fmt.Errorf("failed to create commit: %w", err)
	}

	// Create default branch reference
	branchRef := fmt.Sprintf("refs/heads/%s", options.DefaultBranch)
	if err := nativeUpdateRef(ctx, repoPath, branchRef, commitHash); err != nil {
		return "", fmt.Errorf("failed to create branch reference: %w", err)
	}

	// Set HEAD to point to the default branch
	if err := nativeSetSymbolicRef(ctx, repoPath, branchRef); err != nil {
		return "", fmt.Errorf("failed to set HEAD reference: %w", err)
	}

	return commitHash, nil
}

// buildTreeFromFiles builds a recursive git tree from a flat map of path→blobHash.
// Handles nested paths (e.g., "dir/file.txt") by creating subtrees.
func (rm *DefaultRepositoryManager) buildTreeFromFiles(ctx context.Context, repoPath string, blobHashes map[string]string) (string, error) {
	// Group files by top-level directory
	type dirEntry struct {
		subFiles map[string]string // relative path → blob hash (for subdirectory)
	}

	topLevelBlobs := make(map[string]string) // name → blob hash (files at root)
	subdirs := make(map[string]*dirEntry)    // dir name → sub-files

	for path, hash := range blobHashes {
		parts := strings.SplitN(path, "/", 2)
		if len(parts) == 1 {
			// File at current level
			topLevelBlobs[parts[0]] = hash
		} else {
			// File in subdirectory
			dirName := parts[0]
			subPath := parts[1]
			if subdirs[dirName] == nil {
				subdirs[dirName] = &dirEntry{subFiles: make(map[string]string)}
			}
			subdirs[dirName].subFiles[subPath] = hash
		}
	}

	// Recursively build subtrees
	var entries []nativeMkTreeEntry

	for dirName, de := range subdirs {
		subTreeHash, err := rm.buildTreeFromFiles(ctx, repoPath, de.subFiles)
		if err != nil {
			return "", fmt.Errorf("failed to build subtree for %s: %w", dirName, err)
		}
		entries = append(entries, nativeMkTreeEntry{
			Mode: "040000",
			Type: "tree",
			Hash: subTreeHash,
			Name: dirName,
		})
	}

	// Add top-level blob entries
	for name, hash := range topLevelBlobs {
		entries = append(entries, nativeMkTreeEntry{
			Mode: "100644",
			Type: "blob",
			Hash: hash,
			Name: name,
		})
	}

	// Sort entries by name (git requirement)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	return nativeMkTree(ctx, repoPath, entries)
}

// extractRepositoryName extracts the repository name from a path
func (rm *DefaultRepositoryManager) extractRepositoryName(path string) string {
	name := filepath.Base(path)
	return strings.TrimSuffix(name, ".git")
}

// isBareRepository checks if a repository is bare
func (rm *DefaultRepositoryManager) isBareRepository(path string) bool {
	gitDir := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		return false
	}

	configPath := filepath.Join(path, "config")
	headPath := filepath.Join(path, "HEAD")

	_, configErr := os.Stat(configPath)
	_, headErr := os.Stat(headPath)

	return configErr == nil && headErr == nil
}

// updateRepositoryStats updates repository statistics using native git.
func (rm *DefaultRepositoryManager) updateRepositoryStats(ctx context.Context, repoInfo *RepositoryInfo) error {
	repoPath := repoInfo.Path

	// Get repository size
	if size, err := rm.getDirectorySize(repoPath); err == nil {
		repoInfo.Size = size
	}

	// Get default branch from HEAD via symbolic-ref
	symRef, err := nativeSymbolicRef(ctx, repoPath)
	if err == nil && strings.HasPrefix(symRef, "refs/heads/") {
		repoInfo.DefaultBranch = strings.TrimPrefix(symRef, "refs/heads/")
	}

	// Count branches
	branchCount, err := nativeForEachRefCount(ctx, repoPath, "refs/heads/")
	if err == nil {
		repoInfo.BranchCount = branchCount
	}

	// Count tags
	tagCount, err := nativeForEachRefCount(ctx, repoPath, "refs/tags/")
	if err == nil {
		repoInfo.TagCount = tagCount
	}

	// Count commits (only if HEAD resolves)
	if _, headExists := nativeRevParseVerify(ctx, repoPath, "HEAD"); headExists {
		commitCount, err := nativeRevListCount(ctx, repoPath, "HEAD")
		if err == nil {
			repoInfo.CommitCount = commitCount
		}
	}

	return nil
}

// getDirectorySize calculates the total size of a directory
func (rm *DefaultRepositoryManager) getDirectorySize(path string) (int64, error) {
	var size int64

	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})

	return size, err
}

// =============================================
// Backward compatibility: go-git cache layer
// Used by overview/merge packages that still need *gogit.Repository
// =============================================

// SimpleRepoCache는 스레드 안전한 간단한 저장소 캐시입니다.
type SimpleRepoCache struct {
	mu         sync.RWMutex
	repos      map[string]*cachedRepo
	pendingOps map[string]*sync.Mutex
}

type cachedRepo struct {
	repo     *gogit.Repository
	lastUsed time.Time
	path     string
}

var (
	globalSimpleCache *SimpleRepoCache
	initOnce          sync.Once
)

// GetSimpleCache는 전역 간단 캐시 인스턴스를 반환합니다.
func GetSimpleCache() *SimpleRepoCache {
	initOnce.Do(func() {
		globalSimpleCache = &SimpleRepoCache{
			repos:      make(map[string]*cachedRepo),
			pendingOps: make(map[string]*sync.Mutex),
		}
		go globalSimpleCache.cleanup()
	})
	return globalSimpleCache
}

// Get은 캐시에서 저장소를 가져오거나 캐시되어 있지 않으면 엽니다.
func (c *SimpleRepoCache) Get(path string) (*gogit.Repository, error) {
	c.mu.RLock()
	if cached, exists := c.repos[path]; exists {
		repo := cached.repo
		c.mu.RUnlock()

		c.mu.Lock()
		if cached, stillExists := c.repos[path]; stillExists {
			cached.lastUsed = time.Now()
		}
		c.mu.Unlock()

		return repo, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	pathMutex, exists := c.pendingOps[path]
	if !exists {
		pathMutex = &sync.Mutex{}
		c.pendingOps[path] = pathMutex
	}
	c.mu.Unlock()

	pathMutex.Lock()
	defer pathMutex.Unlock()

	c.mu.RLock()
	if cached, exists := c.repos[path]; exists {
		repo := cached.repo
		c.mu.RUnlock()

		c.mu.Lock()
		if cached, stillExists := c.repos[path]; stillExists {
			cached.lastUsed = time.Now()
		}
		c.mu.Unlock()

		return repo, nil
	}
	c.mu.RUnlock()

	repo, err := gogit.PlainOpen(path)
	if err != nil {
		c.mu.Lock()
		delete(c.pendingOps, path)
		c.mu.Unlock()
		return nil, err
	}

	c.mu.Lock()
	c.repos[path] = &cachedRepo{
		repo:     repo,
		lastUsed: time.Now(),
		path:     path,
	}
	delete(c.pendingOps, path)
	c.mu.Unlock()

	return repo, nil
}

// Invalidate removes a repository from cache.
func (c *SimpleRepoCache) Invalidate(path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, existed := c.repos[path]
	delete(c.repos, path)
	delete(c.pendingOps, path)
	return existed
}

func (c *SimpleRepoCache) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		c.mu.Lock()
		cutoff := time.Now().Add(-10 * time.Minute)
		var toRemove []string

		for path, cached := range c.repos {
			if cached.lastUsed.Before(cutoff) {
				toRemove = append(toRemove, path)
			}
		}

		for _, path := range toRemove {
			delete(c.repos, path)
			delete(c.pendingOps, path)
		}
		c.mu.Unlock()
	}
}

// OpenRepositorySimple opens a repository with caching (backward compat for overview/merge).
func OpenRepositorySimple(path string) (*RepositoryWrapper, error) {
	cache := GetSimpleCache()
	gitRepo, err := cache.Get(path)
	if err != nil {
		return nil, err
	}

	return &RepositoryWrapper{
		path: path,
		repo: gitRepo,
		mu:   sync.RWMutex{},
	}, nil
}

// OpenRepositoryNoCache opens the repository without cache (backward compat for overview/merge).
func OpenRepositoryNoCache(path string) (*RepositoryWrapper, error) {
	gitRepo, err := gogit.PlainOpen(path)
	if err != nil {
		return nil, err
	}
	return &RepositoryWrapper{
		path: path,
		repo: gitRepo,
		mu:   sync.RWMutex{},
	}, nil
}
