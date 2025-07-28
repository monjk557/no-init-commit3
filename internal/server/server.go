package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	custommiddleware "github.com/collabops/git-executor/internal/middleware"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

// Server represents the HTTP server
type Server struct {
	server *http.Server // inheritance가 아니라 implmentation한것이다.
	config *config.Config
	logger *logging.Logger
}

// New creates a new HTTP server instance
func New(cfg *config.Config) *Server {
	return &Server{
		config: cfg,
		logger: logging.GetGlobal(),
	}
}

// Start initializes and starts the HTTP server
func (s *Server) Start() error {
	// Create router
	router := s.SetupRouter()

	// Create HTTP server
	// [핵심] 모든 Go 웹 프레임워크는, HTTP server를 상속받는다. 이것을 확장해서 쓴다.
	// 자기만의 "Structure"을 만들어서 쓴다.
	s.server = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", s.config.Server.Host, s.config.Server.Port),
		Handler:      router, //handler??? router = handler 개념은 같음. 보통하는 일이 L7 요청들어오면 얘가 어디로해주는역할.
		ReadTimeout:  s.config.Server.ReadTimeout,
		WriteTimeout: s.config.Server.WriteTimeout,
		IdleTimeout:  s.config.Server.IdleTimeout,
	}

	// HTTP Server를 쓰는 방법은 tcp를 열고, listen을 함.
	// Check if port is available
	// This part of the code was removed as per the new_code, as net.Listen is no longer imported.
	// The original code had a bug where it was trying to close a listener that was not created.
	// Since the new_code removes net.Listen, this block is effectively removed.

	// Start server in a goroutine
	go func() { // multithread개념임. "go func"는. go 함수앞에 go 라고 붙이면, thread를 생성함.
		s.logger.Info("Server starting",
			zap.String("address", s.server.Addr),
			zap.String("basePath", s.config.Server.BasePath))
		// 이거도 핵심.
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("Server failed to start", zap.Error(err))
		}
	}() // ()가 있어서됨 함수가 바로 실행됨 변수로 저장했다가 바로 실행해도되고 이렇게 바로실행해도됨.

	// Wait for shutdown signal
	// 바로 return 때리면 go routine돌리면 종료되니까, loop로 돌게 해놓은거임.
	return s.waitForShutdown()
}

// SetupRouter configures the HTTP router with middleware and routes
func (s *Server) SetupRouter() *chi.Mux {
	router := chi.NewRouter()

	// Add middleware
	// 순서가 중요함. 양파처럼 layer를 쌓는거임.
	// request id layer를 하는것.

	// error전파를 다 middleware할수가있음.
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(s.loggingMiddleware)
	router.Use(middleware.Recoverer)
	router.Use(middleware.Timeout(60 * time.Second))

	// Health check endpoint
	// 여기에 Health Check router를 넣는거임.
	router.Get(s.config.Server.HealthCheckPath, s.healthCheck)

	// Mount API routes under base path if configured
	if s.config.Server.BasePath != "/" {
		// prefix를 상위에서, 하위에서 설정하는거를 내 스타일대로 하는거임.
		router.Mount(s.config.Server.BasePath, s.setupAPIRoutes())
	} else {
		router.Mount("/", s.setupAPIRoutes())
	}

	return router
}

// 전체적구조 RECAP
// Router생성
// ROuter마다 실행되는 go 도메인들 설정 (middleware에 넣는다는게 이말!)
// 이거에 장점: Router Get/Post/Delete/Update vs Router Use.
// Use 가 미들웨어임. 어떤 영역을 공통적으로 처리해주는가? 이것이 핵심임. 잘 처리해야지, Domain할일 이 줄어들음. Domain은 거의다 비슷한 일을 할거임.
// Domain의 할일 을 줄여야함. Domain이 되게 단순해짐. 이게 핵심.
// CRUD는 도메인임.
// Use에다가 logic을 넣을수도있음.
// setupAPIRoutes configures the API-specific routes.
func (s *Server) setupAPIRoutes() *chi.Mux {
	apiRouter := chi.NewRouter()

	// Basic API info endpoint
	apiRouter.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"message": "CollabOps Git Executor API", "version": "1.0.0", "status": "ready"}`)
	})

	// Git Smart HTTP Protocol routes
	s.setupGitRoutes(apiRouter)

	// REST API routes for Git operations
	s.setupRestAPIRoutes(apiRouter)

	// Enable streaming optimization for large repository operations
	s.enableStreamingOptimization(apiRouter)
	return apiRouter
}

// setupRestAPIRoutes configures REST API routes for Git operations
func (s *Server) setupRestAPIRoutes(router *chi.Mux) {
	// Initialize permission client for REST API routes
	permConfig := permissions.DefaultConfig()
	permConfig.APIServiceURL = "http://localhost:8082" // Use mock permission service for testing
	permClient, err := permissions.NewPermissionClient(permConfig)
	if err != nil {
		// Log error and skip REST API routes
		fmt.Printf("⚠️  Warning: Failed to initialize permission client for REST API: %v\n", err)
		return
	}

	// Create diff handler
	diffHandler, err := protocol.NewDiffHandler(s.config, permClient)
	if err != nil {
		// Log error and skip diff routes
		fmt.Printf("Failed to create diff handler: %v\n", err)
		return
	}

	// Create tree handler
	treeHandler, err := protocol.NewTreeHandler(s.config, permClient)
	if err != nil {
		// Log error and skip tree routes
		fmt.Printf("Failed to create tree handler: %v\n", err)
		return
	}

	// Create blob handler
	blobHandler, err := protocol.NewBlobHandler(s.config, permClient)
	if err != nil {
		// Log error and skip blob routes
		fmt.Printf("Failed to create blob handler: %v\n", err)
		return
	}

	// Initialize access control middleware for REST API endpoints
	accessControl, err := custommiddleware.NewAccessControlMiddleware(
		custommiddleware.DefaultAccessControlConfig(),
		s.logger,
	)
	if err != nil {
		s.logger.Warn("Failed to initialize access control middleware", zap.Error(err))
		// Continue without access control middleware
		accessControl = nil
	}

	// Create authentication middleware for API endpoints
	authMiddleware := s.createAPIAuthMiddleware()

	// CORS middleware for API endpoints
	corsMiddleware := s.createCORSMiddleware()

	// Git diff endpoints with enhanced middleware
	router.Route("/repos", func(r chi.Router) {
		// Apply middleware stack for repo-level operations
		if corsMiddleware != nil {
			r.Use(corsMiddleware)
		}
		if accessControl != nil {
			r.Use(accessControl.Middleware)
		}
		if authMiddleware != nil {
			r.Use(authMiddleware)
		}

		// Apply rate limiting middleware
		r.Use(middleware.Throttle(100)) // 100 concurrent requests max

		r.Route("/{owner}", func(r chi.Router) {
			r.Route("/{repo}", func(r chi.Router) {
				// Tree endpoint
				r.Get("/tree", treeHandler.HandleTreeAPI)
				// Blob endpoint
				r.Get("/blob", blobHandler.HandleBlobAPI)

				// Diff endpoints
				r.Route("/diff", func(r chi.Router) {
					// Compare two commits
					r.Get("/commits/{base}/{head}", diffHandler.HandleCommitDiff)
					// Compare two branches
					r.Get("/branches/{base}/{head}", diffHandler.HandleBranchDiff)
					// Compare specific file between two references
					r.Get("/file", diffHandler.HandleFileDiff)
				})
				// Patch endpoints
				r.Route("/patch", func(r chi.Router) {
					// Generate Git patch format for commit range
					r.Get("/{base}/{head}", diffHandler.HandlePatch)
				})
			})
		})
	})
}

// setupGitRoutes configures Git Smart HTTP protocol routes
func (s *Server) setupGitRoutes(router *chi.Mux) {
	// Initialize permission client (for now, we'll create a basic config)
	// TODO: This should be properly configured with actual API service URL
	permConfig := permissions.DefaultConfig()
	permConfig.APIServiceURL = "http://localhost:8082" // Use mock permission service for testing
	permClient, err := permissions.NewPermissionClient(permConfig)
	if err != nil {
		// For now, log the error and continue without Smart HTTP
		fmt.Printf("⚠️  Warning: Failed to initialize permission client: %v\n", err)
		return
	}

	// Initialize upload-pack handler (fetch operations)
	uploadPackHandler, err := protocol.NewUploadPackHandler(s.config, permClient)
	if err != nil {
		// For now, log the error and continue without Smart HTTP
		fmt.Printf("⚠️  Warning: Failed to initialize upload-pack handler: %v\n", err)
		return
	}

	// Initialize receive-pack handler (push operations)
	receivePackHandler, err := protocol.NewReceivePackHandler(s.config, permClient)
	if err != nil {
		// For now, log the error and continue without receive-pack
		fmt.Printf("⚠️  Warning: Failed to initialize receive-pack handler: %v\n", err)
		// Continue with upload-pack only
	}

	// Initialize SSH protocol handler (SSH-forwarded operations)
	sshHandler, err := protocol.NewSSHProtocolHandler(s.config, permClient)
	if err != nil {
		// For now, log the error and continue without SSH support
		fmt.Printf("⚠️  Warning: Failed to initialize SSH protocol handler: %v\n", err)
		// Continue without SSH support
	}

	// Git Smart HTTP routes for both fetch and push operations (executor endpoints)
	router.Route("/{owner}/{repo}", func(r chi.Router) {
		// Fetch operations (git clone, git fetch)
		// GET /repos/{owner}/{repo}/info/refs?service=git-upload-pack
		r.Get("/info/refs", func(w http.ResponseWriter, req *http.Request) {
			service := req.URL.Query().Get("service")
			switch service {
			case "git-upload-pack":
				uploadPackHandler.HandleInfoRefs(w, req)
			case "git-receive-pack":
				if receivePackHandler != nil {
					receivePackHandler.HandleInfoRefs(w, req)
				} else {
					http.Error(w, "Receive-pack service not available", http.StatusServiceUnavailable)
				}
			default:
				http.Error(w, "Invalid service parameter", http.StatusBadRequest)
			}
		})

		// POST /repos/{owner}/{repo}/git-upload-pack
		r.Post("/git-upload-pack", uploadPackHandler.HandleUploadPack)

		// Push operations (git push)
		// POST /repos/{owner}/{repo}/git-receive-pack
		if receivePackHandler != nil {
			r.Post("/git-receive-pack", receivePackHandler.HandleReceivePack)
		}
	})

	// SSH protocol routes for SSH-forwarded Git operations
	if sshHandler != nil {
		router.Route("/ssh/{owner}/{repo}", func(r chi.Router) {
			// SSH-forwarded fetch operations
			// POST /ssh/{owner}/{repo}/upload-pack
			r.Post("/upload-pack", sshHandler.HandleSSHUploadPack)

			// SSH-forwarded push operations
			// POST /ssh/{owner}/{repo}/receive-pack
			r.Post("/receive-pack", sshHandler.HandleSSHReceivePack)
		})
	}
}

// healthCheck handles the health check endpoint
func (s *Server) healthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{"status": "healthy", "timestamp": "%s"}`, time.Now().UTC().Format(time.RFC3339))
}

// waitForShutdown waits for shutdown signals and performs graceful shutdown
func (s *Server) waitForShutdown() error {
	// Create channel to listen for interrupt signals
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Block until a signal is received
	sig := <-quit
	s.logger.Info("Received shutdown signal", zap.String("signal", sig.String()))

	// Create shutdown context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), s.config.Server.ShutdownTimeout)
	defer cancel()

	// Attempt graceful shutdown
	s.logger.Info("Shutting down server gracefully")
	if err := s.server.Shutdown(ctx); err != nil {
		s.logger.Error("Server forced to shutdown", zap.Error(err))
		return err
	}

	s.logger.Info("Server shutdown complete")
	return nil
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

// GetAddr returns the server address
func (s *Server) GetAddr() string {
	if s.server == nil {
		return ""
	}
	return s.server.Addr
}

// loggingMiddleware logs HTTP requests
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		s.logger.Info("HTTP request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", ww.Status()),
			zap.Int("bytes", ww.BytesWritten()),
			zap.Duration("duration", time.Since(start)),
			zap.String("remote", r.RemoteAddr),
		)
	})
}

// createAPIAuthMiddleware creates middleware for API authentication
func (s *Server) createAPIAuthMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// For REST API endpoints, we rely on the individual handlers
			// to perform authentication (they check X-USER-ID header)
			// This middleware can be enhanced later for JWT tokens, API keys, etc.

			// Add API-specific headers
			w.Header().Set("X-API-Version", "1.0")
			w.Header().Set("X-Service", "collabops-git-executor")

			next.ServeHTTP(w, r)
		})
	}
}

// createCORSMiddleware creates CORS middleware for API endpoints
func (s *Server) createCORSMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Basic CORS headers for API access
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-USER-ID, X-Source")
			w.Header().Set("Access-Control-Max-Age", "86400")

			// Handle preflight requests
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
