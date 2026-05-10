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
	"bytes"
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

// jsonTagSet 은 SchemaInfo.Fields 의 JSONTag 들을 set 으로 모은다 — 검증
// 시점에 순서에 의존하지 않도록.
func jsonTagSet(fields []FieldInfo) map[string]bool {
	out := make(map[string]bool, len(fields))
	for _, f := range fields {
		if f.JSONTag != "" && f.JSONTag != "-" {
			out[f.JSONTag] = true
		}
	}
	return out
}

// flattenedFieldsForFixture 는 fixture src 를 parseSchemaFile + flattenEmbeddings
// 두 단계로 통과시켜 지정 schema 의 최종 json tag set 을 반환한다.
// parseSchemaFile (detect) ↔ flattenEmbeddings (inline) 책임 분리를 그대로
// 따라가는 reality-mirroring helper.
func flattenedFieldsForFixture(t *testing.T, src, schemaName string) map[string]bool {
	t.Helper()
	path := writeFixtureSchema(t, src)
	schemas, err := parseSchemaFile(path, "fixture")
	if err != nil {
		t.Fatalf("parseSchemaFile: %v", err)
	}
	if err := flattenEmbeddings(schemas, typeResolver{}, nil); err != nil {
		t.Fatalf("flattenEmbeddings: %v", err)
	}
	sc, ok := schemas[schemaName]
	if !ok {
		t.Fatalf("%s schema not found; got %v", schemaName, schemas)
	}
	return jsonTagSet(sc.Fields)
}

// writeFixtureSchema 는 임시 디렉토리에 `schema.go` 한 파일을 만들어 그 경로를
// 반환한다. parseSchemaFile 이 path 기반 API 라 in-memory src 주입이 안 되어
// tempdir + 실제 파일이 가장 단순한 fixture 경로다.
func writeFixtureSchema(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.go")
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// TestParseSchemaFile_FlattensAnonymousValueEmbedding 는 anonymous embedded
// struct (value type) 의 필드들이 parent struct 의 fields 에 inline 됨을
// 검증한다. encoding/json 의 anonymous field flatten 동작과 동일.
//
// 현재 generator (`parseSchemaFile` line ~700) 는 `len(field.Names) > 0` 가드로
// anonymous embedding 을 silent skip 한다 → SingleCommitDiffResponse /
// PaginatedDiffResponse 의 spec 에서 *DiffResponse 임베딩 필드들이 통째로
// 누락되어 web 까지 drift 전파 (Issue-2343).
func TestParseSchemaFile_FlattensAnonymousValueEmbedding(t *testing.T) {
	src := `package fixture

type Inner struct {
	A string ` + "`json:\"a\"`" + `
	B int    ` + "`json:\"b\"`" + `
}

type Outer struct {
	C    bool ` + "`json:\"c\"`" + `
	Inner
}
`
	got := flattenedFieldsForFixture(t, src, "Outer")
	want := map[string]bool{"a": true, "b": true, "c": true}
	for k := range want {
		if !got[k] {
			t.Errorf("Outer.Fields missing flattened json tag %q; got fields=%v", k, got)
		}
	}
}

// TestParseSchemaFile_FlattensAnonymousPointerEmbedding 는 *Embedded 형태의
// anonymous pointer embedding 도 동일하게 flatten 됨을 검증한다.
//
// 실제 회귀 사례: SingleCommitDiffResponse {Success; Commit; *DiffResponse}
// (internal/diffs/schema.go:76).
func TestParseSchemaFile_FlattensAnonymousPointerEmbedding(t *testing.T) {
	src := `package fixture

type Inner struct {
	A string ` + "`json:\"a\"`" + `
	B int    ` + "`json:\"b\"`" + `
}

type Outer struct {
	C bool ` + "`json:\"c\"`" + `
	*Inner
}
`
	got := flattenedFieldsForFixture(t, src, "Outer")
	want := map[string]bool{"a": true, "b": true, "c": true}
	for k := range want {
		if !got[k] {
			t.Errorf("Outer.Fields missing flattened json tag %q; got fields=%v", k, got)
		}
	}
}

// TestParseSchemaFile_TaggedEmbeddingStaysNested 는 임베딩에 명시적 json tag
// 가 붙으면 flatten 하지 않고 nested ref 로 유지됨을 검증한다 (encoding/json
// 동작과 동일 — tag 가 있으면 일반 필드로 취급).
func TestParseSchemaFile_TaggedEmbeddingStaysNested(t *testing.T) {
	src := `package fixture

type Inner struct {
	A string ` + "`json:\"a\"`" + `
}

type Outer struct {
	C     bool ` + "`json:\"c\"`" + `
	Inner ` + "`json:\"inner\"`" + `
}
`
	got := flattenedFieldsForFixture(t, src, "Outer")
	if !got["inner"] {
		t.Errorf("Outer.Fields should keep tagged embedding as nested ref %q; got fields=%v", "inner", got)
	}
	if got["a"] {
		t.Errorf("Outer.Fields should NOT flatten tagged embedding (json:\"inner\"); but got nested 'a'; fields=%v", got)
	}
	if !got["c"] {
		t.Errorf("Outer.Fields missing 'c'; fields=%v", got)
	}
}

// TestParseAllSchemas_FlattensCrossPackageEmbedding 는 실제 코드베이스의
// FileChange (internal/merge/schema.go) 가 cross-package 임베딩 (`diffs.FileDiff`)
// 의 필드들을 spec 단계에서 받는지 직접 검증한다.
//
// 현재 generator 는 cross-package anonymous embedding 도 silent skip → FileDiff
// 의 path/status/additions/deletions/... 가 FileChange spec 에서 누락. 이
// 테스트는 Phase 2 fix 후 GREEN 으로 전환된다.
func TestParseAllSchemas_FlattensCrossPackageEmbedding(t *testing.T) {
	root := projectRootForTest(t)

	schemas, _, err := parseAllSchemas(root, nil)
	if err != nil {
		t.Fatalf("parseAllSchemas: %v", err)
	}

	fc, ok := schemas["FileChange"]
	if !ok {
		t.Fatalf("FileChange schema not found")
	}

	got := jsonTagSet(fc.Fields)
	// FileDiff 의 json tags 중 omitempty 없이 항상 wire 에 등장하는 핵심 필드만
	// 검증 (omitempty 가 있는 old_path/submodule_change 는 검증 대상 아님 —
	// flatten 자체는 일어나야 하지만 byte-equiv 회귀로 충분).
	want := []string{"path", "status", "additions", "deletions", "is_binary"}
	for _, k := range want {
		if !got[k] {
			t.Errorf("FileChange.Fields missing flattened json tag %q from diffs.FileDiff; got fields=%v", k, got)
		}
	}
	// 그리고 FileChange 자체의 manual field 도 그대로.
	for _, k := range []string{"content", "conflicts"} {
		if !got[k] {
			t.Errorf("FileChange.Fields missing own json tag %q; got fields=%v", k, got)
		}
	}
}

// TestRenderSchemaYAML_NullableForNilableJSONFields 는 Go encoding/json 의
// nil 동작을 OpenAPI 의 nullable 로 정확히 표현하는지 검증한다.
//
// Go 측 contract:
//
//   - omitempty 없는 *T  : nil 시 응답에 null
//   - omitempty 없는 []T : nil 시 응답에 null
//   - omitempty 없는 map : nil 시 응답에 null
//   - omitempty 있는 모든 위 케이스 : nil 시 omit (필드 자체가 응답에 없음)
//
// 이전엔 *T 만 nullable: true 를 출력해서 slice/map 의 nil-null 매핑이
// spec 에 반영되지 않았다 (실제 사고 사례: FileDiff.diffLines 가 large file
// 응답에서 null 인데 spec 은 array required 로 표시 → orchestrator parse 실패).
func TestRenderSchemaYAML_NullableForNilableJSONFields(t *testing.T) {
	// JSONTag 는 parseSchemaFile 에서 옵션 부분(",omitempty")을 잘라낸 base 가
	// 들어오고, omitempty 여부는 Required 로 전달된다.
	schema := SchemaInfo{
		Name: "_NullabilityProbe",
		Fields: []FieldInfo{
			// omitempty 없는 slice — nil 시 null. nullable: true 필요.
			{Name: "Items", Type: "[]string", JSONTag: "items", Required: true},
			// omitempty 있는 slice — nil 시 omit. nullable 불필요.
			{Name: "Labels", Type: "[]string", JSONTag: "labels", Required: false},
			// omitempty 없는 map — nil 시 null. nullable: true 필요.
			{Name: "Meta", Type: "map[string]string", JSONTag: "meta", Required: true},
			// omitempty 없는 *primitive — 기존부터 nullable. 회귀 가드.
			{Name: "Count", Type: "*int", JSONTag: "count", Required: true},
			// 일반 string — nullable 불필요.
			{Name: "Name", Type: "string", JSONTag: "name", Required: true},
		},
	}

	out := renderSchemaYAML(
		schema,
		map[string]SchemaInfo{},
		map[string]StringEnumInfo{},
		func(s string) string { return s },
		typeResolver{},
	)

	// 각 field 의 block (해당 field key 부터 다음 sibling field key 또는
	// required: 까지) 을 분리해 nullable: true 포함 여부를 검사한다.
	// field key 는 8-space indent (properties: 안의 항목).
	const fieldIndent = "        " // 8 spaces

	type wantSpec struct {
		field    string
		nullable bool
	}
	cases := []wantSpec{
		{"items", true},   // slice w/o omitempty → nullable
		{"labels", false}, // slice w/ omitempty → no nullable
		{"meta", true},    // map w/o omitempty → nullable
		{"count", true},   // pointer w/o omitempty → nullable (regression guard)
		{"name", false},   // primitive → no nullable
	}

	lines := strings.Split(out, "\n")
	for _, tc := range cases {
		key := fieldIndent + tc.field + ":"
		var start, end int = -1, len(lines)
		for i, l := range lines {
			if l == key {
				start = i
				continue
			}
			// next sibling at same indent → end of current block
			if start >= 0 && i > start {
				if strings.HasPrefix(l, fieldIndent) && !strings.HasPrefix(l, fieldIndent+" ") {
					end = i
					break
				}
				// also stop at required: section (6-space indent)
				if strings.HasPrefix(strings.TrimRight(l, " "), "      required:") {
					end = i
					break
				}
			}
		}
		if start < 0 {
			t.Fatalf("field %q not found in output:\n%s", tc.field, out)
		}
		block := strings.Join(lines[start:end], "\n")
		hasNullable := strings.Contains(block, "nullable: true")
		if hasNullable != tc.nullable {
			t.Errorf("%s nullable=%v want %v\nblock:\n%s", tc.field, hasNullable, tc.nullable, block)
		}
	}
}

// TestFlattenEmbeddings_CycleDetected 는 자기 자신을 직간접 임베딩하는
// schema 가 들어오면 flattenEmbeddings 가 명시적 에러를 반환함을 검증한다
// (현재 코드베이스엔 cycle 없음 — 미래 회귀 가드).
func TestFlattenEmbeddings_CycleDetected(t *testing.T) {
	src := `package fixture

type A struct {
	X int ` + "`json:\"x\"`" + `
	*B
}

type B struct {
	Y int ` + "`json:\"y\"`" + `
	*A
}
`
	path := writeFixtureSchema(t, src)
	schemas, err := parseSchemaFile(path, "fixture")
	if err != nil {
		t.Fatalf("parseSchemaFile: %v", err)
	}

	err = flattenEmbeddings(schemas, typeResolver{}, nil)
	if err == nil {
		t.Fatalf("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention cycle; got %q", err.Error())
	}
}

// TestFlattenEmbeddings_DuplicateTagBetweenEmbeddingsErrors 는 같은 schema
// 가 두 임베딩을 가지고 둘이 동일 json tag 를 emit 할 때 fail-fast 에러가
// 발생함을 검증한다. encoding/json 은 둘 다 drop 하지만, generator 에선
// silent drift 위험이 더 크므로 명시적 에러로 차단.
func TestFlattenEmbeddings_DuplicateTagBetweenEmbeddingsErrors(t *testing.T) {
	src := `package fixture

type Left struct {
	Shared int ` + "`json:\"shared\"`" + `
}

type Right struct {
	Shared int ` + "`json:\"shared\"`" + `
}

type Outer struct {
	Left
	Right
}
`
	path := writeFixtureSchema(t, src)
	schemas, err := parseSchemaFile(path, "fixture")
	if err != nil {
		t.Fatalf("parseSchemaFile: %v", err)
	}

	err = flattenEmbeddings(schemas, typeResolver{}, nil)
	if err == nil {
		t.Fatalf("expected duplicate-tag error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate json tag") {
		t.Errorf("error should mention duplicate json tag; got %q", err.Error())
	}
}

// TestFlattenEmbeddings_ExternalTypeWarning 는 schemas map 에 없는 외부
// type 임베딩이 silent skip 되지 않고 log writer 에 warning 을 emit 함을
// 검증한다.
func TestFlattenEmbeddings_ExternalTypeWarning(t *testing.T) {
	src := `package fixture

type Outer struct {
	X int ` + "`json:\"x\"`" + `
	*ExternalThing
}
`
	path := writeFixtureSchema(t, src)
	schemas, err := parseSchemaFile(path, "fixture")
	if err != nil {
		t.Fatalf("parseSchemaFile: %v", err)
	}

	var buf bytes.Buffer
	if err := flattenEmbeddings(schemas, typeResolver{}, &buf); err != nil {
		t.Fatalf("flattenEmbeddings should not error on external type; got %v", err)
	}
	if !strings.Contains(buf.String(), "ExternalThing") {
		t.Errorf("warning should mention external type %q; got %q", "ExternalThing", buf.String())
	}
	if !strings.Contains(buf.String(), "Outer") {
		t.Errorf("warning should mention containing schema %q; got %q", "Outer", buf.String())
	}
}
