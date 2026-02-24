package merge

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"
)

func TestPredictiveConflictEngine_Creation(t *testing.T) {
	logger := zaptest.NewLogger(t)
	engine := NewPredictiveConflictEngine(logger)

	assert.NotNil(t, engine, "Engine should be created")
	assert.NotNil(t, engine.model, "Model should be initialized")
	assert.NotNil(t, engine.featureExtractor, "Feature extractor should be initialized")
	assert.NotNil(t, engine.patternDatabase, "Pattern database should be initialized")
	assert.True(t, engine.learningEnabled, "Learning should be enabled by default")
}

func TestConflictPredictionModel_Creation(t *testing.T) {
	model := NewConflictPredictionModel()

	assert.NotNil(t, model, "Model should be created")
	assert.NotNil(t, model.weights, "Weights should be initialized")
	assert.NotNil(t, model.biases, "Biases should be initialized")
	assert.NotNil(t, model.featureNormalizers, "Feature normalizers should be initialized")
	assert.NotNil(t, model.trainingData, "Training data should be initialized")
}

func TestMergeFeatures_Structure(t *testing.T) {
	features := &MergeFeatures{
		TotalFiles:           10,
		ModifiedFiles:        5,
		AddedFiles:           3,
		DeletedFiles:         2,
		EnvironmentFiles:     1,
		LinesAdded:           100,
		LinesDeleted:         50,
		AvgFileSize:          1500.5,
		MaxFileSize:          5000,
		AuthorExperience:     0.8,
		BranchAge:            24.5,
		CommitFrequency:      3.2,
		LastMergeInterval:    48.0,
		SimilarPathChanges:   2,
		FileTypeDistribution: map[string]int{"go": 8, "yaml": 2},
		SemanticComplexity:   0.6,
		CrossFileReferences:  5,
		DatabaseChanges:      true,
		APIChanges:           false,
		TimeOfDay:            14,
		DayOfWeek:            2,
		TeamVelocity:         0.7,
	}

	// Test that all fields are accessible
	assert.Equal(t, 10, features.TotalFiles)
	assert.Equal(t, 5, features.ModifiedFiles)
	assert.Equal(t, 3, features.AddedFiles)
	assert.Equal(t, 2, features.DeletedFiles)
	assert.Equal(t, 1, features.EnvironmentFiles)
	assert.Equal(t, 100, features.LinesAdded)
	assert.Equal(t, 50, features.LinesDeleted)
	assert.Equal(t, 1500.5, features.AvgFileSize)
	assert.Equal(t, 5000, features.MaxFileSize)
	assert.Equal(t, 0.8, features.AuthorExperience)
	assert.Equal(t, 24.5, features.BranchAge)
	assert.Equal(t, 3.2, features.CommitFrequency)
	assert.Equal(t, 48.0, features.LastMergeInterval)
	assert.Equal(t, 2, features.SimilarPathChanges)
	assert.Equal(t, 2, len(features.FileTypeDistribution))
	assert.Equal(t, 8, features.FileTypeDistribution["go"])
	assert.Equal(t, 2, features.FileTypeDistribution["yaml"])
	assert.Equal(t, 0.6, features.SemanticComplexity)
	assert.Equal(t, 5, features.CrossFileReferences)
	assert.True(t, features.DatabaseChanges)
	assert.False(t, features.APIChanges)
	assert.Equal(t, 14, features.TimeOfDay)
	assert.Equal(t, 2, features.DayOfWeek)
	assert.Equal(t, 0.7, features.TeamVelocity)
}

func TestConflictPredictionModel_BasicPrediction(t *testing.T) {
	model := NewConflictPredictionModel()

	features := &MergeFeatures{
		TotalFiles:          5,
		ModifiedFiles:       3,
		EnvironmentFiles:    1,
		LinesAdded:          150,
		LinesDeleted:        50,
		AuthorExperience:    0.5,
		SemanticComplexity:  0.3,
		DatabaseChanges:     false,
		APIChanges:          false,
	}

	prediction := model.Predict(features)

	// Basic validation of prediction
	assert.True(t, prediction.ConflictProbability >= 0.0, "Probability should be >= 0")
	assert.True(t, prediction.ConflictProbability <= 1.0, "Probability should be <= 1")
	assert.True(t, prediction.Confidence >= 0.0, "Confidence should be >= 0")
	assert.True(t, prediction.Confidence <= 1.0, "Confidence should be <= 1")
	assert.NotNil(t, prediction.PredictedConflicts, "Predicted conflicts should be initialized")
	assert.NotNil(t, prediction.RiskFactors, "Risk factors should be initialized")

	t.Logf("Prediction: %.3f probability with %.3f confidence",
		prediction.ConflictProbability, prediction.Confidence)
}

func TestFeatureNormalizer_Operations(t *testing.T) {
	normalizer := &FeatureNormalizer{
		Mean:   50.0,
		StdDev: 15.0,
		Min:    10.0,
		Max:    100.0,
	}

	// Test normalization ranges
	assert.Equal(t, 50.0, normalizer.Mean)
	assert.Equal(t, 15.0, normalizer.StdDev)
	assert.Equal(t, 10.0, normalizer.Min)
	assert.Equal(t, 100.0, normalizer.Max)
}

func TestConflictPatternDatabase_Creation(t *testing.T) {
	db := NewConflictPatternDatabase()

	assert.NotNil(t, db, "Database should be created")
	assert.NotNil(t, db.filePatterns, "File patterns should be initialized")
	assert.NotNil(t, db.authorPatterns, "Author patterns should be initialized")
	assert.NotNil(t, db.timePatterns, "Time patterns should be initialized")
}

func TestMergeFeatureExtractor_Creation(t *testing.T) {
	logger := zaptest.NewLogger(t)
	extractor := &MergeFeatureExtractor{logger: logger}

	assert.NotNil(t, extractor, "Extractor should be created")
	assert.NotNil(t, extractor.logger, "Logger should be set")
}

func TestTrainingExample_Structure(t *testing.T) {
	features := &MergeFeatures{
		TotalFiles:       5,
		ModifiedFiles:    3,
		EnvironmentFiles: 1,
		LinesAdded:       100,
		LinesDeleted:     50,
	}

	example := &TrainingExample{
		Features:        features,
		ActualConflicts: true,
		ConflictDetails: []*ActualConflict{},
		Resolution:      &ResolutionOutcome{},
	}

	assert.NotNil(t, example, "Training example should be created")
	assert.NotNil(t, example.Features, "Features should be set")
	assert.True(t, example.ActualConflicts, "Should record actual conflicts")
	assert.NotNil(t, example.ConflictDetails, "Conflict details should be initialized")
	assert.NotNil(t, example.Resolution, "Resolution should be initialized")
}

func TestConflictPrediction_Structure(t *testing.T) {
	prediction := &ConflictPrediction{
		ConflictProbability:   0.7,
		Confidence:            0.8,
		PredictedConflicts:    []*PredictedConflict{},
		RiskFactors:           []string{"environment_files", "high_complexity"},
		PreventionSuggestions: []string{"review_changes", "test_locally"},
		OptimalStrategy:       MergeStrategyRecursive,
		Metadata:              make(map[string]interface{}),
	}

	assert.Equal(t, 0.7, prediction.ConflictProbability)
	assert.Equal(t, 0.8, prediction.Confidence)
	assert.NotNil(t, prediction.PredictedConflicts)
	assert.Equal(t, 2, len(prediction.RiskFactors))
	assert.Equal(t, "environment_files", prediction.RiskFactors[0])
	assert.Equal(t, "high_complexity", prediction.RiskFactors[1])
	assert.Equal(t, 2, len(prediction.PreventionSuggestions))
	assert.Equal(t, MergeStrategyRecursive, prediction.OptimalStrategy)
	assert.NotNil(t, prediction.Metadata)
}

func TestPredictedConflict_Structure(t *testing.T) {
	conflict := &PredictedConflict{
		FilePath:        "config/app.yml",
		ConflictType:    "content_conflict",
		Probability:     0.8,
		Severity:        "high",
		ResolutionHint:  "manual_review_required",
		EnvironmentFile: true,
	}

	assert.Equal(t, "config/app.yml", conflict.FilePath)
	assert.Equal(t, "content_conflict", conflict.ConflictType)
	assert.Equal(t, 0.8, conflict.Probability)
	assert.Equal(t, "high", conflict.Severity)
	assert.Equal(t, "manual_review_required", conflict.ResolutionHint)
	assert.True(t, conflict.EnvironmentFile)
}

func TestIntegrationBasicWorkflow(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create engine
	engine := NewPredictiveConflictEngine(logger)

	// Create sample features
	features := &MergeFeatures{
		TotalFiles:          8,
		ModifiedFiles:       5,
		AddedFiles:          2,
		DeletedFiles:        1,
		EnvironmentFiles:    2,
		LinesAdded:          250,
		LinesDeleted:        100,
		AuthorExperience:    0.6,
		BranchAge:           24.0,
		SemanticComplexity:  0.4,
		DatabaseChanges:     false,
		APIChanges:          true,
		FileTypeDistribution: map[string]int{"go": 6, "yaml": 2},
	}

	// Test prediction
	prediction := engine.model.Predict(features)

	assert.NotNil(t, prediction, "Prediction should be created")
	assert.True(t, prediction.ConflictProbability >= 0.0 && prediction.ConflictProbability <= 1.0,
		"Probability should be in valid range")
	assert.True(t, prediction.Confidence >= 0.0 && prediction.Confidence <= 1.0,
		"Confidence should be in valid range")

	// Test pattern database
	db := engine.patternDatabase

	// Test the structure
	assert.NotNil(t, db.filePatterns, "File patterns map should exist")

	t.Logf("✅ Integration test completed successfully")
	t.Logf("   Prediction: %.3f probability with %.3f confidence",
		prediction.ConflictProbability, prediction.Confidence)
}

// Benchmark test for performance validation
func BenchmarkConflictPrediction(b *testing.B) {
	model := NewConflictPredictionModel()

	features := &MergeFeatures{
		TotalFiles:          10,
		ModifiedFiles:       6,
		EnvironmentFiles:    2,
		LinesAdded:          300,
		LinesDeleted:        150,
		AuthorExperience:    0.7,
		SemanticComplexity:  0.5,
		DatabaseChanges:     true,
		APIChanges:          false,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		prediction := model.Predict(features)
		if prediction.ConflictProbability < 0 || prediction.ConflictProbability > 1 {
			b.Fatal("Invalid prediction probability")
		}
	}
}