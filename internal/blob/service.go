package blob

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/go-enry/go-enry/v2"
)

// MaxBlobSize는 blob의 최대 크기입니다 (50MB)
const MaxBlobSize = 50 * 1024 * 1024

// BlobService는 blob 관련 비즈니스 로직을 처리하는 서비스입니다.
type BlobService struct{}

// NewBlobService는 새로운 BlobService를 생성합니다.
func NewBlobService() *BlobService {
	return &BlobService{}
}

// GetBlobContent는 저장소에서 blob 내용을 가져옵니다.
// native git show를 사용합니다 (go-git 대체).
func (s *BlobService) GetBlobContent(ctx context.Context, repoPath, path, ref string) ([]byte, string, error) {
	content, err := NativeGetBlobContent(ctx, repoPath, ref, path)
	if err != nil {
		return nil, "", fmt.Errorf("file not found: %w", err)
	}

	// 파일 크기 체크
	if len(content) > MaxBlobSize {
		return nil, "", fmt.Errorf("file is too large (%d bytes > %d bytes)", len(content), MaxBlobSize)
	}

	// Content-Type 감지
	contentType := DetectContentType(content, path)

	return content, contentType, nil
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

// IsBinaryContent는 내용이 바이너리인지 확인합니다.
func IsBinaryContent(content []byte, contentType string) bool {
	// Content-Type 확인
	if strings.HasPrefix(contentType, "text/") ||
		strings.HasPrefix(contentType, "application/json") ||
		strings.HasPrefix(contentType, "application/xml") ||
		strings.HasPrefix(contentType, "application/javascript") {
		return false
	}

	// UTF-8 유효성 확인
	return !utf8.Valid(content)
}
