//go:build gogit

package merge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/gin-gonic/gin"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/require"
)

// Test_Merge_FileConflict_EmitsMergeConflictCodeAndParams 는 파일 충돌 merge
// 응답이 flat machine code MERGE_CONFLICT 와 보간용 params
// (source_ref/target_ref/conflict_count) 를 싣고, 기존 conflicts[] 는 보존
// 하는지 검증한다 (Issue-5360). git-executor 는 localization 을 하지 않고
// orchestrator 가 GIT_MERGE_CONFLICT 카탈로그로 보간한다.
func Test_Merge_FileConflict_EmitsMergeConflictCodeAndParams(t *testing.T) {
	repoRoot, owner, repoName, repo, _ := setupRepoForMergeHandlers(t)
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "error", Format: "json"},
		Git:     config.GitConfig{RepoRoot: repoRoot},
	}
	handler, err := NewMergeOperationsHandler(cfg)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)
	writeFile(t, wt, "conflict.txt", "base\n")
	_, _ = commitFile(t, wt, "conflict.txt", "base")
	require.NoError(t, wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	}))
	writeFile(t, wt, "conflict.txt", "feature\n")
	_, _ = commitFile(t, wt, "conflict.txt", "feature")
	require.NoError(t, wt.Checkout(&gogit.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")}))
	writeFile(t, wt, "conflict.txt", "main\n")
	_, _ = commitFile(t, wt, "conflict.txt", "main")

	router := gin.New()
	router.POST("/repos/:owner/:repo/merges", adapt(handler.HandleMerge))
	bodyBytes, _ := json.Marshal(&MergeRequest{SourceRef: "feature", TargetRef: "main", ForceConflict: false})
	req := httptest.NewRequest(http.MethodPost, "/repos/"+owner+"/"+repoName+"/merges", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "body: %s", w.Body.String())

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, false, resp["success"])
	require.Equal(t, "MERGE_CONFLICT", resp["code"], "body: %s", w.Body.String())
	require.NotNil(t, resp["conflicts"], "existing conflicts[] must be preserved")

	params, ok := resp["params"].(map[string]interface{})
	require.True(t, ok, "params must be present and flat: %s", w.Body.String())
	require.Equal(t, "feature", params["source_ref"])
	require.Equal(t, "main", params["target_ref"])
	require.NotEmpty(t, params["conflict_count"], "conflict_count param must be set")
}
