package diffs

import (
	"testing"
	"unicode/utf8"
)

func TestParseNameStatus_KoreanFilenames(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []nameStatusEntry
	}{
		{
			name:  "Korean filename already decoded (core.quotePath=false)",
			input: "M\t한글파일.txt",
			want: []nameStatusEntry{
				{status: FileDiffStatusModified, path: "한글파일.txt"},
			},
		},
		{
			name:  "Korean filename with octal escapes (core.quotePath=true fallback)",
			input: "M\t\"\355\225\234\352\270\200\355\214\214\354\235\274.txt\"",
			want: []nameStatusEntry{
				{status: FileDiffStatusModified, path: "한글파일.txt"},
			},
		},
		{
			name:  "rename with Korean paths",
			input: "R100\t\"\354\235\264\354\240\204.txt\"\t\"\354\235\264\355\233\204.txt\"",
			want: []nameStatusEntry{
				{status: FileDiffStatusRenamed, oldPath: "이전.txt", path: "이후.txt"},
			},
		},
		{
			name:  "mixed ASCII and Korean",
			input: "A\tSKY-06-redis-sdk추가/config.go\nD\t\"\353\254\270\354\204\234.md\"",
			want: []nameStatusEntry{
				{status: FileDiffStatusAdded, path: "SKY-06-redis-sdk추가/config.go"},
				{status: FileDiffStatusDeleted, path: "문서.md"},
			},
		},
		{
			name:  "multiple files with various statuses",
			input: "M\tmain.go\nA\t\"\355\205\214\354\212\244\355\212\270.go\"\nD\told.txt",
			want: []nameStatusEntry{
				{status: FileDiffStatusModified, path: "main.go"},
				{status: FileDiffStatusAdded, path: "테스트.go"},
				{status: FileDiffStatusDeleted, path: "old.txt"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNameStatus(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("parseNameStatus() returned %d entries, want %d", len(got), len(tt.want))
			}
			for i, entry := range got {
				if entry.status != tt.want[i].status {
					t.Errorf("entry[%d].status = %q, want %q", i, entry.status, tt.want[i].status)
				}
				if entry.path != tt.want[i].path {
					t.Errorf("entry[%d].path = %q, want %q", i, entry.path, tt.want[i].path)
				}
				if entry.oldPath != tt.want[i].oldPath {
					t.Errorf("entry[%d].oldPath = %q, want %q", i, entry.oldPath, tt.want[i].oldPath)
				}
				// All paths must be valid UTF-8
				if !utf8.ValidString(entry.path) {
					t.Errorf("entry[%d].path is not valid UTF-8: %q", i, entry.path)
				}
			}
		})
	}
}

func TestParseNumstat_KoreanFilenames(t *testing.T) {
	// Numstat doesn't include filenames in the stat part,
	// but verify it handles binary markers correctly alongside Korean files
	input := "10\t5\t\"\355\225\234\352\270\200.txt\"\n-\t-\timage.png"
	entries := parseNumstat(input)

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	if entries[0].additions != 10 || entries[0].deletions != 5 {
		t.Errorf("entry[0]: additions=%d deletions=%d, want 10/5", entries[0].additions, entries[0].deletions)
	}
	if !entries[1].isBinary {
		t.Error("entry[1] should be binary")
	}
}
