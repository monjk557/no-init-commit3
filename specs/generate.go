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

	fmt.Println("🔍 Parsing server.go for routes...")
	routes, err := parseServerRoutes(filepath.Join(projectRoot, "internal/server/server.go"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing server.go: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   Found %d routes\n", len(routes))

	fmt.Println("🔍 Parsing schema files from all packages...")
	schemas, err := parseAllSchemas(projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing schemas: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   Found %d schema types\n", len(schemas))

	fmt.Println("📝 Generating OpenAPI spec...")
	spec, err := generateOpenAPISpec(routes, schemas, projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating OpenAPI spec: %v\n", err)
		os.Exit(1)
	}

	outputPath := filepath.Join(projectRoot, "specs/generated/collabops-git-executor.yaml")
	if err := os.WriteFile(outputPath, []byte(spec), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing spec file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ OpenAPI spec generated successfully: %s\n", outputPath)
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

func parseServerRoutes(filePath string) ([]RouteInfo, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	var routes []RouteInfo

	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				method := sel.Sel.Name
				if isHTTPMethod(method) {
					if len(x.Args) >= 2 {
						if pathLit, ok := x.Args[0].(*ast.BasicLit); ok {
							path := strings.Trim(pathLit.Value, "\"`")
							if selExpr, ok := x.Args[1].(*ast.SelectorExpr); ok {
								routes = append(routes, RouteInfo{
									Method:      method,
									Path:        path,
									HandlerName: selExpr.Sel.Name,
									Package:     selExpr.X.(*ast.Ident).Name,
								})
							} else if ident, ok := x.Args[1].(*ast.Ident); ok {
								routes = append(routes, RouteInfo{
									Method:      method,
									Path:        path,
									HandlerName: ident.Name,
								})
							}
						}
					}
				}
			}
		}
		return true
	})

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

func generateOpenAPISpec(routes []RouteInfo, schemas map[string]SchemaInfo, projectRoot string) (string, error) {
	// 항상 Go 기준으로 스펙 생성
	tmpl, err := template.New("openapi").Parse(openAPITemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	paths := generatePathsFromRoutes(routes)
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

func generatePathsFromRoutes(routes []RouteInfo) string {
	var buf strings.Builder

	// specPath 기준으로 그룹 (OpenAPI 경로: /repos/{owner}/{repo}/...)
	pathMap := make(map[string][]RouteInfo)
	for _, route := range routes {
		specPath, skip := routeToSpecPath(route.Path)
		if skip {
			continue
		}
		pathMap[specPath] = append(pathMap[specPath], route)
	}

	paths := make([]string, 0, len(pathMap))
	for p := range pathMap {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, specPath := range paths {
		routesForPath := pathMap[specPath]
		buf.WriteString(fmt.Sprintf("  %s:\n", specPath))
		for _, route := range routesForPath {
			method := strings.ToLower(route.Method)
			buf.WriteString(fmt.Sprintf("    %s:\n", method))
			buf.WriteString(fmt.Sprintf("      summary: %s\n", route.HandlerName))
			buf.WriteString(fmt.Sprintf("      operationId: %s\n", strings.ToLower(route.HandlerName)))
			buf.WriteString("      tags:\n")
			buf.WriteString(fmt.Sprintf("        - %s\n", getTagFromPath(specPath)))
			buf.WriteString("      parameters:\n")
			buf.WriteString("        - $ref: '#/components/parameters/Owner'\n")
			buf.WriteString("        - $ref: '#/components/parameters/Repo'\n")
			for _, p := range pathParamsFromSpecPath(specPath) {
				buf.WriteString(fmt.Sprintf("        - name: %s\n", p))
				buf.WriteString("          in: path\n")
				buf.WriteString("          required: true\n")
				buf.WriteString("          schema:\n")
				buf.WriteString("            type: string\n")
			}
			if ref := requestSchemaRef(route.Method, specPath); ref != "" {
				buf.WriteString("      requestBody:\n")
				buf.WriteString("        required: true\n")
				buf.WriteString("        content:\n")
				buf.WriteString("          application/json:\n")
				buf.WriteString("            schema:\n")
				buf.WriteString(fmt.Sprintf("              $ref: '#/components/schemas/%s'\n", openAPISchemaName(ref)))
			}
			buf.WriteString("      responses:\n")
			buf.WriteString("        '200':\n")
			buf.WriteString("          description: Success\n")
			if ref := responseSchemaRef(route.Method, specPath); ref != "" {
				buf.WriteString("          content:\n")
				buf.WriteString("            application/json:\n")
				buf.WriteString("              schema:\n")
				buf.WriteString(fmt.Sprintf("                $ref: '#/components/schemas/%s'\n", openAPISchemaName(ref)))
			}
			buf.WriteString("        '400':\n")
			buf.WriteString("          $ref: '#/components/responses/BadRequest'\n")
		}
	}

	return buf.String()
}

// requestSchemaRef returns the component schema name for requestBody (POST/PATCH with body). Empty if none.
func requestSchemaRef(method, specPath string) string {
	switch method {
	case "POST":
		switch specPath {
		case "/repos/{owner}/{repo}":
			return "CreateRepositoryRequest"
		case "/repos/{owner}/{repo}/branches":
			return "CreateBranchRequest"
		case "/repos/{owner}/{repo}/commits":
			return "MultiFileCommitRequest"
		case "/repos/{owner}/{repo}/tags":
			return "CreateTagRequest"
		case "/repos/{owner}/{repo}/merges":
			return "MergeRequest"
		case "/repos/{owner}/{repo}/revert":
			return "RevertRequest"
		case "/repos/{owner}/{repo}/context":
			return "ContextRequest"
		}
	case "PATCH":
		if strings.Contains(specPath, "/branches/{branch}") {
			return "UpdateBranchRequest"
		}
	}
	return ""
}

// responseSchemaRef returns the component schema name for 200 response body. Empty if none.
func responseSchemaRef(method, specPath string) string {
	switch specPath {
	case "/repos/{owner}/{repo}":
		if method == "POST" {
			return "CreateRepositoryResponse"
		}
	case "/repos/{owner}/{repo}/branches":
		if method == "GET" {
			return "GetBranchesResponse"
		}
		if method == "POST" {
			return "CreateBranchResponse"
		}
	case "/repos/{owner}/{repo}/branches/{branch}":
		if method == "PATCH" {
			return "UpdateBranchResponse"
		}
	case "/repos/{owner}/{repo}/commits":
		if method == "GET" {
			return "CommitsResponse"
		}
		if method == "POST" {
			return "MultiFileCommitResponse"
		}
	case "/repos/{owner}/{repo}/commits/compare/summary":
		if method == "GET" {
			return "ComparisonSummaryResponse"
		}
	case "/repos/{owner}/{repo}/commits/{sha}":
		if method == "GET" {
			return "CommitDetailResponse"
		}
	case "/repos/{owner}/{repo}/tags":
		if method == "GET" {
			return "TagsResponse"
		}
		if method == "POST" {
			return "CreateTagResponse"
		}
	case "/repos/{owner}/{repo}/merges":
		if method == "POST" {
			return "MergeResponse"
		}
	case "/repos/{owner}/{repo}/revert":
		if method == "POST" {
			return "RevertResponse"
		}
	case "/repos/{owner}/{repo}/merges/preview":
		return "MergePreviewResponse"
	case "/repos/{owner}/{repo}/merges/diff":
		return "DiffResponse"
	case "/repos/{owner}/{repo}/context":
		return "ContextResponse"
	case "/repos/{owner}/{repo}/overview":
		return "RepositoryOverviewResponse"
	case "/repos/{owner}/{repo}/overview/blob":
		return "BlobOverviewResponse"
	case "/repos/{owner}/{repo}/overview/branches":
		return "BranchesOverviewResponse"
	case "/repos/{owner}/{repo}/overview/latest-commits":
		return "LatestCommitsBatchResponse"
	case "/repos/{owner}/{repo}/overview/tree":
		return "TreeOverviewResponse"
	case "/repos/{owner}/{repo}/overview/tree/commits":
		return "TreeWithCommitOverviewResponse"
	case "/repos/{owner}/{repo}/state":
		return "RepositoryStateData"
	case "/repos/{owner}/{repo}/languages":
		return "LanguageStatsResponse"
	case "/repos/{owner}/{repo}/tree":
		return "TreeEntriesRecursiveResponse"
	case "/repos/{owner}/{repo}/diff/branches":
		return "DiffResponse"
	case "/repos/{owner}/{repo}/diff/branches/{base}/{head}":
		return "DiffResponse"
	case "/repos/{owner}/{repo}/diff/commit/{sha}":
		return "SingleCommitDiffResponse"
	case "/repos/{owner}/{repo}/diff/commits/{base}/{head}":
		return "DiffResponse"
	case "/repos/{owner}/{repo}/diff/expand-context":
		return "ExpandContextResponse"
	case "/repos/{owner}/{repo}/diff/file":
		return "FileDiff"
	case "/repos/{owner}/{repo}/diff/file/branches/{base}/{head}":
		return "DiffResponse"
	}
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
