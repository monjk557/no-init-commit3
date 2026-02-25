package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/server"
	"github.com/gin-gonic/gin"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ComprehensiveIntegrationTestSuite represents the complete integration test suite
type ComprehensiveIntegrationTestSuite struct {
	logger               *logging.Logger
	config               *config.Config
	httpServer           *server.Server
	tcpServer            *server.DuplexTCPServer
	redisClient          redis.UniversalClient
	redisStreamClient    events.RedisStreamClient
	testRepositories     map[string]*TestRepository
	environmentVariables map[string]string

	// Test fixtures
	testDataDir string

	// Test state tracking
	performanceBaselines map[string]time.Duration
	regressionResults    []RegressionTestResult
	mutex                sync.RWMutex
}

// TestRepository represents a test repository with specific characteristics
type TestRepository struct {
	Name          string
	Path          string
	RepoType      RepositoryType
	GitRepo       *gogit.Repository
	HasSubmodules bool
	FileCount     int
	BranchCount   int
	CommitCount   int
}

// RepositoryType categorizes different repository formats for testing
type RepositoryType string

const (
	RepoTypeNormal     RepositoryType = "normal"
	RepoTypeBare       RepositoryType = "bare"
	RepoTypeSubmodules RepositoryType = "submodules"
)

// RegressionTestResult tracks regression test outcomes
type RegressionTestResult struct {
	TestName     string
	Component    string
	Expected     interface{}
	Actual       interface{}
	Passed       bool
	ErrorMessage string
	Timestamp    time.Time
}

// NewComprehensiveIntegrationTestSuite creates a new test suite
func NewComprehensiveIntegrationTestSuite(t *testing.T) *ComprehensiveIntegrationTestSuite {
	// Set up test data directory
	testDataDir, err := os.MkdirTemp("", "comprehensive_integration_test_")
	require.NoError(t, err, "Failed to create test data directory")

	// Initialize logger
	logger := logging.NewLogger(zap.DebugLevel)

	suite := &ComprehensiveIntegrationTestSuite{
		logger:               logger,
		testRepositories:     make(map[string]*TestRepository),
		environmentVariables: make(map[string]string),
		testDataDir:          testDataDir,
		performanceBaselines: make(map[string]time.Duration),
		regressionResults:    make([]RegressionTestResult, 0),
	}

	return suite
}

// TestComprehensiveIntegration runs the complete comprehensive integration test suite
func TestComprehensiveIntegration(t *testing.T) {
	suite := NewComprehensiveIntegrationTestSuite(t)
	defer suite.Cleanup()

	// 1. Integration Testing
	t.Run("1_Setup_Test_Environment", func(t *testing.T) {
		suite.setupTestEnvironment(t)
	})

	t.Run("2_Redis_Events_System_Integration", func(t *testing.T) {
		suite.testRedisEventsSystemIntegration(t)
	})

	t.Run("3_Logging_Configuration_Integration", func(t *testing.T) {
		suite.testLoggingConfigurationIntegration(t)
	})

	t.Run("4_Git_Repository_Format_Integration", func(t *testing.T) {
		suite.testGitRepositoryFormatIntegration(t)
	})

	t.Run("5_Orchestrator_System_Integration", func(t *testing.T) {
		suite.testOrchestratorSystemIntegration(t)
	})

	t.Run("6_External_API_Timeout_Integration", func(t *testing.T) {
		suite.testExternalAPITimeoutIntegration(t)
	})

	// 2. Regression Testing Against Historical Issues
	t.Run("7_Go_Vet_Compilation_Regression", func(t *testing.T) {
		suite.testGoVetCompilationRegression(t)
	})

	t.Run("8_Environment_File_Deletion_Bug_Regression", func(t *testing.T) {
		suite.testEnvironmentFileDeletionBugRegression(t)
	})

	t.Run("9_User_Scenarios_Regression", func(t *testing.T) {
		suite.testUserScenariosRegression(t)
	})

	t.Run("10_Performance_Regression", func(t *testing.T) {
		suite.testPerformanceRegression(t)
	})

	// 3. Cross-Platform Compatibility
	t.Run("11_Path_Separator_Handling", func(t *testing.T) {
		suite.testPathSeparatorHandling(t)
	})

	t.Run("12_File_System_Compatibility", func(t *testing.T) {
		suite.testFileSystemCompatibility(t)
	})

	// 4. API Integration Testing
	t.Run("13_REST_API_Protection_Integration", func(t *testing.T) {
		suite.testRESTAPIProtectionIntegration(t)
	})

	t.Run("14_HTTP_Error_Handling_Integration", func(t *testing.T) {
		suite.testHTTPErrorHandlingIntegration(t)
	})

	t.Run("15_Feature_Flag_Validation_Integration", func(t *testing.T) {
		suite.testFeatureFlagValidationIntegration(t)
	})

	// 5. End-to-End Workflow Testing
	t.Run("16_Complete_Workflow_Integration", func(t *testing.T) {
		suite.testCompleteWorkflowIntegration(t)
	})

	t.Run("17_Error_Rollback_Integration", func(t *testing.T) {
		suite.testErrorRollbackIntegration(t)
	})

	t.Run("18_Observability_Integration", func(t *testing.T) {
		suite.testObservabilityIntegration(t)
	})

	t.Run("19_Event_Publication_Consumption_Integration", func(t *testing.T) {
		suite.testEventPublicationConsumptionIntegration(t)
	})

	// Final validation
	t.Run("20_System_Cohesion_Validation", func(t *testing.T) {
		suite.validateSystemCohesion(t)
	})
}

// setupTestEnvironment prepares the test environment with various configurations
func (suite *ComprehensiveIntegrationTestSuite) setupTestEnvironment(t *testing.T) {
	// Save original environment variables
	originalEnvVars := []string{
		"SERVER_PORT", "SERVER_TCP_GIT_PORT", "SERVER_BASE_PATH", "GIT_REPO_ROOT",
		"REDIS_URL", "MERGE_PROTECTION_ENABLED", "ORCHESTRATOR_BASE_URL",
	}

	for _, envVar := range originalEnvVars {
		suite.environmentVariables[envVar] = os.Getenv(envVar)
	}

	// Set up test repositories with different formats
	suite.createTestRepositories(t)

	// Configure test-specific environment variables
	testRepoRoot := filepath.Join(suite.testDataDir, "repositories")
	err := os.MkdirAll(testRepoRoot, 0755)
	require.NoError(t, err, "Failed to create test repository root")

	// Set test environment variables
	os.Setenv("SERVER_PORT", "0")         // Use random port
	os.Setenv("SERVER_TCP_GIT_PORT", "0") // Use random port
	os.Setenv("SERVER_BASE_PATH", "/test-api")
	os.Setenv("GIT_REPO_ROOT", testRepoRoot)
	os.Setenv("REDIS_URL", "redis://localhost:6379/15") // Use test database
	os.Setenv("MERGE_PROTECTION_ENABLED", "true")
	os.Setenv("ORCHESTRATOR_BASE_URL", "http://localhost:8080")

	// Load configuration with test settings
	cfg, err := config.Load()
	require.NoError(t, err, "Failed to load test configuration")
	suite.config = cfg

	suite.logger.Info("Test environment setup completed",
		zap.String("test_data_dir", suite.testDataDir),
		zap.String("repo_root", testRepoRoot),
		zap.Int("test_repositories", len(suite.testRepositories)))
}

// createTestRepositories creates test repositories of different types
func (suite *ComprehensiveIntegrationTestSuite) createTestRepositories(t *testing.T) {
	repoConfigs := []struct {
		name          string
		repoType      RepositoryType
		fileCount     int
		branches      []string
		hasSubmodules bool
	}{
		{"normal-small", RepoTypeNormal, 10, []string{"main", "develop"}, false},
		{"normal-medium", RepoTypeNormal, 50, []string{"main", "feature/test", "hotfix/bug"}, false},
		{"bare-repo", RepoTypeBare, 25, []string{"main"}, false},
		{"submodule-repo", RepoTypeSubmodules, 20, []string{"main", "feature/sub"}, true},
	}

	for _, config := range repoConfigs {
		testRepo := suite.createTestRepository(t, config.name, config.repoType,
			config.fileCount, config.branches, config.hasSubmodules)
		suite.testRepositories[config.name] = testRepo
	}
}

// createTestRepository creates a single test repository with specified characteristics
func (suite *ComprehensiveIntegrationTestSuite) createTestRepository(t *testing.T, name string,
	repoType RepositoryType, fileCount int, branches []string, hasSubmodules bool) *TestRepository {

	repoDir := filepath.Join(suite.testDataDir, "repositories", name)

	var repo *gogit.Repository
	var err error

	switch repoType {
	case RepoTypeBare:
		repo, err = gogit.PlainInit(repoDir, true)
	default:
		repo, err = gogit.PlainInit(repoDir, false)
	}
	require.NoError(t, err, "Failed to initialize repository: %s", name)

	// Create initial commit (only for non-bare repos)
	if repoType != RepoTypeBare {
		// Create test files
		for i := 0; i < fileCount; i++ {
			fileName := fmt.Sprintf("test_file_%d.txt", i)
			filePath := filepath.Join(repoDir, fileName)
			content := fmt.Sprintf("Content for test file %d in repository %s", i, name)

			err = os.WriteFile(filePath, []byte(content), 0644)
			require.NoError(t, err, "Failed to create test file: %s", fileName)
		}

		// Create config files for environment protection testing
		configFiles := []string{".env", "config.yaml", "docker-compose.yml", "database.conf"}
		for _, configFile := range configFiles {
			filePath := filepath.Join(repoDir, configFile)
			content := fmt.Sprintf("# Configuration file: %s\ntest_value=example", configFile)
			err = os.WriteFile(filePath, []byte(content), 0644)
			require.NoError(t, err, "Failed to create config file: %s", configFile)
		}

		// Stage and commit files
		worktree, err := repo.Worktree()
		require.NoError(t, err, "Failed to get worktree for repo: %s", name)

		_, err = worktree.Add(".")
		require.NoError(t, err, "Failed to stage files for repo: %s", name)

		_, err = worktree.Commit("Initial commit for "+name, &gogit.CommitOptions{
			Author: &object.Signature{
				Name:  "Test Author",
				Email: "test@example.com",
				When:  time.Now(),
			},
		})
		require.NoError(t, err, "Failed to create initial commit for repo: %s", name)

		// Create additional branches
		for i, branchName := range branches {
			if i == 0 { // Skip main branch as it already exists
				continue
			}

			headRef, err := repo.Head()
			require.NoError(t, err, "Failed to get HEAD for branch creation")

			refName := fmt.Sprintf("refs/heads/%s", branchName)
			ref := gogit.NewHashReference(gogit.ReferenceName(refName), headRef.Hash())
			err = repo.Storer.SetReference(ref)
			require.NoError(t, err, "Failed to create branch: %s", branchName)
		}

		// Add submodules if required
		if hasSubmodules {
			suite.addSubmoduleToRepository(t, repo, repoDir)
		}
	}

	testRepo := &TestRepository{
		Name:          name,
		Path:          repoDir,
		RepoType:      repoType,
		GitRepo:       repo,
		HasSubmodules: hasSubmodules,
		FileCount:     fileCount + 4, // Include config files
		BranchCount:   len(branches),
		CommitCount:   1,
	}

	suite.logger.Info("Created test repository",
		zap.String("name", name),
		zap.String("type", string(repoType)),
		zap.String("path", repoDir),
		zap.Int("files", testRepo.FileCount),
		zap.Int("branches", testRepo.BranchCount),
		zap.Bool("has_submodules", hasSubmodules))

	return testRepo
}

// addSubmoduleToRepository adds a submodule to the given repository
func (suite *ComprehensiveIntegrationTestSuite) addSubmoduleToRepository(t *testing.T,
	repo *gogit.Repository, repoDir string) {

	// Create a simple submodule directory structure
	submoduleDir := filepath.Join(repoDir, "submodule")
	err := os.MkdirAll(submoduleDir, 0755)
	require.NoError(t, err, "Failed to create submodule directory")

	// Create a simple submodule file
	submoduleFile := filepath.Join(submoduleDir, "submodule_file.txt")
	err = os.WriteFile(submoduleFile, []byte("Submodule content"), 0644)
	require.NoError(t, err, "Failed to create submodule file")

	// Create .gitmodules file
	gitmodulesPath := filepath.Join(repoDir, ".gitmodules")
	gitmodulesContent := `[submodule "submodule"]
	path = submodule
	url = https://github.com/example/submodule.git`

	err = os.WriteFile(gitmodulesPath, []byte(gitmodulesContent), 0644)
	require.NoError(t, err, "Failed to create .gitmodules file")
}

// testRedisEventsSystemIntegration tests Redis events system integration
func (suite *ComprehensiveIntegrationTestSuite) testRedisEventsSystemIntegration(t *testing.T) {
	if suite.config.Events.RedisURL == "" {
		t.Skip("Redis URL not configured, skipping Redis integration tests")
		return
	}

	ctx := context.Background()

	// Create Redis client
	redisClient, err := suite.createRedisClient()
	require.NoError(t, err, "Failed to create Redis client")
	suite.redisClient = redisClient

	// Test Redis connection
	err = redisClient.Ping(ctx).Err()
	require.NoError(t, err, "Failed to connect to Redis")

	// Create Redis stream client
	streamClient, err := events.NewRedisStreamClient(suite.config.Events.RedisURL, suite.logger.GetZapLogger())
	require.NoError(t, err, "Failed to create Redis stream client")
	suite.redisStreamClient = streamClient

	// Test various event types
	eventTypes := []events.CommitEventType{
		events.EventTypePush,
		events.EventTypeMerge,
		events.EventTypeCommit,
		events.EventTypeDelete,
		events.EventTypeRename,
	}

	for _, eventType := range eventTypes {
		t.Run(fmt.Sprintf("EventType_%s", eventType), func(t *testing.T) {
			event := &events.CommitEvent{
				EventID:       fmt.Sprintf("test-%s-%d", eventType, time.Now().UnixNano()),
				RepoPath:      "test-org/test-repo.git",
				EventType:     eventType,
				CommitHash:    "abc123def456",
				OldRev:        "old123rev456",
				RefName:       "refs/heads/main",
				OldRefName:    "refs/heads/old-main",
				Pusher:        "test-user",
				UserID:        "user-123",
				CommitMessage: "Test commit message",
				Timestamp:     time.Now(),
			}

			err := streamClient.AddCommitEventToStream(ctx, event)
			assert.NoError(t, err, "Failed to publish %s event", eventType)

			// Verify event was published by reading from stream
			streamKey := "git:events"
			result := redisClient.XRead(ctx, &redis.XReadArgs{
				Streams: []string{streamKey, "0"},
				Count:   1,
				Block:   time.Second,
			})

			if result.Err() == nil && len(result.Val()) > 0 && len(result.Val()[0].Messages) > 0 {
				message := result.Val()[0].Messages[0]
				if data, ok := message.Values["data"].(string); ok {
					var eventData map[string]interface{}
					err := json.Unmarshal([]byte(data), &eventData)
					assert.NoError(t, err, "Failed to unmarshal event data")
					assert.Equal(t, string(eventType), eventData["event_type"], "Event type mismatch")
				}
			}
		})
	}

	suite.logger.Info("Redis events system integration test completed",
		zap.Int("event_types_tested", len(eventTypes)))
}

// createRedisClient creates a Redis client for testing
func (suite *ComprehensiveIntegrationTestSuite) createRedisClient() (redis.UniversalClient, error) {
	opts, err := redis.ParseURL(suite.config.Events.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Redis URL: %w", err)
	}

	client := redis.NewClient(opts)
	return client, nil
}

// Cleanup cleans up test resources
func (suite *ComprehensiveIntegrationTestSuite) Cleanup() {
	// Close Redis connections
	if suite.redisStreamClient != nil {
		suite.redisStreamClient.Close()
	}
	if suite.redisClient != nil {
		suite.redisClient.Close()
	}

	// Stop servers
	if suite.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		suite.httpServer.Shutdown(ctx)
	}
	if suite.tcpServer != nil {
		suite.tcpServer.Stop()
	}

	// Restore environment variables
	for key, value := range suite.environmentVariables {
		if value == "" {
			os.Unsetenv(key)
		} else {
			os.Setenv(key, value)
		}
	}

	// Remove test data directory
	if suite.testDataDir != "" {
		os.RemoveAll(suite.testDataDir)
	}

	suite.logger.Info("Test suite cleanup completed")
}
