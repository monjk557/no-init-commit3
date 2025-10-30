// Git 프로토콜을 처리하는 HTTP 서버 구현
package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/observability"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/collabops/git-executor/internal/repository"
	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.uber.org/zap"
)

// setupSwaggerRoutes - Swagger 문서 라우트 설정 (API 문서화용)
func (s *Server) setupSwaggerRoutes(router *gin.Engine) {
	if !s.config.Server.EnableDocs {
		return
	}

	docsPath := s.config.Server.DocsPath
	if docsPath == "" {
		docsPath = "/api-docs"
	}

	// Ensure docs path starts with /
	if !strings.HasPrefix(docsPath, "/") {
		docsPath = "/" + docsPath
	}

	// Create docs group with proper base path
	var docsGroup *gin.RouterGroup
	if s.config.Server.BasePath != "/" {
		docsGroup = router.Group(s.config.Server.BasePath + docsPath)
	} else {
		docsGroup = router.Group(docsPath)
	}

	// Redirect from docs root to Swagger UI
	docsGroup.GET("", func(c *gin.Context) {
		redirectPath := path.Join(docsPath, "swagger/index.html")
		if s.config.Server.BasePath != "/" {
			redirectPath = s.config.Server.BasePath + redirectPath
		}
		c.Redirect(http.StatusFound, redirectPath)
	})

	// Serve OpenAPI specification from configured path
	docsGroup.GET("/collabops-git-executor.yaml", func(c *gin.Context) {
		// Try to serve from embedded file first, fallback to filesystem
		specPath := "./specs/generated/collabops-git-executor.yaml"
		c.File(specPath)
	})

	// Serve Swagger UI
	docsGroup.GET("/swagger/*any", ginSwagger.WrapHandler(
		swaggerFiles.Handler,
		ginSwagger.URL(path.Join(docsPath, "collabops-git-executor.yaml")),
	))
}

// ginContextMiddleware - Gin 컨텍스트를 요청 컨텍스트에 저장하는 미들웨어
func ginContextMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Store Gin context directly in request context for protocol handlers to access
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), protocol.GinContextKey{}, c))
		c.Next()
	}
}

// Server - 통합 모니터링을 갖춘 HTTP 서버 구조체
type Server struct {
	server               *http.Server           // 실제 HTTP 서버 인스턴스
	config               *config.Config         // 애플리케이션 설정
	logger               *logging.Logger        // 로거
	observabilityManager *observability.Manager // 모니터링 매니저 (메트릭, 추적, 헬스체크)
}

// New - 모니터링 기능을 갖춘 새 HTTP 서버 인스턴스 생성
func New(cfg *config.Config) (*Server, error) {
	// 심플 캐시는 첫 사용 시 자동으로 초기화됨

	// Initialize observability manager (without permission client)
	obsManager, err := observability.NewManager(cfg, nil, "1.0.0")
	if err != nil {
		return nil, fmt.Errorf("failed to create observability manager: %w", err)
	}

	return &Server{
		config:               cfg,
		logger:               obsManager.GetLogger(),
		observabilityManager: obsManager,
	}, nil
}

// Start - HTTP 서버 초기화 및 시작
func (s *Server) Start() error {
	// 라우터 생성 (모든 엔드포인트 설정)
	router := s.SetupRouter()

	// HTTP 서버 인스턴스 생성
	s.server = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", s.config.Server.Host, s.config.Server.Port),
		Handler:      router,
		ReadTimeout:  s.config.Server.ReadTimeout,  // 요청 읽기 타임아웃
		WriteTimeout: s.config.Server.WriteTimeout, // 응답 쓰기 타임아웃
		IdleTimeout:  s.config.Server.IdleTimeout,  // 유휴 연결 타임아웃
	}

	// 고루틴에서 서버 시작 (비블로킹)
	go func() {
		s.logger.Info("Server starting",
			zap.String("address", s.server.Addr),
			zap.String("basePath", s.config.Server.BasePath))

		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("Server failed to start", zap.Error(err))
		}
	}()

	// 종료 시그널 대기 (SIGINT, SIGTERM)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	s.logger.Info("Received shutdown signal, shutting down gracefully")
	return nil
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	s.logger.Info("Shutting down HTTP server...")
	return s.server.Shutdown(ctx)
}

// GetAddr returns the server's listening address
func (s *Server) GetAddr() string {
	if s.server == nil {
		return ""
	}
	return s.server.Addr
}

// SetupRouter configures the HTTP router with observability middleware and routes
func (s *Server) SetupRouter() *gin.Engine {
	// Set Gin to release mode for production
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()

	// Add core middleware first
	router.Use(gin.Logger())   // Request logging
	router.Use(gin.Recovery()) // Panic recovery

	// Setup observability middleware and routes
	s.observabilityManager.SetupMiddleware(router)
	s.observabilityManager.SetupRoutes(router)

	// Keep the original simple health check for backward compatibility
	router.GET(s.config.Server.HealthCheckPath, s.healthCheck)

	// Setup Swagger documentation routes with proper configuration
	s.setupSwaggerRoutes(router)

	// Mount API routes under base path if configured
	if s.config.Server.BasePath != "/" {
		apiGroup := router.Group(s.config.Server.BasePath)
		s.setupAPIRoutesOnGroup(apiGroup)
	} else {
		s.setupAPIRoutesOnGroup(router.Group("/"))
	}

	return router
}

// setupAPIRoutesOnGroup configures API routes on a router group
func (s *Server) setupAPIRoutesOnGroup(group *gin.RouterGroup) {
	// Basic API info endpoint
	group.GET("/", func(c *gin.Context) {
		c.Header("Content-Type", "application/json")
		c.Status(http.StatusOK)

		// Record metrics for this request
		s.observabilityManager.RecordHTTPRequest("GET", "/", 200, time.Since(time.Now()), 0, 0)

		_, _ = fmt.Fprintf(c.Writer, `{
			"service": "git-executor",
			"version": "1.0.0",
			"status": "running",
			"timestamp": "%s"
		}`, time.Now().UTC().Format(time.RFC3339))
	})

	// Setup REST API routes
	s.setupRestAPIRoutes(group)

	// Setup Git protocol routes
	s.setupProtocolRoutes(group)
}

// setupRestAPIRoutes configures REST API routes for Git operations
func (s *Server) setupRestAPIRoutes(router *gin.RouterGroup) {
	// Create path configuration for handlers
	pathConfig, err := repository.NewPathConfig(s.config.Git.RepoRoot)
	if err != nil {
		s.logger.Error("Failed to create path config", zap.Error(err))
		return
	}

	// Initialize handlers for REST API endpoints (without permission client)
	treeHandler, err := protocol.NewTreeHandler(s.config, nil)
	if err != nil {
		s.logger.Error("Failed to create tree handler", zap.Error(err))
		return
	}

	blobHandler, err := protocol.NewBlobHandler(pathConfig, nil)
	if err != nil {
		s.logger.Error("Failed to create blob handler", zap.Error(err))
		return
	}

	commitsHandler, err := protocol.NewCommitsHandler(s.config, nil)
	if err != nil {
		s.logger.Error("Failed to create commits handler", zap.Error(err))
		return
	}

	// Initialize diff handler for enhanced Git operations
	diffHandler, err := protocol.NewDiffHandler(s.config, nil)
	if err != nil {
		s.logger.Error("Failed to create diff handler", zap.Error(err))
		return
	}

	// Initialize file operations handler for server-side Git REST APIs
	fileOpsHandler, err := protocol.NewFileOperationsHandler(s.config, nil)
	if err != nil {
		s.logger.Error("Failed to create file operations handler", zap.Error(err))
		return
	}

	// Initialize branch operations handler for branch management APIs
	branchOpsHandler, err := protocol.NewBranchOperationsHandler(s.config, nil)
	if err != nil {
		s.logger.Error("Failed to create branch operations handler", zap.Error(err))
		return
	}

	// Initialize repository state handler for repository management APIs
	repoStateHandler, err := protocol.NewRepositoryStateHandler(s.config, nil)
	if err != nil {
		s.logger.Error("Failed to create repository state handler", zap.Error(err))
		return
	}

	// Initialize repository creation handler for repository creation APIs
	repoCreationHandler, err := protocol.NewRepositoryCreationHandler(s.config)
	if err != nil {
		s.logger.Error("Failed to create repository creation handler", zap.Error(err))
		return
	}

	// Initialize merge handler for merge operations
	mergeOpsHandler, err := protocol.NewMergeOperationsHandler(s.config)
	if err != nil {
		s.logger.Error("Failed to create merge handler", zap.Error(err))
		return
	}

	// Initialize merge preview handler for merge preview operations
	mergePreviewHandler, err := protocol.NewMergePreviewHandler(s.config)
	if err != nil {
		s.logger.Error("Failed to create merge preview handler", zap.Error(err))
		return
	}

	// Initialize JSON response middleware for proper error handling
	jsonResponseMiddleware := api.NewJSONResponseMiddleware(s.logger, "v1")

	// Git diff endpoints with enhanced middleware
	reposGroup := router.Group("/repos")
	reposGroup.Use(ginContextMiddleware()) // Add chi compatibility middleware
	reposGroup.Use(jsonResponseMiddleware.RequestMetadataMiddleware())
	reposGroup.Use(jsonResponseMiddleware.ErrorHandler())

	// Repository detail handler for repository detail APIs
	repositoryDetailHandler, err := protocol.NewRepositoryDetailHandler(s.config)
	if err != nil {
		s.logger.Error("Failed to create repository detail handler", zap.Error(err))
		return
	}

	apiGroup := router.Group("/api/v1")
	apiGroup.GET("/repository/:owner/:repo", repositoryDetailHandler.HandleGetRepositoryDetail)
	apiGroup.GET("/repository/:owner/:repo/blob", repositoryDetailHandler.HandleGetBlobDetail)
	apiGroup.GET("/repository/:owner/:repo/tree", repositoryDetailHandler.HandleGetTreeDetail)
	apiGroup.GET("/repository/:owner/:repo/branches", branchOpsHandler.HandleGetBranchesDetail)

	{
		// Repository creation endpoint - separate from existing repos

		ownerGroup := reposGroup.Group("/:owner")
		{
			repoGroup := ownerGroup.Group("/:repo")
			{
				// Repository creation endpoint
				repoGroup.POST("", repoCreationHandler.HandleCreateRepository)
				// Tree endpoint
				repoGroup.GET("/tree", gin.WrapF(treeHandler.HandleTreeAPI))
				// Blob endpoint
				repoGroup.GET("/blob", blobHandler.HandleBlobAPIGin)

				commitGroup := repoGroup.Group("/commits")
				{
					// Commits endpoint
					commitGroup.GET("", gin.WrapF(commitsHandler.HandleCommitsAPI))
					// Multi-file commit endpoints
					commitGroup.POST("", fileOpsHandler.HandleMultiFileCommit)
				}

				// Diff endpoints
				diffGroup := repoGroup.Group("/diff")
				{
					// Compare two commits
					diffGroup.GET("/commits/:base/:head", gin.WrapF(diffHandler.HandleCommitDiff))
					// Compare two branches
					diffGroup.GET("/branches/:base/:head", gin.WrapF(diffHandler.HandleBranchDiff))
					// Compare specific file between two references
					diffGroup.GET("/file", gin.WrapF(diffHandler.HandleFileDiff))
				}

				// Single commit diff endpoint
				repoGroup.GET("/commits/:sha/diff", gin.WrapF(diffHandler.HandleSingleCommitDiff))

				// Merge endpoints
				mergeGroup := repoGroup.Group("/merges")
				{
					// Preview merge
					mergeGroup.GET("/preview", mergePreviewHandler.HandleMergePreview)
					// Perform merge
					mergeGroup.POST("", mergeOpsHandler.HandleMerge)
				}

				// Patch endpoints
				patchGroup := repoGroup.Group("/patch")
				{
					// Generate Git patch format for commit range
					patchGroup.GET("/:base/:head", gin.WrapF(diffHandler.HandlePatch))
				}

				// File operations endpoints (server-side Git REST APIs)
				filesGroup := repoGroup.Group("/files")
				{
					// GET endpoint for reading file content
					filesGroup.GET("/*path", fileOpsHandler.HandleFileContent)
					// PUT endpoint for creating/updating files
					filesGroup.PUT("/*path", fileOpsHandler.HandleFilePUT)
					// DELETE endpoint for deleting files
					filesGroup.DELETE("/*path", fileOpsHandler.HandleFileDELETE)
				}

				// Branch operations endpoints
				branchesGroup := repoGroup.Group("/branches")
				{
					// POST endpoint for creating branches
					branchesGroup.POST("", branchOpsHandler.HandleCreateBranch)
					// PATCH endpoint for updating branches
					branchesGroup.PATCH("/*branch", branchOpsHandler.HandleUpdateBranch)
					// DELETE endpoint for deleting branches
					branchesGroup.DELETE("/*branch", branchOpsHandler.HandleDeleteBranch)
				}

				// Repository state endpoints
				repoGroup.GET("/state", repoStateHandler.HandleGetRepositoryState)
				repoGroup.GET("/head", repoStateHandler.HandleGetRepositoryHead)
				repoGroup.GET("/references", repoStateHandler.HandleGetRepositoryReferences)
			}
		}
	}
}

// healthCheck handles health check requests
func (s *Server) healthCheck(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	_, _ = fmt.Fprintf(c.Writer, `{"status": "healthy", "timestamp": "%s"}`, time.Now().UTC().Format(time.RFC3339))
}

// setupProtocolRoutes configures Git Smart HTTP protocol routes
func (s *Server) setupProtocolRoutes(router *gin.RouterGroup) {
	// Initialize upload-pack handler (fetch operations)
	uploadPackHandler, err := protocol.NewUploadPackHandler(s.config)
	if err != nil {
		// For now, log the error and continue without Smart HTTP
		fmt.Printf("⚠️  Warning: Failed to initialize upload-pack handler: %v\n", err)
		return
	}

	// Initialize receive-pack handler (push operations)
	receivePackHandler, err := protocol.NewReceivePackHandler(s.config)
	if err != nil {
		// For now, log the error and continue without receive-pack
		fmt.Printf("⚠️  Warning: Failed to initialize receive-pack handler: %v\n", err)
		// Continue with upload-pack only
	}

	// Initialize SSH protocol handler (SSH-forwarded operations)
	sshHandler, err := protocol.NewSSHProtocolHandler(s.config, nil)
	if err != nil {
		// For now, log the error and continue without SSH support
		fmt.Printf("⚠️  Warning: Failed to initialize SSH protocol handler: %v\n", err)
		// Continue without SSH support
	}

	// Git Smart HTTP routes for both fetch and push operations (executor endpoints)
	ownerRepoGroup := router.Group("/:owner/:repo")
	ownerRepoGroup.Use(ginContextMiddleware()) // Add chi compatibility middleware
	{
		// Fetch operations (git clone, git fetch)
		// GET /repos/{owner}/{repo}/info/refs?service=git-upload-pack
		ownerRepoGroup.GET("/info/refs", func(c *gin.Context) {
			service := c.Query("service")
			switch service {
			case "git-upload-pack":
				uploadPackHandler.HandleInfoRefs(c.Writer, c.Request)
			case "git-receive-pack":
				if receivePackHandler != nil {
					receivePackHandler.HandleInfoRefs(c.Writer, c.Request)
				} else {
					c.Status(http.StatusServiceUnavailable)
					_, _ = c.Writer.Write([]byte("Receive-pack service not available"))
				}
			default:
				c.Status(http.StatusBadRequest)
				_, _ = c.Writer.Write([]byte("Invalid service parameter"))
			}
		})

		// POST /repos/{owner}/{repo}/git-upload-pack
		ownerRepoGroup.POST("/git-upload-pack", gin.WrapF(uploadPackHandler.HandleUploadPack))

		// Push operations (git push)
		// POST /repos/{owner}/{repo}/git-receive-pack
		if receivePackHandler != nil {
			ownerRepoGroup.POST("/git-receive-pack", gin.WrapF(receivePackHandler.HandleReceivePack))
		}
	}

	// SSH protocol routes for SSH-forwarded Git operations
	if sshHandler != nil {
		sshGroup := router.Group("/ssh/:owner/:repo")
		sshGroup.Use(ginContextMiddleware()) // Add chi compatibility middleware
		{
			// SSH-forwarded fetch operations
			// POST /ssh/{owner}/{repo}/git-upload-pack
			sshGroup.POST("/git-upload-pack", gin.WrapF(sshHandler.HandleSSHUploadPack))

			// SSH-forwarded push operations
			// POST /ssh/{owner}/{repo}/git-receive-pack
			sshGroup.POST("/git-receive-pack", gin.WrapF(sshHandler.HandleSSHReceivePack))
		}
	}
}
