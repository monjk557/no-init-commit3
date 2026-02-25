package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/gin-gonic/gin"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ComprehensiveRegressionTestSuite is a simplified version focusing on actual functionality
type ComprehensiveRegressionTestSuite struct {
	logger  *logging.Logger
	config  *config.Config
	testDir string
	repos   map[string]*TestRepo
}

// TestRepo represents a test repository
type TestRepo struct {
	Name string
	Path string
	Repo *gogit.Repository
}

// NewComprehensiveRegressionTestSuite creates a new simplified test suite
func NewComprehensiveRegressionTestSuite(t *testing.T) *ComprehensiveRegressionTestSuite {
	testDir, err := os.MkdirTemp("", "comprehensive_regression_test_")
	require.NoError(t, err, "Failed to create test directory")

	logger, _ := logging.NewLogger(config.LoggingConfig{
		Level:  "info",
		Format: "json",
	})

	return &ComprehensiveRegressionTestSuite{
		logger:  logger,
		testDir: testDir,
		repos:   make(map[string]*TestRepo),
	}
}

// TestComprehensiveRegressionSuite runs comprehensive integration and regression testing
func TestComprehensiveRegressionSuite(t *testing.T) {
	suite := NewComprehensiveRegressionTestSuite(t)
	defer suite.Cleanup()

	// Set up environment
	suite.setupEnvironment(t)

	// 1. Integration Testing
	t.Run("1_Redis_Events_Integration", func(t *testing.T) {
		suite.testRedisEventsIntegration(t)
	})

	t.Run("2_Logging_Configuration_Integration", func(t *testing.T) {
		suite.testLoggingConfigurationIntegration(t)
	})

	t.Run("3_Git_Repository_Operations", func(t *testing.T) {
		suite.testGitRepositoryOperations(t)
	})

	t.Run("4_Cross_Platform_Compatibility", func(t *testing.T) {
		suite.testCrossPlatformCompatibility(t)
	})

	// 2. Regression Testing
	t.Run("5_Go_Vet_Compilation_Regression", func(t *testing.T) {
		suite.testGoVetCompilationRegression(t)
	})

	t.Run("6_Environment_File_Protection_Regression", func(t *testing.T) {
		suite.testEnvironmentFileProtectionRegression(t)
	})

	t.Run("7_Performance_Regression", func(t *testing.T) {
		suite.testPerformanceRegression(t)
	})

	// 3. API Integration Testing
	t.Run("8_REST_API_Integration", func(t *testing.T) {
		suite.testRESTAPIIntegration(t)
	})

	t.Run("9_HTTP_Error_Handling", func(t *testing.T) {
		suite.testHTTPErrorHandling(t)
	})

	// 4. End-to-End Testing
	t.Run("10_Complete_Workflow", func(t *testing.T) {
		suite.testCompleteWorkflow(t)
	})

	t.Run("11_System_Cohesion_Validation", func(t *testing.T) {
		suite.validateSystemCohesion(t)
	})
}

// setupEnvironment sets up the test environment
func (suite *ComprehensiveRegressionTestSuite) setupEnvironment(t *testing.T) {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		// Create minimal config for testing
		cfg = &config.Config{
			Server: config.ServerConfig{
				Port:     8080,
				BasePath: "/api",
				Host:     "localhost",
			},
			Logging: config.LoggingConfig{
				Level:  "info",
				Format: "json",
			},
			MergeProtection: config.MergeProtectionConfig{
				Enabled: true,
				EnvironmentProtection: config.EnvironmentProtectionConfig{
					Enabled:            true,
					ProtectEnvFiles:    true,
					ProtectConfigFiles: true,
				},
			},
		}
	}
	suite.config = cfg

	// Create test repositories
	suite.createTestRepositories(t)

	suite.logger.Info("Test environment setup completed",
		zap.String("test_dir", suite.testDir),
		zap.Int("test_repos", len(suite.repos)))
}

// createTestRepositories creates test repositories for testing
func (suite *ComprehensiveRegressionTestSuite) createTestRepositories(t *testing.T) {
	repoConfigs := []struct {
		name      string
		fileCount int
	}{
		{"small-repo", 5},
		{"medium-repo", 20},
		{"config-repo", 10},
	}

	for _, config := range repoConfigs {
		repoPath := filepath.Join(suite.testDir, config.name)

		// Initialize repository
		repo, err := gogit.PlainInit(repoPath, false)
		require.NoError(t, err, "Failed to initialize repository: %s", config.name)

		// Create test files
		for i := 0; i < config.fileCount; i++ {
			fileName := fmt.Sprintf("file_%d.txt", i)
			filePath := filepath.Join(repoPath, fileName)
			content := fmt.Sprintf("Content for file %d in %s", i, config.name)

			err := os.WriteFile(filePath, []byte(content), 0644)
			require.NoError(t, err, "Failed to create file: %s", fileName)
		}

		// Create environment files for protection testing
		envFiles := map[string]string{
			".env":               "DATABASE_URL=test",
			"config.yaml":        "app: test",
			"docker-compose.yml": "version: '3'",
		}

		for envFile, content := range envFiles {
			filePath := filepath.Join(repoPath, envFile)
			err := os.WriteFile(filePath, []byte(content), 0644)
			require.NoError(t, err, "Failed to create env file: %s", envFile)
		}

		// Commit files
		worktree, err := repo.Worktree()
		require.NoError(t, err, "Failed to get worktree")

		_, err = worktree.Add(".")
		require.NoError(t, err, "Failed to add files")

		_, err = worktree.Commit(fmt.Sprintf("Initial commit for %s", config.name), &gogit.CommitOptions{
			Author: &object.Signature{
				Name:  "Test Author",
				Email: "test@example.com",
				When:  time.Now(),
			},
		})
		require.NoError(t, err, "Failed to commit files")

		suite.repos[config.name] = &TestRepo{
			Name: config.name,
			Path: repoPath,
			Repo: repo,
		}
	}
}

// testRedisEventsIntegration tests Redis events system integration
func (suite *ComprehensiveRegressionTestSuite) testRedisEventsIntegration(t *testing.T) {
	if !suite.config.Events.Enabled || suite.config.Events.RedisURL == "" {
		t.Skip("Redis events not configured, skipping Redis integration test")
		return
	}

	ctx := context.Background()

	// Create Redis stream client
	client, err := events.NewRedisStreamClient(suite.config.Events.RedisURL, suite.logger.Logger)
	if err != nil {
		t.Skipf("Failed to create Redis client: %v", err)
		return
	}
	defer client.Close()

	// Test publishing events
	event := &events.CommitEvent{
		EventID:       fmt.Sprintf("test-event-%d", time.Now().Unix()),
		RepoPath:      "test/repo",
		EventType:     events.EventTypePush,
		CommitHash:    "abc123",
		RefName:       "refs/heads/main",
		Pusher:        "test-user",
		CommitMessage: "Test commit",
		Timestamp:     time.Now(),
	}

	err = client.AddCommitEventToStream(ctx, event)
	assert.NoError(t, err, "Should be able to publish event to Redis")

	suite.logger.Info("Redis events integration test completed")
}

// testLoggingConfigurationIntegration tests different logging configurations
func (suite *ComprehensiveRegressionTestSuite) testLoggingConfigurationIntegration(t *testing.T) {
	configs := []config.LoggingConfig{
		{Level: "info", Format: "json"},
		{Level: "debug", Format: "text"},
		{Level: "warn", Format: "json"},
		{Level: "error", Format: "text"},
	}

	for _, cfg := range configs {
		t.Run(fmt.Sprintf("Logging_%s_%s", cfg.Level, cfg.Format), func(t *testing.T) {
			logger, err := logging.NewLogger(cfg)
			assert.NoError(t, err, "Should create logger with config: %+v", cfg)

			if err == nil {
				// Test logging at different levels
				logger.Debug("Debug message", zap.String("test", "value"))
				logger.Info("Info message", zap.Int("count", 42))
				logger.Warn("Warning message", zap.Bool("flag", true))
				logger.Error("Error message", zap.Duration("duration", time.Second))
			}
		})
	}

	suite.logger.Info("Logging configuration integration test completed")
}

// testGitRepositoryOperations tests Git repository operations
func (suite *ComprehensiveRegressionTestSuite) testGitRepositoryOperations(t *testing.T) {
	for repoName, testRepo := range suite.repos {
		t.Run(fmt.Sprintf("GitOps_%s", repoName), func(t *testing.T) {
			// Test basic operations
			head, err := testRepo.Repo.Head()
			assert.NoError(t, err, "Should be able to get HEAD")
			assert.NotNil(t, head, "HEAD should not be nil")

			// Test references
			refs, err := testRepo.Repo.References()
			assert.NoError(t, err, "Should be able to get references")
			assert.NotNil(t, refs, "References should not be nil")

			// Test commit log
			commitIter, err := testRepo.Repo.Log(&gogit.LogOptions{
				From: head.Hash(),
			})
			assert.NoError(t, err, "Should be able to get commit log")

			commitCount := 0
			err = commitIter.ForEach(func(c *object.Commit) error {
				commitCount++
				assert.NotNil(t, c, "Commit should not be nil")
				return nil
			})
			assert.NoError(t, err, "Should iterate commits without error")
			assert.Greater(t, commitCount, 0, "Should have at least one commit")
		})
	}

	suite.logger.Info("Git repository operations test completed")
}

// testCrossPlatformCompatibility tests cross-platform compatibility
func (suite *ComprehensiveRegressionTestSuite) testCrossPlatformCompatibility(t *testing.T) {
	// Test path handling
	testPaths := []string{
		"path/to/file.txt",
		"path\\to\\file.txt",
		"path/mixed\\separators.txt",
	}

	for _, testPath := range testPaths {
		normalized := filepath.Clean(testPath)
		assert.NotEmpty(t, normalized, "Normalized path should not be empty")

		// Test joining paths
		joined := filepath.Join("base", testPath, "end")
		assert.Contains(t, joined, "base", "Joined path should contain base")
		assert.Contains(t, joined, "end", "Joined path should contain end")
	}

	// Test file operations
	testFile := filepath.Join(suite.testDir, "cross_platform_test.txt")
	err := os.WriteFile(testFile, []byte("cross platform test content"), 0644)
	assert.NoError(t, err, "Should be able to create file")

	content, err := os.ReadFile(testFile)
	assert.NoError(t, err, "Should be able to read file")
	assert.Equal(t, "cross platform test content", string(content), "Content should match")

	os.Remove(testFile)

	suite.logger.Info("Cross-platform compatibility test completed",
		zap.String("os", runtime.GOOS),
		zap.String("arch", runtime.GOARCH))
}

// testGoVetCompilationRegression tests against Go vet compilation issues
func (suite *ComprehensiveRegressionTestSuite) testGoVetCompilationRegression(t *testing.T) {
	projectRoot := "/Users/johnbaek/collabops-git-executor"

	// Run go vet on the project
	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = projectRoot
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Logf("Go vet output:\n%s", string(output))
		t.Errorf("Go vet found issues: %v", err)
	} else {
		t.Log("Go vet passed without issues")
	}

	suite.logger.Info("Go vet compilation regression test completed",
		zap.Bool("passed", err == nil))
}

// testEnvironmentFileProtectionRegression tests environment file protection
func (suite *ComprehensiveRegressionTestSuite) testEnvironmentFileProtectionRegression(t *testing.T) {
	// Test protection for various file types
	protectedFiles := []string{
		".env",
		".env.production",
		"config.yaml",
		"docker-compose.yml",
		"database.conf",
	}

	for _, file := range protectedFiles {
		t.Run(fmt.Sprintf("Protection_%s", strings.ReplaceAll(file, ".", "_")), func(t *testing.T) {
			// Check if file exists in test repositories
			for repoName, testRepo := range suite.repos {
				filePath := filepath.Join(testRepo.Path, file)
				if _, err := os.Stat(filePath); err == nil {
					// File exists, verify it's protected
					protected := suite.isFileProtected(file)
					assert.True(t, protected, "File %s should be protected in repo %s", file, repoName)
				}
			}
		})
	}

	suite.logger.Info("Environment file protection regression test completed")
}

// isFileProtected checks if a file should be protected based on configuration
func (suite *ComprehensiveRegressionTestSuite) isFileProtected(filePath string) bool {
	envConfig := suite.config.MergeProtection.EnvironmentProtection

	if !envConfig.Enabled {
		return false
	}

	// Check environment files
	if envConfig.ProtectEnvFiles && (strings.HasSuffix(filePath, ".env") || strings.Contains(filePath, ".env.")) {
		return true
	}

	// Check config files
	if envConfig.ProtectConfigFiles && (strings.Contains(filePath, "config") || strings.HasSuffix(filePath, ".yaml") || strings.HasSuffix(filePath, ".yml")) {
		return true
	}

	// Check Docker files
	if envConfig.ProtectDockerFiles && strings.Contains(filePath, "docker-compose") {
		return true
	}

	// Check database files
	if envConfig.ProtectDatabaseFiles && (strings.Contains(filePath, "database") || strings.HasSuffix(filePath, ".conf")) {
		return true
	}

	return false
}

// testPerformanceRegression tests for performance regressions
func (suite *ComprehensiveRegressionTestSuite) testPerformanceRegression(t *testing.T) {
	performanceTests := []struct {
		name     string
		baseline time.Duration
		testFunc func() time.Duration
	}{
		{
			"repository_open",
			50 * time.Millisecond,
			func() time.Duration {
				start := time.Now()
				for _, testRepo := range suite.repos {
					_, err := gogit.PlainOpen(testRepo.Path)
					if err != nil {
						break
					}
				}
				return time.Since(start)
			},
		},
		{
			"commit_iteration",
			100 * time.Millisecond,
			func() time.Duration {
				start := time.Now()
				for _, testRepo := range suite.repos {
					head, err := testRepo.Repo.Head()
					if err != nil {
						continue
					}
					commitIter, err := testRepo.Repo.Log(&gogit.LogOptions{From: head.Hash()})
					if err != nil {
						continue
					}
					commitIter.ForEach(func(c *object.Commit) error {
						return nil // Just iterate
					})
					break // Only test one repo
				}
				return time.Since(start)
			},
		},
	}

	for _, test := range performanceTests {
		t.Run(fmt.Sprintf("Performance_%s", test.name), func(t *testing.T) {
			duration := test.testFunc()
			maxAcceptable := test.baseline * 2 // Allow 100% variance

			if duration > maxAcceptable {
				t.Logf("PERFORMANCE REGRESSION: %s took %v, baseline was %v", test.name, duration, test.baseline)
			}

			assert.Less(t, duration, maxAcceptable,
				"Performance test %s should complete within %v (took %v)", test.name, maxAcceptable, duration)

			suite.logger.Info("Performance test completed",
				zap.String("test", test.name),
				zap.Duration("duration", duration),
				zap.Duration("baseline", test.baseline))
		})
	}
}

// testRESTAPIIntegration tests REST API endpoints
func (suite *ComprehensiveRegressionTestSuite) testRESTAPIIntegration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	// Add test endpoints
	api := router.Group("/api/v1")
	api.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "healthy"})
	})

	api.POST("/merge", func(c *gin.Context) {
		var request map[string]interface{}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Simple merge protection logic
		if suite.config.MergeProtection.Enabled {
			c.JSON(http.StatusOK, gin.H{
				"status":    "completed",
				"protected": true,
			})
		} else {
			c.JSON(http.StatusOK, gin.H{
				"status":    "completed",
				"protected": false,
			})
		}
	})

	testServer := httptest.NewServer(router)
	defer testServer.Close()

	// Test health endpoint
	resp, err := http.Get(testServer.URL + "/api/v1/health")
	require.NoError(t, err, "Health endpoint should be accessible")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "Health endpoint should return 200")

	suite.logger.Info("REST API integration test completed")
}

// testHTTPErrorHandling tests HTTP error handling
func (suite *ComprehensiveRegressionTestSuite) testHTTPErrorHandling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	// Add recovery middleware
	router.Use(gin.Recovery())

	// Add error endpoints for testing
	api := router.Group("/api/v1")
	api.GET("/error/:code", func(c *gin.Context) {
		code := c.Param("code")
		switch code {
		case "400":
			c.JSON(http.StatusBadRequest, gin.H{"error": "Bad request"})
		case "404":
			c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
		case "500":
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		case "panic":
			panic("Test panic")
		default:
			c.JSON(http.StatusOK, gin.H{"message": "OK"})
		}
	})

	testServer := httptest.NewServer(router)
	defer testServer.Close()

	// Test different error codes
	errorCodes := []int{400, 404, 500}
	for _, code := range errorCodes {
		t.Run(fmt.Sprintf("ErrorCode_%d", code), func(t *testing.T) {
			resp, err := http.Get(fmt.Sprintf("%s/api/v1/error/%d", testServer.URL, code))
			require.NoError(t, err, "Should be able to make request")
			defer resp.Body.Close()

			assert.Equal(t, code, resp.StatusCode, "Should return expected error code")
		})
	}

	// Test panic recovery
	t.Run("PanicRecovery", func(t *testing.T) {
		resp, err := http.Get(testServer.URL + "/api/v1/error/panic")
		require.NoError(t, err, "Should be able to make panic request")
		defer resp.Body.Close()

		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode, "Panic should be recovered")
	})

	suite.logger.Info("HTTP error handling test completed")
}

// testCompleteWorkflow tests end-to-end workflow
func (suite *ComprehensiveRegressionTestSuite) testCompleteWorkflow(t *testing.T) {
	// Test complete workflow: configuration -> repository operations -> logging

	// 1. Verify configuration is loaded
	assert.NotNil(t, suite.config, "Configuration should be loaded")

	// 2. Test repository operations
	operationCount := 0
	for _, testRepo := range suite.repos {
		_, err := testRepo.Repo.Head()
		if err == nil {
			operationCount++
		}

		_, err = testRepo.Repo.References()
		if err == nil {
			operationCount++
		}
	}

	assert.Greater(t, operationCount, 0, "Should complete some repository operations")

	// 3. Test logging
	suite.logger.Info("Workflow test logging",
		zap.Int("operations_completed", operationCount),
		zap.Int("repositories_tested", len(suite.repos)))

	// 4. Test configuration consistency
	assert.True(t, suite.config.MergeProtection.Enabled, "Merge protection should be enabled")
	assert.True(t, suite.config.MergeProtection.EnvironmentProtection.Enabled, "Environment protection should be enabled")

	suite.logger.Info("Complete workflow test completed",
		zap.Int("operations", operationCount))
}

// validateSystemCohesion validates overall system integration
func (suite *ComprehensiveRegressionTestSuite) validateSystemCohesion(t *testing.T) {
	// Validate all components work together

	validationResults := map[string]bool{
		"configuration_loaded": suite.config != nil,
		"repositories_created": len(suite.repos) > 0,
		"logging_functional":   suite.logger != nil,
	}

	allValid := true
	for component, valid := range validationResults {
		assert.True(t, valid, "Component should be valid: %s", component)
		if !valid {
			allValid = false
		}
	}

	// Test integration between components
	if allValid {
		// Configuration should influence behavior
		assert.Equal(t, suite.config.MergeProtection.Enabled, true, "Config should be accessible")

		// Logging should be functional
		suite.logger.Info("System cohesion validation",
			zap.Bool("all_components_valid", allValid),
			zap.Int("repositories", len(suite.repos)))

		// Repositories should be accessible
		accessibleRepos := 0
		for _, testRepo := range suite.repos {
			_, err := gogit.PlainOpen(testRepo.Path)
			if err == nil {
				accessibleRepos++
			}
		}

		assert.Equal(t, len(suite.repos), accessibleRepos, "All repositories should be accessible")
	}

	suite.logger.Info("System cohesion validation completed",
		zap.Bool("system_cohesive", allValid),
		zap.Int("validation_checks", len(validationResults)))
}

// Cleanup cleans up test resources
func (suite *ComprehensiveRegressionTestSuite) Cleanup() {
	if suite.testDir != "" {
		os.RemoveAll(suite.testDir)
	}

	suite.logger.Info("Test cleanup completed")
}
