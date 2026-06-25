package protocol

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWritePackfileSectionHeader 는 packfile 섹션 헤더 pkt-line 출력을 검증한다.
func TestWritePackfileSectionHeader(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WritePackfileSectionHeader(&buf))
	// "packfile" 은 8글자 → 길이헤더 000c (12)
	assert.Equal(t, "000cpackfile", buf.String())
}

// TestPktWriter_Methods 는 PktWriter 의 WriteLine/Flush/Err 를 직접 검증한다.
func TestPktWriter_Methods(t *testing.T) {
	t.Run("WriteLine without LF", func(t *testing.T) {
		var buf bytes.Buffer
		pw := &PktWriter{W: &buf, WithLF: false}
		require.NoError(t, pw.WriteLine("hi"))
		assert.Equal(t, "0006hi", buf.String())
	})

	t.Run("WriteLine with LF", func(t *testing.T) {
		var buf bytes.Buffer
		pw := &PktWriter{W: &buf, WithLF: true}
		require.NoError(t, pw.WriteLine("hi"))
		// "hi\n" → 3 + 4 = 7 → 0007
		assert.Equal(t, "0007hi\n", buf.String())
	})

	t.Run("Flush writes 0000", func(t *testing.T) {
		var buf bytes.Buffer
		pw := &PktWriter{W: &buf}
		require.NoError(t, pw.Flush())
		assert.Equal(t, "0000", buf.String())
	})

	t.Run("Err writes ERR line", func(t *testing.T) {
		var buf bytes.Buffer
		pw := &PktWriter{W: &buf, WithLF: false}
		require.NoError(t, pw.Err("nope"))
		assert.Contains(t, buf.String(), "ERR nope")
	})
}

// TestWriteV2Advert 는 v2 광고 출력 포맷을 검증한다.
func TestWriteV2Advert(t *testing.T) {
	t.Run("with agent and caps", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, WriteV2Advert(&buf, "git/2.40.0", "ls-refs", "fetch"))
		out := buf.String()
		assert.Contains(t, out, "version 2")
		assert.Contains(t, out, "agent=git/2.40.0")
		assert.Contains(t, out, "ls-refs")
		assert.Contains(t, out, "fetch")
		assert.True(t, bytes.HasSuffix(buf.Bytes(), []byte("0000")))
	})

	t.Run("empty agent omits agent line", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, WriteV2Advert(&buf, "", "fetch"))
		assert.NotContains(t, buf.String(), "agent=")
		assert.Contains(t, buf.String(), "version 2")
	})
}

// TestParseLsRefsSection 는 v2 ls-refs 섹션 파싱을 다룬다.
func TestParseLsRefsSection(t *testing.T) {
	cmds := []string{
		"command=ls-refs",
		"agent=git/2.40.0",
		"symrefs",
		"peel",
		"ref-prefix refs/heads/",
		"ref-prefix refs/tags/",
		"unborn",
		"",                  // 빈 줄 무시
		"unknown-extension", // 알 수 없는 옵션 무시
	}
	r, err := ParseLsRefsSection(cmds)
	require.NoError(t, err)
	assert.Equal(t, "git/2.40.0", r.Agent)
	assert.True(t, r.Symrefs)
	assert.True(t, r.Peel)
	assert.True(t, r.Unborn)
	assert.Equal(t, []string{"refs/heads/", "refs/tags/"}, r.RefPrefix)

	t.Run("empty ref-prefix is skipped", func(t *testing.T) {
		r, err := ParseLsRefsSection([]string{"command=ls-refs", "ref-prefix "})
		require.NoError(t, err)
		assert.Empty(t, r.RefPrefix)
	})
}

// TestParseFetchSection 는 v2 fetch 섹션 파싱을 다룬다.
func TestParseFetchSection(t *testing.T) {
	cmds := []string{
		"command=fetch",
		"agent=git/2.40.0",
		"want aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"want bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"have cccccccccccccccccccccccccccccccccccccccc",
		"done",
		"thin-pack",
		"ofs-delta",
		"include-tag",
		"no-progress",
		"shallow dddddddddddddddddddddddddddddddddddddddd",
		"deepen 5",
		"deepen-since 1700000000",
		"deepen-not refs/heads/old",
		"filter blob:none",
		"sideband-all",
		"packfile-uris https",
		"unknown-option",
	}
	r, err := ParseFetchSection(cmds)
	require.NoError(t, err)
	assert.Equal(t, "git/2.40.0", r.Agent)
	assert.Len(t, r.Wants, 2)
	assert.Len(t, r.Haves, 1)
	assert.True(t, r.Done)
	assert.True(t, r.ThinPack)
	assert.True(t, r.OfsDelta)
	assert.True(t, r.IncludeTag)
	assert.True(t, r.NoProgress)
	assert.Equal(t, []string{"dddddddddddddddddddddddddddddddddddddddd"}, r.Shallow)
	assert.Equal(t, 5, r.Deepen)
	assert.Equal(t, int64(1700000000), r.DeepenSince)
	assert.Equal(t, []string{"refs/heads/old"}, r.DeepenNot)
	assert.Equal(t, "blob:none", r.Filter)
	assert.True(t, r.SidebandAll)
	assert.True(t, r.PackfileURIs)
}

// TestValidateFetchSupport 는 지원되지 않는 fetch 옵션 검증을 다룬다.
func TestValidateFetchSupport(t *testing.T) {
	t.Run("supported options pass", func(t *testing.T) {
		r := &FetchReq{ThinPack: true, OfsDelta: true, Deepen: 3, Filter: "blob:none"}
		require.NoError(t, ValidateFetchSupport(r))
	})

	t.Run("sideband-all unsupported", func(t *testing.T) {
		err := ValidateFetchSupport(&FetchReq{SidebandAll: true})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sideband-all")
	})

	t.Run("packfile-uris unsupported", func(t *testing.T) {
		err := ValidateFetchSupport(&FetchReq{PackfileURIs: true})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "packfile-uris")
	})
}

// TestGetShallowBoundary_NoDeepen 는 deepen 이 없을 때 git 호출 없이
// nil 을 반환하는지 검증한다 (순수 경로).
func TestGetShallowBoundary_NoDeepen(t *testing.T) {
	t.Run("nil request", func(t *testing.T) {
		got, err := GetShallowBoundary("/nonexistent", nil)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("deepen zero", func(t *testing.T) {
		got, err := GetShallowBoundary("/nonexistent", &FetchReq{Deepen: 0})
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("negative deepen", func(t *testing.T) {
		got, err := GetShallowBoundary("/nonexistent", &FetchReq{Deepen: -1})
		require.NoError(t, err)
		assert.Nil(t, got)
	})
}
