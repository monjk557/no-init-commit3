package state

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/logging"
	"go.uber.org/zap"
)

const nativeGitTimeout = 5 * time.Second

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

// GetRepositoryState calculates and returns repository state using native git.
func (s *StateService) GetRepositoryState(owner, repo, repoPath string) (*RepositoryStateData, error) {
	// Check if repository exists
	if _, err := os.Stat(repoPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("repository does not exist: %s/%s", owner, repo)
	}

	ctx := context.Background()

	// Determine git dir: if repoPath has .git subdir, it's non-bare
	gitDir := repoPath
	isBare := s.isBareRepository(repoPath)
	if !isBare {
		gitDir = filepath.Join(repoPath, ".git")
	}

	// Get file info for timestamps
	fileInfo, err := os.Stat(repoPath)
	if err != nil {
		s.logger.Warn("Failed to get file info",
			zap.String("path", repoPath),
			zap.Error(err))
	}

	// Get HEAD info
	headBranch, headHash := s.nativeGetHeadInfo(ctx, gitDir)

	// Initialize response
	stateData := &RepositoryStateData{
		Success:        true,
		RepositoryName: repo,
		Owner:          owner,
		Timestamp:      time.Now().Format(time.RFC3339),
		Details: Details{
			RequestPath: fmt.Sprintf("/repos/%s/%s/stats", owner, repo),
			Method:      "GET",
			Includes:    make(map[string]bool),
			RequestType: "stats",
		},
	}

	// Build HEAD information
	if headHash != "" {
		headInfo := s.buildHeadInfo(ctx, gitDir, headBranch, headHash)
		stateData.Head = *headInfo
	}

	// Build references
	references := s.buildReferences(ctx, gitDir, headBranch)
	stateData.References = *references

	// Build repository info
	repoInfo := s.buildRepositoryInfo(ctx, gitDir, repoPath, isBare, fileInfo, headBranch, headHash)
	stateData.RepositoryInfo = *repoInfo

	return stateData, nil
}

// nativeGetHeadInfo returns the HEAD branch (symbolic ref) and its commit hash.
func (s *StateService) nativeGetHeadInfo(ctx context.Context, gitDir string) (string, string) {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// Get symbolic ref (branch name)
	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", gitDir, "symbolic-ref", "-q", "HEAD")
	out, err := cmd.Output()
	branch := ""
	if err == nil {
		branch = strings.TrimSpace(string(out))
	}

	// Get HEAD commit hash
	cmdCtx2, cancel2 := context.WithTimeout(ctx, 3*time.Second)
	defer cancel2()
	cmd2 := exec.CommandContext(cmdCtx2, "git", "--git-dir", gitDir, "rev-parse", "HEAD")
	out2, err2 := cmd2.Output()
	hash := ""
	if err2 == nil {
		hash = strings.TrimSpace(string(out2))
	}

	return branch, hash
}

// buildHeadInfo builds HeadInfo using native git.
func (s *StateService) buildHeadInfo(ctx context.Context, gitDir, headBranch, headHash string) *HeadInfo {
	isDetached := headBranch == ""

	headInfo := &HeadInfo{
		IsDetached: isDetached,
	}

	if !isDetached {
		// Symbolic ref → branch
		shortName := strings.TrimPrefix(headBranch, "refs/heads/")
		headInfo.Type = "branch"
		headInfo.Name = shortName
		headInfo.Target = headBranch
		headInfo.IsBranch = true
		headInfo.Reference = "HEAD"
	} else {
		headInfo.Type = "commit"
		if len(headHash) >= 7 {
			headInfo.Name = headHash[:7]
		}
		headInfo.Target = headHash
		headInfo.IsBranch = false
		headInfo.Reference = "HEAD"
	}

	// Get commit info
	commitInfo := s.nativeGetCommitInfo(ctx, gitDir, headHash)
	headInfo.Commit = commitInfo
	headInfo.CommitInfo = commitInfo
	headInfo.CommitSHA = headHash

	return headInfo
}

// nativeGetCommitInfo returns CommitInfo for a given hash using native git log.
func (s *StateService) nativeGetCommitInfo(ctx context.Context, gitDir, hash string) CommitInfo {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	// format: hash\tauthor_name\tauthor_email\tauthor_date\tcommitter_name\tcommitter_email\tcommitter_date\tsubject
	format := "%H\t%an\t%ae\t%aI\t%cn\t%ce\t%cI\t%s"
	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", gitDir, "log", "-1", "--format="+format, hash)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return CommitInfo{SHA: hash}
	}

	parts := strings.SplitN(strings.TrimSpace(string(out)), "\t", 8)
	if len(parts) < 8 {
		return CommitInfo{SHA: hash}
	}

	return CommitInfo{
		SHA:     parts[0],
		Message: parts[7],
		Author: AuthorInfo{
			Name:  parts[1],
			Email: parts[2],
			Date:  parts[3],
		},
		Committer: AuthorInfo{
			Name:  parts[4],
			Email: parts[5],
			Date:  parts[6],
		},
		CreatedAt: parts[3],
	}
}

// buildReferences builds References from repository using native git for-each-ref.
func (s *StateService) buildReferences(ctx context.Context, gitDir, headBranch string) *References {
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

	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	// Get all refs with commit info
	format := "%(refname)\t%(refname:short)\t%(objectname)\t%(*objectname)\t%(authordate:iso8601-strict)\t%(authorname)\t%(authoremail)\t%(subject)"
	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", gitDir,
		"for-each-ref", "refs/heads/", "refs/tags/", "refs/remotes/",
		"--format="+format)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return refs
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return refs
	}

	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 8)
		if len(parts) < 8 {
			continue
		}

		fullName := parts[0]
		shortName := parts[1]
		hash := parts[2]
		derefHash := parts[3]
		dateStr := parts[4]
		// authorName := parts[5] (available if needed)
		// authorEmail := parts[6] (available if needed)
		subject := parts[7]

		// For annotated tags, use deref hash
		commitHash := hash
		if derefHash != "" {
			commitHash = derefHash
		}

		isHead := fullName == headBranch

		lastMod := dateStr
		if lastMod == "" {
			lastMod = time.Now().Format(time.RFC3339)
		}

		refInfo := ReferenceInfo{
			Name:         shortName,
			FullName:     fullName,
			CommitSHA:    commitHash,
			IsHead:       isHead,
			IsProtected:  false,
			LastModified: lastMod,
		}

		// Build commit info if we have enough data
		if hash != "" && subject != "" {
			ci := s.nativeGetCommitInfo(ctx, gitDir, commitHash)
			refInfo.Commit = &ci
		}

		if strings.HasPrefix(fullName, "refs/heads/") {
			refs.Branches = append(refs.Branches, refInfo)
			refs.Count.Branches++
			refs.Count.Total++
		} else if strings.HasPrefix(fullName, "refs/tags/") {
			refs.Tags = append(refs.Tags, refInfo)
			refs.Count.Tags++
			refs.Count.Total++
		} else if strings.HasPrefix(fullName, "refs/remotes/") {
			refs.Count.Remotes++
			refs.Count.Total++
		}
	}

	return refs
}

// buildRepositoryInfo builds RepositoryInfo using native git.
func (s *StateService) buildRepositoryInfo(ctx context.Context, gitDir, repoPath string, isBare bool, fileInfo os.FileInfo, headBranch, headHash string) *RepositoryInfo {
	repoInfo := &RepositoryInfo{
		Path:   repoPath,
		IsBare: isBare,
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

	// Set git directory path
	if isBare {
		repoInfo.GitDirectory = repoPath
	} else {
		repoInfo.GitDirectory = filepath.Join(repoPath, ".git")
	}

	repoInfo.HasWorktree = false

	// Get default branch from HEAD
	if headBranch != "" && strings.HasPrefix(headBranch, "refs/heads/") {
		repoInfo.DefaultBranch = strings.TrimPrefix(headBranch, "refs/heads/")
	}

	// Count branches and tags using for-each-ref (fast)
	repoInfo.BranchCount = s.nativeCountRefs(ctx, gitDir, "refs/heads/")
	repoInfo.TagCount = s.nativeCountRefs(ctx, gitDir, "refs/tags/")

	// Count commits using rev-list --count
	if headHash != "" {
		repoInfo.CommitCount = s.nativeCountCommits(ctx, gitDir, headHash)
	}

	return repoInfo
}

// nativeCountRefs counts refs under a prefix using for-each-ref.
func (s *StateService) nativeCountRefs(ctx context.Context, gitDir, prefix string) int {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", gitDir, "for-each-ref", prefix, "--format=x")
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return 0
	}
	return len(strings.Split(raw, "\n"))
}

// nativeCountCommits counts commits reachable from a ref using rev-list --count.
func (s *StateService) nativeCountCommits(ctx context.Context, gitDir, ref string) int {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", gitDir, "rev-list", "--count", ref)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return count
}

// isBareRepository checks if a repository is bare
func (s *StateService) isBareRepository(path string) bool {
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

// getDirectorySize calculates the total size of a directory
func (s *StateService) getDirectorySize(path string) (int64, error) {
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
