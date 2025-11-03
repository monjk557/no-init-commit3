package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/protocol"
)

// SetupRepositoryDetailTest는 repository detail API 테스트를 위한 테스트 환경을 생성합니다
func SetupRepositoryDetailTest(t *testing.T) *RepositoryDetailTestSuite {
	// 라우트 충돌을 피하기 위해 Gin을 테스트 모드로 설정
	gin.SetMode(gin.TestMode)

	// 테스트 저장소를 위한 임시 디렉토리 생성
	tempDir, err := os.MkdirTemp("", "repository-detail-test-*")
	require.NoError(t, err)

	// 설정 초기화
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
		Server: config.ServerConfig{
			Port: 8080,
		},
	}

	// 로거 생성
	logger, err := logging.NewLogger(cfg.Logging)
	require.NoError(t, err)

	// 테스트 라우터 생성
	router := gin.New()
	router.Use(gin.Recovery())

	// repository detail 핸들러 초기화
	repositoryDetailHandler, err := protocol.NewRepositoryDetailHandler(cfg)
	require.NoError(t, err)

	// API 라우트 추가
	apiGroup := router.Group("/api/v1")
	apiGroup.GET("/repository/:owner/:repo", repositoryDetailHandler.HandleGetRepositoryDetail)

	// 테스트 서버 생성
	testServer := httptest.NewServer(router)

	suite := &RepositoryDetailTestSuite{
		testServer: testServer,
		config:     cfg,
		logger:     logger,
		tempDir:    tempDir,
	}

	// 정리 함수 설정
	t.Cleanup(func() {
		suite.Cleanup()
	})

	return suite
}

type RepositoryDetailTestSuite struct {
	testServer *httptest.Server
	config     *config.Config
	logger     *logging.Logger
	tempDir    string
}

func (suite *RepositoryDetailTestSuite) Cleanup() {
	if suite.testServer != nil {
		suite.testServer.Close()
	}
	if suite.tempDir != "" {
		_ = os.RemoveAll(suite.tempDir)
	}
}

// createRepositoryWithReadme는 README.md 파일이 있는 저장소를 생성합니다
func (suite *RepositoryDetailTestSuite) createRepositoryWithReadme(t *testing.T, owner, repo string, readmeContent string) {
	repoPath := filepath.Join(suite.tempDir, owner, repo+".git")
	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	// bare 저장소 초기화
	gitRepo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)

	// README.md를 위한 blob 생성
	blob := gitRepo.Storer.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	blob.SetSize(int64(len(readmeContent)))
	writer, err := blob.Writer()
	require.NoError(t, err)
	_, err = writer.Write([]byte(readmeContent))
	require.NoError(t, err)
	_ = writer.Close()

	blobHash, err := gitRepo.Storer.SetEncodedObject(blob)
	require.NoError(t, err)

	// README.md가 포함된 tree 생성
	tree := &object.Tree{
		Entries: []object.TreeEntry{
			{
				Name: "README.md",
				Mode: filemode.Regular,
				Hash: blobHash,
			},
		},
	}

	treeObj := gitRepo.Storer.NewEncodedObject()
	err = tree.Encode(treeObj)
	require.NoError(t, err)

	treeHash, err := gitRepo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// 커밋 생성
	commit := &object.Commit{
		Message: "Initial commit with README",
		Author: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
		},
		Committer: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
		},
		TreeHash: treeHash,
	}

	commitObj := gitRepo.Storer.NewEncodedObject()
	err = commit.Encode(commitObj)
	require.NoError(t, err)

	commitHash, err := gitRepo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	// master 브랜치 참조 생성
	masterRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), commitHash)
	err = gitRepo.Storer.SetReference(masterRef)
	require.NoError(t, err)

	// HEAD 업데이트
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/master"))
	err = gitRepo.Storer.SetReference(headRef)
	require.NoError(t, err)
}

// createRepositoryWithoutReadme는 README.md 파일이 없는 저장소를 생성합니다
func (suite *RepositoryDetailTestSuite) createRepositoryWithoutReadme(t *testing.T, owner, repo string) {
	repoPath := filepath.Join(suite.tempDir, owner, repo+".git")
	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	// bare 저장소 초기화
	gitRepo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)

	// 빈 tree 생성 (파일 없음)
	tree := &object.Tree{
		Entries: []object.TreeEntry{},
	}

	treeObj := gitRepo.Storer.NewEncodedObject()
	err = tree.Encode(treeObj)
	require.NoError(t, err)

	treeHash, err := gitRepo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// 커밋 생성
	commit := &object.Commit{
		Message: "Initial commit without README",
		Author: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
		},
		Committer: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
		},
		TreeHash: treeHash,
	}

	commitObj := gitRepo.Storer.NewEncodedObject()
	err = commit.Encode(commitObj)
	require.NoError(t, err)

	commitHash, err := gitRepo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	// master 브랜치 참조 생성
	masterRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), commitHash)
	err = gitRepo.Storer.SetReference(masterRef)
	require.NoError(t, err)

	// HEAD 업데이트
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/master"))
	err = gitRepo.Storer.SetReference(headRef)
	require.NoError(t, err)
}

func (suite *RepositoryDetailTestSuite) makeRequest(method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequest(method, suite.testServer.URL+path, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-USER-ID", "testuser")
	req.Header.Set("X-Source", "orchestrator")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return http.DefaultClient.Do(req)
}

// TestRepositoryDetailWithReadme는 README.md가 있을 때 repository detail API를 테스트합니다
func TestRepositoryDetailWithReadme(t *testing.T) {
	suite := SetupRepositoryDetailTest(t)

	owner := "testowner"
	repo := "repo-with-readme"
	expectedReadme := "# Test Repository\n\nThis repository has a README file.\n"

	// README가 있는 저장소 생성
	suite.createRepositoryWithReadme(t, owner, repo, expectedReadme)

	// repository detail API 요청
	resp, err := suite.makeRequest("GET", fmt.Sprintf("/api/v1/repository/%s/%s", owner, repo), nil)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// 200 OK를 반환해야 함
	assert.Equal(t, http.StatusOK, resp.StatusCode, "README가 있을 때 repository detail API는 200을 반환해야 합니다")

	// 응답 파싱
	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	// 응답 구조 확인
	assert.Equal(t, true, response["success"], "응답은 성공을 나타내야 합니다")
	assert.Contains(t, response, "data", "응답은 data 필드를 포함해야 합니다")

	// data 객체 가져오기
	data, ok := response["data"].(map[string]interface{})
	require.True(t, ok, "data는 map이어야 합니다")

	// readme 필드가 존재하고 예상된 내용을 포함하는지 확인
	readme, ok := data["readme"].(string)
	require.True(t, ok, "readme는 string이어야 합니다")
	assert.Equal(t, expectedReadme, readme, "readme 내용은 예상 값과 일치해야 합니다")
	assert.NotEmpty(t, readme, "파일이 있을 때 readme는 비어있지 않아야 합니다")
}

// TestRepositoryDetailWithoutReadme는 README.md가 없을 때 repository detail API를 테스트합니다
func TestRepositoryDetailWithoutReadme(t *testing.T) {
	suite := SetupRepositoryDetailTest(t)

	owner := "testowner"
	repo := "repo-without-readme"

	// README가 없는 저장소 생성
	suite.createRepositoryWithoutReadme(t, owner, repo)

	// repository detail API 요청
	resp, err := suite.makeRequest("GET", fmt.Sprintf("/api/v1/repository/%s/%s", owner, repo), nil)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// 200 OK를 반환해야 함 (500 에러가 아님)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "README가 없을 때도 repository detail API는 200을 반환해야 합니다 (500 에러가 아님)")

	// 응답 파싱
	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	// 응답 구조 확인
	assert.Equal(t, true, response["success"], "응답은 성공을 나타내야 합니다")
	assert.Contains(t, response, "data", "응답은 data 필드를 포함해야 합니다")

	// data 객체 가져오기
	data, ok := response["data"].(map[string]interface{})
	require.True(t, ok, "data는 map이어야 합니다")

	// readme 필드가 존재하고 빈 문자열인지 확인
	readme, ok := data["readme"].(string)
	require.True(t, ok, "readme는 string이어야 합니다")
	assert.Equal(t, "", readme, "파일이 없을 때 readme는 빈 문자열이어야 합니다")
}

// TestRepositoryDetailReadmeComparison는 README가 있는 경우와 없는 경우의 응답을 비교합니다
func TestRepositoryDetailReadmeComparison(t *testing.T) {
	suite := SetupRepositoryDetailTest(t)

	owner := "testowner"
	expectedReadme := "# Test Repository\n\nThis is a test README.\n"

	// 테스트 1: README가 있는 저장소
	repoWithReadme := "repo-with-readme"
	suite.createRepositoryWithReadme(t, owner, repoWithReadme, expectedReadme)

	respWith, err := suite.makeRequest("GET", fmt.Sprintf("/api/v1/repository/%s/%s", owner, repoWithReadme), nil)
	require.NoError(t, err)
	defer func() { _ = respWith.Body.Close() }()

	assert.Equal(t, http.StatusOK, respWith.StatusCode)

	var responseWith map[string]interface{}
	err = json.NewDecoder(respWith.Body).Decode(&responseWith)
	require.NoError(t, err)

	dataWith, ok := responseWith["data"].(map[string]interface{})
	require.True(t, ok)

	readmeWith := dataWith["readme"].(string)
	assert.Equal(t, expectedReadme, readmeWith, "README가 있는 경우 파일 내용을 포함해야 합니다")

	// 테스트 2: README가 없는 저장소
	repoWithoutReadme := "repo-without-readme"
	suite.createRepositoryWithoutReadme(t, owner, repoWithoutReadme)

	respWithout, err := suite.makeRequest("GET", fmt.Sprintf("/api/v1/repository/%s/%s", owner, repoWithoutReadme), nil)
	require.NoError(t, err)
	defer func() { _ = respWithout.Body.Close() }()

	assert.Equal(t, http.StatusOK, respWithout.StatusCode, "README가 없을 때 500 에러를 반환하지 않아야 합니다")

	var responseWithout map[string]interface{}
	err = json.NewDecoder(respWithout.Body).Decode(&responseWithout)
	require.NoError(t, err)

	dataWithout, ok := responseWithout["data"].(map[string]interface{})
	require.True(t, ok)

	readmeWithout := dataWithout["readme"].(string)
	assert.Equal(t, "", readmeWithout, "README가 없는 경우 빈 문자열이어야 합니다")

	// 비교: 둘 다 성공해야 하며, readme 내용은 달라야 함
	assert.Equal(t, true, responseWith["success"])
	assert.Equal(t, true, responseWithout["success"])
	assert.NotEqual(t, readmeWith, readmeWithout, "README가 있는 저장소와 없는 저장소의 readme 내용은 달라야 합니다")
	assert.NotEmpty(t, readmeWith, "README가 있는 경우 비어있지 않아야 합니다")
	assert.Empty(t, readmeWithout, "README가 없는 경우 비어있어야 합니다")
}
