package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// catalogguard_test.go — internal/ 아래 gin.H{"success": ...} 형태의 raw
// error envelope 리터럴을 allowlist 밖에서 새로 만들면 실패시키는 정적
// 가드. errcatalog_test.go 의 TestCatalogHasNo422 와 같은 층위 — golangci
// 커스텀 룰이 아니라 평범한 Go AST 스캔 테스트.
//
// 목적: 카탈로그(api.NewCoded/NewCodedWithParams)를 거치지 않고 손으로 조립한
// 에러 응답이 새로 생기는 걸 리뷰어 기억력이 아니라 CI 가 잡도록 한다
// (docs/architecture — "카탈로그 가드" 섹션 참조).
//
// 잡지 않는 것: bind 400(writeError 직접 호출, wrapper.go 안이라 allowlist)과
// gin.Recovery() 의 panic 500(애초에 gin.H 리터럴이 아니라 plain text)은
// 이 스캔의 대상이 아니다.

// catalogGuardAllowlist 는 gin.H{"success": ...} 리터럴 구성이 허용된 파일
// (repo-root 기준 상대경로) — ratchet baseline. 두 종류로 나뉜다:
//
//  1. 아키텍처상 정당한 예외 — (a) wrapper 자기 자신, (b) conflicts/
//     resolution_options 등 다중 필드 응답이 필요해 typed.HTTPError 로
//     표현이 안 되는 responseErr(merge)/responseOverride(diffs) — handler.go
//     (리터럴 생성)와 routes.go(*HTTPError/500 fallback) 양쪽에 남는다,
//     (c) typed.RegisterRaw 로 등록된 binary/text streaming endpoint(blob
//     의 /blob) — JSON wrapper 를 애초에 안 거치므로 handler 가 wrapper.go
//     의 writeErrorWithCode 와 동일한 shape 을 직접 조립해야 한다. 값은
//     카탈로그(api.NewCoded)에서 오지만 envelope 조립 자체는 raw — 영구 허용.
//  2. 아직 typed/카탈로그로 마이그레이션되지 않은 legacy 코드 —
//     indexing/webhook_handler(관리자용 webhook, "message" 필드를 쓰는
//     별개 wire contract라 REST 도메인 에러 카탈로그 스코프 밖). "영구
//     허용"이 아니라 "아직 손 안 댐" — 새 항목 추가는 반드시 리뷰에서
//     사유를 남기고, 기존 항목은 각자 마이그레이션되면 이 목록에서 지운다
//     (CTL 의 docs/error-catalog-baseline.yaml 과 동일한 ratchet 원칙:
//     축소만 허용, 신규는 거부).
var catalogGuardAllowlist = map[string]bool{
	// 아키텍처상 정당한 예외.
	"internal/api/typed/wrapper.go": true, // writeError/writeErrorWithCode 자신
	"internal/merge/routes.go":      true, // *HTTPError/500 fallback
	"internal/merge/handler.go":     true, // responseErr 리터럴 생성부
	"internal/diffs/routes.go":      true, // *HTTPError/500 fallback
	"internal/diffs/handler.go":     true, // responseOverride 리터럴 생성부 + HandlePatch(RegisterRaw)
	"internal/blob/handler.go":      true, // RegisterRaw(/blob) — JSON wrapper 우회, writeCatalogError 가 값은 카탈로그에서 가져옴

	// 미마이그레이션 legacy — baseline, 축소 대상.
	"internal/indexing/webhook_handler.go": true, // 관리자 webhook, REST 도메인 스코프 밖 ("message" 필드 별개 wire contract)
}

// TestNoRawErrorEnvelopeOutsideCatalog 는 internal/ 아래 (allowlist 제외)
// .go 파일을 AST 로 훑어 gin.H{"success": ...} composite literal 이 있으면
// 실패시킨다.
func TestNoRawErrorEnvelopeOutsideCatalog(t *testing.T) {
	root := repoRootForCatalogGuard(t)
	internalDir := filepath.Join(root, "internal")

	fset := token.NewFileSet()

	err := filepath.Walk(internalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if catalogGuardAllowlist[rel] {
			return nil
		}

		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			// 파싱 불가한 파일은 이 가드의 관심사가 아니다 — 다른 곳에서 실패한다.
			return nil //nolint:nilerr
		}

		ast.Inspect(f, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if !isGinHType(cl.Type) {
				return true
			}
			if hasSuccessKey(cl) {
				pos := fset.Position(cl.Pos())
				t.Errorf("%s:%d: raw gin.H{\"success\": ...} outside the catalog — use api.NewCoded/NewCodedWithParams instead (or add to catalogGuardAllowlist if a rich multi-field response genuinely needs it)", rel, pos.Line)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", internalDir, err)
	}
}

// isGinHType 은 expr 가 `gin.H` 타입 참조인지 판별한다.
func isGinHType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == "gin" && sel.Sel.Name == "H"
}

// hasSuccessKey 는 composite literal 이 "success" 키를 갖는지 확인한다.
func hasSuccessKey(cl *ast.CompositeLit) bool {
	for _, elt := range cl.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		lit, ok := kv.Key.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		if lit.Value == `"success"` {
			return true
		}
	}
	return false
}

// repoRootForCatalogGuard 는 이 테스트 파일 자신의 위치(runtime.Caller)에서
// go.mod 를 찾아 올라가며 repo root 를 결정한다 — `go test` 의 CWD(패키지
// 디렉터리)에 의존하지 않는다.
func repoRootForCatalogGuard(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found while walking up from " + thisFile)
		}
		dir = parent
	}
}
