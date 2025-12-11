package merge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// ConflictDetector는 Merge 충돌 감지 및 분석 기능을 제공합니다.
type ConflictDetector struct {
	pathConfig *repositories.PathConfig
	logger     *logging.Logger
}

// NewConflictDetector는 새로운 충돌 감지기 인스턴스를 생성합니다.
func NewConflictDetector(pathConfig *repositories.PathConfig, logger *logging.Logger) *ConflictDetector {
	return &ConflictDetector{
		pathConfig: pathConfig,
		logger:     logger,
	}
}

// AnalyzeMerge performs comprehensive merge conflict analysis
func (cd *ConflictDetector) AnalyzeMerge(ctx context.Context, req *MergeAnalysisRequest) (*MergeAnalysisResult, error) {
	cd.logger.Info("Starting merge conflict analysis",
		zap.String("owner", req.Owner),
		zap.String("repo", req.Repo),
		zap.String("source_ref", req.SourceRef),
		zap.String("target_ref", req.TargetRef),
		zap.String("strategy", string(req.Strategy)))

	// Use provided repository or open new one
	var repo *gogit.Repository
	var err error

	if req.Repository != nil {
		// Use already opened repository (효율적)
		repo = req.Repository
		cd.logger.Debug("Using provided repository instance")
	} else {
		// Open repository from path (기존 방식)
		repoPath := cd.pathConfig.FormatRepoPath(req.Owner, req.Repo)
		repo, err = gogit.PlainOpen(repoPath)
		if err != nil {
			return nil, fmt.Errorf("failed to open repository: %w", err)
		}
		cd.logger.Debug("Opened repository from path", zap.String("path", repoPath))
	}

	// Resolve source and target commits
	sourceCommit, err := cd.resolveCommit(repo, req.SourceRef)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve source ref %s: %w", req.SourceRef, err)
	}

	targetCommit, err := cd.resolveCommit(repo, req.TargetRef)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve target ref %s: %w", req.TargetRef, err)
	}

	cd.logger.Debug("Resolved commits",
		zap.String("source_commit", sourceCommit.Hash.String()),
		zap.String("target_commit", targetCommit.Hash.String()))

	// Check for fast-forward merge
	isFastForward, err := cd.isFastForwardMerge(targetCommit, sourceCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to check fast-forward merge: %w", err)
	}

	if isFastForward {
		cd.logger.Info("Fast-forward merge detected")
		return &MergeAnalysisResult{
			CanMerge:          true,
			HasConflicts:      false,
			IsFastForward:     true,
			ConflictCount:     0,
			FileConflicts:     []FileConflict{},
			SourceCommit:      convertCommitToInfo(sourceCommit),
			TargetCommit:      convertCommitToInfo(targetCommit),
			Strategy:          req.Strategy,
			AutoResolvable:    0,
			SeverityBreakdown: make(map[ConflictSeverity]int),
			EstimatedTime:     0,
			Recommendations:   []string{"Fast-forward merge - no conflicts expected"},
		}, nil
	}

	// Find merge base (common ancestor)
	mergeBase, err := cd.findMergeBase(sourceCommit, targetCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}

	cd.logger.Debug("Found merge base", zap.String("merge_base", mergeBase.Hash.String()))

	// Perform three-way merge analysis
	conflicts, err := cd.analyzeThreeWayMerge(ctx, repo, mergeBase, sourceCommit, targetCommit, req)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze three-way merge: %w", err)
	}

	// Calculate summary statistics
	result := &MergeAnalysisResult{
		CanMerge:          len(conflicts) == 0 || cd.allAutoResolvable(conflicts),
		HasConflicts:      len(conflicts) > 0,
		IsFastForward:     false,
		ConflictCount:     len(conflicts),
		FileConflicts:     conflicts,
		MergeBase:         convertCommitToInfo(mergeBase),
		SourceCommit:      convertCommitToInfo(sourceCommit),
		TargetCommit:      convertCommitToInfo(targetCommit),
		Strategy:          req.Strategy,
		AutoResolvable:    cd.countAutoResolvable(conflicts),
		SeverityBreakdown: cd.calculateSeverityBreakdown(conflicts),
		EstimatedTime:     cd.estimateResolutionTime(conflicts),
		Recommendations:   cd.generateRecommendations(conflicts, req.Strategy),
	}

	cd.logger.Info("Merge analysis completed",
		zap.Int("total_conflicts", result.ConflictCount),
		zap.Int("auto_resolvable", result.AutoResolvable),
		zap.Bool("can_merge", result.CanMerge),
		zap.Duration("estimated_time", result.EstimatedTime))

	return result, nil
}

// resolveCommit resolves a reference to a commit object
func (cd *ConflictDetector) resolveCommit(repo *gogit.Repository, ref string) (*object.Commit, error) {
	// Try to resolve as hash first
	if hash := plumbing.NewHash(ref); !hash.IsZero() {
		return repo.CommitObject(hash)
	}

	// Try to resolve as reference
	reference, err := repo.Reference(plumbing.ReferenceName(ref), true)
	if err == nil {
		return repo.CommitObject(reference.Hash())
	}

	// Try with refs/heads/ prefix for branch names
	if !strings.HasPrefix(ref, "refs/") {
		branchRef := "refs/heads/" + ref
		reference, err := repo.Reference(plumbing.ReferenceName(branchRef), true)
		if err == nil {
			return repo.CommitObject(reference.Hash())
		}

		// Try with refs/tags/ prefix for tag names
		tagRef := "refs/tags/" + ref
		reference, err = repo.Reference(plumbing.ReferenceName(tagRef), true)
		if err == nil {
			return repo.CommitObject(reference.Hash())
		}
	}

	return nil, fmt.Errorf("unable to resolve reference: %s", ref)
}

// isFastForwardMerge checks if the merge can be fast-forwarded
func (cd *ConflictDetector) isFastForwardMerge(base, head *object.Commit) (bool, error) {
	// Check if base is an ancestor of head
	isAncestor, err := base.IsAncestor(head)
	if err != nil {
		return false, fmt.Errorf("failed to check ancestry: %w", err)
	}
	return isAncestor, nil
}

// findMergeBase finds the common ancestor of two commits
func (cd *ConflictDetector) findMergeBase(commit1, commit2 *object.Commit) (*object.Commit, error) {
	// Use go-git's merge base functionality
	bases, err := commit1.MergeBase(commit2)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}

	if len(bases) == 0 {
		return nil, fmt.Errorf("no common ancestor found")
	}

	// Return the first (most recent) common ancestor
	return bases[0], nil
}

// analyzeThreeWayMerge performs detailed three-way merge analysis
func (cd *ConflictDetector) analyzeThreeWayMerge(ctx context.Context, repo *gogit.Repository, base, source, target *object.Commit, req *MergeAnalysisRequest) ([]FileConflict, error) {
	var conflicts []FileConflict

	// Get trees for all three commits
	baseTree, err := base.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get base tree: %w", err)
	}

	sourceTree, err := source.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get source tree: %w", err)
	}

	targetTree, err := target.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get target tree: %w", err)
	}

	// Compare trees to find differences
	baseFiles := make(map[string]*object.TreeEntry)
	sourceFiles := make(map[string]*object.TreeEntry)
	targetFiles := make(map[string]*object.TreeEntry)

	// Collect all files from all trees
	err = cd.collectTreeFiles(baseTree, baseFiles)
	if err != nil {
		return nil, fmt.Errorf("failed to collect base tree files: %w", err)
	}

	err = cd.collectTreeFiles(sourceTree, sourceFiles)
	if err != nil {
		return nil, fmt.Errorf("failed to collect source tree files: %w", err)
	}

	err = cd.collectTreeFiles(targetTree, targetFiles)
	if err != nil {
		return nil, fmt.Errorf("failed to collect target tree files: %w", err)
	}

	// Find all unique file paths
	allPaths := make(map[string]bool)
	for path := range baseFiles {
		allPaths[path] = true
	}
	for path := range sourceFiles {
		allPaths[path] = true
	}
	for path := range targetFiles {
		allPaths[path] = true
	}

	// Analyze each file for conflicts
	for path := range allPaths {
		conflict, err := cd.analyzeFileConflict(ctx, repo, path, baseFiles[path], sourceFiles[path], targetFiles[path], req)
		if err != nil {
			cd.logger.Warn("Failed to analyze file conflict", zap.String("path", path), zap.Error(err))
			continue
		}

		if conflict != nil {
			conflicts = append(conflicts, *conflict)
		}
	}

	return conflicts, nil
}

// collectTreeFiles recursively collects all files from a tree
func (cd *ConflictDetector) collectTreeFiles(tree *object.Tree, files map[string]*object.TreeEntry) error {
	return tree.Files().ForEach(func(file *object.File) error {
		files[file.Name] = &object.TreeEntry{
			Name: file.Name,
			Mode: file.Mode,
			Hash: file.Hash,
		}
		return nil
	})
}

// analyzeFileConflict analyzes a single file for conflicts
func (cd *ConflictDetector) analyzeFileConflict(ctx context.Context, repo *gogit.Repository, path string, base, source, target *object.TreeEntry, req *MergeAnalysisRequest) (*FileConflict, error) {
	// Determine file existence in each version
	baseExists := base != nil
	sourceExists := source != nil
	targetExists := target != nil

	// No conflict if file is unchanged
	if baseExists && sourceExists && targetExists &&
		base.Hash == source.Hash && base.Hash == target.Hash {
		return nil, nil
	}

	// Analyze conflict type and create file versions
	baseVersion := cd.createFileVersion(repo, base)
	sourceVersion := cd.createFileVersion(repo, source)
	targetVersion := cd.createFileVersion(repo, target)

	conflict := &FileConflict{
		FilePath:     path,
		OurVersion:   sourceVersion,
		TheirVersion: targetVersion,
		BaseVersion:  baseVersion,
		Regions:      []ConflictRegion{},
		Metadata:     make(map[string]interface{}),
	}

	// Determine conflict type
	conflict.ConflictType = cd.determineConflictType(baseExists, sourceExists, targetExists, base, source, target)
	conflict.Severity = cd.calculateConflictSeverity(conflict.ConflictType, sourceVersion, targetVersion)

	// Analyze content conflicts for text files
	if req.IncludeContent && conflict.ConflictType == ConflictTypeContent {
		regions, err := cd.analyzeContentConflicts(repo, base, source, target, req.MaxFileSize)
		if err != nil {
			cd.logger.Warn("Failed to analyze content conflicts", zap.String("path", path), zap.Error(err))
		} else {
			conflict.Regions = regions
		}
	}

	// Determine if auto-resolvable
	conflict.AutoResolvable = cd.isAutoResolvable(conflict)

	// Generate resolution suggestion if auto-resolvable
	if conflict.AutoResolvable && req.AutoResolve {
		resolution := cd.generateResolution(conflict)
		conflict.Resolution = resolution
	}

	return conflict, nil
}

// Helper methods for conflict analysis
func (cd *ConflictDetector) createFileVersion(repo *gogit.Repository, entry *object.TreeEntry) *FileVersion {
	if entry == nil {
		return &FileVersion{Exists: false}
	}

	version := &FileVersion{
		Hash:   entry.Hash,
		Mode:   uint32(entry.Mode),
		Exists: true,
	}

	// Get file object to determine additional properties
	if obj, err := repo.BlobObject(entry.Hash); err == nil {
		version.Size = obj.Size
		version.ContentHash = obj.Hash.String()

		// Check if binary (simple heuristic)
		if reader, err := obj.Reader(); err == nil {
			defer func() {
				if err := reader.Close(); err != nil {
					fmt.Printf("Warning: failed to close reader during cleanup: %v\n", err)
				}
			}()
			buffer := make([]byte, 512)
			if n, err := reader.Read(buffer); err == nil {
				version.IsBinary = cd.isBinaryContent(buffer[:n])
			}
		}
	}

	return version
}

// determineConflictType determines the type of conflict based on file states
func (cd *ConflictDetector) determineConflictType(baseExists, sourceExists, targetExists bool, base, source, target *object.TreeEntry) ConflictType {
	// File deleted in one branch, modified in other
	if baseExists && !sourceExists && targetExists {
		// Check if base and target have different hashes (indicating modification)
		if base != nil && target != nil && base.Hash != target.Hash {
			return ConflictTypeDelete
		} else if base == nil || target == nil {
			// If we don't have tree entries but have existence flags, assume it's a delete conflict
			return ConflictTypeDelete
		}
	}
	if baseExists && sourceExists && !targetExists {
		// Check if base and source have different hashes (indicating modification)
		if base != nil && source != nil && base.Hash != source.Hash {
			return ConflictTypeDelete
		} else if base == nil || source == nil {
			// If we don't have tree entries but have existence flags, assume it's a delete conflict
			return ConflictTypeDelete
		}
	}

	// File added in both branches
	if !baseExists && sourceExists && targetExists {
		if source != nil && target != nil && source.Hash != target.Hash {
			return ConflictTypeAdd
		} else if source == nil || target == nil {
			// If we don't have tree entries but both exist, assume it's an add conflict
			return ConflictTypeAdd
		}
	}

	// Mode conflicts
	if sourceExists && targetExists && source != nil && target != nil && source.Hash == target.Hash && source.Mode != target.Mode {
		return ConflictTypeMode
	}

	// Content conflicts (most common)
	if sourceExists && targetExists && source != nil && target != nil && source.Hash != target.Hash {
		return ConflictTypeContent
	}

	// Default fallback
	return ConflictTypeContent
}

// calculateConflictSeverity determines the severity of a conflict
func (cd *ConflictDetector) calculateConflictSeverity(conflictType ConflictType, sourceVersion, targetVersion *FileVersion) ConflictSeverity {
	switch conflictType {
	case ConflictTypeMode:
		return ConflictSeverityLow
	case ConflictTypeAdd:
		if sourceVersion != nil && targetVersion != nil && sourceVersion.IsBinary != targetVersion.IsBinary {
			return ConflictSeverityHigh
		}
		return ConflictSeverityMedium
	case ConflictTypeDelete:
		return ConflictSeverityHigh
	case ConflictTypeContent:
		if (sourceVersion != nil && sourceVersion.IsBinary) || (targetVersion != nil && targetVersion.IsBinary) {
			return ConflictSeverityCritical
		}
		return ConflictSeverityMedium
	default:
		return ConflictSeverityMedium
	}
}

// analyzeContentConflicts analyzes content-level conflicts within a file
func (cd *ConflictDetector) analyzeContentConflicts(repo *gogit.Repository, base, source, target *object.TreeEntry, maxFileSize int64) ([]ConflictRegion, error) {
	// This is a simplified implementation - in practice, you'd want more sophisticated
	// three-way merge analysis using libraries like go-git's merge package
	var regions []ConflictRegion

	// Get file contents
	baseContent, err := cd.getFileContent(repo, base, maxFileSize)
	if err != nil {
		return nil, fmt.Errorf("failed to get base content: %w", err)
	}

	sourceContent, err := cd.getFileContent(repo, source, maxFileSize)
	if err != nil {
		return nil, fmt.Errorf("failed to get source content: %w", err)
	}

	targetContent, err := cd.getFileContent(repo, target, maxFileSize)
	if err != nil {
		return nil, fmt.Errorf("failed to get target content: %w", err)
	}

	// Simple line-based conflict detection
	baseLines := strings.Split(baseContent, "\n")
	sourceLines := strings.Split(sourceContent, "\n")
	targetLines := strings.Split(targetContent, "\n")

	// This is a simplified algorithm - real implementation would use
	// Myers diff algorithm or similar for accurate three-way merge
	if len(sourceLines) != len(targetLines) || strings.Join(sourceLines, "\n") != strings.Join(targetLines, "\n") {
		regions = append(regions, ConflictRegion{
			StartLine:    1,
			EndLine:      len(sourceLines),
			OurContent:   strings.Join(sourceLines, "\n"),
			TheirContent: strings.Join(targetLines, "\n"),
			BaseContent:  strings.Join(baseLines, "\n"),
			MarkerStyle:  "standard",
		})
	}

	return regions, nil
}

// Helper methods
func (cd *ConflictDetector) getFileContent(repo *gogit.Repository, entry *object.TreeEntry, maxFileSize int64) (string, error) {
	if entry == nil {
		return "", nil
	}

	obj, err := repo.BlobObject(entry.Hash)
	if err != nil {
		return "", err
	}

	if obj.Size > maxFileSize {
		return "", fmt.Errorf("file too large: %d bytes", obj.Size)
	}

	reader, err := obj.Reader()
	if err != nil {
		return "", err
	}
	defer func() {
		if err := reader.Close(); err != nil {
			fmt.Printf("Warning: failed to close reader during cleanup: %v\n", err)
		}
	}()

	content := make([]byte, obj.Size)
	_, err = reader.Read(content)
	if err != nil {
		return "", err
	}

	return string(content), nil
}

func (cd *ConflictDetector) isBinaryContent(data []byte) bool {
	// Simple binary detection - check for null bytes
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}

func (cd *ConflictDetector) isAutoResolvable(conflict *FileConflict) bool {
	switch conflict.ConflictType {
	case ConflictTypeMode:
		return true // Mode conflicts are often auto-resolvable
	case ConflictTypeAdd:
		return false // Adding same file with different content needs manual review
	case ConflictTypeDelete:
		return false // Delete conflicts need manual review
	case ConflictTypeContent:
		return len(conflict.Regions) == 0 || (conflict.OurVersion != nil && !conflict.OurVersion.IsBinary && conflict.TheirVersion != nil && !conflict.TheirVersion.IsBinary)
	default:
		return false
	}
}

func (cd *ConflictDetector) generateResolution(conflict *FileConflict) *ConflictResolution {
	switch conflict.ConflictType {
	case ConflictTypeMode:
		// For mode conflicts, typically choose the more permissive mode
		return &ConflictResolution{
			Strategy:   "prefer_permissive_mode",
			Confidence: 0.8,
			Reasoning:  "Mode conflicts resolved by choosing more permissive file mode",
		}
	default:
		return &ConflictResolution{
			Strategy:   "manual_review_required",
			Confidence: 0.0,
			Reasoning:  "This conflict requires manual review and resolution",
		}
	}
}

func (cd *ConflictDetector) allAutoResolvable(conflicts []FileConflict) bool {
	for _, conflict := range conflicts {
		if !conflict.AutoResolvable {
			return false
		}
	}
	return true
}

func (cd *ConflictDetector) countAutoResolvable(conflicts []FileConflict) int {
	count := 0
	for _, conflict := range conflicts {
		if conflict.AutoResolvable {
			count++
		}
	}
	return count
}

func (cd *ConflictDetector) calculateSeverityBreakdown(conflicts []FileConflict) map[ConflictSeverity]int {
	breakdown := make(map[ConflictSeverity]int)
	for _, conflict := range conflicts {
		breakdown[conflict.Severity]++
	}
	return breakdown
}

func (cd *ConflictDetector) estimateResolutionTime(conflicts []FileConflict) time.Duration {
	var totalMinutes int
	for _, conflict := range conflicts {
		switch conflict.Severity {
		case ConflictSeverityLow:
			totalMinutes += 2
		case ConflictSeverityMedium:
			totalMinutes += 10
		case ConflictSeverityHigh:
			totalMinutes += 30
		case ConflictSeverityCritical:
			totalMinutes += 60
		}
	}
	return time.Duration(totalMinutes) * time.Minute
}

func (cd *ConflictDetector) generateRecommendations(conflicts []FileConflict, strategy MergeStrategy) []string {
	var recommendations []string

	if len(conflicts) == 0 {
		return []string{"No conflicts detected - merge can proceed safely"}
	}

	autoResolvable := cd.countAutoResolvable(conflicts)
	if autoResolvable == len(conflicts) {
		recommendations = append(recommendations, "All conflicts can be automatically resolved")
	} else {
		recommendations = append(recommendations, fmt.Sprintf("%d of %d conflicts require manual resolution", len(conflicts)-autoResolvable, len(conflicts)))
	}

	// Strategy-specific recommendations
	switch strategy {
	case MergeStrategyRecursive:
		recommendations = append(recommendations, "Recursive strategy provides good conflict detection")
	case MergeStrategyOurs:
		recommendations = append(recommendations, "Using 'ours' strategy will favor your changes in conflicts")
	case MergeStrategyTheirs:
		recommendations = append(recommendations, "Using 'theirs' strategy will favor their changes in conflicts")
	}

	return recommendations
}
