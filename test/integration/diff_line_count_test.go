package integration

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDiffLineCount_SingleLineAddition은 1줄짜리 파일을 추가했을 때 정확히 1줄만 추가되는지 테스트합니다
func TestDiffLineCount_SingleLineAddition(t *testing.T) {
	suite := SetupIntegrationTest(t)

	// 테스트를 위한 새 브랜치 생성
	branchName := "test-single-line-diff"
	t.Run("CreateTestBranch", func(t *testing.T) {
		createBranch := map[string]interface{}{
			"name":   branchName,
			"source": "master",
		}
		resp, err := suite.makeRequest("POST", "/repos/testowner/testrepo/branches", createBranch)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	// 1줄짜리 파일 추가
	t.Run("AddSingleLineFile", func(t *testing.T) {
		commitBody := map[string]interface{}{
			"base_branch": branchName,
			"operations": []map[string]interface{}{
				{
					"operation": "create",
					"path":      "single-line.txt",
					"content":   "SGVsbG8=", // base64로 인코딩된 "Hello" - 단일 라인
				},
			},
			"message": "Add single line file",
			"author": map[string]string{
				"name":  "Test User",
				"email": "test@example.com",
			},
		}

		resp, err := suite.makeRequest("POST", "/repos/testowner/testrepo/commits", commitBody)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	// master와 새 브랜치 간의 diff 가져오기
	t.Run("CheckDiff_OneLineAdded", func(t *testing.T) {
		resp, err := suite.makeRequest("GET", "/repos/testowner/testrepo/diff/branches/master/"+branchName, nil)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var diffResponse struct {
			Success bool `json:"success"`
			Files   []struct {
				Path      string `json:"path"`
				Status    string `json:"status"`
				Additions int    `json:"additions"`
				Deletions int    `json:"deletions"`
				Changes   []struct {
					Type    string `json:"type"`
					LineNum int    `json:"line"`
					Content string `json:"content"`
				} `json:"changes,omitempty"`
			} `json:"files"`
			Stats struct {
				TotalAdditions int `json:"total_additions"`
				TotalDeletions int `json:"total_deletions"`
				FilesChanged   int `json:"files_changed"`
			} `json:"stats"`
		}

		err = json.NewDecoder(resp.Body).Decode(&diffResponse)
		require.NoError(t, err)
		require.True(t, diffResponse.Success, "Diff 요청이 성공해야 합니다")

		// diff에서 single-line.txt 파일 찾기
		var singleLineFile *struct {
			Path      string `json:"path"`
			Status    string `json:"status"`
			Additions int    `json:"additions"`
			Deletions int    `json:"deletions"`
			Changes   []struct {
				Type    string `json:"type"`
				LineNum int    `json:"line"`
				Content string `json:"content"`
			} `json:"changes,omitempty"`
		}

		for i := range diffResponse.Files {
			if diffResponse.Files[i].Path == "single-line.txt" {
				singleLineFile = &diffResponse.Files[i]
				break
			}
		}

		require.NotNil(t, singleLineFile, "single-line.txt가 diff에 있어야 합니다")
		assert.Equal(t, "added", singleLineFile.Status, "파일이 추가됨으로 표시되어야 합니다")
		assert.Equal(t, 1, singleLineFile.Additions, "정확히 1줄만 추가되어야 합니다")
		assert.Equal(t, 0, singleLineFile.Deletions, "삭제된 줄이 없어야 합니다")
		assert.Equal(t, 1, diffResponse.Stats.TotalAdditions, "전체 추가 줄 수는 1이어야 합니다")
		assert.Equal(t, 0, diffResponse.Stats.TotalDeletions, "전체 삭제 줄 수는 0이어야 합니다")

		// 라인 변경사항에 정확히 하나의 "add" 타입 변경이 포함되어 있는지 확인
		if assert.NotNil(t, singleLineFile.Changes, "Changes가 nil이 아니어야 합니다") {
			addCount := 0
			for _, change := range singleLineFile.Changes {
				if change.Type == "add" {
					addCount++
				}
			}
			assert.Equal(t, 1, addCount, "정확히 1개의 'add' 타입 변경이 있어야 합니다")
		}
	})
}

// TestDiffLineCount_SingleLineAddedToExistingFile는 기존 파일에 1줄을 추가했을 때를 테스트합니다
func TestDiffLineCount_SingleLineAddedToExistingFile(t *testing.T) {
	suite := SetupIntegrationTest(t)

	// 1줄짜리 초기 파일 생성
	branchName := "test-single-line-add-to-file"
	t.Run("SetupInitialFile", func(t *testing.T) {
		// 브랜치 생성
		createBranch := map[string]interface{}{
			"name":   branchName,
			"source": "master",
		}
		resp, err := suite.makeRequest("POST", "/repos/testowner/testrepo/branches", createBranch)
		require.NoError(t, err)
		_ = resp.Body.Close()

		// 1줄짜리 초기 파일 생성
		commitBody := map[string]interface{}{
			"base_branch": branchName,
			"operations": []map[string]interface{}{
				{
					"operation": "create",
					"path":      "existing.txt",
					"content":   "First line", // 단일 라인
				},
			},
			"message": "Add initial file",
			"author": map[string]string{
				"name":  "Test User",
				"email": "test@example.com",
			},
		}

		resp, err = suite.makeRequest("POST", "/repos/testowner/testrepo/commits", commitBody)
		require.NoError(t, err)
		_ = resp.Body.Close()
	})

	// 파일에 한 줄 더 추가
	t.Run("AddSecondLine", func(t *testing.T) {
		commitBody := map[string]interface{}{
			"base_branch": branchName,
			"operations": []map[string]interface{}{
				{
					"operation": "update",
					"path":      "existing.txt",
					"content":   "First line\nSecond line", // 이제 두 줄
				},
			},
			"message": "Add second line",
			"author": map[string]string{
				"name":  "Test User",
				"email": "test@example.com",
			},
		}

		resp, err := suite.makeRequest("POST", "/repos/testowner/testrepo/commits", commitBody)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	// diff를 위한 커밋 SHA 가져오기
	t.Run("GetCommitSHA", func(t *testing.T) {
		// 최신 커밋을 찾기 위해 커밋 목록 가져오기
		resp, err := suite.makeRequest("GET", "/repos/testowner/testrepo/commits?branch="+branchName+"&limit=2", nil)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		var commitsResponse struct {
			Success bool `json:"success"`
			Commits []struct {
				SHA string `json:"sha"`
			} `json:"commits"`
		}

		err = json.NewDecoder(resp.Body).Decode(&commitsResponse)
		require.NoError(t, err)
		require.True(t, len(commitsResponse.Commits) >= 2, "최소 2개의 커밋이 있어야 합니다")

		// 두 커밋 간의 diff 가져오기
		parentSHA := commitsResponse.Commits[1].SHA
		currentSHA := commitsResponse.Commits[0].SHA

		resp, err = suite.makeRequest("GET", "/repos/testowner/testrepo/diff/commits/"+parentSHA+"/"+currentSHA, nil)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var diffResponse struct {
			Success bool `json:"success"`
			Files   []struct {
				Path      string `json:"path"`
				Status    string `json:"status"`
				Additions int    `json:"additions"`
				Deletions int    `json:"deletions"`
			} `json:"files"`
			Stats struct {
				TotalAdditions int `json:"total_additions"`
				TotalDeletions int `json:"total_deletions"`
			} `json:"stats"`
		}

		err = json.NewDecoder(resp.Body).Decode(&diffResponse)
		require.NoError(t, err)
		require.True(t, diffResponse.Success, "Diff 요청이 성공해야 합니다")

		// diff에서 existing.txt 파일 찾기
		var modifiedFile *struct {
			Path      string `json:"path"`
			Status    string `json:"status"`
			Additions int    `json:"additions"`
			Deletions int    `json:"deletions"`
		}

		for i := range diffResponse.Files {
			if diffResponse.Files[i].Path == "existing.txt" {
				modifiedFile = &diffResponse.Files[i]
				break
			}
		}

		require.NotNil(t, modifiedFile, "existing.txt가 diff에 있어야 합니다")
		assert.Equal(t, "modified", modifiedFile.Status, "파일이 수정됨으로 표시되어야 합니다")
		assert.Equal(t, 1, modifiedFile.Additions, "정확히 1줄만 추가되어야 합니다")
		assert.Equal(t, 0, modifiedFile.Deletions, "삭제된 줄이 없어야 합니다")
		assert.Equal(t, 1, diffResponse.Stats.TotalAdditions, "전체 추가 줄 수는 1이어야 합니다")
		assert.Equal(t, 0, diffResponse.Stats.TotalDeletions, "전체 삭제 줄 수는 0이어야 합니다")
	})
}
