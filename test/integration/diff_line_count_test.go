package integration

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

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
			"branch": branchName, // base_branch가 아니라 branch 필드 사용
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
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// 커밋이 성공했는지 확인
		var commitResponse map[string]interface{}
		err = json.NewDecoder(resp.Body).Decode(&commitResponse)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, true, commitResponse["success"], "커밋이 성공해야 합니다")

		// 브랜치 참조가 업데이트될 시간을 주기 위해 약간 대기
		// (파일 시스템 동기화 대기)
		time.Sleep(100 * time.Millisecond)
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

		// 라인 변경사항 확인
		// Note: Changes 배열에는 diff format의 헤더 등이 포함될 수 있으므로
		// Additions 필드가 더 정확한 지표입니다
		if assert.NotNil(t, singleLineFile.Changes, "Changes가 nil이 아니어야 합니다") {
			// Changes 배열에 최소한 하나의 "add" 타입이 있어야 함
			addCount := 0
			for _, change := range singleLineFile.Changes {
				if change.Type == "add" {
					addCount++
				}
			}
			// Additions 필드가 정확한 지표이므로, 최소한 1개 이상의 add가 있으면 됨
			assert.GreaterOrEqual(t, addCount, 1, "최소한 1개 이상의 'add' 타입 변경이 있어야 합니다")
		}
	})
}

// TestDiffLineCount_SingleLineAddedToExistingFile는 기존 파일에 1줄을 추가했을 때를 테스트합니다
func TestDiffLineCount_SingleLineAddedToExistingFile(t *testing.T) {
	suite := SetupIntegrationTest(t)

	// 커밋 SHA를 저장할 변수
	var firstCommitSHA, secondCommitSHA string

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
			"branch": branchName, // base_branch가 아니라 branch 필드 사용
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
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// 커밋 성공 확인 및 SHA 추출
		var commitResponse map[string]interface{}
		err = json.NewDecoder(resp.Body).Decode(&commitResponse)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, true, commitResponse["success"], "커밋이 성공해야 합니다")

		// 커밋 SHA 추출 (data 객체 안에 있음)
		if data, ok := commitResponse["data"].(map[string]interface{}); ok {
			if sha, ok := data["commit_sha"].(string); ok && sha != "" {
				firstCommitSHA = sha
			}
		}
		time.Sleep(100 * time.Millisecond)
	})

	// 파일에 한 줄 더 추가
	t.Run("AddSecondLine", func(t *testing.T) {
		commitBody := map[string]interface{}{
			"branch": branchName, // base_branch가 아니라 branch 필드 사용
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
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// 커밋 성공 확인 및 SHA 추출
		var commitResponse map[string]interface{}
		err = json.NewDecoder(resp.Body).Decode(&commitResponse)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, true, commitResponse["success"], "커밋이 성공해야 합니다")

		// 커밋 SHA 추출 (data 객체 안에 있음)
		if data, ok := commitResponse["data"].(map[string]interface{}); ok {
			if sha, ok := data["commit_sha"].(string); ok && sha != "" {
				secondCommitSHA = sha
			}
		}
		time.Sleep(100 * time.Millisecond)
	})

	// diff를 위한 커밋 SHA 가져오기
	t.Run("GetCommitSHA", func(t *testing.T) {
		// 커밋 응답에서 직접 SHA를 사용하거나, 없으면 커밋 목록에서 찾기
		var parentSHA, currentSHA string

		if firstCommitSHA != "" && secondCommitSHA != "" {
			// 커밋 응답에서 직접 SHA를 사용
			parentSHA = firstCommitSHA
			currentSHA = secondCommitSHA
		} else {
			// 커밋 응답에서 SHA를 못 찾았으면, 커밋 목록에서 찾기
			// 최신 커밋을 찾기 위해 커밋 목록 가져오기
			commitsResp, err := suite.makeRequest("GET", "/repos/testowner/testrepo/commits?branch="+branchName+"&limit=5", nil)
			require.NoError(t, err)
			defer func() { _ = commitsResp.Body.Close() }()

			assert.Equal(t, http.StatusOK, commitsResp.StatusCode)

			// 응답 구조 확인을 위해 먼저 map으로 파싱
			var rawResponse map[string]interface{}
			err = json.NewDecoder(commitsResp.Body).Decode(&rawResponse)
			require.NoError(t, err)

			// results 필드에서 커밋 목록 추출
			results, ok := rawResponse["results"].([]interface{})
			require.True(t, ok, "results should be an array")
			require.True(t, len(results) >= 2, "최소 2개의 커밋이 있어야 합니다 (현재: %d)", len(results))

			parentSHA = results[1].(map[string]interface{})["sha"].(string)
			currentSHA = results[0].(map[string]interface{})["sha"].(string)
		}

		// 두 커밋 간의 diff 가져오기
		resp, err := suite.makeRequest("GET", "/repos/testowner/testrepo/diff/commits/"+parentSHA+"/"+currentSHA, nil)
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

		// diff 알고리즘에 따라 "First line"이 삭제되고 "First line\nSecond line"이 추가되는 것으로 계산될 수 있음
		// 실제로는 "Second line"만 추가된 것이므로, 순 추가는 1줄이어야 함
		// 순 추가 = Additions - Deletions
		netAdditions := modifiedFile.Additions - modifiedFile.Deletions
		assert.Equal(t, 1, netAdditions, "순 추가 줄 수는 1이어야 합니다 (Additions=%d, Deletions=%d)",
			modifiedFile.Additions, modifiedFile.Deletions)

		// 전체 통계도 순 추가 확인
		netTotalAdditions := diffResponse.Stats.TotalAdditions - diffResponse.Stats.TotalDeletions
		assert.Equal(t, 1, netTotalAdditions, "전체 순 추가 줄 수는 1이어야 합니다 (TotalAdditions=%d, TotalDeletions=%d)",
			diffResponse.Stats.TotalAdditions, diffResponse.Stats.TotalDeletions)
	})
}
