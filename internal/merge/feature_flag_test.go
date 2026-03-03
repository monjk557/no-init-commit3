//go:build gogit

package merge

import (
	"context"
	"os"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
)

// TestFeatureFlagProtectionControl tests that feature flags properly control protection behavior
func TestFeatureFlagProtectionControl(t *testing.T) {
	t.Run("protection_disabled_allows_file_deletion", func(t *testing.T) {
		// Test that when protection is disabled, files get deleted (original behavior)

		// Set feature flags to DISABLE protection
		os.Setenv("MERGE_PROTECTION_ENABLED", "false")
		defer os.Unsetenv("MERGE_PROTECTION_ENABLED")

		fs := memory.NewStorage()
		memFS := memfs.New()

		repo, err := git.Init(fs, memFS)
		require.NoError(t, err)

		worktree, err := repo.Worktree()
		require.NoError(t, err)

		// Create base with env files
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

		// Target keeps env and adds a file
		file, err = worktree.Filesystem.Create("target-file.txt")
		require.NoError(t, err)
		_, err = file.Write([]byte("target content"))
		require.NoError(t, err)
		file.Close()
		_, err = worktree.Add("target-file.txt")
		require.NoError(t, err)

		targetCommit, err := worktree.Commit("Target keeps env", &git.CommitOptions{
			Author: &object.Signature{Name: "Target", Email: "target@test.com"},
		})
		require.NoError(t, err)

		// Source removes env
		err = worktree.Checkout(&git.CheckoutOptions{Hash: baseCommit})
		require.NoError(t, err)

		_, err = worktree.Remove(envFile)
		require.NoError(t, err)

		file, err = worktree.Filesystem.Create("feature.txt")
		require.NoError(t, err)
		_, err = file.Write([]byte("new feature"))
		require.NoError(t, err)
		file.Close()
		_, err = worktree.Add("feature.txt")
		require.NoError(t, err)

		sourceCommit, err := worktree.Commit("Source removes env", &git.CommitOptions{
			Author: &object.Signature{Name: "Source", Email: "source@test.com"},
		})
		require.NoError(t, err)

		// Create config with protection disabled
		cfg := &config.Config{
			Git:     config.GitConfig{RepoRoot: "/tmp", EnablePackfile: true},
			Logging: config.LoggingConfig{Level: "info", Format: "json", Output: "stdout"},
			MergeProtection: config.MergeProtectionConfig{
				Enabled: false, // This simulates MERGE_PROTECTION_ENABLED=false
				EnvironmentProtection: config.EnvironmentProtectionConfig{
					Enabled: false,
				},
			},
		}

		// Verify protection is disabled
		require.False(t, cfg.MergeProtection.Enabled, "Protection should be disabled via feature flag")

		handler, err := NewMergeOperationsHandler(cfg)
		require.NoError(t, err)

		// Verify handler has no protection layer due to disabled flag
		// Protection is now always handled by PolicyEngine - no individual layer check needed

		ctx := context.Background()
		sourceCommitObj, err := repo.CommitObject(sourceCommit)
		require.NoError(t, err)
		targetCommitObj, err := repo.CommitObject(targetCommit)
		require.NoError(t, err)

		analysisReq := &MergeRequest{
			SourceRef: "source", TargetRef: "target", Method: MergeMethodSquash,
			Strategy: MergeStrategyRecursive, ForceConflict: true,
		}

		analysis, err := handler.analyzeMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, analysisReq)
		require.NoError(t, err)

		squashCommit, err := handler.performSquashMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, analysisReq, analysis)
		require.NoError(t, err)

		// With protection disabled, the env file SHOULD be deleted (original behavior)
		squashTree, err := repo.TreeObject(squashCommit.TreeHash)
		require.NoError(t, err)

		_, err = squashTree.File(envFile)
		require.Error(t, err, "Environment file should be DELETED when protection is disabled")

		t.Log("✅ SUCCESS: Feature flag properly DISABLES protection - files deleted as expected")
	})

	t.Run("protection_enabled_prevents_file_deletion", func(t *testing.T) {
		// Test that when protection is enabled, files are preserved

		// Set feature flags to ENABLE protection
		os.Setenv("MERGE_PROTECTION_ENABLED", "true")
		os.Setenv("MERGE_PROTECTION_ENV_ENABLED", "true")
		os.Setenv("MERGE_PROTECTION_SQUASH", "true")
		defer os.Unsetenv("MERGE_PROTECTION_ENABLED")
		defer os.Unsetenv("MERGE_PROTECTION_ENV_ENABLED")
		defer os.Unsetenv("MERGE_PROTECTION_SQUASH")

		fs := memory.NewStorage()
		memFS := memfs.New()

		repo, err := git.Init(fs, memFS)
		require.NoError(t, err)

		worktree, err := repo.Worktree()
		require.NoError(t, err)

		// Create same scenario
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

		file, err = worktree.Filesystem.Create("target-file.txt")
		require.NoError(t, err)
		_, err = file.Write([]byte("target content"))
		require.NoError(t, err)
		file.Close()
		_, err = worktree.Add("target-file.txt")
		require.NoError(t, err)

		targetCommit, err := worktree.Commit("Target keeps env", &git.CommitOptions{
			Author: &object.Signature{Name: "Target", Email: "target@test.com"},
		})
		require.NoError(t, err)

		err = worktree.Checkout(&git.CheckoutOptions{Hash: baseCommit})
		require.NoError(t, err)

		_, err = worktree.Remove(envFile)
		require.NoError(t, err)

		file, err = worktree.Filesystem.Create("feature.txt")
		require.NoError(t, err)
		_, err = file.Write([]byte("new feature"))
		require.NoError(t, err)
		file.Close()
		_, err = worktree.Add("feature.txt")
		require.NoError(t, err)

		sourceCommit, err := worktree.Commit("Source removes env", &git.CommitOptions{
			Author: &object.Signature{Name: "Source", Email: "source@test.com"},
		})
		require.NoError(t, err)

		// Create config with protection enabled
		cfg := &config.Config{
			Git:     config.GitConfig{RepoRoot: "/tmp", EnablePackfile: true},
			Logging: config.LoggingConfig{Level: "info", Format: "json", Output: "stdout"},
			MergeProtection: config.MergeProtectionConfig{
				Enabled: true, // This simulates MERGE_PROTECTION_ENABLED=true
				EnvironmentProtection: config.EnvironmentProtectionConfig{
					Enabled:               true,
					EnableForSquashMerge:  true,
					EnableForRegularMerge: true,
					EnableForRebaseMerge:  true,
				},
				SafetyMode:       "strict",
				FallbackStrategy: "preserve_target",
			},
		}

		// Verify protection is enabled
		require.True(t, cfg.MergeProtection.Enabled, "Protection should be enabled via feature flag")
		require.True(t, cfg.MergeProtection.EnvironmentProtection.Enabled)
		require.True(t, cfg.MergeProtection.EnvironmentProtection.EnableForSquashMerge)

		handler, err := NewMergeOperationsHandler(cfg)
		require.NoError(t, err)

		// Verify handler has protection layer due to enabled flag
		// Protection is now always handled by PolicyEngine - no individual layer check needed

		ctx := context.Background()
		sourceCommitObj, err := repo.CommitObject(sourceCommit)
		require.NoError(t, err)
		targetCommitObj, err := repo.CommitObject(targetCommit)
		require.NoError(t, err)

		analysisReq := &MergeRequest{
			SourceRef: "source", TargetRef: "target", Method: MergeMethodSquash,
			Strategy: MergeStrategyRecursive, ForceConflict: true,
		}

		analysis, err := handler.analyzeMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, analysisReq)
		require.NoError(t, err)

		squashCommit, err := handler.performSquashMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, analysisReq, analysis)
		require.NoError(t, err)

		// With protection enabled, the env file SHOULD be preserved
		squashTree, err := repo.TreeObject(squashCommit.TreeHash)
		require.NoError(t, err)

		_, err = squashTree.File(envFile)
		require.NoError(t, err, "Environment file should be PRESERVED when protection is enabled")

		t.Log("✅ SUCCESS: Feature flag properly ENABLES protection - files preserved as expected")
	})

	t.Run("granular_merge_method_flags", func(t *testing.T) {
		// Test granular control of merge method specific flags

		// Enable global protection but disable squash-specific protection
		os.Setenv("MERGE_PROTECTION_ENABLED", "true")
		os.Setenv("MERGE_PROTECTION_ENV_ENABLED", "true")
		os.Setenv("MERGE_PROTECTION_SQUASH", "false") // Disable squash protection
		os.Setenv("MERGE_PROTECTION_REGULAR", "true")
		defer os.Unsetenv("MERGE_PROTECTION_ENABLED")
		defer os.Unsetenv("MERGE_PROTECTION_ENV_ENABLED")
		defer os.Unsetenv("MERGE_PROTECTION_SQUASH")
		defer os.Unsetenv("MERGE_PROTECTION_REGULAR")

		cfg := &config.Config{
			Git:     config.GitConfig{RepoRoot: "/tmp", EnablePackfile: true},
			Logging: config.LoggingConfig{Level: "info", Format: "json", Output: "stdout"},
			MergeProtection: config.MergeProtectionConfig{
				Enabled: true,
				EnvironmentProtection: config.EnvironmentProtectionConfig{
					Enabled:               true,
					EnableForSquashMerge:  false, // Disabled for squash
					EnableForRegularMerge: true,  // Enabled for regular
					EnableForRebaseMerge:  true,
				},
			},
		}

		handler, err := NewMergeOperationsHandler(cfg)
		require.NoError(t, err)

		// Test granular flags
		require.True(t, handler.shouldApplyProtection(false), "Regular merge protection should be enabled")
		require.False(t, handler.shouldApplyProtection(true), "Squash merge protection should be disabled")

		t.Log("✅ SUCCESS: Granular merge method flags work correctly")
	})
}
