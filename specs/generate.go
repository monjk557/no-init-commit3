package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
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
		fmt.Fprintln(log, "📚 Populating registry from domain RegisterRoutes...")
		setupSpecRoutes(registry)
		fmt.Fprintf(log, "   Registry endpoints: %d\n", len(registry.Snapshot()))
	}

	fmt.Fprintln(log, "🔍 Parsing server.go for routes...")
	routes, err := parseServerRoutes(filepath.Join(projectRoot, "internal/server/server.go"))
	if err != nil {
		return "", fmt.Errorf("Error parsing server.go: %w", err)
	}
	fmt.Fprintf(log, "   Found %d routes\n", len(routes))

	fmt.Fprintln(log, "🔍 Parsing schema files from all packages...")
	schemas, resolver, err := parseAllSchemas(projectRoot)
	if err != nil {
		return "", fmt.Errorf("Error parsing schemas: %w", err)
	}
	fmt.Fprintf(log, "   Found %d schema types\n", len(schemas))

	stringEnums, err := parseAllStringEnums(projectRoot)
	if err != nil {
		return "", fmt.Errorf("Error parsing typed-string enums: %w", err)
	}
	fmt.Fprintf(log, "   Found %d typed-string enum aliases\n", len(stringEnums))

	fmt.Fprintln(log, "📝 Generating OpenAPI spec...")
	spec, err := generateOpenAPISpec(routes, schemas, stringEnums, resolver, projectRoot, registry)
	if err != nil {
		return "", fmt.Errorf("Error generating OpenAPI spec: %w", err)
	}
	return spec, nil
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

func parseAllSchemas(projectRoot string) (map[string]SchemaInfo, typeResolver, error) {
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

	return schemas, resolver, nil
}

func ensurePkg(r typeResolver, pkg string) map[string]string {
	if r[pkg] == nil {
		r[pkg] = map[string]string{}
	}
	return r[pkg]
}

// schemasEqual 은 두 SchemaInfo 의 field set 이 동일한지 비교한다 (필드 순서
// 무관, 이름·타입·json tag·required 비교).
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
		buf.WriteString(fmt.Sprintf("  %s:\n", specPath))
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
	buf.WriteString(fmt.Sprintf("    %s:\n", method))
	buf.WriteString(fmt.Sprintf("      summary: %s\n", op.summary))
	buf.WriteString(fmt.Sprintf("      operationId: %s\n", op.operationID))
	buf.WriteString("      tags:\n")
	buf.WriteString(fmt.Sprintf("        - %s\n", op.tag))
	buf.WriteString("      parameters:\n")
	buf.WriteString("        - $ref: '#/components/parameters/Owner'\n")
	buf.WriteString("        - $ref: '#/components/parameters/Repo'\n")
	for _, p := range op.pathParams {
		buf.WriteString(fmt.Sprintf("        - name: %s\n", p))
		buf.WriteString("          in: path\n")
		buf.WriteString("          required: true\n")
		buf.WriteString("          schema:\n")
		buf.WriteString("            type: string\n")
	}
	for _, q := range op.queryParams {
		buf.WriteString(fmt.Sprintf("        - name: %s\n", q.name))
		buf.WriteString("          in: query\n")
		if q.required {
			buf.WriteString("          required: true\n")
		}
		buf.WriteString("          schema:\n")
		buf.WriteString(fmt.Sprintf("            type: %s\n", q.openAPI.typ))
		if q.openAPI.format != "" {
			buf.WriteString(fmt.Sprintf("            format: %s\n", q.openAPI.format))
		}
	}
	if op.requestRef != "" {
		buf.WriteString("      requestBody:\n")
		buf.WriteString("        required: true\n")
		buf.WriteString("        content:\n")
		buf.WriteString("          application/json:\n")
		buf.WriteString("            schema:\n")
		buf.WriteString(fmt.Sprintf("              $ref: '#/components/schemas/%s'\n", openAPISchemaName(op.requestRef)))
	}
	buf.WriteString("      responses:\n")
	buf.WriteString("        '200':\n")
	buf.WriteString("          description: Success\n")
	switch {
	case op.responseRef != "":
		buf.WriteString("          content:\n")
		buf.WriteString("            application/json:\n")
		buf.WriteString("              schema:\n")
		buf.WriteString(fmt.Sprintf("                $ref: '#/components/schemas/%s'\n", openAPISchemaName(op.responseRef)))
	case op.rawResponseContentType != "":
		// RegisterRaw endpoint — schema 없는 binary/text content. orchestrator
		// auto-gen 클라이언트는 이 content-type 만 보고 raw bytes/text reader
		// 를 emit 한다.
		buf.WriteString("          content:\n")
		buf.WriteString(fmt.Sprintf("            %s:\n", op.rawResponseContentType))
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
	for t.Kind() == reflect.Ptr {
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

	// Sort schema names for consistent output
	names := make([]string, 0, len(schemas))
	for name := range schemas {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		schema := schemas[name]
		buf.WriteString(renderSchemaYAML(schema, schemas, stringEnums, openAPISchemaName, resolver))
	}

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
	buf.WriteString(fmt.Sprintf("    %s:\n", schemaNameFn(schema.Name)))
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
		buf.WriteString(fmt.Sprintf("        %s:\n", fieldName))
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
			buf.WriteString(fmt.Sprintf("        - %s\n", r))
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
