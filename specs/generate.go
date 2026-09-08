package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/collabops/git-executor/internal/api/typed"
)

// OpenAPI spec generator that reads Go schema files and server.go to generate OpenAPI spec

const openAPITemplate = `openapi: 3.0.3
info:
  title: CollabOps Git Executor - Server-side Git REST API
  description: |
    Comprehensive REST API for server-side Git operations enabling web applications to perform Git operations without direct Git client interaction.

    ## Features
    - **Repository Management**: Create and initialize new Git repositories
    - **Multi-file Commits**: Atomic operations across multiple files
    - **Branch Management**: Create, update, and delete Git branches
    - **Tag Management**: Create, list, and delete Git tags
    - **Tree Operations**: Browse repository directory structure
    - **Blob Operations**: Retrieve file contents
    - **Commit Operations**: Get commit history and create commits
    - **Diff Operations**: Compare commits, branches, and files
    - **Repository State**: Query repository metadata and current state
    - **Merge Operations**: Perform Git merges with conflict detection
    - **Merge Preview**: Non-destructive merge analysis and conflict detection
    - **Revert Operations**: Create a branch with a revert commit that undoes a prior merge or commit
    - **Overview Operations**: Get comprehensive repository overviews

    ## Authentication
    All API endpoints require the following headers:
    - ` + "`X-USER-ID`" + `: User identifier for authentication
    - ` + "`X-Source`" + `: Source system identifier (must be "orchestrator" for REST API access)

    ## Error Handling
    All errors follow a consistent format with appropriate HTTP status codes and detailed error information.

    ## Rate Limiting
    APIs include built-in rate limiting and resource management to ensure optimal performance.
  version: 1.0.0
  contact:
    name: CollabOps Git Executor API Support
    url: https://github.com/collabops/git-executor
  license:
    name: MIT
    url: https://opensource.org/licenses/MIT

servers:
  - url: http://localhost:5001
    description: Development server
  - url: https://api.collabops.com
    description: Production server

security:
  - ApiKeyAuth: []

paths:
{{.Paths}}

components:
  securitySchemes:
    ApiKeyAuth:
      type: apiKey
      in: header
      name: X-USER-ID
      description: User authentication via X-USER-ID header

  parameters:
    Owner:
      name: owner
      in: path
      required: true
      description: Repository owner username
      schema:
        type: string
        pattern: '^[a-zA-Z0-9_-]+$'
        example: "testowner"

    Repo:
      name: repo
      in: path
      required: true
      description: Repository name
      schema:
        type: string
        pattern: '^[a-zA-Z0-9_.-]+$'
        example: "testrepo"

  schemas:
{{.Schemas}}

  responses:
    BadRequest:
      description: Bad request - Invalid input parameters
      content:
        application/json:
          schema:
            $ref: '#/components/schemas/ErrorResponse'
          example:
            success: false
            error: "Invalid input parameters"

    Unauthorized:
      description: Unauthorized - Missing or invalid authentication
      content:
        application/json:
          schema:
            $ref: '#/components/schemas/ErrorResponse'
          example:
            success: false
            error: "X-USER-ID header is required"

    Forbidden:
      description: Forbidden - Insufficient permissions
      content:
        application/json:
          schema:
            $ref: '#/components/schemas/ErrorResponse'
          example:
            success: false
            error: "Insufficient permissions for this operation"

    NotFound:
      description: Not found - Repository or resource not found
      content:
        application/json:
          schema:
            $ref: '#/components/schemas/ErrorResponse'
          example:
            success: false
            error: "Repository not found"

    InternalError:
      description: Internal server error
      content:
        application/json:
          schema:
            $ref: '#/components/schemas/ErrorResponse'
          example:
            success: false
            error: "An unexpected error occurred"

tags:
  - name: Repository Management
    description: Repository creation and initialization operations
  - name: Repository State
    description: Repository metadata and state queries
  - name: Branches
    description: Git branch management
  - name: Tree
    description: Repository directory structure
  - name: Commits
    description: Commit history and creation
  - name: Tags
    description: Git tag management
  - name: Blob
    description: File content retrieval
  - name: Diff
    description: Diff and comparison
  - name: Merge
    description: Git merge and merge preview
  - name: Revert
    description: Revert commits on a new branch
  - name: Overview
    description: Comprehensive repository overview
`

type RouteInfo struct {
	Method      string
	Path        string
	HandlerName string
	Package     string
}

type SchemaInfo struct {
	Name    string
	Fields  []FieldInfo
	Package string
	// Embeds 는 anonymous embedded field 의 참조 정보. JSON tag 가 없는
	// 임베딩만 담긴다 — tag 가 있으면 일반 named field 로 취급되어 Fields
	// 에 들어간다 (encoding/json 동작과 동일). flattenEmbeddings 가 후처리
	// 단계에서 이 슬라이스를 walk 하여 대상 schema 의 Fields 를 parent 의
	// Fields 에 inline 한다.
	Embeds []EmbedRef
}

// EmbedRef 는 anonymous embedded field 한 건의 메타. parseSchemaFile 단계
// 에서 채우고 flattenEmbeddings 가 resolver 를 거쳐 target SchemaInfo 를
// 찾아 펼친다.
type EmbedRef struct {
	// TypeName 은 임베딩된 타입의 unqualified 이름 (예: "DiffResponse").
	TypeName string
	// PkgQual 은 cross-package 임베딩 시 selector 의 package qualifier.
	// same-package 임베딩이면 빈 문자열. 예: `diffs.FileDiff` → "diffs".
	PkgQual string
}

type FieldInfo struct {
	Name     string
	Type     string
	JSONTag  string
	Required bool
}

// StringEnumInfo 는 “type X string“ typed-string alias + 같은 패키지의
// “const (X = "...")“ 블록으로 정의된 enum 의 정보를 담는다. 이를 OpenAPI
// 의 “type: string, enum: [...]“ 로 emit 해서 typed client (orchestrator
// auto-gen) 가 올바르게 “Literal[...]“ 으로 받아들이게 한다.
type StringEnumInfo struct {
	Name   string
	Values []string
}

func main() {
	projectRoot := findProjectRoot()
	if projectRoot == "" {
		fmt.Fprintf(os.Stderr, "Error: Could not find project root\n")
		os.Exit(1)
	}

	spec, err := runGeneration(projectRoot, typed.DefaultRegistry(), os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	outputPath := filepath.Join(projectRoot, "specs/generated/collabops-git-executor.yaml")
	if err := os.WriteFile(outputPath, []byte(spec), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing spec file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ OpenAPI spec generated successfully: %s\n", outputPath)
}

// runGeneration 은 spec 생성 파이프라인을 실행하고 결과 yaml 문자열을
// 반환한다. main 은 이 결과를 disk 에 쓰고, 테스트는 이 결과를 baseline 과
// 직접 비교한다.
//
// registry 가 nil 또는 비어있으면 모든 path 가 legacy AST 경로로 처리된다
// (Phase 1 의 byte-equivalent 회귀 가드). Phase 2 에서 도메인이 점진
// 마이그레이션되면 registry 가 우선, legacy 가 잔여 path 의 fallback.
func runGeneration(projectRoot string, registry *typed.Registry, log *os.File) (string, error) {
	if registry != nil {
		_, _ = fmt.Fprintln(log, "📚 Populating registry from domain RegisterRoutes...")
		setupSpecRoutes(registry)
		_, _ = fmt.Fprintf(log, "   Registry endpoints: %d\n", len(registry.Snapshot()))
	}

	_, _ = fmt.Fprintln(log, "🔍 Parsing server.go for routes...")
	routes, err := parseServerRoutes(filepath.Join(projectRoot, "internal/server/server.go"))
	if err != nil {
		return "", fmt.Errorf("error parsing server.go: %w", err)
	}
	_, _ = fmt.Fprintf(log, "   Found %d routes\n", len(routes))

	_, _ = fmt.Fprintln(log, "🔍 Parsing schema files from all packages...")
	schemas, resolver, err := parseAllSchemas(projectRoot, log)
	if err != nil {
		return "", fmt.Errorf("error parsing schemas: %w", err)
	}
	_, _ = fmt.Fprintf(log, "   Found %d schema types\n", len(schemas))

	stringEnums, err := parseAllStringEnums(projectRoot)
	if err != nil {
		return "", fmt.Errorf("error parsing typed-string enums: %w", err)
	}
	_, _ = fmt.Fprintf(log, "   Found %d typed-string enum aliases\n", len(stringEnums))

	_, _ = fmt.Fprintln(log, "📝 Generating OpenAPI spec...")
	spec, err := generateOpenAPISpec(routes, schemas, stringEnums, resolver, projectRoot, registry)
	if err != nil {
		return "", fmt.Errorf("error generating OpenAPI spec: %w", err)
	}

	// Self-validate before returning: a $ref to an undefined component schema
	// passes YAML parsing but breaks downstream OpenAPI codegen (orchestrator DTO
	// generation). Fail here — the earliest point — instead of after deploy.
	if err := validateSchemaRefs(spec); err != nil {
		return "", err
	}
	return spec, nil
}

// validateSchemaRefs fails generation when the spec references a component schema
// under #/components/schemas/ that is never defined in components.schemas. The
// usual root cause is a new typed domain whose schema.go was omitted from
// schemaFilePaths(): its route registers and emits a $ref, but the schema type is
// never parsed into the spec — a dangling ref that silently ships a broken spec.
func validateSchemaRefs(spec string) error {
	defined := definedSchemaNames(spec)

	refRe := regexp.MustCompile(`#/components/schemas/([A-Za-z0-9_]+)`)
	missing := map[string]struct{}{}
	for _, m := range refRe.FindAllStringSubmatch(spec, -1) {
		if _, ok := defined[m[1]]; !ok {
			missing[m[1]] = struct{}{}
		}
	}
	if len(missing) == 0 {
		return nil
	}

	names := make([]string, 0, len(missing))
	for n := range missing {
		names = append(names, n)
	}
	sort.Strings(names)
	return fmt.Errorf("dangling $ref: schema(s) referenced but not defined in "+
		"components.schemas: %s\n  → add the owning domain's schema.go to "+
		"schemaFilePaths() in specs/generate.go", strings.Join(names, ", "))
}

// definedSchemaNames returns the set of schema names defined under
// components.schemas. It scans line-by-line: the block starts at the 2-space
// "schemas:" key and ends at the next 2-space sibling key (e.g. "responses:");
// top-level schema names are the 4-space-indented keys inside it.
func definedSchemaNames(spec string) map[string]struct{} {
	defined := map[string]struct{}{}
	nameRe := regexp.MustCompile(`^    ([A-Za-z0-9_]+):`)
	inSchemas := false
	for _, line := range strings.Split(spec, "\n") {
		if !inSchemas {
			if line == "  schemas:" {
				inSchemas = true
			}
			continue
		}
		// A 2-space-indented key (indent exactly 2) is the next sibling section,
		// ending the schemas block.
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") &&
			strings.TrimSpace(line) != "" {
			break
		}
		if m := nameRe.FindStringSubmatch(line); m != nil {
			defined[m[1]] = struct{}{}
		}
	}
	return defined
}

func findProjectRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// nonRESTHelpers 는 REST API 가 아닌 endpoint 를 등록하는 helper 함수 이름.
// 이 함수들의 본문 안에서 호출되는 g.METHOD(path, handler) 는 AST scan 에서
// 무시한다 (LFS / Git protocol / docs 등 spec 대상 아님).
var nonRESTHelpers = map[string]bool{
	"registerLFSRoutes":        true,
	"registerLFSStorageRoutes": true,
	"setupSwaggerRoutes":       true,
	"setupProtocolRoutes":      true,
}

// nonRESTHandlerReceivers 는 handler 호출 시 receiver 이름이 이 목록에 있으면
// REST API endpoint 가 아니라고 간주 (server-level / LFS / Git protocol).
//
//	router.GET("/health", s.healthCheck)             — receiver "s"
//	router.GET("/ready", s.readyCheck)               — receiver "s"
//	router.GET("/workspaces/:owner/lfs/usage", lfsHandler.HandleGetUsage)
//	                                                 — receiver "lfsHandler"
//
// LFS 와 server-level routes 는 OpenAPI spec 대상이 아님 (LFS 는 별도 프로토콜,
// health/ready 는 infra). spec 출력에서 자동 제외.
var nonRESTHandlerReceivers = map[string]bool{
	"s":                  true, // server methods (s.healthCheck, s.readyCheck, ...)
	"lfsHandler":         true, // LFS endpoints (별도 프로토콜)
	"uploadPackHandler":  true, // Git Smart HTTP
	"receivePackHandler": true, // Git Smart HTTP
}

func parseServerRoutes(filePath string) ([]RouteInfo, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	var routes []RouteInfo

	// Walk top-level decls 직접 — 각 FuncDecl 별로 nonRESTHelpers 인지 판별 후
	// 본문을 skip 하거나 inspect.
	for _, decl := range node.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Body == nil {
			continue
		}
		if nonRESTHelpers[fn.Name.Name] {
			// LFS / protocol / docs helper — body 통째로 skip.
			continue
		}

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			callExpr, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := callExpr.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			method := sel.Sel.Name
			if !isHTTPMethod(method) {
				return true
			}
			if len(callExpr.Args) < 2 {
				return true
			}
			pathLit, ok := callExpr.Args[0].(*ast.BasicLit)
			if !ok {
				return true
			}
			path := strings.Trim(pathLit.Value, "\"`")

			// handler arg 분석 — receiver 가 nonRESTHandlerReceivers 면 skip.
			if selExpr, ok := callExpr.Args[1].(*ast.SelectorExpr); ok {
				recv := ""
				if ident, ok := selExpr.X.(*ast.Ident); ok {
					recv = ident.Name
				}
				if nonRESTHandlerReceivers[recv] {
					return true
				}
				routes = append(routes, RouteInfo{
					Method:      method,
					Path:        path,
					HandlerName: selExpr.Sel.Name,
					Package:     recv,
				})
			} else if ident, ok := callExpr.Args[1].(*ast.Ident); ok {
				routes = append(routes, RouteInfo{
					Method:      method,
					Path:        path,
					HandlerName: ident.Name,
				})
			}
			return true
		})
	}

	return routes, nil
}

func isHTTPMethod(name string) bool {
	methods := []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	for _, m := range methods {
		if name == m {
			return true
		}
	}
	return false
}

// routeToSpecPath converts Gin route path (ownerRepoGroup) to OpenAPI spec path.
// - Gin uses :param and *param; OpenAPI uses {param}
// - ownerRepoGroup paths are relative to /repos/:owner/:repo
// - docs routes (/openapi.yaml etc.) and /ready return skip=true (스펙에 정의 안 함)
func routeToSpecPath(routePath string) (specPath string, skip bool) {
	switch routePath {
	case "/openapi.yaml", "/openapi.yml", "/swagger.yaml", "/swagger.yml", "/ready":
		return "", true
	}
	specPath = "/repos/{owner}/{repo}" + routePath
	// Gin :param → OpenAPI {param}
	specPath = regexp.MustCompile(`:([a-zA-Z0-9_]+)`).ReplaceAllString(specPath, "{$1}")
	// Gin *param (catch-all) → OpenAPI {param}
	specPath = regexp.MustCompile(`\*([a-zA-Z0-9_]+)`).ReplaceAllString(specPath, "{$1}")
	return specPath, false
}

// parseAllStringEnums 는 schema 파일들에서 typed-string alias + const 블록을
// 찾아 OpenAPI enum 으로 emit 할 수 있는 형태로 모은다. 같은 파일 안에서
// alias 와 const 가 짝지어져 있어야 한다 (Go convention).
func parseAllStringEnums(projectRoot string) (map[string]StringEnumInfo, error) {
	enums := make(map[string]StringEnumInfo)

	for _, relPath := range schemaFilePaths() {
		fullPath := filepath.Join(projectRoot, relPath)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			continue
		}
		fileEnums, err := parseStringEnumsInFile(fullPath)
		if err != nil {
			return nil, fmt.Errorf("error parsing enums from %s: %w", relPath, err)
		}
		for name, ei := range fileEnums {
			enums[name] = ei
		}
	}

	return enums, nil
}

// parseStringEnumsInFile 는 단일 Go 파일에서 typed-string alias 와 그 alias
// 의 const 값을 수집한다. const 가 없는 alias 는 enum 으로 취급하지 않는다.
func parseStringEnumsInFile(filePath string) (map[string]StringEnumInfo, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	// Phase 1: typed-string alias 후보 수집 (`type X string`).
	aliases := make(map[string]bool)
	for _, decl := range node.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name == nil || !ts.Name.IsExported() {
				continue
			}
			ident, ok := ts.Type.(*ast.Ident)
			if !ok || ident.Name != "string" {
				continue
			}
			aliases[ts.Name.Name] = true
		}
	}

	if len(aliases) == 0 {
		return nil, nil
	}

	// Phase 2: 그 alias 들의 const 값 수집. const 블록 안에서 첫 spec 의
	// type 이 alias 면 그 블록 전체가 같은 type 으로 간주 (Go iota-like
	// 패턴이지만 string 도 동일하게 group). 다만 spec 마다 type 명시되어
	// 있을 수도 있으니 spec.Type 이 있으면 우선.
	enums := make(map[string]StringEnumInfo)
	for _, decl := range node.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		var blockType string
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if vs.Type != nil {
				if ident, ok := vs.Type.(*ast.Ident); ok {
					blockType = ident.Name
				} else {
					blockType = ""
				}
			}
			if !aliases[blockType] {
				continue
			}
			for _, val := range vs.Values {
				lit, ok := val.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				strVal := strings.Trim(lit.Value, "\"")
				ei := enums[blockType]
				ei.Name = blockType
				ei.Values = append(ei.Values, strVal)
				enums[blockType] = ei
			}
		}
	}

	return enums, nil
}

// schemaFilePaths 는 generator 가 walking 하는 schema source 파일 경로 목록.
// parseAllSchemas / parseAllStringEnums 모두 같은 set 을 사용한다.
func schemaFilePaths() []string {
	return []string{
		"internal/api/schema.go",
		"internal/repositories/schema.go",
		"internal/branches/schema.go",
		"internal/commits/schema.go",
		"internal/tags/schema.go",
		"internal/tree/schema.go",
		"internal/blob/schema.go",
		"internal/diffs/schema.go",
		"internal/merge/schema.go",
		"internal/revert/schema.go",
		"internal/state/schema.go",
		"internal/overview/schema.go",
		"internal/context/schema.go",
		"internal/langstats/schema.go",
		"internal/maintenance/schema.go",
		"internal/gitsig/schema.go",
	}
}

// typeResolver maps (parent_package, unqualified_type_name) → final spec name.
// Resolves type references inside a schema's fields to the right OpenAPI
// component name when cross-package name collisions exist.
//
// 예: commits 패키지의 `PaginationInfo` 와 overview 패키지의 `PaginationInfo` 가
// shape 이 다르면 각각 `CommitsPaginationInfo` / `OverviewPaginationInfo` 로
// qualify 된다. 동일 shape 이면 collision 없다고 보고 단일 schema 유지.
type typeResolver map[string]map[string]string

// resolve 는 parentPkg 안에서 참조된 unqualified 타입명을 spec 안 final
// 스키마명으로 매핑한다. 매핑이 없으면 원본 이름을 그대로 반환 (primitive 나
// 외부 타입).
func (r typeResolver) resolve(parentPkg, name string) string {
	if r == nil {
		return name
	}
	if pkgMap, ok := r[parentPkg]; ok {
		if mapped, ok := pkgMap[name]; ok {
			return mapped
		}
	}
	return name
}

func parseAllSchemas(projectRoot string, log io.Writer) (map[string]SchemaInfo, typeResolver, error) {
	// Phase 1: 패키지별로 schema 를 수집. 같은 이름이라도 다른 패키지면
	// 별도 보관해서 collision 검출에 쓴다.
	perPackage := map[string]map[string]SchemaInfo{}

	for _, relPath := range schemaFilePaths() {
		fullPath := filepath.Join(projectRoot, relPath)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			continue
		}

		pkg := filepath.Base(filepath.Dir(relPath))
		fileSchemas, err := parseSchemaFile(fullPath, pkg)
		if err != nil {
			return nil, nil, fmt.Errorf("error parsing %s: %w", relPath, err)
		}

		if perPackage[pkg] == nil {
			perPackage[pkg] = map[string]SchemaInfo{}
		}
		for name, schema := range fileSchemas {
			perPackage[pkg][name] = schema
		}
	}

	// Phase 2: name → []pkg 로 뒤집어서 cross-package 충돌 검출.
	nameToPackages := map[string][]string{}
	for pkg, schemas := range perPackage {
		for name := range schemas {
			nameToPackages[name] = append(nameToPackages[name], pkg)
		}
	}

	// Phase 3: 충돌 있는 이름은 패키지 prefix 로 qualify, 없으면 원본 유지.
	// 동일 shape (== identical struct) 가 여러 패키지에 있으면 collision 으로
	// 보지 않고 한 컴포넌트로 합친다 (드물지만 안전).
	schemas := map[string]SchemaInfo{}
	resolver := typeResolver{}

	for name, pkgs := range nameToPackages {
		sort.Strings(pkgs) // deterministic
		if len(pkgs) == 1 {
			pkg := pkgs[0]
			schemas[name] = perPackage[pkg][name]
			ensurePkg(resolver, pkg)[name] = name
			continue
		}

		// 다중 패키지 — shape 가 모두 같으면 한 schema 로 통합.
		first := perPackage[pkgs[0]][name]
		allSame := true
		for _, pkg := range pkgs[1:] {
			if !schemasEqual(first, perPackage[pkg][name]) {
				allSame = false
				break
			}
		}

		if allSame {
			schemas[name] = first
			for _, pkg := range pkgs {
				ensurePkg(resolver, pkg)[name] = name
			}
			continue
		}

		// 진짜 collision — 패키지 prefix 로 qualify.
		for _, pkg := range pkgs {
			qualified := titleCasePackage(pkg) + name
			schema := perPackage[pkg][name]
			schema.Name = qualified
			schemas[qualified] = schema
			ensurePkg(resolver, pkg)[name] = qualified
		}
	}

	// Phase 4: anonymous embedded fields 를 flatten — encoding/json 의 anonymous
	// field 동작과 동일. cycle detection + parent-wins (이름 충돌 시 parent 우선).
	if err := flattenEmbeddings(schemas, resolver, log); err != nil {
		return nil, nil, fmt.Errorf("flatten embeddings: %w", err)
	}

	return schemas, resolver, nil
}

// flattenEmbeddings 는 schemas 안의 모든 SchemaInfo.Embeds 를 walk 하여 대상
// schema 의 Fields 를 parent.Fields 에 inline 한다. encoding/json 의
// anonymous field 동작 mirror:
//
//   - parent 와 embedded 의 같은 json key 충돌 시 parent 우선 (Go shadowing 규칙)
//   - 같은 depth 의 다중 임베딩 간 동일 json key 충돌 시 fail-fast 에러
//     (encoding/json 은 둘 다 drop 하지만, generator 에선 silent drift 위험을
//     막기 위해 명시적 에러 — 의도된 케이스라면 한 쪽 임베딩에 json tag 명시
//     으로 해소)
//   - cycle (자기 자신 직간접 임베딩) 발견 시 명시적 에러
//   - 같은 임베딩이 여러 단계 거쳐 들어와도 한 번만 inline (fixed-point)
//   - 외부 type (resolver 가 매핑 못함) 임베딩은 skip + log 에 warning
//
// 결정성: schemas iteration 은 sort 된 name 순서로 시작. 각 schema 의
// Fields 는 source-order 보존, embedded fields 는 parent's own fields 앞에
// prepend (encoding/json 의 marshal 결과는 source-order 따라 임베딩 위치에
// inline 되지만 OpenAPI 의 properties 는 unordered → 단순한 prepend 가
// readable).
//
// log 가 nil 이면 warning 출력은 생략 (test 등 silent 모드).
func flattenEmbeddings(schemas map[string]SchemaInfo, resolver typeResolver, log io.Writer) error {
	// flat 은 visit 가 끝난 schema 의 최종 Fields 를 캐시한다 (동일 schema
	// 가 다른 parent 에 임베딩될 때 재계산 방지).
	flat := map[string][]FieldInfo{}

	var visit func(name string, stack map[string]bool) error
	visit = func(name string, stack map[string]bool) error {
		if _, done := flat[name]; done {
			return nil
		}
		if stack[name] {
			return fmt.Errorf("cycle detected involving %q", name)
		}
		sc, ok := schemas[name]
		if !ok {
			// 외부 type (resolver 가 매핑 못함) — flatten 대상 아님. parent
			// 측에서는 embedded fields 0건으로 취급.
			return nil
		}

		stack[name] = true
		defer delete(stack, name)

		// 1) parent own fields 의 json tag set — parent-wins 충돌 처리에 사용.
		ownTags := map[string]bool{}
		for _, f := range sc.Fields {
			if f.JSONTag != "" && f.JSONTag != "-" {
				ownTags[f.JSONTag] = true
			}
		}

		// 2) 각 embedding 을 재귀 flatten 후 parent 에 prepend.
		var inlined []FieldInfo
		seenInlinedTags := map[string]bool{}
		for _, e := range sc.Embeds {
			lookupPkg := e.PkgQual
			if lookupPkg == "" {
				lookupPkg = sc.Package
			}
			target := resolver.resolve(lookupPkg, e.TypeName)
			if err := visit(target, stack); err != nil {
				return err
			}
			embeddedFields, ok := flat[target]
			if !ok {
				// 외부 type (generator 의 schemaFilePaths() 밖의 type, 예:
				// third-party) — flatten 대상 schema 가 없으므로 skip.
				// 미래 회귀 가드용으로 warning 만 emit.
				if log != nil {
					qual := e.TypeName
					if e.PkgQual != "" {
						qual = e.PkgQual + "." + e.TypeName
					}
					_, _ = fmt.Fprintf(log, "⚠️  flattenEmbeddings: %q embeds %q which is not in the schema set; embedded fields will be missing from spec\n",
						name, qual)
				}
				continue
			}
			for _, f := range embeddedFields {
				if f.JSONTag == "" || f.JSONTag == "-" {
					continue
				}
				if ownTags[f.JSONTag] {
					continue // parent-wins (Go shadowing)
				}
				if seenInlinedTags[f.JSONTag] {
					// 같은 depth 의 다른 임베딩이 동일 tag 를 가진 경우.
					// encoding/json 은 둘 다 drop 하지만, generator 에선
					// silent drift 위험이 더 크므로 fail-fast. 의도된 충돌이면
					// 한 쪽 임베딩에 json tag 명시 으로 해소 가능.
					return fmt.Errorf(
						"schema %q: duplicate json tag %q from multiple anonymous embeddings",
						name, f.JSONTag)
				}
				seenInlinedTags[f.JSONTag] = true
				inlined = append(inlined, f)
			}
		}

		merged := append(inlined, sc.Fields...)
		flat[name] = merged
		sc.Fields = merged
		schemas[name] = sc
		return nil
	}

	// 결정성: schemas iteration 순서를 sort 로 고정.
	names := make([]string, 0, len(schemas))
	for name := range schemas {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := visit(name, map[string]bool{}); err != nil {
			return err
		}
	}
	return nil
}

func ensurePkg(r typeResolver, pkg string) map[string]string {
	if r[pkg] == nil {
		r[pkg] = map[string]string{}
	}
	return r[pkg]
}

// schemasEqual 은 두 SchemaInfo 의 shape 이 동일한지 비교한다 (필드 순서
// 무관, 이름·타입·json tag·required + anonymous embedding 참조 set 비교).
//
// Embeds 비교가 빠지면 두 패키지 같은 이름 schema 중 한 쪽만 임베딩을 가질
// 때 collision detection 이 둘을 동일 판정해 한 쪽 임베딩이 silent 하게
// 무시될 수 있음 (Issue-2343 review 에서 식별된 회귀 가드).
func schemasEqual(a, b SchemaInfo) bool {
	if len(a.Fields) != len(b.Fields) {
		return false
	}
	aFields := map[string]FieldInfo{}
	for _, f := range a.Fields {
		aFields[f.Name] = f
	}
	for _, f := range b.Fields {
		af, ok := aFields[f.Name]
		if !ok {
			return false
		}
		if af.Type != f.Type || af.JSONTag != f.JSONTag || af.Required != f.Required {
			return false
		}
	}
	if len(a.Embeds) != len(b.Embeds) {
		return false
	}
	embedKey := func(e EmbedRef) string { return e.PkgQual + "." + e.TypeName }
	aEmbeds := map[string]bool{}
	for _, e := range a.Embeds {
		aEmbeds[embedKey(e)] = true
	}
	for _, e := range b.Embeds {
		if !aEmbeds[embedKey(e)] {
			return false
		}
	}
	return true
}

// titleCasePackage 는 패키지 이름의 첫 글자를 대문자로 변환한다 (예:
// "commits" → "Commits"). schema name qualify 시 prefix 로 쓰인다.
func titleCasePackage(pkg string) string {
	if pkg == "" {
		return ""
	}
	return strings.ToUpper(pkg[:1]) + pkg[1:]
}

func parseSchemaFile(filePath string, pkg string) (map[string]SchemaInfo, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	schemas := make(map[string]SchemaInfo)

	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.TypeSpec:
			if structType, ok := x.Type.(*ast.StructType); ok {
				if x.Name != nil && x.Name.IsExported() {
					schema := SchemaInfo{
						Name:    x.Name.Name,
						Fields:  []FieldInfo{},
						Package: pkg,
					}

					if structType.Fields != nil {
						for _, field := range structType.Fields.List {
							if len(field.Names) > 0 && field.Names[0].IsExported() {
								fieldInfo := FieldInfo{
									Name: field.Names[0].Name,
									Type: getTypeString(field.Type),
								}

								if field.Tag != nil {
									tag := reflect.StructTag(strings.Trim(field.Tag.Value, "`"))
									jsonTag := tag.Get("json")
									if jsonTag != "" {
										parts := strings.Split(jsonTag, ",")
										fieldInfo.JSONTag = parts[0]
										fieldInfo.Required = !strings.Contains(jsonTag, "omitempty")
									}
								}

								// Go pointer 타입 (`*T`) 은 nil 가능 — 의미상 nullable.
								// `omitempty` 없이도 nil 이면 wire 가 `null` 을 emit 한다.
								// Spec 에서 required 로 두면 client 가 strict validation 에서
								// null 거부 (ResponseValidationError) → drift. 따라서 pointer
								// field 는 항상 not-required 로 emit 한다.
								//
								// 영향: Pydantic auto-gen 등 typed client 는 `Optional[T]` 로
								// 받게 되어 null 을 안전하게 수용. 비-pointer (value) 타입은
								// 기존 omitempty 기반 로직 유지.
								if strings.HasPrefix(fieldInfo.Type, "*") {
									fieldInfo.Required = false
								}

								schema.Fields = append(schema.Fields, fieldInfo)
							} else if len(field.Names) == 0 {
								// Anonymous embedded field. encoding/json 의 동작:
								//   - JSON tag 없음 → embedded struct 의 fields 가
								//     parent 에 inline (flatten). Embeds 에 추가하여
								//     Phase 4 flattenEmbeddings 가 후처리.
								//   - JSON tag 있음 → 일반 named field 로 취급 (nested
								//     ref). field name 은 임베딩된 type name.
								//   - JSON tag "-" → 무시.
								typeName, pkgQual := parseEmbeddedType(field.Type)
								if typeName == "" || !isExportedIdent(typeName) {
									continue
								}

								var jsonTag string
								var jsonTagFull string
								if field.Tag != nil {
									tag := reflect.StructTag(strings.Trim(field.Tag.Value, "`"))
									jsonTagFull = tag.Get("json")
									if jsonTagFull == "-" {
										continue
									}
									if jsonTagFull != "" {
										parts := strings.Split(jsonTagFull, ",")
										jsonTag = parts[0]
									}
								}

								if jsonTag != "" {
									// Tagged embedding → 일반 field 처럼 emit.
									fieldInfo := FieldInfo{
										Name:     typeName,
										Type:     getTypeString(field.Type),
										JSONTag:  jsonTag,
										Required: !strings.Contains(jsonTagFull, "omitempty"),
									}
									if strings.HasPrefix(fieldInfo.Type, "*") {
										fieldInfo.Required = false
									}
									schema.Fields = append(schema.Fields, fieldInfo)
									continue
								}

								// Untagged anonymous embedding → flatten 대상.
								schema.Embeds = append(schema.Embeds, EmbedRef{
									TypeName: typeName,
									PkgQual:  pkgQual,
								})
							}
						}
					}

					schemas[schema.Name] = schema
				}
			}
		}
		return true
	})

	return schemas, nil
}

// parseEmbeddedType 은 anonymous embedded field 의 type 표현식을 (typeName,
// pkgQual) 로 분해한다. pointer 임베딩은 deref. cross-package 임베딩이면
// pkgQual 에 selector 의 X (package alias) 가 들어간다.
//
// 지원 형태:
//   - Foo            → ("Foo", "")
//   - *Foo           → ("Foo", "")
//   - pkg.Foo        → ("Foo", "pkg")
//   - *pkg.Foo       → ("Foo", "pkg")
//
// 그 외 (interface 임베딩, generic 등) 는 ("", "") 반환 → 호출자가 skip.
func parseEmbeddedType(expr ast.Expr) (typeName, pkgQual string) {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name, ""
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return t.Sel.Name, id.Name
		}
	case *ast.StarExpr:
		return parseEmbeddedType(t.X)
	}
	return "", ""
}

// isExportedIdent 은 Go 의 exported identifier 규칙 (첫 글자 대문자) 을 검사.
// ast.Ident.IsExported 와 동일하지만 raw string 입력에 사용.
func isExportedIdent(name string) bool {
	if name == "" {
		return false
	}
	c := name[0]
	return c >= 'A' && c <= 'Z'
}

func getTypeString(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.ArrayType:
		return "[]" + getTypeString(x.Elt)
	case *ast.MapType:
		return "map[" + getTypeString(x.Key) + "]" + getTypeString(x.Value)
	case *ast.StarExpr:
		return "*" + getTypeString(x.X)
	case *ast.InterfaceType:
		return "interface{}"
	default:
		return "unknown"
	}
}

func generateOpenAPISpec(routes []RouteInfo, schemas map[string]SchemaInfo, stringEnums map[string]StringEnumInfo, resolver typeResolver, projectRoot string, registry *typed.Registry) (string, error) {
	// 항상 Go 기준으로 스펙 생성
	tmpl, err := template.New("openapi").Parse(openAPITemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	paths := generatePathsFromRoutes(routes, registry)
	schemaDefs := generateSchemasFromStructs(schemas, stringEnums, resolver)

	data := struct {
		Paths   string
		Schemas string
	}{
		Paths:   paths,
		Schemas: schemaDefs,
	}

	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// pathOperation 은 registry / legacy 양쪽에서 모이는 path operation 의
// 통일된 중간 형태. renderPathOperations 가 yaml 출력으로 변환한다.
type pathOperation struct {
	method      string
	specPath    string
	summary     string
	operationID string
	tag         string
	pathParams  []string         // owner/repo 외 추가 path param 이름들
	queryParams []queryParamSpec // ?source_ref=... 같은 query 파라미터 (typed.RegisterGetWithQuery 전용)
	requestRef  string           // requestBody $ref schema 이름; 빈 문자열이면 body 없음
	responseRef string           // 200 응답 $ref schema 이름; 빈 문자열이면 body 없음

	// rawResponseContentType 은 RegisterRaw endpoint 의 200 응답 content-type
	// (예: application/octet-stream, text/plain) 을 담는다. responseRef 가
	// 비어있고 이 값이 set 되면 spec 에 schema 없는 raw content 블록을 emit.
	// orchestrator auto-gen 클라이언트가 raw bytes/text endpoint 로 인식하기
	// 위함 (Issue-2327).
	rawResponseContentType string
}

// queryParamSpec 은 OpenAPI 의 `parameters: [in: query, ...]` 한 줄을
// 그리는 데 필요한 최소 메타데이터.
type queryParamSpec struct {
	name     string
	required bool
	openAPI  queryParamSchema // type / format
}

// queryParamSchema 는 query parameter 의 OpenAPI schema 를 정의한다.
// gin 의 form binding 이 다루는 primitive 만 지원 (struct/map/slice 는
// query 에 부적절).
type queryParamSchema struct {
	typ    string // "string" | "integer" | "boolean" | "number"
	format string // optional ("int64" 등); 빈 문자열이면 미출력
}

// generatePathsFromRoutes 는 registry-first hybrid 로 path operation 들을
// 모은다. 동일 (method, specPath) 가 registry 와 legacy 양쪽에 있으면 registry
// 가 우선. registry 비어있는 경우 (Phase 1) 출력은 legacy-only 결과와 byte
// equivalent.
func generatePathsFromRoutes(routes []RouteInfo, registry *typed.Registry) string {
	var ops []pathOperation
	registered := make(map[string]bool)

	if registry != nil {
		registry.Walk(func(ep typed.Endpoint) {
			// ExcludeFromSpec 도 registered 에 마킹해서 legacy fallback 이 다시
			// 노출하지 못하게 한다 (런타임은 그대로, spec 만 숨김).
			registered[ep.Method+" "+ep.Path] = true
			if ep.ExcludeFromSpec {
				return
			}
			ops = append(ops, registryEndpointToOperation(ep))
		})
	}

	for _, route := range routes {
		specPath, skip := routeToSpecPath(route.Path)
		if skip {
			continue
		}
		if registered[route.Method+" "+specPath] {
			continue
		}
		ops = append(ops, legacyRouteToOperation(route, specPath))
	}

	return renderPathOperations(ops)
}

// registryEndpointToOperation 은 typed.Endpoint 를 generator 의 중간 형태로
// 변환한다.
func registryEndpointToOperation(ep typed.Endpoint) pathOperation {
	op := pathOperation{
		method:      ep.Method,
		specPath:    ep.Path,
		summary:     ep.Summary,
		operationID: ep.OperationID,
		tag:         ep.Tag,
		pathParams:  pathParamsFromSpecPath(ep.Path),
	}
	if op.summary == "" && ep.OperationID != "" {
		op.summary = ep.OperationID
	}
	if op.operationID == "" && op.summary != "" {
		op.operationID = strings.ToLower(op.summary)
	}
	if ep.ReqType != nil && ep.ReqLocation == typed.LocationBodyJSON {
		op.requestRef = ep.ReqType.Name()
	}
	if ep.ReqLocation == typed.LocationQuery {
		op.queryParams = queryParamsFromEndpoint(ep.QueryParams)
	}
	for _, r := range ep.Responses {
		if r.Status == 200 || r.Status == 0 {
			switch {
			case r.SchemaRef != "":
				op.responseRef = r.SchemaRef
			case r.RespType != nil:
				op.responseRef = r.RespType.Name()
			}
			// RegisterRaw endpoint: schema 가 없고 ContentType 이 application/json
			// 이외인 경우 raw content type 을 별도 필드로 보관하여
			// renderSingleOperation 이 spec 에 명시적으로 노출하도록 한다.
			if op.responseRef == "" && r.ContentType != "" && r.ContentType != "application/json" {
				op.rawResponseContentType = r.ContentType
			}
			break
		}
	}
	return op
}

// legacyRouteToOperation 은 server.go AST 추출 결과 + hardcoded switch 로
// 채워진 legacy 매핑을 중간 형태로 변환한다 (Phase 2 마이그레이션이 끝나면
// 이 경로는 dead code 가 되어 삭제).
func legacyRouteToOperation(route RouteInfo, specPath string) pathOperation {
	return pathOperation{
		method:      route.Method,
		specPath:    specPath,
		summary:     route.HandlerName,
		operationID: strings.ToLower(route.HandlerName),
		tag:         getTagFromPath(specPath),
		pathParams:  pathParamsFromSpecPath(specPath),
		requestRef:  requestSchemaRef(route.Method, specPath),
		responseRef: responseSchemaRef(route.Method, specPath),
	}
}

// renderPathOperations 는 path 별로 그룹핑 후 path 알파벳 정렬, path 안에서는
// 입력 순서를 유지하여 yaml 을 작성한다.
func renderPathOperations(ops []pathOperation) string {
	pathMap := make(map[string][]pathOperation)
	pathOrder := make([]string, 0)
	seen := make(map[string]bool)
	for _, op := range ops {
		if !seen[op.specPath] {
			pathOrder = append(pathOrder, op.specPath)
			seen[op.specPath] = true
		}
		pathMap[op.specPath] = append(pathMap[op.specPath], op)
	}

	sort.Strings(pathOrder)

	var buf strings.Builder
	for _, specPath := range pathOrder {
		_, _ = fmt.Fprintf(&buf, "  %s:\n", specPath)
		for _, op := range pathMap[specPath] {
			renderSingleOperation(&buf, op)
		}
	}
	return buf.String()
}

// renderSingleOperation 은 한 (method, path) operation 의 yaml 블록을
// 작성한다. 출력 포맷은 기존 generatePathsFromRoutes 의 inline 코드와
// byte-equivalent.
func renderSingleOperation(buf *strings.Builder, op pathOperation) {
	method := strings.ToLower(op.method)
	_, _ = fmt.Fprintf(buf, "    %s:\n", method)
	_, _ = fmt.Fprintf(buf, "      summary: %s\n", op.summary)
	_, _ = fmt.Fprintf(buf, "      operationId: %s\n", op.operationID)
	buf.WriteString("      tags:\n")
	_, _ = fmt.Fprintf(buf, "        - %s\n", op.tag)
	buf.WriteString("      parameters:\n")
	buf.WriteString("        - $ref: '#/components/parameters/Owner'\n")
	buf.WriteString("        - $ref: '#/components/parameters/Repo'\n")
	for _, p := range op.pathParams {
		_, _ = fmt.Fprintf(buf, "        - name: %s\n", p)
		buf.WriteString("          in: path\n")
		buf.WriteString("          required: true\n")
		buf.WriteString("          schema:\n")
		buf.WriteString("            type: string\n")
	}
	for _, q := range op.queryParams {
		_, _ = fmt.Fprintf(buf, "        - name: %s\n", q.name)
		buf.WriteString("          in: query\n")
		if q.required {
			buf.WriteString("          required: true\n")
		}
		buf.WriteString("          schema:\n")
		_, _ = fmt.Fprintf(buf, "            type: %s\n", q.openAPI.typ)
		if q.openAPI.format != "" {
			_, _ = fmt.Fprintf(buf, "            format: %s\n", q.openAPI.format)
		}
	}
	if op.requestRef != "" {
		buf.WriteString("      requestBody:\n")
		buf.WriteString("        required: true\n")
		buf.WriteString("        content:\n")
		buf.WriteString("          application/json:\n")
		buf.WriteString("            schema:\n")
		_, _ = fmt.Fprintf(buf, "              $ref: '#/components/schemas/%s'\n", openAPISchemaName(op.requestRef))
	}
	buf.WriteString("      responses:\n")
	buf.WriteString("        '200':\n")
	buf.WriteString("          description: Success\n")
	switch {
	case op.responseRef != "":
		buf.WriteString("          content:\n")
		buf.WriteString("            application/json:\n")
		buf.WriteString("              schema:\n")
		_, _ = fmt.Fprintf(buf, "                $ref: '#/components/schemas/%s'\n", openAPISchemaName(op.responseRef))
	case op.rawResponseContentType != "":
		// RegisterRaw endpoint — schema 없는 binary/text content. orchestrator
		// auto-gen 클라이언트는 이 content-type 만 보고 raw bytes/text reader
		// 를 emit 한다.
		buf.WriteString("          content:\n")
		_, _ = fmt.Fprintf(buf, "            %s:\n", op.rawResponseContentType)
		buf.WriteString("              schema:\n")
		buf.WriteString("                type: string\n")
		buf.WriteString("                format: binary\n")
	}
	buf.WriteString("        '400':\n")
	buf.WriteString("          $ref: '#/components/responses/BadRequest'\n")
}

// requestSchemaRef returns the component schema name for requestBody (POST/PATCH with body).
//
// Phase 2.13 (모든 REST 도메인 typed migration 완료) 이후 비어있다 — registry
// 가 모든 request schema 를 처리한다. AST-scanned route 가 새로 추가되어야
// 한다면 (예: LFS 미마이그레이션 잔존, future endpoint), 그 path 의 case 를
// 여기 추가하기보다는 도메인 routes.go 에 typed.RegisterX + WithRequestBody 로
// 등록하는 것이 권장된다.
func requestSchemaRef(method, specPath string) string {
	_ = method
	_ = specPath
	return ""
}

// responseSchemaRef returns the component schema name for 200 response body.
//
// Phase 2.13 이후 비어있다 — registry 가 모든 response schema 를 처리. (위
// requestSchemaRef 와 같은 권장사항 적용.)
func responseSchemaRef(method, specPath string) string {
	_ = method
	_ = specPath
	return ""
}

// queryParamsFromEndpoint 은 typed.Endpoint.QueryParams 를 generator 중간
// 형태로 변환한다. RegisterGetWithQuery 등록 시점에 reflect 로 채워진
// ParamSpec 을 OpenAPI 의 in:query parameter 로 그릴 수 있도록 type/format
// 을 매핑한다.
func queryParamsFromEndpoint(params []typed.ParamSpec) []queryParamSpec {
	if len(params) == 0 {
		return nil
	}
	out := make([]queryParamSpec, 0, len(params))
	for _, p := range params {
		out = append(out, queryParamSpec{
			name:     p.Name,
			required: p.Required,
			openAPI:  reflectTypeToQuerySchema(p.GoType),
		})
	}
	return out
}

// reflectTypeToQuerySchema 는 Go reflect.Type 을 OpenAPI primitive
// (type/format) 으로 매핑한다. gin form binding 의 가능 대상만 다룬다 —
// pointer 는 deref, 그 외 unsupported 는 string 으로 fallback (orchestrator
// 가 안전하게 받기 위함).
func reflectTypeToQuerySchema(t reflect.Type) queryParamSchema {
	if t == nil {
		return queryParamSchema{typ: "string"}
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	// `type X string` typed-string alias 는 OpenAPI 에선 underlying primitive
	// (string) 으로 노출 — query 는 enum 표현보다 plain string 이 호환성
	// 측면에서 안전하다 (orchestrator client 가 Literal 또는 str 둘 다 OK).
	switch t.Kind() {
	case reflect.String:
		return queryParamSchema{typ: "string"}
	case reflect.Bool:
		return queryParamSchema{typ: "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return queryParamSchema{typ: "integer"}
	case reflect.Int64, reflect.Uint64:
		return queryParamSchema{typ: "integer", format: "int64"}
	case reflect.Float32, reflect.Float64:
		return queryParamSchema{typ: "number"}
	default:
		return queryParamSchema{typ: "string"}
	}
}

// pathParamsFromSpecPath returns path param names (e.g. branch, name) from specPath, excluding owner/repo.
func pathParamsFromSpecPath(specPath string) []string {
	re := regexp.MustCompile(`\{([a-zA-Z0-9_]+)\}`)
	matches := re.FindAllStringSubmatch(specPath, -1)
	var out []string
	for _, m := range matches {
		if len(m) >= 2 {
			name := m[1]
			if name != "owner" && name != "repo" {
				out = append(out, name)
			}
		}
	}
	return out
}

func getTagFromPath(path string) string {
	if strings.Contains(path, "/branches") {
		return "Branches"
	} else if strings.Contains(path, "/commits") {
		return "Commits"
	} else if strings.Contains(path, "/tags") {
		return "Tags"
	} else if strings.Contains(path, "/tree") {
		return "Tree"
	} else if strings.Contains(path, "/blob") {
		return "Blob"
	} else if strings.Contains(path, "/diff") {
		return "Diff"
	} else if strings.Contains(path, "/merges") {
		return "Merge"
	} else if strings.Contains(path, "/revert") {
		return "Revert"
	} else if strings.Contains(path, "/overview") {
		return "Overview"
	} else if strings.Contains(path, "/state") {
		return "Repository State"
	}
	return "Repository Management"
}

// openAPISchemaName returns the schema name for OpenAPI (no dash), so client generators produce e.g. FileOperationResponseOutput instead of FileOperationResponse-Output.
func openAPISchemaName(s string) string {
	return strings.ReplaceAll(s, "-", "")
}

func generateSchemasFromStructs(schemas map[string]SchemaInfo, stringEnums map[string]StringEnumInfo, resolver typeResolver) string {
	var buf strings.Builder

	// component schema 이름 = struct + 승격된 named(`Enum` 접미사) string enum.
	// 둘을 한 알파벳 정렬 목록으로 합쳐 결정적으로 emit 한다 (이름 공간은
	// 서로 disjoint — 기존 11개 alias 중 `Enum` 으로 끝나는 것은 없음).
	seen := make(map[string]bool, len(schemas)+len(stringEnums))
	names := make([]string, 0, len(schemas)+len(stringEnums))
	for name := range schemas {
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	for name, ei := range stringEnums {
		if isNamedEnum(name) && len(ei.Values) > 0 && !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	sort.Strings(names)

	for _, name := range names {
		if schema, ok := schemas[name]; ok {
			buf.WriteString(renderSchemaYAML(schema, schemas, stringEnums, openAPISchemaName, resolver))
			continue
		}
		buf.WriteString(renderEnumSchemaYAML(stringEnums[name], openAPISchemaName))
	}

	return buf.String()
}

// isNamedEnum 은 typed-string enum alias 를 top-level components/schemas 엔트리
// 로 emit (+ 사용 지점에서 $ref) 할지 판정한다. 규약: alias 이름이 `Enum` 으로
// 끝나면 named component, 아니면 기존대로 사용 지점 inline.
func isNamedEnum(name string) bool {
	return strings.HasSuffix(name, "Enum")
}

// renderEnumSchemaYAML 은 string-enum 을 top-level component schema 로 그린다:
//
//	ErrorCodeEnum:
//	  type: string
//	  enum: [A, B, C]
func renderEnumSchemaYAML(ei StringEnumInfo, schemaNameFn func(string) string) string {
	var buf strings.Builder
	_, _ = fmt.Fprintf(&buf, "    %s:\n", schemaNameFn(ei.Name))
	buf.WriteString("      type: string\n")
	_, _ = fmt.Fprintf(&buf, "      enum: [%s]\n", strings.Join(ei.Values, ", "))
	return buf.String()
}

func mapGoTypeToOpenAPI(goType string) string {
	switch {
	case goType == "string":
		return "string"
	case goType == "int" || goType == "int64" || goType == "int32" ||
		goType == "int16" || goType == "int8" ||
		goType == "uint" || goType == "uint64" || goType == "uint32" ||
		goType == "uint16" || goType == "uint8" ||
		goType == "rune" || goType == "byte":
		return "integer"
	case goType == "bool":
		return "boolean"
	case goType == "float64" || goType == "float32":
		return "number"
	case goType == "Duration":
		// time.Duration → int64 nanoseconds (Go marshal 동작)
		return "integer"
	case strings.HasPrefix(goType, "[]"):
		return "array"
	case strings.HasPrefix(goType, "map["):
		return "object"
	case strings.HasPrefix(goType, "*"):
		return mapGoTypeToOpenAPI(goType[1:])
	default:
		return "object"
	}
}

func renderSchemaYAML(schema SchemaInfo, knownSchemas map[string]SchemaInfo, stringEnums map[string]StringEnumInfo, schemaNameFn func(string) string, resolver typeResolver) string {
	var buf strings.Builder
	_, _ = fmt.Fprintf(&buf, "    %s:\n", schemaNameFn(schema.Name))
	buf.WriteString("      type: object\n")

	// Only include fields with explicit json tags to avoid accidentally generating incorrect schemas
	// for internal structs (e.g. MergeResult) that aren't intended for JSON responses.
	taggedFields := make([]FieldInfo, 0, len(schema.Fields))
	for _, f := range schema.Fields {
		if f.JSONTag == "" || f.JSONTag == "-" {
			continue
		}
		taggedFields = append(taggedFields, f)
	}
	if len(taggedFields) == 0 {
		return buf.String()
	}

	required := make([]string, 0, len(taggedFields))
	buf.WriteString("      properties:\n")
	for _, field := range taggedFields {
		fieldName := field.JSONTag
		_, _ = fmt.Fprintf(&buf, "        %s:\n", fieldName)
		propLines := renderOpenAPIPropertyYAML(field.Type, schema.Package, knownSchemas, stringEnums, schemaNameFn, resolver)
		for _, l := range propLines {
			buf.WriteString("          " + l + "\n")
		}
		// Go encoding/json 의 nil 동작을 OpenAPI nullable 로 매핑한다.
		// - pointer (`*T`): Go 의미상 항상 nullable. parseSchemaFile 이 Required=false
		//   로 강제했지만, wire 는 (omitempty 가 없는 한) null 키로 직렬화하므로
		//   client codegen 이 `T | null` 로 인식하도록 nullable: true 를 emit.
		// - slice / map: omitempty 없을 때만 (Required=true) nullable 로 표기.
		//   omitempty 있으면 Go 가 키를 omit 하므로 spec 도 단순 optional.
		// - $ref 와 함께는 nullable 을 직접 둘 수 없어 primitive/array/object 한정
		//   (OpenAPI 3.0 에서 $ref + nullable 은 allOf 래핑이 필요함).
		isPointer := strings.HasPrefix(field.Type, "*")
		isNilableSliceMap := !isPointer &&
			(strings.HasPrefix(field.Type, "[]") || strings.HasPrefix(field.Type, "map["))
		shouldEmitNullable := isPointer || (field.Required && isNilableSliceMap)
		if shouldEmitNullable && !isRefProperty(propLines) {
			buf.WriteString("          nullable: true\n")
		}
		if field.Required {
			required = append(required, fieldName)
		}
	}

	if len(required) > 0 {
		sort.Strings(required)
		buf.WriteString("      required:\n")
		for _, r := range required {
			_, _ = fmt.Fprintf(&buf, "        - %s\n", r)
		}
	}

	return buf.String()
}

// isRefProperty 는 렌더된 property 라인들이 $ref 로 시작하는지 판정한다.
// OpenAPI 3.0 에서 $ref 와 동일 레벨에 nullable 을 두는 것이 유효하지 않기 때문에
// 포인터 필드라도 $ref 인 경우 nullable 을 추가하지 않는다.
func isRefProperty(propLines []string) bool {
	for _, l := range propLines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		return strings.HasPrefix(trimmed, "$ref:")
	}
	return false
}

func renderOpenAPIPropertyYAML(goType string, parentPkg string, knownSchemas map[string]SchemaInfo, stringEnums map[string]StringEnumInfo, schemaNameFn func(string) string, resolver typeResolver) []string {
	base := strings.TrimPrefix(goType, "*")

	// 우선 typed-string enum (``type X string`` + const) 으로 정의된 alias 면
	// inline enum 으로 emit. 같은 이름의 struct schema 보다 우선 — string-typed
	// alias 와 struct 이름이 충돌할 일은 없지만 의도를 명확히 한다.
	if ei, ok := stringEnums[base]; ok && len(ei.Values) > 0 {
		// `Enum` 접미사 alias 는 top-level component 로 승격되어 있으므로
		// 사용 지점에서는 inline 대신 $ref 로 참조한다 (downstream codegen /
		// orchestrator error-code enum sync 가 공유 타입으로 소비). 그 외
		// alias 는 기존대로 inline enum 유지 (zero churn).
		if isNamedEnum(base) {
			return []string{fmt.Sprintf("$ref: '#/components/schemas/%s'", schemaNameFn(base))}
		}
		return []string{
			"type: string",
			fmt.Sprintf("enum: [%s]", strings.Join(ei.Values, ", ")),
		}
	}

	// 같은 패키지에서 unqualified 타입 ref 는 collision 이 있을 수 있으므로
	// resolver 를 거쳐 final spec name 으로 매핑한다.
	resolvedBase := resolver.resolve(parentPkg, base)
	if _, ok := knownSchemas[resolvedBase]; ok {
		return []string{fmt.Sprintf("$ref: '#/components/schemas/%s'", schemaNameFn(resolvedBase))}
	}

	switch {
	case base == "string":
		return []string{"type: string"}
	case base == "int" || base == "int64" || base == "int32" ||
		base == "int16" || base == "int8" ||
		base == "uint" || base == "uint64" || base == "uint32" ||
		base == "uint16" || base == "uint8" ||
		base == "rune" || base == "byte":
		return []string{"type: integer"}
	case base == "bool":
		return []string{"type: boolean"}
	case base == "float64" || base == "float32":
		return []string{"type: number"}
	case base == "Time":
		// Best-effort mapping for time.Time (getTypeString strips package)
		return []string{"type: string", "format: date-time"}
	case base == "Duration":
		// time.Duration 은 int64 nanoseconds 로 JSON marshal 된다
		// (`json:",omitempty"` 무관). 기본 type alias 라 known schema 가 아니라
		// 여기서 명시 매핑. format: int64 로 client codegen 이 정확한 타입 받게 함.
		return []string{"type: integer", "format: int64"}
	case strings.HasPrefix(base, "[]"):
		elem := strings.TrimPrefix(strings.TrimPrefix(base, "[]"), "*")
		resolvedElem := resolver.resolve(parentPkg, elem)
		if _, ok := knownSchemas[resolvedElem]; ok {
			return []string{
				"type: array",
				"items:",
				fmt.Sprintf("  $ref: '#/components/schemas/%s'", schemaNameFn(resolvedElem)),
			}
		}
		return []string{
			"type: array",
			"items:",
			fmt.Sprintf("  type: %s", mapGoTypeToOpenAPI(elem)),
		}
	case strings.HasPrefix(base, "map["):
		// Keep it generic; we don't model key/value types precisely here.
		return []string{"type: object", "additionalProperties: true"}
	default:
		return []string{fmt.Sprintf("type: %s", mapGoTypeToOpenAPI(base))}
	}
}
