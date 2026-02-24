package merge

import (
	"context"
	"fmt"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// ValidationPipeline orchestrates pre-merge validation checks
type ValidationPipeline struct {
	logger            *zap.Logger
	environmentSystem *EnvironmentProtectionSystem
	validators        []Validator
	backupManager     *BackupManager
}

// Validator interface for different types of validation checks
type Validator interface {
	Name() string
	Validate(ctx context.Context, change *ValidationFileChange) *ValidationResult
	Priority() int // Lower numbers = higher priority
}

// ValidationFileChange represents a change to a file in the merge for validation
type ValidationFileChange struct {
	Path          string
	ChangeType    ChangeType
	SourceContent []byte
	TargetContent []byte
	BaseContent   []byte
}

// ChangeType represents the type of change
type ChangeType string

const (
	ChangeTypeAdded    ChangeType = "added"
	ChangeTypeModified ChangeType = "modified"
	ChangeTypeDeleted  ChangeType = "deleted"
	ChangeTypeRenamed  ChangeType = "renamed"
)

// PipelineResult contains the overall validation result
type PipelineResult struct {
	Valid             bool                         `json:"valid"`
	TotalChecks       int                          `json:"total_checks"`
	PassedChecks      int                          `json:"passed_checks"`
	FailedChecks      int                          `json:"failed_checks"`
	ValidationResults map[string]*ValidationResult `json:"validation_results"`
	EnvironmentImpact *EnvironmentImpactAnalysis   `json:"environment_impact"`
	Recommendations   []string                     `json:"recommendations"`
	ExecutionTime     time.Duration                `json:"execution_time"`
	Metadata          map[string]interface{}       `json:"metadata"`
}

// EnvironmentImpactAnalysis provides detailed environment impact analysis
type EnvironmentImpactAnalysis struct {
	AffectedEnvironments []string                              `json:"affected_environments"`
	EnvironmentFiles     []*EnvironmentClassification          `json:"environment_files"`
	RiskAssessment       string                                `json:"risk_assessment"`
	BackupRecommended    bool                                  `json:"backup_recommended"`
	ManualReviewRequired bool                                  `json:"manual_review_required"`
	PolicyViolations     []string                              `json:"policy_violations"`
	FileClassifications  map[string]*EnvironmentClassification `json:"file_classifications"`
}

// NewValidationPipeline creates a new validation pipeline
func NewValidationPipeline(logger *zap.Logger) *ValidationPipeline {
	pipeline := &ValidationPipeline{
		logger:            logger,
		environmentSystem: NewEnvironmentProtectionSystem(logger),
		validators:        make([]Validator, 0),
		backupManager:     NewBackupManager(logger),
	}

	// Register default validators
	pipeline.RegisterValidator(&EnvironmentFileValidator{envSystem: pipeline.environmentSystem, logger: logger})
	pipeline.RegisterValidator(&SyntaxValidator{logger: logger})
	pipeline.RegisterValidator(&SecurityValidator{logger: logger})
	pipeline.RegisterValidator(&PolicyValidator{logger: logger})

	return pipeline
}

// RegisterValidator adds a new validator to the pipeline
func (vp *ValidationPipeline) RegisterValidator(validator Validator) {
	vp.validators = append(vp.validators, validator)

	// Sort validators by priority
	for i := len(vp.validators) - 1; i > 0; i-- {
		if vp.validators[i].Priority() < vp.validators[i-1].Priority() {
			vp.validators[i], vp.validators[i-1] = vp.validators[i-1], vp.validators[i]
		} else {
			break
		}
	}

	vp.logger.Debug("Validator registered",
		zap.String("validator", validator.Name()),
		zap.Int("priority", validator.Priority()))
}

// ValidateMerge performs comprehensive pre-merge validation
func (vp *ValidationPipeline) ValidateMerge(
	ctx context.Context,
	repo *gogit.Repository,
	sourceCommit, targetCommit *object.Commit,
) *PipelineResult {
	startTime := time.Now()

	result := &PipelineResult{
		Valid:             true,
		ValidationResults: make(map[string]*ValidationResult),
		EnvironmentImpact: &EnvironmentImpactAnalysis{
			FileClassifications: make(map[string]*EnvironmentClassification),
		},
		Metadata: make(map[string]interface{}),
	}

	vp.logger.Info("Starting pre-merge validation",
		zap.String("source", sourceCommit.Hash.String()[:7]),
		zap.String("target", targetCommit.Hash.String()[:7]))

	// 1. Analyze file changes
	changes, err := vp.analyzeFileChanges(repo, sourceCommit, targetCommit)
	if err != nil {
		result.Valid = false
		result.ValidationResults["file_analysis"] = &ValidationResult{
			Valid:  false,
			Errors: []string{fmt.Sprintf("Failed to analyze file changes: %v", err)},
		}
		return result
	}

	// 2. Classify environment files
	vp.classifyEnvironmentFiles(changes, result.EnvironmentImpact)

	// 3. Run validation checks
	for _, change := range changes {
		if ctx.Err() != nil {
			result.Valid = false
			result.ValidationResults["context_cancelled"] = &ValidationResult{
				Valid:  false,
				Errors: []string{"Validation cancelled due to context timeout"},
			}
			break
		}

		vp.validateFileChange(ctx, change, result)
	}

	// 4. Generate environment impact analysis
	vp.generateEnvironmentImpactAnalysis(changes, result.EnvironmentImpact)

	// 5. Generate recommendations
	result.Recommendations = vp.generateRecommendations(result)

	// 6. Calculate final result
	result.ExecutionTime = time.Since(startTime)
	result.TotalChecks = len(result.ValidationResults)
	for _, validationResult := range result.ValidationResults {
		if validationResult.Valid {
			result.PassedChecks++
		} else {
			result.FailedChecks++
			result.Valid = false
		}
	}

	vp.logger.Info("Pre-merge validation completed",
		zap.Bool("valid", result.Valid),
		zap.Int("total_checks", result.TotalChecks),
		zap.Int("passed", result.PassedChecks),
		zap.Int("failed", result.FailedChecks),
		zap.Duration("execution_time", result.ExecutionTime))

	return result
}

// analyzeFileChanges identifies all file changes between commits
func (vp *ValidationPipeline) analyzeFileChanges(
	repo *gogit.Repository,
	sourceCommit, targetCommit *object.Commit,
) ([]*ValidationFileChange, error) {
	changes := make([]*ValidationFileChange, 0)

	// Get trees
	sourceTree, err := sourceCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get source tree: %w", err)
	}

	targetTree, err := targetCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get target tree: %w", err)
	}

	// Compare trees to find changes
	patch, err := targetTree.Patch(sourceTree)
	if err != nil {
		return nil, fmt.Errorf("failed to generate patch: %w", err)
	}

	for _, filePatch := range patch.FilePatches() {
		from, to := filePatch.Files()

		var change *ValidationFileChange

		if from == nil && to != nil {
			// File added
			sourceContent, _ := vp.getFileContent(repo, sourceTree, to.Path())
			change = &ValidationFileChange{
				Path:          to.Path(),
				ChangeType:    ChangeTypeAdded,
				SourceContent: sourceContent,
			}
		} else if from != nil && to == nil {
			// File deleted
			targetContent, _ := vp.getFileContent(repo, targetTree, from.Path())
			change = &ValidationFileChange{
				Path:          from.Path(),
				ChangeType:    ChangeTypeDeleted,
				TargetContent: targetContent,
			}
		} else if from != nil && to != nil {
			// File modified or renamed
			sourceContent, _ := vp.getFileContent(repo, sourceTree, to.Path())
			targetContent, _ := vp.getFileContent(repo, targetTree, from.Path())

			changeType := ChangeTypeModified
			if from.Path() != to.Path() {
				changeType = ChangeTypeRenamed
			}

			change = &ValidationFileChange{
				Path:          to.Path(),
				ChangeType:    changeType,
				SourceContent: sourceContent,
				TargetContent: targetContent,
			}
		}

		if change != nil {
			changes = append(changes, change)
		}
	}

	vp.logger.Debug("File changes analyzed",
		zap.Int("total_changes", len(changes)))

	return changes, nil
}

// getFileContent retrieves file content from a tree
func (vp *ValidationPipeline) getFileContent(repo *gogit.Repository, tree *object.Tree, path string) ([]byte, error) {
	file, err := tree.File(path)
	if err != nil {
		return nil, err
	}

	reader, err := file.Reader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	content := make([]byte, file.Size)
	_, err = reader.Read(content)
	return content, err
}

// classifyEnvironmentFiles identifies and classifies environment files in the changes
func (vp *ValidationPipeline) classifyEnvironmentFiles(changes []*ValidationFileChange, impact *EnvironmentImpactAnalysis) {
	environmentTypes := make(map[string]bool)

	for _, change := range changes {
		if IsEnvironmentFile(change.Path) {
			classification := vp.environmentSystem.ClassifyEnvironmentFile(change.Path)
			impact.EnvironmentFiles = append(impact.EnvironmentFiles, classification)
			impact.FileClassifications[change.Path] = classification

			// Track affected environment types
			environmentTypes[string(classification.Type)] = true

			// Check if backup is required
			if classification.RequiresBackup {
				impact.BackupRecommended = true
			}

			// Check if manual review is required
			if classification.ProtectionLevel == ProtectionImmutable ||
				(classification.ProtectionLevel == ProtectionValidated && classification.IsSensitive) {
				impact.ManualReviewRequired = true
			}
		}
	}

	// Convert environment types map to slice
	for envType := range environmentTypes {
		impact.AffectedEnvironments = append(impact.AffectedEnvironments, envType)
	}
}

// validateFileChange runs all validators against a file change
func (vp *ValidationPipeline) validateFileChange(ctx context.Context, change *ValidationFileChange, result *PipelineResult) {
	for _, validator := range vp.validators {
		validatorResult := validator.Validate(ctx, change)

		if validatorResult != nil {
			key := fmt.Sprintf("%s_%s", validator.Name(), change.Path)
			result.ValidationResults[key] = validatorResult

			vp.logger.Debug("Validator executed",
				zap.String("validator", validator.Name()),
				zap.String("file", change.Path),
				zap.Bool("valid", validatorResult.Valid))
		}
	}
}

// generateEnvironmentImpactAnalysis creates comprehensive environment impact analysis
func (vp *ValidationPipeline) generateEnvironmentImpactAnalysis(changes []*ValidationFileChange, impact *EnvironmentImpactAnalysis) {
	// Assess risk level
	riskFactors := []string{}

	productionFiles := 0
	sensitiveFiles := 0

	for _, classification := range impact.FileClassifications {
		if classification.Type == EnvTypeProduction {
			productionFiles++
			riskFactors = append(riskFactors, "Production environment files affected")
		}

		if classification.IsSensitive {
			sensitiveFiles++
			riskFactors = append(riskFactors, "Sensitive configuration files affected")
		}

		if classification.ProtectionLevel == ProtectionImmutable {
			impact.PolicyViolations = append(impact.PolicyViolations,
				fmt.Sprintf("Immutable file %s cannot be modified automatically", classification.Path))
		}
	}

	// Determine overall risk assessment
	if productionFiles > 0 || len(impact.PolicyViolations) > 0 {
		impact.RiskAssessment = "HIGH"
	} else if sensitiveFiles > 0 || impact.ManualReviewRequired {
		impact.RiskAssessment = "MEDIUM"
	} else if len(impact.EnvironmentFiles) > 0 {
		impact.RiskAssessment = "LOW"
	} else {
		impact.RiskAssessment = "MINIMAL"
	}
}

// generateRecommendations creates actionable recommendations based on validation results
func (vp *ValidationPipeline) generateRecommendations(result *PipelineResult) []string {
	recommendations := []string{}

	// Environment-specific recommendations
	if result.EnvironmentImpact.BackupRecommended {
		recommendations = append(recommendations,
			"Create backup of environment files before merging")
	}

	if result.EnvironmentImpact.ManualReviewRequired {
		recommendations = append(recommendations,
			"Manual review required for sensitive or immutable files")
	}

	if result.EnvironmentImpact.RiskAssessment == "HIGH" {
		recommendations = append(recommendations,
			"High-risk merge detected - consider additional validation and testing")
	}

	// Validation failure recommendations
	if !result.Valid {
		recommendations = append(recommendations,
			"Resolve validation failures before proceeding with merge")
	}

	// Policy violation recommendations
	if len(result.EnvironmentImpact.PolicyViolations) > 0 {
		recommendations = append(recommendations,
			"Address policy violations or obtain necessary approvals")
	}

	return recommendations
}

// BackupManager handles environment file backups
type BackupManager struct {
	logger *zap.Logger
}

// NewBackupManager creates a new backup manager
func NewBackupManager(logger *zap.Logger) *BackupManager {
	return &BackupManager{
		logger: logger,
	}
}

// CreateBackup creates a backup of environment files (placeholder implementation)
func (bm *BackupManager) CreateBackup(files []string) error {
	// In a real implementation, this would:
	// 1. Create timestamped backup directory
	// 2. Copy files to backup location
	// 3. Store backup metadata
	// 4. Return backup ID

	bm.logger.Info("Environment backup created",
		zap.Strings("files", files))

	return nil
}
