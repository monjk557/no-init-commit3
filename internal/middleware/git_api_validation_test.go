package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newGitVal은 Git API 검증 미들웨어를 생성하는 헬퍼.
func newGitVal(t *testing.T, cfg *GitAPIValidationConfig) *GitAPIValidationMiddleware {
	t.Helper()
	m, err := NewGitAPIValidationMiddleware(cfg, nopLogger())
	require.NoError(t, err)
	require.NotNil(t, m)
	return m
}

// gitValEngine은 검증 미들웨어를 장착한 gin 엔진을 만든다.
// owner/repo/path 파라미터가 검증 로직에 노출되도록 라우트를 구성한다.
func gitValEngine(m *GitAPIValidationMiddleware) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(m.ValidateGitAPIRequest())

	ok := func(c *gin.Context) { c.String(http.StatusOK, "ok") }
	// 파라미터화된 라우트들 — validateURLParameters 가 c.Param 으로 owner/repo/path 를 읽는다.
	r.PUT("/repos/:owner/:repo/files/*path", ok)
	r.POST("/repos/:owner/:repo/commits", ok)
	r.POST("/repos/:owner/:repo/branches", ok)
	r.PATCH("/repos/:owner/:repo/branches", ok)
	r.POST("/repos/:owner/:repo/merges", ok)
	r.GET("/repos/:owner/:repo/tree", ok)
	r.POST("/repos/:owner/:repo/other", ok)
	r.POST("/non-git/endpoint", ok)
	return r
}

// doReq는 요청을 보내고 레코더를 돌려준다.
func doReq(r *gin.Engine, method, path, contentType, body string) *httptest.ResponseRecorder {
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestNewGitAPIValidationMiddleware(t *testing.T) {
	t.Run("nil config uses defaults", func(t *testing.T) {
		m, err := NewGitAPIValidationMiddleware(nil, nopLogger())
		require.NoError(t, err)
		assert.NotNil(t, m.config)
		assert.Equal(t, int64(50*1024*1024), m.config.MaxFileSize)
	})

	t.Run("nil logger rejected", func(t *testing.T) {
		m, err := NewGitAPIValidationMiddleware(nil, nil)
		assert.Error(t, err)
		assert.Nil(t, m)
	})
}

func TestDefaultGitAPIValidationConfig(t *testing.T) {
	cfg := DefaultGitAPIValidationConfig()
	require.NotNil(t, cfg)
	assert.Equal(t, 2048, cfg.MaxCommitMessageLength)
	assert.Equal(t, 4096, cfg.MaxPathLength)
	assert.Equal(t, 1000, cfg.MaxFilesPerCommit)
	assert.True(t, cfg.EnableContentValidation)
	assert.Contains(t, cfg.BannedFileExtensions, ".exe")
}

func TestValidateGitAPIRequest_Passthrough(t *testing.T) {
	m := newGitVal(t, nil)
	r := gitValEngine(m)

	t.Run("GET requests skip validation", func(t *testing.T) {
		w := doReq(r, http.MethodGet, "/repos/owner/repo/tree", "", "")
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("non-git endpoint skips validation", func(t *testing.T) {
		// content-type 없이도 통과해야 함 (git endpoint 가 아니므로).
		w := doReq(r, http.MethodPost, "/non-git/endpoint", "", "")
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

func TestValidateGitAPIRequest_ContentType(t *testing.T) {
	m := newGitVal(t, nil)
	r := gitValEngine(m)

	t.Run("missing content-type rejected", func(t *testing.T) {
		w := doReq(r, http.MethodPost, "/repos/owner/repo/commits", "", `{}`)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "Content-Type")
	})

	t.Run("malformed content-type rejected", func(t *testing.T) {
		w := doReq(r, http.MethodPost, "/repos/owner/repo/commits", "application/", `{}`)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("unsupported content-type rejected", func(t *testing.T) {
		w := doReq(r, http.MethodPost, "/repos/owner/repo/commits", "text/plain", `{}`)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "unsupported")
	})

	t.Run("allowed git content-type passes", func(t *testing.T) {
		w := doReq(r, http.MethodPost, "/repos/owner/repo/other", "application/x-git-upload-pack", "")
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

func TestValidateGitAPIRequest_RequestSize(t *testing.T) {
	cfg := DefaultGitAPIValidationConfig()
	cfg.MaxFileSize = 10 // 아주 작은 한계로 size 검증을 트리거.
	m := newGitVal(t, cfg)
	r := gitValEngine(m)

	w := doReq(r, http.MethodPost, "/repos/owner/repo/other", "application/json",
		`{"some":"payload exceeding ten bytes"}`)
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	assert.Contains(t, w.Body.String(), "too large")
}

func TestValidateGitAPIRequest_URLParameters(t *testing.T) {
	m := newGitVal(t, nil)
	r := gitValEngine(m)

	t.Run("owner with path traversal rejected", func(t *testing.T) {
		w := doReq(r, http.MethodPost, "/repos/..%2f/repo/other", "application/json", "")
		// gin 이 디코드한 owner 에 ".." 가 포함되면 400.
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("invalid branch query rejected", func(t *testing.T) {
		w := doReq(r, http.MethodPost, "/repos/owner/repo/other?branch=bad%20branch",
			"application/json", "")
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "Invalid URL parameters")
	})

	t.Run("file path in URL with banned extension rejected", func(t *testing.T) {
		w := doReq(r, http.MethodPut, "/repos/owner/repo/files/malware.exe",
			"application/json", `{"content":"x","message":"m"}`)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("valid params and body pass", func(t *testing.T) {
		w := doReq(r, http.MethodPut, "/repos/owner/repo/files/dir/note.txt",
			"application/json", `{"content":"hello","message":"add note"}`)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

func TestValidateGitAPIRequest_FileOperationBody(t *testing.T) {
	m := newGitVal(t, nil)
	r := gitValEngine(m)
	put := func(body string) *httptest.ResponseRecorder {
		return doReq(r, http.MethodPut, "/repos/owner/repo/files/note.txt", "application/json", body)
	}

	t.Run("malformed JSON rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, put(`{not json`).Code)
	})

	t.Run("empty commit message rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, put(`{"content":"x","message":""}`).Code)
	})

	t.Run("bad branch rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, put(`{"content":"x","message":"m","branch":"bad branch"}`).Code)
	})

	t.Run("bad committer email rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, put(`{"content":"x","message":"m","committer_email":"noatsign"}`).Code)
	})

	t.Run("full valid body passes", func(t *testing.T) {
		assert.Equal(t, http.StatusOK, put(`{"content":"x","message":"m","branch":"main","committer_name":"alice","committer_email":"a@b.c"}`).Code)
	})
}

func TestValidateGitAPIRequest_MultiFileBody(t *testing.T) {
	m := newGitVal(t, nil)
	r := gitValEngine(m)
	post := func(body string) *httptest.ResponseRecorder {
		return doReq(r, http.MethodPost, "/repos/owner/repo/commits", "application/json", body)
	}

	t.Run("invalid file path rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest,
			post(`{"message":"m","files":[{"path":"../escape","content":"c","operation":"create"}]}`).Code)
	})

	t.Run("invalid operation rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest,
			post(`{"message":"m","files":[{"path":"a.txt","content":"c","operation":"explode"}]}`).Code)
	})

	t.Run("empty commit message rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest,
			post(`{"message":"","files":[{"path":"a.txt","content":"c","operation":"create"}]}`).Code)
	})

	t.Run("valid multi-file body passes", func(t *testing.T) {
		assert.Equal(t, http.StatusOK,
			post(`{"message":"m","files":[{"path":"a.txt","content":"c","operation":"create"},{"path":"b/c.txt","content":"d","operation":"update"}]}`).Code)
	})
}

func TestValidateGitAPIRequest_BranchBody(t *testing.T) {
	m := newGitVal(t, nil)
	r := gitValEngine(m)
	post := func(body string) *httptest.ResponseRecorder {
		return doReq(r, http.MethodPost, "/repos/owner/repo/branches", "application/json", body)
	}

	t.Run("empty branch name rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, post(`{"name":""}`).Code)
	})

	t.Run("invalid source ref rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, post(`{"name":"feature","source_ref":"bad ref"}`).Code)
	})

	t.Run("invalid target ref rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, post(`{"name":"feature","target_ref":"bad ref"}`).Code)
	})

	t.Run("valid branch with commit-hash refs passes", func(t *testing.T) {
		hash := "0123456789abcdef0123456789abcdef01234567"
		assert.Equal(t, http.StatusOK,
			post(`{"name":"feature","source_ref":"`+hash+`","target_ref":"main"}`).Code)
	})

	t.Run("PATCH branch also validated", func(t *testing.T) {
		w := doReq(r, http.MethodPatch, "/repos/owner/repo/branches", "application/json", `{"name":""}`)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestValidateGitAPIRequest_MergeBody(t *testing.T) {
	m := newGitVal(t, nil)
	r := gitValEngine(m)
	post := func(body string) *httptest.ResponseRecorder {
		return doReq(r, http.MethodPost, "/repos/owner/repo/merges", "application/json", body)
	}

	t.Run("empty base rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, post(`{"base":"","head":"main"}`).Code)
	})

	t.Run("empty head rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, post(`{"base":"main","head":""}`).Code)
	})

	t.Run("invalid strategy rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, post(`{"base":"main","head":"dev","strategy":"teleport"}`).Code)
	})

	t.Run("valid merge passes", func(t *testing.T) {
		assert.Equal(t, http.StatusOK,
			post(`{"base":"main","head":"dev","commit_message":"merge it","strategy":"squash"}`).Code)
	})
}

// --- 헬퍼 검증 함수 단위 테스트 (직접 호출로 분기 망라) ----------------------

func TestGitVal_validatePathComponent(t *testing.T) {
	m := newGitVal(t, nil)
	assert.Error(t, m.validatePathComponent("", "owner"))
	assert.Error(t, m.validatePathComponent(strings.Repeat("a", 256), "owner"))
	assert.Error(t, m.validatePathComponent("a..b", "owner"))
	assert.Error(t, m.validatePathComponent("a/b", "owner"))
	assert.NoError(t, m.validatePathComponent("valid-owner_1", "owner"))
}

func TestGitVal_validateFilePath(t *testing.T) {
	m := newGitVal(t, nil)
	assert.Error(t, m.validateFilePath(""))
	assert.Error(t, m.validateFilePath(strings.Repeat("a", m.config.MaxPathLength+1)))
	assert.Error(t, m.validateFilePath("../etc/passwd"))
	assert.Error(t, m.validateFilePath("/abs/path"))
	assert.Error(t, m.validateFilePath("bad.exe"))
	assert.NoError(t, m.validateFilePath("dir/sub/file.txt"))

	t.Run("content validation disabled allows banned ext", func(t *testing.T) {
		cfg := DefaultGitAPIValidationConfig()
		cfg.EnableContentValidation = false
		m2 := newGitVal(t, cfg)
		assert.NoError(t, m2.validateFilePath("thing.exe"))
	})
}

func TestGitVal_validateBranchName(t *testing.T) {
	m := newGitVal(t, nil)
	assert.Error(t, m.validateBranchName(""))
	assert.Error(t, m.validateBranchName(strings.Repeat("a", 251)))
	assert.Error(t, m.validateBranchName("bad branch"))
	assert.Error(t, m.validateBranchName("-leadingdash"))
	assert.Error(t, m.validateBranchName("trailingdot."))
	assert.NoError(t, m.validateBranchName("feature/new-thing"))
}

func TestGitVal_validateRefName(t *testing.T) {
	m := newGitVal(t, nil)
	assert.Error(t, m.validateRefName(""))
	assert.NoError(t, m.validateRefName("0123456789abcdef0123456789abcdef01234567")) // commit hash
	assert.NoError(t, m.validateRefName("main"))                                     // branch name
	assert.Error(t, m.validateRefName("bad ref"))
}

func TestGitVal_validateCommitMessage(t *testing.T) {
	m := newGitVal(t, nil)
	assert.Error(t, m.validateCommitMessage(""))
	assert.Error(t, m.validateCommitMessage(strings.Repeat("x", m.config.MaxCommitMessageLength+1)))
	assert.NoError(t, m.validateCommitMessage("fix: something"))
}

func TestGitVal_validateCommitterInfo(t *testing.T) {
	m := newGitVal(t, nil)
	assert.Error(t, m.validateCommitterInfo(strings.Repeat("n", 101), ""))
	assert.Error(t, m.validateCommitterInfo("alice", strings.Repeat("e", 255)))
	assert.Error(t, m.validateCommitterInfo("alice", "noatsign"))
	assert.NoError(t, m.validateCommitterInfo("alice", "a@b.c"))
	assert.NoError(t, m.validateCommitterInfo("", ""))
}

func TestGitVal_validateFileOperation(t *testing.T) {
	m := newGitVal(t, nil)
	for _, op := range []string{"create", "update", "delete"} {
		assert.NoError(t, m.validateFileOperation(op))
	}
	assert.Error(t, m.validateFileOperation("rename"))
}

func TestGitVal_isGitAPIEndpoint(t *testing.T) {
	m := newGitVal(t, nil)
	assert.True(t, m.isGitAPIEndpoint("/repos/o/r/tree"))
	assert.False(t, m.isGitAPIEndpoint("/health"))
}

func TestGitVal_validateRequestBody_NonJSONSkipped(t *testing.T) {
	// JSON 이 아닌 content-type 이면 body 검증을 건너뛴다 (early return).
	m := newGitVal(t, nil)
	r := gitValEngine(m)
	w := doReq(r, http.MethodPost, "/repos/owner/repo/other", "application/x-git-receive-pack", "raw-bytes")
	assert.Equal(t, http.StatusOK, w.Code)
}
