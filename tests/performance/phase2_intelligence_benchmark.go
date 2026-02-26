package performance

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	"github.com/collabops/git-executor/internal/merge"
)

// Phase2IntelligenceBenchmark provides comprehensive performance testing for Phase 2 features
type Phase2IntelligenceBenchmark struct {
	logger             *zap.Logger
	predictiveEngine   *merge.PredictiveConflictEngine
	validationPipeline *merge.ValidationPipeline
	strategySelector   *merge.AdaptiveMergeStrategySelector
	patternDatabase    *merge.ConflictPatternDatabase
}

// NewPhase2IntelligenceBenchmark creates a new benchmark suite
func NewPhase2IntelligenceBenchmark(logger *zap.Logger) *Phase2IntelligenceBenchmark {
	return &Phase2IntelligenceBenchmark{
		logger:             logger,
		predictiveEngine:   merge.NewPredictiveConflictEngine(logger),
		validationPipeline: merge.NewValidationPipeline(logger),
		strategySelector:   merge.NewAdaptiveMergeStrategySelector(logger),
		patternDatabase:    merge.NewConflictPatternDatabase(logger),
	}
}

// BenchmarkConflictPrediction tests conflict prediction performance under various loads
func BenchmarkConflictPrediction(b *testing.B) {
	logger := zaptest.NewLogger(b)
	benchmark := NewPhase2IntelligenceBenchmark(logger)

	scenarios := []struct {
		name     string
		features *merge.MergeFeatures
	}{
		{
			name: "simple_merge",
			features: &merge.MergeFeatures{
				FilesChanged:     3,
				LinesChanged:     50,
				OverlappingFiles: 0,
				AuthorCount:      1,
				MergeComplexity:  0.1,
			},
		},
		{
			name: "medium_complexity",
			features: &merge.MergeFeatures{
				FilesChanged:        15,
				LinesChanged:        500,
				OverlappingFiles:    5,
				AuthorCount:         3,
				MergeComplexity:     0.5,
				HasEnvironmentFiles: true,
			},
		},
		{
			name: "high_complexity",
			features: &merge.MergeFeatures{
				FilesChanged:          50,
				LinesChanged:          2000,
				OverlappingFiles:      25,
				AuthorCount:           8,
				MergeComplexity:       0.9,
				HasEnvironmentFiles:   true,
				HasSensitiveFiles:     true,
				RecentConflictPattern: 0.8,
			},
		},
	}

	for _, scenario := range scenarios {
		b.Run(scenario.name, func(b *testing.B) {
			ctx := context.Background()

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				prediction := benchmark.predictiveEngine.PredictConflictProbability(ctx, scenario.features)
				if prediction.Probability < 0 || prediction.Probability > 1 {
					b.Fatal("Invalid prediction probability")
				}
			}
		})
	}
}

// BenchmarkValidationPipeline tests validation pipeline performance
func BenchmarkValidationPipeline(b *testing.B) {
	logger := zaptest.NewLogger(b)
	benchmark := NewPhase2IntelligenceBenchmark(logger)

	// Create test repository scenarios
	scenarios := []struct {
		name      string
		fileCount int
		envFiles  int
	}{
		{"small_repo", 10, 2},
		{"medium_repo", 50, 5},
		{"large_repo", 200, 15},
	}

	for _, scenario := range scenarios {
		b.Run(scenario.name, func(b *testing.B) {
			repo, sourceCommit, targetCommit := createBenchmarkRepo(b, scenario.fileCount, scenario.envFiles)
			ctx := context.Background()

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				result := benchmark.validationPipeline.ValidateMerge(ctx, repo, sourceCommit, targetCommit)
				if result == nil {
					b.Fatal("Validation should produce result")
				}
			}
		})
	}
}

// BenchmarkStrategySelection tests adaptive strategy selection performance
func BenchmarkStrategySelection(b *testing.B) {
	logger := zaptest.NewLogger(b)
	benchmark := NewPhase2IntelligenceBenchmark(logger)

	ctx := context.Background()
	features := &merge.MergeFeatures{
		FilesChanged:     20,
		LinesChanged:     800,
		OverlappingFiles: 8,
		AuthorCount:      4,
		MergeComplexity:  0.6,
	}

	prediction := &merge.ConflictPrediction{
		Probability: 0.65,
		RiskFactors: []string{"overlapping_changes", "multiple_authors"},
		Confidence:  0.8,
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		strategy := benchmark.strategySelector.SelectStrategy(ctx, features, prediction)
		if strategy.Strategy == "" {
			b.Fatal("Strategy selection should produce valid strategy")
		}
	}
}

// BenchmarkPatternDatabase tests pattern storage and retrieval performance
func BenchmarkPatternDatabase(b *testing.B) {
	logger := zaptest.NewLogger(b)
	benchmark := NewPhase2IntelligenceBenchmark(logger)

	// Pre-populate database with patterns
	for i := 0; i < 1000; i++ {
		pattern := &merge.ConflictPattern{
			ID:        fmt.Sprintf("pattern-%d", i),
			Files:     []string{fmt.Sprintf("file-%d.go", i), fmt.Sprintf("file-%d.yml", i)},
			Authors:   []string{fmt.Sprintf("author-%d", i%10)},
			Frequency: i % 20,
			LastSeen:  time.Now(),
		}
		benchmark.patternDatabase.StorePattern(pattern)
	}

	b.Run("store_pattern", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			pattern := &merge.ConflictPattern{
				ID:        fmt.Sprintf("bench-pattern-%d", i),
				Files:     []string{fmt.Sprintf("bench-file-%d.go", i)},
				Authors:   []string{fmt.Sprintf("bench-author-%d", i%5)},
				Frequency: 1,
			}
			benchmark.patternDatabase.StorePattern(pattern)
		}
	})

	b.Run("find_similar_patterns", func(b *testing.B) {
		files := []string{"file-100.go", "file-100.yml"}
		authors := []string{"author-5"}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			patterns := benchmark.patternDatabase.FindSimilarPatterns(files, authors)
			if len(patterns) == 0 {
				// This is expected for some queries, but we want to measure performance
			}
		}
	})

	b.Run("analyze_patterns", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			analysis := benchmark.patternDatabase.AnalyzePatterns()
			if analysis.TotalPatterns == 0 {
				b.Fatal("Analysis should find patterns")
			}
		}
	})
}

// BenchmarkConcurrentIntelligence tests system performance under concurrent load
func BenchmarkConcurrentIntelligence(b *testing.B) {
	logger := zaptest.NewLogger(b)
	benchmark := NewPhase2IntelligenceBenchmark(logger)

	concurrency := []int{1, 5, 10, 25, 50}

	for _, workers := range concurrency {
		b.Run(fmt.Sprintf("workers_%d", workers), func(b *testing.B) {
			ctx := context.Background()

			features := &merge.MergeFeatures{
				FilesChanged:     10,
				LinesChanged:     300,
				OverlappingFiles: 3,
				AuthorCount:      2,
				MergeComplexity:  0.4,
			}

			b.ResetTimer()
			b.ReportAllocs()

			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					prediction := benchmark.predictiveEngine.PredictConflictProbability(ctx, features)
					strategy := benchmark.strategySelector.SelectStrategy(ctx, features, prediction)

					if prediction.Probability < 0 || prediction.Probability > 1 {
						b.Fatal("Invalid prediction")
					}
					if strategy.Strategy == "" {
						b.Fatal("Invalid strategy")
					}
				}
			})
		})
	}
}

// BenchmarkMemoryUsage tests memory efficiency of intelligence components
func BenchmarkMemoryUsage(b *testing.B) {
	logger := zaptest.NewLogger(b)

	b.Run("predictive_engine_memory", func(b *testing.B) {
		var engines []*merge.PredictiveConflictEngine

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			engine := merge.NewPredictiveConflictEngine(logger)
			engines = append(engines, engine)

			// Prevent garbage collection
			runtime.KeepAlive(engines)
		}
	})

	b.Run("validation_pipeline_memory", func(b *testing.B) {
		var pipelines []*merge.ValidationPipeline

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			pipeline := merge.NewValidationPipeline(logger)
			pipelines = append(pipelines, pipeline)

			runtime.KeepAlive(pipelines)
		}
	})

	b.Run("pattern_database_growth", func(b *testing.B) {
		db := merge.NewConflictPatternDatabase(logger)

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			pattern := &merge.ConflictPattern{
				ID:      fmt.Sprintf("mem-test-%d", i),
				Files:   []string{fmt.Sprintf("file-%d", i)},
				Authors: []string{fmt.Sprintf("author-%d", i)},
			}
			db.StorePattern(pattern)
		}

		analysis := db.AnalyzePatterns()
		if analysis.TotalPatterns != b.N {
			b.Fatalf("Expected %d patterns, got %d", b.N, analysis.TotalPatterns)
		}
	})
}

// BenchmarkMachineLearningOperations tests ML-specific performance
func BenchmarkMachineLearningOperations(b *testing.B) {
	logger := zaptest.NewLogger(b)
	model := merge.NewConflictPredictionModel(logger)

	// Pre-train model with sample data
	samples := generateTrainingSamples(100)
	ctx := context.Background()
	err := model.Train(ctx, samples)
	require.NoError(b, err)

	b.Run("model_prediction", func(b *testing.B) {
		features := &merge.MergeFeatures{
			FilesChanged:     15,
			LinesChanged:     500,
			OverlappingFiles: 5,
			AuthorCount:      3,
			MergeComplexity:  0.6,
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			probability := model.Predict(features)
			if probability < 0 || probability > 1 {
				b.Fatal("Invalid prediction probability")
			}
		}
	})

	b.Run("model_feedback_update", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sample := &merge.TrainingSample{
				Features: &merge.MergeFeatures{
					FilesChanged:     5 + (i % 10),
					LinesChanged:     100 * (i%5 + 1),
					OverlappingFiles: i % 3,
					AuthorCount:      (i % 4) + 1,
					MergeComplexity:  float64(i%10) / 10.0,
				},
				HadConflict: i%2 == 0,
				Resolution:  "automatic",
				Duration:    time.Minute,
			}

			err := model.UpdateFromFeedback(sample)
			if err != nil {
				b.Fatal("Feedback update failed:", err)
			}
		}
	})

	b.Run("batch_training", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			batchSamples := generateTrainingSamples(50)
			err := model.Train(ctx, batchSamples)
			if err != nil {
				b.Fatal("Batch training failed:", err)
			}
		}
	})
}

// BenchmarkEndToEndIntelligence tests complete intelligence workflow performance
func BenchmarkEndToEndIntelligence(b *testing.B) {
	logger := zaptest.NewLogger(b)
	benchmark := NewPhase2IntelligenceBenchmark(logger)

	// Create realistic test scenario
	repo, sourceCommit, targetCommit := createBenchmarkRepo(b, 25, 3)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		ctx := context.Background()

		// Complete intelligence workflow
		extractor := merge.NewMergeFeatureExtractor(logger)
		features, err := extractor.ExtractFeatures(ctx, repo, sourceCommit, targetCommit)
		if err != nil {
			b.Fatal("Feature extraction failed:", err)
		}

		prediction := benchmark.predictiveEngine.PredictConflictProbability(ctx, features)
		strategy := benchmark.strategySelector.SelectStrategy(ctx, features, prediction)
		validation := benchmark.validationPipeline.ValidateMerge(ctx, repo, sourceCommit, targetCommit)

		// Validate results
		if prediction.Probability < 0 || prediction.Probability > 1 {
			b.Fatal("Invalid prediction")
		}
		if strategy.Strategy == "" {
			b.Fatal("Invalid strategy")
		}
		if validation == nil {
			b.Fatal("Invalid validation")
		}
	}
}

// BenchmarkScalability tests system behavior at various scales
func BenchmarkScalability(b *testing.B) {
	logger := zaptest.NewLogger(b)

	scales := []struct {
		name      string
		fileCount int
		envFiles  int
		patterns  int
	}{
		{"small_scale", 10, 2, 100},
		{"medium_scale", 100, 10, 1000},
		{"large_scale", 500, 25, 5000},
		{"enterprise_scale", 2000, 100, 20000},
	}

	for _, scale := range scales {
		b.Run(scale.name, func(b *testing.B) {
			benchmark := NewPhase2IntelligenceBenchmark(logger)

			// Pre-populate pattern database
			for i := 0; i < scale.patterns; i++ {
				pattern := &merge.ConflictPattern{
					ID:        fmt.Sprintf("scale-pattern-%d", i),
					Files:     []string{fmt.Sprintf("scale-file-%d.go", i)},
					Authors:   []string{fmt.Sprintf("author-%d", i%20)},
					Frequency: i % 50,
				}
				benchmark.patternDatabase.StorePattern(pattern)
			}

			repo, sourceCommit, targetCommit := createBenchmarkRepo(b, scale.fileCount, scale.envFiles)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ctx := context.Background()

				extractor := merge.NewMergeFeatureExtractor(logger)
				features, err := extractor.ExtractFeatures(ctx, repo, sourceCommit, targetCommit)
				if err != nil {
					b.Fatal("Feature extraction failed:", err)
				}

				prediction := benchmark.predictiveEngine.PredictConflictProbability(ctx, features)
				if prediction.Probability < 0 || prediction.Probability > 1 {
					b.Fatal("Invalid prediction at scale")
				}
			}
		})
	}
}

// Helper functions

func createBenchmarkRepo(b *testing.B, fileCount, envFileCount int) (*git.Repository, *object.Commit, *object.Commit) {
	repo, err := git.Init(memory.NewStorage(), nil)
	require.NoError(b, err)

	// Create source files
	sourceFiles := make(map[string]string)
	for i := 0; i < fileCount; i++ {
		if i < envFileCount {
			sourceFiles[fmt.Sprintf("config/env-%d.yml", i)] = fmt.Sprintf("env_%d:\n  value: source-%d", i, i)
		} else {
			sourceFiles[fmt.Sprintf("src/file-%d.go", i)] = fmt.Sprintf("package main\n\n// Source file %d\nfunc source%d() {}", i, i)
		}
	}

	sourceCommit := createBenchmarkCommit(b, repo, "source-author", sourceFiles)

	// Create target files with some overlapping changes
	targetFiles := make(map[string]string)
	for i := 0; i < fileCount; i++ {
		if i < envFileCount {
			targetFiles[fmt.Sprintf("config/env-%d.yml", i)] = fmt.Sprintf("env_%d:\n  value: target-%d", i, i)
		} else if i%3 == 0 { // 1/3 overlap
			targetFiles[fmt.Sprintf("src/file-%d.go", i)] = fmt.Sprintf("package main\n\n// Target file %d\nfunc target%d() {}", i, i)
		} else {
			targetFiles[fmt.Sprintf("src/new-file-%d.go", i)] = fmt.Sprintf("package main\n\n// New file %d\nfunc new%d() {}", i, i)
		}
	}

	targetCommit := createBenchmarkCommit(b, repo, "target-author", targetFiles)

	return repo, sourceCommit, targetCommit
}

func createBenchmarkCommit(b *testing.B, repo *git.Repository, authorName string, files map[string]string) *object.Commit {
	w, err := repo.Worktree()
	require.NoError(b, err)

	for path, content := range files {
		file, err := w.Filesystem.Create(path)
		require.NoError(b, err)

		_, err = file.Write([]byte(content))
		require.NoError(b, err)
		file.Close()

		_, err = w.Add(path)
		require.NoError(b, err)
	}

	commit, err := w.Commit("Benchmark commit by "+authorName, &git.CommitOptions{
		Author: &object.Signature{
			Name:  authorName,
			Email: authorName + "@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(b, err)

	commitObj, err := repo.CommitObject(commit)
	require.NoError(b, err)

	return commitObj
}

func generateTrainingSamples(count int) []*merge.TrainingSample {
	samples := make([]*merge.TrainingSample, count)

	for i := 0; i < count; i++ {
		samples[i] = &merge.TrainingSample{
			Features: &merge.MergeFeatures{
				FilesChanged:        (i % 20) + 1,
				LinesChanged:        ((i % 10) + 1) * 50,
				OverlappingFiles:    i % 5,
				AuthorCount:         (i % 3) + 1,
				MergeComplexity:     float64(i%10) / 10.0,
				HasEnvironmentFiles: i%3 == 0,
			},
			HadConflict: i%2 == 0,
			Resolution:  []string{"automatic", "manual", "semi-automatic"}[i%3],
			Duration:    time.Duration((i%10)+1) * time.Minute,
		}
	}

	return samples
}

// Performance metrics collection

type IntelligenceMetrics struct {
	PredictionLatency    time.Duration
	ValidationLatency    time.Duration
	StrategyLatency      time.Duration
	EndToEndLatency      time.Duration
	MemoryUsage          int64
	ConcurrentThroughput int
	ErrorRate            float64
}

func (metrics *IntelligenceMetrics) String() string {
	return fmt.Sprintf(
		"Intelligence Metrics:\n"+
			"  Prediction Latency: %v\n"+
			"  Validation Latency: %v\n"+
			"  Strategy Latency: %v\n"+
			"  End-to-End Latency: %v\n"+
			"  Memory Usage: %d bytes\n"+
			"  Concurrent Throughput: %d ops/sec\n"+
			"  Error Rate: %.2f%%",
		metrics.PredictionLatency,
		metrics.ValidationLatency,
		metrics.StrategyLatency,
		metrics.EndToEndLatency,
		metrics.MemoryUsage,
		metrics.ConcurrentThroughput,
		metrics.ErrorRate*100,
	)
}
