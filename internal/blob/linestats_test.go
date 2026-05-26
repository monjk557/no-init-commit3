package blob

import (
	"strings"
	"testing"
)

func TestComputeLineStats(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantLineCount  int
		wantMaxLineLen int
	}{
		{
			name:           "empty",
			input:          "",
			wantLineCount:  0,
			wantMaxLineLen: 0,
		},
		{
			name:           "single line no trailing newline",
			input:          "hello",
			wantLineCount:  1,
			wantMaxLineLen: 5,
		},
		{
			name:           "single line with trailing newline",
			input:          "hello\n",
			wantLineCount:  1,
			wantMaxLineLen: 5,
		},
		{
			name:           "two lines",
			input:          "hello\nworld\n",
			wantLineCount:  2,
			wantMaxLineLen: 5,
		},
		{
			name:           "lines of different lengths",
			input:          "a\nbbbbb\ncc\n",
			wantLineCount:  3,
			wantMaxLineLen: 5,
		},
		{
			name:           "crlf line endings",
			input:          "a\r\nbb\r\nccc\r\n",
			wantLineCount:  3,
			wantMaxLineLen: 3,
		},
		{
			name:           "mixed crlf and lf",
			input:          "a\nbb\r\nccc\n",
			wantLineCount:  3,
			wantMaxLineLen: 3,
		},
		{
			name:           "last line no newline",
			input:          "a\nbb",
			wantLineCount:  2,
			wantMaxLineLen: 2,
		},
		{
			name:           "very long single line — inline sourcemap shape",
			input:          "//# sourceMappingURL=data:" + strings.Repeat("x", 100000),
			wantLineCount:  1,
			wantMaxLineLen: len("//# sourceMappingURL=data:") + 100000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeLineStats([]byte(tt.input))
			if got.LineCount != tt.wantLineCount {
				t.Errorf("LineCount = %d, want %d", got.LineCount, tt.wantLineCount)
			}
			if got.MaxLineLength != tt.wantMaxLineLen {
				t.Errorf("MaxLineLength = %d, want %d", got.MaxLineLength, tt.wantMaxLineLen)
			}
		})
	}
}

// TestComputeLineStats_NoCopy 는 ComputeLineStats 가 입력 slice 를 복제하지
// 않고 직접 순회한다는 것을 간접 검증한다 — 783KB 입력에 대해 normal time
// 으로 끝나면 OK (기존 strings.ReplaceAll 기반 코드는 메모리 2배 + 시간 2배
// 였음).
func TestComputeLineStats_LargeInput(t *testing.T) {
	// 1MB 입력 — 1만 라인 × 100 chars + 1 라인 999990 chars (가짜 sourcemap)
	var buf strings.Builder
	for i := 0; i < 10000; i++ {
		buf.WriteString(strings.Repeat("a", 100))
		buf.WriteByte('\n')
	}
	buf.WriteString(strings.Repeat("z", 999990))
	input := []byte(buf.String())

	stats := ComputeLineStats(input)
	if stats.LineCount != 10001 {
		t.Errorf("LineCount = %d, want 10001", stats.LineCount)
	}
	if stats.MaxLineLength != 999990 {
		t.Errorf("MaxLineLength = %d, want 999990", stats.MaxLineLength)
	}
}
