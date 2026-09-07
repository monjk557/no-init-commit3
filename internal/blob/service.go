package blob

import (
	"bufio"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"

	"github.com/collabops/git-executor/internal/contentkind"
	"github.com/go-enry/go-enry/v2"
)

// MaxInlinePreviewBytes 는 blob 을 메모리에 올려 inline 미리보기(텍스트 렌더,
// binary 판정, line count)를 시도하는 상한이다.
//
// 이 상한을 넘는 blob 은 "미리보기 대상이 아님" 으로 즉시 판정한다 — 내용을
// 읽지 않으므로 요청당 메모리가 blob 크기와 무관하게 상수로 유지된다. raw
// 다운로드(/blob)는 스트리밍이라 이 상한의 영향을 받지 않는다.
//
// 5MB: 사람이 실제로 읽는 소스/문서의 상한선 (GitHub 의 inline 렌더 한도는
// 이보다도 낮다). 올리려면 동시 요청 수 × 상한 만큼 힙이 늘어남을 감안할 것.
const MaxInlinePreviewBytes int64 = 5 * 1024 * 1024

// BlobService는 blob 관련 비즈니스 로직을 처리하는 서비스입니다.
type BlobService struct{}

// NewBlobService는 새로운 BlobService를 생성합니다.
func NewBlobService() *BlobService {
	return &BlobService{}
}

// StatBlob 은 내용을 읽지 않고 blob 메타데이터(type/size/oid)를 조회한다.
func (s *BlobService) StatBlob(ctx context.Context, repoPath, path, ref string) (BlobStat, error) {
	stat, err := NativeStatBlob(ctx, repoPath, ref, path)
	if err != nil {
		return BlobStat{}, err
	}
	if stat.Type == "tree" {
		return BlobStat{}, ErrBlobIsDirectory
	}
	return stat, nil
}

// OpenBlob 은 blob 내용 스트림을 연다. 호출자가 Close 책임을 진다.
func (s *BlobService) OpenBlob(ctx context.Context, repoPath, oid string) (io.ReadCloser, error) {
	return NativeOpenBlob(ctx, repoPath, oid)
}

// BlobPreview 는 미리보기 판정 결과다. Content 는 inline 렌더 대상일 때만
// 채워진다.
type BlobPreview struct {
	ContentType string
	IsBinary    bool
	Content     []byte
}

// PreviewBlob 은 미리보기 정책을 한 곳에서 적용한다.
//
// 판정(content-type / binary)은 앞 contentkind.SniffBytes 만으로 끝나므로, 바이너리로
// 판명되면 **나머지 바이트는 아예 읽지 않는다** — 4MB 바이너리를 "미리보기
// 안 할 파일" 이라고 결론내리기 위해 4MB 를 메모리에 올릴 이유가 없다.
// 메모리에 올리는 건 inline 렌더할 텍스트 뿐이고, 그것도 상한까지만이다.
func PreviewBlob(ctx context.Context, repoPath string, stat BlobStat, path string) (BlobPreview, error) {
	stream, err := NativeOpenBlob(ctx, repoPath, stat.OID)
	if err != nil {
		return BlobPreview{}, err
	}
	// 조기 반환 시 파이프가 닫히며 git 이 SIGPIPE 로 끝난다 — 남은 바이트를
	// 흘려보내지 않는다.
	defer func() { _ = stream.Close() }()

	buffered := bufio.NewReaderSize(stream, contentkind.SniffBytes)
	head, err := buffered.Peek(contentkind.SniffBytes)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return BlobPreview{}, err
	}

	preview := BlobPreview{ContentType: DetectContentType(head, path)}
	// 상한 초과는 내용이 온전치 않으므로 inline 렌더 대상이 아니다 — client
	// 계약상 IsBinary 가 곧 "inline 렌더 안 함" 이다.
	preview.IsBinary = stat.Size > MaxInlinePreviewBytes || contentkind.IsBinary(head)
	if preview.IsBinary {
		return preview, nil
	}

	content, err := io.ReadAll(io.LimitReader(buffered, MaxInlinePreviewBytes))
	if err != nil {
		return BlobPreview{}, err
	}
	preview.Content = content
	return preview, nil
}

// DetectContentType는 파일 내용과 이름을 기반으로 Content-Type을 결정합니다.
//
// 우선순위:
//  1. OS mime.TypeByExtension — IANA 등록된 표준 미디어 타입 (text/markdown,
//     application/json 등). 기존 클라이언트가 의존하는 값을 보존.
//  2. enry (GitHub Linguist 룰 셋) — mime DB 에 없는 modern 언어 확장자
//     (.tsx, .jsx, .vue, .svelte, .toml, .astro 등) 의 mime_type 매핑.
//     600+ 언어 커버 → 새 확장자 추가는 enry 버전 업 한 번으로 끝남
//     (Issue-3144 의 hand-rolled switch 제거).
//  3. http.DetectContentType — 콘텐츠 sniffing 최종 fallback.
//
// enry 를 2순위로 둔 이유: enry 가 markdown 에 대해 `text/x-gfm` (GitHub
// Flavored Markdown) 같은 vendor-specific 값을 반환하는데, 기존 API 클라이언트는
// `text/markdown` (IANA 표준) 을 기대한다. mime DB 를 먼저 보면 표준 값을
// 그대로 유지하고 enry 는 mime DB 가 모르는 빈틈만 채운다.
func DetectContentType(content []byte, filename string) string {
	// 1. OS mime DB (IANA 표준).
	ext := filepath.Ext(filename)
	if contentType := mime.TypeByExtension(ext); contentType != "" {
		return contentType
	}

	// 2. enry — linguist languages.yml 기반 mime 매핑.
	//
	// content 를 함께 넘기면 ambiguous extension (.h, .m 등) 에서 베이지안
	// 분류기가 개입한다. 빈 콘텐츠일 때도 안전 (extension only fallback).
	if lang := enry.GetLanguage(filename, content); lang != "" {
		if mt := enry.GetMIMEType(filename, lang); mt != "" {
			return normalizeMIMEType(mt)
		}
	}

	// 3. 콘텐츠 sniffing fallback.
	return http.DetectContentType(content)
}

// enryMIMENormalize 는 enry/linguist 의 vendor-specific MIME 값을 IANA
// 등록된 표준 미디어 타입으로 정규화한다.
//
// 예) `text/x-gfm` (GitHub-Flavored Markdown) → `text/markdown`
// (IANA RFC 7763). 기존 API 클라이언트가 표준 값에 의존하므로 vendor
// variant 노출을 피한다.
//
// 이 맵은 의도적으로 매우 짧게 유지한다 — 새 항목 추가 전에 "정말로 IANA
// 표준이 따로 있는가" 를 먼저 확인할 것 (그렇지 않으면 enry 값을 그대로
// 쓰는 것이 맞다).
var enryMIMENormalize = map[string]string{
	"text/x-gfm": "text/markdown",
}

func normalizeMIMEType(mt string) string {
	if v, ok := enryMIMENormalize[mt]; ok {
		return v
	}
	return mt
}
