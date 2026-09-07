package contentkind

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_IsBinary(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want bool
	}{
		{"plain text", []byte("hello\nworld\n"), false},
		{"empty", nil, false},
		{"NUL byte", []byte("abc\x00def"), true},
		{"PNG header", []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsBinary(tc.in))
		})
	}
}

// Test_IsBinary_TruncatedMultibyteRune 은 판정용으로 앞부분만 넘겼을 때
// 멀티바이트 문자가 경계에서 잘려도 텍스트로 남는지 확인한다. UTF-8 유효성
// 기준을 쓰면 여기서 바이너리로 오판된다 (8KB 넘는 한글 소스 전부 해당).
func Test_IsBinary_TruncatedMultibyteRune(t *testing.T) {
	korean := strings.Repeat("가나다라마바사", 2000) // 8KB 훨씬 초과
	head := []byte(korean)[:SniffBytes]

	assert.False(t, IsBinary(head), "잘린 룬 때문에 바이너리로 보면 안 된다")
}

// Test_IsBinary_NonUTF8Text 는 UTF-8 이 아닌 텍스트(CP949 등)를 살리는지 본다.
func Test_IsBinary_NonUTF8Text(t *testing.T) {
	cp949 := []byte{0xB0, 0xA1, 0xB3, 0xAA, '\n'} // "가나" CP949

	assert.False(t, IsBinary(cp949))
}

// Test_IsBinary_BoundedScan 은 SniffBytes 이후의 NUL 을 보지 않는지 확인한다
// — 판정 비용을 파일 크기와 무관하게 유지하기 위한 계약.
func Test_IsBinary_BoundedScan(t *testing.T) {
	late := append(bytes.Repeat([]byte("a"), SniffBytes+10), 0x00)

	assert.False(t, IsBinary(late))
}

func Test_IsBinaryPath(t *testing.T) {
	assert.True(t, IsBinaryPath("image.PNG"))
	assert.True(t, IsBinaryPath("a/b/archive.zip"))
	assert.True(t, IsBinaryPath("report.xlsx"))
	assert.False(t, IsBinaryPath("source.go"))
	assert.False(t, IsBinaryPath("README"))
}
