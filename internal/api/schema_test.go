package api

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErrorResponseJSONRoundTrip(t *testing.T) {
	t.Run("with code", func(t *testing.T) {
		in := ErrorResponse{
			Success: false,
			Error:   "something broke",
			Code:    ErrCodeInvalidInput,
		}
		b, err := json.Marshal(in)
		require.NoError(t, err)

		var keys map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(b, &keys))
		assert.Contains(t, keys, "success")
		assert.Contains(t, keys, "error")
		assert.Contains(t, keys, "code")

		var out ErrorResponse
		require.NoError(t, json.Unmarshal(b, &out))
		assert.Equal(t, in, out)
	})

	t.Run("empty code is omitted", func(t *testing.T) {
		// 회귀: Code 는 omitempty — 빈 값이면 직렬화에서 빠진다.
		b, err := json.Marshal(ErrorResponse{Success: false, Error: "no code"})
		require.NoError(t, err)

		var keys map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(b, &keys))
		assert.NotContains(t, keys, "code")
		assert.Contains(t, keys, "success")
		assert.Contains(t, keys, "error")
	})
}

func TestErrorCodeEnumValues(t *testing.T) {
	// raw 값이 prefix 없이 그대로 유지되는지 확인 (Doc-500 규약).
	cases := map[ErrorCodeEnum]string{
		ErrCodeBranchAlreadyExists:     "BRANCH_ALREADY_EXISTS",
		ErrCodeCommitNotFound:          "COMMIT_NOT_FOUND",
		ErrCodeConcurrentLimitExceeded: "CONCURRENT_LIMIT_EXCEEDED",
		ErrCodeConflict:                "CONFLICT",
		ErrCodeInternalError:           "INTERNAL_ERROR",
		ErrCodeInvalidInput:            "INVALID_INPUT",
		ErrCodeNotFound:                "NOT_FOUND",
		ErrCodeRateLimitExceeded:       "RATE_LIMIT_EXCEEDED",
		ErrCodeUnknown:                 "UNKNOWN",
		ErrCodeUnknownError:            "UNKNOWN_ERROR",
	}
	for enum, want := range cases {
		assert.Equal(t, want, string(enum))
	}
}
