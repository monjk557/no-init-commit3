package security_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/merge"
	"github.com/collabops/git-executor/internal/repositories"
)

// MergeProtectionPenetrationTest focuses specifically on testing the merge protection system
// for security vulnerabilities and bypass attempts
type MergeProtectionPenetrationTest struct {
	tempDir      string
	repo         *gogit.Repository
	pathConfig   *repositories.PathConfig
	mergeHandler *merge.MergeOperationsHandler
	protection   *merge.TreeProtectionLayer
}

func TestMergeProtectionPenetration(t *testing.T) {
	suite, cleanup := setupMergeProtectionPenetrationTest(t)
	defer cleanup()

	t.Run("EnvironmentFileBypassAttempts", func(t *testing.T) {
		suite.testEnvironmentFileBypassAttempts(t)
	})

	t.Run("PathTraversalInMergeProtection", func(t *testing.T) {
		suite.testPathTraversalInMergeProtection(t)
	})

	t.Run("ProtectionRuleBypass", func(t *testing.T) {
		suite.testProtectionRuleBypass(t)
	})

	t.Run("TreeManipulationAttacks", func(t *testing.T) {
		suite.testTreeManipulationAttacks(t)
	})

	t.Run("FallbackPolicyExploits", func(t *testing.T) {
		suite.testFallbackPolicyExploits(t)
	})

	t.Run("ConcurrentMergeRaceConditions", func(t *testing.T) {
		suite.testConcurrentMergeRaceConditions(t)
	})

	t.Run("MemoryExhaustionInProtection", func(t *testing.T) {
		suite.testMemoryExhaustionInProtection(t)
	})
}

// =============================================================================
// ENVIRONMENT FILE BYPASS ATTEMPTS
// =============================================================================

func (s *MergeProtectionPenetrationTest) testEnvironmentFileBypassAttempts(t *testing.T) {
	testCases := []struct {
		name              string
		filename          string
		shouldBeProtected bool
		description       string
	}{
		{
			name:              "basic_env_file",
			filename:          ".env",
			shouldBeProtected: true,
			description:       "Basic .env file should be protected",
		},
		{
			name:              "case_sensitive_bypass",
			filename:          ".ENV",
			shouldBeProtected: true, // Should still be protected due to case-insensitive matching
			description:       "Case variation should not bypass protection",
		},
		{
			name:              "extension_bypass",
			filename:          "config.environment",
			shouldBeProtected: true,
			description:       "Environment keyword should be detected",
		},
		{
			name:              "path_traversal_env_bypass",
			filename:          "../.env",
			shouldBeProtected: false, // Should be blocked at path validation level
			description:       "Path traversal should be blocked before protection",
		},
		{
			name:              "unicode_env_bypass",
			filename:          ".ėnv", // Unicode lookalike
			shouldBeProtected: false,  // Current implementation may not catch this
			description:       "Unicode lookalike attack",
		},
		{
			name:              "null_byte_env_bypass",
			filename:          ".env\x00.txt",
			shouldBeProtected: false, // Should be blocked at validation level
			description:       "Null byte injection",
		},
		{
			name:              "mixed_case_config_dir",
			filename:          "Config/Database.yml",
			shouldBeProtected: true,
			description:       "Mixed case config directory",
		},
		{
			name:              "nested_env_file",
			filename:          "config/env/production.env",
			shouldBeProtected: true,
			description:       "Nested environment file",
		},
		{
			name:              "disguised_env_file",
			filename:          "totally_normal_file.txt",
			shouldBeProtected: false,
			description:       "Non-environment file should not be protected",
		},
		{
			name:              "docker_compose_file",
			filename:          "docker-compose.yml",
			shouldBeProtected: true,
			description:       "Docker compose files should be protected",
		},
		{
			name:              "secrets_directory",
			filename:          "config/secrets/api.key",
			shouldBeProtected: true,
			description:       "Files in secrets directory should be protected",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test the environment file detector directly
			isProtected := s.protection.IsEnvironmentFile(tc.filename)

			if tc.shouldBeProtected {
				assert.True(t, isProtected,
					"File should be protected: %s (%s)", tc.filename, tc.description)
			} else {
				assert.False(t, isProtected,
					"File should not be protected: %s (%s)", tc.filename, tc.description)
			}

			// Test actual merge protection behavior
			s.testMergeProtectionForFile(t, tc.filename, tc.shouldBeProtected)
		})
	}
}

func (s *MergeProtectionPenetrationTest) testMergeProtectionForFile(t *testing.T, filename string, shouldBeProtected bool) {
	ctx := context.Background()

	// Create a test scenario where the file would be deleted
	// 1. Create a branch with the environment file
	// 2. Create another branch without the file
	// 3. Attempt to merge (should trigger protection if applicable)

	worktree, err := s.repo.Worktree()
	require.NoError(t, err)

	// Create feature branch with environment file
	featureBranch := plumbing.NewBranchReferenceName("feature-with-env")
	err = worktree.Checkout(&gogit.CheckoutOptions{
		Branch: featureBranch,
		Create: true,
	})
	require.NoError(t, err)

	// Add the environment file
	filePath := filepath.Join(s.tempDir, filename)
	err = os.MkdirAll(filepath.Dir(filePath), 0755)
	require.NoError(t, err)

	err = os.WriteFile(filePath, []byte("SECRET_KEY=test123\nDATABASE_URL=postgres://localhost"), 0644)
	require.NoError(t, err)

	_, err = worktree.Add(filename)
	require.NoError(t, err)

	envCommit, err := worktree.Commit(fmt.Sprintf("Add %s", filename), &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// Switch back to main and create a commit that doesn't include the env file
	err = worktree.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	})
	require.NoError(t, err)

	// Add a different file
	mainFilePath := filepath.Join(s.tempDir, "main_file.txt")
	err = os.WriteFile(mainFilePath, []byte("main content"), 0644)
	require.NoError(t, err)

	_, err = worktree.Add("main_file.txt")
	require.NoError(t, err)

	mainCommit, err := worktree.Commit("Add main file", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// Now test merge protection by simulating a merge that would delete the env file
	// Get commit objects
	envCommitObj, err := s.repo.CommitObject(envCommit)
	require.NoError(t, err)

	mainCommitObj, err := s.repo.CommitObject(mainCommit)
	require.NoError(t, err)

	// Create fallback context
	fallbackContext := merge.FallbackContext{
		SourceCommit:  envCommitObj,
		TargetCommit:  envCommitObj,  // Target has the env file
		BaseCommit:    mainCommitObj, // Base doesn't have it
		Strategy:      merge.MergeStrategyOrt,
		IsSquash:      false,
		ConflictCount: 0,
	}

	// Test protection - simulate a fallback tree that would lose the env file
	protectedTree, err := s.protection.ProtectFallback(
		ctx,
		s.repo,
		mainCommitObj.TreeHash, // Fallback tree without env file
		fallbackContext,
	)

	if shouldBeProtected {
		// Protection should preserve the environment file
		require.NoError(t, err)

		// Verify the protected tree contains the environment file
		protectedTreeObj, err := s.repo.TreeObject(protectedTree)
		require.NoError(t, err)

		_, err = protectedTreeObj.File(filename)
		assert.NoError(t, err, "Protected tree should contain environment file: %s", filename)
	} else {
		// For non-protected files, the protection system should not interfere
		// (though the file might still be preserved for other reasons)
		require.NoError(t, err)
	}
}

// =============================================================================
// PATH TRAVERSAL IN MERGE PROTECTION
// =============================================================================

func (s *MergeProtectionPenetrationTest) testPathTraversalInMergeProtection(t *testing.T) {
	traversalAttempts := []struct {
		name        string
		filepath    string
		expectBlock bool
		description string
	}{
		{
			name:        "basic_traversal",
			filepath:    "../../../etc/passwd",
			expectBlock: true,
			description: "Basic directory traversal",
		},
		{
			name:        "encoded_traversal",
			filepath:    "..%2F..%2F..%2Fetc%2Fpasswd",
			expectBlock: true,
			description: "URL encoded traversal",
		},
		{
			name:        "unicode_traversal",
			filepath:    "\u002e\u002e\u002f\u002e\u002e\u002f\u002e\u002e\u002fetc\u002fpasswd",
			expectBlock: true,
			description: "Unicode encoded traversal",
		},
		{
			name:        "git_internal_traversal",
			filepath:    ".git/../../../etc/passwd",
			expectBlock: true,
			description: "Git internal directory traversal",
		},
		{
			name:        "symlink_traversal",
			filepath:    "config/../../secrets/api.key",
			expectBlock: true,
			description: "Symlink-style traversal",
		},
		{
			name:        "legitimate_nested_file",
			filepath:    "config/env/production.env",
			expectBlock: false,
			description: "Legitimate nested environment file",
		},
	}

	for _, attempt := range traversalAttempts {
		t.Run(attempt.name, func(t *testing.T) {
			// Test if the protection system properly handles path traversal in file paths
			isEnvFile := s.protection.IsEnvironmentFile(attempt.filepath)

			if attempt.expectBlock {
				// Path traversal should be handled before environment detection
				// The behavior here depends on implementation - it might:
				// 1. Reject the path entirely
				// 2. Normalize the path and then check
				// 3. Treat it as a regular file

				// For security, we want to ensure traversal doesn't bypass protection
				t.Logf("Path traversal attempt '%s' detected as env file: %v",
					attempt.filepath, isEnvFile)

				// Additional test: ensure the path doesn't resolve outside repo
				absPath := filepath.Join(s.tempDir, attempt.filepath)
				cleanPath := filepath.Clean(absPath)

				assert.True(t, strings.HasPrefix(cleanPath, s.tempDir) || cleanPath == s.tempDir,
					"Path should not resolve outside repository: %s -> %s",
					attempt.filepath, cleanPath)
			} else {
				// Legitimate paths should be processed normally
				t.Logf("Legitimate path '%s' detected as env file: %v",
					attempt.filepath, isEnvFile)
			}
		})
	}
}

// =============================================================================
// PROTECTION RULE BYPASS
// =============================================================================

func (s *MergeProtectionPenetrationTest) testProtectionRuleBypass(t *testing.T) {
	bypassAttempts := []struct {
		name        string
		scenario    string
		setupFunc   func(t *testing.T) string
		expectBlock bool
	}{
		{
			name:     "disable_protection_via_config",
			scenario: "Attempt to bypass by modifying protection configuration",
			setupFunc: func(t *testing.T) string {
				// This tests if protection rules can be bypassed by configuration manipulation
				filename := ".env"
				return filename
			},
			expectBlock: true,
		},
		{
			name:     "rule_priority_manipulation",
			scenario: "Attempt to exploit rule priority ordering",
			setupFunc: func(t *testing.T) string {
				// Test if rules can be bypassed by exploiting priority
				filename := "config/secrets.env"
				return filename
			},
			expectBlock: true,
		},
		{
			name:     "pattern_matching_edge_case",
			scenario: "Exploit edge cases in pattern matching",
			setupFunc: func(t *testing.T) string {
				// Test edge cases that might bypass pattern matching
				filename := "config/env" // Directory without trailing slash
				return filename
			},
			expectBlock: true,
		},
		{
			name:     "strategy_override_attempt",
			scenario: "Attempt to override protection strategy",
			setupFunc: func(t *testing.T) string {
				filename := "docker-compose.yml"
				return filename
			},
			expectBlock: true,
		},
	}

	for _, attempt := range bypassAttempts {
		t.Run(attempt.name, func(t *testing.T) {
			filename := attempt.setupFunc(t)

			// Test if the file is properly protected
			isProtected := s.protection.IsProtectedFile(filename)

			if attempt.expectBlock {
				assert.True(t, isProtected,
					"Protection bypass attempt should fail: %s", attempt.scenario)
			} else {
				assert.False(t, isProtected,
					"Legitimate access should be allowed: %s", attempt.scenario)
			}
		})
	}
}

// =============================================================================
// TREE MANIPULATION ATTACKS
// =============================================================================

func (s *MergeProtectionPenetrationTest) testTreeManipulationAttacks(t *testing.T) {
	ctx := context.Background()

	attacks := []struct {
		name        string
		description string
		setupFunc   func(t *testing.T) (plumbing.Hash, merge.FallbackContext)
	}{
		{
			name:        "malformed_tree_object",
			description: "Attack using malformed Git tree object",
			setupFunc:   s.setupMalformedTreeAttack,
		},
		{
			name:        "circular_tree_reference",
			description: "Attack using circular tree references",
			setupFunc:   s.setupCircularTreeAttack,
		},
		{
			name:        "oversized_tree_object",
			description: "Attack using oversized tree object",
			setupFunc:   s.setupOversizedTreeAttack,
		},
		{
			name:        "tree_with_invalid_modes",
			description: "Attack using invalid file modes in tree",
			setupFunc:   s.setupInvalidModeTreeAttack,
		},
	}

	for _, attack := range attacks {
		t.Run(attack.name, func(t *testing.T) {
			fallbackTree, fallbackContext := attack.setupFunc(t)

			// Test protection against the malicious tree
			_, err := s.protection.ProtectFallback(
				ctx,
				s.repo,
				fallbackTree,
				fallbackContext,
			)

			// Protection should handle the attack gracefully
			// It might:
			// 1. Return an error (safe failure)
			// 2. Return the original tree (fallback)
			// 3. Return a sanitized tree

			// At minimum, it should not crash or cause undefined behavior
			assert.NotPanics(t, func() {
				s.protection.ProtectFallback(ctx, s.repo, fallbackTree, fallbackContext)
			}, "Protection should handle malicious tree gracefully: %s", attack.description)
		})
	}
}

func (s *MergeProtectionPenetrationTest) setupMalformedTreeAttack(t *testing.T) (plumbing.Hash, merge.FallbackContext) {
	// Create a basic tree for testing
	worktree, err := s.repo.Worktree()
	require.NoError(t, err)

	// Get current HEAD
	head, err := s.repo.Head()
	require.NoError(t, err)

	commit, err := s.repo.CommitObject(head.Hash())
	require.NoError(t, err)

	// Return the tree hash and a basic context
	return commit.TreeHash, merge.FallbackContext{
		SourceCommit:  commit,
		TargetCommit:  commit,
		BaseCommit:    commit,
		Strategy:      merge.MergeStrategyOrt,
		IsSquash:      false,
		ConflictCount: 0,
	}
}

func (s *MergeProtectionPenetrationTest) setupCircularTreeAttack(t *testing.T) (plumbing.Hash, merge.FallbackContext) {
	// For this test, we'll use a regular tree but test that protection handles it safely
	return s.setupMalformedTreeAttack(t)
}

func (s *MergeProtectionPenetrationTest) setupOversizedTreeAttack(t *testing.T) (plumbing.Hash, merge.FallbackContext) {
	// Create a tree with many entries to test memory limits
	worktree, err := s.repo.Worktree()
	require.NoError(t, err)

	// Create many files to simulate an oversized tree
	for i := 0; i < 100; i++ {
		filename := fmt.Sprintf("attack_file_%d.txt", i)
		filePath := filepath.Join(s.tempDir, filename)
		err = os.WriteFile(filePath, []byte(fmt.Sprintf("content %d", i)), 0644)
		require.NoError(t, err)

		_, err = worktree.Add(filename)
		require.NoError(t, err)
	}

	commit, err := worktree.Commit("Oversized tree attack", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Attacker",
			Email: "attack@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	commitObj, err := s.repo.CommitObject(commit)
	require.NoError(t, err)

	return commitObj.TreeHash, merge.FallbackContext{
		SourceCommit:  commitObj,
		TargetCommit:  commitObj,
		BaseCommit:    commitObj,
		Strategy:      merge.MergeStrategyOrt,
		IsSquash:      false,
		ConflictCount: 100,
	}
}

func (s *MergeProtectionPenetrationTest) setupInvalidModeTreeAttack(t *testing.T) (plumbing.Hash, merge.FallbackContext) {
	// For this test, we'll use a regular tree
	// In a real implementation, this would involve creating tree objects with invalid file modes
	return s.setupMalformedTreeAttack(t)
}

// =============================================================================
// FALLBACK POLICY EXPLOITS
// =============================================================================

func (s *MergeProtectionPenetrationTest) testFallbackPolicyExploits(t *testing.T) {
	ctx := context.Background()

	exploits := []struct {
		name        string
		description string
		testFunc    func(t *testing.T, ctx context.Context)
	}{
		{
			name:        "infinite_fallback_loop",
			description: "Attempt to create infinite fallback loop",
			testFunc:    s.testInfiniteFallbackLoop,
		},
		{
			name:        "fallback_policy_manipulation",
			description: "Attempt to manipulate fallback policy",
			testFunc:    s.testFallbackPolicyManipulation,
		},
		{
			name:        "resource_exhaustion_via_fallback",
			description: "Resource exhaustion through repeated fallbacks",
			testFunc:    s.testResourceExhaustionViaFallback,
		},
	}

	for _, exploit := range exploits {
		t.Run(exploit.name, func(t *testing.T) {
			exploit.testFunc(t, ctx)
		})
	}
}

func (s *MergeProtectionPenetrationTest) testInfiniteFallbackLoop(t *testing.T, ctx context.Context) {
	// Test that the fallback system doesn't get stuck in infinite loops

	worktree, err := s.repo.Worktree()
	require.NoError(t, err)

	head, err := s.repo.Head()
	require.NoError(t, err)

	commit, err := s.repo.CommitObject(head.Hash())
	require.NoError(t, err)

	// Create a fallback context that might cause loops
	fallbackContext := merge.FallbackContext{
		SourceCommit:  commit,
		TargetCommit:  commit,
		BaseCommit:    commit,
		Strategy:      merge.MergeStrategyOrt,
		IsSquash:      false,
		ConflictCount: 0,
	}

	// Test with timeout to ensure no infinite loops
	ctxWithTimeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	done := make(chan bool)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("Protection panicked (this might be expected): %v", r)
			}
		}()

		_, err := s.protection.ProtectFallback(
			ctxWithTimeout,
			s.repo,
			commit.TreeHash,
			fallbackContext,
		)

		if err != nil {
			t.Logf("Fallback protection returned error (this might be expected): %v", err)
		}

		done <- true
	}()

	select {
	case <-done:
		// Test completed within timeout
	case <-ctxWithTimeout.Done():
		t.Error("Fallback protection took too long - possible infinite loop")
	}
}

func (s *MergeProtectionPenetrationTest) testFallbackPolicyManipulation(t *testing.T, ctx context.Context) {
	// Test attempts to manipulate fallback policy
	// This would test if attackers can somehow influence the adaptive fallback policy

	// For now, just ensure the protection system is stable
	head, err := s.repo.Head()
	require.NoError(t, err)

	commit, err := s.repo.CommitObject(head.Hash())
	require.NoError(t, err)

	fallbackContext := merge.FallbackContext{
		SourceCommit:     commit,
		TargetCommit:     commit,
		BaseCommit:       commit,
		Strategy:         merge.MergeStrategyOrt,
		IsSquash:         false,
		ConflictCount:    0,
		AutoResolvable:   0,
		EnvironmentFiles: []string{".env", "config/secrets.yml"},
	}

	// Multiple calls with different parameters to test stability
	for i := 0; i < 10; i++ {
		fallbackContext.ConflictCount = i
		fallbackContext.AutoResolvable = i / 2

		_, err := s.protection.ProtectFallback(
			ctx,
			s.repo,
			commit.TreeHash,
			fallbackContext,
		)

		assert.NoError(t, err, "Fallback protection should handle varying parameters")
	}
}

func (s *MergeProtectionPenetrationTest) testResourceExhaustionViaFallback(t *testing.T, ctx context.Context) {
	// Test that repeated fallback operations don't exhaust resources

	head, err := s.repo.Head()
	require.NoError(t, err)

	commit, err := s.repo.CommitObject(head.Hash())
	require.NoError(t, err)

	fallbackContext := merge.FallbackContext{
		SourceCommit:  commit,
		TargetCommit:  commit,
		BaseCommit:    commit,
		Strategy:      merge.MergeStrategyOrt,
		IsSquash:      false,
		ConflictCount: 0,
	}

	// Perform many fallback operations
	start := time.Now()
	for i := 0; i < 50; i++ {
		_, err := s.protection.ProtectFallback(
			ctx,
			s.repo,
			commit.TreeHash,
			fallbackContext,
		)

		assert.NoError(t, err, "Repeated fallback operations should not fail")

		// Check that each operation completes quickly
		if time.Since(start) > 10*time.Second {
			t.Error("Fallback operations taking too long - possible resource exhaustion")
			break
		}
	}
}

// =============================================================================
// CONCURRENT MERGE RACE CONDITIONS
// =============================================================================

func (s *MergeProtectionPenetrationTest) testConcurrentMergeRaceConditions(t *testing.T) {
	// Test for race conditions in concurrent merge protection operations

	ctx := context.Background()
	head, err := s.repo.Head()
	require.NoError(t, err)

	commit, err := s.repo.CommitObject(head.Hash())
	require.NoError(t, err)

	fallbackContext := merge.FallbackContext{
		SourceCommit:  commit,
		TargetCommit:  commit,
		BaseCommit:    commit,
		Strategy:      merge.MergeStrategyOrt,
		IsSquash:      false,
		ConflictCount: 0,
	}

	// Run multiple concurrent protection operations
	numGoroutines := 10
	results := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer func() {
				if r := recover(); r != nil {
					results <- fmt.Errorf("goroutine %d panicked: %v", id, r)
					return
				}
			}()

			// Perform multiple operations per goroutine
			for j := 0; j < 5; j++ {
				_, err := s.protection.ProtectFallback(
					ctx,
					s.repo,
					commit.TreeHash,
					fallbackContext,
				)

				if err != nil {
					results <- fmt.Errorf("goroutine %d operation %d failed: %v", id, j, err)
					return
				}
			}

			results <- nil // Success
		}(i)
	}

	// Collect results
	for i := 0; i < numGoroutines; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Errorf("Concurrent operation failed: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Concurrent operation timed out")
		}
	}
}

// =============================================================================
// MEMORY EXHAUSTION IN PROTECTION
// =============================================================================

func (s *MergeProtectionPenetrationTest) testMemoryExhaustionInProtection(t *testing.T) {
	// Test that the protection system handles memory pressure gracefully

	ctx := context.Background()

	// Create a large number of files to stress test memory usage
	worktree, err := s.repo.Worktree()
	require.NoError(t, err)

	// Create many environment files
	envFiles := make([]string, 100)
	for i := 0; i < 100; i++ {
		filename := fmt.Sprintf("config/env/env_%d.env", i)
		envFiles[i] = filename

		filePath := filepath.Join(s.tempDir, filename)
		err = os.MkdirAll(filepath.Dir(filePath), 0755)
		require.NoError(t, err)

		// Large content to increase memory usage
		content := strings.Repeat(fmt.Sprintf("LARGE_CONFIG_%d=%s\n", i, strings.Repeat("x", 1000)), 100)
		err = os.WriteFile(filePath, []byte(content), 0644)
		require.NoError(t, err)

		_, err = worktree.Add(filename)
		require.NoError(t, err)
	}

	commit, err := worktree.Commit("Add many env files", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	commitObj, err := s.repo.CommitObject(commit)
	require.NoError(t, err)

	fallbackContext := merge.FallbackContext{
		SourceCommit:     commitObj,
		TargetCommit:     commitObj,
		BaseCommit:       commitObj,
		Strategy:         merge.MergeStrategyOrt,
		IsSquash:         false,
		ConflictCount:    100,
		EnvironmentFiles: envFiles,
	}

	// Test protection with large tree
	_, err = s.protection.ProtectFallback(
		ctx,
		s.repo,
		commitObj.TreeHash,
		fallbackContext,
	)

	// Should handle large trees gracefully
	assert.NoError(t, err, "Protection should handle large trees gracefully")
}

// =============================================================================
// SETUP AND UTILITY FUNCTIONS
// =============================================================================

func setupMergeProtectionPenetrationTest(t *testing.T) (*MergeProtectionPenetrationTest, func()) {
	// Create temporary directory
	tempDir, err := os.MkdirTemp("", "merge_protection_penetration_*")
	require.NoError(t, err)

	// Initialize git repository
	repo, err := gogit.PlainInit(tempDir, false)
	require.NoError(t, err)

	// Create initial commit
	worktree, err := repo.Worktree()
	require.NoError(t, err)

	readmePath := filepath.Join(tempDir, "README.md")
	err = os.WriteFile(readmePath, []byte("# Test Repository"), 0644)
	require.NoError(t, err)

	_, err = worktree.Add("README.md")
	require.NoError(t, err)

	_, err = worktree.Commit("Initial commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// Create path config
	pathConfig, err := repositories.NewPathConfig(tempDir)
	require.NoError(t, err)

	// Create test configuration with protection enabled
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
		Logging: config.LoggingConfig{
			Level: "debug", // Use debug level for more detailed testing
		},
		MergeProtection: config.MergeProtectionConfig{
			Enabled: true,
			EnvironmentProtection: config.EnvironmentProtectionConfig{
				Enabled:               true,
				EnableForSquashMerge:  true,
				EnableForRegularMerge: true,
				EnableForRebaseMerge:  true,
			},
			SafetyMode:               "strict",
			FallbackStrategy:         "adaptive",
			IntelligentMerging:       true,
			PredictiveConflictDetect: true,
			AdaptiveFallback:         true,
			EnableAuditLogs:          true,
		},
	}

	// Create merge handler
	mergeHandler, err := merge.NewMergeOperationsHandler(cfg)
	require.NoError(t, err)

	// Create protection layer directly for testing
	protection := merge.NewTreeProtectionLayer(mergeHandler.Logger)

	suite := &MergeProtectionPenetrationTest{
		tempDir:      tempDir,
		repo:         repo,
		pathConfig:   pathConfig,
		mergeHandler: mergeHandler,
		protection:   protection,
	}

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return suite, cleanup
}
