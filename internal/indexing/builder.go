package indexing

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/models"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IndexBuilder provides commit indexing functionality for performance optimization
// Converts O(C×N×F) Git traversal to O(1) database lookups
type IndexBuilder struct {
	db     *gorm.DB
	gitSvc GitService
	config *IndexConfig
}

// IndexConfig contains configuration for the index builder
type IndexConfig struct {
	BatchSize          int           `default:"100"`
	MaxConcurrency     int           `default:"4"`
	IndexTimeout       time.Duration `default:"30m"`
	IncrementalTimeout time.Duration `default:"5m"`
	MaxCommitsPerRun   int           `default:"10000"`
	EnableMetrics      bool          `default:"true"`
}

// GitService interface for Git repository operations
type GitService interface {
	GetAllCommits(repoPath string) ([]Commit, error)
	GetCommitsBySHAs(repoPath string, shas []string) (map[string]*Commit, error)
	OpenRepository(repoPath string) (*gogit.Repository, error)
}

// Commit represents a git commit with file changes
type Commit struct {
	SHA      string
	Message  string
	Author   CommitAuthor
	Time     time.Time
	Changes  []FileChange
	TreeHash string
}

// CommitAuthor represents commit author information
type CommitAuthor struct {
	Name  string
	Email string
	Date  time.Time
}

// FileChange represents a file change in a commit
type FileChange struct {
	Path string
	Type ChangeType
	Size int64
	Hash string
}

// ChangeType represents the type of change
type ChangeType string

const (
	ChangeAdded    ChangeType = "added"
	ChangeModified ChangeType = "modified"
	ChangeDeleted  ChangeType = "deleted"
	ChangeRenamed  ChangeType = "renamed"
)

// IndexingProgress tracks the progress of indexing operations
type IndexingProgress struct {
	RepoID           string    `json:"repo_id"`
	TotalCommits     int       `json:"total_commits"`
	ProcessedCommits int       `json:"processed_commits"`
	CurrentCommitSHA string    `json:"current_commit_sha"`
	StartedAt        time.Time `json:"started_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Status           string    `json:"status"` // running, completed, failed
	ErrorMessage     string    `json:"error_message,omitempty"`
}

// NewIndexBuilder creates a new IndexBuilder instance
func NewIndexBuilder(db *gorm.DB, gitSvc GitService, config *IndexConfig) *IndexBuilder {
	if config == nil {
		config = &IndexConfig{
			BatchSize:          100,
			MaxConcurrency:     4,
			IndexTimeout:       30 * time.Minute,
			IncrementalTimeout: 5 * time.Minute,
			MaxCommitsPerRun:   10000,
			EnableMetrics:      true,
		}
	}

	return &IndexBuilder{
		db:     db,
		gitSvc: gitSvc,
		config: config,
	}
}

// BuildFullIndex performs complete repository indexing
// This is used for initial setup or rebuilding corrupted indexes
func (b *IndexBuilder) BuildFullIndex(ctx context.Context, repoID, repoPath string) error {
	log.Printf("Starting full index build for repo: %s", repoID)

	startTime := time.Now()
	progress := &IndexingProgress{
		RepoID:    repoID,
		StartedAt: startTime,
		Status:    "running",
	}

	// Create context with timeout
	indexCtx, cancel := context.WithTimeout(ctx, b.config.IndexTimeout)
	defer cancel()

	// Open Git repository
	repo, err := b.gitSvc.OpenRepository(repoPath)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}

	// Get all commits from the repository
	commits, err := b.getAllCommitsFromRepo(indexCtx, repo)
	if err != nil {
		return fmt.Errorf("failed to get commits: %w", err)
	}

	progress.TotalCommits = len(commits)
	log.Printf("Found %d commits to process", len(commits))

	// Clear existing index for this repository
	if err := b.clearExistingIndex(repoID); err != nil {
		log.Printf("Warning: failed to clear existing index: %v", err)
	}

	// Process commits in batches
	batchSize := b.config.BatchSize
	for i := 0; i < len(commits); i += batchSize {
		select {
		case <-indexCtx.Done():
			return indexCtx.Err()
		default:
		}

		end := i + batchSize
		if end > len(commits) {
			end = len(commits)
		}

		batch := commits[i:end]
		if err := b.processBatch(indexCtx, repoID, batch); err != nil {
			return fmt.Errorf("failed to process batch %d-%d: %w", i, end-1, err)
		}

		progress.ProcessedCommits = end
		if end < len(commits) {
			progress.CurrentCommitSHA = commits[end-1].SHA
		}
		progress.UpdatedAt = time.Now()

		log.Printf("Processed commits %d-%d of %d", i+1, end, len(commits))
	}

	duration := time.Since(startTime)
	log.Printf("Completed full index build for repo: %s in %v", repoID, duration)

	progress.Status = "completed"
	progress.UpdatedAt = time.Now()

	return nil
}

// UpdateIndex performs incremental index updates for new commits
// This is called by Push hooks to keep the index up-to-date
func (b *IndexBuilder) UpdateIndex(ctx context.Context, repoID string, newCommits []Commit) error {
	if len(newCommits) == 0 {
		return nil
	}

	log.Printf("Starting incremental index update for repo: %s with %d commits", repoID, len(newCommits))

	// Create context with shorter timeout for incremental updates
	updateCtx, cancel := context.WithTimeout(ctx, b.config.IncrementalTimeout)
	defer cancel()

	// Process new commits
	err := b.processBatch(updateCtx, repoID, newCommits)
	if err != nil {
		return fmt.Errorf("failed to update index: %w", err)
	}

	log.Printf("Completed incremental index update for repo: %s", repoID)
	return nil
}

// UpdateIndexFromPush handles Push webhook updates
func (b *IndexBuilder) UpdateIndexFromPush(ctx context.Context, repoID, repoPath string, newCommitSHAs []string) error {
	if len(newCommitSHAs) == 0 {
		return nil
	}

	log.Printf("Processing push webhook for repo: %s with %d new commits", repoID, len(newCommitSHAs))

	// Open repository
	repo, err := b.gitSvc.OpenRepository(repoPath)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}

	// Get commit objects for the new SHAs
	var newCommits []Commit
	for _, sha := range newCommitSHAs {
		hash := plumbing.NewHash(sha)
		commit, err := repo.CommitObject(hash)
		if err != nil {
			log.Printf("Warning: failed to get commit %s: %v", sha, err)
			continue
		}

		convertedCommit, err := b.convertGoGitCommit(commit)
		if err != nil {
			log.Printf("Warning: failed to convert commit %s: %v", sha, err)
			continue
		}

		newCommits = append(newCommits, *convertedCommit)
	}

	return b.UpdateIndex(ctx, repoID, newCommits)
}

// processBatch processes a batch of commits and updates the index
func (b *IndexBuilder) processBatch(ctx context.Context, repoID string, commits []Commit) error {
	tx := b.db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	for _, commit := range commits {
		select {
		case <-ctx.Done():
			tx.Rollback()
			return ctx.Err()
		default:
		}

		for _, change := range commit.Changes {
			// Skip binary files and very large files
			if change.Size > 10*1024*1024 { // 10MB limit
				continue
			}

			index := &models.CommitPathIndex{
				RepoID:        repoID,
				FilePath:      change.Path,
				LastCommitSHA: commit.SHA,
				CommitTime:    commit.Time,
				ChangeType:    string(change.Type),
				FileSize:      change.Size,
				UpdatedAt:     time.Now(),
			}

			// Use upsert to handle conflicts
			result := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{
					{Name: "repo_id"},
					{Name: "file_path"},
				},
				DoUpdates: clause.AssignmentColumns([]string{
					"last_commit_sha",
					"commit_time",
					"change_type",
					"file_size",
					"updated_at",
				}),
			}).Create(index)

			if result.Error != nil {
				tx.Rollback()
				return fmt.Errorf("failed to upsert index entry: %w", result.Error)
			}
		}
	}

	return tx.Commit().Error
}

// clearExistingIndex removes all existing index entries for a repository
func (b *IndexBuilder) clearExistingIndex(repoID string) error {
	result := b.db.Where("repo_id = ?", repoID).Delete(&models.CommitPathIndex{})
	return result.Error
}

// getAllCommitsFromRepo retrieves all commits from a Git repository
func (b *IndexBuilder) getAllCommitsFromRepo(ctx context.Context, repo *gogit.Repository) ([]Commit, error) {
	// Get HEAD reference
	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD: %w", err)
	}

	// Create commit iterator
	iter, err := repo.Log(&gogit.LogOptions{From: head.Hash()})
	if err != nil {
		return nil, fmt.Errorf("failed to create log iterator: %w", err)
	}
	defer iter.Close()

	var commits []Commit
	commitCount := 0

	err = iter.ForEach(func(c *object.Commit) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Limit number of commits per run to prevent memory issues
		if commitCount >= b.config.MaxCommitsPerRun {
			log.Printf("Reached maximum commits per run limit: %d", b.config.MaxCommitsPerRun)
			return fmt.Errorf("max_commits_reached")
		}

		commit, err := b.convertGoGitCommit(c)
		if err != nil {
			log.Printf("Warning: failed to convert commit %s: %v", c.Hash.String(), err)
			return nil // Continue with next commit
		}

		commits = append(commits, *commit)
		commitCount++

		if commitCount%1000 == 0 {
			log.Printf("Processed %d commits", commitCount)
		}

		return nil
	})

	if err != nil && err.Error() != "max_commits_reached" {
		return nil, fmt.Errorf("failed to iterate commits: %w", err)
	}

	// Reverse commits to process oldest first (better for incremental indexing)
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}

	return commits, nil
}

// convertGoGitCommit converts a go-git commit to our internal format
func (b *IndexBuilder) convertGoGitCommit(c *object.Commit) (*Commit, error) {
	commit := &Commit{
		SHA:      c.Hash.String(),
		Message:  c.Message,
		TreeHash: c.TreeHash.String(),
		Author: CommitAuthor{
			Name:  c.Author.Name,
			Email: c.Author.Email,
			Date:  c.Author.When,
		},
		Time: c.Author.When,
	}

	// Get file changes for this commit
	changes, err := b.getCommitChanges(c)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit changes: %w", err)
	}

	commit.Changes = changes
	return commit, nil
}

// getCommitChanges extracts file changes from a commit
func (b *IndexBuilder) getCommitChanges(c *object.Commit) ([]FileChange, error) {
	var changes []FileChange

	// Get current tree
	tree, err := c.Tree()
	if err != nil {
		return nil, err
	}

	// If this is the first commit (no parents), all files are added
	if len(c.ParentHashes) == 0 {
		err = tree.Files().ForEach(func(f *object.File) error {
			changes = append(changes, FileChange{
				Path: f.Name,
				Type: ChangeAdded,
				Size: f.Size,
				Hash: f.Hash.String(),
			})
			return nil
		})
		return changes, err
	}

	// Compare with parent commit
	parent, err := c.Parent(0)
	if err != nil {
		return changes, nil // Skip if we can't get parent
	}

	parentTree, err := parent.Tree()
	if err != nil {
		return changes, nil
	}

	// Get diff between trees
	diff, err := object.DiffTree(parentTree, tree)
	if err != nil {
		return changes, nil
	}

	for _, change := range diff {
		var changeType ChangeType
		var path string
		var size int64

		// Determine change type based on From/To zero values
		// For insertions, From is zero value; for deletions, To is zero value
		if change.From.Name == "" && change.To.Name != "" {
			// File was added
			changeType = ChangeAdded
			path = change.To.Name
			if file, err := tree.File(path); err == nil {
				size = file.Size
			}
		} else if change.From.Name != "" && change.To.Name == "" {
			// File was deleted
			changeType = ChangeDeleted
			path = change.From.Name
		} else if change.From.Name != "" && change.To.Name != "" {
			// File was modified
			changeType = ChangeModified
			path = change.To.Name
			if file, err := tree.File(path); err == nil {
				size = file.Size
			}
		} else {
			continue // Skip unknown actions
		}

		// Skip very long paths (likely binary or generated files)
		if len(path) > 1000 {
			continue
		}

		// Skip common non-source paths
		if b.shouldSkipPath(path) {
			continue
		}

		changes = append(changes, FileChange{
			Path: path,
			Type: changeType,
			Size: size,
		})
	}

	return changes, nil
}

// shouldSkipPath determines if a file path should be skipped during indexing
func (b *IndexBuilder) shouldSkipPath(path string) bool {
	// Skip hidden files and directories
	if strings.HasPrefix(path, ".") && path != ".gitignore" && path != ".github" {
		return true
	}

	// Skip common build/dependency directories
	skipDirs := []string{
		"node_modules/",
		"vendor/",
		"target/",
		"build/",
		"dist/",
		"__pycache__/",
		".idea/",
		".vscode/",
		"coverage/",
		"tmp/",
		"temp/",
	}

	for _, skipDir := range skipDirs {
		if strings.HasPrefix(path, skipDir) || strings.Contains(path, "/"+skipDir) {
			return true
		}
	}

	// Skip common binary file extensions
	skipExtensions := []string{
		".exe", ".dll", ".so", ".dylib", ".jar", ".war",
		".zip", ".tar", ".gz", ".bz2", ".xz",
		".jpg", ".jpeg", ".png", ".gif", ".bmp", ".ico",
		".mp3", ".mp4", ".avi", ".mov", ".pdf",
		".o", ".a", ".lib", ".pdb",
	}

	for _, ext := range skipExtensions {
		if strings.HasSuffix(strings.ToLower(path), ext) {
			return true
		}
	}

	return false
}

// GetIndexStats returns statistics about the commit index
func (b *IndexBuilder) GetIndexStats(ctx context.Context, repoID string) (*models.CommitIndexStats, error) {
	var stats models.CommitIndexStats

	// Count total indexed files for this repo
	err := b.db.Model(&models.CommitPathIndex{}).
		Where("repo_id = ? AND change_type != 'deleted'", repoID).
		Count(&stats.TotalIndexedFiles).Error
	if err != nil {
		return nil, err
	}

	// Get latest index time
	err = b.db.Model(&models.CommitPathIndex{}).
		Where("repo_id = ?", repoID).
		Select("MAX(updated_at) as latest_index_time").
		Scan(&stats.LatestIndexTime).Error
	if err != nil {
		return nil, err
	}

	// Count indexed repositories
	err = b.db.Model(&models.CommitPathIndex{}).
		Distinct("repo_id").
		Count(&stats.IndexedRepos).Error
	if err != nil {
		return nil, err
	}

	return &stats, nil
}

// IsIndexUpToDate checks if the index is up-to-date for a repository
func (b *IndexBuilder) IsIndexUpToDate(ctx context.Context, repoID, repoPath string, latestCommitSHA string) (bool, error) {
	var count int64
	err := b.db.Model(&models.CommitPathIndex{}).
		Where("repo_id = ? AND last_commit_sha = ?", repoID, latestCommitSHA).
		Count(&count).Error

	return count > 0, err
}
