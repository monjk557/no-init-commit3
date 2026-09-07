package diffs

// 순수 함수 단위 테스트 — git 저장소나 gin context 없이 입력/출력만 검증한다.
// myers_diff_adapter.go / gogit_compat.go / service.go / schema.go / native_git.go
// 의 순수 로직 커버리지를 채운다.

import (
	"net/http/httptest"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ----------------------------------------------------------------------------
// myers_diff_adapter.go
// ----------------------------------------------------------------------------

func TestMyersDiffAdapter_OptimizedLineDiff_AddDelContext(t *testing.T) {
	t.Parallel()
	adapter := NewMyersDiffAdapter()
	require.NotNil(t, adapter)

	base := []string{"a", "b", "c"}
	head := []string{"a", "x", "c"}

	changes, adds, dels := adapter.OptimizedLineDiff(base, head, 0, "unified")

	// b 삭제 + x 추가 → adds=1, dels=1.
	assert.Equal(t, 1, adds)
	assert.Equal(t, 1, dels)

	// 변경 종류별 카운트 검증.
	var ctxN, addN, delN int
	for _, ch := range changes {
		switch ch.Type {
		case LineChangeTypeContext:
			ctxN++
		case LineChangeTypeAdd:
			addN++
		case LineChangeTypeDel:
			delN++
		}
	}
	assert.Equal(t, 1, addN)
	assert.Equal(t, 1, delN)
	assert.GreaterOrEqual(t, ctxN, 2) // a, c context.
}

func TestMyersDiffAdapter_OptimizedLineDiff_NoChange(t *testing.T) {
	t.Parallel()
	adapter := NewMyersDiffAdapter()
	base := []string{"same", "lines"}
	head := []string{"same", "lines"}

	changes, adds, dels := adapter.OptimizedLineDiff(base, head, 0, "unified")
	assert.Equal(t, 0, adds)
	assert.Equal(t, 0, dels)
	for _, ch := range changes {
		assert.Equal(t, LineChangeTypeContext, ch.Type)
	}
}

func TestMyersDiffAdapter_OptimizedLineDiff_ContextTrimming(t *testing.T) {
	t.Parallel()
	adapter := NewMyersDiffAdapter()

	// 변경 라인 1개를 다수 context 가 둘러싼 입력 — contextLines=1 로 trimming.
	base := []string{"c1", "c2", "c3", "old", "c4", "c5", "c6"}
	head := []string{"c1", "c2", "c3", "new", "c4", "c5", "c6"}

	full, _, _ := adapter.OptimizedLineDiff(base, head, 0, "unified")
	trimmed, _, _ := adapter.OptimizedLineDiff(base, head, 1, "unified")

	// trimming 후가 전체보다 짧아야 한다 (먼 context 가 제거됨).
	assert.Less(t, len(trimmed), len(full))
	assert.NotEmpty(t, trimmed)
}

func TestOptimizedTrimContext_EmptyOrZeroContext(t *testing.T) {
	t.Parallel()
	adapter := NewMyersDiffAdapter()

	// 빈 입력은 그대로 반환.
	got := adapter.optimizedTrimContext(nil, 3)
	assert.Empty(t, got)

	// contextLines<=0 은 입력 그대로.
	in := []LineChange{{Type: LineChangeTypeContext, Text: "x"}}
	got = adapter.optimizedTrimContext(in, 0)
	assert.Equal(t, in, got)
}

// ----------------------------------------------------------------------------
// gogit_compat.go — GetFileName
// ----------------------------------------------------------------------------

func TestGetFileName_PrefersToName(t *testing.T) {
	t.Parallel()
	ch := &object.Change{}
	ch.To.Name = "new/path.go"
	ch.From.Name = "old/path.go"
	assert.Equal(t, "new/path.go", GetFileName(ch))
}

func TestGetFileName_FallsBackToFromName(t *testing.T) {
	t.Parallel()
	ch := &object.Change{}
	ch.From.Name = "deleted/path.go"
	assert.Equal(t, "deleted/path.go", GetFileName(ch))
}

// ----------------------------------------------------------------------------
// schema.go — DiffMode.IsValid, DiffTooLargeError.Error
// ----------------------------------------------------------------------------

func TestDiffMode_IsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, DiffModeMergeBase.IsValid())
	assert.True(t, DiffModeDirect.IsValid())
	assert.False(t, DiffMode("bogus").IsValid())
	assert.False(t, DiffMode("").IsValid())
}

func TestDiffTooLargeError_Error(t *testing.T) {
	t.Parallel()
	err := &DiffTooLargeError{Reason: "too many lines"}
	assert.Contains(t, err.Error(), "too many lines")
	assert.Contains(t, err.Error(), "too large")
}

func TestResponseOverride_Error(t *testing.T) {
	t.Parallel()
	ro := &responseOverride{status: 413, body: nil}
	assert.Equal(t, "response override", ro.Error())
}

// ----------------------------------------------------------------------------
// service.go — extractPaginationParams / paginateFiles / IsBinaryFile
// ----------------------------------------------------------------------------

func TestExtractPaginationParams_Defaults(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest("GET", "/x", nil)
	p := extractPaginationParams(r)
	assert.Equal(t, 1, p.Page)
	assert.Equal(t, 50, p.PageSize)
}

func TestExtractPaginationParams_CustomValues(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest("GET", "/x?page=3&page_size=20", nil)
	p := extractPaginationParams(r)
	assert.Equal(t, 3, p.Page)
	assert.Equal(t, 20, p.PageSize)
}

func TestExtractPaginationParams_InvalidAndOutOfRangeIgnored(t *testing.T) {
	t.Parallel()
	// page=0, page_size=999 (>200) 은 무시되고 default 유지.
	r := httptest.NewRequest("GET", "/x?page=0&page_size=999", nil)
	p := extractPaginationParams(r)
	assert.Equal(t, 1, p.Page)
	assert.Equal(t, 50, p.PageSize)

	// 비숫자 값도 무시.
	r2 := httptest.NewRequest("GET", "/x?page=abc&page_size=xyz", nil)
	p2 := extractPaginationParams(r2)
	assert.Equal(t, 1, p2.Page)
	assert.Equal(t, 50, p2.PageSize)
}

func TestPaginateFiles_FirstPageHasMore(t *testing.T) {
	t.Parallel()
	files := make([]FileDiff, 5)
	for i := range files {
		files[i] = FileDiff{Path: "f"}
	}
	out, p := paginateFiles(files, PaginationParams{Page: 1, PageSize: 2})
	assert.Len(t, out, 2)
	assert.Equal(t, 5, p.Total)
	assert.True(t, p.HasMore)
}

func TestPaginateFiles_LastPageNoMore(t *testing.T) {
	t.Parallel()
	files := make([]FileDiff, 5)
	out, p := paginateFiles(files, PaginationParams{Page: 3, PageSize: 2})
	assert.Len(t, out, 1) // 마지막 1개.
	assert.False(t, p.HasMore)
}

func TestPaginateFiles_PageBeyondTotalReturnsEmpty(t *testing.T) {
	t.Parallel()
	files := make([]FileDiff, 3)
	out, p := paginateFiles(files, PaginationParams{Page: 10, PageSize: 5})
	assert.Empty(t, out)
	assert.False(t, p.HasMore)
	assert.Equal(t, 3, p.Total)
}

// ----------------------------------------------------------------------------
// native_git.go — parseFileStatus / zeroSHAToEmpty
// ----------------------------------------------------------------------------

func TestParseFileStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		line string
		want FileDiffStatus
	}{
		{"A\tnew.go", FileDiffStatusAdded},
		{"D\tgone.go", FileDiffStatusDeleted},
		{"R100\told.go\tnew.go", FileDiffStatusRenamed},
		{"M\tmod.go", FileDiffStatusModified},
		{"", FileDiffStatusModified},           // 빈 입력 → default.
		{"X\tunknown", FileDiffStatusModified}, // 알 수 없는 코드 → default.
	}
	for _, tt := range tests {
		assert.Equalf(t, tt.want, parseFileStatus(tt.line), "line=%q", tt.line)
	}
}

func TestZeroSHAToEmpty(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", zeroSHAToEmpty(""))
	assert.Equal(t, "", zeroSHAToEmpty("0000000000000000000000000000000000000000"))
	assert.Equal(t, "abc123", zeroSHAToEmpty("abc123"))
}

// isBinaryFileByExtension 은 contentkind.IsBinaryPath 위임 wrapper — 판정
// 규칙 자체의 검증은 internal/contentkind 테스트에 있다. 여기서는 diff 경로가
// 그 규칙을 실제로 태우는지만 확인한다.
func TestIsBinaryFileByExtension(t *testing.T) {
	t.Parallel()
	assert.True(t, isBinaryFileByExtension("image.PNG"))
	assert.True(t, isBinaryFileByExtension("archive.zip"))
	assert.False(t, isBinaryFileByExtension("source.go"))
	assert.False(t, isBinaryFileByExtension("README")) // 확장자 없음.
}
