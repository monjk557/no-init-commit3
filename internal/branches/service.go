package branches

import (
	"fmt"
	"time"

	"github.com/collabops/git-executor/internal/logging"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// BranchService는 브랜치 관련 비즈니스 로직을 처리합니다.
type BranchService struct {
	logger *logging.Logger
}

// NewBranchService는 새로운 브랜치 서비스를 생성합니다.
func NewBranchService(logger *logging.Logger) *BranchService {
	return &BranchService{
		logger: logger,
	}
}

// CreateBranchResult는 브랜치 생성 결과를 나타냅니다.
type CreateBranchResult struct {
	BranchName  string
	CommitSHA   string
	Source      string
	RefPath     string
	IsNewBranch bool
	Message     string
	CreatedAt   time.Time
	Details     CreateBranchDetails
}

// GetBranches는 저장소의 모든 브랜치를 가져옵니다.
func (s *BranchService) GetBranches(repo *gogit.Repository) ([]BranchInfo, error) {
	refs, err := repo.References()
	if err != nil {
		return nil, fmt.Errorf("failed to get references: %w", err)
	}
	defer refs.Close()

	var branches []BranchInfo
	headRef, _ := repo.Head()

	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().IsBranch() {
			branch := BranchInfo{
				Name:      ref.Name().Short(),
				CommitSHA: ref.Hash().String(),
				RefPath:   ref.Name().String(),
				IsHead:    false,
			}

			if headRef != nil {
				if headRef.Type() == plumbing.SymbolicReference {
					branch.IsHead = headRef.Target() == ref.Name()
				} else {
					branch.IsHead = headRef.Name() == ref.Name()
				}
			}

			// 커밋 날짜 가져오기
			commit, err := repo.CommitObject(ref.Hash())
			if err == nil {
				// 커밋이 존재하면 Author 날짜 사용 (Committer 날짜가 더 최신일 수 있지만 일반적으로 Author 사용)
				branch.Date = commit.Author.When
			}

			branches = append(branches, branch)
		}
		return nil
	})

	return branches, err
}

// CreateBranch는 새로운 브랜치를 생성합니다.
func (s *BranchService) CreateBranch(repo *gogit.Repository, req CreateBranchRequest) (*CreateBranchResult, error) {
	// Set default source if not provided - support both field names for compatibility
	source := req.Source
	if source == "" {
		source = req.SourceBranch
	}
	if source == "" {
		source = "HEAD"
	}

	// Check if branch already exists
	branchExists, _, err := s.checkBranchExists(repo, req.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to check branch existence: %w", err)
	}

	// Handle existing branch
	if branchExists && !req.Force {
		return nil, fmt.Errorf("branch '%s' already exists. Use force=true to overwrite", req.Name)
	}

	// Resolve source reference
	sourceCommit, err := s.resolveSourceReference(repo, source)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve source reference '%s': %w", source, err)
	}

	// Create the branch
	refPath := fmt.Sprintf("refs/heads/%s", req.Name)
	ref := plumbing.NewHashReference(plumbing.ReferenceName(refPath), sourceCommit)

	// Store the reference
	if err := repo.Storer.SetReference(ref); err != nil {
		return nil, fmt.Errorf("failed to create branch: %w", err)
	}

	// Update HEAD if checkout is requested
	if req.Checkout {
		headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName(refPath))
		if err := repo.Storer.SetReference(headRef); err != nil {
			s.logger.Warn("Failed to update HEAD to new branch",
				zap.String("branch", req.Name),
				zap.Error(err))
		}
	}

	result := &CreateBranchResult{
		BranchName:  req.Name,
		CommitSHA:   sourceCommit.String(),
		Source:      source,
		RefPath:     refPath,
		IsNewBranch: !branchExists,
		Message:     fmt.Sprintf("Branch '%s' created successfully", req.Name),
		CreatedAt:   time.Now(),
		Details: CreateBranchDetails{
			Checkout:  req.Checkout,
			ForceUsed: req.Force && branchExists,
			Author:    req.AuthorName,
		},
	}

	s.logger.Info("Branch created successfully",
		zap.String("branch", req.Name),
		zap.String("commit_sha", sourceCommit.String()),
		zap.String("source", source),
		zap.Bool("is_new_branch", !branchExists),
		zap.Bool("checkout", req.Checkout))

	return result, nil
}

// UpdateBranchResult는 브랜치 업데이트 결과를 나타냅니다.
type UpdateBranchResult struct {
	Operation     string // "rename" or "update"
	BranchName    string
	CommitHash    string
	PreviousHash  string
	RefPath       string
	FastForward   bool
	Forced        bool
	Message       string
	OldBranchName string // for rename operation
	NewBranchName string // for rename operation
	HeadUpdated   bool   // for rename operation
}

// UpdateBranch는 브랜치를 업데이트합니다 (rename 또는 commit update).
func (s *BranchService) UpdateBranch(repo *gogit.Repository, branchName string, req UpdateBranchRequest) (*UpdateBranchResult, error) {
	// Validate that either target or new_name is provided, but not both
	hasTarget := req.Target != ""
	hasNewName := req.NewName != ""

	if !hasTarget && !hasNewName {
		return nil, fmt.Errorf("either 'target' (for commit update) or 'new_name' (for rename) must be provided")
	}
	if hasTarget && hasNewName {
		return nil, fmt.Errorf("cannot provide both 'target' and 'new_name'. Use 'target' for commit update or 'new_name' for rename")
	}

	// Check if branch exists
	branchExists, currentCommit, err := s.checkBranchExists(repo, branchName)
	if err != nil {
		return nil, fmt.Errorf("failed to check branch existence: %w", err)
	}

	if !branchExists {
		return nil, fmt.Errorf("branch '%s' does not exist", branchName)
	}

	// Handle rename operation
	if hasNewName {
		return s.renameBranch(repo, branchName, req.NewName, currentCommit, req.Force)
	}

	// Handle commit update operation
	return s.updateBranchCommit(repo, branchName, req.Target, currentCommit, req.Force, req.Message)
}

// renameBranch는 브랜치 이름을 변경합니다.
func (s *BranchService) renameBranch(repo *gogit.Repository, oldName, newName string, currentCommit plumbing.Hash, force bool) (*UpdateBranchResult, error) {
	// Check if old and new names are the same
	if oldName == newName {
		return nil, fmt.Errorf("old branch name and new branch name cannot be the same")
	}

	// Check if new branch name already exists
	newBranchExists, _, err := s.checkBranchExists(repo, newName)
	if err != nil {
		return nil, fmt.Errorf("failed to check new branch existence: %w", err)
	}

	if newBranchExists && !force {
		return nil, fmt.Errorf("branch '%s' already exists. Use force=true to overwrite", newName)
	}

	// Check if HEAD points to the old branch
	headRef, err := repo.Head()
	if err != nil {
		headRef = nil
	}

	isHeadBranch := false
	oldRefPath := plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", oldName))
	if headRef != nil {
		if headRef.Type() == plumbing.SymbolicReference && headRef.Target() == oldRefPath {
			isHeadBranch = true
		} else if headRef.Name() == oldRefPath {
			isHeadBranch = true
		}
	}

	// If new branch exists and force is true, delete it first
	if newBranchExists && force {
		newRefPath := plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", newName))
		if err := repo.Storer.RemoveReference(newRefPath); err != nil {
			s.logger.Warn("Failed to remove existing new branch reference",
				zap.String("new_branch", newName),
				zap.Error(err))
		}
	}

	// Read old branch reference
	oldRef, err := repo.Reference(oldRefPath, false)
	if err != nil {
		return nil, fmt.Errorf("failed to read old branch reference: %w", err)
	}

	// Create new branch reference with same commit hash
	newRefPath := plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", newName))
	newRef := plumbing.NewHashReference(newRefPath, oldRef.Hash())

	if err := repo.Storer.SetReference(newRef); err != nil {
		return nil, fmt.Errorf("failed to create new branch reference: %w", err)
	}

	// Update HEAD if it pointed to the old branch
	if isHeadBranch {
		newHeadRef := plumbing.NewSymbolicReference(plumbing.HEAD, newRefPath)
		if err := repo.Storer.SetReference(newHeadRef); err != nil {
			s.logger.Warn("Failed to update HEAD to renamed branch",
				zap.String("new_branch", newName),
				zap.Error(err))
		} else {
			s.logger.Info("Updated HEAD to renamed branch",
				zap.String("new_branch", newName))
		}
	}

	// Delete old branch reference
	if err := repo.Storer.RemoveReference(oldRefPath); err != nil {
		// Cleanup: remove new reference if old deletion fails
		repo.Storer.RemoveReference(newRefPath)
		return nil, fmt.Errorf("failed to delete old branch reference: %w", err)
	}

	result := &UpdateBranchResult{
		Operation:     "rename",
		BranchName:    newName,
		CommitHash:    currentCommit.String(),
		PreviousHash:  currentCommit.String(),
		RefPath:       newRefPath.String(),
		OldBranchName: oldName,
		NewBranchName: newName,
		HeadUpdated:   isHeadBranch,
		Forced:        force && newBranchExists,
	}

	s.logger.Info("Branch renamed successfully",
		zap.String("old_branch", oldName),
		zap.String("new_branch", newName),
		zap.String("commit_hash", currentCommit.String()),
		zap.Bool("head_updated", isHeadBranch))

	return result, nil
}

// updateBranchCommit는 브랜치가 가리키는 커밋을 업데이트합니다.
func (s *BranchService) updateBranchCommit(repo *gogit.Repository, branchName, target string, currentCommit plumbing.Hash, force bool, message string) (*UpdateBranchResult, error) {
	// Resolve target reference
	targetCommit, err := s.resolveSourceReference(repo, target)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve target reference '%s': %w", target, err)
	}

	// Check if update is needed
	if currentCommit == targetCommit {
		return &UpdateBranchResult{
			Operation:    "update",
			BranchName:   branchName,
			CommitHash:   targetCommit.String(),
			PreviousHash: currentCommit.String(),
			FastForward:  true,
			Forced:       false,
			Message:      "Branch already points to target commit",
		}, nil
	}

	// Check if update is fast-forward (optional, for safety)
	isFastForward := false
	if !force {
		// Check if target commit is ahead of current commit
		commitIter, err := repo.Log(&gogit.LogOptions{From: targetCommit})
		if err == nil {
			_ = commitIter.ForEach(func(commit *object.Commit) error {
				if commit.Hash == currentCommit {
					isFastForward = true
					return fmt.Errorf("found") // break iteration
				}
				return nil
			})
		}

		if !isFastForward {
			return nil, fmt.Errorf("update is not a fast-forward. Current: %s, Target: %s. Use force=true to override", currentCommit.String()[:8], targetCommit.String()[:8])
		}
	} else {
		isFastForward = true // force update
	}

	// Update the branch reference
	refPath := fmt.Sprintf("refs/heads/%s", branchName)
	ref := plumbing.NewHashReference(plumbing.ReferenceName(refPath), targetCommit)

	if err := repo.Storer.SetReference(ref); err != nil {
		return nil, fmt.Errorf("failed to update branch: %w", err)
	}

	result := &UpdateBranchResult{
		Operation:    "update",
		BranchName:   branchName,
		CommitHash:   targetCommit.String(),
		PreviousHash: currentCommit.String(),
		RefPath:      refPath,
		FastForward:  isFastForward,
		Forced:       force,
		Message:      message,
	}

	s.logger.Info("Branch updated successfully",
		zap.String("branch", branchName),
		zap.String("from_commit", currentCommit.String()),
		zap.String("to_commit", targetCommit.String()),
		zap.Bool("fast_forward", isFastForward),
		zap.Bool("forced", force))

	return result, nil
}

// DeleteBranchResult는 브랜치 삭제 결과를 나타냅니다.
type DeleteBranchResult struct {
	BranchName string
	CommitHash string
}

// DeleteBranch는 브랜치를 삭제합니다.
func (s *BranchService) DeleteBranch(repo *gogit.Repository, branchName string) (*DeleteBranchResult, error) {
	// Check if branch exists
	branchExists, branchCommit, err := s.checkBranchExists(repo, branchName)
	if err != nil {
		return nil, fmt.Errorf("failed to check branch existence: %w", err)
	}

	if !branchExists {
		return nil, fmt.Errorf("branch '%s' does not exist", branchName)
	}

	// Delete the branch reference
	refPath := fmt.Sprintf("refs/heads/%s", branchName)
	if err := repo.Storer.RemoveReference(plumbing.ReferenceName(refPath)); err != nil {
		return nil, fmt.Errorf("failed to delete branch: %w", err)
	}

	result := &DeleteBranchResult{
		BranchName: branchName,
		CommitHash: branchCommit.String(),
	}

	s.logger.Info("Branch deleted successfully",
		zap.String("branch", branchName),
		zap.String("deleted_commit", branchCommit.String()))

	return result, nil
}

// resolveSourceReference resolves a source reference (branch/tag/commit) to a commit hash
func (s *BranchService) resolveSourceReference(repo *gogit.Repository, source string) (plumbing.Hash, error) {
	// Try as commit hash first
	if len(source) >= 7 && len(source) <= 40 {
		if hash := plumbing.NewHash(source); hash != plumbing.ZeroHash {
			if _, err := repo.CommitObject(hash); err == nil {
				return hash, nil
			}
		}
	}

	// Try as branch reference
	branchRef := plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", source))
	if ref, err := repo.Reference(branchRef, false); err == nil {
		return ref.Hash(), nil
	}

	// Try as tag reference
	tagRef := plumbing.ReferenceName(fmt.Sprintf("refs/tags/%s", source))
	if ref, err := repo.Reference(tagRef, false); err == nil {
		return ref.Hash(), nil
	}

	// Try HEAD
	if source == "HEAD" {
		if headRef, err := repo.Head(); err == nil {
			return headRef.Hash(), nil
		}
	}

	return plumbing.ZeroHash, fmt.Errorf("unable to resolve reference '%s'", source)
}

// checkBranchExists checks if a branch exists and returns its commit hash
func (s *BranchService) checkBranchExists(repo *gogit.Repository, branchName string) (bool, plumbing.Hash, error) {
	refName := plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", branchName))
	ref, err := repo.Reference(refName, false)
	if err != nil {
		if err == plumbing.ErrReferenceNotFound {
			return false, plumbing.ZeroHash, nil
		}
		return false, plumbing.ZeroHash, err
	}

	return true, ref.Hash(), nil
}

// SimpleBranchInfo는 간단한 브랜치 정보를 나타냅니다.
type SimpleBranchInfo struct {
	Name      string
	Hash      string
	IsRemote  bool
	IsCurrent bool
}

// GetBranches는 저장소의 모든 브랜치를 가져옵니다.
func GetBranches(repo *gogit.Repository) ([]SimpleBranchInfo, error) {
	refs, err := repo.References()
	if err != nil {
		return nil, fmt.Errorf("failed to get references: %w", err)
	}
	defer refs.Close()

	var branches []SimpleBranchInfo
	headRef, _ := repo.Head()

	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().IsBranch() {
			branch := SimpleBranchInfo{
				Name:      ref.Name().Short(),
				Hash:      ref.Hash().String(),
				IsRemote:  false,
				IsCurrent: false,
			}

			if headRef != nil {
				if headRef.Type() == plumbing.SymbolicReference {
					branch.IsCurrent = headRef.Target() == ref.Name()
				} else {
					branch.IsCurrent = headRef.Name() == ref.Name()
				}
			}

			branches = append(branches, branch)
		}
		return nil
	})

	return branches, err
}

// BranchAheadBehind represents ahead/behind counts for a branch compared to another branch
type BranchAheadBehind struct {
	Ahead  int
	Behind int
}

// CalculateAheadBehind calculates how many commits a branch is ahead/behind compared to a base branch
func CalculateAheadBehind(repo *gogit.Repository, branchCommit, baseCommit *object.Commit) (*BranchAheadBehind, error) {
	// Find merge base
	mergeBases, err := branchCommit.MergeBase(baseCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}
	if len(mergeBases) == 0 {
		return &BranchAheadBehind{Ahead: 0, Behind: 0}, nil
	}
	mergeBase := mergeBases[0]

	// Calculate ahead count (from merge base to branch commit)
	aheadCount, err := countCommitsBetween(repo, mergeBase, branchCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to count ahead commits: %w", err)
	}

	// Calculate behind count (from merge base to base commit)
	behindCount, err := countCommitsBetween(repo, mergeBase, baseCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to count behind commits: %w", err)
	}

	return &BranchAheadBehind{
		Ahead:  aheadCount,
		Behind: behindCount,
	}, nil
}

// countCommitsBetween counts commits between two commits (from excluded, to included)
// Iterates from 'to' backwards until 'from' hash is found
func countCommitsBetween(repo *gogit.Repository, from, to *object.Commit) (int, error) {
	if from.Hash == to.Hash {
		return 0, nil
	}

	iter, err := repo.Log(&gogit.LogOptions{From: to.Hash})
	if err != nil {
		return 0, fmt.Errorf("failed to get commit log: %w", err)
	}
	defer iter.Close()

	count := 0
	fromHash := from.Hash.String()

	walkErr := iter.ForEach(func(c *object.Commit) error {
		if c.Hash.String() == fromHash {
			return fmt.Errorf("stop iteration")
		}
		count++
		return nil
	})

	if walkErr != nil && walkErr.Error() == "stop iteration" {
		return count, nil
	}
	if walkErr == nil {
		return count, nil
	}
	return 0, fmt.Errorf("failed to iterate commits: %w", walkErr)
}
