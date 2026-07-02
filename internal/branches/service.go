package branches

import (
	"context"
	"fmt"
	"time"

	"github.com/collabops/git-executor/internal/logging"

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
// native git for-each-ref를 사용합니다.
func (s *BranchService) GetBranches(ctx context.Context, repoPath string, sort BranchSortField) ([]BranchInfo, error) {
	return NativeGetBranches(ctx, repoPath, sort)
}

// GetBranchesLight lists branches without committer emails (cheap wide scan).
// Pair with EnrichCommitterEmails on the paginated page. See Issue-5071.
func (s *BranchService) GetBranchesLight(ctx context.Context, repoPath string, sort BranchSortField) ([]BranchInfo, error) {
	return NativeGetBranchesLight(ctx, repoPath, sort)
}

// EnrichCommitterEmails fills committer emails for the given (paginated) branches.
func (s *BranchService) EnrichCommitterEmails(ctx context.Context, repoPath string, branches []BranchInfo) error {
	return NativeEnrichCommitterEmails(ctx, repoPath, branches)
}

// CreateBranch는 새로운 브랜치를 생성합니다 (native git).
func (s *BranchService) CreateBranch(ctx context.Context, repoPath string, req CreateBranchRequest) (*CreateBranchResult, error) {
	// Set default source if not provided - support both field names for compatibility
	source := req.Source
	if source == "" {
		source = req.SourceBranch
	}
	if source == "" {
		source = "HEAD"
	}

	// Check if branch already exists
	branchExists, _, err := NativeCheckBranchExists(ctx, repoPath, req.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to check branch existence: %w", err)
	}

	// Handle existing branch
	if branchExists && !req.Force {
		return nil, fmt.Errorf("branch '%s' already exists", req.Name)
	}

	// Resolve source reference
	sourceCommitHash, err := NativeResolveRef(ctx, repoPath, source)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve source reference '%s': %w", source, err)
	}

	// Create the branch: git update-ref refs/heads/<name> <hash>
	refPath := fmt.Sprintf("refs/heads/%s", req.Name)
	if err := NativeUpdateRef(ctx, repoPath, refPath, sourceCommitHash); err != nil {
		return nil, fmt.Errorf("failed to create branch: %w", err)
	}

	// Update HEAD if checkout is requested
	if req.Checkout {
		if err := NativeSetSymbolicRef(ctx, repoPath, refPath); err != nil {
			s.logger.Warn("Failed to update HEAD to new branch",
				zap.String("branch", req.Name),
				zap.Error(err))
		}
	}

	result := &CreateBranchResult{
		BranchName:  req.Name,
		CommitSHA:   sourceCommitHash,
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

	s.logger.LogInfo(ctx, "Branch created successfully",
		zap.String("event", "branch_created"),
		zap.String("branch", req.Name),
		zap.String("commit_sha", sourceCommitHash),
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
func (s *BranchService) UpdateBranch(ctx context.Context, repoPath string, branchName string, req UpdateBranchRequest) (*UpdateBranchResult, error) {
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
	branchExists, currentHash, err := NativeCheckBranchExists(ctx, repoPath, branchName)
	if err != nil {
		return nil, fmt.Errorf("failed to check branch existence: %w", err)
	}

	if !branchExists {
		return nil, fmt.Errorf("branch '%s' does not exist", branchName)
	}

	// Handle rename operation
	if hasNewName {
		return s.renameBranch(ctx, repoPath, branchName, req.NewName, currentHash, req.Force)
	}

	// Handle commit update operation
	return s.updateBranchCommit(ctx, repoPath, branchName, req.Target, currentHash, req.Force, req.Message)
}

// renameBranch는 브랜치 이름을 변경합니다 (native git).
func (s *BranchService) renameBranch(ctx context.Context, repoPath, oldName, newName, currentHash string, force bool) (*UpdateBranchResult, error) {
	// Check if old and new names are the same
	if oldName == newName {
		return nil, fmt.Errorf("old branch name and new branch name cannot be the same")
	}

	// Check if new branch name already exists
	newBranchExists, _, err := NativeCheckBranchExists(ctx, repoPath, newName)
	if err != nil {
		return nil, fmt.Errorf("failed to check new branch existence: %w", err)
	}

	if newBranchExists && !force {
		return nil, fmt.Errorf("branch '%s' already exists. Use force=true to overwrite", newName)
	}

	// Check if HEAD points to the old branch
	headRef := nativeGetHead(ctx, repoPath)
	oldRefPath := fmt.Sprintf("refs/heads/%s", oldName)
	isHeadBranch := headRef == oldRefPath

	// If new branch exists and force is true, delete it first
	if newBranchExists && force {
		newRefPath := fmt.Sprintf("refs/heads/%s", newName)
		if err := NativeDeleteRef(ctx, repoPath, newRefPath); err != nil {
			s.logger.LogWarning(ctx, "Failed to remove existing new branch reference",
				zap.String("event", "branch_rename"),
				zap.String("new_branch", newName),
				zap.Error(err))
		}
	}

	// Create new branch reference with same commit hash
	newRefPath := fmt.Sprintf("refs/heads/%s", newName)
	if err := NativeUpdateRef(ctx, repoPath, newRefPath, currentHash); err != nil {
		return nil, fmt.Errorf("failed to create new branch reference: %w", err)
	}

	// Update HEAD if it pointed to the old branch
	if isHeadBranch {
		if err := NativeSetSymbolicRef(ctx, repoPath, newRefPath); err != nil {
			s.logger.LogWarning(ctx, "Failed to update HEAD to renamed branch",
				zap.String("event", "branch_rename"),
				zap.String("new_branch", newName),
				zap.Error(err))
		} else {
			s.logger.LogInfo(ctx, "Updated HEAD to renamed branch",
				zap.String("event", "branch_rename"),
				zap.String("new_branch", newName))
		}
	}

	// Delete old branch reference
	if err := NativeDeleteRef(ctx, repoPath, oldRefPath); err != nil {
		// Cleanup: remove new reference if old deletion fails
		_ = NativeDeleteRef(ctx, repoPath, newRefPath)
		return nil, fmt.Errorf("failed to delete old branch reference: %w", err)
	}

	result := &UpdateBranchResult{
		Operation:     "rename",
		BranchName:    newName,
		CommitHash:    currentHash,
		PreviousHash:  currentHash,
		RefPath:       newRefPath,
		OldBranchName: oldName,
		NewBranchName: newName,
		HeadUpdated:   isHeadBranch,
		Forced:        force && newBranchExists,
	}

	s.logger.LogInfo(ctx, "Branch renamed successfully",
		zap.String("event", "branch_renamed"),
		zap.String("old_branch", oldName),
		zap.String("new_branch", newName),
		zap.String("commit_hash", currentHash),
		zap.Bool("head_updated", isHeadBranch))

	return result, nil
}

// updateBranchCommit는 브랜치가 가리키는 커밋을 업데이트합니다 (native git).
func (s *BranchService) updateBranchCommit(ctx context.Context, repoPath, branchName, target, currentHash string, force bool, message string) (*UpdateBranchResult, error) {
	// Resolve target reference
	targetHash, err := NativeResolveRef(ctx, repoPath, target)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve target reference '%s': %w", target, err)
	}

	// Check if update is needed
	if currentHash == targetHash {
		return &UpdateBranchResult{
			Operation:    "update",
			BranchName:   branchName,
			CommitHash:   targetHash,
			PreviousHash: currentHash,
			FastForward:  true,
			Forced:       false,
			Message:      "Branch already points to target commit",
		}, nil
	}

	// Check if update is fast-forward
	isFastForward := false
	if !force {
		isFastForward, err = NativeIsFastForward(ctx, repoPath, currentHash, targetHash)
		if err != nil {
			return nil, fmt.Errorf("failed to check fast-forward: %w", err)
		}

		if !isFastForward {
			return nil, fmt.Errorf("update is not a fast-forward. Current: %s, Target: %s. Use force=true to override", currentHash[:8], targetHash[:8])
		}
	} else {
		isFastForward = true // force update
	}

	// Update the branch reference
	refPath := fmt.Sprintf("refs/heads/%s", branchName)
	if err := NativeUpdateRef(ctx, repoPath, refPath, targetHash); err != nil {
		return nil, fmt.Errorf("failed to update branch: %w", err)
	}

	result := &UpdateBranchResult{
		Operation:    "update",
		BranchName:   branchName,
		CommitHash:   targetHash,
		PreviousHash: currentHash,
		RefPath:      refPath,
		FastForward:  isFastForward,
		Forced:       force,
		Message:      message,
	}

	s.logger.LogInfo(ctx, "Branch updated successfully",
		zap.String("event", "branch_updated"),
		zap.String("branch", branchName),
		zap.String("from_commit", currentHash),
		zap.String("to_commit", targetHash),
		zap.Bool("fast_forward", isFastForward),
		zap.Bool("forced", force))

	return result, nil
}

// DeleteBranchResult는 브랜치 삭제 결과를 나타냅니다.
type DeleteBranchResult struct {
	BranchName string
	CommitHash string
}

// DeleteBranch는 브랜치를 삭제합니다 (native git).
func (s *BranchService) DeleteBranch(ctx context.Context, repoPath string, branchName string) (*DeleteBranchResult, error) {
	// Check if branch exists
	branchExists, branchHash, err := NativeCheckBranchExists(ctx, repoPath, branchName)
	if err != nil {
		return nil, fmt.Errorf("failed to check branch existence: %w", err)
	}

	if !branchExists {
		return nil, fmt.Errorf("branch '%s' does not exist", branchName)
	}

	// Delete the branch reference
	refPath := fmt.Sprintf("refs/heads/%s", branchName)
	if err := NativeDeleteRef(ctx, repoPath, refPath); err != nil {
		return nil, fmt.Errorf("failed to delete branch: %w", err)
	}

	result := &DeleteBranchResult{
		BranchName: branchName,
		CommitHash: branchHash,
	}

	s.logger.LogInfo(ctx, "Branch deleted successfully",
		zap.String("event", "branch_deleted"),
		zap.String("branch", branchName),
		zap.String("deleted_commit", branchHash))

	return result, nil
}

// GetBranches는 저장소의 모든 브랜치를 가져옵니다.
// native git for-each-ref를 사용합니다.
func GetBranches(ctx context.Context, repoPath string) ([]SimpleBranchInfo, error) {
	return NativeGetSimpleBranches(ctx, repoPath)
}

// CalculateAheadBehind calculates how many commits a branch is ahead/behind compared to a base branch
// native git rev-list --count --left-right를 사용합니다.
func CalculateAheadBehind(ctx context.Context, repoPath, branchRef, baseRef string) (*BranchAheadBehind, error) {
	return NativeCalculateAheadBehind(ctx, repoPath, branchRef, baseRef)
}
