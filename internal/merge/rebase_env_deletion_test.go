//go:build gogit

package merge

import (
	"context"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
)

// TestRebaseEnvDeletion tests if rebase also deletes environment files
func TestRebaseEnvDeletion(t *testing.T) {
	t.Run("rebase_deletes_config_env_build_it_files", func(t *testing.T) {
		t.Log("=== TESTING REBASE ENVIRONMENT FILE DELETION ===")

		// Create test repository
		fs := memory.NewStorage()
		memFS := memfs.New()

		repo, err := git.Init(fs, memFS)
		require.NoError(t, err)

		worktree, err := repo.Worktree()
		require.NoError(t, err)

		// Create base commit with environment files
		envFiles := map[string]string{
			"config/env/build-it/api-piper.env": "API_URL=https://build-it.example.com\nAPI_KEY=build-secret",
			"config/env/build-it/database.env":  "DB_HOST=build-db\nDB_PORT=5432\nDB_NAME=builddb",
			"config/env/staging/app.env":        "APP_ENV=staging\nDEBUG=true\nLOG_LEVEL=debug",
			"config/env/production.env":         "APP_ENV=production\nDEBUG=false\nLOG_LEVEL=error",
			"config/database.yml":               "production:\n  host: prod-db\nstaging:\n  host: staging-db",
			"docker-compose.yml":                "version: '3.8'\nservices:\n  app:\n    build: .",
			"README.md":                         "# Project with complex env structure",
		}

		// Create base commit
		for path, content := range envFiles {
			file, err := worktree.Filesystem.Create(path)
			require.NoError(t, err)
			_, err = file.Write([]byte(content))
			require.NoError(t, err)
			file.Close()
			_, err = worktree.Add(path)
			require.NoError(t, err)
		}

		baseCommit, err := worktree.Commit("Base commit with env files", &git.CommitOptions{
			Author: &object.Signature{Name: "Base", Email: "base@test.com"},
		})
		require.NoError(t, err)
		t.Logf("✅ Base commit: %s", baseCommit.String())

		// Create target branch that modifies env files (this is where env files should be preserved)
		for path, content := range envFiles {
			file, err := worktree.Filesystem.Create(path)
			require.NoError(t, err)
			_, err = file.Write([]byte(content + "\nUPDATED_BY_TARGET=true"))
			require.NoError(t, err)
			file.Close()
			_, err = worktree.Add(path)
			require.NoError(t, err)
		}

		targetCommit, err := worktree.Commit("Target updates env files", &git.CommitOptions{
			Author: &object.Signature{Name: "Target", Email: "target@test.com"},
		})
		require.NoError(t, err)
		t.Logf("✅ Target commit: %s", targetCommit.String())

		// Create source branch that removes ALL env files and adds new features
		err = worktree.Checkout(&git.CheckoutOptions{Hash: baseCommit})
		require.NoError(t, err)

		// Remove all env files
		for path := range envFiles {
			if path != "README.md" { // Keep README but remove all env files
				_, err = worktree.Remove(path)
				require.NoError(t, err)
			}
		}

		// Add new feature files
		featureFiles := map[string]string{
			"src/new-feature.js":    "console.log('New awesome feature');",
			"tests/feature.test.js": "describe('Feature', () => { it('works', () => {}); });",
			"docs/feature.md":       "# New Feature Documentation",
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

		// Update README
		readmeFile, err := worktree.Filesystem.Create("README.md")
		require.NoError(t, err)
		_, err = readmeFile.Write([]byte("# Project with new feature (no env files)"))
		require.NoError(t, err)
		readmeFile.Close()
		_, err = worktree.Add("README.md")
		require.NoError(t, err)

		sourceCommit, err := worktree.Commit("Source adds features, removes env files", &git.CommitOptions{
			Author: &object.Signature{Name: "Source", Email: "source@test.com"},
		})
		require.NoError(t, err)
		t.Logf("✅ Source commit: %s", sourceCommit.String())

		// Now test REBASE: rebase source onto target (should preserve env files from target)
		t.Log("\n=== TESTING REBASE WITH ENVIRONMENT PROTECTION ===")

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

		// Analyze rebase
		analysisReq := &MergeRequest{
			SourceRef: "source", TargetRef: "target", Method: MergeMethodRebase,
			Strategy: MergeStrategyRecursive, ForceConflict: true,
		}

		analysis, err := handler.analyzeMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, analysisReq)
		require.NoError(t, err)
		t.Logf("Rebase analysis: conflicts=%d, auto_resolvable=%d", analysis.ConflictCount, analysis.AutoResolvable)

		// Perform rebase
		rebaseReq := &MergeRequest{
			SourceRef: "source", TargetRef: "target", Method: MergeMethodRebase,
			Strategy: MergeStrategyRecursive, ForceConflict: true,
			Message: "Rebase with env protection",
		}

		rebaseCommit, err := handler.performRebase(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, rebaseReq, analysis)
		require.NoError(t, err, "Rebase should succeed")
		require.NotNil(t, rebaseCommit, "Rebase commit should be created")
		t.Logf("✅ Rebase commit: %s", rebaseCommit.Hash.String())

		// CRITICAL: Verify ALL environment files are preserved in rebase result
		t.Log("\n=== VERIFYING ENVIRONMENT FILE PRESERVATION IN REBASE ===")
		rebaseTree, err := repo.TreeObject(rebaseCommit.TreeHash)
		require.NoError(t, err)

		deletedFiles := []string{}
		preservedFiles := []string{}

		// Check all original env files
		for envPath := range envFiles {
			_, err := rebaseTree.File(envPath)
			if err != nil {
				deletedFiles = append(deletedFiles, envPath)
				t.Errorf("💥 DELETED IN REBASE: %s (Error: %v)", envPath, err)
			} else {
				preservedFiles = append(preservedFiles, envPath)
				t.Logf("✅ PRESERVED IN REBASE: %s", envPath)
			}
		}

		// Also check new feature files are included
		newFeatureFiles := []string{}
		for featurePath := range featureFiles {
			_, err := rebaseTree.File(featurePath)
			if err == nil {
				newFeatureFiles = append(newFeatureFiles, featurePath)
				t.Logf("✅ NEW FEATURE INCLUDED: %s", featurePath)
			}
		}

		// Final assessment
		t.Log("\n=== REBASE RESULTS ===")
		t.Logf("Environment files preserved: %d/%d", len(preservedFiles), len(envFiles))
		t.Logf("New feature files included: %d/%d", len(newFeatureFiles), len(featureFiles))
		t.Logf("Environment files deleted: %d", len(deletedFiles))

		if len(deletedFiles) > 0 {
			t.Logf("❌ REBASE FAILED: Environment files deleted:")
			for _, deleted := range deletedFiles {
				t.Logf("   - %s", deleted)
			}
		} else {
			t.Logf("🎉 REBASE SUCCESS: All environment files preserved!")
		}

		// These assertions should pass with our protection system
		require.Empty(t, deletedFiles, "CRITICAL: No environment files should be deleted during rebase")
		require.Equal(t, len(envFiles), len(preservedFiles), "All original env files should be preserved")
		require.Greater(t, len(newFeatureFiles), 0, "New feature files should also be included")
	})

	t.Run("rebase_vs_squash_comparison", func(t *testing.T) {
		t.Log("=== COMPARING REBASE VS SQUASH ENVIRONMENT PROTECTION ===")

		// Test that both rebase and squash protect environment files identically
		// This ensures consistency across merge methods

		// Setup identical scenario for both
		fs := memory.NewStorage()
		memFS := memfs.New()

		repo, err := git.Init(fs, memFS)
		require.NoError(t, err)

		worktree, err := repo.Worktree()
		require.NoError(t, err)

		// Simple env file scenario
		envFiles := map[string]string{
			"config/env/build-it/api-piper.env": "API_KEY=secret123",
			"config/database.yml":               "host: db-server",
			"docker-compose.yml":                "version: '3'",
		}

		// Create commits (same as before but simpler)
		for path, content := range envFiles {
			file, err := worktree.Filesystem.Create(path)
			require.NoError(t, err)
			_, err = file.Write([]byte(content))
			require.NoError(t, err)
			file.Close()
			_, err = worktree.Add(path)
			require.NoError(t, err)
		}

		baseCommit, err := worktree.Commit("Base", &git.CommitOptions{
			Author: &object.Signature{Name: "Base", Email: "base@test.com"},
		})
		require.NoError(t, err)

		// Target branch
		targetCommit, err := worktree.Commit("Target", &git.CommitOptions{
			Author: &object.Signature{Name: "Target", Email: "target@test.com"},
		})
		require.NoError(t, err)

		// Source branch removes env files
		err = worktree.Checkout(&git.CheckoutOptions{Hash: baseCommit})
		require.NoError(t, err)

		for path := range envFiles {
			_, err = worktree.Remove(path)
			require.NoError(t, err)
		}

		file, err := worktree.Filesystem.Create("feature.js")
		require.NoError(t, err)
		_, err = file.Write([]byte("console.log('feature');"))
		require.NoError(t, err)
		file.Close()
		_, err = worktree.Add("feature.js")
		require.NoError(t, err)

		sourceCommit, err := worktree.Commit("Source removes env", &git.CommitOptions{
			Author: &object.Signature{Name: "Source", Email: "source@test.com"},
		})
		require.NoError(t, err)

		// Test both methods
		cfg := &config.Config{
			Git:     config.GitConfig{RepoRoot: "/tmp", EnablePackfile: true},
			Logging: config.LoggingConfig{Level: "info", Format: "json", Output: "stdout"},
		}

		handler, err := NewMergeOperationsHandler(cfg)
		require.NoError(t, err)

		ctx := context.Background()

		sourceCommitObj, err := repo.CommitObject(sourceCommit)
		require.NoError(t, err)
		targetCommitObj, err := repo.CommitObject(targetCommit)
		require.NoError(t, err)

		// Test SQUASH
		squashReq := &MergeRequest{
			SourceRef: "source", TargetRef: "target", Method: MergeMethodSquash,
			Strategy: MergeStrategyRecursive, ForceConflict: true,
		}
		analysis, err := handler.analyzeMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, squashReq)
		require.NoError(t, err)

		squashCommit, err := handler.performSquashMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, squashReq, analysis)
		require.NoError(t, err)

		// Test REBASE
		rebaseReq := &MergeRequest{
			SourceRef: "source", TargetRef: "target", Method: MergeMethodRebase,
			Strategy: MergeStrategyRecursive, ForceConflict: true,
		}

		rebaseCommit, err := handler.performRebase(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, rebaseReq, analysis)
		require.NoError(t, err)

		// Verify both preserve environment files
		squashTree, err := repo.TreeObject(squashCommit.TreeHash)
		require.NoError(t, err)
		rebaseTree, err := repo.TreeObject(rebaseCommit.TreeHash)
		require.NoError(t, err)

		squashPreserved := 0
		rebasePreserved := 0

		for envPath := range envFiles {
			if _, err := squashTree.File(envPath); err == nil {
				squashPreserved++
			}
			if _, err := rebaseTree.File(envPath); err == nil {
				rebasePreserved++
			}
		}

		t.Logf("Squash preserved: %d/%d env files", squashPreserved, len(envFiles))
		t.Logf("Rebase preserved: %d/%d env files", rebasePreserved, len(envFiles))

		// Both should preserve all files
		require.Equal(t, len(envFiles), squashPreserved, "Squash should preserve all env files")
		require.Equal(t, len(envFiles), rebasePreserved, "Rebase should preserve all env files")
		require.Equal(t, squashPreserved, rebasePreserved, "Squash and rebase should preserve same number of files")

		t.Log("🎉 BOTH SQUASH AND REBASE PROTECT ENVIRONMENT FILES CORRECTLY!")
	})
}
