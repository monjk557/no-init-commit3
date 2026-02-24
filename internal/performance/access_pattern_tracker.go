package performance

import (
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// AccessPatternTracker analyzes repository access patterns for intelligent caching
type AccessPatternTracker struct {
	logger *zap.Logger

	// Recent access history (circular buffer)
	recentAccesses []AccessRecord
	accessIndex    int
	windowSize     int
	mutex          sync.RWMutex

	// Repository statistics
	repoStats map[string]*RepositoryStats

	// Time-based patterns
	timePatterns map[string]*TimePattern // key: "hour:weekday"

	// Co-occurrence tracking
	coOccurrence map[string]map[string]int // repo -> related repo -> count
}

// AccessRecord represents a single repository access
type AccessRecord struct {
	Repository  string    `json:"repository"`
	AccessTime  time.Time `json:"access_time"`
	AccessType  string    `json:"access_type"`  // "read", "write", "clone", etc.
	UserContext string    `json:"user_context"` // optional user/session context
}

// RepositoryStats tracks statistics for a single repository
type RepositoryStats struct {
	Repository    string    `json:"repository"`
	TotalAccesses int64     `json:"total_accesses"`
	LastAccess    time.Time `json:"last_access"`
	FirstAccess   time.Time `json:"first_access"`
	AverageGap    float64   `json:"average_gap_minutes"` // Average time between accesses
	AccessPattern string    `json:"access_pattern"`      // "frequent", "sporadic", "burst", etc.
	RelatedRepos  []string  `json:"related_repos"`       // Frequently co-accessed repositories
}

// TimePattern represents time-based access patterns
type TimePattern struct {
	Hour       int       `json:"hour"`    // 0-23
	Weekday    int       `json:"weekday"` // 0-6 (Sunday=0)
	Repository string    `json:"repository"`
	Frequency  float64   `json:"frequency"` // 0.0-1.0
	LastSeen   time.Time `json:"last_seen"`
}

// RelatedRepository represents a repository that's often accessed together
type RelatedRepository struct {
	Repository string  `json:"repository"`
	Score      float64 `json:"score"` // Correlation score 0.0-1.0
	Count      int     `json:"count"` // Number of co-occurrences
}

// NewAccessPatternTracker creates a new access pattern tracker
func NewAccessPatternTracker(windowSize int, logger *zap.Logger) *AccessPatternTracker {
	return &AccessPatternTracker{
		logger:         logger,
		recentAccesses: make([]AccessRecord, windowSize),
		windowSize:     windowSize,
		repoStats:      make(map[string]*RepositoryStats),
		timePatterns:   make(map[string]*TimePattern),
		coOccurrence:   make(map[string]map[string]int),
	}
}

// RecordAccess records a new repository access
func (apt *AccessPatternTracker) RecordAccess(repository string) {
	apt.RecordAccessWithContext(repository, "read", "")
}

// RecordAccessWithContext records a repository access with additional context
func (apt *AccessPatternTracker) RecordAccessWithContext(repository, accessType, userContext string) {
	apt.mutex.Lock()
	defer apt.mutex.Unlock()

	now := time.Now()

	// Record in circular buffer
	access := AccessRecord{
		Repository:  repository,
		AccessTime:  now,
		AccessType:  accessType,
		UserContext: userContext,
	}

	apt.recentAccesses[apt.accessIndex] = access
	apt.accessIndex = (apt.accessIndex + 1) % apt.windowSize

	// Update repository statistics
	apt.updateRepositoryStats(repository, now)

	// Update time patterns
	apt.updateTimePatterns(repository, now)

	// Update co-occurrence patterns
	apt.updateCoOccurrence(repository, now)
}

// updateRepositoryStats updates statistics for a repository
func (apt *AccessPatternTracker) updateRepositoryStats(repository string, accessTime time.Time) {
	stats, exists := apt.repoStats[repository]
	if !exists {
		stats = &RepositoryStats{
			Repository:   repository,
			FirstAccess:  accessTime,
			RelatedRepos: make([]string, 0),
		}
		apt.repoStats[repository] = stats
	}

	stats.TotalAccesses++

	// Calculate average gap between accesses
	if stats.LastAccess.IsZero() {
		stats.LastAccess = accessTime
	} else {
		gap := accessTime.Sub(stats.LastAccess).Minutes()
		if stats.AverageGap == 0 {
			stats.AverageGap = gap
		} else {
			// Exponential moving average
			stats.AverageGap = stats.AverageGap*0.8 + gap*0.2
		}
		stats.LastAccess = accessTime
	}

	// Determine access pattern
	stats.AccessPattern = apt.determineAccessPattern(stats)
}

// determineAccessPattern analyzes access frequency to determine pattern type
func (apt *AccessPatternTracker) determineAccessPattern(stats *RepositoryStats) string {
	if stats.TotalAccesses >= 10 {
		if stats.AverageGap < 5 { // Less than 5 minutes between accesses
			return "burst"
		} else if stats.AverageGap < 60 { // Less than 1 hour
			return "frequent"
		} else if stats.AverageGap < 1440 { // Less than 24 hours
			return "regular"
		}
	}
	return "sporadic"
}

// updateTimePatterns tracks time-based access patterns
func (apt *AccessPatternTracker) updateTimePatterns(repository string, accessTime time.Time) {
	hour := accessTime.Hour()
	weekday := int(accessTime.Weekday())
	key := fmt.Sprintf("%d:%d", hour, weekday)

	pattern, exists := apt.timePatterns[key]
	if !exists {
		pattern = &TimePattern{
			Hour:       hour,
			Weekday:    weekday,
			Repository: repository,
			Frequency:  0.0,
		}
		apt.timePatterns[key] = pattern
	}

	// Update frequency using exponential moving average
	pattern.Frequency = pattern.Frequency*0.9 + 0.1
	pattern.LastSeen = accessTime
}

// updateCoOccurrence tracks which repositories are accessed together
func (apt *AccessPatternTracker) updateCoOccurrence(repository string, accessTime time.Time) {
	// Look for repositories accessed within the last 5 minutes
	timeWindow := 5 * time.Minute
	recentRepos := apt.getRepositoriesInTimeWindow(accessTime, timeWindow)

	// Initialize co-occurrence map for this repository if needed
	if apt.coOccurrence[repository] == nil {
		apt.coOccurrence[repository] = make(map[string]int)
	}

	// Update co-occurrence counts
	for _, relatedRepo := range recentRepos {
		if relatedRepo != repository {
			apt.coOccurrence[repository][relatedRepo]++

			// Also update reverse relationship
			if apt.coOccurrence[relatedRepo] == nil {
				apt.coOccurrence[relatedRepo] = make(map[string]int)
			}
			apt.coOccurrence[relatedRepo][repository]++
		}
	}
}

// getRepositoriesInTimeWindow returns repositories accessed within a time window
func (apt *AccessPatternTracker) getRepositoriesInTimeWindow(referenceTime time.Time, window time.Duration) []string {
	var repos []string
	repoMap := make(map[string]bool) // Deduplication

	for _, access := range apt.recentAccesses {
		if access.Repository != "" && access.AccessTime.After(referenceTime.Add(-window)) {
			if !repoMap[access.Repository] {
				repos = append(repos, access.Repository)
				repoMap[access.Repository] = true
			}
		}
	}

	return repos
}

// GetRecentAccesses returns the most recent repository accesses
func (apt *AccessPatternTracker) GetRecentAccesses(count int) []AccessRecord {
	apt.mutex.RLock()
	defer apt.mutex.RUnlock()

	var result []AccessRecord

	// Collect from circular buffer in chronological order
	for i := 0; i < apt.windowSize && len(result) < count; i++ {
		index := (apt.accessIndex - 1 - i + apt.windowSize) % apt.windowSize
		access := apt.recentAccesses[index]

		if access.Repository != "" { // Skip empty entries
			result = append(result, access)
		}
	}

	return result
}

// GetTopRepositories returns the most frequently accessed repositories
func (apt *AccessPatternTracker) GetTopRepositories(count int) []RepositoryStats {
	apt.mutex.RLock()
	defer apt.mutex.RUnlock()

	var repos []RepositoryStats
	for _, stats := range apt.repoStats {
		repos = append(repos, *stats) // Copy to avoid data races
	}

	// Sort by total accesses (descending)
	for i := 0; i < len(repos)-1; i++ {
		for j := i + 1; j < len(repos); j++ {
			if repos[j].TotalAccesses > repos[i].TotalAccesses {
				repos[i], repos[j] = repos[j], repos[i]
			}
		}
	}

	if len(repos) > count {
		repos = repos[:count]
	}

	return repos
}

// GetRelatedRepositories returns repositories often accessed with the given repository
func (apt *AccessPatternTracker) GetRelatedRepositories(repository string, count int) []RelatedRepository {
	apt.mutex.RLock()
	defer apt.mutex.RUnlock()

	relatedMap, exists := apt.coOccurrence[repository]
	if !exists {
		return []RelatedRepository{}
	}

	var related []RelatedRepository
	totalAccesses := apt.repoStats[repository].TotalAccesses

	for relatedRepo, coCount := range relatedMap {
		if _, exists := apt.repoStats[relatedRepo]; exists {
			// Calculate correlation score
			score := float64(coCount) / float64(totalAccesses)
			related = append(related, RelatedRepository{
				Repository: relatedRepo,
				Score:      score,
				Count:      coCount,
			})
		}
	}

	// Sort by score (descending)
	for i := 0; i < len(related)-1; i++ {
		for j := i + 1; j < len(related); j++ {
			if related[j].Score > related[i].Score {
				related[i], related[j] = related[j], related[i]
			}
		}
	}

	if len(related) > count {
		related = related[:count]
	}

	return related
}

// GetCoOccurrenceScore returns the co-occurrence score between two repositories
func (apt *AccessPatternTracker) GetCoOccurrenceScore(repo1, repo2 string) float64 {
	apt.mutex.RLock()
	defer apt.mutex.RUnlock()

	if relatedMap, exists := apt.coOccurrence[repo1]; exists {
		if count, exists := relatedMap[repo2]; exists {
			if stats, exists := apt.repoStats[repo1]; exists && stats.TotalAccesses > 0 {
				return float64(count) / float64(stats.TotalAccesses)
			}
		}
	}

	return 0.0
}

// GetTimeBasedPatterns returns repositories typically accessed at a specific time
func (apt *AccessPatternTracker) GetTimeBasedPatterns(hour int, weekday time.Weekday) []TimePattern {
	apt.mutex.RLock()
	defer apt.mutex.RUnlock()

	var patterns []TimePattern

	// Look for patterns matching the time window (±1 hour, same weekday)
	for h := hour - 1; h <= hour+1; h++ {
		key := fmt.Sprintf("%d:%d", h, int(weekday))
		if pattern, exists := apt.timePatterns[key]; exists && pattern.Frequency > 0.3 {
			patterns = append(patterns, *pattern)
		}
	}

	return patterns
}

// GetStatistics returns overall access pattern statistics
func (apt *AccessPatternTracker) GetStatistics() map[string]interface{} {
	apt.mutex.RLock()
	defer apt.mutex.RUnlock()

	stats := make(map[string]interface{})

	// Repository count
	stats["total_repositories"] = len(apt.repoStats)

	// Access patterns distribution
	patternCounts := make(map[string]int)
	for _, repoStat := range apt.repoStats {
		patternCounts[repoStat.AccessPattern]++
	}
	stats["access_patterns"] = patternCounts

	// Time pattern count
	stats["time_patterns"] = len(apt.timePatterns)

	// Co-occurrence relationships
	totalRelationships := 0
	for _, relatedMap := range apt.coOccurrence {
		totalRelationships += len(relatedMap)
	}
	stats["co_occurrence_relationships"] = totalRelationships

	return stats
}
