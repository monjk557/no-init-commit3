package gitcmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunPackObjects_RoundTripIndex(t *testing.T) {
	// 1) 실제 내용을 가진 source repo 생성.
	src, head, _, _ := setupContentRepo(t)

	// 2) head 까지의 모든 객체를 pack-objects 로 packfile 스트림으로 만든다.
	var progressLines []string
	opts := &PackObjectsOpts{
		Wants:           []string{head},
		OfsDelta:        true,
		DeltaBaseOffset: true,
		OnProgress: func(line string) {
			progressLines = append(progressLines, line)
		},
	}
	var pack bytes.Buffer
	count, err := RunPackObjects(context.Background(), src, opts, &pack)
	require.NoError(t, err)
	// count 는 stderr "Total N" 라인 파싱 결과 — git 은 stderr 가 TTY 가 아니면
	// progress 를 억제하므로 비대화 테스트 환경에서는 0 일 수 있다. packfile 본문
	// 유효성으로 round-trip 을 검증한다.
	assert.GreaterOrEqual(t, count, 0)
	_ = progressLines
	require.NotEmpty(t, pack.Bytes())
	assert.Equal(t, "PACK", string(pack.Bytes()[:4]), "output should be a valid packfile")

	// 3) 빈 bare repo 를 만들고 그 packfile 을 index-pack 으로 색인한다.
	dst := t.TempDir()
	gitInRepo(t, dst, "init", "-q", "--bare")

	res, err := IndexPackFromReader(context.Background(), dst, bytes.NewReader(pack.Bytes()))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.NotEmpty(t, res.PackHash, "index-pack should return a non-empty pack hash")

	// 4) 색인 후 head 커밋 객체가 dst repo 에 존재해야 한다.
	typ, err := ObjectExists(context.Background(), dst, head)
	require.NoError(t, err)
	assert.Equal(t, "commit", typ)
}

func TestRunPackObjects_WithHaves(t *testing.T) {
	src, head, parent, _ := setupContentRepo(t)

	// parent 를 have 로 주면 incremental pack — 여전히 유효한 packfile 이어야 한다.
	opts := &PackObjectsOpts{
		Wants: []string{head},
		Haves: []string{parent},
	}
	var pack bytes.Buffer
	count, err := RunPackObjects(context.Background(), src, opts, &pack)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, count, 0)
	require.NotEmpty(t, pack.Bytes())
	assert.Equal(t, "PACK", string(pack.Bytes()[:4]))
}

func TestRunPackObjects_BadWant(t *testing.T) {
	src, _, _, _ := setupContentRepo(t)

	opts := &PackObjectsOpts{
		Wants: []string{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
	}
	var pack bytes.Buffer
	_, err := RunPackObjects(context.Background(), src, opts, &pack)
	require.Error(t, err, "존재하지 않는 want 는 pack-objects 실패를 일으켜야 한다")
	assert.Contains(t, err.Error(), "pack-objects")
}

func TestIndexPackFromReader_EmptyInput(t *testing.T) {
	dst := t.TempDir()
	gitInRepo(t, dst, "init", "-q", "--bare")

	// CopyPackfileFromStream 은 빈 스트림에서 0 바이트 → "empty packfile" 에러.
	_, err := IndexPackFromReader(context.Background(), dst, bytes.NewReader(nil))
	require.Error(t, err)
}

func TestIndexPackFromReader_GarbageInput(t *testing.T) {
	dst := t.TempDir()
	gitInRepo(t, dst, "init", "-q", "--bare")

	// 유효한 packfile trailer 가 없는 쓰레기 입력 → 저장 또는 색인 단계에서 실패.
	garbage := bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, 64)
	_, err := IndexPackFromReader(context.Background(), dst, bytes.NewReader(garbage))
	require.Error(t, err)
}

func TestIndexPackFromReader_CreatesPackDir(t *testing.T) {
	src, head, _, _ := setupContentRepo(t)
	var pack bytes.Buffer
	_, err := RunPackObjects(context.Background(), src, &PackObjectsOpts{Wants: []string{head}}, &pack)
	require.NoError(t, err)

	// objects/pack 디렉터리가 없는 상태에서도 index-pack 이 만들어 써야 한다.
	dst := t.TempDir()
	gitInRepo(t, dst, "init", "-q", "--bare")
	require.NoError(t, os.RemoveAll(filepath.Join(dst, "objects", "pack")))

	res, err := IndexPackFromReader(context.Background(), dst, bytes.NewReader(pack.Bytes()))
	require.NoError(t, err)
	assert.NotEmpty(t, res.PackHash)

	// pack/idx 파일이 실제로 기록되었는지 확인.
	entries, err := os.ReadDir(filepath.Join(dst, "objects", "pack"))
	require.NoError(t, err)
	var hasIdx, hasPack bool
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".idx") {
			hasIdx = true
		}
		if strings.HasSuffix(e.Name(), ".pack") {
			hasPack = true
		}
	}
	assert.True(t, hasIdx && hasPack, "index-pack should write both .pack and .idx")
}

func TestParsePackObjectsTotal(t *testing.T) {
	tests := []struct {
		name string
		line string
		want int
	}{
		{"정상", "Total 42 (delta 5), reused 0 (delta 0), pack-reused 0", 42},
		{"Total_아님", "Counting objects: 10", 0},
		{"필드_부족", "Total", 0},
		{"숫자_아님", "Total xyz (delta 0)", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parsePackObjectsTotal(tt.line))
		})
	}
}

func TestScanCROrLF(t *testing.T) {
	// scanCROrLF 를 직접 단위 검증 — \r 과 \n 모두에서 토큰 분리.
	t.Run("LF_분리", func(t *testing.T) {
		adv, tok, err := scanCROrLF([]byte("abc\ndef"), false)
		require.NoError(t, err)
		assert.Equal(t, 4, adv)
		assert.Equal(t, "abc", string(tok))
	})
	t.Run("CR_분리", func(t *testing.T) {
		adv, tok, err := scanCROrLF([]byte("xy\rz"), false)
		require.NoError(t, err)
		assert.Equal(t, 3, adv)
		assert.Equal(t, "xy", string(tok))
	})
	t.Run("구분자_없고_미완", func(t *testing.T) {
		adv, tok, err := scanCROrLF([]byte("partial"), false)
		require.NoError(t, err)
		assert.Equal(t, 0, adv)
		assert.Nil(t, tok)
	})
	t.Run("EOF_잔여데이터", func(t *testing.T) {
		adv, tok, err := scanCROrLF([]byte("last"), true)
		require.NoError(t, err)
		assert.Equal(t, 4, adv)
		assert.Equal(t, "last", string(tok))
	})
	t.Run("EOF_빈데이터", func(t *testing.T) {
		adv, tok, err := scanCROrLF([]byte{}, true)
		require.NoError(t, err)
		assert.Equal(t, 0, adv)
		assert.Nil(t, tok)
	})
}
