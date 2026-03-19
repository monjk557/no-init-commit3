package fork

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"go.uber.org/zap"
)

const (
	// metadataFileName is the file stored inside the bare repo root to track fork relationships.
	metadataFileName = ".fork-metadata.json"

	// upstreamRemoteName is the git remote name used for the upstream (source) repository.
	upstreamRemoteName = "upstream"
)

// ForkService는 포크 관련 비즈니스 로직을 처리합니다.
type ForkService struct {
	logger     *logging.Logger
	pathConfig *repositories.PathConfig
}

// NewForkService는 새로운 포크 서비스를 생성합니다.
func NewForkService(logger *logging.Logger, pathConfig *repositories.PathConfig) *ForkService {
	return &ForkService{
		logger:     logger,
		pathConfig: pathConfig,
	}
}

// ForkResult는 포크 생성 결과를 나타냅니다.
type ForkResult struct {
	SourceOwner   string
	SourceRepo    string
	TargetOwner   string
	TargetRepo    string
	TargetPath    string
	DefaultBranch string
	CreatedAt     time.Time
	ForkMetadata  *ForkMetadata
}

// ForkRepository는 소스 저장소를 타겟 경로로 포크합니다.
func (s *ForkService) ForkRepository(ctx context.Context, sourceOwner, sourceRepo, targetOwner, targetRepo, forkedBy string) (*ForkResult, error) {
	// 1. Validate source repo exists
	sourcePath, err := repositories.GetRepoPath(s.pathConfig, sourceOwner, sourceRepo)
	if err != nil {
		return nil, fmt.Errorf("invalid source path: %w", err)
	}
	if err := repositories.EnsureRepoExists(s.pathConfig, sourcePath); err != nil {
		return nil, ErrSourceNotFound
	}

	// 2. Validate target path components
	if err := repositories.ValidatePathComponents(targetOwner, targetRepo); err != nil {
		return nil, fmt.Errorf("invalid target path components: %w", err)
	}

	// 3. Generate target path and ensure it does NOT exist
	targetPath, err := repositories.GetRepoPath(s.pathConfig, targetOwner, targetRepo)
	if err != nil {
		return nil, fmt.Errorf("invalid target path: %w", err)
	}
	if _, err := os.Stat(targetPath); err == nil {
		return nil, ErrForkAlreadyExists
	}

	// 4. Create parent directory
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create target directory: %w", err)
	}

	// 5. Clone bare repository
	s.logger.LogInfo(ctx, "Forking repository",
		zap.String("event", "fork_start"),
		zap.String("source", sourcePath),
		zap.String("target", targetPath))

	if err := nativeCloneBare(ctx, sourcePath, targetPath); err != nil {
		// Cleanup on failure
		_ = os.RemoveAll(targetPath)
		return nil, fmt.Errorf("failed to clone repository: %w", err)
	}

	// 6. git clone --bare sets 'origin' remote to the source path.
	// Rename 'origin' to 'upstream' for clarity.
	remotes, err := nativeListRemotes(ctx, targetPath)
	if err == nil {
		for _, r := range remotes {
			if r == "origin" {
				// Remove origin and add upstream
				_ = nativeRemoteRemove(ctx, targetPath, "origin")
				break
			}
		}
	}
	if err := nativeRemoteAdd(ctx, targetPath, upstreamRemoteName, sourcePath); err != nil {
		s.logger.Warn("Failed to add upstream remote",
			zap.String("target", targetPath),
			zap.Error(err))
	}

	// 7. Read default branch from source
	defaultBranch := "main"
	symRef, err := nativeSymbolicRef(ctx, targetPath)
	if err == nil && strings.HasPrefix(symRef, "refs/heads/") {
		defaultBranch = strings.TrimPrefix(symRef, "refs/heads/")
	}

	// 8. Write fork metadata
	now := time.Now()
	metadata := &ForkMetadata{
		SourceOwner: sourceOwner,
		SourceRepo:  sourceRepo,
		SourcePath:  sourcePath,
		ForkedAt:    now,
		ForkedBy:    forkedBy,
	}
	if err := s.writeMetadata(targetPath, metadata); err != nil {
		s.logger.Warn("Failed to write fork metadata",
			zap.String("target", targetPath),
			zap.Error(err))
	}

	s.logger.LogInfo(ctx, "Repository forked successfully",
		zap.String("event", "fork_complete"),
		zap.String("source", fmt.Sprintf("%s/%s", sourceOwner, sourceRepo)),
		zap.String("target", fmt.Sprintf("%s/%s", targetOwner, targetRepo)))

	return &ForkResult{
		SourceOwner:   sourceOwner,
		SourceRepo:    sourceRepo,
		TargetOwner:   targetOwner,
		TargetRepo:    targetRepo,
		TargetPath:    targetPath,
		DefaultBranch: defaultBranch,
		CreatedAt:     now,
		ForkMetadata:  metadata,
	}, nil
}

// GetForkMetadata는 저장소의 포크 메타데이터를 반환합니다.
func (s *ForkService) GetForkMetadata(repoPath string) (*ForkMetadata, error) {
	return s.readMetadata(repoPath)
}

// IsFork는 저장소가 포크인지 확인합니다.
func (s *ForkService) IsFork(repoPath string) bool {
	metaPath := filepath.Join(repoPath, metadataFileName)
	_, err := os.Stat(metaPath)
	return err == nil
}

// SyncUpstreamResult는 업스트림 동기화 결과를 나타냅니다.
type SyncUpstreamResult struct {
	BranchesSynced []SyncedBranch
	Conflicts      []ConflictBranch
}

// SyncUpstream은 포크된 저장소를 업스트림과 동기화합니다.
func (s *ForkService) SyncUpstream(ctx context.Context, repoPath string) (*SyncUpstreamResult, error) {
	// 1. Read fork metadata
	metadata, err := s.readMetadata(repoPath)
	if err != nil {
		return nil, ErrNotAFork
	}

	// 2. Validate upstream still exists
	if _, err := os.Stat(metadata.SourcePath); os.IsNotExist(err) {
		return nil, ErrUpstreamNotFound
	}

	// 3. Ensure upstream remote URL is correct (source may have moved)
	currentURL, err := nativeRemoteGetURL(ctx, repoPath, upstreamRemoteName)
	if err != nil {
		// Upstream remote doesn't exist, re-add it
		if addErr := nativeRemoteAdd(ctx, repoPath, upstreamRemoteName, metadata.SourcePath); addErr != nil {
			return nil, fmt.Errorf("failed to add upstream remote: %w", addErr)
		}
	} else if currentURL != metadata.SourcePath {
		if setErr := nativeRemoteSetURL(ctx, repoPath, upstreamRemoteName, metadata.SourcePath); setErr != nil {
			return nil, fmt.Errorf("failed to update upstream remote URL: %w", setErr)
		}
	}

	// 4. Fetch from upstream
	s.logger.LogInfo(ctx, "Fetching upstream",
		zap.String("event", "sync_upstream_fetch"),
		zap.String("repo", repoPath))

	if err := nativeFetchUpstream(ctx, repoPath, upstreamRemoteName); err != nil {
		return nil, fmt.Errorf("failed to fetch upstream: %w", err)
	}

	// 5. Compare local branches with upstream refs
	localBranches, err := nativeForEachRef(ctx, repoPath, "refs/heads/")
	if err != nil {
		return nil, fmt.Errorf("failed to list local branches: %w", err)
	}

	upstreamRefs, err := nativeForEachRef(ctx, repoPath, "refs/remotes/"+upstreamRemoteName+"/")
	if err != nil {
		return nil, fmt.Errorf("failed to list upstream refs: %w", err)
	}

	// Build upstream refs map (branch name -> hash)
	upstreamMap := make(map[string]string, len(upstreamRefs))
	for _, ref := range upstreamRefs {
		// Strip "upstream/" prefix from short name
		name := strings.TrimPrefix(ref.Name, upstreamRemoteName+"/")
		upstreamMap[name] = ref.Hash
	}

	result := &SyncUpstreamResult{}

	for _, local := range localBranches {
		upstreamHash, exists := upstreamMap[local.Name]
		if !exists {
			// Branch doesn't exist in upstream, skip
			continue
		}

		if local.Hash == upstreamHash {
			// Already in sync
			continue
		}

		// Check if local can be fast-forwarded to upstream
		mergeBase, err := nativeMergeBase(ctx, repoPath, local.Hash, upstreamHash)
		if err != nil {
			result.Conflicts = append(result.Conflicts, ConflictBranch{
				Name:   local.Name,
				Reason: "failed to find merge base",
			})
			continue
		}

		if mergeBase == local.Hash {
			// Local is behind upstream — fast-forward possible
			behindCount, _ := nativeRevListCountBetween(ctx, repoPath, local.Hash, upstreamHash)

			refPath := fmt.Sprintf("refs/heads/%s", local.Name)
			if err := nativeUpdateRef(ctx, repoPath, refPath, upstreamHash); err != nil {
				result.Conflicts = append(result.Conflicts, ConflictBranch{
					Name:   local.Name,
					Reason: fmt.Sprintf("failed to update ref: %v", err),
				})
				continue
			}

			result.BranchesSynced = append(result.BranchesSynced, SyncedBranch{
				Name:          local.Name,
				OldCommitSHA:  local.Hash,
				NewCommitSHA:  upstreamHash,
				CommitsBehind: behindCount,
			})
		} else {
			// Branches have diverged
			result.Conflicts = append(result.Conflicts, ConflictBranch{
				Name:   local.Name,
				Reason: "branches have diverged, manual merge required",
			})
		}
	}

	s.logger.LogInfo(ctx, "Upstream sync completed",
		zap.String("event", "sync_upstream_complete"),
		zap.String("repo", repoPath),
		zap.Int("synced", len(result.BranchesSynced)),
		zap.Int("conflicts", len(result.Conflicts)))

	return result, nil
}

// writeMetadata writes fork metadata to the bare repo root.
func (s *ForkService) writeMetadata(repoPath string, metadata *ForkMetadata) error {
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal fork metadata: %w", err)
	}

	metaPath := filepath.Join(repoPath, metadataFileName)
	if err := os.WriteFile(metaPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write fork metadata: %w", err)
	}

	return nil
}

// readMetadata reads fork metadata from the bare repo root.
func (s *ForkService) readMetadata(repoPath string) (*ForkMetadata, error) {
	metaPath := filepath.Join(repoPath, metadataFileName)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotAFork
		}
		return nil, fmt.Errorf("failed to read fork metadata: %w", err)
	}

	var metadata ForkMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse fork metadata: %w", err)
	}

	return &metadata, nil
}
