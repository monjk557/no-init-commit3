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
	"github.com/collabops/git-executor/internal/observability"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

// Server represents the HTTP server with integrated observability
type Server struct {
	server               *http.Server
	config               *config.Config
	logger               *logging.Logger
	observabilityManager *observability.Manager
}

// New creates a new HTTP server instance with observability
func New(cfg *config.Config) (*Server, error) {
	// Note: Simple cache initializes itself automatically on first use

	// Create permission client
	permConfig := permissions.DefaultConfig()
	permConfig.APIServiceURL = "http://localhost:8082" // Use mock permission service for testing
	permClient, err := permissions.NewPermissionClient(permConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create permission client: %w", err)
	}

	// Initialize observability manager
	obsManager, err := observability.NewManager(cfg, permClient, "1.0.0")
	if err != nil {
		return nil, fmt.Errorf("failed to create observability manager: %w", err)
	}

	return &Server{
		config:               cfg,
		logger:               obsManager.GetLogger(),
		observabilityManager: obsManager,
	}, nil
}

// Start initializes and starts the HTTP server
func (s *Server) Start() error {
	// Create router
	router := s.SetupRouter()

	// Create HTTP server
	s.server = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", s.config.Server.Host, s.config.Server.Port),
		Handler:      router,
		ReadTimeout:  s.config.Server.ReadTimeout,
		WriteTimeout: s.config.Server.WriteTimeout,
		IdleTimeout:  s.config.Server.IdleTimeout,
	}

	// Start server in a goroutine
	go func() {
		s.logger.Info("Server starting",
			zap.String("address", s.server.Addr),
			zap.String("basePath", s.config.Server.BasePath))

		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("Server failed to start", zap.Error(err))
		}
	}()

	// Wait for shutdown signal
	return s.waitForShutdown()
}

// SetupRouter configures the HTTP router with observability middleware and routes
func (s *Server) SetupRouter() *chi.Mux {
	router := chi.NewRouter()

	// Add core middleware first
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)

	// Add observability middleware (metrics and tracing)
	s.observabilityManager.SetupMiddleware(router)

	// Add existing middleware
	router.Use(s.loggingMiddleware)
	router.Use(middleware.Recoverer)
	router.Use(middleware.Timeout(60 * time.Second))

	// Setup observability routes (health checks, metrics)
	s.observabilityManager.SetupRoutes(router)

	// Keep the original simple health check for backward compatibility
	router.Get(s.config.Server.HealthCheckPath, s.healthCheck)

	// Mount API routes under base path if configured
	if s.config.Server.BasePath != "/" {
		router.Mount(s.config.Server.BasePath, s.setupAPIRoutes())
	} else {
		router.Mount("/", s.setupAPIRoutes())
	}

	return router
}

// setupAPIRoutes configures the API-specific routes
func (s *Server) setupAPIRoutes() *chi.Mux {
	apiRouter := chi.NewRouter()

	// Basic API info endpoint
	apiRouter.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		// Record metrics for this request
		s.observabilityManager.RecordHTTPRequest("GET", "/", 200, time.Since(time.Now()), 0, 0)

		fmt.Fprintf(w, `{
			"service": "git-executor",
			"version": "1.0.0",
			"status": "running",
			"timestamp": "%s"
		}`, time.Now().UTC().Format(time.RFC3339))
	})

	// Set up REST API routes
	s.setupRestAPIRoutes(apiRouter)

	// Set up SSH and Smart HTTP protocol routes
	s.setupProtocolRoutes(apiRouter)

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

	// Create commits handler
	commitsHandler, err := protocol.NewCommitsHandler(s.config, permClient)
	if err != nil {
		// Log error and skip commits routes
		fmt.Printf("Failed to create commits handler: %v\n", err)
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
				// Commits endpoint
				r.Get("/commits", commitsHandler.HandleCommitsAPI)

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

// setupProtocolRoutes configures Git Smart HTTP protocol routes
func (s *Server) setupProtocolRoutes(router *chi.Mux) {
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

	// Shutdown HTTP server first
	if err := s.server.Shutdown(ctx); err != nil {
		s.logger.Error("Server forced to shutdown", zap.Error(err))
		return err
	}

	// Shutdown observability components
	if s.observabilityManager != nil {
		s.logger.Info("Shutting down observability components")
		if err := s.observabilityManager.Shutdown(ctx); err != nil {
			s.logger.Error("Failed to shutdown observability components", zap.Error(err))
			// Don't return error here as server shutdown is more critical
		}
	}

	s.logger.Info("Server shutdown complete")
	return nil
}

// Shutdown gracefully shuts down the server and observability components
func (s *Server) Shutdown(ctx context.Context) error {
	var lastErr error

	// Shutdown HTTP server
	if s.server != nil {
		if err := s.server.Shutdown(ctx); err != nil {
			lastErr = err
		}
	}

	// Shutdown observability components
	if s.observabilityManager != nil {
		if err := s.observabilityManager.Shutdown(ctx); err != nil {
			lastErr = err
		}
	}

	return lastErr
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
