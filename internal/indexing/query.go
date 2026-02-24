package indexing

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/models"
	"gorm.io/gorm"
)

// QueryService provides high-performance commit history queries
// Replaces O(C×N×F) Git traversal with O(1) database lookups
// Target: 8,450ms → 200ms performance improvement (97% reduction)
type QueryService struct {
	db      *gorm.DB
	config  *QueryConfig
	metrics *QueryMetrics
}

// QueryConfig contains configuration for query operations
type QueryConfig struct {
	QueryTimeout         time.Duration `default:"5s"`
	MaxPathsPerQuery     int           `default:"1000"`
	EnableQueryProfiling bool          `default:"true"`
	CacheResults         bool          `default:"true"`
	CacheTTL             time.Duration `default:"5m"`
}

// QueryMetrics tracks query performance metrics
type QueryMetrics struct {
	TotalQueries     int64         `json:"total_queries"`
	AverageQueryTime time.Duration `json:"average_query_time"`
	CacheHitRate     float64       `json:"cache_hit_rate"`
	LastQueryTime    time.Time     `json:"last_query_time"`
	SlowestQueryTime time.Duration `json:"slowest_query_time"`
	IndexHitRate     float64       `json:"index_hit_rate"`
}

// CommitInfo represents commit information for a file
type CommitInfo struct {
	SHA         string    `json:"sha"`
	Message     string    `json:"message"`
	Author      string    `json:"author"`
	AuthorEmail string    `json:"author_email"`
	Time        time.Time `json:"time"`
	ChangeType  string    `json:"change_type"`
	FileSize    int64     `json:"file_size"`
}

// FileWithCommit represents a file with its latest commit information
type FileWithCommit struct {
	Path          string    `json:"path"`
	Size          int64     `json:"size"`
	LastCommitSHA string    `json:"last_commit_sha"`
	CommitTime    time.Time `json:"commit_time"`
	ChangeType    string    `json:"change_type"`
	Author        string    `json:"author,omitempty"`
	Message       string    `json:"message,omitempty"`
}

// DirectoryListing represents a directory with all files and their commit info
type DirectoryListing struct {
	Path         string           `json:"path"`
	Files        []FileWithCommit `json:"files"`
	TotalFiles   int              `json:"total_files"`
	LastModified time.Time        `json:"last_modified"`
	QueryTime    time.Duration    `json:"query_time"`
}

// NewQueryService creates a new QueryService instance
func NewQueryService(db *gorm.DB, config *QueryConfig) *QueryService {
	if config == nil {
		config = &QueryConfig{
			QueryTimeout:         5 * time.Second,
			MaxPathsPerQuery:     1000,
			EnableQueryProfiling: true,
			CacheResults:         true,
			CacheTTL:             5 * time.Minute,
		}
	}

	return &QueryService{
		db:      db,
		config:  config,
		metrics: &QueryMetrics{},
	}
}

// GetLatestCommitsForPaths provides O(1) lookup of latest commits for multiple paths
// This replaces the expensive O(C×N×F) Git log traversal
func (q *QueryService) GetLatestCommitsForPaths(ctx context.Context, repoID string, paths []string) (map[string]*CommitInfo, error) {
	startTime := time.Now()
	defer func() {
		queryTime := time.Since(startTime)
		q.updateMetrics(queryTime)
	}()

	if len(paths) == 0 {
		return make(map[string]*CommitInfo), nil
	}

	if len(paths) > q.config.MaxPathsPerQuery {
		return nil, fmt.Errorf("too many paths requested: %d (max: %d)", len(paths), q.config.MaxPathsPerQuery)
	}

	// Create context with timeout
	queryCtx, cancel := context.WithTimeout(ctx, q.config.QueryTimeout)
	defer cancel()

	// Query the index for all paths in a single database call
	var indices []models.CommitPathIndex
	err := q.db.WithContext(queryCtx).
		Where("repo_id = ? AND file_path IN ?", repoID, paths).
		Find(&indices).Error

	if err != nil {
		return nil, fmt.Errorf("failed to query commit index: %w", err)
	}

	// Convert results to map
	result := make(map[string]*CommitInfo)
	for _, index := range indices {
		// Skip deleted files unless specifically requested
		if index.ChangeType == "deleted" {
			continue
		}

		result[index.FilePath] = &CommitInfo{
			SHA:         index.LastCommitSHA,
			Time:        index.CommitTime,
			ChangeType:  index.ChangeType,
			FileSize:    index.FileSize,
			Author:      "", // Will be filled by commit detail lookup if needed
			AuthorEmail: "",
			Message:     "",
		}
	}

	return result, nil
}

// GetDirectoryWithCommits provides optimized directory listing with commit information
// Uses pattern matching for efficient subdirectory queries
func (q *QueryService) GetDirectoryWithCommits(ctx context.Context, repoID, path string) (*DirectoryListing, error) {
	startTime := time.Now()
	defer func() {
		queryTime := time.Since(startTime)
		q.updateMetrics(queryTime)
	}()

	// Normalize path
	normalizedPath := strings.Trim(path, "/")

	// Build pattern for directory children (direct children only, not recursive)
	var pattern string
	if normalizedPath == "" {
		// Root directory: match files not containing "/"
		pattern = "%"
	} else {
		// Specific directory: match files starting with path + "/"
		pattern = normalizedPath + "/%"
	}

	// Create context with timeout
	queryCtx, cancel := context.WithTimeout(ctx, q.config.QueryTimeout)
	defer cancel()

	// Query files in the directory
	var indices []models.CommitPathIndex
	query := q.db.WithContext(queryCtx).
		Where("repo_id = ? AND change_type != 'deleted'", repoID)

	if normalizedPath == "" {
		// Root directory: files that don't contain "/" (top-level files)
		// and directories that are one level deep
		query = query.Where("file_path NOT LIKE '%/%' OR (file_path LIKE '%/%' AND file_path NOT LIKE '%/%/%')")
	} else {
		// Specific directory: files starting with path + "/"
		query = query.Where("file_path LIKE ?", pattern).
			Where("file_path NOT LIKE ?", normalizedPath+"/%/%") // Exclude deeper subdirectories
	}

	err := query.Order("file_path ASC").Find(&indices).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query directory contents: %w", err)
	}

	// Convert to FileWithCommit slice
	files := make([]FileWithCommit, 0, len(indices))
	var lastModified time.Time

	for _, index := range indices {
		if index.CommitTime.After(lastModified) {
			lastModified = index.CommitTime
		}

		files = append(files, FileWithCommit{
			Path:          index.FilePath,
			Size:          index.FileSize,
			LastCommitSHA: index.LastCommitSHA,
			CommitTime:    index.CommitTime,
			ChangeType:    index.ChangeType,
		})
	}

	return &DirectoryListing{
		Path:         normalizedPath,
		Files:        files,
		TotalFiles:   len(files),
		LastModified: lastModified,
		QueryTime:    time.Since(startTime),
	}, nil
}

// GetRecentChanges returns recently changed files in a repository
func (q *QueryService) GetRecentChanges(ctx context.Context, repoID string, since time.Time, limit int) ([]FileWithCommit, error) {
	startTime := time.Now()
	defer func() {
		queryTime := time.Since(startTime)
		q.updateMetrics(queryTime)
	}()

	if limit <= 0 || limit > 1000 {
		limit = 100 // Default limit
	}

	queryCtx, cancel := context.WithTimeout(ctx, q.config.QueryTimeout)
	defer cancel()

	var indices []models.CommitPathIndex
	err := q.db.WithContext(queryCtx).
		Where("repo_id = ? AND commit_time > ? AND change_type != 'deleted'", repoID, since).
		Order("commit_time DESC").
		Limit(limit).
		Find(&indices).Error

	if err != nil {
		return nil, fmt.Errorf("failed to query recent changes: %w", err)
	}

	files := make([]FileWithCommit, len(indices))
	for i, index := range indices {
		files[i] = FileWithCommit{
			Path:          index.FilePath,
			Size:          index.FileSize,
			LastCommitSHA: index.LastCommitSHA,
			CommitTime:    index.CommitTime,
			ChangeType:    index.ChangeType,
		}
	}

	return files, nil
}

// GetFileHistory returns the commit history for a specific file
func (q *QueryService) GetFileHistory(ctx context.Context, repoID, filePath string, limit int) ([]CommitInfo, error) {
	startTime := time.Now()
	defer func() {
		queryTime := time.Since(startTime)
		q.updateMetrics(queryTime)
	}()

	if limit <= 0 || limit > 100 {
		limit = 20 // Default limit
	}

	queryCtx, cancel := context.WithTimeout(ctx, q.config.QueryTimeout)
	defer cancel()

	// For file history, we need to query all commits that touched this file
	// This would require extending the index to store multiple commits per file
	// For now, just return the latest commit
	var index models.CommitPathIndex
	err := q.db.WithContext(queryCtx).
		Where("repo_id = ? AND file_path = ?", repoID, filePath).
		First(&index).Error

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return []CommitInfo{}, nil
		}
		return nil, fmt.Errorf("failed to query file history: %w", err)
	}

	return []CommitInfo{
		{
			SHA:        index.LastCommitSHA,
			Time:       index.CommitTime,
			ChangeType: index.ChangeType,
			FileSize:   index.FileSize,
		},
	}, nil
}

// GetLargestFiles returns the largest files in a repository
func (q *QueryService) GetLargestFiles(ctx context.Context, repoID string, limit int) ([]FileWithCommit, error) {
	startTime := time.Now()
	defer func() {
		queryTime := time.Since(startTime)
		q.updateMetrics(queryTime)
	}()

	if limit <= 0 || limit > 100 {
		limit = 20
	}

	queryCtx, cancel := context.WithTimeout(ctx, q.config.QueryTimeout)
	defer cancel()

	var indices []models.CommitPathIndex
	err := q.db.WithContext(queryCtx).
		Where("repo_id = ? AND change_type != 'deleted' AND file_size > 0", repoID).
		Order("file_size DESC").
		Limit(limit).
		Find(&indices).Error

	if err != nil {
		return nil, fmt.Errorf("failed to query largest files: %w", err)
	}

	files := make([]FileWithCommit, len(indices))
	for i, index := range indices {
		files[i] = FileWithCommit{
			Path:          index.FilePath,
			Size:          index.FileSize,
			LastCommitSHA: index.LastCommitSHA,
			CommitTime:    index.CommitTime,
			ChangeType:    index.ChangeType,
		}
	}

	return files, nil
}

// GetRepositoryStats returns statistics about the repository
func (q *QueryService) GetRepositoryStats(ctx context.Context, repoID string) (*RepositoryStats, error) {
	startTime := time.Now()
	defer func() {
		queryTime := time.Since(startTime)
		q.updateMetrics(queryTime)
	}()

	queryCtx, cancel := context.WithTimeout(ctx, q.config.QueryTimeout)
	defer cancel()

	stats := &RepositoryStats{RepoID: repoID}

	// Count total files
	err := q.db.WithContext(queryCtx).
		Model(&models.CommitPathIndex{}).
		Where("repo_id = ? AND change_type != 'deleted'", repoID).
		Count(&stats.TotalFiles).Error
	if err != nil {
		return nil, fmt.Errorf("failed to count files: %w", err)
	}

	// Calculate total size
	err = q.db.WithContext(queryCtx).
		Model(&models.CommitPathIndex{}).
		Where("repo_id = ? AND change_type != 'deleted'", repoID).
		Select("COALESCE(SUM(file_size), 0)").
		Scan(&stats.TotalSize).Error
	if err != nil {
		return nil, fmt.Errorf("failed to calculate total size: %w", err)
	}

	// Get latest activity
	err = q.db.WithContext(queryCtx).
		Model(&models.CommitPathIndex{}).
		Where("repo_id = ?", repoID).
		Select("MAX(commit_time)").
		Scan(&stats.LastActivity).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get last activity: %w", err)
	}

	// Count by file types (simplified)
	var fileTypeCounts []FileTypeCount
	err = q.db.WithContext(queryCtx).
		Model(&models.CommitPathIndex{}).
		Where("repo_id = ? AND change_type != 'deleted'", repoID).
		Select("SUBSTRING(file_path FROM '\\.[^.]*$') as extension, COUNT(*) as count").
		Group("extension").
		Order("count DESC").
		Limit(10).
		Find(&fileTypeCounts).Error
	if err == nil {
		stats.FileTypes = fileTypeCounts
	}

	return stats, nil
}

// RepositoryStats represents statistics about a repository
type RepositoryStats struct {
	RepoID       string          `json:"repo_id"`
	TotalFiles   int64           `json:"total_files"`
	TotalSize    int64           `json:"total_size"`
	LastActivity time.Time       `json:"last_activity"`
	FileTypes    []FileTypeCount `json:"file_types"`
}

// FileTypeCount represents count of files by type
type FileTypeCount struct {
	Extension string `json:"extension"`
	Count     int64  `json:"count"`
}

// IsIndexAvailable checks if the index is available and up-to-date for a repository
func (q *QueryService) IsIndexAvailable(ctx context.Context, repoID string) (bool, error) {
	var count int64
	err := q.db.WithContext(ctx).
		Model(&models.CommitPathIndex{}).
		Where("repo_id = ?", repoID).
		Count(&count).Error

	return count > 0, err
}

// GetQueryMetrics returns performance metrics for the query service
func (q *QueryService) GetQueryMetrics() *QueryMetrics {
	return q.metrics
}

// updateMetrics updates internal query performance metrics
func (q *QueryService) updateMetrics(queryTime time.Duration) {
	q.metrics.TotalQueries++
	q.metrics.LastQueryTime = time.Now()

	// Update average query time (simple moving average)
	if q.metrics.AverageQueryTime == 0 {
		q.metrics.AverageQueryTime = queryTime
	} else {
		q.metrics.AverageQueryTime = (q.metrics.AverageQueryTime + queryTime) / 2
	}

	// Track slowest query
	if queryTime > q.metrics.SlowestQueryTime {
		q.metrics.SlowestQueryTime = queryTime
	}

	// Simple index hit rate calculation (assumes all queries hit the index)
	q.metrics.IndexHitRate = 1.0
}

// InvalidateCache invalidates cached results for a repository
// Called when the index is updated
func (q *QueryService) InvalidateCache(ctx context.Context, repoID string) error {
	// TODO: Implement cache invalidation when caching is added
	return nil
}

// WarmupCache pre-loads frequently accessed data into cache
func (q *QueryService) WarmupCache(ctx context.Context, repoID string, commonPaths []string) error {
	// Pre-load common paths to improve response times
	_, err := q.GetLatestCommitsForPaths(ctx, repoID, commonPaths)
	return err
}
