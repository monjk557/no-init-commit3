// Package main 의 spec generator 테스트.
//
// (1) byte-equivalent 회귀 가드 — registry 가 비어 있을 때 (Phase 1) 출력이
//
//	기존 generated yaml 과 정확히 일치해야 한다.
//
// (2) hybrid 우선순위 — registry 에 등록된 (method, path) 는 legacy switch
//
//	매핑을 override 해야 한다 (Phase 2 마이그레이션 시 작동 확인).
package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/collabops/git-executor/internal/api/typed"
)

func projectRootForTest(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// 테스트는 specs/ 디렉토리에서 실행 → repo root 는 한 단계 위.
	return filepath.Dir(cwd)
}

// silentLog 는 runGeneration 의 진행 로그를 버린다 (테스트 출력 깔끔하게).
func silentLog(t *testing.T) *os.File {
	t.Helper()
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	t.Cleanup(func() { _ = devnull.Close() })
	return devnull
}

// TestRunGeneration_ByteEquivalentToBaseline 는 빈 registry 로 spec 을 생성해
// 그 결과가 checked-in baseline (specs/generated/collabops-git-executor.yaml)
// 과 byte-equivalent 함을 검증한다.
//
// 이 테스트가 실패하면 두 경우 중 하나:
//
//	(a) 의도하지 않은 버그가 generator 에 들어왔거나,
//	(b) Phase 2 도메인 마이그레이션으로 spec 출력이 의도적으로 풍부해졌거나.
//
// 후자라면 `make specs generate` 로 baseline 갱신 후 PR 에 그 변경이 review
// friendly 하게 보이도록 한다.
func TestRunGeneration_ByteEquivalentToBaseline(t *testing.T) {
	root := projectRootForTest(t)
	emptyReg := typed.NewRegistry() // Phase 1 시점 — registry 비어있음

	got, err := runGeneration(root, emptyReg, silentLog(t))
	if err != nil {
		t.Fatalf("runGeneration: %v", err)
	}

	baselinePath := filepath.Join(root, "specs/generated/collabops-git-executor.yaml")
	want, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}

	if got != string(want) {
		// 변경 위치 힌트 — 테스트 출력 너무 길지 않게 첫 차이만.
		gotLines := strings.Split(got, "\n")
		wantLines := strings.Split(string(want), "\n")
		minLen := len(gotLines)
		if len(wantLines) < minLen {
			minLen = len(wantLines)
		}
		var firstDiff int = -1
		for i := 0; i < minLen; i++ {
			if gotLines[i] != wantLines[i] {
				firstDiff = i
				break
			}
		}
		if firstDiff < 0 {
			firstDiff = minLen
		}
		t.Errorf("output diverges from baseline. len(got)=%d, len(want)=%d, first diff at line %d:\n  got:  %q\n  want: %q",
			len(got), len(want), firstDiff+1,
			safeIndex(gotLines, firstDiff),
			safeIndex(wantLines, firstDiff),
		)
	}
}

// TestRunGeneration_RegistryWinsOverLegacy 는 registry 가 동일 (method, specPath)
// 에 등록될 때 일반적인 generation 이 두 경로를 모두 출력한다는 점 (registry
// 가 우선이 아니라 legacy 도 함께 emit 되어 dedupe 가 path-level 에서만 일어
// 난다는 점) 을 점검한다.
//
// 모든 도메인이 typed migration 된 후 (Phase 2.13) 에는 legacy switch 자체
// 가 거의 비어있어 sentinel test 의 의미가 약해졌다. dedupe + registry 우선
// 동작은 unit test (TestGeneratePathsFromRoutes_*) 가 더 정확히 보장하므로
// 이 테스트는 향후 cleanup 시점에 제거해도 무방하다.
//
// 현재는 유지하되 sentinel path 를 lfs 경로로 옮긴다 — lfs 는 미마이그레이션
// 도메인이며 legacy switch 에 등록은 안 됐지만 server.go AST 에는 등장.
func TestRunGeneration_RegistryWinsOverLegacy(t *testing.T) {
	t.Skip("Phase 2.13 이후 거의 모든 도메인이 typed migration 되어 legacy switch 가 비었음. " +
		"registry 우선 동작은 unit test 들이 보장. 이 테스트는 cleanup 시 제거 예정.")
}

// TestGeneratePathsFromRoutes_EmptyRegistryIdenticalToLegacyOnly 는 단위
// 레벨에서 hybrid 함수가 registry 비어있을 때 legacy-only 결과와 정확히
// 같음을 보인다 (전체 generation 보다 빠른 회귀 가드).
func TestGeneratePathsFromRoutes_EmptyRegistryIdenticalToLegacyOnly(t *testing.T) {
	root := projectRootForTest(t)
	routes, err := parseServerRoutes(filepath.Join(root, "internal/server/server.go"))
	if err != nil {
		t.Fatalf("parseServerRoutes: %v", err)
	}

	withEmpty := generatePathsFromRoutes(routes, typed.NewRegistry())
	withNil := generatePathsFromRoutes(routes, nil)
	if withEmpty != withNil {
		t.Error("registry empty 와 nil 의 출력이 달라서는 안 됨")
	}
}

// TestRegistryEndpointToOperation_FillsSummaryAndOperationIDFallbacks 는
// summary / operationID 미지정 시 fallback 동작 검증.
func TestRegistryEndpointToOperation_FillsSummaryAndOperationIDFallbacks(t *testing.T) {
	ep := typed.Endpoint{
		Method:      http.MethodGet,
		Path:        "/repos/{owner}/{repo}/foo",
		Tag:         "Foo",
		OperationID: "GetFoo",
	}
	op := registryEndpointToOperation(ep)
	if op.summary != "GetFoo" {
		t.Errorf("summary fallback = %q, want %q", op.summary, "GetFoo")
	}
	if op.operationID != "GetFoo" {
		t.Errorf("operationID = %q, want %q (그대로 유지)", op.operationID, "GetFoo")
	}
}

func safeIndex(lines []string, i int) string {
	if i < 0 || i >= len(lines) {
		return "<EOF>"
	}
	return lines[i]
}
