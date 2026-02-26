//go:build integration
// +build integration

package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/collabops/git-executor/internal/merge"
)

// TestPhase2IntelligenceIntegration tests the complete Phase 2 intelligence system
func TestPhase2IntelligenceIntegration(t *testing.T) {
	logger := zaptest.NewLogger(t)

	tests := []struct {
		name             string
		scenario         string
		setupRepo        func() (*git.Repository, *object.Commit, *object.Commit)
		expectedOutcome  string
		expectedStrategy merge.MergeStrategy
		minProbability   float64
		maxProbability   float64
	}{
		{
			name:     "high_risk_environment_file_merge",
			scenario: "Merging production environment configuration with multiple overlapping changes",
			setupRepo: func() (*git.Repository, *object.Commit, *object.Commit) {
				return createEnvironmentConflictScenario(t)
			},
			expectedOutcome:  "high_risk_detected",
			expectedStrategy: merge.MergeStrategyRecursive,
			minProbability:   0.6,
			maxProbability:   1.0,
		},
		{
			name:     "simple_code_change_merge",
			scenario: "Simple single-file code changes with no overlap",
			setupRepo: func() (*git.Repository, *object.Commit, *object.Commit) {
				return createSimpleCodeScenario(t)
			},
			expectedOutcome:  "low_risk_fast_track",
			expectedStrategy: merge.MergeStrategyResolve,
			minProbability:   0.0,
			maxProbability:   0.3,
		},
		{
			name:     "multi_author_complex_merge",
			scenario: "Multiple authors with overlapping changes in complex codebase",
			setupRepo: func() (*git.Repository, *object.Commit, *object.Commit) {
				return createComplexMultiAuthorScenario(t)
			},
			expectedOutcome:  "medium_risk_careful_merge",
			expectedStrategy: merge.MergeStrategyORT,
			minProbability:   0.3,
			maxProbability:   0.7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Setup test repository
			repo, sourceCommit, targetCommit := tt.setupRepo()

			// Initialize Phase 2 intelligence system
			predictiveEngine := merge.NewPredictiveConflictEngine(logger)
			validationPipeline := merge.NewValidationPipeline(logger)
			strategySelector := merge.NewAdaptiveMergeStrategySelector(logger)

			t.Logf("Testing scenario: %s", tt.scenario)

			// Step 1: Extract merge features
			extractor := merge.NewMergeFeatureExtractor(logger)
			features, err := extractor.ExtractFeatures(ctx, repo, sourceCommit, targetCommit)
			require.NoError(t, err, "Feature extraction should succeed")

			// Validate extracted features
			assert.Greater(t, features.FilesChanged, 0, "Should have changed files")
			assert.GreaterOrEqual(t, features.AuthorCount, 1, "Should have at least one author")
			assert.Greater(t, features.TimeSpan, time.Duration(0), "Should have time span")

			// Step 2: Predict conflict probability
			prediction := predictiveEngine.PredictConflictProbability(ctx, features)

			// Validate prediction
			assert.True(t, prediction.Probability >= tt.minProbability &&
				prediction.Probability <= tt.maxProbability,
				"Conflict probability should be in expected range [%.2f, %.2f], got %.2f",
				tt.minProbability, tt.maxProbability, prediction.Probability)

			assert.NotEmpty(t, prediction.Reason, "Prediction should include reasoning")
			assert.NotEmpty(t, prediction.RiskFactors, "Should identify risk factors")
			assert.Greater(t, prediction.Confidence, 0.0, "Should have confidence score")

			// Step 3: Run validation pipeline
			validationResult := validationPipeline.ValidateMerge(ctx, repo, sourceCommit, targetCommit)

			assert.NotNil(t, validationResult, "Validation should produce result")
			assert.Greater(t, validationResult.TotalChecks, 0, "Should run validation checks")

			// Step 4: Select adaptive merge strategy
			strategy := strategySelector.SelectStrategy(ctx, features, prediction)

			assert.Equal(t, tt.expectedStrategy, strategy.Strategy,
				"Should select expected merge strategy for scenario")
			assert.NotEmpty(t, strategy.Reason, "Should explain strategy choice")
			assert.Greater(t, strategy.Confidence, 0.0, "Should have strategy confidence")

			// Step 5: Test intelligence feedback loop
			if prediction.Probability > 0.5 {
				// Simulate conflict occurred - update learning system
				sample := &merge.TrainingSample{
					Features:    features,
					HadConflict: true,
					Resolution:  string(strategy.Strategy),
					Duration:    2 * time.Minute,
					Metadata: map[string]interface{}{
						"scenario":    tt.scenario,
						"test_run":    true,
						"prediction":  prediction.Probability,
						"actual_risk": tt.expectedOutcome,
					},
				}

				model := merge.NewConflictPredictionModel(logger)
				err := model.UpdateFromFeedback(sample)
				assert.NoError(t, err, "Learning system should accept feedback")
			}

			// Step 6: Verify end-to-end intelligence coordination
			t.Logf("Phase 2 Intelligence Results:")
			t.Logf("  Conflict Probability: %.3f (confidence: %.2f)",
				prediction.Probability, prediction.Confidence)
			t.Logf("  Selected Strategy: %s (confidence: %.2f)",
				strategy.Strategy, strategy.Confidence)
			t.Logf("  Validation Checks: %d passed, %d failed",
				validationResult.PassedChecks, validationResult.FailedChecks)
			t.Logf("  Environment Impact: %s risk assessment",
				validationResult.EnvironmentImpact.RiskAssessment)

			// Verify intelligence system coherence
			verifyIntelligenceCoherence(t, prediction, strategy, validationResult)
		})
	}
}

// TestPhase2PerformanceRequirements validates system meets performance targets
func TestPhase2PerformanceRequirements(t *testing.T) {
	logger := zaptest.NewLogger(t)

	t.Run("prediction_performance_requirements", func(t *testing.T) {
		engine := merge.NewPredictiveConflictEngine(logger)

		features := &merge.MergeFeatures{
			FilesChanged:        20,
			LinesChanged:        800,
			OverlappingFiles:    8,
			AuthorCount:         4,
			MergeComplexity:     0.6,
			HasEnvironmentFiles: true,
		}

		ctx := context.Background()
		start := time.Now()

		// Performance requirement: <100ms for conflict prediction
		prediction := engine.PredictConflictProbability(ctx, features)
		duration := time.Since(start)

		assert.Less(t, duration, 100*time.Millisecond,
			"Conflict prediction should complete within 100ms, took %v", duration)
		assert.True(t, prediction.Probability >= 0 && prediction.Probability <= 1,
			"Prediction should be valid")

		t.Logf("Prediction performance: %v", duration)
	})

	t.Run("validation_pipeline_performance", func(t *testing.T) {
		pipeline := merge.NewValidationPipeline(logger)
		repo, sourceCommit, targetCommit := createComplexMultiAuthorScenario(t)

		ctx := context.Background()
		start := time.Now()

		// Performance requirement: <5s for comprehensive validation
		result := pipeline.ValidateMerge(ctx, repo, sourceCommit, targetCommit)
		duration := time.Since(start)

		assert.Less(t, duration, 5*time.Second,
			"Validation pipeline should complete within 5s, took %v", duration)
		assert.NotNil(t, result, "Should produce validation result")

		t.Logf("Validation performance: %v", duration)
	})

	t.Run("concurrent_processing_capability", func(t *testing.T) {
		engine := merge.NewPredictiveConflictEngine(logger)

		// Test concurrent prediction processing
		concurrentRequests := 10
		results := make(chan *merge.ConflictPrediction, concurrentRequests)

		start := time.Now()

		for i := 0; i < concurrentRequests; i++ {
			go func(id int) {
				features := &merge.MergeFeatures{
					FilesChanged:     5 + id,
					LinesChanged:     100 * (id + 1),
					OverlappingFiles: id % 3,
					AuthorCount:      (id % 4) + 1,
					MergeComplexity:  float64(id) / 10.0,
				}

				ctx := context.Background()
				prediction := engine.PredictConflictProbability(ctx, features)
				results <- prediction
			}(i)
		}

		// Collect all results
		for i := 0; i < concurrentRequests; i++ {
			prediction := <-results
			assert.True(t, prediction.Probability >= 0 && prediction.Probability <= 1,
				"Concurrent prediction should be valid")
		}

		duration := time.Since(start)
		assert.Less(t, duration, 500*time.Millisecond,
			"Concurrent processing should complete within 500ms, took %v", duration)

		t.Logf("Concurrent processing performance (%d requests): %v",
			concurrentRequests, duration)
	})
}

// TestPhase2LearningAndAdaptation tests the machine learning feedback system
func TestPhase2LearningAndAdaptation(t *testing.T) {
	logger := zaptest.NewLogger(t)

	t.Run("model_learning_from_feedback", func(t *testing.T) {
		model := merge.NewConflictPredictionModel(logger)

		// Create training samples with clear patterns
		trainingSamples := []*merge.TrainingSample{
			{
				Features: &merge.MergeFeatures{
					FilesChanged:        5,
					LinesChanged:        100,
					OverlappingFiles:    3,
					AuthorCount:         2,
					MergeComplexity:     0.6,
					HasEnvironmentFiles: true,
				},
				HadConflict: true,
				Resolution:  "manual",
				Duration:    5 * time.Minute,
			},
			{
				Features: &merge.MergeFeatures{
					FilesChanged:        2,
					LinesChanged:        30,
					OverlappingFiles:    0,
					AuthorCount:         1,
					MergeComplexity:     0.1,
					HasEnvironmentFiles: false,
				},
				HadConflict: false,
				Resolution:  "automatic",
				Duration:    30 * time.Second,
			},
		}

		ctx := context.Background()
		err := model.Train(ctx, trainingSamples)
		assert.NoError(t, err, "Model training should succeed")

		// Test prediction accuracy after training
		highRiskFeatures := &merge.MergeFeatures{
			FilesChanged:        10,
			LinesChanged:        300,
			OverlappingFiles:    5,
			AuthorCount:         3,
			MergeComplexity:     0.7,
			HasEnvironmentFiles: true,
		}

		lowRiskFeatures := &merge.MergeFeatures{
			FilesChanged:        1,
			LinesChanged:        10,
			OverlappingFiles:    0,
			AuthorCount:         1,
			MergeComplexity:     0.05,
			HasEnvironmentFiles: false,
		}

		highRiskPrediction := model.Predict(highRiskFeatures)
		lowRiskPrediction := model.Predict(lowRiskFeatures)

		assert.Greater(t, highRiskPrediction, lowRiskPrediction,
			"Model should predict higher risk for complex merges")

		t.Logf("Learning validation - High risk: %.3f, Low risk: %.3f",
			highRiskPrediction, lowRiskPrediction)
	})

	t.Run("pattern_database_evolution", func(t *testing.T) {
		db := merge.NewConflictPatternDatabase(logger)

		// Simulate pattern accumulation over time
		patterns := []*merge.ConflictPattern{
			{
				ID:          "env-config-conflict",
				Files:       []string{"config/app.yml", "config/database.yml"},
				Authors:     []string{"alice", "bob"},
				Frequency:   8,
				AvgDuration: 10 * time.Minute,
				SuccessRate: 0.75,
				LastSeen:    time.Now(),
				Resolution:  []string{"manual_review", "choose_target"},
			},
			{
				ID:          "go-import-conflict",
				Files:       []string{"main.go", "imports.go"},
				Authors:     []string{"charlie", "dave"},
				Frequency:   3,
				AvgDuration: 2 * time.Minute,
				SuccessRate: 0.9,
				LastSeen:    time.Now().Add(-24 * time.Hour),
				Resolution:  []string{"automatic", "merge_imports"},
			},
		}

		for _, pattern := range patterns {
			err := db.StorePattern(pattern)
			assert.NoError(t, err, "Should store pattern successfully")
		}

		// Test pattern analysis and learning
		analysis := db.AnalyzePatterns()
		assert.GreaterOrEqual(t, analysis.TotalPatterns, 2, "Should have stored patterns")
		assert.NotEmpty(t, analysis.TopConflictFiles, "Should identify top conflict files")

		// Test pattern-based prediction enhancement
		similarPatterns := db.FindSimilarPatterns(
			[]string{"config/app.yml"},
			[]string{"alice"},
		)
		assert.NotEmpty(t, similarPatterns, "Should find similar patterns")
		assert.Equal(t, "env-config-conflict", similarPatterns[0].ID,
			"Should find the correct pattern")

		t.Logf("Pattern analysis: %d total patterns, %.2f avg success rate",
			analysis.TotalPatterns, analysis.AvgSuccessRate)
	})
}

// Helper functions for test scenarios

func createEnvironmentConflictScenario(t *testing.T) (*git.Repository, *object.Commit, *object.Commit) {
	repo, err := git.Init(memory.NewStorage(), nil)
	require.NoError(t, err)

	// Create commits with environment file conflicts
	sourceCommit := createTestCommit(t, repo, "source", map[string]string{
		"config/production.env": "DB_HOST=prod-db-1\nDB_PORT=5432\nDEBUG=false",
		"config/app.yml":        "production:\n  host: app-prod-1\n  ssl: true",
	})

	targetCommit := createTestCommit(t, repo, "target", map[string]string{
		"config/production.env": "DB_HOST=prod-db-2\nDB_PORT=5432\nDEBUG=false",
		"config/app.yml":        "production:\n  host: app-prod-2\n  ssl: true",
	})

	return repo, sourceCommit, targetCommit
}

func createSimpleCodeScenario(t *testing.T) (*git.Repository, *object.Commit, *object.Commit) {
	repo, err := git.Init(memory.NewStorage(), nil)
	require.NoError(t, err)

	sourceCommit := createTestCommit(t, repo, "source", map[string]string{
		"main.go": "package main\n\nfunc main() {\n\tprintln(\"Hello World\")\n}",
	})

	targetCommit := createTestCommit(t, repo, "target", map[string]string{
		"utils.go": "package main\n\nfunc helper() string {\n\treturn \"helper\"\n}",
	})

	return repo, sourceCommit, targetCommit
}

func createComplexMultiAuthorScenario(t *testing.T) (*git.Repository, *object.Commit, *object.Commit) {
	repo, err := git.Init(memory.NewStorage(), nil)
	require.NoError(t, err)

	sourceCommit := createTestCommit(t, repo, "alice", map[string]string{
		"src/main.go":    "package main\n\n// Alice's changes\nfunc main() {\n\tprocessData()\n}",
		"src/process.go": "package main\n\nfunc processData() {\n\t// Alice implementation\n}",
		"config/app.yml": "app:\n  version: 2.0\n  features: [auth, api]",
		"docs/README.md": "# Project\n\nAlice's documentation updates",
	})

	targetCommit := createTestCommit(t, repo, "bob", map[string]string{
		"src/main.go":    "package main\n\n// Bob's changes\nfunc main() {\n\tprocessData()\n\tlogResults()\n}",
		"src/logging.go": "package main\n\nfunc logResults() {\n\t// Bob implementation\n}",
		"config/app.yml": "app:\n  version: 2.1\n  features: [auth, api, logging]",
		"docs/API.md":    "# API Documentation\n\nBob's API docs",
	})

	return repo, sourceCommit, targetCommit
}

func createTestCommit(t *testing.T, repo *git.Repository, authorName string, files map[string]string) *object.Commit {
	w, err := repo.Worktree()
	require.NoError(t, err)

	// Create files
	for path, content := range files {
		dir := filepath.Dir(path)
		if dir != "." {
			err = os.MkdirAll(filepath.Join(w.Filesystem.Root(), dir), 0755)
			require.NoError(t, err)
		}

		file, err := w.Filesystem.Create(path)
		require.NoError(t, err)

		_, err = file.Write([]byte(content))
		require.NoError(t, err)
		file.Close()

		_, err = w.Add(path)
		require.NoError(t, err)
	}

	// Create commit
	commit, err := w.Commit("Test commit by "+authorName, &git.CommitOptions{
		Author: &object.Signature{
			Name:  authorName,
			Email: authorName + "@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	commitObj, err := repo.CommitObject(commit)
	require.NoError(t, err)

	return commitObj
}

func verifyIntelligenceCoherence(t *testing.T, prediction *merge.ConflictPrediction, strategy *merge.StrategySelection, validation *merge.PipelineResult) {
	// High-risk predictions should align with careful strategies
	if prediction.Probability > 0.7 {
		carefulStrategies := []merge.MergeStrategy{
			merge.MergeStrategyRecursive,
			merge.MergeStrategyORT,
		}
		assert.Contains(t, carefulStrategies, strategy.Strategy,
			"High-risk predictions should use careful strategies")
	}

	// Low-risk predictions can use faster strategies
	if prediction.Probability < 0.3 {
		fastStrategies := []merge.MergeStrategy{
			merge.MergeStrategyResolve,
			merge.MergeStrategyRecursive,
		}
		assert.Contains(t, fastStrategies, strategy.Strategy,
			"Low-risk predictions can use fast strategies")
	}

	// Validation failures should reduce strategy confidence
	if !validation.Valid {
		assert.Less(t, strategy.Confidence, 0.8,
			"Strategy confidence should be reduced when validation fails")
	}

	// Environment files should increase caution
	if validation.EnvironmentImpact.ManualReviewRequired {
		assert.Greater(t, prediction.Confidence, 0.6,
			"Environment file changes should increase prediction confidence")
	}
}
