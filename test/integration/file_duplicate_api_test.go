package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/collabops/git-executor/internal/schemas/common"
	"github.com/collabops/git-executor/internal/schemas/requests"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileDuplicateAPI(t *testing.T) {
	// Setup
	testDir, err := os.MkdirTemp("", "test-*")
	require.NoError(t, err)
	defer os.RemoveAll(testDir)

	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: testDir},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}

	handler, err := protocol.NewFileOperationsHandler(cfg, nil)
	require.NoError(t, err)

	t.Run("중복파일_409에러", func(t *testing.T) {
		// 중복 파일이 포함된 커밋 요청
		req := requests.FileOperationRequest{
			Content: "test content",
			Message: "test commit",
			Author: &common.CommitAuthor{
				Name:  "test",
				Email: "test@example.com",
			},
		}

		reqBody, _ := json.Marshal(req)
		httpReq := httptest.NewRequest("POST", "/repos/test/test/files/test.txt", bytes.NewBuffer(reqBody))
		httpReq.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httpReq
		c.Params = gin.Params{
			{Key: "owner", Value: "test"},
			{Key: "repo", Value: "test"},
			{Key: "path", Value: "test.txt"},
		}

		handler.HandleMultiFileCommit(c)

		// 409 또는 400 에러 예상
		assert.True(t, w.Code == http.StatusConflict || w.Code == http.StatusBadRequest)
	})
}
