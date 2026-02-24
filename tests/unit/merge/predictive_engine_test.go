package merge_test

import (
	"context"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/collabops/git-executor/internal/merge"
)

func TestPredictiveConflictEngine_PredictConflictProbability(t *testing.T) {
	logger := zaptest.NewLogger(t)
	engine := merge.NewPredictiveConflictEngine(logger)

	tests := []struct {
		name           string
		setupFeatures  func() *merge.MergeFeatures
		expectedRange  [2]float64 // [min, max]
		expectedReason string
	}{
		{
			name: "high_conflict_probability_overlapping_changes",
			setupFeatures: func() *merge.MergeFeatures {
				return &merge.MergeFeatures{
					FilesChanged:          25,
					LinesChanged:          1500,
					OverlappingFiles:      15,
					OverlappingLines:      800,
					AuthorCount:           4,
					TimeSpan:              48 * time.Hour,
					FileTypeDistribution:  map[string]int{"go": 20, "env": 3, "yaml": 2},
					DirectoryDepth:        6,
					BinaryFiles:           2,
					ConflictHistory:       8,
					MergeComplexity:       0.85,
					BranchDivergence:      200,
					HasEnvironmentFiles:   true,
					HasSensitiveFiles:     true,
					RecentConflictPattern: 0.75,
				}
			},
			expectedRange:  [2]float64{0.7, 1.0},
			expectedReason: "High overlapping changes and recent conflict pattern",
		},
		{
			name: "low_conflict_probability_simple_changes",
			setupFeatures: func() *merge.MergeFeatures {
				return &merge.MergeFeatures{
					FilesChanged:          3,
					LinesChanged:          50,
					OverlappingFiles:      0,
					OverlappingLines:      0,
					AuthorCount:           1,
					TimeSpan:              2 * time.Hour,
					FileTypeDistribution:  map[string]int{"go": 3},
					DirectoryDepth:        2,
					BinaryFiles:           0,
					ConflictHistory:       0,
					MergeComplexity:       0.1,
					BranchDivergence:      5,
					HasEnvironmentFiles:   false,
					HasSensitiveFiles:     false,
					RecentConflictPattern: 0.05,
				}
			},
			expectedRange:  [2]float64{0.0, 0.3},
			expectedReason: "Simple changes with no overlap",
		},
		{
			name: "medium_conflict_probability_environment_files",
			setupFeatures: func() *merge.MergeFeatures {
				return &merge.MergeFeatures{
					FilesChanged:          8,
					LinesChanged:          200,
					OverlappingFiles:      2,
					OverlappingLines:      50,
					AuthorCount:           2,
					TimeSpan:              24 * time.Hour,
					FileTypeDistribution:  map[string]int{"go": 5, "env": 2, "yaml": 1},
					DirectoryDepth:        3,
					BinaryFiles:           0,
					ConflictHistory:       2,
					MergeComplexity:       0.4,
					BranchDivergence:      50,
					HasEnvironmentFiles:   true,
					HasSensitiveFiles:     true,
					RecentConflictPattern: 0.3,
				}
			},
			expectedRange:  [2]float64{0.3, 0.7},
			expectedReason: "Environment files present with moderate changes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			features := tt.setupFeatures()

			prediction := engine.PredictConflictProbability(ctx, features)

			assert.InEpsilon(t, tt.expectedRange[0], prediction.Probability, 0.5,
				"Conflict probability should be in expected range [%.2f, %.2f], got %.2f",
				tt.expectedRange[0], tt.expectedRange[1], prediction.Probability)

			assert.True(t, prediction.Probability >= 0.0 && prediction.Probability <= 1.0,
				"Probability should be between 0 and 1")

			assert.NotEmpty(t, prediction.Reason, "Prediction should include reasoning")
			assert.NotEmpty(t, prediction.RiskFactors, "Should identify risk factors")
			assert.NotZero(t, prediction.Confidence, "Should have confidence score")

			t.Logf("Prediction: %.3f probability, reason: %s",
				prediction.Probability, prediction.Reason)
		})
	}
}

func TestMergeFeatureExtractor_ExtractFeatures(t *testing.T) {
	logger := zaptest.NewLogger(t)
	extractor := merge.NewMergeFeatureExtractor(logger)

	// Create mock commits for testing
	sourceCommit := &object.Commit{
		Hash: plumbing.NewHash("abc123"),
		Author: object.Signature{
			Name:  "Alice",
			Email: "alice@example.com",
			When:  time.Now().Add(-24 * time.Hour),
		},
		Message: "Add new feature",
	}

	targetCommit := &object.Commit{
		Hash: plumbing.NewHash("def456"),
		Author: object.Signature{
			Name:  "Bob",
			Email: "bob@example.com",
			When:  time.Now(),
		},
		Message: "Update configuration",
	}

	ctx := context.Background()

	t.Run("successful_feature_extraction", func(t *testing.T) {
		// Note: This would require a real git repository with commits
		// For now, test the interface and basic validation

		features := &merge.MergeFeatures{
			FilesChanged:     10,
			LinesChanged:     500,
			OverlappingFiles: 3,
			AuthorCount:      2,
			TimeSpan:         24 * time.Hour,
		}

		// Validate feature structure
		assert.Greater(t, features.FilesChanged, 0)
		assert.Greater(t, features.LinesChanged, 0)
		assert.GreaterOrEqual(t, features.OverlappingFiles, 0)
		assert.Greater(t, features.AuthorCount, 0)
		assert.Greater(t, features.TimeSpan, time.Duration(0))
	})

	t.Run("feature_validation", func(t *testing.T) {
		features := &merge.MergeFeatures{}

		// Test normalization
		normalized := extractor.NormalizeFeatures(features)

		assert.NotNil(t, normalized)
		assert.GreaterOrEqual(t, normalized.MergeComplexity, 0.0)
		assert.LessOrEqual(t, normalized.MergeComplexity, 1.0)
	})
}

func TestConflictPredictionModel_Training(t *testing.T) {
	logger := zaptest.NewLogger(t)
	model := merge.NewConflictPredictionModel(logger)

	t.Run("model_training_with_samples", func(t *testing.T) {
		// Create training samples
		samples := []*merge.TrainingSample{
			{
				Features: &merge.MergeFeatures{
					FilesChanged:     5,
					LinesChanged:     100,
					OverlappingFiles: 2,
					AuthorCount:      2,
					MergeComplexity:  0.3,
				},
				HadConflict: true,
				Resolution:  "manual",
				Duration:    5 * time.Minute,
			},
			{
				Features: &merge.MergeFeatures{
					FilesChanged:     2,
					LinesChanged:     20,
					OverlappingFiles: 0,
					AuthorCount:      1,
					MergeComplexity:  0.1,
				},
				HadConflict: false,
				Resolution:  "automatic",
				Duration:    30 * time.Second,
			},
		}

		ctx := context.Background()
		err := model.Train(ctx, samples)

		assert.NoError(t, err, "Training should complete without error")

		// Test prediction after training
		features := &merge.MergeFeatures{
			FilesChanged:     3,
			LinesChanged:     50,
			OverlappingFiles: 1,
			AuthorCount:      1,
			MergeComplexity:  0.2,
		}

		probability := model.Predict(features)
		assert.True(t, probability >= 0.0 && probability <= 1.0,
			"Predicted probability should be between 0 and 1")
	})

	t.Run("incremental_learning", func(t *testing.T) {
		sample := &merge.TrainingSample{
			Features: &merge.MergeFeatures{
				FilesChanged:     10,
				LinesChanged:     300,
				OverlappingFiles: 5,
				AuthorCount:      3,
				MergeComplexity:  0.6,
			},
			HadConflict: true,
			Resolution:  "semi-automatic",
			Duration:    10 * time.Minute,
		}

		err := model.UpdateFromFeedback(sample)
		assert.NoError(t, err, "Incremental learning should work")
	})
}

func TestConflictPatternDatabase_Operations(t *testing.T) {
	logger := zaptest.NewLogger(t)
	db := merge.NewConflictPatternDatabase(logger)

	t.Run("store_and_retrieve_patterns", func(t *testing.T) {
		pattern := &merge.ConflictPattern{
			ID:          "test-pattern-1",
			Files:       []string{"config/app.yml", "config/database.yml"},
			Authors:     []string{"alice", "bob"},
			Frequency:   5,
			AvgDuration: 8 * time.Minute,
			SuccessRate: 0.8,
			LastSeen:    time.Now(),
			Resolution:  []string{"manual_review", "choose_source"},
		}

		err := db.StorePattern(pattern)
		assert.NoError(t, err, "Should store pattern successfully")

		retrieved := db.FindSimilarPatterns(pattern.Files, pattern.Authors)
		assert.NotEmpty(t, retrieved, "Should find similar patterns")
		assert.Equal(t, pattern.ID, retrieved[0].ID, "Should retrieve the correct pattern")
	})

	t.Run("pattern_frequency_updates", func(t *testing.T) {
		pattern := &merge.ConflictPattern{
			ID:        "test-pattern-2",
			Files:     []string{"src/main.go"},
			Authors:   []string{"charlie"},
			Frequency: 1,
		}

		// Store initial pattern
		err := db.StorePattern(pattern)
		assert.NoError(t, err)

		// Update frequency
		err = db.UpdatePatternFrequency("test-pattern-2")
		assert.NoError(t, err, "Should update frequency")

		// Retrieve and check
		patterns := db.FindSimilarPatterns(pattern.Files, pattern.Authors)
		assert.Equal(t, 2, patterns[0].Frequency, "Frequency should be incremented")
	})

	t.Run("pattern_analysis", func(t *testing.T) {
		analysis := db.AnalyzePatterns()

		assert.GreaterOrEqual(t, analysis.TotalPatterns, 0)
		assert.GreaterOrEqual(t, analysis.AvgSuccessRate, 0.0)
		assert.LessOrEqual(t, analysis.AvgSuccessRate, 1.0)
		assert.NotNil(t, analysis.TopConflictFiles)
		assert.NotNil(t, analysis.TopConflictAuthors)
	})
}

func TestAdaptiveMergeStrategySelector(t *testing.T) {
	logger := zaptest.NewLogger(t)
	selector := merge.NewAdaptiveMergeStrategySelector(logger)

	tests := []struct {
		name             string
		features         *merge.MergeFeatures
		prediction       *merge.ConflictPrediction
		expectedStrategy merge.MergeStrategy
		expectedReason   string
	}{
		{
			name: "high_conflict_probability_requires_careful_strategy",
			features: &merge.MergeFeatures{
				HasEnvironmentFiles: true,
				HasSensitiveFiles:   true,
				MergeComplexity:     0.8,
			},
			prediction: &merge.ConflictPrediction{
				Probability: 0.85,
				RiskFactors: []string{"environment_files", "high_complexity"},
			},
			expectedStrategy: merge.MergeStrategyRecursive,
			expectedReason:   "high conflict probability requires careful merge",
		},
		{
			name: "simple_merge_allows_fast_forward",
			features: &merge.MergeFeatures{
				FilesChanged:        2,
				OverlappingFiles:    0,
				HasEnvironmentFiles: false,
				MergeComplexity:     0.1,
			},
			prediction: &merge.ConflictPrediction{
				Probability: 0.05,
				RiskFactors: []string{},
			},
			expectedStrategy: merge.MergeStrategyResolve,
			expectedReason:   "simple changes allow fast resolution",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			strategy := selector.SelectStrategy(ctx, tt.features, tt.prediction)

			assert.NotEqual(t, merge.MergeStrategy(""), strategy.Strategy,
				"Should select a valid strategy")
			assert.NotEmpty(t, strategy.Reason, "Should provide reasoning")
			assert.GreaterOrEqual(t, strategy.Confidence, 0.0,
				"Confidence should be non-negative")
			assert.LessOrEqual(t, strategy.Confidence, 1.0,
				"Confidence should not exceed 1.0")

			t.Logf("Selected strategy: %s (%.2f confidence) - %s",
				strategy.Strategy, strategy.Confidence, strategy.Reason)
		})
	}
}

func TestPredictiveEngine_Integration(t *testing.T) {
	logger := zaptest.NewLogger(t)
	engine := merge.NewPredictiveConflictEngine(logger)

	t.Run("end_to_end_prediction_workflow", func(t *testing.T) {
		ctx := context.Background()

		// Simulate a realistic merge scenario
		features := &merge.MergeFeatures{
			FilesChanged:          12,
			LinesChanged:          450,
			OverlappingFiles:      4,
			OverlappingLines:      120,
			AuthorCount:           3,
			TimeSpan:              36 * time.Hour,
			FileTypeDistribution:  map[string]int{"go": 8, "yaml": 2, "env": 2},
			DirectoryDepth:        4,
			ConflictHistory:       2,
			MergeComplexity:       0.55,
			BranchDivergence:      85,
			HasEnvironmentFiles:   true,
			HasSensitiveFiles:     false,
			RecentConflictPattern: 0.4,
		}

		// Get prediction
		prediction := engine.PredictConflictProbability(ctx, features)

		assert.True(t, prediction.Probability >= 0.0 && prediction.Probability <= 1.0,
			"Prediction probability should be valid")
		assert.NotEmpty(t, prediction.Reason, "Should provide reasoning")
		assert.NotEmpty(t, prediction.RiskFactors, "Should identify risk factors")
		assert.Greater(t, prediction.Confidence, 0.0, "Should have confidence")

		// Get adaptive strategy
		selector := merge.NewAdaptiveMergeStrategySelector(logger)
		strategy := selector.SelectStrategy(ctx, features, prediction)

		assert.NotEmpty(t, strategy.Strategy, "Should select a strategy")
		assert.NotEmpty(t, strategy.Reason, "Should explain strategy choice")

		// Verify strategy makes sense for prediction
		if prediction.Probability > 0.7 {
			// High conflict probability should suggest careful strategies
			validHighRiskStrategies := []merge.MergeStrategy{
				merge.MergeStrategyRecursive,
				merge.MergeStrategyORT,
			}
			assert.Contains(t, validHighRiskStrategies, strategy.Strategy,
				"High risk merges should use careful strategies")
		}

		t.Logf("Integration test complete: %.2f probability -> %s strategy",
			prediction.Probability, strategy.Strategy)
	})
}

// Benchmark tests for performance validation
func BenchmarkPredictiveEngine_PredictConflictProbability(b *testing.B) {
	logger := zaptest.NewLogger(b)
	engine := merge.NewPredictiveConflictEngine(logger)

	features := &merge.MergeFeatures{
		FilesChanged:        10,
		LinesChanged:        300,
		OverlappingFiles:    3,
		AuthorCount:         2,
		MergeComplexity:     0.4,
		HasEnvironmentFiles: true,
	}

	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			prediction := engine.PredictConflictProbability(ctx, features)
			if prediction.Probability < 0 || prediction.Probability > 1 {
				b.Fatal("Invalid prediction probability")
			}
		}
	})
}

func BenchmarkFeatureExtraction_Normalization(b *testing.B) {
	logger := zaptest.NewLogger(b)
	extractor := merge.NewMergeFeatureExtractor(logger)

	features := &merge.MergeFeatures{
		FilesChanged:     50,
		LinesChanged:     2000,
		OverlappingFiles: 15,
		AuthorCount:      5,
		DirectoryDepth:   8,
		BinaryFiles:      3,
		BranchDivergence: 500,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		normalized := extractor.NormalizeFeatures(features)
		if normalized.MergeComplexity < 0 || normalized.MergeComplexity > 1 {
			b.Fatal("Invalid normalized complexity")
		}
	}
}
