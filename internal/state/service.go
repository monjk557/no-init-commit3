package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/logging"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// StateService provides repository state calculation
type StateService struct {
	logger *logging.Logger
}

// NewStateService creates a new state service
func NewStateService(logger *logging.Logger) (*StateService, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}

	return &StateService{
		logger: logger,
	}, nil
}

// GetRepositoryState calculates and returns repository state
func (s *StateService) GetRepositoryState(owner, repo, repoPath string) (*RepositoryStateData, error) {
	// Check if repository exists
	if _, err := os.Stat(repoPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("repository does not exist: %s/%s", owner, repo)
	}

	// Open repository
	gitRepo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Get file info for timestamps
	fileInfo, err := os.Stat(repoPath)
	if err != nil {
		s.logger.Warn("Failed to get file info",
			zap.String("path", repoPath),
			zap.Error(err))
	}

	// Get HEAD to determine current branch
	head, err := gitRepo.Head()
	var currentBranchName string
	if err == nil && head != nil && head.Type() == plumbing.SymbolicReference {
		currentBranchName = head.Target().Short()
	}

	// Initialize response
	stateData := &RepositoryStateData{
		Success:        true,
		RepositoryName: repo,
		Owner:          owner,
		Timestamp:      time.Now().Format(time.RFC3339),
		Details: Details{
			RequestPath: fmt.Sprintf("/repos/%s/%s/stats", owner, repo),
			Method:      "GET",
			Includes:    make(map[string]bool), // Empty dictionary by default
			RequestType: "stats",
		},
	}

	// Get HEAD information
	if head != nil && err == nil {
		headInfo, err := s.buildHeadInfo(gitRepo, head, currentBranchName)
		if err == nil {
			stateData.Head = *headInfo
		}
	}

	// Get references (branches and tags)
	references, err := s.buildReferences(gitRepo, currentBranchName)
	if err == nil {
		stateData.References = *references
	}

	// Get repository info
	repoInfo, err := s.buildRepositoryInfo(gitRepo, repoPath, fileInfo)
	if err == nil {
		stateData.RepositoryInfo = *repoInfo
	}

	return stateData, nil
}

// buildHeadInfo builds HeadInfo from HEAD reference
func (s *StateService) buildHeadInfo(gitRepo *gogit.Repository, head *plumbing.Reference, currentBranchName string) (*HeadInfo, error) {
	headInfo := &HeadInfo{
		Reference:  head.Name().String(),
		IsDetached: head.Type() == plumbing.HashReference,
	}

	// Determine type and name
	if head.Type() == plumbing.SymbolicReference {
		headInfo.Type = "branch"
		headInfo.Name = head.Target().Short()
		headInfo.Target = head.Target().String()
		headInfo.IsBranch = true
	} else {
		headInfo.Type = "commit"
		headInfo.Name = head.Hash().String()[:7] // Short hash
		headInfo.Target = head.Hash().String()
		headInfo.IsBranch = false
	}

	// Get commit information
	commit, err := gitRepo.CommitObject(head.Hash())
	if err != nil {
		return headInfo, fmt.Errorf("failed to get commit: %w", err)
	}

	commitInfo := s.buildCommitInfo(commit)
	headInfo.Commit = commitInfo
	headInfo.CommitInfo = commitInfo
	headInfo.CommitSHA = commit.Hash.String()

	return headInfo, nil
}

// buildCommitInfo builds CommitInfo from commit object
func (s *StateService) buildCommitInfo(commit *object.Commit) CommitInfo {
	commitInfo := CommitInfo{
		SHA:       commit.Hash.String(),
		Message:   commit.Message,
		CreatedAt: commit.Author.When.Format(time.RFC3339),
		Author: AuthorInfo{
			Name:  commit.Author.Name,
			Email: commit.Author.Email,
			Date:  commit.Author.When.Format(time.RFC3339),
		},
		Committer: AuthorInfo{
			Name:  commit.Committer.Name,
			Email: commit.Committer.Email,
			Date:  commit.Committer.When.Format(time.RFC3339),
		},
	}

	return commitInfo
}

// buildReferences builds References from repository
func (s *StateService) buildReferences(gitRepo *gogit.Repository, currentBranchName string) (*References, error) {
	refs := &References{
		Branches: []ReferenceInfo{},
		Tags:     []ReferenceInfo{},
		Count: ReferenceCount{
			Branches: 0,
			Tags:     0,
			Remotes:  0,
			Total:    0,
		},
	}

	// Get HEAD to determine which branch is current
	head, err := gitRepo.Head()
	var headTarget string
	if err == nil && head != nil && head.Type() == plumbing.SymbolicReference {
		headTarget = head.Target().String()
	}

	iter, err := gitRepo.References()
	if err != nil {
		return nil, fmt.Errorf("failed to get references: %w", err)
	}

	err = iter.ForEach(func(ref *plumbing.Reference) error {
		refInfo := ReferenceInfo{
			Name:        ref.Name().Short(),
			FullName:    ref.Name().String(),
			CommitSHA:   ref.Hash().String(),
			IsHead:      ref.Name().String() == headTarget,
			IsProtected: false, // Default to false, can be enhanced later
		}

		// Get commit information if available
		commit, err := gitRepo.CommitObject(ref.Hash())
		if err == nil {
			commitInfo := s.buildCommitInfo(commit)
			refInfo.Commit = &commitInfo
			refInfo.LastModified = commit.Author.When.Format(time.RFC3339)
		} else {
			// If commit not found, use current time as fallback
			refInfo.LastModified = time.Now().Format(time.RFC3339)
		}

		if ref.Name().IsBranch() {
			refs.Branches = append(refs.Branches, refInfo)
			refs.Count.Branches++
			refs.Count.Total++
		} else if ref.Name().IsTag() {
			refs.Tags = append(refs.Tags, refInfo)
			refs.Count.Tags++
			refs.Count.Total++
		} else if ref.Name().IsRemote() {
			// Count remotes but don't add to branches/tags list
			refs.Count.Remotes++
			refs.Count.Total++
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to iterate references: %w", err)
	}

	return refs, nil
}

// buildRepositoryInfo builds RepositoryInfo from repository
func (s *StateService) buildRepositoryInfo(gitRepo *gogit.Repository, repoPath string, fileInfo os.FileInfo) (*RepositoryInfo, error) {
	repoInfo := &RepositoryInfo{
		Path: repoPath,
	}

	// Set timestamps
	if fileInfo != nil {
		repoInfo.CreatedAt = fileInfo.ModTime()
		repoInfo.LastModified = fileInfo.ModTime()
	}

	// Get repository size
	if size, err := s.getDirectorySize(repoPath); err == nil {
		repoInfo.Size = size
	}

	// Check if bare
	isBare := s.isBareRepository(repoPath)
	repoInfo.IsBare = isBare

	// Set git directory path
	if isBare {
		repoInfo.GitDirectory = repoPath
	} else {
		repoInfo.GitDirectory = filepath.Join(repoPath, ".git")
	}

	// Check for worktree (simplified - can be enhanced)
	repoInfo.HasWorktree = false // Default to false, can be enhanced later

	// Get default branch from HEAD
	head, err := gitRepo.Head()
	if err == nil && head.Type() == plumbing.SymbolicReference {
		targetBranch := head.Target().String()
		if strings.HasPrefix(targetBranch, "refs/heads/") {
			repoInfo.DefaultBranch = strings.TrimPrefix(targetBranch, "refs/heads/")
		}
	}

	// Count references
	refs, err := gitRepo.References()
	if err == nil {
		branchCount := 0
		tagCount := 0

		err = refs.ForEach(func(ref *plumbing.Reference) error {
			if ref.Name().IsBranch() {
				branchCount++
			} else if ref.Name().IsTag() {
				tagCount++
			}
			return nil
		})

		if err == nil {
			repoInfo.BranchCount = branchCount
			repoInfo.TagCount = tagCount
		}
	}

	// Count commits (this can be expensive for large repos)
	if head != nil {
		commitIter, err := gitRepo.Log(&gogit.LogOptions{From: head.Hash()})
		if err == nil {
			commitCount := 0
			err = commitIter.ForEach(func(c *object.Commit) error {
				commitCount++
				return nil
			})
			if err == nil {
				repoInfo.CommitCount = commitCount
			}
		}
	}

	return repoInfo, nil
}

// isBareRepository checks if a repository is bare
func (s *StateService) isBareRepository(path string) bool {
	// Check for presence of .git directory (indicates non-bare)
	gitDir := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		return false
	}

	// Check for typical bare repository files
	configPath := filepath.Join(path, "config")
	headPath := filepath.Join(path, "HEAD")

	_, configErr := os.Stat(configPath)
	_, headErr := os.Stat(headPath)

	return configErr == nil && headErr == nil
}

// getDirectorySize calculates the total size of a directory
func (s *StateService) getDirectorySize(path string) (int64, error) {
	var size int64

	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files that cause errors
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})

	return size, err
}
