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
	schemas, err := parseAllSchemas(projectRoot)
	if err != nil {
		return "", fmt.Errorf("Error parsing schemas: %w", err)
	}
	fmt.Fprintf(log, "   Found %d schema types\n", len(schemas))

	fmt.Fprintln(log, "📝 Generating OpenAPI spec...")
	spec, err := generateOpenAPISpec(routes, schemas, projectRoot, registry)
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

func parseAllSchemas(projectRoot string) (map[string]SchemaInfo, error) {
	schemas := make(map[string]SchemaInfo)

	schemaFiles := []string{
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

	for _, relPath := range schemaFiles {
		fullPath := filepath.Join(projectRoot, relPath)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			continue
		}

		pkg := filepath.Base(filepath.Dir(relPath))
		fileSchemas, err := parseSchemaFile(fullPath, pkg)
		if err != nil {
			return nil, fmt.Errorf("error parsing %s: %w", relPath, err)
		}

		for name, schema := range fileSchemas {
			schemas[name] = schema
		}
	}

	return schemas, nil
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

func generateOpenAPISpec(routes []RouteInfo, schemas map[string]SchemaInfo, projectRoot string, registry *typed.Registry) (string, error) {
	// 항상 Go 기준으로 스펙 생성
	tmpl, err := template.New("openapi").Parse(openAPITemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	paths := generatePathsFromRoutes(routes, registry)
	schemaDefs := generateSchemasFromStructs(schemas)

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
	pathParams  []string // owner/repo 외 추가 path param 이름들
	requestRef  string   // requestBody $ref schema 이름; 빈 문자열이면 body 없음
	responseRef string   // 200 응답 $ref schema 이름; 빈 문자열이면 body 없음
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
	for _, r := range ep.Responses {
		if r.Status == 200 || r.Status == 0 {
			switch {
			case r.SchemaRef != "":
				op.responseRef = r.SchemaRef
			case r.RespType != nil:
				op.responseRef = r.RespType.Name()
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
	if op.responseRef != "" {
		buf.WriteString("          content:\n")
		buf.WriteString("            application/json:\n")
		buf.WriteString("              schema:\n")
		buf.WriteString(fmt.Sprintf("                $ref: '#/components/schemas/%s'\n", openAPISchemaName(op.responseRef)))
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

func generateSchemasFromStructs(schemas map[string]SchemaInfo) string {
	var buf strings.Builder

	// Sort schema names for consistent output
	names := make([]string, 0, len(schemas))
	for name := range schemas {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		schema := schemas[name]
		buf.WriteString(renderSchemaYAML(schema, schemas, openAPISchemaName))
	}

	return buf.String()
}

func mapGoTypeToOpenAPI(goType string) string {
	switch {
	case goType == "string":
		return "string"
	case goType == "int" || goType == "int64" || goType == "int32":
		return "integer"
	case goType == "bool":
		return "boolean"
	case goType == "float64" || goType == "float32":
		return "number"
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

func renderSchemaYAML(schema SchemaInfo, knownSchemas map[string]SchemaInfo, schemaNameFn func(string) string) string {
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
		propLines := renderOpenAPIPropertyYAML(field.Type, knownSchemas, schemaNameFn)
		for _, l := range propLines {
			buf.WriteString("          " + l + "\n")
		}
		// Pointer-to-primitive 는 JSON null 을 반환할 수 있으므로 nullable 로 표시.
		// $ref 와 함께는 nullable 을 쓸 수 없어서 primitive/array 인 경우로 한정한다
		// (OpenAPI 3.0 에서 $ref + nullable 은 allOf 래핑이 필요함).
		if strings.HasPrefix(field.Type, "*") && !isRefProperty(propLines) {
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

func renderOpenAPIPropertyYAML(goType string, knownSchemas map[string]SchemaInfo, schemaNameFn func(string) string) []string {
	base := strings.TrimPrefix(goType, "*")

	// If this matches another known schema type name, use $ref
	if _, ok := knownSchemas[base]; ok {
		return []string{fmt.Sprintf("$ref: '#/components/schemas/%s'", schemaNameFn(base))}
	}

	switch {
	case base == "string":
		return []string{"type: string"}
	case base == "int" || base == "int64" || base == "int32":
		return []string{"type: integer"}
	case base == "bool":
		return []string{"type: boolean"}
	case base == "float64" || base == "float32":
		return []string{"type: number"}
	case base == "Time":
		// Best-effort mapping for time.Time (getTypeString strips package)
		return []string{"type: string", "format: date-time"}
	case base == "LineChangeType":
		return []string{"type: string", "enum: [ADD, DELETE, CONTEXT, HUNK]"}
	case base == "FileDiffStatus":
		return []string{"type: string", "enum: [added, deleted, modified, renamed]"}
	case base == "FileLoadStatus":
		return []string{"type: string", "enum: [loaded, collapsed, too_large, binary]"}
	case strings.HasPrefix(base, "[]"):
		elem := strings.TrimPrefix(strings.TrimPrefix(base, "[]"), "*")
		if _, ok := knownSchemas[elem]; ok {
			return []string{
				"type: array",
				"items:",
				fmt.Sprintf("  $ref: '#/components/schemas/%s'", schemaNameFn(elem)),
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
