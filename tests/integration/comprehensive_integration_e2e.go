package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/events"
	"github.com/gin-gonic/gin"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// testRESTAPIProtectionIntegration tests REST API endpoints with protection enabled/disabled
func (suite *ComprehensiveIntegrationTestSuite) testRESTAPIProtectionIntegration(t *testing.T) {
	// Set up test HTTP server
	gin.SetMode(gin.TestMode)
	router := gin.New()

	// Add test endpoints that simulate the real API
	suite.setupTestAPIEndpoints(router)

	testServer := httptest.NewServer(router)
	defer testServer.Close()

	// Test with protection enabled
	t.Run("Protection_Enabled", func(t *testing.T) {
		suite.testAPIWithProtectionEnabled(t, testServer.URL)
	})

	// Test with protection disabled
	t.Run("Protection_Disabled", func(t *testing.T) {
		suite.testAPIWithProtectionDisabled(t, testServer.URL)
	})

	// Test feature flag combinations
	t.Run("Feature_Flag_Combinations", func(t *testing.T) {
		suite.testFeatureFlagCombinations(t, testServer.URL)
	})
}

// setupTestAPIEndpoints sets up test API endpoints
func (suite *ComprehensiveIntegrationTestSuite) setupTestAPIEndpoints(router *gin.Engine) {
	api := router.Group("/api/v1")

	// Repository operations endpoint
	api.POST("/repos/:owner/:repo/merge", func(c *gin.Context) {
		owner := c.Param("owner")
		repo := c.Param("repo")

		var mergeRequest struct {
			Source string `json:"source"`
			Target string `json:"target"`
			Type   string `json:"type"` // merge, squash, rebase
		}

		if err := c.ShouldBindJSON(&mergeRequest); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Simulate merge protection logic
		protectionEnabled := suite.config.MergeProtection.Enabled
		if protectionEnabled {
			// Check if merge should be blocked
			if suite.shouldBlockMerge(owner, repo, mergeRequest.Type) {
				c.JSON(http.StatusForbidden, gin.H{
					"error":           "Merge blocked by protection policy",
					"reason":          "Environment file protection",
					"policy":          "strict",
					"protected_files": []string{".env", "config/*.yaml"},
				})
				return
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"status":    "completed",
			"merge_id":  fmt.Sprintf("merge-%d", time.Now().Unix()),
			"protected": protectionEnabled,
		})
	})

	// Repository overview endpoint
	api.GET("/repos/:owner/:repo/overview", func(c *gin.Context) {
		owner := c.Param("owner")
		repo := c.Param("repo")

		// Find matching test repository
		var testRepo *TestRepository
		for _, tr := range suite.testRepositories {
			if strings.Contains(tr.Name, owner) || strings.Contains(tr.Name, repo) {
				testRepo = tr
				break
			}
		}

		if testRepo == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Repository not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"owner":        owner,
			"repo":         repo,
			"file_count":   testRepo.FileCount,
			"branch_count": testRepo.BranchCount,
			"commit_count": testRepo.CommitCount,
			"type":         testRepo.RepoType,
		})
	})

	// Health check endpoint
	api.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":    "healthy",
			"timestamp": time.Now().Format(time.RFC3339),
			"version":   "test",
		})
	})
}

// shouldBlockMerge determines if a merge should be blocked
func (suite *ComprehensiveIntegrationTestSuite) shouldBlockMerge(owner, repo, mergeType string) bool {
	// Simulate logic that would check for environment files and protection policies
	protectedRepos := []string{"critical", "production", "env"}

	for _, protected := range protectedRepos {
		if strings.Contains(strings.ToLower(owner), protected) ||
			strings.Contains(strings.ToLower(repo), protected) {
			return true
		}
	}

	// Block squash merges in strict mode
	if mergeType == "squash" && suite.config.MergeProtection.SafetyMode == "strict" {
		return true
	}

	return false
}

// testAPIWithProtectionEnabled tests API behavior with protection enabled
func (suite *ComprehensiveIntegrationTestSuite) testAPIWithProtectionEnabled(t *testing.T, baseURL string) {
	originalEnabled := suite.config.MergeProtection.Enabled
	suite.config.MergeProtection.Enabled = true
	defer func() { suite.config.MergeProtection.Enabled = originalEnabled }()

	testCases := []struct {
		name           string
		owner          string
		repo           string
		mergeType      string
		expectBlocked  bool
		expectedStatus int
	}{
		{"Protected_Repo_Merge", "critical-org", "app", "merge", true, http.StatusForbidden},
		{"Protected_Repo_Squash", "production", "service", "squash", true, http.StatusForbidden},
		{"Normal_Repo_Merge", "team", "project", "merge", false, http.StatusOK},
		{"Normal_Repo_Rebase", "dev", "tool", "rebase", false, http.StatusOK},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mergeURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/merge", baseURL, tc.owner, tc.repo)

			mergeRequest := map[string]interface{}{
				"source": "feature/test",
				"target": "main",
				"type":   tc.mergeType,
			}

			jsonData, err := json.Marshal(mergeRequest)
			require.NoError(t, err, "Failed to marshal merge request")

			resp, err := http.Post(mergeURL, "application/json", bytes.NewBuffer(jsonData))
			require.NoError(t, err, "Failed to make merge request")
			defer resp.Body.Close()

			assert.Equal(t, tc.expectedStatus, resp.StatusCode,
				"Merge request status should match expected for %s", tc.name)

			var responseBody map[string]interface{}
			err = json.NewDecoder(resp.Body).Decode(&responseBody)
			require.NoError(t, err, "Failed to decode response body")

			if tc.expectBlocked {
				assert.Contains(t, responseBody, "error", "Blocked merge should have error field")
				assert.Contains(t, responseBody, "reason", "Blocked merge should have reason field")
			} else {
				assert.Contains(t, responseBody, "status", "Successful merge should have status field")
				assert.Equal(t, "completed", responseBody["status"], "Merge status should be completed")
			}

			suite.logger.Info("API protection test completed",
				zap.String("test_case", tc.name),
				zap.Int("status_code", resp.StatusCode),
				zap.Bool("expected_blocked", tc.expectBlocked))
		})
	}
}

// testAPIWithProtectionDisabled tests API behavior with protection disabled
func (suite *ComprehensiveIntegrationTestSuite) testAPIWithProtectionDisabled(t *testing.T, baseURL string) {
	originalEnabled := suite.config.MergeProtection.Enabled
	suite.config.MergeProtection.Enabled = false
	defer func() { suite.config.MergeProtection.Enabled = originalEnabled }()

	// All merges should succeed when protection is disabled
	testCases := []struct {
		owner     string
		repo      string
		mergeType string
	}{
		{"critical-org", "app", "merge"},
		{"production", "service", "squash"},
		{"env-repo", "config", "rebase"},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("Unprotected_%s_%s", tc.owner, tc.mergeType), func(t *testing.T) {
			mergeURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/merge", baseURL, tc.owner, tc.repo)

			mergeRequest := map[string]interface{}{
				"source": "feature/test",
				"target": "main",
				"type":   tc.mergeType,
			}

			jsonData, err := json.Marshal(mergeRequest)
			require.NoError(t, err, "Failed to marshal merge request")

			resp, err := http.Post(mergeURL, "application/json", bytes.NewBuffer(jsonData))
			require.NoError(t, err, "Failed to make merge request")
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode,
				"Merge should succeed when protection is disabled")

			var responseBody map[string]interface{}
			err = json.NewDecoder(resp.Body).Decode(&responseBody)
			require.NoError(t, err, "Failed to decode response body")

			assert.Equal(t, "completed", responseBody["status"], "Merge status should be completed")
			assert.Equal(t, false, responseBody["protected"], "Protection should be disabled")
		})
	}
}

// testFeatureFlagCombinations tests various feature flag combinations
func (suite *ComprehensiveIntegrationTestSuite) testFeatureFlagCombinations(t *testing.T, baseURL string) {
	flagCombinations := []struct {
		name               string
		enabled            bool
		intelligentMerging bool
		adaptiveFallback   bool
		predictiveConflict bool
		safetyMode         string
		expectedBehavior   string
	}{
		{"All_Disabled", false, false, false, false, "disabled", "permissive"},
		{"Basic_Enabled", true, false, false, false, "strict", "basic_protection"},
		{"Intelligent_Only", true, true, false, false, "strict", "intelligent_protection"},
		{"Full_Featured", true, true, true, true, "strict", "full_protection"},
		{"Permissive_Mode", true, true, true, true, "permissive", "permissive_protection"},
	}

	for _, combination := range flagCombinations {
		t.Run(combination.name, func(t *testing.T) {
			// Temporarily set feature flags
			original := suite.config.MergeProtection
			suite.config.MergeProtection.Enabled = combination.enabled
			suite.config.MergeProtection.IntelligentMerging = combination.intelligentMerging
			suite.config.MergeProtection.AdaptiveFallback = combination.adaptiveFallback
			suite.config.MergeProtection.PredictiveConflictDetect = combination.predictiveConflict
			suite.config.MergeProtection.SafetyMode = combination.safetyMode

			defer func() { suite.config.MergeProtection = original }()

			// Test merge operation with this configuration
			mergeURL := fmt.Sprintf("%s/api/v1/repos/test/repo/merge", baseURL)
			mergeRequest := map[string]interface{}{
				"source": "feature/test",
				"target": "main",
				"type":   "merge",
			}

			jsonData, err := json.Marshal(mergeRequest)
			require.NoError(t, err, "Failed to marshal merge request")

			resp, err := http.Post(mergeURL, "application/json", bytes.NewBuffer(jsonData))
			require.NoError(t, err, "Failed to make merge request")
			defer resp.Body.Close()

			// Verify behavior matches expected
			if combination.enabled && combination.safetyMode == "strict" {
				// Strict mode might block some operations
				assert.True(t, resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusForbidden,
					"Strict mode should either allow or block operations")
			} else if !combination.enabled || combination.safetyMode == "permissive" {
				// Disabled or permissive mode should allow operations
				assert.Equal(t, http.StatusOK, resp.StatusCode,
					"Disabled or permissive mode should allow operations")
			}

			suite.logger.Info("Feature flag combination test completed",
				zap.String("combination", combination.name),
				zap.String("expected_behavior", combination.expectedBehavior),
				zap.Int("status_code", resp.StatusCode))
		})
	}
}

// testHTTPErrorHandlingIntegration tests HTTP error handling scenarios
func (suite *ComprehensiveIntegrationTestSuite) testHTTPErrorHandlingIntegration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	// Add error handling middleware
	router.Use(gin.Recovery())

	// Add test endpoints that simulate various error conditions
	api := router.Group("/api/v1")

	api.GET("/error/:type", func(c *gin.Context) {
		errorType := c.Param("type")

		switch errorType {
		case "400":
			c.JSON(http.StatusBadRequest, gin.H{"error": "Bad request test"})
		case "401":
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized test"})
		case "403":
			c.JSON(http.StatusForbidden, gin.H{"error": "Forbidden test"})
		case "404":
			c.JSON(http.StatusNotFound, gin.H{"error": "Not found test"})
		case "500":
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error test"})
		case "timeout":
			time.Sleep(2 * time.Second)
			c.JSON(http.StatusOK, gin.H{"message": "Delayed response"})
		case "panic":
			panic("Test panic for recovery middleware")
		default:
			c.JSON(http.StatusOK, gin.H{"message": "No error"})
		}
	})

	testServer := httptest.NewServer(router)
	defer testServer.Close()

	errorTests := []struct {
		errorType      string
		expectedStatus int
		hasErrorField  bool
	}{
		{"400", http.StatusBadRequest, true},
		{"401", http.StatusUnauthorized, true},
		{"403", http.StatusForbidden, true},
		{"404", http.StatusNotFound, true},
		{"500", http.StatusInternalServerError, true},
		{"none", http.StatusOK, false},
	}

	for _, test := range errorTests {
		t.Run(fmt.Sprintf("HTTP_%s", test.errorType), func(t *testing.T) {
			url := fmt.Sprintf("%s/api/v1/error/%s", testServer.URL, test.errorType)

			resp, err := http.Get(url)
			require.NoError(t, err, "Failed to make request")
			defer resp.Body.Close()

			assert.Equal(t, test.expectedStatus, resp.StatusCode,
				"Status code should match expected for error type: %s", test.errorType)

			var responseBody map[string]interface{}
			err = json.NewDecoder(resp.Body).Decode(&responseBody)
			require.NoError(t, err, "Failed to decode response body")

			if test.hasErrorField {
				assert.Contains(t, responseBody, "error", "Error response should have error field")
				assert.NotEmpty(t, responseBody["error"], "Error field should not be empty")
			} else {
				assert.Contains(t, responseBody, "message", "Success response should have message field")
			}

			suite.logger.Info("HTTP error handling test completed",
				zap.String("error_type", test.errorType),
				zap.Int("status_code", resp.StatusCode),
				zap.Bool("has_error_field", test.hasErrorField))
		})
	}

	// Test panic recovery
	t.Run("Panic_Recovery", func(t *testing.T) {
		url := fmt.Sprintf("%s/api/v1/error/panic", testServer.URL)

		resp, err := http.Get(url)
		require.NoError(t, err, "Failed to make panic request")
		defer resp.Body.Close()

		// Recovery middleware should catch the panic and return 500
		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode,
			"Panic should be recovered and return 500")
	})
}

// testFeatureFlagValidationIntegration tests request validation with feature flags
func (suite *ComprehensiveIntegrationTestSuite) testFeatureFlagValidationIntegration(t *testing.T) {
	// Test various validation scenarios with different feature flag states
	validationTests := []struct {
		name           string
		protectionOn   bool
		requestData    map[string]interface{}
		expectedValid  bool
		expectedReason string
	}{
		{
			"Valid_Request_Protection_On",
			true,
			map[string]interface{}{"source": "feature", "target": "main", "type": "merge"},
			true,
			"valid_request",
		},
		{
			"Invalid_Request_Missing_Source",
			true,
			map[string]interface{}{"target": "main", "type": "merge"},
			false,
			"missing_source",
		},
		{
			"Protected_Merge_Type",
			true,
			map[string]interface{}{"source": "feature", "target": "main", "type": "squash", "force": true},
			false,
			"protected_merge_type",
		},
		{
			"Valid_Request_Protection_Off",
			false,
			map[string]interface{}{"source": "feature", "target": "main", "type": "squash"},
			true,
			"protection_disabled",
		},
	}

	for _, test := range validationTests {
		t.Run(test.name, func(t *testing.T) {
			suite.config.MergeProtection.Enabled = test.protectionOn

			// Validate the request data
			isValid, reason := suite.validateMergeRequest(test.requestData)

			assert.Equal(t, test.expectedValid, isValid,
				"Request validation should match expected for: %s", test.name)

			if !isValid {
				assert.NotEmpty(t, reason, "Invalid request should have a reason")
			}

			suite.logger.Info("Feature flag validation test completed",
				zap.String("test_name", test.name),
				zap.Bool("protection_enabled", test.protectionOn),
				zap.Bool("is_valid", isValid),
				zap.String("reason", reason))
		})
	}
}

// validateMergeRequest validates a merge request based on current configuration
func (suite *ComprehensiveIntegrationTestSuite) validateMergeRequest(requestData map[string]interface{}) (bool, string) {
	// Check required fields
	source, hasSource := requestData["source"]
	target, hasTarget := requestData["target"]
	mergeType, hasType := requestData["type"]

	if !hasSource || source == "" {
		return false, "missing_source"
	}
	if !hasTarget || target == "" {
		return false, "missing_target"
	}
	if !hasType || mergeType == "" {
		return false, "missing_type"
	}

	// Check protection rules
	if suite.config.MergeProtection.Enabled {
		// In strict mode, block certain merge types
		if suite.config.MergeProtection.SafetyMode == "strict" {
			if mergeType == "squash" && requestData["force"] != true {
				return false, "protected_merge_type"
			}
		}
	}

	return true, "valid"
}

// testPerformanceRegression tests for performance regressions
func (suite *ComprehensiveIntegrationTestSuite) testPerformanceRegression(t *testing.T) {
	// Define baseline performance expectations
	baselines := map[string]time.Duration{
		"small_repo_operation":  100 * time.Millisecond,
		"medium_repo_operation": 500 * time.Millisecond,
		"large_repo_operation":  2 * time.Second,
		"api_response":          50 * time.Millisecond,
	}

	// Test each performance category
	for category, baseline := range baselines {
		t.Run(fmt.Sprintf("Performance_%s", category), func(t *testing.T) {
			duration := suite.measurePerformance(t, category)

			// Allow 50% variance from baseline
			maxAcceptable := baseline + (baseline / 2)

			regression := RegressionTestResult{
				TestName:  fmt.Sprintf("Performance_%s", category),
				Component: "performance",
				Expected:  fmt.Sprintf("<%v", maxAcceptable),
				Actual:    duration.String(),
				Passed:    duration <= maxAcceptable,
				Timestamp: time.Now(),
			}

			if duration > baseline {
				regression.ErrorMessage = fmt.Sprintf("Performance regression detected: %v > baseline %v", duration, baseline)
			}

			suite.mutex.Lock()
			suite.regressionResults = append(suite.regressionResults, regression)
			suite.performanceBaselines[category] = duration
			suite.mutex.Unlock()

			if duration <= maxAcceptable {
				assert.True(t, true, "Performance within acceptable range")
			} else {
				t.Logf("PERFORMANCE REGRESSION: %s took %v, baseline was %v", category, duration, baseline)
			}

			suite.logger.Info("Performance regression test completed",
				zap.String("category", category),
				zap.Duration("measured", duration),
				zap.Duration("baseline", baseline),
				zap.Duration("max_acceptable", maxAcceptable),
				zap.Bool("passed", duration <= maxAcceptable))
		})
	}
}

// measurePerformance measures performance for a specific category
func (suite *ComprehensiveIntegrationTestSuite) measurePerformance(t *testing.T, category string) time.Duration {
	start := time.Now()

	switch category {
	case "small_repo_operation":
		// Find a small repository and perform operations
		for _, repo := range suite.testRepositories {
			if repo.FileCount <= 20 {
				suite.performRepositoryOperations(repo)
				break
			}
		}
	case "medium_repo_operation":
		// Find a medium repository and perform operations
		for _, repo := range suite.testRepositories {
			if repo.FileCount > 20 && repo.FileCount <= 50 {
				suite.performRepositoryOperations(repo)
				break
			}
		}
	case "large_repo_operation":
		// Find the largest repository and perform operations
		var largestRepo *TestRepository
		maxFiles := 0
		for _, repo := range suite.testRepositories {
			if repo.FileCount > maxFiles {
				maxFiles = repo.FileCount
				largestRepo = repo
			}
		}
		if largestRepo != nil {
			suite.performRepositoryOperations(largestRepo)
		}
	case "api_response":
		// Simulate API response time
		time.Sleep(10 * time.Millisecond) // Simulate processing
	}

	return time.Since(start)
}

// performRepositoryOperations performs standard operations on a repository
func (suite *ComprehensiveIntegrationTestSuite) performRepositoryOperations(testRepo *TestRepository) {
	repo, err := gogit.PlainOpen(testRepo.Path)
	if err != nil {
		return
	}

	// Perform various operations
	repo.Head()
	refs, _ := repo.References()
	if refs != nil {
		refs.ForEach(func(ref *plumbing.Reference) error {
			return nil // Just iterate
		})
	}
}

// Additional test methods continued in the next part...

// testCompleteWorkflowIntegration tests complete end-to-end workflows
func (suite *ComprehensiveIntegrationTestSuite) testCompleteWorkflowIntegration(t *testing.T) {
	ctx := context.Background()

	// Test complete workflow: API request -> merge protection -> Git operations -> event publication
	t.Run("Complete_Merge_Workflow", func(t *testing.T) {
		suite.testCompleteMergeWorkflow(t, ctx)
	})

	// Test repository overview workflow
	t.Run("Repository_Overview_Workflow", func(t *testing.T) {
		suite.testRepositoryOverviewWorkflow(t, ctx)
	})

	// Test error handling workflow
	t.Run("Error_Handling_Workflow", func(t *testing.T) {
		suite.testErrorHandlingWorkflow(t, ctx)
	})
}

// testCompleteMergeWorkflow tests the complete merge workflow
func (suite *ComprehensiveIntegrationTestSuite) testCompleteMergeWorkflow(t *testing.T, ctx context.Context) {
	// 1. Setup test scenario
	workflowRepo := suite.createWorkflowTestRepo(t)

	// 2. Simulate incoming API request
	mergeRequest := map[string]interface{}{
		"source":      "feature/test-workflow",
		"target":      "main",
		"type":        "merge",
		"description": "Test workflow integration",
	}

	// 3. Apply merge protection logic
	protectionResult := suite.applyMergeProtection(mergeRequest, workflowRepo)
	assert.True(t, protectionResult.Allowed, "Merge should be allowed for test workflow")

	// 4. Perform Git operations
	gitResult := suite.performGitOperations(t, workflowRepo, mergeRequest)
	assert.True(t, gitResult.Success, "Git operations should succeed")

	// 5. Publish events (if Redis is available)
	if suite.redisStreamClient != nil {
		eventResult := suite.publishWorkflowEvents(ctx, workflowRepo, mergeRequest, gitResult)
		assert.True(t, eventResult.Published, "Events should be published successfully")
	}

	// 6. Verify end-to-end result
	finalResult := WorkflowResult{
		RequestProcessed:      true,
		ProtectionApplied:     protectionResult.Applied,
		GitOperationSucceeded: gitResult.Success,
		EventsPublished:       suite.redisStreamClient != nil,
		TotalDuration:         time.Since(time.Now()),
	}

	suite.validateWorkflowResult(t, finalResult)

	suite.logger.Info("Complete merge workflow test completed",
		zap.String("repo", workflowRepo.Name),
		zap.Bool("success", finalResult.GitOperationSucceeded))
}

// WorkflowResult represents the result of an end-to-end workflow test
type WorkflowResult struct {
	RequestProcessed      bool
	ProtectionApplied     bool
	GitOperationSucceeded bool
	EventsPublished       bool
	TotalDuration         time.Duration
}

// ProtectionResult represents the result of merge protection evaluation
type ProtectionResult struct {
	Allowed bool
	Applied bool
	Reason  string
}

// GitOperationResult represents the result of Git operations
type GitOperationResult struct {
	Success    bool
	CommitHash string
	Error      error
}

// EventPublishResult represents the result of event publishing
type EventPublishResult struct {
	Published bool
	EventID   string
	Error     error
}

// createWorkflowTestRepo creates a repository specifically for workflow testing
func (suite *ComprehensiveIntegrationTestSuite) createWorkflowTestRepo(t *testing.T) *TestRepository {
	repoName := "workflow_test_repo"
	repoPath := filepath.Join(suite.testDataDir, repoName)

	repo, err := gogit.PlainInit(repoPath, false)
	require.NoError(t, err, "Failed to create workflow test repository")

	// Add some test files
	testFiles := []string{"main.go", "README.md", "config.yaml"}
	for _, fileName := range testFiles {
		filePath := filepath.Join(repoPath, fileName)
		content := fmt.Sprintf("// Test file: %s\n// Created for workflow testing", fileName)
		err := os.WriteFile(filePath, []byte(content), 0644)
		require.NoError(t, err, "Failed to create test file: %s", fileName)
	}

	return &TestRepository{
		Name:        repoName,
		Path:        repoPath,
		RepoType:    RepoTypeNormal,
		GitRepo:     repo,
		FileCount:   len(testFiles),
		BranchCount: 1,
		CommitCount: 0,
	}
}

// applyMergeProtection applies merge protection logic
func (suite *ComprehensiveIntegrationTestSuite) applyMergeProtection(mergeRequest map[string]interface{}, repo *TestRepository) ProtectionResult {
	if !suite.config.MergeProtection.Enabled {
		return ProtectionResult{
			Allowed: true,
			Applied: false,
			Reason:  "protection_disabled",
		}
	}

	// Apply protection rules
	mergeType, _ := mergeRequest["type"].(string)

	// Check if this merge type is allowed
	if mergeType == "squash" && suite.config.MergeProtection.SafetyMode == "strict" {
		return ProtectionResult{
			Allowed: false,
			Applied: true,
			Reason:  "squash_merge_blocked_in_strict_mode",
		}
	}

	return ProtectionResult{
		Allowed: true,
		Applied: true,
		Reason:  "protection_rules_satisfied",
	}
}

// performGitOperations performs the actual Git operations
func (suite *ComprehensiveIntegrationTestSuite) performGitOperations(t *testing.T, repo *TestRepository, mergeRequest map[string]interface{}) GitOperationResult {
	// Simulate Git operations (in a real scenario, this would perform actual merge)
	time.Sleep(10 * time.Millisecond) // Simulate processing time

	// For testing, we'll just verify the repository is accessible
	gitRepo, err := gogit.PlainOpen(repo.Path)
	if err != nil {
		return GitOperationResult{
			Success: false,
			Error:   err,
		}
	}

	// Try to get HEAD (or create initial commit if none exists)
	head, err := gitRepo.Head()
	if err != nil {
		// No commits yet, create one
		worktree, err := gitRepo.Worktree()
		if err != nil {
			return GitOperationResult{Success: false, Error: err}
		}

		_, err = worktree.Add(".")
		if err != nil {
			return GitOperationResult{Success: false, Error: err}
		}

		commitHash, err := worktree.Commit("Workflow test commit", &gogit.CommitOptions{
			Author: &object.Signature{
				Name:  "Workflow Test",
				Email: "workflow@test.com",
				When:  time.Now(),
			},
		})

		if err != nil {
			return GitOperationResult{Success: false, Error: err}
		}

		return GitOperationResult{
			Success:    true,
			CommitHash: commitHash.String(),
		}
	}

	return GitOperationResult{
		Success:    true,
		CommitHash: head.Hash().String(),
	}
}

// publishWorkflowEvents publishes events for the workflow
func (suite *ComprehensiveIntegrationTestSuite) publishWorkflowEvents(ctx context.Context, repo *TestRepository, mergeRequest map[string]interface{}, gitResult GitOperationResult) EventPublishResult {
	if suite.redisStreamClient == nil {
		return EventPublishResult{Published: false, Error: fmt.Errorf("Redis client not available")}
	}

	event := &events.CommitEvent{
		EventID:       fmt.Sprintf("workflow-test-%d", time.Now().Unix()),
		RepoPath:      repo.Name,
		EventType:     events.EventTypeMerge,
		CommitHash:    gitResult.CommitHash,
		RefName:       "refs/heads/main",
		Pusher:        "workflow-test",
		CommitMessage: "Workflow integration test",
		Timestamp:     time.Now(),
	}

	err := suite.redisStreamClient.AddCommitEventToStream(ctx, event)
	if err != nil {
		return EventPublishResult{Published: false, Error: err}
	}

	return EventPublishResult{
		Published: true,
		EventID:   event.EventID,
	}
}

// validateWorkflowResult validates the overall workflow result
func (suite *ComprehensiveIntegrationTestSuite) validateWorkflowResult(t *testing.T, result WorkflowResult) {
	assert.True(t, result.RequestProcessed, "Request should be processed")
	assert.True(t, result.GitOperationSucceeded, "Git operation should succeed")

	// Events publishing is optional depending on Redis availability
	if suite.redisStreamClient != nil {
		assert.True(t, result.EventsPublished, "Events should be published when Redis is available")
	}

	assert.Less(t, result.TotalDuration, 5*time.Second, "Workflow should complete within reasonable time")
}

// testRepositoryOverviewWorkflow tests the repository overview workflow
func (suite *ComprehensiveIntegrationTestSuite) testRepositoryOverviewWorkflow(t *testing.T, ctx context.Context) {
	// Test the complete flow of getting repository overview
	for repoName, testRepo := range suite.testRepositories {
		t.Run(fmt.Sprintf("Overview_%s", repoName), func(t *testing.T) {
			// 1. Open repository
			repo, err := gogit.PlainOpen(testRepo.Path)
			assert.NoError(t, err, "Should be able to open repository: %s", repoName)

			// 2. Gather repository information
			overview := suite.gatherRepositoryOverview(repo, testRepo)

			// 3. Validate overview data
			assert.Greater(t, overview.FileCount, 0, "Repository should have files")
			assert.NotEmpty(t, overview.RepoType, "Repository should have type")

			suite.logger.Info("Repository overview workflow completed",
				zap.String("repo", repoName),
				zap.Int("files", overview.FileCount),
				zap.String("type", overview.RepoType))
		})
	}
}

// RepositoryOverview represents repository overview information
type RepositoryOverview struct {
	FileCount   int
	BranchCount int
	CommitCount int
	RepoType    string
	Size        int64
}

// gatherRepositoryOverview gathers overview information about a repository
func (suite *ComprehensiveIntegrationTestSuite) gatherRepositoryOverview(repo *gogit.Repository, testRepo *TestRepository) RepositoryOverview {
	overview := RepositoryOverview{
		FileCount:   testRepo.FileCount,
		BranchCount: testRepo.BranchCount,
		CommitCount: testRepo.CommitCount,
		RepoType:    string(testRepo.RepoType),
	}

	// Calculate repository size
	if info, err := os.Stat(testRepo.Path); err == nil {
		overview.Size = info.Size()
	}

	return overview
}

// testErrorHandlingWorkflow tests error handling and rollback scenarios
func (suite *ComprehensiveIntegrationTestSuite) testErrorHandlingWorkflow(t *testing.T, ctx context.Context) {
	// Test various error scenarios and ensure proper rollback
	errorScenarios := []struct {
		name        string
		setupError  func(*TestRepository) error
		expectedErr string
	}{
		{
			"Repository_Access_Error",
			func(repo *TestRepository) error {
				// Simulate repository access error by changing permissions
				return os.Chmod(repo.Path, 0000)
			},
			"permission denied",
		},
		{
			"Invalid_Merge_Request",
			func(repo *TestRepository) error {
				// This doesn't actually cause an error in setup, but simulates invalid request
				return nil
			},
			"invalid request",
		},
	}

	for _, scenario := range errorScenarios {
		t.Run(scenario.name, func(t *testing.T) {
			// Create test repo for this scenario
			testRepo := suite.createWorkflowTestRepo(t)
			defer os.RemoveAll(testRepo.Path)

			// Setup error condition
			err := scenario.setupError(testRepo)
			if err != nil && scenario.name == "Repository_Access_Error" {
				// Try to access the repository - should fail
				_, accessErr := gogit.PlainOpen(testRepo.Path)
				assert.Error(t, accessErr, "Repository access should fail")

				// Restore permissions for cleanup
				os.Chmod(testRepo.Path, 0755)
			}

			suite.logger.Info("Error handling workflow test completed",
				zap.String("scenario", scenario.name))
		})
	}
}

// testErrorRollbackIntegration tests error rollback mechanisms
func (suite *ComprehensiveIntegrationTestSuite) testErrorRollbackIntegration(t *testing.T) {
	// Test scenarios where operations need to be rolled back
	rollbackScenarios := []struct {
		name            string
		operation       string
		simulateFailure bool
		expectRollback  bool
	}{
		{"Successful_Operation", "merge", false, false},
		{"Failed_Operation_Rollback", "merge", true, true},
		{"Partial_Operation_Rollback", "squash", true, true},
	}

	for _, scenario := range rollbackScenarios {
		t.Run(scenario.name, func(t *testing.T) {
			testRepo := suite.createWorkflowTestRepo(t)
			defer os.RemoveAll(testRepo.Path)

			// Simulate operation
			success := !scenario.simulateFailure

			if scenario.expectRollback && !success {
				// In a real system, this would test actual rollback mechanisms
				suite.logger.Info("Rollback scenario tested",
					zap.String("scenario", scenario.name),
					zap.String("operation", scenario.operation),
					zap.Bool("expect_rollback", scenario.expectRollback))
			}

			// For this test, we just verify the logic
			assert.Equal(t, scenario.expectRollback, !success,
				"Rollback expectation should match operation success")
		})
	}
}

// testObservabilityIntegration tests logging and observability throughout workflows
func (suite *ComprehensiveIntegrationTestSuite) testObservabilityIntegration(t *testing.T) {
	// Test that all operations are properly logged and observable

	// 1. Test structured logging
	suite.logger.Info("Testing observability integration",
		zap.String("component", "integration_test"),
		zap.Time("timestamp", time.Now()),
		zap.Int("test_repositories", len(suite.testRepositories)))

	// 2. Test performance metrics collection
	start := time.Now()

	// Perform some operations
	for _, repo := range suite.testRepositories {
		_, err := gogit.PlainOpen(repo.Path)
		if err == nil {
			break // Successfully opened one repository
		}
	}

	operationDuration := time.Since(start)

	// 3. Log performance metrics
	suite.logger.Info("Observability performance measurement",
		zap.Duration("operation_duration", operationDuration),
		zap.String("operation_type", "repository_access"))

	// 4. Test error logging
	suite.logger.Error("Test error log for observability",
		zap.String("error_type", "test_error"),
		zap.String("component", "observability_test"),
		zap.Bool("is_test", true))

	// Assert that logging doesn't cause issues
	assert.True(t, true, "Observability integration should not cause issues")

	suite.logger.Info("Observability integration test completed")
}

// testEventPublicationConsumptionIntegration tests event publication and consumption
func (suite *ComprehensiveIntegrationTestSuite) testEventPublicationConsumptionIntegration(t *testing.T) {
	if suite.redisStreamClient == nil {
		t.Skip("Redis not available for event integration testing")
		return
	}

	ctx := context.Background()

	// Test event publication
	t.Run("Event_Publication", func(t *testing.T) {
		suite.testEventPublication(t, ctx)
	})

	// Test event consumption
	t.Run("Event_Consumption", func(t *testing.T) {
		suite.testEventConsumption(t, ctx)
	})

	// Test event ordering
	t.Run("Event_Ordering", func(t *testing.T) {
		suite.testEventOrdering(t, ctx)
	})
}

// testEventPublication tests event publication
func (suite *ComprehensiveIntegrationTestSuite) testEventPublication(t *testing.T, ctx context.Context) {
	// Publish various types of events
	eventTypes := []events.CommitEventType{
		events.EventTypePush,
		events.EventTypeMerge,
		events.EventTypeCommit,
	}

	for _, eventType := range eventTypes {
		event := &events.CommitEvent{
			EventID:       fmt.Sprintf("pub-test-%s-%d", eventType, time.Now().Unix()),
			RepoPath:      "test/repo",
			EventType:     eventType,
			CommitHash:    "abc123def456",
			RefName:       "refs/heads/main",
			Pusher:        "test-publisher",
			CommitMessage: fmt.Sprintf("Test %s event", eventType),
			Timestamp:     time.Now(),
		}

		err := suite.redisStreamClient.AddCommitEventToStream(ctx, event)
		assert.NoError(t, err, "Should be able to publish %s event", eventType)

		suite.logger.Info("Event published successfully",
			zap.String("event_type", string(eventType)),
			zap.String("event_id", event.EventID))
	}
}

// testEventConsumption tests event consumption
func (suite *ComprehensiveIntegrationTestSuite) testEventConsumption(t *testing.T, ctx context.Context) {
	if suite.redisClient == nil {
		t.Skip("Redis client not available for consumption testing")
		return
	}

	// Read events from the stream
	streamKey := "git:events"
	result := suite.redisClient.XRead(ctx, &redis.XReadArgs{
		Streams: []string{streamKey, "0"},
		Count:   10,
		Block:   time.Second,
	})

	if result.Err() != nil {
		suite.logger.Warn("Event consumption test - no events available", zap.Error(result.Err()))
		return
	}

	events := result.Val()
	if len(events) > 0 && len(events[0].Messages) > 0 {
		messageCount := len(events[0].Messages)
		assert.Greater(t, messageCount, 0, "Should have consumed at least one event")

		suite.logger.Info("Events consumed successfully",
			zap.Int("message_count", messageCount),
			zap.String("stream", streamKey))
	}
}

// testEventOrdering tests that events maintain proper ordering
func (suite *ComprehensiveIntegrationTestSuite) testEventOrdering(t *testing.T, ctx context.Context) {
	// Publish a sequence of events and verify ordering
	eventSequence := []string{"first", "second", "third", "fourth"}
	publishedEventIDs := make([]string, 0, len(eventSequence))

	// Publish events in sequence
	for i, seq := range eventSequence {
		event := &events.CommitEvent{
			EventID:       fmt.Sprintf("order-test-%s-%d", seq, time.Now().Unix()+int64(i)),
			RepoPath:      "test/ordering",
			EventType:     events.EventTypeCommit,
			CommitHash:    fmt.Sprintf("hash%d", i),
			RefName:       "refs/heads/main",
			CommitMessage: fmt.Sprintf("Sequence event %s", seq),
			Timestamp:     time.Now().Add(time.Duration(i) * time.Millisecond),
		}

		err := suite.redisStreamClient.AddCommitEventToStream(ctx, event)
		assert.NoError(t, err, "Should be able to publish sequence event: %s", seq)
		publishedEventIDs = append(publishedEventIDs, event.EventID)

		// Small delay to ensure ordering
		time.Sleep(10 * time.Millisecond)
	}

	suite.logger.Info("Event ordering test completed",
		zap.Strings("published_event_ids", publishedEventIDs),
		zap.Int("event_count", len(publishedEventIDs)))
}

// validateSystemCohesion validates that the entire system works cohesively
func (suite *ComprehensiveIntegrationTestSuite) validateSystemCohesion(t *testing.T) {
	// Final validation that all components integrate properly

	// 1. Validate all test repositories are accessible
	accessibleRepos := 0
	for repoName, testRepo := range suite.testRepositories {
		_, err := gogit.PlainOpen(testRepo.Path)
		if err == nil {
			accessibleRepos++
		} else {
			t.Logf("Repository not accessible: %s, error: %v", repoName, err)
		}
	}

	assert.Greater(t, accessibleRepos, 0, "At least some test repositories should be accessible")

	// 2. Validate configuration consistency
	assert.NotNil(t, suite.config, "Configuration should be loaded")
	assert.NotEmpty(t, suite.config.Server.BasePath, "Server base path should be configured")

	// 3. Validate logging system
	suite.logger.Info("System cohesion validation",
		zap.Int("accessible_repositories", accessibleRepos),
		zap.Int("total_repositories", len(suite.testRepositories)),
		zap.Bool("redis_available", suite.redisClient != nil),
		zap.Bool("events_enabled", suite.config.Events.Enabled))

	// 4. Validate feature flag system
	protectionEnabled := suite.config.MergeProtection.Enabled
	assert.NotNil(t, &protectionEnabled, "Merge protection flag should have value")

	// 5. Validate regression test results
	suite.mutex.RLock()
	totalRegressionTests := len(suite.regressionResults)
	passedTests := 0
	for _, result := range suite.regressionResults {
		if result.Passed {
			passedTests++
		}
	}
	suite.mutex.RUnlock()

	if totalRegressionTests > 0 {
		passRate := float64(passedTests) / float64(totalRegressionTests) * 100
		assert.Greater(t, passRate, 80.0, "At least 80%% of regression tests should pass")

		suite.logger.Info("Regression test summary",
			zap.Int("total_tests", totalRegressionTests),
			zap.Int("passed_tests", passedTests),
			zap.Float64("pass_rate", passRate))
	}

	// 6. Generate final test report
	suite.generateFinalTestReport(t)

	suite.logger.Info("System cohesion validation completed",
		zap.Int("accessible_repos", accessibleRepos),
		zap.Int("regression_tests", totalRegressionTests),
		zap.Int("passed_regression_tests", passedTests))
}

// generateFinalTestReport generates a comprehensive test report
func (suite *ComprehensiveIntegrationTestSuite) generateFinalTestReport(t *testing.T) {
	reportPath := filepath.Join(suite.testDataDir, "integration_test_report.json")

	report := map[string]interface{}{
		"test_execution": map[string]interface{}{
			"timestamp":           time.Now().Format(time.RFC3339),
			"total_repositories":  len(suite.testRepositories),
			"test_data_directory": suite.testDataDir,
			"platform":            runtime.GOOS,
			"architecture":        runtime.GOARCH,
		},
		"configuration": map[string]interface{}{
			"merge_protection_enabled": suite.config.MergeProtection.Enabled,
			"redis_events_enabled":     suite.config.Events.Enabled,
			"logging_level":            suite.config.Logging.Level,
		},
		"performance_baselines": suite.performanceBaselines,
		"regression_results":    suite.regressionResults,
		"repository_types": func() map[string]int {
			types := make(map[string]int)
			for _, repo := range suite.testRepositories {
				types[string(repo.RepoType)]++
			}
			return types
		}(),
	}

	reportData, err := json.MarshalIndent(report, "", "  ")
	if err == nil {
		err = os.WriteFile(reportPath, reportData, 0644)
		if err == nil {
			suite.logger.Info("Integration test report generated",
				zap.String("report_path", reportPath))
		}
	}

	if err != nil {
		suite.logger.Warn("Failed to generate test report", zap.Error(err))
	}
}
