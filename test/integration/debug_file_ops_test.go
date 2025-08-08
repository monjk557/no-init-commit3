package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDebugFileOperationsAPI(t *testing.T) {
	suite := SetupFileOperationsIntegrationTest(t)
	defer suite.Cleanup()

	t.Run("debug file creation", func(t *testing.T) {
		requestBody := map[string]interface{}{
			"content":        "test content",
			"commit_message": "Test commit",
			"branch":         "master",
			"author_name":    "Test Developer",
			"author_email":   "dev@example.com",
		}

		resp := suite.makeRequest(t, "PUT", "/repos/testowner/testrepo/files/debug.txt", requestBody)
		defer func() { _ = resp.Body.Close() }() // errcheck

		// Read the full response body
		bodyBytes, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		fmt.Printf("Status Code: %d\n", resp.StatusCode)
		fmt.Printf("Response Headers: %+v\n", resp.Header)
		fmt.Printf("Response Body: %s\n", string(bodyBytes))

		// Try to parse as JSON
		var response map[string]interface{}
		if err := json.Unmarshal(bodyBytes, &response); err != nil {
			fmt.Printf("JSON Parse Error: %v\n", err)
		} else {
			fmt.Printf("Parsed JSON: %+v\n", response)
		}

		// Also check if it's an error response
		if resp.StatusCode != http.StatusOK {
			fmt.Printf("This is an error response - status %d\n", resp.StatusCode)
		}
	})
}
