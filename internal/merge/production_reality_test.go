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

// TestProductionReality tests if our fix actually works in real production scenarios
// This reproduces the EXACT scenario the user experienced
func TestProductionReality(t *testing.T) {
	t.Run("exact_user_scenario_feature_branch_to_main", func(t *testing.T) {
		// This reproduces the exact scenario where user's environment files got deleted

		fs := memory.NewStorage()
		memFS := memfs.New()

		repo, err := git.Init(fs, memFS)
		require.NoError(t, err)

		worktree, err := repo.Worktree()
		require.NoError(t, err)

		t.Log("=== REPRODUCING EXACT PRODUCTION SCENARIO ===")
		t.Log("Scenario: Feature branch removes env files, gets squash-merged to main")
		t.Log("Expected: config/env/build-it/api-piper.env should be preserved")

		// STEP 1: Create main branch with production environment files
		productionEnvFiles := map[string]string{
			"config/env/build-it/api-piper.env": `API_URL=https://build-it.production.com
API_KEY=prod-secret-key
DATABASE_URL=postgres://prod-db:5432/api_db
REDIS_URL=redis://prod-redis:6379/0
LOG_LEVEL=info
DEBUG=false`,
			"config/env/build-it/database.env": `DB_HOST=build-it-prod-db
DB_PORT=5432
DB_NAME=build_it_prod
DB_USER=api_user
DB_POOL_SIZE=20`,
			"config/env/staging/app.env": `APP_ENV=staging
DEBUG=true
LOG_LEVEL=debug
RATE_LIMIT=1000`,
			"config/database.yml": `production:
  adapter: postgresql
  host: prod-db
  database: api_prod
  username: api_user
staging:
  adapter: postgresql
  host: staging-db
  database: api_staging`,
			"docker-compose.yml": `version: '3.8'
services:
  api:
    build: .
    environment:
      - NODE_ENV=production
    volumes:
      - ./config:/app/config
  redis:
    image: redis:alpine
  postgres:
    image: postgres:13`,
		}

		// Create main branch with all production files
		for path, content := range productionEnvFiles {
			file, err := worktree.Filesystem.Create(path)
			require.NoError(t, err)
			_, err = file.Write([]byte(content))
			require.NoError(t, err)
			file.Close()
			_, err = worktree.Add(path)
			require.NoError(t, err)
		}

		// Add some normal application files
		appFiles := map[string]string{
			"package.json": `{"name": "api", "version": "1.0.0"}`,
			"src/app.js":   `const express = require('express'); const app = express();`,
			"README.md":    `# API Service`,
			"Dockerfile":   `FROM node:16\nCOPY . /app\nWORKDIR /app\nRUN npm install`,
			".gitignore":   `node_modules\n.env.local\nlogs/`,
		}

		for path, content := range appFiles {
			file, err := worktree.Filesystem.Create(path)
			require.NoError(t, err)
			_, err = file.Write([]byte(content))
			require.NoError(t, err)
			file.Close()
			_, err = worktree.Add(path)
			require.NoError(t, err)
		}

		mainCommit, err := worktree.Commit("Production main branch with all configs", &git.CommitOptions{
			Author: &object.Signature{Name: "Main", Email: "main@company.com"},
		})
		require.NoError(t, err)
		t.Logf("✅ Main commit: %s", mainCommit.String())

		// STEP 2: Create feature branch that removes ALL environment files (common in development)
		err = worktree.Checkout(&git.CheckoutOptions{Hash: mainCommit})
		require.NoError(t, err)

		// Remove ALL environment files (this is what happened to user)
		for envPath := range productionEnvFiles {
			_, err = worktree.Remove(envPath)
			require.NoError(t, err)
			t.Logf("Removed from feature branch: %s", envPath)
		}

		// Add new feature code (this is normal development)
		featureFiles := map[string]string{
			"src/auth.js":        `module.exports = { authenticate: () => {} };`,
			"src/middleware.js":  `module.exports = { validate: () => {} };`,
			"tests/auth.test.js": `describe('auth', () => { it('works', () => {}); });`,
			"docs/auth.md":       `# Authentication Module`,
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

		// Update package.json (common in feature branches)
		packageFile, err := worktree.Filesystem.Create("package.json")
		require.NoError(t, err)
		_, err = packageFile.Write([]byte(`{"name": "api", "version": "1.1.0", "dependencies": {"express": "^4.18.0"}}`))
		require.NoError(t, err)
		packageFile.Close()
		_, err = worktree.Add("package.json")
		require.NoError(t, err)

		featureCommit, err := worktree.Commit("Feature: Add authentication module", &git.CommitOptions{
			Author: &object.Signature{Name: "Developer", Email: "dev@company.com"},
		})
		require.NoError(t, err)
		t.Logf("✅ Feature commit: %s", featureCommit.String())

		// STEP 3: Simulate GitHub/GitLab squash merge (this is where files got deleted)
		t.Log("\n=== SIMULATING GITHUB SQUASH MERGE ===")
		t.Log("This is the exact moment where environment files got deleted in production")

		// Test with protection ENABLED
		cfg := &config.Config{
			Git:     config.GitConfig{RepoRoot: "/tmp", EnablePackfile: true},
			Logging: config.LoggingConfig{Level: "info", Format: "json", Output: "stdout"},
			MergeProtection: config.MergeProtectionConfig{
				Enabled: true,
				EnvironmentProtection: config.EnvironmentProtectionConfig{
					Enabled:              true,
					EnableForSquashMerge: true,
					StrictMode:           true,
				},
				SafetyMode: "strict",
			},
		}

		handler, err := NewMergeOperationsHandler(cfg)
		require.NoError(t, err)

		ctx := context.Background()
		featureCommitObj, err := repo.CommitObject(featureCommit)
		require.NoError(t, err)
		mainCommitObj, err := repo.CommitObject(mainCommit)
		require.NoError(t, err)

		// Analyze the merge (feature -> main)
		analysisReq := &MergeRequest{
			SourceRef: "feature", TargetRef: "main",
			Method: MergeMethodSquash, Strategy: MergeStrategyRecursive,
			ForceConflict: true, Message: "Feature: Add authentication module (#123)",
		}

		analysis, err := handler.analyzeMerge(ctx, repo, featureCommitObj.Hash, mainCommitObj.Hash, analysisReq)
		require.NoError(t, err)
		t.Logf("Analysis: conflicts=%d, auto_resolvable=%d", analysis.ConflictCount, analysis.AutoResolvable)

		// Perform the squash merge (this is where the magic should happen)
		squashCommit, err := handler.performSquashMerge(ctx, repo, featureCommitObj.Hash, mainCommitObj.Hash, analysisReq, analysis)
		require.NoError(t, err)
		require.NotNil(t, squashCommit)
		t.Logf("✅ Squash merge commit: %s", squashCommit.Hash.String())

		// STEP 4: CRITICAL VERIFICATION - Check if ALL environment files are preserved
		t.Log("\n=== CRITICAL VERIFICATION ===")
		t.Log("Checking if our protection system saved all environment files")

		squashTree, err := repo.TreeObject(squashCommit.TreeHash)
		require.NoError(t, err)

		preservedEnvFiles := 0
		deletedEnvFiles := []string{}
		preservedFeatureFiles := 0

		// Check all original environment files
		for envPath := range productionEnvFiles {
			_, err := squashTree.File(envPath)
			if err != nil {
				deletedEnvFiles = append(deletedEnvFiles, envPath)
				t.Errorf("💥 PRODUCTION FAILURE: %s was DELETED during squash merge", envPath)
			} else {
				preservedEnvFiles++
				t.Logf("✅ PROTECTED: %s preserved by protection system", envPath)
			}
		}

		// Check that new feature files were included
		for featurePath := range featureFiles {
			_, err := squashTree.File(featurePath)
			if err == nil {
				preservedFeatureFiles++
			}
		}

		// Final assessment
		t.Log("\n=== PRODUCTION REALITY CHECK ===")
		t.Logf("Environment files preserved: %d/%d", preservedEnvFiles, len(productionEnvFiles))
		t.Logf("Feature files included: %d/%d", preservedFeatureFiles, len(featureFiles))
		t.Logf("Environment files DELETED: %d", len(deletedEnvFiles))

		if len(deletedEnvFiles) > 0 {
			t.Log("❌ PRODUCTION FAILURE: The following critical files were deleted:")
			for _, deleted := range deletedEnvFiles {
				t.Logf("   - %s (CRITICAL PRODUCTION CONFIG LOST)", deleted)
			}
			t.Log("This would cause production outage, database connection loss, API failures")
		} else {
			t.Log("🎉 PRODUCTION SUCCESS: All environment files preserved!")
			t.Log("✅ API keys, database configs, Docker configs all safe")
			t.Log("✅ Feature code successfully merged")
			t.Log("✅ No production downtime risk")
		}

		// These assertions represent production safety
		require.Empty(t, deletedEnvFiles,
			"CRITICAL PRODUCTION SAFETY: No environment files should be deleted during feature merges")
		require.Equal(t, len(productionEnvFiles), preservedEnvFiles,
			"All production environment files must be preserved")
		require.Greater(t, preservedFeatureFiles, 0,
			"Feature files should be included in the merge")

		// Verify specific critical file mentioned by user
		_, err = squashTree.File("config/env/build-it/api-piper.env")
		require.NoError(t, err, "User's specific file config/env/build-it/api-piper.env must be preserved")

		t.Log("\n🎉 PRODUCTION REALITY TEST PASSED")
		t.Log("The merge protection system successfully prevents environment file deletion")
		t.Log("User's production issue has been resolved!")
	})
}
