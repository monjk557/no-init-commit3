package observability

import (
	"testing"
	"unicode/utf8"
)

func TestNormalizeEndpoint_UTF8Safety(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "Korean branch name in path",
			input: "/repos/owner/repo/branches/resolve/SKY-06-redis-sdk추가",
		},
		{
			name:  "full Korean path",
			input: "/repos/owner/repo/tree/refs/heads/한글브랜치/문서/테스트파일.txt",
		},
		{
			name:  "Japanese characters",
			input: "/repos/owner/repo/blob/main/テスト/ファイル.go",
		},
		{
			name:  "Chinese characters",
			input: "/repos/owner/repo/tree/main/测试文件/配置.yaml",
		},
		{
			name:  "emoji in path",
			input: "/repos/owner/repo/blob/main/🚀/launch.go",
		},
		{
			name:  "long path with Korean exceeding byte limit",
			input: "/repos/owner/repo/tree/main/" + repeatString("한글테스트파일이름", 10),
		},
		{
			name:  "ASCII path",
			input: "/repos/owner/repo/branches",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeEndpoint(tt.input)

			// Result must always be valid UTF-8
			if !utf8.ValidString(result) {
				t.Errorf("normalizeEndpoint(%q) produced invalid UTF-8: %q", tt.input, result)
			}

			// Result must not be empty
			if result == "" {
				t.Errorf("normalizeEndpoint(%q) returned empty string", tt.input)
			}
		})
	}
}

func TestNormalizeEndpoint_TruncationIsRuneSafe(t *testing.T) {
	// Create a path that's > 200 bytes with multi-byte chars
	// Each Korean char is 3 bytes, so 70 chars = 210 bytes
	longKoreanPath := "/repos/o/r/"
	for i := 0; i < 70; i++ {
		longKoreanPath += "가"
	}

	result := normalizeEndpoint(longKoreanPath)

	// Must be valid UTF-8
	if !utf8.ValidString(result) {
		t.Fatalf("truncated path is not valid UTF-8: %q", result)
	}

	// Must end with "..."
	if len(result) > 10 && result[len(result)-3:] != "..." {
		t.Errorf("truncated path should end with '...': %q", result)
	}

	// Verify no byte-level truncation of multi-byte chars
	withoutEllipsis := result[:len(result)-3]
	if !utf8.ValidString(withoutEllipsis) {
		t.Errorf("truncated path (without ellipsis) is not valid UTF-8: %q", withoutEllipsis)
	}
}

func repeatString(s string, n int) string {
	result := ""
	for i := 0; i < n; i++ {
		result += s
	}
	return result
}
