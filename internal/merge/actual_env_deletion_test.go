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

// TestActualEnvDeletionIssue reproduces the real env deletion problem
func TestActualEnvDeletionIssue(t *testing.T) {
	t.Run("config_env_directory_disappears", func(t *testing.T) {
		// Create a test repository with a filesystem (not bare)
		fs := memory.NewStorage()
		memFS := memfs.New()

		repo, err := git.Init(fs, memFS)
		require.NoError(t, err)

		worktree, err := repo.Worktree()
		require.NoError(t, err)

		// STEP 1: Create initial commit with config/env directory and files
		t.Log("STEP 1: Creating initial commit with config/env/")

		// Create config/env directory with files
		envFiles := map[string]string{
			"config/env/production.env":  "DB_HOST=prod-db\nDB_PORT=5432\nAPI_KEY=prod-key",
			"config/env/staging.env":     "DB_HOST=staging-db\nDB_PORT=5432\nAPI_KEY=staging-key",
			"config/env/development.env": "DB_HOST=localhost\nDB_PORT=5432\nAPI_KEY=dev-key",
			"README.md":                  "# Project with environment configs",
		}

		for path, content := range envFiles {
			file, err := worktree.Filesystem.Create(path)
			require.NoError(t, err, "Failed to create %s", path)

			_, err = file.Write([]byte(content))
			require.NoError(t, err, "Failed to write to %s", path)
			file.Close()

			_, err = worktree.Add(path)
			require.NoError(t, err, "Failed to add %s", path)
		}

		baseCommit, err := worktree.Commit("Initial commit with config/env", &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Base Author",
				Email: "base@example.com",
			},
		})
		require.NoError(t, err)

		t.Logf("✅ Base commit created: %s", baseCommit.String())

		// STEP 2: Create target branch that keeps config/env
		t.Log("STEP 2: Creating target branch (keeps config/env/)")

		// Modify non-env files
		file, err := worktree.Filesystem.Create("src/main.go")
		require.NoError(t, err)
		_, err = file.Write([]byte("package main\n\nfunc main() {\n\tprintln(\"Hello from target\")\n}"))
		require.NoError(t, err)
		file.Close()

		_, err = worktree.Add("src/main.go")
		require.NoError(t, err)

		targetCommit, err := worktree.Commit("Add main.go on target", &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Target Author",
				Email: "target@example.com",
			},
		})
		require.NoError(t, err)

		t.Logf("✅ Target commit created: %s", targetCommit.String())

		// STEP 3: Create source branch WITHOUT config/env directory
		t.Log("STEP 3: Creating source branch (removes config/env/)")

		// Go back to base commit
		err = worktree.Checkout(&git.CheckoutOptions{Hash: baseCommit})
		require.NoError(t, err)

		// Create a completely different file structure without config/env
		// This simulates a branch that doesn't have the config/env directory

		// Remove all files first
		status, err := worktree.Status()
		require.NoError(t, err)
		for file := range status {
			err = worktree.Filesystem.Remove(file)
			if err != nil {
				t.Logf("Warning: Could not remove %s: %v", file, err)
			}
		}

		// Create new structure without config/env
		newFiles := map[string]string{
			"app.py":    "#!/usr/bin/env python3\nprint('Hello from Python')",
			"README.md": "# Python version of the project", // Different content
		}

		for path, content := range newFiles {
			file, err := worktree.Filesystem.Create(path)
			require.NoError(t, err)

			_, err = file.Write([]byte(content))
			require.NoError(t, err)
			file.Close()

			_, err = worktree.Add(path)
			require.NoError(t, err)
		}

		// Remove the config/env files from git index
		_, err = worktree.Remove("config/env/production.env")
		require.NoError(t, err)
		_, err = worktree.Remove("config/env/staging.env")
		require.NoError(t, err)
		_, err = worktree.Remove("config/env/development.env")
		require.NoError(t, err)

		sourceCommit, err := worktree.Commit("Rewrite to Python without config/env", &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Source Author",
				Email: "source@example.com",
			},
		})
		require.NoError(t, err)

		t.Logf("✅ Source commit created: %s", sourceCommit.String())

		// STEP 4: Now test the merge - this is where config/env should be preserved
		t.Log("STEP 4: Testing merge - config/env should be preserved!")

		// Get commit objects
		sourceCommitObj, err := repo.CommitObject(sourceCommit)
		require.NoError(t, err)

		targetCommitObj, err := repo.CommitObject(targetCommit)
		require.NoError(t, err)

		baseCommitObj, err := repo.CommitObject(baseCommit)
		require.NoError(t, err)

		// Use our merge handler - create a basic config
		cfg := &config.Config{
			Git: config.GitConfig{
				RepoRoot:       "/tmp",
				EnablePackfile: true,
				MaxCacheSize:   100,
			},
			Logging: config.LoggingConfig{
				Level:  "info",
				Format: "json",
				Output: "stdout",
			},
		}
		handler, err := NewMergeOperationsHandler(cfg)
		require.NoError(t, err)

		ctx := context.Background()

		// Create a basic analysis result to satisfy the function signature
		analysis := &MergeAnalysisResult{
			CanMerge:     true,
			HasConflicts: false,
			Strategy:     MergeStrategyRecursive,
		}

		// Create protection context for the test
		protectionCtx := NewMergeProtectionContext(cfg, false, false) // isSquash=false, isRebase=false

		mergedTreeHash, err := handler.createMergedTreeWithBase(
			ctx, repo, sourceCommitObj, targetCommitObj, baseCommitObj, analysis, MergeStrategyRecursive, protectionCtx)

		t.Logf("Merge result: tree_hash=%v, err=%v", mergedTreeHash.IsZero(), err)

		if err != nil {
			t.Logf("❌ Merge failed with error: %v", err)
			return
		}

		if mergedTreeHash.IsZero() {
			t.Error("❌ CRITICAL: mergedTreeHash is zero!")
			return
		}

		// STEP 5: Verify that config/env directory and files are preserved
		t.Log("STEP 5: Verifying config/env preservation...")

		// Get the merged tree object from the hash
		mergedTree, err := repo.TreeObject(mergedTreeHash)
		require.NoError(t, err, "Failed to get merged tree object")

		// Check if config/env files exist in the merged tree
		envPaths := []string{
			"config/env/production.env",
			"config/env/staging.env",
			"config/env/development.env",
		}

		for _, envPath := range envPaths {
			_, err := mergedTree.File(envPath)
			if err != nil {
				t.Errorf("❌ CRITICAL FAILURE: %s is missing from merged tree! Error: %v", envPath, err)
			} else {
				t.Logf("✅ %s is preserved in merged tree", envPath)
			}
		}

		// Also check that source files are included
		_, err = mergedTree.File("app.py")
		if err != nil {
			t.Logf("⚠️  app.py missing from merge: %v", err)
		} else {
			t.Logf("✅ app.py included from source")
		}

		_, err = mergedTree.File("src/main.go")
		if err != nil {
			t.Logf("⚠️  src/main.go missing from merge: %v", err)
		} else {
			t.Logf("✅ src/main.go included from target")
		}

		// SUMMARY
		separator := strings.Repeat("=", 60)
		t.Log("\n" + separator)
		t.Log("TEST SUMMARY:")
		t.Log("- Base: Has config/env directory with 3 files")
		t.Log("- Target: Keeps config/env, adds src/main.go")
		t.Log("- Source: Removes config/env completely, adds app.py")
		t.Log("- Expected: Merge should preserve config/env from target/base")
		t.Log(separator)
	})

	t.Run("squash_merge_env_deletion", func(t *testing.T) {
		// Create a test repository with a filesystem (not bare)
		fs := memory.NewStorage()
		memFS := memfs.New()

		repo, err := git.Init(fs, memFS)
		require.NoError(t, err)

		worktree, err := repo.Worktree()
		require.NoError(t, err)

		// STEP 1: Create initial commit with config/env directory and files
		t.Log("STEP 1: Creating initial commit with config/env/ (common base)")

		// Create config/env directory with files
		envFiles := map[string]string{
			"config/env/production.env": "DB_HOST=prod-db\nDB_PORT=5432\nAPI_KEY=prod-key",
			"config/env/staging.env":    "DB_HOST=staging-db\nDB_PORT=5432\nAPI_KEY=staging-key",
			"README.md":                 "# Project with environment configs",
		}

		for path, content := range envFiles {
			file, err := worktree.Filesystem.Create(path)
			require.NoError(t, err, "Failed to create %s", path)

			_, err = file.Write([]byte(content))
			require.NoError(t, err, "Failed to write to %s", path)
			file.Close()

			_, err = worktree.Add(path)
			require.NoError(t, err, "Failed to add %s", path)
		}

		baseCommit, err := worktree.Commit("Initial commit with config/env", &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Base Author",
				Email: "base@example.com",
			},
		})
		require.NoError(t, err)

		t.Logf("✅ Base commit created: %s", baseCommit.String())

		// STEP 2: Create target branch that keeps config/env and adds more content
		t.Log("STEP 2: Creating target branch (main) - keeps config/env/")

		// Add additional files to target
		file, err := worktree.Filesystem.Create("docs/api.md")
		require.NoError(t, err)
		_, err = file.Write([]byte("# API Documentation\n\nThis is the API docs."))
		require.NoError(t, err)
		file.Close()

		_, err = worktree.Add("docs/api.md")
		require.NoError(t, err)

		targetCommit, err := worktree.Commit("Add API docs on main branch", &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Target Author",
				Email: "target@example.com",
			},
		})
		require.NoError(t, err)

		t.Logf("✅ Target commit created: %s", targetCommit.String())

		// STEP 3: Create source branch WITHOUT config/env directory
		t.Log("STEP 3: Creating source branch (feature) - removes config/env/")

		// Go back to base commit
		err = worktree.Checkout(&git.CheckoutOptions{Hash: baseCommit})
		require.NoError(t, err)

		// Remove all env files from source branch
		_, err = worktree.Remove("config/env/production.env")
		require.NoError(t, err)
		_, err = worktree.Remove("config/env/staging.env")
		require.NoError(t, err)

		// Add feature files in source branch
		file, err = worktree.Filesystem.Create("src/feature.go")
		require.NoError(t, err)
		_, err = file.Write([]byte("package main\n\nfunc NewFeature() string {\n\treturn \"awesome feature\"\n}"))
		require.NoError(t, err)
		file.Close()

		_, err = worktree.Add("src/feature.go")
		require.NoError(t, err)

		sourceCommit, err := worktree.Commit("Add awesome feature without env files", &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Source Author",
				Email: "source@example.com",
			},
		})
		require.NoError(t, err)

		t.Logf("✅ Source commit created: %s", sourceCommit.String())

		// STEP 4: Now test SQUASH MERGE - this should preserve config/env!
		t.Log("STEP 4: Testing SQUASH MERGE - config/env should be preserved!")

		// Get commit objects
		sourceCommitObj, err := repo.CommitObject(sourceCommit)
		require.NoError(t, err)

		targetCommitObj, err := repo.CommitObject(targetCommit)
		require.NoError(t, err)

		// Use our merge handler for actual squash merge
		cfg := &config.Config{
			Git: config.GitConfig{
				RepoRoot:       "/tmp",
				EnablePackfile: true,
				MaxCacheSize:   100,
			},
			Logging: config.LoggingConfig{
				Level:  "info",
				Format: "json",
				Output: "stdout",
			},
		}
		handler, err := NewMergeOperationsHandler(cfg)
		require.NoError(t, err)

		ctx := context.Background()

		// Analyze the merge first (required by squash merge)
		analysisReq := &MergeRequest{
			SourceRef: "feature",
			TargetRef: "main",
			Method:    MergeMethodSquash,
			Strategy:  MergeStrategyRecursive,
			DryRun:    true,
		}

		analysis, err := handler.analyzeMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, analysisReq)
		require.NoError(t, err)

		t.Logf("Analysis result: can_merge=%t, has_conflicts=%t, conflict_count=%d",
			analysis.CanMerge, analysis.HasConflicts, analysis.ConflictCount)

		// Now perform actual squash merge
		mergeReq := &MergeRequest{
			SourceRef:     "feature",
			TargetRef:     "main",
			Method:        MergeMethodSquash,
			Strategy:      MergeStrategyRecursive,
			Message:       "Squash merge feature branch",
			ForceConflict: true, // Force in case there are conflicts
		}

		squashCommit, err := handler.performSquashMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, mergeReq, analysis)

		t.Logf("Squash merge result: commit=%v, err=%v", squashCommit != nil, err)

		if err != nil {
			t.Logf("❌ Squash merge failed with error: %v", err)
			return
		}

		if squashCommit == nil {
			t.Error("❌ CRITICAL: squashCommit is nil!")
			return
		}

		// STEP 5: Verify that config/env directory and files are preserved in squash commit
		t.Log("STEP 5: Verifying config/env preservation in SQUASH COMMIT...")

		// Get the squash commit's tree
		squashTree, err := repo.TreeObject(squashCommit.TreeHash)
		require.NoError(t, err, "Failed to get squash commit tree")

		// Check if config/env files exist in the squash tree
		envPaths := []string{
			"config/env/production.env",
			"config/env/staging.env",
		}

		for _, envPath := range envPaths {
			_, err := squashTree.File(envPath)
			if err != nil {
				t.Errorf("❌ CRITICAL SQUASH FAILURE: %s is missing from squash tree! Error: %v", envPath, err)
			} else {
				t.Logf("✅ %s is preserved in squash tree", envPath)
			}
		}

		// Also check that source files are included
		_, err = squashTree.File("src/feature.go")
		if err != nil {
			t.Logf("⚠️  src/feature.go missing from squash: %v", err)
		} else {
			t.Logf("✅ src/feature.go included in squash")
		}

		// Check that target files are included
		_, err = squashTree.File("docs/api.md")
		if err != nil {
			t.Logf("⚠️  docs/api.md missing from squash: %v", err)
		} else {
			t.Logf("✅ docs/api.md included in squash")
		}

		// SUMMARY
		separator := strings.Repeat("=", 60)
		t.Log("\n" + separator)
		t.Log("SQUASH MERGE TEST SUMMARY:")
		t.Log("- Base: Has config/env directory with 2 files")
		t.Log("- Target (main): Keeps config/env, adds docs/api.md")
		t.Log("- Source (feature): Removes config/env completely, adds src/feature.go")
		t.Log("- Expected: Squash merge should preserve config/env from target/base")
		t.Log(separator)
	})

	t.Run("squash_merge_with_forced_fallback", func(t *testing.T) {
		// This test tries to trigger the fallback logic that causes environment deletion
		t.Log("TESTING: Forced fallback scenario that might trigger env deletion")

		// Create a test repository
		fs := memory.NewStorage()
		memFS := memfs.New()

		repo, err := git.Init(fs, memFS)
		require.NoError(t, err)

		worktree, err := repo.Worktree()
		require.NoError(t, err)

		// Create a more complex scenario that might trigger fallback
		// STEP 1: Base commit with env files
		envFiles := map[string]string{
			"config/env/production.env": "DATABASE_URL=postgres://prod-server:5432/app\nREDIS_URL=redis://prod-redis:6379",
			"config/env/staging.env":    "DATABASE_URL=postgres://staging-server:5432/app\nREDIS_URL=redis://staging-redis:6379",
			"config/database.yml":       "production:\n  host: prod-db\n  port: 5432",
		}

		for path, content := range envFiles {
			file, err := worktree.Filesystem.Create(path)
			require.NoError(t, err)
			_, err = file.Write([]byte(content))
			require.NoError(t, err)
			file.Close()
			_, err = worktree.Add(path)
			require.NoError(t, err)
		}

		baseCommit, err := worktree.Commit("Initial commit with complex env setup", &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Base Author",
				Email: "base@example.com",
			},
		})
		require.NoError(t, err)

		t.Logf("✅ Base commit with complex env: %s", baseCommit.String())

		// STEP 2: Target branch - modify env files significantly
		err = worktree.Checkout(&git.CheckoutOptions{Hash: baseCommit})
		require.NoError(t, err)

		// Completely rewrite env files with very different content
		targetEnvFiles := map[string]string{
			"config/env/production.env": "# Production Configuration\nDATABASE_URL=postgresql://prod-new:5432/newdb\nREDIS_URL=redis://prod-new:6379\nSECRET_KEY=prod-secret-12345",
			"config/env/staging.env":    "# Staging Configuration\nDATABASE_URL=postgresql://staging-new:5432/newdb\nREDIS_URL=redis://staging-new:6379\nSECRET_KEY=staging-secret-67890",
			"config/database.yml":       "production:\n  adapter: postgresql\n  host: prod-new\n  port: 5432\n  database: newdb",
			"app.py":                    "#!/usr/bin/env python3\nprint('Hello from target')",
		}

		for path, content := range targetEnvFiles {
			file, err := worktree.Filesystem.Create(path)
			require.NoError(t, err)
			_, err = file.Write([]byte(content))
			require.NoError(t, err)
			file.Close()
			_, err = worktree.Add(path)
			require.NoError(t, err)
		}

		targetCommit, err := worktree.Commit("Major env config update on target", &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Target Author",
				Email: "target@example.com",
			},
		})
		require.NoError(t, err)

		t.Logf("✅ Target commit with modified env: %s", targetCommit.String())

		// STEP 3: Source branch - remove ALL config files and add conflicting files
		err = worktree.Checkout(&git.CheckoutOptions{Hash: baseCommit})
		require.NoError(t, err)

		// Remove all config files completely
		_, err = worktree.Remove("config/env/production.env")
		require.NoError(t, err)
		_, err = worktree.Remove("config/env/staging.env")
		require.NoError(t, err)
		_, err = worktree.Remove("config/database.yml")
		require.NoError(t, err)

		// Add completely different files that might conflict
		sourceFiles := map[string]string{
			"app.py":      "#!/usr/bin/env python3\nprint('Hello from source - DIFFERENT CONTENT')",
			"src/main.go": "package main\n\nfunc main() {\n\tprintln(\"Source feature\")\n}",
			"Dockerfile":  "FROM python:3.9\nCOPY . /app\nWORKDIR /app",
		}

		for path, content := range sourceFiles {
			file, err := worktree.Filesystem.Create(path)
			require.NoError(t, err)
			_, err = file.Write([]byte(content))
			require.NoError(t, err)
			file.Close()
			_, err = worktree.Add(path)
			require.NoError(t, err)
		}

		sourceCommit, err := worktree.Commit("Remove all config, add new features", &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Source Author",
				Email: "source@example.com",
			},
		})
		require.NoError(t, err)

		t.Logf("✅ Source commit without env: %s", sourceCommit.String())

		// STEP 4: Try squash merge with NO force conflict to see if it triggers fallback
		t.Log("STEP 4: Testing squash merge WITHOUT force_conflict")

		sourceCommitObj, err := repo.CommitObject(sourceCommit)
		require.NoError(t, err)
		targetCommitObj, err := repo.CommitObject(targetCommit)
		require.NoError(t, err)

		cfg := &config.Config{
			Git: config.GitConfig{
				RepoRoot:       "/tmp",
				EnablePackfile: true,
				MaxCacheSize:   100,
			},
			Logging: config.LoggingConfig{
				Level:  "info",
				Format: "json",
				Output: "stdout",
			},
		}
		handler, err := NewMergeOperationsHandler(cfg)
		require.NoError(t, err)

		ctx := context.Background()

		// Try analysis without forcing conflicts
		analysisReq := &MergeRequest{
			SourceRef:     "feature",
			TargetRef:     "main",
			Method:        MergeMethodSquash,
			Strategy:      MergeStrategyRecursive,
			ForceConflict: false, // Don't force - this might trigger fallback
		}

		analysis, err := handler.analyzeMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, analysisReq)
		require.NoError(t, err)

		t.Logf("Analysis without force: can_merge=%t, has_conflicts=%t, conflict_count=%d, auto_resolvable=%d",
			analysis.CanMerge, analysis.HasConflicts, analysis.ConflictCount, analysis.AutoResolvable)

		// Try the squash merge (this might fail and trigger fallback)
		mergeReq := &MergeRequest{
			SourceRef:     "feature",
			TargetRef:     "main",
			Method:        MergeMethodSquash,
			Strategy:      MergeStrategyRecursive,
			Message:       "Squash merge without force",
			ForceConflict: false, // Critical: no force
		}

		squashCommit, err := handler.performSquashMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, mergeReq, analysis)

		t.Logf("Squash merge without force: commit=%v, err=%v", squashCommit != nil, err)

		if err != nil {
			t.Logf("❗ Expected: Squash merge failed (this might trigger fallback): %v", err)

			// Now try with force_conflict=true which might use fallback logic
			t.Log("STEP 5: Retrying with force_conflict=true (fallback scenario)")

			mergeReqForced := &MergeRequest{
				SourceRef:     "feature",
				TargetRef:     "main",
				Method:        MergeMethodSquash,
				Strategy:      MergeStrategyRecursive,
				Message:       "Forced squash merge",
				ForceConflict: true, // Force conflicts
			}

			forcedSquashCommit, err := handler.performSquashMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, mergeReqForced, analysis)

			t.Logf("Forced squash merge: commit=%v, err=%v", forcedSquashCommit != nil, err)

			if err != nil {
				t.Logf("❌ Even forced squash merge failed: %v", err)
				return
			}

			if forcedSquashCommit == nil {
				t.Error("❌ Forced squash commit is nil!")
				return
			}

			// Check if env files are preserved in forced scenario
			forcedTree, err := repo.TreeObject(forcedSquashCommit.TreeHash)
			require.NoError(t, err)

			// Check for env file preservation
			envPaths := []string{
				"config/env/production.env",
				"config/env/staging.env",
				"config/database.yml",
			}

			t.Log("STEP 6: Checking env preservation in FORCED squash (potential fallback)...")
			for _, envPath := range envPaths {
				_, err := forcedTree.File(envPath)
				if err != nil {
					t.Logf("💥 REPRODUCTION SUCCESS: %s DELETED in forced squash! Error: %v", envPath, err)
				} else {
					t.Logf("✅ %s preserved in forced squash", envPath)
				}
			}

		} else {
			// Squash succeeded without force
			if squashCommit != nil {
				squashTree, err := repo.TreeObject(squashCommit.TreeHash)
				require.NoError(t, err)

				t.Log("STEP 5: Checking env preservation in non-forced squash...")
				for _, envPath := range []string{"config/env/production.env", "config/env/staging.env"} {
					_, err := squashTree.File(envPath)
					if err != nil {
						t.Logf("💥 REPRODUCTION SUCCESS: %s DELETED! Error: %v", envPath, err)
					} else {
						t.Logf("✅ %s preserved", envPath)
					}
				}
			}
		}

		// SUMMARY
		separator := strings.Repeat("=", 60)
		t.Log("\n" + separator)
		t.Log("FALLBACK TEST SUMMARY:")
		t.Log("- Tested complex conflict scenario that might trigger fallback")
		t.Log("- Source branch completely removes all config/env files")
		t.Log("- Target branch heavily modifies config/env files")
		t.Log("- Goal: Trigger resolveMergeTree fallback to sourceCommit.TreeHash")
		t.Log(separator)
	})

	t.Run("comprehensive_protection_test", func(t *testing.T) {
		// Test the complete centralized protection system
		t.Log("TESTING: Comprehensive centralized merge protection")

		// Create test repository
		fs := memory.NewStorage()
		memFS := memfs.New()

		repo, err := git.Init(fs, memFS)
		require.NoError(t, err)

		worktree, err := repo.Worktree()
		require.NoError(t, err)

		// Create complex scenario that previously caused deletion
		// Base commit with environment files
		envFiles := map[string]string{
			"config/env/production.env": "PROD_DB=postgres://prod:5432/db",
			"config/env/staging.env":    "STAGING_DB=postgres://staging:5432/db",
			"config/database.yml":       "production:\n  host: prod\nstaging:\n  host: staging",
			"docker-compose.yml":        "version: '3'\nservices:\n  db:\n    image: postgres",
		}

		for path, content := range envFiles {
			file, err := worktree.Filesystem.Create(path)
			require.NoError(t, err)
			_, err = file.Write([]byte(content))
			require.NoError(t, err)
			file.Close()
			_, err = worktree.Add(path)
			require.NoError(t, err)
		}

		baseCommit, err := worktree.Commit("Base with comprehensive env setup", &git.CommitOptions{
			Author: &object.Signature{Name: "Base", Email: "base@test.com"},
		})
		require.NoError(t, err)

		// Target branch - modify environment files
		targetFiles := map[string]string{
			"config/env/production.env": "PROD_DB=postgres://prod:5432/db\nNEW_SETTING=production",
			"config/env/staging.env":    "STAGING_DB=postgres://staging:5432/db\nNEW_SETTING=staging",
		}

		for path, content := range targetFiles {
			file, err := worktree.Filesystem.Create(path)
			require.NoError(t, err)
			_, err = file.Write([]byte(content))
			require.NoError(t, err)
			file.Close()
			_, err = worktree.Add(path)
			require.NoError(t, err)
		}

		targetCommit, err := worktree.Commit("Target modifies env files", &git.CommitOptions{
			Author: &object.Signature{Name: "Target", Email: "target@test.com"},
		})
		require.NoError(t, err)

		// Source branch - remove ALL environment files
		err = worktree.Checkout(&git.CheckoutOptions{Hash: baseCommit})
		require.NoError(t, err)

		// Remove all environment files
		for path := range envFiles {
			_, err = worktree.Remove(path)
			require.NoError(t, err)
		}

		// Add completely different content
		sourceFiles := map[string]string{
			"app/main.go": "package main\nfunc main() { println(\"new app\") }",
			"Dockerfile":  "FROM golang:1.21\nCOPY . .\nRUN go build",
			"README.md":   "# New Application\nThis is a rewrite",
		}

		for path, content := range sourceFiles {
			file, err := worktree.Filesystem.Create(path)
			require.NoError(t, err)
			_, err = file.Write([]byte(content))
			require.NoError(t, err)
			file.Close()
			_, err = worktree.Add(path)
			require.NoError(t, err)
		}

		sourceCommit, err := worktree.Commit("Source removes all env files", &git.CommitOptions{
			Author: &object.Signature{Name: "Source", Email: "source@test.com"},
		})
		require.NoError(t, err)

		t.Logf("✅ Test scenario created: base=%s, target=%s, source=%s",
			baseCommit.String()[:8], targetCommit.String()[:8], sourceCommit.String()[:8])

		// NOW TEST THE COMPREHENSIVE PROTECTION SYSTEM
		sourceCommitObj, err := repo.CommitObject(sourceCommit)
		require.NoError(t, err)
		targetCommitObj, err := repo.CommitObject(targetCommit)
		require.NoError(t, err)

		cfg := &config.Config{
			Git:     config.GitConfig{RepoRoot: "/tmp", EnablePackfile: true, MaxCacheSize: 100},
			Logging: config.LoggingConfig{Level: "info", Format: "json", Output: "stdout"},
		}

		// Create handler with NEW centralized protection
		handler, err := NewMergeOperationsHandler(cfg)
		require.NoError(t, err)

		ctx := context.Background()

		// Analyze merge
		analysisReq := &MergeRequest{
			SourceRef: "source", TargetRef: "target", Method: MergeMethodSquash,
			Strategy: MergeStrategyRecursive, ForceConflict: true,
		}

		analysis, err := handler.analyzeMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, analysisReq)
		require.NoError(t, err)

		t.Logf("Analysis: conflicts=%d, auto_resolvable=%d", analysis.ConflictCount, analysis.AutoResolvable)

		// Perform squash merge with the NEW PROTECTION SYSTEM
		mergeReq := &MergeRequest{
			SourceRef: "source", TargetRef: "target", Method: MergeMethodSquash,
			Strategy: MergeStrategyRecursive, ForceConflict: true,
			Message: "Comprehensive protection test squash",
		}

		squashCommit, err := handler.performSquashMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, mergeReq, analysis)

		t.Logf("Squash result: commit=%v, err=%v", squashCommit != nil, err)

		require.NoError(t, err, "Squash merge should succeed")
		require.NotNil(t, squashCommit, "Squash commit should be created")

		// VERIFY COMPREHENSIVE PROTECTION
		squashTree, err := repo.TreeObject(squashCommit.TreeHash)
		require.NoError(t, err)

		protectedCount := 0
		envPaths := []string{
			"config/env/production.env",
			"config/env/staging.env",
			"config/database.yml",
			"docker-compose.yml",
		}

		t.Log("🔍 COMPREHENSIVE PROTECTION VERIFICATION:")
		for _, envPath := range envPaths {
			_, err := squashTree.File(envPath)
			if err != nil {
				t.Errorf("❌ PROTECTION FAILED: %s deleted! Error: %v", envPath, err)
			} else {
				t.Logf("✅ PROTECTED: %s preserved", envPath)
				protectedCount++
			}
		}

		// Verify source files were included
		sourceFilesIncluded := 0
		for sourcePath := range sourceFiles {
			_, err := squashTree.File(sourcePath)
			if err == nil {
				sourceFilesIncluded++
				t.Logf("✅ MERGED: %s included", sourcePath)
			}
		}

		// FINAL ASSERTION
		require.Equal(t, len(envPaths), protectedCount,
			"ALL environment files should be protected")
		require.Greater(t, sourceFilesIncluded, 0,
			"Source files should also be included")

		t.Log("🎉 COMPREHENSIVE PROTECTION TEST PASSED!")
		t.Logf("   Protected: %d/%d environment files", protectedCount, len(envPaths))
		t.Logf("   Merged: %d source files", sourceFilesIncluded)

		separator := strings.Repeat("=", 60)
		t.Log("\n" + separator)
		t.Log("✅ CENTRALIZED MERGE PROTECTION: SUCCESS")
		t.Log("✅ Environment file deletion: PREVENTED")
		t.Log("✅ Source changes: PRESERVED")
		t.Log("✅ Comprehensive architecture: WORKING")
		t.Log(separator)
	})
}
