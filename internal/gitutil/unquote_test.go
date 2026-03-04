package gitutil

import (
	"testing"
	"unicode/utf8"
)

func TestUnquoteGitPath(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "unquoted ASCII path",
			input: "src/main.go",
			want:  "src/main.go",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "quoted ASCII path with no escapes",
			input: `"src/main.go"`,
			want:  "src/main.go",
		},
		{
			name:  "Korean filename with octal escapes (ㅁㄴ)",
			input: `"\343\205\201\343\204\264"`,
			want:  "ㅁㄴ",
		},
		{
			name:  "Korean path with directory",
			input: `"src/\355\225\234\352\270\200/test.go"`,
			want:  "src/한글/test.go",
		},
		{
			name:  "mixed ASCII and Korean",
			input: `"README-\355\225\234\352\270\200.md"`,
			want:  "README-한글.md",
		},
		{
			name:  "backslash escape",
			input: `"path\\to\\file"`,
			want:  `path\to\file`,
		},
		{
			name:  "quote escape",
			input: `"file\"name"`,
			want:  `file"name`,
		},
		{
			name:  "tab and newline escapes",
			input: `"col1\tcol2\nrow2"`,
			want:  "col1\tcol2\nrow2",
		},
		{
			name:  "single char — not quoted",
			input: `x`,
			want:  "x",
		},
		{
			name:  "추가 (Korean for 'addition') via octal",
			input: `"\354\266\224\352\260\200"`,
			want:  "추가",
		},
		// Real-world Korean filename test cases
		{
			name:  "한글파일 — exact scenario from user report",
			input: `"\355\225\234\352\270\200\355\214\214\354\235\274"`,
			want:  "한글파일",
		},
		{
			name:  "Korean branch name: SKY-06-redis-sdk추가",
			input: `"SKY-06-redis-sdk\354\266\224\352\260\200"`,
			want:  "SKY-06-redis-sdk추가",
		},
		{
			name:  "Korean filename in nested path",
			input: `"docs/\353\254\270\354\204\234/\355\205\214\354\212\244\355\212\270.txt"`,
			want:  "docs/문서/테스트.txt",
		},
		{
			name:  "Japanese filename via octal",
			input: `"\343\203\206\343\202\271\343\203\210.txt"`,
			want:  "テスト.txt",
		},
		{
			name:  "Chinese filename via octal",
			input: `"\346\265\213\350\257\225.txt"`,
			want:  "测试.txt",
		},
		{
			name:  "emoji filename via octal (4-byte UTF-8)",
			input: `"\360\237\232\200.md"`,
			want:  "🚀.md",
		},
		{
			name:  "already UTF-8 (core.quotePath=false output)",
			input: "한글파일.txt",
			want:  "한글파일.txt",
		},
		{
			name:  "bell and form-feed escapes",
			input: `"\a\b\f\v"`,
			want:  "\a\b\f\v",
		},
		{
			name:  "trailing backslash at end of quoted string",
			input: `"file\"`,
			want:  "file\\",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UnquoteGitPath(tt.input)
			if got != tt.want {
				t.Errorf("UnquoteGitPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestUnquoteGitPath_AlwaysValidUTF8(t *testing.T) {
	// All git-quoted Korean/CJK paths should produce valid UTF-8
	inputs := []string{
		`"\355\225\234\352\270\200\355\214\214\354\235\274"`,                      // 한글파일
		`"\354\266\224\352\260\200"`,                                              // 추가
		`"src/\353\254\270\354\204\234/\355\205\214\354\212\244\355\212\270.txt"`, // src/문서/테스트.txt
		`"\343\203\206\343\202\271\343\203\210"`,                                  // テスト
		`"\346\265\213\350\257\225"`,                                              // 测试
		`"\360\237\232\200"`,                                                      // 🚀
		`"plain-ascii"`,
		"unquoted-ascii",
	}

	for _, input := range inputs {
		got := UnquoteGitPath(input)
		if !utf8.ValidString(got) {
			t.Errorf("UnquoteGitPath(%q) produced invalid UTF-8: %q", input, got)
		}
	}
}

func TestUnquoteGitPath_RoundTrip(t *testing.T) {
	// Verify that common Korean filenames survive the git quoting → unquoting cycle
	koreanPaths := map[string]string{
		`"\355\225\234\352\270\200\355\214\214\354\235\274"`:      "한글파일",
		`"\354\266\224\352\260\200"`:                              "추가",
		`"SKY-06-redis-sdk\354\266\224\352\260\200"`:              "SKY-06-redis-sdk추가",
		`"\353\246\254\353\223\234\353\257\270"`:                  "리드미",
		`"\354\204\244\354\240\225\355\214\214\354\235\274.yaml"`: "설정파일.yaml",
	}

	for quoted, expected := range koreanPaths {
		got := UnquoteGitPath(quoted)
		if got != expected {
			t.Errorf("Round-trip failed: UnquoteGitPath(%q) = %q, want %q", quoted, got, expected)
		}
		if !utf8.ValidString(got) {
			t.Errorf("Result is not valid UTF-8: %q", got)
		}
	}
}
