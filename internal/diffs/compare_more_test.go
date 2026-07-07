package diffs

// compare.go 의 HandleCompareChanged (HTTP typed handler) 와 compareDiffChanged
// (순수 diff signature 비교) 의 미커버 브랜치를 실제 bare 저장소 fixture 로 검증한다.
//
// 기존 handler_more_test.go / native_git_diff_test.go 의 헬퍼
// (makeBlob/makeTree/makeCommit, diffPath/diffURL, postJSON, adapt) 를 재사용한다.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compareFixture 는 base 에서 분기한 old / new / newSame 세 head 를 가진 bare
// 저장소를 만든다.
//
// compareDiffChanged 는 contextLines=0 으로 diff 를 계산하므로 line-level Changes
// 는 채워지지 않고 diffContentSignature 는 파일 단위 메타데이터(path/status/
// old_path/binary)만 비교한다. 따라서 "변경됨" 케이스는 파일 집합 자체가 달라지도록
// 구성한다.
//
//   - base:    a.txt = "base\n"
//   - old:     a.txt = "base\nOLD\n"        → diff(base..old)     = [a.txt modified]
//   - new:     a.txt 그대로 + b.txt 추가     → diff(base..new)     = [b.txt added]
//     (old 와 파일 집합이 달라 signature 상이 → content_changed=true)
//   - newSame: old 와 동일 tree 지만 커밋 메시지가 달라 SHA 만 다름
//     → diff(base..old) 와 diff(base..newSame) 는 signature 가 같다 (trivial rebase).
func compareFixture(t *testing.T) (h *DiffHandler, repoPath, base, old, newHead, newSame string) {
	t.Helper()
	tempDir := t.TempDir()
	repoPath = filepath.Join(tempDir, "testowner", "testrepo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	bBase := makeBlob(t, repo, []byte("base\n"))
	tBase := makeTree(t, repo, []object.TreeEntry{{Name: "a.txt", Mode: 0100644, Hash: bBase}})
	cBase := makeCommit(t, repo, "base", tBase, nil, now)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference("refs/heads/master", cBase)))

	bOld := makeBlob(t, repo, []byte("base\nOLD\n"))
	tOld := makeTree(t, repo, []object.TreeEntry{{Name: "a.txt", Mode: 0100644, Hash: bOld}})
	cOld := makeCommit(t, repo, "old head", tOld, []plumbing.Hash{cBase}, now)

	// new: a.txt 는 base 와 동일, b.txt 추가 → 파일 집합이 old 와 다름.
	bAdded := makeBlob(t, repo, []byte("added\n"))
	tNew := makeTree(t, repo, []object.TreeEntry{
		{Name: "a.txt", Mode: 0100644, Hash: bBase},
		{Name: "b.txt", Mode: 0100644, Hash: bAdded},
	})
	cNew := makeCommit(t, repo, "new head", tNew, []plumbing.Hash{cBase}, now)

	// old 와 동일 tree, 다른 커밋 메시지 → SHA 만 다르고 diff content 는 동일.
	cNewSame := makeCommit(t, repo, "new head with identical tree", tOld, []plumbing.Hash{cBase}, now)
	require.NotEqual(t, cOld.String(), cNewSame.String(), "메시지가 달라 SHA 는 달라야 함")

	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: tempDir},
	}
	h, err = NewDiffHandler(cfg)
	require.NoError(t, err)

	return h, repoPath, cBase.String(), cOld.String(), cNew.String(), cNewSame.String()
}

// ----------------------------------------------------------------------------
// HandleCompareChanged (HTTP)
// ----------------------------------------------------------------------------

func TestHandleCompareChanged_ContentChangedTrue(t *testing.T) {
	h, _, base, old, newHead, _ := compareFixture(t)
	router := goginNew()
	router.POST(diffPath("/diff/compare-changed"), adapt(h.HandleCompareChanged))

	w := postJSON(t, router, diffURL("/diff/compare-changed"), CompareChangedRequest{
		BaseSHA: base, OldHeadSHA: old, NewHeadSHA: newHead,
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp CompareChangedResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.True(t, resp.ContentChanged, "old/new 두 head 의 파일 집합이 다르므로 content_changed=true")
	assert.Equal(t, base, resp.BaseSHA)
	assert.Equal(t, old, resp.OldHeadSHA)
	assert.Equal(t, newHead, resp.NewHeadSHA)
}

func TestHandleCompareChanged_ContentChangedFalseIdenticalTrees(t *testing.T) {
	h, _, base, old, _, newSame := compareFixture(t)
	router := goginNew()
	router.POST(diffPath("/diff/compare-changed"), adapt(h.HandleCompareChanged))

	// old != newSame(SHA 다름) 이지만 diff content 는 동일 → 계산 후 content_changed=false.
	w := postJSON(t, router, diffURL("/diff/compare-changed"), CompareChangedRequest{
		BaseSHA: base, OldHeadSHA: old, NewHeadSHA: newSame,
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp CompareChangedResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.False(t, resp.ContentChanged, "동일 tree 이므로 trivial rebase — content_changed=false")
}

func TestHandleCompareChanged_SameSHAFastPath(t *testing.T) {
	h, _, base, old, _, _ := compareFixture(t)
	router := goginNew()
	router.POST(diffPath("/diff/compare-changed"), adapt(h.HandleCompareChanged))

	// old == new → diff 계산 없이 fast path 로 content_changed=false.
	w := postJSON(t, router, diffURL("/diff/compare-changed"), CompareChangedRequest{
		BaseSHA: base, OldHeadSHA: old, NewHeadSHA: old,
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp CompareChangedResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.False(t, resp.ContentChanged)
}

func TestHandleCompareChanged_InvalidJSON(t *testing.T) {
	h, _, _, _, _, _ := compareFixture(t)
	router := goginNew()
	router.POST(diffPath("/diff/compare-changed"), adapt(h.HandleCompareChanged))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", diffURL("/diff/compare-changed"),
		bytes.NewReader([]byte("{not-json")))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleCompareChanged_MissingOwnerParams(t *testing.T) {
	h, _, base, old, newHead, _ := compareFixture(t)
	router := goginNew()
	// :owner/:repo 없는 route → validateAndExtractParams 가 owner="" 로 400.
	router.POST("/noparams/diff/compare-changed", adapt(h.HandleCompareChanged))

	w := postJSON(t, router, "/noparams/diff/compare-changed", CompareChangedRequest{
		BaseSHA: base, OldHeadSHA: old, NewHeadSHA: newHead,
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func TestHandleCompareChanged_InvalidRepoPath(t *testing.T) {
	h, _, base, old, newHead, _ := compareFixture(t)
	router := goginNew()
	router.POST(diffPath("/diff/compare-changed"), adapt(h.HandleCompareChanged))

	// owner 에 ".." → GetAndValidateRepoPath 의 path-traversal 검증 실패 → 400.
	url := "/api/v2/repository/bad..owner/testrepo/diff/compare-changed"
	w := postJSON(t, router, url, CompareChangedRequest{
		BaseSHA: base, OldHeadSHA: old, NewHeadSHA: newHead,
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func TestHandleCompareChanged_ComputeErrorBadBase(t *testing.T) {
	h, _, _, old, newHead, _ := compareFixture(t)
	router := goginNew()
	router.POST(diffPath("/diff/compare-changed"), adapt(h.HandleCompareChanged))

	// old != new 로 fast path 를 지나 compareDiffChanged 진입 → base 가 해석 불가라
	// diff 계산 실패 → 500.
	w := postJSON(t, router, diffURL("/diff/compare-changed"), CompareChangedRequest{
		BaseSHA: "this-base-does-not-exist", OldHeadSHA: old, NewHeadSHA: newHead,
	})
	assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
}

// ----------------------------------------------------------------------------
// compareDiffChanged (직접 호출 — signature 비교 로직 + 에러 경로)
// ----------------------------------------------------------------------------

func TestCompareDiffChanged_IdenticalTreesFalse(t *testing.T) {
	_, repoPath, base, old, _, newSame := compareFixture(t)
	changed, err := compareDiffChanged(context.Background(), repoPath, base, old, newSame)
	require.NoError(t, err)
	assert.False(t, changed, "동일 tree diff → signature 동일 → changed=false")
}

func TestCompareDiffChanged_DifferentContentTrue(t *testing.T) {
	_, repoPath, base, old, newHead, _ := compareFixture(t)
	changed, err := compareDiffChanged(context.Background(), repoPath, base, old, newHead)
	require.NoError(t, err)
	assert.True(t, changed, "다른 파일 집합 → signature 상이 → changed=true")
}

func TestCompareDiffChanged_OldDiffError(t *testing.T) {
	_, repoPath, _, old, newHead, _ := compareFixture(t)
	// base 가 해석 불가 → 첫 번째(old) diff 계산에서 에러.
	_, err := compareDiffChanged(context.Background(), repoPath, "no-such-base", old, newHead)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "old diff")
}

func TestCompareDiffChanged_NewDiffError(t *testing.T) {
	_, repoPath, base, old, _, _ := compareFixture(t)
	// base/old 는 정상, newHead 만 해석 불가 → 두 번째(new) diff 계산에서 에러.
	_, err := compareDiffChanged(context.Background(), repoPath, base, old, "no-such-new-head")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "new diff")
}
