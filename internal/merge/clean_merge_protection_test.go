package merge

import (
	"context"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
)

// TestCleanMergeProtection tests the CRITICAL FIX for successful merge protection
func TestCleanMergeProtection(t *testing.T) {
	t.Run("clean_merge_applies_environment_protection", func(t *testing.T) {
		t.Log("=== TESTING CLEAN MERGE PROTECTION (THE REAL PRODUCTION SCENARIO) ===")

		// This test reproduces the EXACT production scenario:
		// 1. No conflicts detected
		// 2. createMergedTree succeeds
		// 3. Environment files would be silently deleted
		// 4. Our fix should now apply protection to successful merges

		fs := memory.NewStorage()
		memFS := memfs.New()

		repo, err := git.Init(fs, memFS)
		require.NoError(t, err)

		worktree, err := repo.Worktree()
		require.NoError(t, err)

		// Create base commit with environment files
		envFiles := map[string]string{
			"config/env/build-it/api-piper.env": "API_URL=https://build-it.example.com\nAPI_KEY=build-secret",
			"config/env/production.env":         "APP_ENV=production\nDEBUG=false",
			"config/database.yml":               "production:\n  host: prod-db",
			"docker-compose.yml":                "version: '3.8'\nservices:\n  app:\n    build: .",
		}

		// Create shared files that exist in all branches
		sharedFiles := map[string]string{
			"README.md":    "# Shared Project",
			"package.json": "{\"name\": \"app\", \"version\": \"1.0.0\"}",
			"src/index.js": "console.log('Hello World');",
		}

		// Add all files to base
		allFiles := make(map[string]string)
		for k, v := range envFiles {
			allFiles[k] = v
		}
		for k, v := range sharedFiles {
			allFiles[k] = v
		}

		for path, content := range allFiles {
			file, err := worktree.Filesystem.Create(path)
			require.NoError(t, err)
			_, err = file.Write([]byte(content))
			require.NoError(t, err)
			file.Close()
			_, err = worktree.Add(path)
			require.NoError(t, err)
		}

		baseCommit, err := worktree.Commit("Base commit with all files", &git.CommitOptions{
			Author: &object.Signature{Name: "Base", Email: "base@test.com"},
		})
		require.NoError(t, err)
		t.Logf("✅ Base commit: %s", baseCommit.String())

		// Create target branch - keeps ALL files, adds a new target-specific file (NO conflicts)
		file, err := worktree.Filesystem.Create("target-only.txt")
		require.NoError(t, err)
		_, err = file.Write([]byte("Target-only feature"))
		require.NoError(t, err)
		file.Close()
		_, err = worktree.Add("target-only.txt")
		require.NoError(t, err)

		targetCommit, err := worktree.Commit("Target adds new file, keeps env", &git.CommitOptions{
			Author: &object.Signature{Name: "Target", Email: "target@test.com"},
		})
		require.NoError(t, err)
		t.Logf("✅ Target commit: %s", targetCommit.String())

		// Create source branch - removes ALL env files, adds new feature
		err = worktree.Checkout(&git.CheckoutOptions{Hash: baseCommit})
		require.NoError(t, err)

		// Remove ALL environment files
		for envPath := range envFiles {
			_, err = worktree.Remove(envPath)
			require.NoError(t, err)
		}

		// Add new feature files (will not conflict with target - unique to source)
		featureFiles := map[string]string{
			"src/feature.js":        "console.log('New feature');",
			"tests/feature.test.js": "test('feature works', () => {});",
			"docs/feature.md":       "# Feature documentation",
		}

		for path, content := range featureFiles {
			file, err := worktree.Filesystem.Create(path)
			require.NoError(t, err)
			_, err = file.Write([]byte(content))
			require.NoError(t, err)
			file.Close()
			_, err = worktree.Add(path)
			require.NoError(t, err)
		}

		sourceCommit, err := worktree.Commit("Source removes env, adds non-conflicting features", &git.CommitOptions{
			Author: &object.Signature{Name: "Source", Email: "source@test.com"},
		})
		require.NoError(t, err)
		t.Logf("✅ Source commit: %s", sourceCommit.String())

		// Now test the CLEAN MERGE scenario
		t.Log("\n=== TESTING REGULAR MERGE (CLEAN, NO CONFLICTS) ===")

		sourceCommitObj, err := repo.CommitObject(sourceCommit)
		require.NoError(t, err)
		targetCommitObj, err := repo.CommitObject(targetCommit)
		require.NoError(t, err)

		cfg := &config.Config{
			Git:     config.GitConfig{RepoRoot: "/tmp", EnablePackfile: true},
			Logging: config.LoggingConfig{Level: "info", Format: "json", Output: "stdout"},
		}

		handler, err := NewMergeOperationsHandler(cfg)
		require.NoError(t, err)

		ctx := context.Background()

		// Use REGULAR MERGE (not squash, not rebase)
		analysisReq := &MergeRequest{
			SourceRef: "source", TargetRef: "target",
			Method:        MergeMethodMerge, // This is the key - regular three-way merge
			Strategy:      MergeStrategyRecursive,
			ForceConflict: true, // Enable conflict handling to test protection system
		}

		analysis, err := handler.analyzeMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, analysisReq)
		require.NoError(t, err)

		t.Logf("Clean merge analysis: conflicts=%d, auto_resolvable=%d, can_merge=%t",
			analysis.ConflictCount, analysis.AutoResolvable, analysis.CanMerge)

		// The key insight: if conflicts=0 or all auto-resolvable, this will trigger "clean merge" path
		// Previously: environment files would be silently deleted
		// Now with our fix: protection should be applied to successful merge

		// Perform regular three-way merge
		mergeReq := &MergeRequest{
			SourceRef: "source", TargetRef: "target",
			Method:        MergeMethodMerge,
			Strategy:      MergeStrategyRecursive,
			ForceConflict: true, // Enable conflict handling
			Message:       "Clean merge with environment protection",
		}

		mergeResult, err := handler.performMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, mergeReq, analysis)
		require.NoError(t, err, "Clean merge should succeed")
		require.NotNil(t, mergeResult, "Merge result should exist")
		require.NotNil(t, mergeResult.MergeCommit, "Merge commit should be created")

		t.Logf("✅ Clean merge commit: %s", mergeResult.MergeCommit.Hash.String())

		// CRITICAL VERIFICATION: Check if environment files are preserved
		t.Log("\n=== VERIFYING ENVIRONMENT PROTECTION IN CLEAN MERGE ===")

		mergeTree, err := repo.TreeObject(mergeResult.MergeCommit.TreeHash)
		require.NoError(t, err)

		// Check ALL environment files
		protectedCount := 0
		deletedFiles := []string{}

		for envPath := range envFiles {
			_, err := mergeTree.File(envPath)
			if err != nil {
				deletedFiles = append(deletedFiles, envPath)
				t.Errorf("💥 CLEAN MERGE FAILED: %s DELETED! Error: %v", envPath, err)
			} else {
				protectedCount++
				t.Logf("✅ CLEAN MERGE PROTECTED: %s preserved", envPath)
			}
		}

		// Check that new feature files are also included
		includedFeatures := 0
		for featurePath := range featureFiles {
			_, err := mergeTree.File(featurePath)
			if err == nil {
				includedFeatures++
				t.Logf("✅ FEATURE INCLUDED: %s", featurePath)
			}
		}

		// Final results
		t.Log("\n=== CLEAN MERGE PROTECTION RESULTS ===")
		t.Logf("Environment files protected: %d/%d", protectedCount, len(envFiles))
		t.Logf("Feature files included: %d/%d", includedFeatures, len(featureFiles))
		t.Logf("Environment files deleted: %d", len(deletedFiles))

		if len(deletedFiles) == 0 {
			t.Log("🎉 SUCCESS: Clean merge protection working!")
			t.Log("   The CRITICAL FIX prevents environment deletion in successful merges")
		} else {
			t.Log("❌ FAILURE: Clean merge still deleting environment files")
			for _, deleted := range deletedFiles {
				t.Logf("   - %s", deleted)
			}
		}

		// These assertions verify our fix
		require.Empty(t, deletedFiles, "CRITICAL: Clean merge should not delete environment files")
		require.Equal(t, len(envFiles), protectedCount, "All environment files should be protected in clean merge")
		require.Equal(t, len(featureFiles), includedFeatures, "All feature files should be included")

		t.Log("\n" + strings.Repeat("=", 60))
		t.Log("✅ CLEAN MERGE ENVIRONMENT PROTECTION: VERIFIED")
		t.Log("✅ config/env/build-it/api-piper.env: PROTECTED")
		t.Log("✅ Production environment files: SAFE")
		t.Log(strings.Repeat("=", 60))
	})

	t.Run("clean_merge_no_conflicts_scenario", func(t *testing.T) {
		t.Log("=== TESTING ABSOLUTELY CLEAN MERGE (ZERO CONFLICTS) ===")

		// This test ensures the fix works even when there are absolutely no conflicts
		// This is the most common production scenario

		fs := memory.NewStorage()
		memFS := memfs.New()

		repo, err := git.Init(fs, memFS)
		require.NoError(t, err)

		worktree, err := repo.Worktree()
		require.NoError(t, err)

		// Base commit with environment
		envFile := "config/env/build-it/api-piper.env"
		envContent := "API_KEY=secret123"

		file, err := worktree.Filesystem.Create(envFile)
		require.NoError(t, err)
		_, err = file.Write([]byte(envContent))
		require.NoError(t, err)
		file.Close()
		_, err = worktree.Add(envFile)
		require.NoError(t, err)

		baseCommit, err := worktree.Commit("Base with env", &git.CommitOptions{
			Author: &object.Signature{Name: "Base", Email: "base@test.com"},
		})
		require.NoError(t, err)

		// Target: keeps env, adds different file
		file, err = worktree.Filesystem.Create("target-feature.txt")
		require.NoError(t, err)
		_, err = file.Write([]byte("target feature"))
		require.NoError(t, err)
		file.Close()
		_, err = worktree.Add("target-feature.txt")
		require.NoError(t, err)

		targetCommit, err := worktree.Commit("Target adds file", &git.CommitOptions{
			Author: &object.Signature{Name: "Target", Email: "target@test.com"},
		})
		require.NoError(t, err)

		// Source: removes env, adds different file (ZERO conflicts)
		err = worktree.Checkout(&git.CheckoutOptions{Hash: baseCommit})
		require.NoError(t, err)

		_, err = worktree.Remove(envFile)
		require.NoError(t, err)

		file, err = worktree.Filesystem.Create("source-feature.txt")
		require.NoError(t, err)
		_, err = file.Write([]byte("source feature"))
		require.NoError(t, err)
		file.Close()
		_, err = worktree.Add("source-feature.txt")
		require.NoError(t, err)

		sourceCommit, err := worktree.Commit("Source removes env, adds file", &git.CommitOptions{
			Author: &object.Signature{Name: "Source", Email: "source@test.com"},
		})
		require.NoError(t, err)

		// Test clean merge
		sourceCommitObj, err := repo.CommitObject(sourceCommit)
		require.NoError(t, err)
		targetCommitObj, err := repo.CommitObject(targetCommit)
		require.NoError(t, err)

		cfg := &config.Config{
			Git:     config.GitConfig{RepoRoot: "/tmp", EnablePackfile: true},
			Logging: config.LoggingConfig{Level: "info", Format: "json", Output: "stdout"},
		}

		handler, err := NewMergeOperationsHandler(cfg)
		require.NoError(t, err)

		ctx := context.Background()

		analysisReq := &MergeRequest{
			SourceRef: "source", TargetRef: "target", Method: MergeMethodMerge,
			Strategy: MergeStrategyRecursive, ForceConflict: true,
		}

		analysis, err := handler.analyzeMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, analysisReq)
		require.NoError(t, err)

		t.Logf("Zero-conflict analysis: conflicts=%d, can_merge=%t", analysis.ConflictCount, analysis.CanMerge)

		mergeReq := &MergeRequest{
			SourceRef: "source", TargetRef: "target", Method: MergeMethodMerge,
			Strategy: MergeStrategyRecursive, ForceConflict: true,
			Message: "Zero conflict merge",
		}

		result, err := handler.performMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, mergeReq, analysis)
		require.NoError(t, err)

		// Verify environment file protected
		mergeTree, err := repo.TreeObject(result.MergeCommit.TreeHash)
		require.NoError(t, err)

		_, err = mergeTree.File(envFile)
		if err != nil {
			t.Errorf("❌ ZERO-CONFLICT MERGE FAILED: %s deleted", envFile)
		} else {
			t.Logf("✅ ZERO-CONFLICT PROTECTION: %s preserved", envFile)
		}

		require.NoError(t, err, "Environment file should be protected even in zero-conflict merges")

		t.Log("🎉 ZERO-CONFLICT MERGE PROTECTION: SUCCESS")
	})
}
