package lfs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 회귀: "LFS pointer 파싱/포맷이 round-trip 으로 일관되어야 함"

const validOID = "4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393"

func validPointer() string {
	return FormatLFSPointer(validOID, 12345)
}

func TestFormatLFSPointer(t *testing.T) {
	got := FormatLFSPointer(validOID, 12345)
	want := "version https://git-lfs.github.com/spec/v1\n" +
		"oid sha256:" + validOID + "\n" +
		"size 12345\n"
	assert.Equal(t, want, got)
}

func TestIsLFSPointer(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"valid pointer", validPointer(), true},
		{"plain text", "hello world", false},
		{"empty", "", false},
		{"truncated", "version https://git-lfs.github.com/spec/v1\n", false},
		{"bad oid length", "version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 1\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsLFSPointer([]byte(tt.content)))
		})
	}
}

func TestParseLFSPointer(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		oid, size, ok := ParseLFSPointer([]byte(validPointer()))
		require.True(t, ok)
		assert.Equal(t, validOID, oid)
		assert.Equal(t, int64(12345), size)
	})

	t.Run("invalid returns ok=false", func(t *testing.T) {
		oid, size, ok := ParseLFSPointer([]byte("not a pointer"))
		assert.False(t, ok)
		assert.Empty(t, oid)
		assert.Zero(t, size)
	})

	t.Run("round-trip", func(t *testing.T) {
		content := FormatLFSPointer(validOID, 987654321)
		oid, size, ok := ParseLFSPointer([]byte(content))
		require.True(t, ok)
		assert.Equal(t, validOID, oid)
		assert.Equal(t, int64(987654321), size)
	})
}
