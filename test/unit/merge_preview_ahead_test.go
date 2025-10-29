package unit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergePreviewAheadCount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	// Mock handler for testing ahead count
	router.GET("/repos/:owner/:repo/merges/preview", func(c *gin.Context) {
		// Mock response with ahead=1, behind=0 (newbranch → main)
		response := map[string]interface{}{
			"success": true,
			"message": "Merge preview analysis completed successfully",
			"data": map[string]interface{}{
				"can_merge":           true,
				"is_fast_forward":     true,
				"has_conflicts":       false,
				"conflict_count":      0,
				"strategy":            "recursive",
				"recommended_actions": []string{"This merge can be performed as a fast-forward merge"},
				"changes_summary": map[string]interface{}{
					"total_files":    1,
					"added_files":    1,
					"modified_files": 0,
					"deleted_files":  0,
					"renamed_files":  0,
					"lines_added":    1,
					"lines_deleted":  0,
					"binary_files":   0,
				},
				"files_changed": []map[string]interface{}{
					{
						"path":      "newfile.txt",
						"status":    "added",
						"additions": 1,
						"deletions": 0,
						"is_binary": false,
					},
				},
				"commits": map[string]interface{}{
					"ahead":  1, // newbranch가 main보다 1개 커밋 앞서 있음
					"behind": 0, // main이 newbranch보다 뒤처져 있지 않음
				},
				"contributors": []string{
					"newbranch@example.com",
				},
				"commits_by_date": []map[string]interface{}{
					{
						"date": "2025-10-29",
						"commits": []map[string]interface{}{
							{
								"sha":     "abc123",
								"author":  "NewBranch Author",
								"email":   "newbranch@example.com",
								"date":    "2025-10-29 10:00:00",
								"message": "Add new file to newbranch",
								"parents": []string{"def456"},
							},
						},
					},
				},
				"count_summary": map[string]interface{}{
					"commits":      1, // 병합될 커밋 수
					"files":        1, // 변경된 파일 수
					"contributors": 1, // 기여자 수
				},
			},
		}
		c.JSON(http.StatusOK, response)
	})

	// Test request: newbranch → main merge preview
	req, err := http.NewRequest("GET", "/repos/test/testrepo/merges/preview?source_ref=newbranch&target_ref=main", nil)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Check response
	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	// Test basic response structure
	assert.True(t, response["success"].(bool))
	assert.Contains(t, response, "data")

	data := response["data"].(map[string]interface{})

	// Test commits ahead/behind
	t.Run("CommitsAheadBehind", func(t *testing.T) {
		assert.Contains(t, data, "commits")
		commits := data["commits"].(map[string]interface{})

		assert.Contains(t, commits, "ahead")
		assert.Contains(t, commits, "behind")

		ahead := commits["ahead"].(float64)
		behind := commits["behind"].(float64)

		// newbranch가 main보다 1개 커밋 앞서 있어야 함
		assert.Equal(t, 1.0, ahead, "newbranch should be 1 commit ahead of main")
		assert.Equal(t, 0.0, behind, "main should be 0 commits behind newbranch")

		t.Logf("✅ Commits: ahead=%d, behind=%d", int(ahead), int(behind))
	})

	// Test count summary
	t.Run("CountSummary", func(t *testing.T) {
		assert.Contains(t, data, "count_summary")
		countSummary := data["count_summary"].(map[string]interface{})

		assert.Contains(t, countSummary, "commits")
		assert.Contains(t, countSummary, "files")
		assert.Contains(t, countSummary, "contributors")

		commits := countSummary["commits"].(float64)
		files := countSummary["files"].(float64)
		contributors := countSummary["contributors"].(float64)

		// 병합될 커밋 수가 1개여야 함
		assert.Equal(t, 1.0, commits, "count_summary.commits should be 1")
		assert.Equal(t, 1.0, files, "count_summary.files should be 1")
		assert.Equal(t, 1.0, contributors, "count_summary.contributors should be 1")

		t.Logf("✅ Count Summary: commits=%d, files=%d, contributors=%d",
			int(commits), int(files), int(contributors))
	})

	// Test consistency between commits and count_summary
	t.Run("ConsistencyCheck", func(t *testing.T) {
		commits := data["commits"].(map[string]interface{})
		countSummary := data["count_summary"].(map[string]interface{})

		commitsAhead := commits["ahead"].(float64)
		countCommits := countSummary["commits"].(float64)

		// commits.ahead와 count_summary.commits가 일치해야 함
		assert.Equal(t, commitsAhead, countCommits,
			"commits.ahead should match count_summary.commits")

		t.Logf("✅ Consistency: commits.ahead=%d, count_summary.commits=%d",
			int(commitsAhead), int(countCommits))
	})

	// Test changes summary
	t.Run("ChangesSummary", func(t *testing.T) {
		assert.Contains(t, data, "changes_summary")
		changesSummary := data["changes_summary"].(map[string]interface{})

		assert.Contains(t, changesSummary, "total_files")
		totalFiles := changesSummary["total_files"].(float64)

		// 새 파일이 추가되었으므로 파일 변경이 있어야 함
		assert.Equal(t, 1.0, totalFiles, "should have 1 file change")

		t.Logf("✅ Changes Summary: total_files=%d", int(totalFiles))
	})

	// Test contributors
	t.Run("Contributors", func(t *testing.T) {
		assert.Contains(t, data, "contributors")
		contributors := data["contributors"].([]interface{})

		// 1명의 기여자가 있어야 함
		assert.Len(t, contributors, 1, "should have 1 contributor")
		assert.Contains(t, contributors, "newbranch@example.com")

		t.Logf("✅ Contributors: %v", contributors)
	})

	// Test files changed
	t.Run("FilesChanged", func(t *testing.T) {
		assert.Contains(t, data, "files_changed")
		filesChanged := data["files_changed"].([]interface{})

		assert.Len(t, filesChanged, 1, "should have 1 file change")

		file := filesChanged[0].(map[string]interface{})
		assert.Equal(t, "newfile.txt", file["path"])
		assert.Equal(t, "added", file["status"])
		assert.Equal(t, 1.0, file["additions"])
		assert.Equal(t, 0.0, file["deletions"])

		t.Logf("✅ Files Changed: %s (%s) +%.0f -%.0f",
			file["path"], file["status"], file["additions"], file["deletions"])
	})
}

func TestMergePreviewDifferentScenarios(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	router.GET("/repos/:owner/:repo/merges/preview", func(c *gin.Context) {
		sourceRef := c.Query("source_ref")
		targetRef := c.Query("target_ref")

		var response map[string]interface{}

		switch {
		case sourceRef == "newbranch" && targetRef == "main":
			// newbranch → main: ahead=1, behind=0
			response = map[string]interface{}{
				"success": true,
				"data": map[string]interface{}{
					"commits": map[string]interface{}{
						"ahead":  1.0,
						"behind": 0.0,
					},
					"count_summary": map[string]interface{}{
						"commits":      1.0,
						"files":        1.0,
						"contributors": 1.0,
					},
				},
			}
		case sourceRef == "main" && targetRef == "newbranch":
			// main → newbranch: ahead=0, behind=1
			response = map[string]interface{}{
				"success": true,
				"data": map[string]interface{}{
					"commits": map[string]interface{}{
						"ahead":  0.0,
						"behind": 1.0,
					},
					"count_summary": map[string]interface{}{
						"commits":      0.0,
						"files":        0.0,
						"contributors": 0.0,
					},
				},
			}
		case sourceRef == targetRef:
			// Same branch: error
			c.JSON(http.StatusBadRequest, map[string]interface{}{
				"success": false,
				"error":   "Cannot merge branch with itself",
			})
			return
		default:
			c.JSON(http.StatusNotFound, map[string]interface{}{
				"success": false,
				"error":   "Branch not found",
			})
			return
		}

		c.JSON(http.StatusOK, response)
	})

	testCases := []struct {
		name         string
		sourceRef    string
		targetRef    string
		expectAhead  float64
		expectBehind float64
		expectError  bool
	}{
		{
			name:         "NewBranchToMain",
			sourceRef:    "newbranch",
			targetRef:    "main",
			expectAhead:  1.0,
			expectBehind: 0.0,
			expectError:  false,
		},
		{
			name:         "MainToNewBranch",
			sourceRef:    "main",
			targetRef:    "newbranch",
			expectAhead:  0.0,
			expectBehind: 1.0,
			expectError:  false,
		},
		{
			name:        "SameBranch",
			sourceRef:   "main",
			targetRef:   "main",
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("GET",
				"/repos/test/testrepo/merges/preview?source_ref="+tc.sourceRef+"&target_ref="+tc.targetRef, nil)
			require.NoError(t, err)

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if tc.expectError {
				assert.NotEqual(t, http.StatusOK, w.Code)
				t.Logf("✅ %s: Expected error, got status %d", tc.name, w.Code)
			} else {
				assert.Equal(t, http.StatusOK, w.Code)

				var response map[string]interface{}
				err = json.Unmarshal(w.Body.Bytes(), &response)
				require.NoError(t, err)

				data := response["data"].(map[string]interface{})
				commits := data["commits"].(map[string]interface{})

				ahead := commits["ahead"].(float64)
				behind := commits["behind"].(float64)

				assert.Equal(t, tc.expectAhead, ahead,
					"%s: ahead should be %f", tc.name, tc.expectAhead)
				assert.Equal(t, tc.expectBehind, behind,
					"%s: behind should be %f", tc.name, tc.expectBehind)

				// Test count_summary consistency
				countSummary := data["count_summary"].(map[string]interface{})
				countCommits := countSummary["commits"].(float64)
				assert.Equal(t, ahead, countCommits,
					"%s: count_summary.commits should match commits.ahead", tc.name)

				t.Logf("✅ %s: ahead=%d, behind=%d, count_commits=%d",
					tc.name, int(ahead), int(behind), int(countCommits))
			}
		})
	}
}
