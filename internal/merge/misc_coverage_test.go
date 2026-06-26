package merge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/api/typed"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// adapter_env.go
// ---------------------------------------------------------------------------

func Test_MergeEnv(t *testing.T) {
	t.Parallel()

	logger, err := logging.NewLogger(config.LoggingConfig{Level: "error", Format: "json"})
	require.NoError(t, err)
	pc, err := repositories.NewPathConfig(t.TempDir())
	require.NoError(t, err)

	env := NewMergeEnv(context.Background(), "/repo.git", logger, pc)
	require.Equal(t, "/repo.git", env.RepoPath)
	require.False(t, env.IsCancelled())

	when := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	envT := NewMergeEnvWithTime(context.Background(), "/repo.git", when, logger, pc)
	require.Equal(t, when, envT.Now)

	// cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelledEnv := NewMergeEnv(ctx, "/x", logger, pc)
	require.True(t, cancelledEnv.IsCancelled())
}

// ---------------------------------------------------------------------------
// routes.go
// ---------------------------------------------------------------------------

func Test_responseErr_Error(t *testing.T) {
	t.Parallel()

	withMsg := &responseErr{status: 409, body: gin.H{"error": "boom"}}
	require.Equal(t, "boom", withMsg.Error())

	noMsg := &responseErr{status: 500, body: gin.H{"other": 1}}
	require.Equal(t, "response error", noMsg.Error())
}

func Test_mergeErrorMappers(t *testing.T) {
	t.Parallel()

	// responseErr passthrough
	status, body := mergeErrorMapper(&responseErr{status: 409, body: gin.H{"error": "conflict"}})
	require.Equal(t, 409, status)
	require.Equal(t, gin.H{"error": "conflict"}, body)

	// HTTPError
	status, _ = mergeErrorMapper(typed.NewHTTPError(http.StatusBadRequest, "bad"))
	require.Equal(t, http.StatusBadRequest, status)

	// generic → 500
	status, _ = mergeErrorMapper(context.Canceled)
	require.Equal(t, http.StatusInternalServerError, status)

	// mergeDiffErrorMapper delegates
	status, _ = mergeDiffErrorMapper(typed.NewHTTPError(http.StatusNotFound, "nf"))
	require.Equal(t, http.StatusNotFound, status)
}

func Test_RegisterRoutes_SpecOnly_And_Live(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	// spec-only mode (nil handlers) — should register without panic.
	specRouter := gin.New()
	require.NotPanics(t, func() {
		RegisterRoutes(specRouter, nil, nil)
	})

	// live mode with real handlers.
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "error", Format: "json"},
		Git:     config.GitConfig{RepoRoot: t.TempDir()},
	}
	opsH, err := NewMergeOperationsHandler(cfg)
	require.NoError(t, err)
	prevH, err := NewMergePreviewHandler(cfg)
	require.NoError(t, err)

	liveRouter := gin.New()
	require.NotPanics(t, func() {
		RegisterRoutes(liveRouter, opsH, prevH)
	})
}

// ---------------------------------------------------------------------------
// gitcmd.go conflict extraction (pure)
// ---------------------------------------------------------------------------

func Test_extractConflictType(t *testing.T) {
	t.Parallel()

	require.Equal(t, "content", extractConflictType("CONFLICT (content): Merge conflict in foo.txt"))
	require.Equal(t, "modify/delete", extractConflictType("CONFLICT (modify/delete): foo deleted"))
	require.Equal(t, "unknown", extractConflictType("no parens here"))
}

func Test_extractConflictPath(t *testing.T) {
	t.Parallel()

	require.Equal(t, "path/to/file", extractConflictPath("CONFLICT (content): Merge conflict in path/to/file"))
	require.Equal(t, "foo.txt", extractConflictPath("CONFLICT (modify/delete): foo.txt deleted in branch"))
	require.Equal(t, "", extractConflictPath("garbage with no colon"))
}

func Test_parseMergeConflicts(t *testing.T) {
	t.Parallel()

	out := "Auto-merging foo.txt\nCONFLICT (content): Merge conflict in foo.txt\n"
	conflicts := parseMergeConflicts(out)
	require.Len(t, conflicts, 2)
	require.Equal(t, "content", conflicts[1].Type)
	require.Equal(t, "foo.txt", conflicts[1].Path)
}

func Test_parseMergeTreeOutput(t *testing.T) {
	t.Parallel()

	clean, err := parseMergeTreeOutput("treehash123", false)
	require.NoError(t, err)
	require.Equal(t, "treehash123", clean.TreeHash)
	require.False(t, clean.HasConflicts)

	conflict, err := parseMergeTreeOutput("treehash123\nCONFLICT (content): Merge conflict in f.txt", true)
	require.NoError(t, err)
	require.True(t, conflict.HasConflicts)
	require.NotEmpty(t, conflict.Conflicts)

	_, err = parseMergeTreeOutput("", false)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// pure_tree_plan.go resolveDeleteModifyConflict
// ---------------------------------------------------------------------------

func Test_resolveDeleteModifyConflict(t *testing.T) {
	t.Parallel()

	require.Equal(t, EntrySourceSource, resolveDeleteModifyConflict(MergeStrategyTheirs, true))
	require.Equal(t, EntrySourceTarget, resolveDeleteModifyConflict(MergeStrategyTheirs, false))
	require.Equal(t, EntrySourceTarget, resolveDeleteModifyConflict(MergeStrategyOurs, true))
	require.Equal(t, EntrySourceTarget, resolveDeleteModifyConflict(MergeStrategyOurs, false))
	require.Equal(t, EntrySourceSource, resolveDeleteModifyConflict(MergeStrategyRecursive, true))
	require.Equal(t, EntrySourceTarget, resolveDeleteModifyConflict(MergeStrategyRecursive, false))
}

// ---------------------------------------------------------------------------
// HandleMergePreview HTTP — FF case (works without --merge-base) + errors
// ---------------------------------------------------------------------------

func setupPreviewRepo(t *testing.T, owner, repo string) (*MergePreviewHandler, string, string, string) {
	t.Helper()

	root := t.TempDir()
	repoPath := filepath.Join(root, owner, repo+".git")
	cmd := exec.Command("git", "init", "--bare", repoPath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	blob := testHashObject(t, repoPath, "init\n")
	tree := testMkTree(t, repoPath, "100644 blob "+blob+"\tinit.txt\n")
	base := testCommitTree(t, repoPath, tree, nil, "Initial commit")
	tip := addCommit(t, repoPath, base, "added.txt", "added\n", "advance")
	run(t, "git", "--git-dir", repoPath, "update-ref", "refs/heads/main", tip)

	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "error", Format: "json"},
		Git:     config.GitConfig{RepoRoot: root},
	}
	h, err := NewMergePreviewHandler(cfg)
	require.NoError(t, err)
	return h, repoPath, base, tip
}

func doPreview(t *testing.T, h *MergePreviewHandler, owner, repo string, q map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	req := httptest.NewRequest(http.MethodGet, "/preview", nil)
	values := req.URL.Query()
	for k, v := range q {
		values.Set(k, v)
	}
	req.URL.RawQuery = values.Encode()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "owner", Value: owner}, {Key: "repo", Value: repo}}

	adaptQuery(h.HandleMergePreview)(c)
	return w
}

func Test_HandleMergePreview_FastForward(t *testing.T) {
	t.Parallel()
	h, _, base, tip := setupPreviewRepo(t, "carol", "proj")

	// source=tip (ahead), target=base (behind) → FF, no conflict, no merge-tree base.
	w := doPreview(t, h, "carol", "proj", map[string]string{
		"source_ref": tip, "target_ref": base,
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp MergePreviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.IsFastForward)
	require.True(t, resp.CanMerge)
	require.False(t, resp.HasConflicts)
	require.NotNil(t, resp.ChangesSummary)
	require.NotNil(t, resp.PerformanceMetrics)
	require.NotNil(t, resp.CountSummary)
}

func Test_HandleMergePreview_MissingSourceRef(t *testing.T) {
	t.Parallel()
	h, _, _, _ := setupPreviewRepo(t, "carol", "proj")
	// missing source_ref → binding fails at adaptQuery (400).
	w := doPreview(t, h, "carol", "proj", map[string]string{"target_ref": "main"})
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func Test_HandleMergePreview_SameCommit(t *testing.T) {
	t.Parallel()
	h, _, _, tip := setupPreviewRepo(t, "carol", "proj")
	// source == target → 400 "same commit".
	w := doPreview(t, h, "carol", "proj", map[string]string{
		"source_ref": tip, "target_ref": tip,
	})
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func Test_HandleMergePreview_UnresolvableRef(t *testing.T) {
	t.Parallel()
	h, _, base, _ := setupPreviewRepo(t, "carol", "proj")
	w := doPreview(t, h, "carol", "proj", map[string]string{
		"source_ref": "no-such", "target_ref": base,
	})
	require.Equal(t, http.StatusNotFound, w.Code)
}

func Test_validatePath_PreviewHandler(t *testing.T) {
	t.Parallel()
	h, _, _, _ := setupPreviewRepo(t, "carol", "proj")
	require.Error(t, h.validatePath("", "repo"))
	require.Error(t, h.validatePath("owner", ""))
	require.NoError(t, h.validatePath("owner", "repo"))
}

// ---------------------------------------------------------------------------
// HandleMergeDiff HTTP — merge-base diff (works without --merge-base flag)
// ---------------------------------------------------------------------------

func doDiff(t *testing.T, h *MergePreviewHandler, owner, repo string, q map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	req := httptest.NewRequest(http.MethodGet, "/diff", nil)
	values := req.URL.Query()
	for k, v := range q {
		values.Set(k, v)
	}
	req.URL.RawQuery = values.Encode()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "owner", Value: owner}, {Key: "repo", Value: repo}}

	adaptQuery(h.HandleMergeDiff)(c)
	return w
}

func Test_HandleMergeDiff_Success(t *testing.T) {
	t.Parallel()
	h, _, base, tip := setupPreviewRepo(t, "carol", "proj")

	w := doDiff(t, h, "carol", "proj", map[string]string{
		"source_ref": tip, "target_ref": base,
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func Test_HandleMergeDiff_RefNotFound(t *testing.T) {
	t.Parallel()
	h, _, base, _ := setupPreviewRepo(t, "carol", "proj")

	w := doDiff(t, h, "carol", "proj", map[string]string{
		"source_ref": "missing-ref", "target_ref": base,
	})
	// REF_NOT_FOUND → 404 (responseErr) or 400 fallback depending on git msg.
	require.Contains(t, []int{http.StatusNotFound, http.StatusBadRequest}, w.Code)
}

func Test_HandleMergeDiff_MissingRequiredRef(t *testing.T) {
	t.Parallel()
	h, _, _, _ := setupPreviewRepo(t, "carol", "proj")
	w := doDiff(t, h, "carol", "proj", map[string]string{"target_ref": "main"})
	require.Equal(t, http.StatusBadRequest, w.Code)
}
