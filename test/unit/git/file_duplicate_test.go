package git

import (
	"context"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileDuplicateValidation(t *testing.T) {
	// Setup
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: "/tmp/test"},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}

	logger, err := logging.NewLogger(cfg.Logging)
	require.NoError(t, err)

	pathConfig, err := repository.NewPathConfig(cfg.Git.RepoRoot)
	require.NoError(t, err)

	gom, err := git.NewGitObjectManager(pathConfig, logger)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("중복파일_에러발생", func(t *testing.T) {
		changes := []git.FileChange{
			{Path: "file1.txt", Operation: git.FileOperationCreate, Content: []byte("content1")},
			{Path: "file1.txt", Operation: git.FileOperationUpdate, Content: []byte("content2")}, // 중복!
		}

		err := gom.ValidateFileChanges(ctx, changes)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate path file1.txt")
	})

	t.Run("정상파일_통과", func(t *testing.T) {
		changes := []git.FileChange{
			{Path: "file1.txt", Operation: git.FileOperationCreate, Content: []byte("content1")},
			{Path: "file2.txt", Operation: git.FileOperationCreate, Content: []byte("content2")},
		}

		err := gom.ValidateFileChanges(ctx, changes)
		assert.NoError(t, err)
	})
}
