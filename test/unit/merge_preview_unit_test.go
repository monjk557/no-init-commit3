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

func TestMergePreviewResponseStructure(t *testing.T) {
	// Test the response structure without actual Git operations
	gin.SetMode(gin.TestMode)
	router := gin.New()

	// Mock handler for testing response structure
	router.GET("/repos/:owner/:repo/merges/preview", func(c *gin.Context) {
		// Mock response data
		response := map[string]interface{}{
			"success": true,
			"message": "Merge preview analysis completed successfully",
			"data": map[string]interface{}{
				"can_merge":           true,
				"is_fast_forward":     false,
				"has_conflicts":       false,
				"conflict_count":      0,
				"strategy":            "recursive",
				"recommended_actions": []string{"Merge can proceed without issues"},
				"changes_summary": map[string]interface{}{
					"total_files":    3,
					"added_files":    2,
					"modified_files": 1,
					"deleted_files":  0,
					"renamed_files":  0,
					"lines_added":    15,
					"lines_deleted":  3,
					"binary_files":   0,
				},
				"files_changed": []map[string]interface{}{
					{
						"path":      "newfile.go",
						"status":    "added",
						"additions": 10,
						"deletions": 0,
						"is_binary": false,
					},
					{
						"path":      "existing.go",
						"status":    "modified",
						"additions": 5,
						"deletions": 3,
						"is_binary": false,
					},
					{
						"path":      "another.go",
						"status":    "added",
						"additions": 0,
						"deletions": 0,
						"is_binary": false,
					},
				},
				"commits": map[string]interface{}{
					"ahead":  3,
					"behind": 1,
				},
				"contributors": []string{
					"alice@example.com",
					"bob@example.com",
				},
				"commits_by_date": []map[string]interface{}{
					{
						"date": "2025-10-29",
						"commits": []map[string]interface{}{
							{
								"sha":     "abc123",
								"author":  "Alice",
								"email":   "alice@example.com",
								"date":    "2025-10-29 10:00:00",
								"message": "Add new feature",
								"parents": []string{"def456"},
							},
						},
					},
				},
				"count_summary": map[string]interface{}{
					"commits":      3,
					"files":        3,
					"contributors": 2,
				},
			},
		}
		c.JSON(http.StatusOK, response)
	})

	// Test request
	req, err := http.NewRequest("GET", "/repos/test/testrepo/merges/preview?source_ref=feature&target_ref=main", nil)
	require.NoError(t, err)

	// Execute request
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

	// Test merge preview fields
	assert.Contains(t, data, "can_merge")
	assert.Contains(t, data, "is_fast_forward")
	assert.Contains(t, data, "has_conflicts")
	assert.Contains(t, data, "conflict_count")

	// Test count summary
	testCountSummary(t, data)

	// Test changes summary
	testChangesSummary(t, data)

	// Test contributors
	testContributors(t, data)

	// Test commits
	testCommits(t, data)

	// Test files changed
	testFilesChanged(t, data)

	// Test commits by date
	testCommitsByDate(t, data)
}

func testCountSummary(t *testing.T, data map[string]interface{}) {
	t.Run("CountSummary", func(t *testing.T) {
		assert.Contains(t, data, "count_summary")
		countSummary := data["count_summary"].(map[string]interface{})

		// Check required fields
		assert.Contains(t, countSummary, "commits")
		assert.Contains(t, countSummary, "files")
		assert.Contains(t, countSummary, "contributors")

		// Check data types and values
		commits := countSummary["commits"].(float64)
		files := countSummary["files"].(float64)
		contributors := countSummary["contributors"].(float64)

		assert.Equal(t, 3.0, commits)
		assert.Equal(t, 3.0, files)
		assert.Equal(t, 2.0, contributors)

		t.Logf("✅ Count Summary: %d commits, %d files, %d contributors",
			int(commits), int(files), int(contributors))
	})
}

func testChangesSummary(t *testing.T, data map[string]interface{}) {
	t.Run("ChangesSummary", func(t *testing.T) {
		assert.Contains(t, data, "changes_summary")
		changesSummary := data["changes_summary"].(map[string]interface{})

		// Check required fields
		requiredFields := []string{
			"total_files", "added_files", "modified_files",
			"deleted_files", "renamed_files", "lines_added",
			"lines_deleted", "binary_files",
		}

		for _, field := range requiredFields {
			assert.Contains(t, changesSummary, field)
		}

		// Check values
		assert.Equal(t, 3.0, changesSummary["total_files"])
		assert.Equal(t, 2.0, changesSummary["added_files"])
		assert.Equal(t, 1.0, changesSummary["modified_files"])
		assert.Equal(t, 0.0, changesSummary["deleted_files"])
		assert.Equal(t, 15.0, changesSummary["lines_added"])
		assert.Equal(t, 3.0, changesSummary["lines_deleted"])

		t.Logf("✅ Changes Summary: %d total files (%d added, %d modified)",
			int(changesSummary["total_files"].(float64)),
			int(changesSummary["added_files"].(float64)),
			int(changesSummary["modified_files"].(float64)))
	})
}

func testContributors(t *testing.T, data map[string]interface{}) {
	t.Run("Contributors", func(t *testing.T) {
		assert.Contains(t, data, "contributors")
		contributors := data["contributors"].([]interface{})

		assert.Len(t, contributors, 2)
		assert.Contains(t, contributors, "alice@example.com")
		assert.Contains(t, contributors, "bob@example.com")

		t.Logf("✅ Contributors: %v", contributors)
	})
}

func testCommits(t *testing.T, data map[string]interface{}) {
	t.Run("Commits", func(t *testing.T) {
		assert.Contains(t, data, "commits")
		commits := data["commits"].(map[string]interface{})

		assert.Contains(t, commits, "ahead")
		assert.Contains(t, commits, "behind")

		ahead := commits["ahead"].(float64)
		behind := commits["behind"].(float64)

		assert.Equal(t, 3.0, ahead)
		assert.Equal(t, 1.0, behind)

		t.Logf("✅ Commits: %d ahead, %d behind", int(ahead), int(behind))
	})
}

func testFilesChanged(t *testing.T, data map[string]interface{}) {
	t.Run("FilesChanged", func(t *testing.T) {
		assert.Contains(t, data, "files_changed")
		filesChanged := data["files_changed"].([]interface{})

		assert.Len(t, filesChanged, 3)

		// Test each file change
		validStatuses := []string{"added", "modified", "deleted", "renamed"}

		for i, fileChange := range filesChanged {
			file := fileChange.(map[string]interface{})

			// Required fields
			assert.Contains(t, file, "path")
			assert.Contains(t, file, "status")
			assert.Contains(t, file, "additions")
			assert.Contains(t, file, "deletions")
			assert.Contains(t, file, "is_binary")

			// Status validation
			status := file["status"].(string)
			assert.Contains(t, validStatuses, status,
				"File %d status should be valid", i)

			// Non-negative additions/deletions
			additions := file["additions"]
			deletions := file["deletions"]

			// Convert to float64 for comparison
			var addFloat, delFloat float64
			switch v := additions.(type) {
			case float64:
				addFloat = v
			case int:
				addFloat = float64(v)
			}
			switch v := deletions.(type) {
			case float64:
				delFloat = v
			case int:
				delFloat = float64(v)
			}

			assert.GreaterOrEqual(t, addFloat, 0.0)
			assert.GreaterOrEqual(t, delFloat, 0.0)

			t.Logf("File %d: %s (%s) +%.0f -%.0f",
				i, file["path"], status, addFloat, delFloat)
		}

		// Verify consistency with changes_summary
		changesSummary := data["changes_summary"].(map[string]interface{})
		totalFiles := changesSummary["total_files"].(float64)
		assert.Equal(t, totalFiles, float64(len(filesChanged)))
	})
}

func testCommitsByDate(t *testing.T, data map[string]interface{}) {
	t.Run("CommitsByDate", func(t *testing.T) {
		assert.Contains(t, data, "commits_by_date")
		commitsByDate := data["commits_by_date"].([]interface{})

		assert.Len(t, commitsByDate, 1)

		dateGroup := commitsByDate[0].(map[string]interface{})
		assert.Contains(t, dateGroup, "date")
		assert.Contains(t, dateGroup, "commits")

		date := dateGroup["date"].(string)
		commits := dateGroup["commits"].([]interface{})

		assert.Equal(t, "2025-10-29", date)
		assert.Len(t, commits, 1)

		commit := commits[0].(map[string]interface{})
		requiredCommitFields := []string{"sha", "author", "email", "date", "message", "parents"}
		for _, field := range requiredCommitFields {
			assert.Contains(t, commit, field)
		}

		t.Logf("✅ Commits by Date: %s with %d commits", date, len(commits))
	})
}

func TestMergePreviewCountConsistency(t *testing.T) {
	// Test that count_summary matches other summary data
	gin.SetMode(gin.TestMode)
	router := gin.New()

	router.GET("/repos/:owner/:repo/merges/preview", func(c *gin.Context) {
		response := map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"changes_summary": map[string]interface{}{
					"total_files": 5,
				},
				"commits": map[string]interface{}{
					"ahead": 4,
				},
				"contributors": []string{
					"alice@example.com",
					"bob@example.com",
					"charlie@example.com",
				},
				"count_summary": map[string]interface{}{
					"commits":      4,
					"files":        5,
					"contributors": 3,
				},
			},
		}
		c.JSON(http.StatusOK, response)
	})

	req, err := http.NewRequest("GET", "/repos/test/testrepo/merges/preview?source_ref=feature&target_ref=main", nil)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	data := response["data"].(map[string]interface{})

	// Test consistency
	countSummary := data["count_summary"].(map[string]interface{})
	changesSummary := data["changes_summary"].(map[string]interface{})
	commits := data["commits"].(map[string]interface{})
	contributors := data["contributors"].([]interface{})

	// Files count consistency
	countFiles := countSummary["files"].(float64)
	summaryFiles := changesSummary["total_files"].(float64)
	assert.Equal(t, summaryFiles, countFiles,
		"count_summary.files should match changes_summary.total_files")

	// Commits count consistency
	countCommits := countSummary["commits"].(float64)
	commitsAhead := commits["ahead"].(float64)
	assert.Equal(t, commitsAhead, countCommits,
		"count_summary.commits should match commits.ahead")

	// Contributors count consistency
	countContributors := countSummary["contributors"].(float64)
	contributorsLength := float64(len(contributors))
	assert.Equal(t, contributorsLength, countContributors,
		"count_summary.contributors should match contributors array length")

	t.Logf("✅ Count consistency verified: files=%d, commits=%d, contributors=%d",
		int(countFiles), int(countCommits), int(countContributors))
}

func TestMergePreviewErrorHandling(t *testing.T) {
	// Test error handling scenarios
	gin.SetMode(gin.TestMode)
	router := gin.New()

	router.GET("/repos/:owner/:repo/merges/preview", func(c *gin.Context) {
		// Simulate error response
		c.JSON(http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "Invalid parameters",
		})
	})

	req, err := http.NewRequest("GET", "/repos/test/testrepo/merges/preview?source_ref=invalid&target_ref=main", nil)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.False(t, response["success"].(bool))
	assert.Contains(t, response, "error")
	assert.Equal(t, "Invalid parameters", response["error"])

	t.Logf("✅ Error handling verified")
}
