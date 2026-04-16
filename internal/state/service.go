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
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/gitutil"
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
// Parallelized: HEAD info, references, commit count, and repo size run concurrently.
func (s *StateService) GetRepositoryState(owner, repo, repoPath string) (*RepositoryStateData, error) {
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

	fileInfo, err := os.Stat(repoPath)
	if err != nil {
		s.logger.Warn("Failed to get file info",
			zap.String("path", repoPath),
			zap.Error(err))
	}

	// Phase 1: HEAD info (needed by phases 2 & 3)
	headBranch, headHash := s.nativeGetHeadInfo(ctx, gitDir)

	// Phase 2: Parallel — head commit, references, commit count, repo size
	var (
		wg          sync.WaitGroup
		headInfo    *HeadInfo
		references  *References
		commitCount int
		repoSize    int64
		repoSizeOk  bool
	)

	// goroutine 1: HEAD commit details
	wg.Add(1)
	go func() {
		defer wg.Done()
		if headHash != "" {
			headInfo = s.buildHeadInfo(ctx, gitDir, headBranch, headHash)
		}
	}()

	// goroutine 2: references (branches + tags from single for-each-ref, no per-ref git log)
	wg.Add(1)
	go func() {
		defer wg.Done()
		references = s.buildReferences(ctx, gitDir, headBranch)
	}()

	// goroutine 3: commit count
	wg.Add(1)
	go func() {
		defer wg.Done()
		if headHash != "" {
			commitCount = s.nativeCountCommits(ctx, gitDir, headHash)
		}
	}()

	// goroutine 4: repo size via git count-objects
	wg.Add(1)
	go func() {
		defer wg.Done()
		if size, err := s.nativeRepoSize(ctx, gitDir); err == nil {
			repoSize = size
			repoSizeOk = true
		}
	}()

	wg.Wait()

	// Assemble response
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

	if headInfo != nil {
		stateData.Head = *headInfo
	}

	if references != nil {
		stateData.References = *references
	}

	// Build repository info from already-collected data (no extra git calls)
	repoInfo := &RepositoryInfo{
		Path:   repoPath,
		IsBare: isBare,
	}

	if fileInfo != nil {
		repoInfo.CreatedAt = fileInfo.ModTime()
		repoInfo.LastModified = fileInfo.ModTime()
	}

	if repoSizeOk {
		repoInfo.Size = repoSize
	}

	if isBare {
		repoInfo.GitDirectory = repoPath
	} else {
		repoInfo.GitDirectory = filepath.Join(repoPath, ".git")
	}

	repoInfo.HasWorktree = false

	if headBranch != "" && strings.HasPrefix(headBranch, "refs/heads/") {
		repoInfo.DefaultBranch = strings.TrimPrefix(headBranch, "refs/heads/")
	}

	// Reuse counts from references (no redundant git calls)
	if references != nil {
		repoInfo.BranchCount = references.Count.Branches
		repoInfo.TagCount = references.Count.Tags
	}
	repoInfo.CommitCount = commitCount

	stateData.RepositoryInfo = *repoInfo

	return stateData, nil
}

// nativeGetHeadInfo returns the HEAD branch (symbolic ref) and its commit hash.
func (s *StateService) nativeGetHeadInfo(ctx context.Context, gitDir string) (string, string) {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", gitDir, "symbolic-ref", "-q", "HEAD")
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	branch := ""
	if err == nil {
		branch = strings.TrimSpace(string(out))
	}

	cmdCtx2, cancel2 := context.WithTimeout(ctx, 3*time.Second)
	defer cancel2()
	cmd2 := exec.CommandContext(cmdCtx2, "git", "--git-dir", gitDir, "rev-parse", "HEAD")
	out2, err2 := gitutil.OutputWithLimit(cmd2, gitutil.DefaultMaxOutput)
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

	format := "%H\t%an\t%ae\t%aI\t%cn\t%ce\t%cI\t%s"
	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", gitDir, "log", "-1", "--format="+format, hash)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
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

// buildReferences builds References from a single for-each-ref call.
// Commit metadata is extracted directly from the format — no per-ref git log calls.
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

	// Single for-each-ref call: extract all commit metadata inline
	format := "%(refname)\t%(refname:short)\t%(objectname)\t%(*objectname)\t%(authordate:iso8601-strict)\t%(authorname)\t%(authoremail)\t%(subject)\t%(committerdate:iso8601-strict)\t%(committername)\t%(committeremail)"
	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", gitDir,
		"for-each-ref", "refs/heads/", "refs/tags/", "refs/remotes/",
		"--format="+format)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
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
		parts := strings.SplitN(line, "\t", 11)
		if len(parts) < 8 {
			continue
		}

		fullName := parts[0]
		shortName := parts[1]
		hash := parts[2]
		derefHash := parts[3]
		authorDate := parts[4]
		authorName := parts[5]
		authorEmail := parts[6]
		subject := parts[7]

		// Extract committer fields (may be absent for lightweight tags)
		committerDate := ""
		committerName := ""
		committerEmail := ""
		if len(parts) >= 11 {
			committerDate = parts[8]
			committerName = parts[9]
			committerEmail = parts[10]
		}

		// For annotated tags, use deref hash
		commitHash := hash
		if derefHash != "" {
			commitHash = derefHash
		}

		isHead := fullName == headBranch

		lastMod := authorDate
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

		// Build CommitInfo directly from for-each-ref output (no extra git log call)
		if commitHash != "" && subject != "" {
			ci := CommitInfo{
				SHA:     commitHash,
				Message: subject,
				Author: AuthorInfo{
					Name:  authorName,
					Email: strings.Trim(authorEmail, "<>"),
					Date:  authorDate,
				},
				Committer: AuthorInfo{
					Name:  committerName,
					Email: strings.Trim(committerEmail, "<>"),
					Date:  committerDate,
				},
				CreatedAt: authorDate,
			}
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

// nativeCountCommits counts commits reachable from a ref using rev-list --count.
func (s *StateService) nativeCountCommits(ctx context.Context, gitDir, ref string) int {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", gitDir, "rev-list", "--count", ref)
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return 0
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return count
}

// nativeRepoSize returns the repository size in bytes using git count-objects.
// Much faster than filepath.Walk for large repositories.
func (s *StateService) nativeRepoSize(ctx context.Context, gitDir string) (int64, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", gitDir, "count-objects", "-v")
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return 0, fmt.Errorf("git count-objects: %w", err)
	}

	var totalSize int64
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		// size: loose object size in KiB
		// size-pack: packed object size in KiB
		if strings.HasPrefix(line, "size:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "size:"))
			if n, err := strconv.ParseInt(val, 10, 64); err == nil {
				totalSize += n * 1024
			}
		} else if strings.HasPrefix(line, "size-pack:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "size-pack:"))
			if n, err := strconv.ParseInt(val, 10, 64); err == nil {
				totalSize += n * 1024
			}
		}
	}

	return totalSize, nil
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
