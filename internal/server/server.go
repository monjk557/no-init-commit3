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
	"github.com/collabops/git-executor/internal/blob"
	"github.com/collabops/git-executor/internal/branches"
	"github.com/collabops/git-executor/internal/commits"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/diffs"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/merge"
	"github.com/collabops/git-executor/internal/observability"
	"github.com/collabops/git-executor/internal/overview"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/collabops/git-executor/internal/state"
	"github.com/collabops/git-executor/internal/tags"
	"github.com/collabops/git-executor/internal/tree"
	"github.com/gin-gonic/gin"
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
	obsManager, err := observability.NewManager(cfg, "1.0.0")
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
	// Only register if not already registered (avoid conflicts in tests)
	if s.config.Server.HealthCheckPath != "" {
		router.GET(s.config.Server.HealthCheckPath, s.healthCheck)
	}

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
	// Check if route is already registered to avoid conflicts in tests
	// Note: Gin doesn't provide a way to check if a route exists, so we'll
	// rely on the fact that SetupRouter creates a new router each time
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

	// Initialize JSON response middleware for proper error handling
	jsonResponseMiddleware := api.NewJSONResponseMiddleware(s.logger, "v1")

	apiGroup := router.Group("/repos")
	apiGroup.Use(ginContextMiddleware())
	apiGroup.Use(jsonResponseMiddleware.RequestMetadataMiddleware())
	apiGroup.Use(jsonResponseMiddleware.ErrorHandler())

	repositoryHandler, err := repositories.NewRepositoryCreationHandler(s.config)
	if err != nil {
		s.logger.Error("Failed to create repository creation handler", zap.Error(err))
		return
	}

	branchesHandler, err := branches.NewBranchOperationsHandler(s.config)
	if err != nil {
		s.logger.Error("Failed to create branch operations handler", zap.Error(err))
		return
	}

	tagsHandler, err := tags.NewTagsHandler(s.config)
	if err != nil {
		s.logger.Error("Failed to create tags handler", zap.Error(err))
		return
	}

	treeHandler, err := tree.NewTreeHandler(s.config)
	if err != nil {
		s.logger.Error("Failed to create tree handler", zap.Error(err))
		return
	}

	commitsHandler, err := commits.NewCommitsHandler(s.config)
	if err != nil {
		s.logger.Error("Failed to create commits handler", zap.Error(err))
		return
	}

	blobHandler, err := blob.NewBlobHandler(s.config)
	if err != nil {
		s.logger.Error("Failed to create blob handler", zap.Error(err))
		return
	}

	diffsHandler, err := diffs.NewDiffHandler(s.config)
	if err != nil {
		s.logger.Error("Failed to create diff handler", zap.Error(err))
		return
	}

	mergePreviewHandler, err := merge.NewMergePreviewHandler(s.config)
	if err != nil {
		s.logger.Error("Failed to create merge preview handler", zap.Error(err))
		return
	}

	mergeOpsHandler, err := merge.NewMergeOperationsHandler(s.config)
	if err != nil {
		s.logger.Error("Failed to create merge operations handler", zap.Error(err))
		return
	}

	stateHandler, err := state.NewStateHandler(s.config)
	if err != nil {
		s.logger.Error("Failed to create state handler", zap.Error(err))
		return
	}

	overviewHandler, err := overview.NewRepositoryOverviewHandler(s.config)
	if err != nil {
		s.logger.Error("Failed to create overview handler", zap.Error(err))
		return
	}

	ownerRepoGroup := apiGroup.Group("/:owner/:repo")
	ownerRepoGroup.Use(ginContextMiddleware())
	ownerRepoGroup.Use(jsonResponseMiddleware.RequestMetadataMiddleware())
	ownerRepoGroup.Use(jsonResponseMiddleware.ErrorHandler())

	// Repository endpoints
	ownerRepoGroup.POST("", repositoryHandler.HandleCreateRepository)
	ownerRepoGroup.GET("/state", stateHandler.HandleGetRepositoryState)

	// Branches endpoints
	ownerRepoGroup.GET("/branches", branchesHandler.HandleGetBranches)
	ownerRepoGroup.POST("/branches", branchesHandler.HandleCreateBranch)
	ownerRepoGroup.PATCH("/branches/*branch", branchesHandler.HandleUpdateBranch)
	ownerRepoGroup.DELETE("/branches/*branch", branchesHandler.HandleDeleteBranch)

	// Tree endpoints
	ownerRepoGroup.GET("/tree", treeHandler.HandleTreeAPI)

	// Commits endpoints
	ownerRepoGroup.GET("/commits", commitsHandler.HandleCommitsAPI)
	ownerRepoGroup.POST("/commits", commitsHandler.HandleMultiFileCommit)

	// Tags endpoints
	ownerRepoGroup.GET("/tags", tagsHandler.HandleGetTags)
	ownerRepoGroup.POST("/tags", tagsHandler.HandleCreateTag)
	ownerRepoGroup.DELETE("/tags/*tag", tagsHandler.HandleDeleteTag)

	// Blob endpoints
	ownerRepoGroup.GET("/blob", blobHandler.HandleBlobAPI)

	// Diff endpoints
	ownerRepoGroup.GET("/diff/commits/:base/:head", diffsHandler.HandleCommitDiff)
	ownerRepoGroup.GET("/diff/branches/:base/:head", diffsHandler.HandleBranchDiff)
	ownerRepoGroup.GET("/diff/file", diffsHandler.HandleFileDiff)
	ownerRepoGroup.GET("/diff/commit/:sha", diffsHandler.HandleSingleCommitDiff)
	ownerRepoGroup.GET("/diff/patch/:base/:head", diffsHandler.HandlePatch)

	// Merge endpoints
	ownerRepoGroup.GET("/merges/preview", mergePreviewHandler.HandleMergePreview)
	ownerRepoGroup.POST("/merges", mergeOpsHandler.HandleMerge)

	// Overview endpoints
	ownerRepoGroup.GET("/overview", overviewHandler.HandleGetRepositoryOverview)
	ownerRepoGroup.GET("/overview/tree", overviewHandler.HandleGetTreeOverview)
	ownerRepoGroup.GET("/overview/tree/commits", overviewHandler.HandleGetTreeWithCommitOverview)
	ownerRepoGroup.GET("/overview/blob", overviewHandler.HandleGetBlobOverview)
	ownerRepoGroup.GET("/overview/branches", overviewHandler.HandleGetBranchesOverview)

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

	// Git Smart HTTP routes for both fetch and push operations (executor endpoints)
	ownerRepoGroup := router.Group("/:owner/:repo")
	ownerRepoGroup.Use(ginContextMiddleware()) // Add chi compatibility middleware
	{
		// Fetch operations (git clone, git fetch)
		// GET /repos/{owner}/{repo}/info/refs?service=git-upload-pack
		ownerRepoGroup.GET("/info/refs", func(c *gin.Context) {
			fmt.Printf("[HTTP server] info/refs 요청: owner=%s, repo=%s, service=%s\n", c.Param("owner"), c.Param("repo"), c.Query("service"))
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
		ownerRepoGroup.POST("/git-upload-pack", func(c *gin.Context) {
			fmt.Printf("[HTTP server] git-upload-pack POST 요청: owner=%s, repo=%s, path=%s\n", c.Param("owner"), c.Param("repo"), c.Request.URL.Path)
			uploadPackHandler.HandleUploadPack(c.Writer, c.Request)
		})

		// Push operations (git push)
		// POST /repos/{owner}/{repo}/git-receive-pack
		if receivePackHandler != nil {
			ownerRepoGroup.POST("/git-receive-pack", gin.WrapF(receivePackHandler.HandleReceivePack))
		}
	}

}
