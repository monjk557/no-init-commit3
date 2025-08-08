package git

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repository"
)

// createConflictTestLogger creates a logger for testing
func createConflictTestLogger() *logging.Logger {
	cfg := config.LoggingConfig{
		Level:  "info",
		Format: "json",
	}
	logger, _ := logging.NewLogger(cfg)
	return logger
}

// createConflictTestPathConfig creates a path config for testing
func createConflictTestPathConfig() *repository.PathConfig {
	pathConfig, _ := repository.NewPathConfig("/Users/atci/Documents/GitHub/collabops-git-executor/test-repos")
	return pathConfig
}

func TestNewConflictDetector(t *testing.T) {
	pathConfig := createConflictTestPathConfig()
	logger := createConflictTestLogger()

	detector := NewConflictDetector(pathConfig, logger)

	assert.NotNil(t, detector)
	assert.Equal(t, pathConfig, detector.pathConfig)
	assert.Equal(t, logger, detector.logger)
}

func TestConflictType_Constants(t *testing.T) {
	tests := []struct {
		name     string
		value    ConflictType
		expected string
	}{
		{"Content", ConflictTypeContent, "content"},
		{"Rename", ConflictTypeRename, "rename"},
		{"Delete", ConflictTypeDelete, "delete"},
		{"Add", ConflictTypeAdd, "add"},
		{"Mode", ConflictTypeMode, "mode"},
		{"Submodule", ConflictTypeSubmodule, "submodule"},
		{"Symlink", ConflictTypeSymlink, "symlink"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestConflictSeverity_Constants(t *testing.T) {
	tests := []struct {
		name     string
		value    ConflictSeverity
		expected string
	}{
		{"Low", ConflictSeverityLow, "low"},
		{"Medium", ConflictSeverityMedium, "medium"},
		{"High", ConflictSeverityHigh, "high"},
		{"Critical", ConflictSeverityCritical, "critical"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestMergeStrategy_Constants(t *testing.T) {
	tests := []struct {
		name     string
		value    MergeStrategy
		expected string
	}{
		{"Recursive", MergeStrategyRecursive, "recursive"},
		{"Resolve", MergeStrategyResolve, "resolve"},
		{"Octopus", MergeStrategyOctopus, "octopus"},
		{"Ours", MergeStrategyOurs, "ours"},
		{"Theirs", MergeStrategyTheirs, "theirs"},
		{"Subtree", MergeStrategySubtree, "subtree"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestMergeAnalysisRequest_Structure(t *testing.T) {
	req := &MergeAnalysisRequest{
		Owner:          "testowner",
		Repo:           "testrepo",
		SourceRef:      "feature-branch",
		TargetRef:      "main",
		Strategy:       MergeStrategyRecursive,
		AutoResolve:    true,
		IncludeContent: true,
		MaxFileSize:    1024 * 1024, // 1MB
	}

	assert.Equal(t, "testowner", req.Owner)
	assert.Equal(t, "testrepo", req.Repo)
	assert.Equal(t, "feature-branch", req.SourceRef)
	assert.Equal(t, "main", req.TargetRef)
	assert.Equal(t, MergeStrategyRecursive, req.Strategy)
	assert.True(t, req.AutoResolve)
	assert.True(t, req.IncludeContent)
	assert.Equal(t, int64(1024*1024), req.MaxFileSize)
}

func TestMergeAnalysisResult_Structure(t *testing.T) {
	result := &MergeAnalysisResult{
		CanMerge:       true,
		HasConflicts:   false,
		IsFastForward:  true,
		ConflictCount:  0,
		FileConflicts:  []FileConflict{},
		Strategy:       MergeStrategyRecursive,
		AutoResolvable: 0,
		SeverityBreakdown: map[ConflictSeverity]int{
			ConflictSeverityLow: 0,
		},
		EstimatedTime:   0,
		Recommendations: []string{"Fast-forward merge"},
	}

	assert.True(t, result.CanMerge)
	assert.False(t, result.HasConflicts)
	assert.True(t, result.IsFastForward)
	assert.Equal(t, 0, result.ConflictCount)
	assert.Empty(t, result.FileConflicts)
	assert.Equal(t, MergeStrategyRecursive, result.Strategy)
	assert.Equal(t, 0, result.AutoResolvable)
	assert.Contains(t, result.SeverityBreakdown, ConflictSeverityLow)
	assert.Equal(t, time.Duration(0), result.EstimatedTime)
	assert.Contains(t, result.Recommendations, "Fast-forward merge")
}

func TestFileConflict_Structure(t *testing.T) {
	conflict := &FileConflict{
		FilePath:     "src/main.go",
		ConflictType: ConflictTypeContent,
		Severity:     ConflictSeverityMedium,
		OurVersion: &FileVersion{
			Exists:   true,
			Size:     1024,
			IsBinary: false,
		},
		TheirVersion: &FileVersion{
			Exists:   true,
			Size:     1536,
			IsBinary: false,
		},
		BaseVersion: &FileVersion{
			Exists:   true,
			Size:     512,
			IsBinary: false,
		},
		Regions: []ConflictRegion{
			{
				StartLine:    10,
				EndLine:      15,
				OurContent:   "our code",
				TheirContent: "their code",
				BaseContent:  "base code",
				MarkerStyle:  "standard",
			},
		},
		AutoResolvable: false,
		Metadata:       map[string]interface{}{"test": "value"},
	}

	assert.Equal(t, "src/main.go", conflict.FilePath)
	assert.Equal(t, ConflictTypeContent, conflict.ConflictType)
	assert.Equal(t, ConflictSeverityMedium, conflict.Severity)
	assert.NotNil(t, conflict.OurVersion)
	assert.NotNil(t, conflict.TheirVersion)
	assert.NotNil(t, conflict.BaseVersion)
	assert.Len(t, conflict.Regions, 1)
	assert.False(t, conflict.AutoResolvable)
	assert.Contains(t, conflict.Metadata, "test")
}

func TestFileVersion_Structure(t *testing.T) {
	version := &FileVersion{
		Mode:        0644,
		Size:        1024,
		Content:     "file content",
		ContentHash: "abc123",
		IsBinary:    false,
		Exists:      true,
	}

	assert.Equal(t, uint32(0644), version.Mode)
	assert.Equal(t, int64(1024), version.Size)
	assert.Equal(t, "file content", version.Content)
	assert.Equal(t, "abc123", version.ContentHash)
	assert.False(t, version.IsBinary)
	assert.True(t, version.Exists)
}

func TestConflictRegion_Structure(t *testing.T) {
	region := &ConflictRegion{
		StartLine:    10,
		EndLine:      20,
		OurContent:   "our changes",
		TheirContent: "their changes",
		BaseContent:  "original content",
		MarkerStyle:  "standard",
	}

	assert.Equal(t, 10, region.StartLine)
	assert.Equal(t, 20, region.EndLine)
	assert.Equal(t, "our changes", region.OurContent)
	assert.Equal(t, "their changes", region.TheirContent)
	assert.Equal(t, "original content", region.BaseContent)
	assert.Equal(t, "standard", region.MarkerStyle)
}

func TestConflictResolution_Structure(t *testing.T) {
	resolution := &ConflictResolution{
		Strategy:     "prefer_ours",
		Content:      "resolved content",
		Confidence:   0.8,
		AutoApplied:  false,
		Reasoning:    "Preferred our version due to better logic",
		Alternatives: []string{"prefer_theirs", "manual_merge"},
		Metadata:     map[string]interface{}{"source": "auto"},
	}

	assert.Equal(t, "prefer_ours", resolution.Strategy)
	assert.Equal(t, "resolved content", resolution.Content)
	assert.Equal(t, 0.8, resolution.Confidence)
	assert.False(t, resolution.AutoApplied)
	assert.Equal(t, "Preferred our version due to better logic", resolution.Reasoning)
	assert.Contains(t, resolution.Alternatives, "prefer_theirs")
	assert.Contains(t, resolution.Metadata, "source")
}

func TestCommitInfo_Structure(t *testing.T) {
	commit := &MergeCommitInfo{
		SHA:       "abc123def456",
		Message:   "Test commit",
		Author:    AuthorInfo{Name: "John Doe", Email: "john@example.com", Date: time.Now()},
		Committer: AuthorInfo{Name: "Jane Doe", Email: "jane@example.com", Date: time.Now()},
		CreatedAt: time.Now(),
		Parents:   []string{"parent1", "parent2"},
		TreeSHA:   "tree123",
		Metadata:  map[string]interface{}{"branch": "main"},
	}

	assert.Equal(t, "abc123def456", commit.SHA)
	assert.Equal(t, "Test commit", commit.Message)
	assert.Equal(t, "John Doe", commit.Author.Name)
	assert.Equal(t, "Jane Doe", commit.Committer.Name)
	assert.Len(t, commit.Parents, 2)
	assert.Equal(t, "tree123", commit.TreeSHA)
	assert.Contains(t, commit.Metadata, "branch")
}

func TestAuthorInfo_Structure(t *testing.T) {
	now := time.Now()
	author := &AuthorInfo{
		Name:  "John Doe",
		Email: "john@example.com",
		Date:  now,
	}

	assert.Equal(t, "John Doe", author.Name)
	assert.Equal(t, "john@example.com", author.Email)
	assert.Equal(t, now, author.Date)
}

func TestConflictDetector_DetermineConflictType(t *testing.T) {
	detector := NewConflictDetector(createConflictTestPathConfig(), createConflictTestLogger())

	tests := []struct {
		name         string
		baseExists   bool
		sourceExists bool
		targetExists bool
		expected     ConflictType
	}{
		{
			name:         "Content conflict",
			baseExists:   true,
			sourceExists: true,
			targetExists: true,
			expected:     ConflictTypeContent,
		},
		{
			name:         "Add conflict - both add",
			baseExists:   false,
			sourceExists: true,
			targetExists: true,
			expected:     ConflictTypeAdd,
		},
		{
			name:         "Delete conflict - source deleted",
			baseExists:   true,
			sourceExists: false,
			targetExists: true,
			expected:     ConflictTypeDelete,
		},
		{
			name:         "Delete conflict - target deleted",
			baseExists:   true,
			sourceExists: true,
			targetExists: false,
			expected:     ConflictTypeDelete,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detector.determineConflictType(tt.baseExists, tt.sourceExists, tt.targetExists, nil, nil, nil)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConflictDetector_CalculateConflictSeverity(t *testing.T) {
	detector := NewConflictDetector(createConflictTestPathConfig(), createConflictTestLogger())

	tests := []struct {
		name         string
		conflictType ConflictType
		expected     ConflictSeverity
	}{
		{
			name:         "Mode conflict - low severity",
			conflictType: ConflictTypeMode,
			expected:     ConflictSeverityLow,
		},
		{
			name:         "Add conflict - medium severity",
			conflictType: ConflictTypeAdd,
			expected:     ConflictSeverityMedium,
		},
		{
			name:         "Delete conflict - high severity",
			conflictType: ConflictTypeDelete,
			expected:     ConflictSeverityHigh,
		},
		{
			name:         "Content conflict - medium severity",
			conflictType: ConflictTypeContent,
			expected:     ConflictSeverityMedium,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sourceVersion := &FileVersion{IsBinary: false}
			targetVersion := &FileVersion{IsBinary: false}
			result := detector.calculateConflictSeverity(tt.conflictType, sourceVersion, targetVersion)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConflictDetector_IsAutoResolvable(t *testing.T) {
	detector := NewConflictDetector(createConflictTestPathConfig(), createConflictTestLogger())

	tests := []struct {
		name     string
		conflict *FileConflict
		expected bool
	}{
		{
			name: "Mode conflict - auto resolvable",
			conflict: &FileConflict{
				ConflictType: ConflictTypeMode,
				Regions:      []ConflictRegion{},
			},
			expected: true,
		},
		{
			name: "Add conflict - not auto resolvable",
			conflict: &FileConflict{
				ConflictType: ConflictTypeAdd,
				Regions:      []ConflictRegion{},
			},
			expected: false,
		},
		{
			name: "Delete conflict - not auto resolvable",
			conflict: &FileConflict{
				ConflictType: ConflictTypeDelete,
				Regions:      []ConflictRegion{},
			},
			expected: false,
		},
		{
			name: "Content conflict with no regions - auto resolvable",
			conflict: &FileConflict{
				ConflictType: ConflictTypeContent,
				Regions:      []ConflictRegion{},
				OurVersion:   &FileVersion{IsBinary: false},
				TheirVersion: &FileVersion{IsBinary: false},
			},
			expected: true,
		},
		{
			name: "Content conflict with binary - not auto resolvable",
			conflict: &FileConflict{
				ConflictType: ConflictTypeContent,
				Regions:      []ConflictRegion{},
				OurVersion:   &FileVersion{IsBinary: true},
				TheirVersion: &FileVersion{IsBinary: false},
			},
			expected: true, // Still considers the no-regions case
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detector.isAutoResolvable(tt.conflict)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConflictDetector_IsBinaryContent(t *testing.T) {
	detector := NewConflictDetector(createConflictTestPathConfig(), createConflictTestLogger())

	tests := []struct {
		name     string
		data     []byte
		expected bool
	}{
		{
			name:     "Text content",
			data:     []byte("Hello, world!"),
			expected: false,
		},
		{
			name:     "Binary content with null byte",
			data:     []byte{0x48, 0x65, 0x6c, 0x6c, 0x6f, 0x00, 0x77, 0x6f, 0x72, 0x6c, 0x64},
			expected: true,
		},
		{
			name:     "Empty content",
			data:     []byte{},
			expected: false,
		},
		{
			name:     "UTF-8 content",
			data:     []byte("Hello, 世界!"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detector.isBinaryContent(tt.data)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConflictDetector_CountAutoResolvable(t *testing.T) {
	detector := NewConflictDetector(createConflictTestPathConfig(), createConflictTestLogger())

	conflicts := []FileConflict{
		{ConflictType: ConflictTypeMode, AutoResolvable: true},
		{ConflictType: ConflictTypeAdd, AutoResolvable: false},
		{ConflictType: ConflictTypeContent, AutoResolvable: true},
		{ConflictType: ConflictTypeDelete, AutoResolvable: false},
	}

	result := detector.countAutoResolvable(conflicts)
	assert.Equal(t, 2, result)
}

func TestConflictDetector_AllAutoResolvable(t *testing.T) {
	detector := NewConflictDetector(createConflictTestPathConfig(), createConflictTestLogger())

	tests := []struct {
		name      string
		conflicts []FileConflict
		expected  bool
	}{
		{
			name: "All auto resolvable",
			conflicts: []FileConflict{
				{AutoResolvable: true},
				{AutoResolvable: true},
			},
			expected: true,
		},
		{
			name: "Some not auto resolvable",
			conflicts: []FileConflict{
				{AutoResolvable: true},
				{AutoResolvable: false},
			},
			expected: false,
		},
		{
			name:      "Empty conflicts",
			conflicts: []FileConflict{},
			expected:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detector.allAutoResolvable(tt.conflicts)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConflictDetector_CalculateSeverityBreakdown(t *testing.T) {
	detector := NewConflictDetector(createConflictTestPathConfig(), createConflictTestLogger())

	conflicts := []FileConflict{
		{Severity: ConflictSeverityLow},
		{Severity: ConflictSeverityLow},
		{Severity: ConflictSeverityMedium},
		{Severity: ConflictSeverityHigh},
		{Severity: ConflictSeverityHigh},
		{Severity: ConflictSeverityCritical},
	}

	result := detector.calculateSeverityBreakdown(conflicts)

	assert.Equal(t, 2, result[ConflictSeverityLow])
	assert.Equal(t, 1, result[ConflictSeverityMedium])
	assert.Equal(t, 2, result[ConflictSeverityHigh])
	assert.Equal(t, 1, result[ConflictSeverityCritical])
}

func TestConflictDetector_EstimateResolutionTime(t *testing.T) {
	detector := NewConflictDetector(createConflictTestPathConfig(), createConflictTestLogger())

	conflicts := []FileConflict{
		{Severity: ConflictSeverityLow},      // 2 minutes
		{Severity: ConflictSeverityMedium},   // 10 minutes
		{Severity: ConflictSeverityHigh},     // 30 minutes
		{Severity: ConflictSeverityCritical}, // 60 minutes
	}

	result := detector.estimateResolutionTime(conflicts)
	expected := time.Duration(2+10+30+60) * time.Minute // 102 minutes

	assert.Equal(t, expected, result)
}

func TestConflictDetector_GenerateRecommendations(t *testing.T) {
	detector := NewConflictDetector(createConflictTestPathConfig(), createConflictTestLogger())

	tests := []struct {
		name      string
		conflicts []FileConflict
		strategy  MergeStrategy
		expected  []string
	}{
		{
			name:      "No conflicts",
			conflicts: []FileConflict{},
			strategy:  MergeStrategyRecursive,
			expected:  []string{"No conflicts detected - merge can proceed safely"},
		},
		{
			name: "All auto resolvable",
			conflicts: []FileConflict{
				{AutoResolvable: true},
				{AutoResolvable: true},
			},
			strategy: MergeStrategyRecursive,
			expected: []string{
				"All conflicts can be automatically resolved",
				"Recursive strategy provides good conflict detection",
			},
		},
		{
			name: "Some manual resolution required",
			conflicts: []FileConflict{
				{AutoResolvable: true},
				{AutoResolvable: false},
			},
			strategy: MergeStrategyOurs,
			expected: []string{
				"1 of 2 conflicts require manual resolution",
				"Using 'ours' strategy will favor your changes in conflicts",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detector.generateRecommendations(tt.conflicts, tt.strategy)
			for _, expected := range tt.expected {
				assert.Contains(t, result, expected)
			}
		})
	}
}

// Integration test for repository access (simplified)
func TestConflictDetector_AnalyzeMerge_InvalidRepository(t *testing.T) {
	detector := NewConflictDetector(createConflictTestPathConfig(), createConflictTestLogger())

	req := &MergeAnalysisRequest{
		Owner:     "nonexistent",
		Repo:      "nonexistent",
		SourceRef: "main",
		TargetRef: "feature",
		Strategy:  MergeStrategyRecursive,
	}

	ctx := context.Background()
	result, err := detector.AnalyzeMerge(ctx, req)

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed to open repository")
}
